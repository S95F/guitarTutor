package pitchml

import (
	"fmt"
	"math"
	"slices"

	"github.com/S95F/guitarTutor/internal/pitch"
)

// An Estimator is what New returns, as a concrete interface: a
// pitch.F0Estimator that additionally owns a model session (Close) and can
// report an inference failure that EstimateF0's signature has nowhere to
// put (Err). New's declared result type is pitch.F0Estimator, so reach
// these through a type assertion.
//
// Like the underlying pitch.F0Estimator, every method belongs to the
// analysis goroutine; Err is not a cross-goroutine health check.
type Estimator interface {
	pitch.F0Estimator
	// Err reports the last inference or resizing failure, if any. It is
	// sticky: it is not cleared by a subsequent good window, because a
	// caller that only checks occasionally still needs to see that the
	// backend broke.
	Err() error
	// Close releases the model session and its tensors. Further calls to
	// EstimateF0 return 0.
	Close() error
}

// A modelRunner is the seam between this package's pure-Go front end
// (decimation, centering, output reduction) and the ONNX session. The
// front end is exercised in tests against a fake runner, so everything
// except the ONNX calls themselves is covered without the runtime or the
// model on disk.
type modelRunner interface {
	// name identifies the backend for pitch.F0Estimator.Name.
	name() string
	// resize prepares the model to consume exactly samples 16 kHz
	// samples per run and returns the length actually adopted, which
	// differs when the model fixes its own input length. It may
	// allocate; the front end calls it only when the analysis window
	// length changes.
	resize(samples int) (int, error)
	// input is the reusable input buffer, len == the adopted length.
	// Valid until the next resize.
	input() []float32
	// run executes the model over input and returns per-frame pitch in
	// Hz and confidence. Both slices alias buffers owned by the runner
	// and are valid until the next run.
	run() (pitchHz, confidence []float32, err error)
	// close releases the session.
	close() error
}

const (
	// minModelSamples floors the input length handed to the model. A
	// 2048-sample window at 48 kHz decimates to 682 samples (~43 ms),
	// which is plenty; this only guards absurdly short windows from
	// producing a model input with no frames in it at all.
	minModelSamples = 256
	// selectConfidenceRatio picks which of the model's output frames
	// vote on the answer: those within this fraction of the best
	// confidence in the window. One frame would be noisy and every frame
	// would average in the zero-padded edges; this takes the median of
	// the frames the model itself is most sure about.
	selectConfidenceRatio = 0.8
)

// estimator is the pure-Go front end. Steady state (an unchanging window
// length) allocates nothing: the decimation buffer, the model input
// tensor, and the median scratch are all sized once and reused.
type estimator struct {
	r   modelRunner
	dec *decimator

	winLen int // analysis-window length the current sizing was built for
	dsLen  int // decimated length of that window; 0 disables the estimator

	// How the decimated window maps into the model input buffer: copy
	// ds[srcOff:srcOff+copyLen] to input[dstOff:dstOff+copyLen]. Both
	// are centered, so a short window is zero-padded symmetrically and a
	// long one is cropped symmetrically, keeping the window's center —
	// the instant the detector stamps the frame at — in the middle of
	// what the model sees.
	srcOff, dstOff, copyLen int

	ds      []float32 // decimated window, sized once per window length
	scratch []float64 // pitches of the selected frames, for the median
	err     error
	closed  bool
}

// newEstimator wires a runner to a decimator for the given input rate.
func newEstimator(r modelRunner, sampleRate int) (*estimator, error) {
	dec, err := newDecimator(sampleRate)
	if err != nil {
		return nil, err
	}
	return &estimator{
		r:       r,
		dec:     dec,
		scratch: make([]float64, 0, 128),
	}, nil
}

// Name implements pitch.F0Estimator.
func (e *estimator) Name() string { return e.r.name() }

// Err implements Estimator.
func (e *estimator) Err() error { return e.err }

// Close implements Estimator. It is idempotent.
func (e *estimator) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.r.close()
}

