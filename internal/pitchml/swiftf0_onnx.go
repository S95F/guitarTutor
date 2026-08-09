//go:build onnx

package pitchml

import (
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/S95F/guitarTutor/internal/pitch"
)

// This file is the only place in guitarTutor that touches
// github.com/yalue/onnxruntime_go, and it exists only under the onnx build
// tag. The binding dlopen's the ONNX Runtime at run time from a path we
// hand it, which is why D4 chose it: no import library, no headers, no
// system install, and a default build that has never heard of it.

// Available reports whether this build includes the ONNX backend.
func Available() bool { return true }

// New loads the SwiftF0 model and returns it as a pitch.F0Estimator.
// Resolution of both files is described on Options; every failure names
// the path it tried and the action that fixes it. The result also
// satisfies Estimator, so a caller owning its lifetime should Close it.
func New(opts Options) (pitch.F0Estimator, error) {
	res, err := osResolver().resolve(opts)
	if err != nil {
		return nil, err
	}
	if err := initRuntime(res.RuntimePath); err != nil {
		return nil, err
	}
	r, err := newORTRunner(res.ModelPath)
	if err != nil {
		return nil, err
	}
	est, err := newEstimator(r, res.SampleRate)
	if err != nil {
		r.close()
		return nil, err
	}
	return est, nil
}

// The ONNX environment is process-global in the C API: one shared library,
// one OrtEnv, initialized once. Guard it so two New calls (settings
// reopened, two detectors) cannot race the loader.
var (
	runtimeMu   sync.Mutex
	runtimePath string
)

// initRuntime loads the ONNX Runtime shared library from path, once per
// process.
func initRuntime(path string) error {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	if ort.IsInitialized() {
		if runtimePath != "" && runtimePath != path {
			return fmt.Errorf("pitchml: the ONNX runtime is already loaded from %s, so %s cannot also be used in this process; restart with a single runtime path", runtimePath, path)
		}
		return nil
	}
	ort.SetSharedLibraryPath(path)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("%w: %s exists but could not be loaded: %w. It is usually the wrong architecture (a 32-bit or arm64 build for an amd64 executable, or vice versa) or is missing its own dependencies; %s", ErrRuntimeNotFound, path, err, runtimeHelp(runtimeLibName(goruntime.GOOS)))
	}
	runtimePath = path
	return nil
}

// An ortRunner is the modelRunner backed by a real ONNX session.
//
// Steady state must not allocate, so the session is an AdvancedSession
// with fixed input and output tensors — its Run passes pointers it already
// holds and allocates nothing, unlike the dynamic session, which builds
// value slices per call. The catch is that fixed output tensors need the
// output shape up front, and SwiftF0's output length depends on the input
// length. resize therefore runs the model once through a throwaway dynamic
// session, keeps the output tensors that run allocated, and builds the
// fixed session around them. The .onnx bytes are held for the lifetime of
// the runner so that this can be redone (cheaply, from memory) if the
// analysis window length ever changes.
type ortRunner struct {
	label     string
	data      []byte
	inputName string
	outNames  []string // exactly two: pitch, then confidence
	inputRank int
	staticLen int // >0 when the model fixes its own input length

	session *ort.AdvancedSession
	in      *ort.Tensor[float32]
	outs    []ort.Value
	pitchT  *ort.Tensor[float32]
	confT   *ort.Tensor[float32]
}

// newORTRunner reads the model and works out its input and output layout
// from the graph's own metadata rather than hard-coding SwiftF0's tensor
// names, which have changed across its exports.
func newORTRunner(modelPath string) (*ortRunner, error) {
	data, err := os.ReadFile(modelPath)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %s", ErrModelNotFound, modelPath, cleanStatError(err))
	}
	inputs, outputs, err := ort.GetInputOutputInfoWithONNXData(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a loadable ONNX graph: %w", ErrModelLayout, modelPath, err)
	}

	r := &ortRunner{label: EstimatorName, data: data}
	if err := r.describeInput(modelPath, inputs); err != nil {
		return nil, err
	}
	if err := r.describeOutputs(modelPath, outputs); err != nil {
		return nil, err
	}
	return r, nil
}

