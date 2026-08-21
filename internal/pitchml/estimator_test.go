package pitchml

import (
	"errors"
	"math"
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
)

type fakeRunner struct {
	label string

	fixedLen int

	adoptZero bool

	shrinkTo int
	resizeEr error
	runEr    error

	buf     []float32
	resizes int
	runs    int
	seen    []float32

	pitchOut []float32
	confOut  []float32
}

func (f *fakeRunner) name() string { return f.label }

func (f *fakeRunner) resize(samples int) (int, error) {
	if f.resizeEr != nil {
		return 0, f.resizeEr
	}
	f.resizes++
	if f.adoptZero {
		return 0, nil
	}
	n := samples
	if f.fixedLen > 0 {
		n = f.fixedLen
	}
	f.buf = make([]float32, n)
	f.seen = make([]float32, n)
	return n, nil
}

func (f *fakeRunner) input() []float32 {
	if f.shrinkTo > 0 && f.runs > 0 {
		return f.buf[:f.shrinkTo]
	}
	return f.buf
}

func (f *fakeRunner) run() ([]float32, []float32, error) {
	f.runs++
	if f.runEr != nil {
		return nil, nil, f.runEr
	}
	copy(f.seen, f.buf)
	return f.pitchOut, f.confOut, nil
}

func (f *fakeRunner) close() error { return nil }

func newFake() *fakeRunner {
	return &fakeRunner{
		label:    EstimatorName,
		pitchOut: []float32{110},
		confOut:  []float32{0.9},
	}
}

func mustEstimator(t *testing.T, r modelRunner, rate int) *estimator {
	t.Helper()
	e, err := newEstimator(r, rate)
	if err != nil {
		t.Fatalf("newEstimator: %v", err)
	}
	return e
}

var (
	_ pitch.F0Estimator = (*estimator)(nil)
	_ Estimator         = (*estimator)(nil)
)

func TestEstimatorNameComesFromTheBackend(t *testing.T) {
	e := mustEstimator(t, &fakeRunner{label: "swiftf0"}, 48000)
	if e.Name() != "swiftf0" {
		t.Errorf("Name = %q, want %q", e.Name(), "swiftf0")
	}
}

func TestEstimateF0ReportsMedianOfConfidentFrames(t *testing.T) {
	f := newFake()

	f.pitchOut = []float32{40, 219, 220, 221, 3000}
	f.confOut = []float32{0.1, 0.95, 0.99, 0.90, 0.2}
	e := mustEstimator(t, f, 48000)

	f0, clarity := e.EstimateF0(make([]float32, 2048))
	if math.Abs(f0-220) > 1e-6 {
		t.Errorf("f0 = %v, want 220 (median of the confident frames)", f0)
	}
	if math.Abs(clarity-0.99) > 1e-6 {
		t.Errorf("clarity = %v, want 0.99 (the best confidence)", clarity)
	}
}

func TestEstimateF0AveragesTheMiddlePairWhenEven(t *testing.T) {
	f := newFake()
	f.pitchOut = []float32{100, 110, 120, 130}
	f.confOut = []float32{0.9, 0.9, 0.9, 0.9}
	e := mustEstimator(t, f, 48000)

	if f0, _ := e.EstimateF0(make([]float32, 2048)); math.Abs(f0-115) > 1e-6 {
		t.Errorf("f0 = %v, want 115", f0)
	}
}

func TestEstimateF0UnvoicedCases(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pitch, conf []float32
	}{
		{"silence", []float32{0, 0}, []float32{0, 0}},
		{"no frames", nil, nil},
		{"negative pitch", []float32{-1}, []float32{0.9}},
		{"NaN pitch", []float32{float32(math.NaN())}, []float32{0.9}},
		{"Inf pitch", []float32{float32(math.Inf(1))}, []float32{0.9}},
		{"NaN confidence", []float32{220}, []float32{float32(math.NaN())}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.pitchOut, f.confOut = tc.pitch, tc.conf
			e := mustEstimator(t, f, 48000)
			if f0, _ := e.EstimateF0(make([]float32, 2048)); f0 != 0 {
				t.Errorf("f0 = %v, want 0 (unvoiced)", f0)
			}
		})
	}
}

func TestEstimateF0ClampsClarityIntoRange(t *testing.T) {
	f := newFake()
	f.pitchOut = []float32{220}
	f.confOut = []float32{1.7}
	e := mustEstimator(t, f, 48000)

	if _, clarity := e.EstimateF0(make([]float32, 2048)); clarity != 1 {
		t.Errorf("clarity = %v, want 1 (clamped)", clarity)
	}
}

