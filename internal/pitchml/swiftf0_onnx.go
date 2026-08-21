//go:build onnx

package pitchml

import (
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/S95F/musicTutor/internal/pitch"
)

func Available() bool { return true }

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

var (
	runtimeMu   sync.Mutex
	runtimePath string
)

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

type ortRunner struct {
	label     string
	data      []byte
	inputName string
	outNames  []string
	inputRank int
	staticLen int

	session *ort.AdvancedSession
	in      *ort.Tensor[float32]
	outs    []ort.Value
	pitchT  *ort.Tensor[float32]
	confT   *ort.Tensor[float32]
}

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

func (r *ortRunner) describeOutputs(modelPath string, outputs []ort.InputOutputInfo) error {
	pitchIdx := indexMatching(outputs, "pitch", "f0", "freq")
	confIdx := indexMatching(outputs, "conf", "voic", "prob")
	if pitchIdx < 0 || confIdx < 0 || pitchIdx == confIdx {
		if len(outputs) != 2 {
			return fmt.Errorf("%w: cannot tell which of %s's outputs are pitch and confidence (%s)", ErrModelLayout, modelPath, describeAll(outputs))
		}

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

func describeAll(infos []ort.InputOutputInfo) string {
	parts := make([]string, len(infos))
	for i := range infos {
		parts[i] = infos[i].String()
	}
	return strings.Join(parts, "; ")
}

func (r *ortRunner) name() string { return r.label }

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

func destroyAll(vs []ort.Value) {
	for _, v := range vs {
		if v != nil {
			v.Destroy()
		}
	}
}

func (r *ortRunner) input() []float32 {
	if r.in == nil {
		return nil
	}
	return r.in.GetData()
}

func (r *ortRunner) run() ([]float32, []float32, error) {
	if r.session == nil {
		return nil, nil, fmt.Errorf("pitchml: run before resize")
	}
	if err := r.session.Run(); err != nil {
		return nil, nil, fmt.Errorf("pitchml: SwiftF0 inference failed: %w", err)
	}
	return r.pitchT.GetData(), r.confT.GetData(), nil
}

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

func (r *ortRunner) close() error {
	r.releaseSession()
	r.data = nil
	return nil
}