// EstimateF0 implements pitch.F0Estimator: decimate the window to 16 kHz,
// center it in the model's input tensor, run, and reduce the model's
// per-frame output to one (Hz, clarity) pair. A failure anywhere returns
// (0, 0) — unvoiced, the same thing the built-in detector reports when it
// has nothing to say — and is recorded in Err rather than panicking or
// logging from the audio path.
func (e *estimator) EstimateF0(window []float32) (float64, float64) {
	if e.closed || len(window) == 0 {
		return 0, 0
	}
	if len(window) != e.winLen {
		if err := e.reshape(len(window)); err != nil {
			e.err = err
			return 0, 0
		}
	}
	if e.dsLen == 0 {
		return 0, 0
	}

	ds := e.dec.decimate(window, e.ds)
	in := e.r.input()
	if len(in) < e.dstOff+e.copyLen || len(ds) < e.srcOff+e.copyLen {
		// A runner may only change its buffer inside resize, so this is
		// a broken runner rather than a recoverable state. Refuse to
		// index into it, and stay refused (see reshape's note on why
		// nothing here retries on the audio path).
		e.err = fmt.Errorf("pitchml: model input buffer is %d samples, too short for the %d it accepted", len(in), e.dstOff+e.copyLen)
		e.dsLen = 0
		return 0, 0
	}
	copy(in[e.dstOff:e.dstOff+e.copyLen], ds[e.srcOff:e.srcOff+e.copyLen])

	pitchHz, confidence, err := e.r.run()
	if err != nil {
		e.err = err
		return 0, 0
	}
	return e.reduce(pitchHz, confidence)
}

// reshape re-derives every size that depends on the analysis-window
// length. It runs on the first call and again only if the caller changes
// its window size, which the detector never does mid-stream.
//
// A failure here leaves dsLen at 0, which makes every later window of the
// same length report unvoiced without retrying. That is deliberate: if
// allocating a tensor or loading a session failed once, retrying it every
// hop on the audio path would turn a dead backend into a stuttering one.
// The caller sees the reason through Err and can rebuild the estimator.
func (e *estimator) reshape(n int) error {
	e.winLen = n
	e.dsLen = e.dec.outLen(n)
	if e.dsLen <= 0 {
		return fmt.Errorf("pitchml: analysis window of %d samples is shorter than the %d:1 decimation ratio", n, e.dec.ratio)
	}

	want := e.dsLen
	if want < minModelSamples {
		want = minModelSamples
	}
	got, err := e.r.resize(want)
	if err != nil {
		e.dsLen = 0
		return err
	}
	if got <= 0 {
		e.dsLen = 0
		return fmt.Errorf("pitchml: model adopted an input length of %d samples", got)
	}

	if cap(e.ds) < e.dsLen {
		e.ds = make([]float32, e.dsLen)
	}
	e.ds = e.ds[:e.dsLen]

	e.copyLen = e.dsLen
	e.srcOff = 0
	if e.copyLen > got {
		e.srcOff = (e.copyLen - got) / 2
		e.copyLen = got
	}
	e.dstOff = (got - e.copyLen) / 2

	// The padding around the copy region never changes while the window
	// length is constant, so zero the input once here instead of on
	// every call.
	in := e.r.input()
	for i := range in {
		in[i] = 0
	}
	return nil
}

// reduce turns the model's per-frame pitch and confidence into the single
// (f0, clarity) the F0Estimator contract wants. Clarity is the best
// confidence in the window — the model's own answer to "was anything
// pitched here" — and f0 is the median pitch over the frames within
// selectConfidenceRatio of it. f0 is 0 (unvoiced) when nothing in the
// window carried a usable pitch.
func (e *estimator) reduce(pitchHz, confidence []float32) (float64, float64) {
	n := min(len(pitchHz), len(confidence))
	if n == 0 {
		return 0, 0
	}

	best := math.Inf(-1)
	for i := range n {
		if c := float64(confidence[i]); c > best {
			best = c
		}
	}
	if !(best > 0) { // covers all-NaN and silence alike
		return 0, 0
	}
	clarity := min(best, 1)

	cut := selectConfidenceRatio * best
	sel := e.scratch[:0]
	for i := range n {
		if c := float64(confidence[i]); !(c >= cut) {
			continue
		}
		f := float64(pitchHz[i])
		if !(f > 0) || math.IsInf(f, 0) {
			continue
		}
		sel = append(sel, f)
	}
	e.scratch = sel
	if len(sel) == 0 {
		return 0, clarity
	}
	slices.Sort(sel)
	return median(sel), clarity
}

// median of a sorted slice; the mean of the middle pair when even.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return 0.5 * (sorted[n/2-1] + sorted[n/2])
}