func TestEstimateF0IgnoresRaggedOutputTails(t *testing.T) {

	f := newFake()
	f.pitchOut = []float32{220, 440, 880}
	f.confOut = []float32{0.9}
	e := mustEstimator(t, f, 48000)

	if f0, _ := e.EstimateF0(make([]float32, 2048)); f0 != 220 {
		t.Errorf("f0 = %v, want 220", f0)
	}
}

func TestEstimateF0DecimatesToTheModelRate(t *testing.T) {
	f := newFake()
	e := mustEstimator(t, f, 48000)
	e.EstimateF0(sine(2048, 440, 48000))

	if got, want := len(f.buf), 2048/3; got != want {
		t.Fatalf("model input length = %d, want %d (2048 samples at 48 kHz decimated 3:1)", got, want)
	}

	if rms := middleRMS(f.seen); math.Abs(rms-math.Sqrt2/2) > 0.05 {
		t.Errorf("decimated RMS = %.4f, want ~0.707", rms)
	}
	period := periodEstimate(f.seen, ModelSampleRate)
	if math.Abs(period-440) > 5 {
		t.Errorf("decimated tone measured at %.1f Hz, want ~440", period)
	}
}

func TestEstimateF0CentersInAnOversizedModelInput(t *testing.T) {

	f := newFake()
	f.fixedLen = 1024
	e := mustEstimator(t, f, 48000)

	win := make([]float32, 2048)
	for i := range win {
		win[i] = 1
	}
	e.EstimateF0(win)

	if len(f.seen) != 1024 {
		t.Fatalf("model input length = %d, want the model's fixed 1024", len(f.seen))
	}
	dsLen := 2048 / 3
	lead := (1024 - dsLen) / 2
	for i := 0; i < lead; i++ {
		if f.seen[i] != 0 {
			t.Fatalf("leading pad at %d = %v, want 0", i, f.seen[i])
		}
	}
	for i := lead + dsLen; i < 1024; i++ {
		if f.seen[i] != 0 {
			t.Fatalf("trailing pad at %d = %v, want 0", i, f.seen[i])
		}
	}

	mid := lead + dsLen/2
	if math.Abs(float64(f.seen[mid])-1) > 1e-4 {
		t.Errorf("center sample = %v, want ~1", f.seen[mid])
	}
}

func TestEstimateF0CropsCenteredIntoAnUndersizedModelInput(t *testing.T) {

	f := newFake()
	f.fixedLen = 300
	e := mustEstimator(t, f, 48000)

	win := make([]float32, 2048)
	for i := range win {
		win[i] = float32(i)
	}
	e.EstimateF0(win)

	if len(f.seen) != 300 {
		t.Fatalf("model input length = %d, want 300", len(f.seen))
	}
	dsLen := 2048 / 3
	srcOff := (dsLen - 300) / 2

	want := float64((srcOff) * 3)
	if got := float64(f.seen[0]); math.Abs(got-want) > 3 {
		t.Errorf("first model sample = %.1f, want ~%.1f (centered crop)", got, want)
	}
}

func TestEstimateF0ResizesOnlyWhenTheWindowLengthChanges(t *testing.T) {
	f := newFake()
	e := mustEstimator(t, f, 48000)

	for range 10 {
		e.EstimateF0(make([]float32, 2048))
	}
	if f.resizes != 1 {
		t.Errorf("resizes = %d over 10 identical windows, want 1", f.resizes)
	}
	e.EstimateF0(make([]float32, 4096))
	if f.resizes != 2 {
		t.Errorf("resizes = %d after a window-length change, want 2", f.resizes)
	}
	if len(f.buf) != 4096/3 {
		t.Errorf("model input length = %d, want %d", len(f.buf), 4096/3)
	}
}

func TestEstimateF0IsAllocationFreeInSteadyState(t *testing.T) {
	f := newFake()
	f.pitchOut = []float32{219, 220, 221}
	f.confOut = []float32{0.95, 0.99, 0.9}
	e := mustEstimator(t, f, 48000)
	win := sine(2048, 440, 48000)
	e.EstimateF0(win)

	if n := testing.AllocsPerRun(200, func() { e.EstimateF0(win) }); n != 0 {
		t.Errorf("EstimateF0 allocated %v times per call in steady state, want 0", n)
	}
}

func TestEstimateF0FloorsTheModelInputLength(t *testing.T) {

	f := newFake()
	e := mustEstimator(t, f, 48000)
	e.EstimateF0(make([]float32, 120))
	if len(f.buf) != minModelSamples {
		t.Errorf("model input length = %d, want the %d floor", len(f.buf), minModelSamples)
	}
}