// describeInput validates the single audio input and records its rank and
// (if fixed) its length.
func (r *ortRunner) describeInput(modelPath string, inputs []ort.InputOutputInfo) error {
	if len(inputs) != 1 {
		return fmt.Errorf("%w: %s has %d inputs, want 1 audio input (%s)", ErrModelLayout, modelPath, len(inputs), describeAll(inputs))
	}
	in := inputs[0]
	if in.OrtValueType != ort.ONNXTypeTensor || in.DataType != ort.TensorElementDataTypeFloat {
		return fmt.Errorf("%w: %s input %q is %s, want a float32 tensor", ErrModelLayout, modelPath, in.Name, in.String())
	}
	rank := len(in.Dimensions)
	if rank < 1 || rank > 2 {
		return fmt.Errorf("%w: %s input %q has shape %s, want [samples] or [1, samples]", ErrModelLayout, modelPath, in.Name, in.Dimensions)
	}
	if rank == 2 && in.Dimensions[0] > 1 {
		return fmt.Errorf("%w: %s input %q wants a batch of %d, and this backend feeds one window at a time", ErrModelLayout, modelPath, in.Name, in.Dimensions[0])
	}
	r.inputName = in.Name
	r.inputRank = rank
	if last := in.Dimensions[rank-1]; last > 0 {
		r.staticLen = int(last)
	}
	return nil
}

// describeOutputs picks the pitch and confidence outputs by name, falling
// back to positional order when the names are unfamiliar. Requesting only
// these two means extra outputs (timestamps, voicing masks) cost nothing.
func (r *ortRunner) describeOutputs(modelPath string, outputs []ort.InputOutputInfo) error {
	pitchIdx := indexMatching(outputs, "pitch", "f0", "freq")
	confIdx := indexMatching(outputs, "conf", "voic", "prob")
	if pitchIdx < 0 || confIdx < 0 || pitchIdx == confIdx {
		if len(outputs) != 2 {
			return fmt.Errorf("%w: cannot tell which of %s's outputs are pitch and confidence (%s)", ErrModelLayout, modelPath, describeAll(outputs))
		}
		// Two unnamed-ish outputs: SwiftF0 emits pitch first.
		pitchIdx, confIdx = 0, 1
	}
	for _, i := range []int{pitchIdx, confIdx} {
		o := outputs[i]
		if o.OrtValueType != ort.ONNXTypeTensor || o.DataType != ort.TensorElementDataTypeFloat {
			return fmt.Errorf("%w: %s output %q is %s, want a float32 tensor", ErrModelLayout, modelPath, o.Name, o.String())
		}
	}
	r.outNames = []string{outputs[pitchIdx].Name, outputs[confIdx].Name}
	return nil
}

// indexMatching returns the first output whose name contains any of the
// substrings, case-insensitively, or -1.
func indexMatching(infos []ort.InputOutputInfo, substrings ...string) int {
	for i, info := range infos {
		name := strings.ToLower(info.Name)
		for _, s := range substrings {
			if strings.Contains(name, s) {
				return i
			}
		}
	}
	return -1
}

// describeAll renders a model's inputs or outputs for an error message.
func describeAll(infos []ort.InputOutputInfo) string {
	parts := make([]string, len(infos))
	for i := range infos {
		parts[i] = infos[i].String()
	}
	return strings.Join(parts, "; ")
}

func (r *ortRunner) name() string { return r.label }

// sessionOptions pins ONNX Runtime to a single thread. EstimateF0 runs on
// the analysis goroutine once per hop (every ~10 ms); a thread pool spun
// up per inference would add scheduling jitter to a real-time path and
// oversubscribe a machine that is also rendering audio and a UI.
func sessionOptions() (*ort.SessionOptions, error) {
	o, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("pitchml: creating ONNX session options: %w", err)
	}
	if err := o.SetIntraOpNumThreads(1); err != nil {
		o.Destroy()
		return nil, fmt.Errorf("pitchml: setting ONNX intra-op threads: %w", err)
	}
	if err := o.SetInterOpNumThreads(1); err != nil {
		o.Destroy()
		return nil, fmt.Errorf("pitchml: setting ONNX inter-op threads: %w", err)
	}
	return o, nil
}