func TestEstimateF0OnWindowsShorterThanTheRatio(t *testing.T) {
	f := newFake()
	e := mustEstimator(t, f, 48000)
	if f0, clarity := e.EstimateF0(make([]float32, 2)); f0 != 0 || clarity != 0 {
		t.Errorf("EstimateF0(2 samples) = (%v, %v), want (0, 0)", f0, clarity)
	}
	if e.Err() == nil {
		t.Error("Err = nil, want an explanation of the too-short window")
	}
	if f.runs != 0 {
		t.Errorf("runs = %d, want 0: the model must not be called at all", f.runs)
	}
}

func TestEstimateF0OnEmptyWindow(t *testing.T) {
	f := newFake()
	e := mustEstimator(t, f, 48000)
	if f0, clarity := e.EstimateF0(nil); f0 != 0 || clarity != 0 {
		t.Errorf("EstimateF0(nil) = (%v, %v), want (0, 0)", f0, clarity)
	}
	if f.runs != 0 {
		t.Errorf("runs = %d, want 0", f.runs)
	}
}

func TestInferenceFailureIsUnvoicedAndRecorded(t *testing.T) {
	boom := errors.New("ort exploded")
	f := newFake()
	f.runEr = boom
	e := mustEstimator(t, f, 48000)

	f0, clarity := e.EstimateF0(make([]float32, 2048))
	if f0 != 0 || clarity != 0 {
		t.Errorf("EstimateF0 = (%v, %v), want (0, 0) on inference failure", f0, clarity)
	}
	if !errors.Is(e.Err(), boom) {
		t.Errorf("Err = %v, want the inference error", e.Err())
	}
}

func TestResizeFailureIsUnvoicedAndRecorded(t *testing.T) {
	boom := errors.New("no memory for a tensor")
	f := newFake()
	f.resizeEr = boom
	e := mustEstimator(t, f, 48000)

	if f0, _ := e.EstimateF0(make([]float32, 2048)); f0 != 0 {
		t.Errorf("f0 = %v, want 0", f0)
	}
	if !errors.Is(e.Err(), boom) {
		t.Errorf("Err = %v, want the resize error", e.Err())
	}
}

func TestCloseIsIdempotentAndStopsEstimating(t *testing.T) {
	f := newFake()
	e := mustEstimator(t, f, 48000)
	e.EstimateF0(make([]float32, 2048))

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	before := f.runs
	if f0, _ := e.EstimateF0(make([]float32, 2048)); f0 != 0 {
		t.Errorf("f0 after Close = %v, want 0", f0)
	}
	if f.runs != before {
		t.Errorf("runs = %d after Close, want %d", f.runs, before)
	}
}

func TestZeroLengthAdoptionIsRejected(t *testing.T) {
	f := newFake()
	f.adoptZero = true
	e := mustEstimator(t, f, 48000)

	if f0, _ := e.EstimateF0(make([]float32, 2048)); f0 != 0 {
		t.Errorf("f0 = %v, want 0", f0)
	}
	if e.Err() == nil {
		t.Fatal("Err = nil, want a complaint about the adopted input length")
	}
	if f.runs != 0 {
		t.Errorf("runs = %d, want 0", f.runs)
	}
}

func TestRunnerShrinkingItsBufferIsCaughtNotIndexedPast(t *testing.T) {
	f := newFake()
	f.shrinkTo = 8
	e := mustEstimator(t, f, 48000)

	if f0, _ := e.EstimateF0(make([]float32, 2048)); f0 != 110 {
		t.Fatalf("first call f0 = %v, want 110", f0)
	}
	f0, clarity := e.EstimateF0(make([]float32, 2048))
	if f0 != 0 || clarity != 0 {
		t.Errorf("EstimateF0 = (%v, %v), want (0, 0) once the buffer shrank", f0, clarity)
	}
	if e.Err() == nil {
		t.Error("Err = nil, want a complaint about the shrunken input buffer")
	}

	before := f.runs
	if f0, _ := e.EstimateF0(make([]float32, 2048)); f0 != 0 {
		t.Errorf("f0 = %v on a later call, want 0", f0)
	}
	if f.runs != before {
		t.Errorf("runs = %d, want %d: a broken runner must not be called again", f.runs, before)
	}
}

func TestNewEstimatorRejectsBadSampleRate(t *testing.T) {
	if _, err := newEstimator(newFake(), 44100); !errors.Is(err, ErrSampleRate) {
		t.Errorf("err = %v, want ErrSampleRate", err)
	}
}

func periodEstimate(x []float32, rate int) float64 {
	lo, hi := len(x)/4, 3*len(x)/4
	crossings, first, last := 0, -1, -1
	for i := lo + 1; i < hi; i++ {
		if x[i-1] <= 0 && x[i] > 0 {
			if first < 0 {
				first = i
			}
			last = i
			crossings++
		}
	}
	if crossings < 2 {
		return 0
	}
	return float64(rate) * float64(crossings-1) / float64(last-first)
}