// resize implements modelRunner.
func (r *ortRunner) resize(samples int) (int, error) {
	n := samples
	if r.staticLen > 0 {
		n = r.staticLen
	}
	if n < minModelSamples {
		n = minModelSamples
	}
	if r.in != nil && len(r.in.GetData()) == n {
		return n, nil
	}
	r.releaseSession()

	shape := ort.NewShape(int64(n))
	if r.inputRank == 2 {
		shape = ort.NewShape(1, int64(n))
	}
	in, err := ort.NewEmptyTensor[float32](shape)
	if err != nil {
		return 0, fmt.Errorf("pitchml: allocating a %s input tensor: %w", shape, err)
	}

	// Probe: one run through a dynamic session tells us the output
	// shapes and hands back tensors already sized for them.
	outs, err := r.probeOutputs(in)
	if err != nil {
		in.Destroy()
		return 0, err
	}
	pitchT, ok := outs[0].(*ort.Tensor[float32])
	if !ok {
		destroyAll(outs)
		in.Destroy()
		return 0, fmt.Errorf("%w: output %q did not come back as a float32 tensor", ErrModelLayout, r.outNames[0])
	}
	confT, ok := outs[1].(*ort.Tensor[float32])
	if !ok {
		destroyAll(outs)
		in.Destroy()
		return 0, fmt.Errorf("%w: output %q did not come back as a float32 tensor", ErrModelLayout, r.outNames[1])
	}

	opts, err := sessionOptions()
	if err != nil {
		destroyAll(outs)
		in.Destroy()
		return 0, err
	}
	defer opts.Destroy()
	session, err := ort.NewAdvancedSessionWithONNXData(r.data,
		[]string{r.inputName}, r.outNames, []ort.Value{in}, outs, opts)
	if err != nil {
		destroyAll(outs)
		in.Destroy()
		return 0, fmt.Errorf("pitchml: creating the SwiftF0 session: %w", err)
	}

	r.session, r.in, r.outs, r.pitchT, r.confT = session, in, outs, pitchT, confT
	return n, nil
}

// probeOutputs runs the model once through a throwaway dynamic session so
// ONNX Runtime allocates output tensors of the right shape, which the
// fixed session then reuses forever.
func (r *ortRunner) probeOutputs(in *ort.Tensor[float32]) ([]ort.Value, error) {
	opts, err := sessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	probe, err := ort.NewDynamicAdvancedSessionWithONNXData(r.data,
		[]string{r.inputName}, r.outNames, opts)
	if err != nil {
		return nil, fmt.Errorf("pitchml: loading the SwiftF0 graph: %w", err)
	}
	defer probe.Destroy()

	outs := make([]ort.Value, len(r.outNames))
	if err := probe.Run([]ort.Value{in}, outs); err != nil {
		destroyAll(outs)
		return nil, fmt.Errorf("pitchml: the SwiftF0 model rejected a %d-sample input: %w", len(in.GetData()), err)
	}
	for i, v := range outs {
		if v == nil {
			destroyAll(outs)
			return nil, fmt.Errorf("%w: output %q was not produced", ErrModelLayout, r.outNames[i])
		}
	}
	return outs, nil
}

// destroyAll releases every non-nil value in vs.
func destroyAll(vs []ort.Value) {
	for _, v := range vs {
		if v != nil {
			v.Destroy()
		}
	}
}

// input implements modelRunner.
func (r *ortRunner) input() []float32 {
	if r.in == nil {
		return nil
	}
	return r.in.GetData()
}

// run implements modelRunner. No allocation: the session, its tensors and
// their backing slices were all built by resize.
func (r *ortRunner) run() ([]float32, []float32, error) {
	if r.session == nil {
		return nil, nil, fmt.Errorf("pitchml: run before resize")
	}
	if err := r.session.Run(); err != nil {
		return nil, nil, fmt.Errorf("pitchml: SwiftF0 inference failed: %w", err)
	}
	return r.pitchT.GetData(), r.confT.GetData(), nil
}

// releaseSession tears down the session and its tensors, in that order:
// the tensors must outlive the session that points at them.
func (r *ortRunner) releaseSession() {
	if r.session != nil {
		r.session.Destroy()
		r.session = nil
	}
	destroyAll(r.outs)
	r.outs, r.pitchT, r.confT = nil, nil, nil
	if r.in != nil {
		r.in.Destroy()
		r.in = nil
	}
}

// close implements modelRunner. The process-global ONNX environment is
// deliberately left initialized: another estimator may still be running,
// and tearing down the OrtEnv while one is would crash the process.
func (r *ortRunner) close() error {
	r.releaseSession()
	r.data = nil
	return nil
}
