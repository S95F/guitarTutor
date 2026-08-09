package pitch

import "testing"

// fixedEstimator is a stub F0Estimator: it reports the same pitch for
// every window, which no real signal would produce, so any frame carrying
// it proves the built-in MPM path was bypassed.
type fixedEstimator struct {
	f0      float64
	clarity float64
	calls   int
	lastLen int
}

func (e *fixedEstimator) Name() string { return "fixed-stub" }

func (e *fixedEstimator) EstimateF0(window []float32) (float64, float64) {
	e.calls++
	e.lastLen = len(window)
	return e.f0, e.clarity
}

// TestEstimatorOverridesDetection: with a Config.Estimator set, the
// reported pitch is the estimator's, not the signal's. The window it is
// handed is one full analysis window of the live stream.
func TestEstimatorOverridesDetection(t *testing.T) {
	const stubF0 = 987.77 // B5, nowhere near the 220 Hz being played
	est := &fixedEstimator{f0: stubF0, clarity: 0.95}
	cfg := DefaultConfig(testSR)
	cfg.Estimator = est
	d := NewDetector(cfg)

	frames := feedAll(d, sine(220, 0.4, 0.5), 480)
	voiced := voicedFrames(frames)
	if len(voiced) < 10 {
		t.Fatalf("only %d voiced frames, want >= 10", len(voiced))
	}
	for _, f := range voiced {
		if f.F0 != stubF0 {
			t.Fatalf("frame %d: f0 = %.2f, want the estimator's %.2f", f.Frame, f.F0, stubF0)
		}
		if f.Clarity != 0.95 {
			t.Fatalf("frame %d: clarity = %.3f, want the estimator's 0.95", f.Frame, f.Clarity)
		}
	}
	if est.calls == 0 {
		t.Fatal("estimator never called")
	}
	if est.lastLen != cfg.Window {
		t.Errorf("estimator handed a %d-sample window, want %d", est.lastLen, cfg.Window)
	}
	if got, want := d.EstimatorName(), "fixed-stub"; got != want {
		t.Errorf("EstimatorName() = %q, want %q", got, want)
	}
}

// TestEstimatorKeepsGateAndVoicingRules pins which stages an external
// estimator does NOT replace: the RMS noise gate still silences it, and
// ClarityThreshold and the MinHz/MaxHz range still decide voicing.
func TestEstimatorKeepsGateAndVoicingRules(t *testing.T) {
	tests := []struct {
		name       string
		f0         float64
		clarity    float64
		x          []float32
		wantVoiced bool
	}{
		{"confident and in range", 220, 0.95, sine(220, 0.4, 0.4), true},
		{"under the clarity threshold", 220, 0.5, sine(220, 0.4, 0.4), false},
		{"above MaxHz", 3000, 0.99, sine(220, 0.4, 0.4), false},
		{"below MinHz", 30, 0.99, sine(220, 0.4, 0.4), false},
		{"unvoiced report", 0, 0.99, sine(220, 0.4, 0.4), false},
		{"gated by the noise floor", 220, 0.99, silence(0.4), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig(testSR)
			cfg.Estimator = &fixedEstimator{f0: tt.f0, clarity: tt.clarity}
			d := NewDetector(cfg)
			frames := feedAll(d, tt.x, 480)
			if len(frames) == 0 {
				t.Fatal("no frames emitted")
			}
			got := len(voicedFrames(frames)) > 0
			if got != tt.wantVoiced {
				t.Errorf("voiced = %v, want %v", got, tt.wantVoiced)
			}
		})
	}
}

// TestEstimatorKeepsOnsetsAndStrums: swapping the pitch estimator leaves
// the parts of the pipeline that do not depend on it — the onset detector
// and Phase 4's chroma/strum accumulation — running exactly as before.
func TestEstimatorKeepsOnsetsAndStrums(t *testing.T) {
	x := append(silence(0.2), ksNote(45, 0.5)...)

	cfg := strumConfig()
	cfg.Estimator = &fixedEstimator{f0: 220, clarity: 0.9}
	withEst := NewDetector(cfg)
	estFrames, estStrums := feedStrums(withEst, x, 480)

	builtIn := NewDetector(strumConfig())
	biFrames, biStrums := feedStrums(builtIn, x, 480)

	if len(estStrums) != 1 || len(biStrums) != 1 {
		t.Fatalf("strums: %d with an estimator, %d without; want 1 each", len(estStrums), len(biStrums))
	}
	if estStrums[0].Frame != biStrums[0].Frame {
		t.Errorf("strum stamped at %d with an estimator, %d without", estStrums[0].Frame, biStrums[0].Frame)
	}
	if estStrums[0].Chroma != biStrums[0].Chroma {
		t.Errorf("chroma differs with an estimator:\n got%s\nwant%s",
			formatChroma(estStrums[0].Chroma), formatChroma(biStrums[0].Chroma))
	}
	// Strum.Clarity comes from the estimator by design (it is the pitch
	// estimator's confidence), so only the onsets are compared here.
	for i := range biFrames {
		if estFrames[i].Onset != biFrames[i].Onset || estFrames[i].RMS != biFrames[i].RMS {
			t.Fatalf("frame %d: onset/RMS differ: %+v with an estimator, %+v without",
				i, estFrames[i], biFrames[i])
		}
	}
}

// TestNilEstimatorIsTheBuiltIn: a nil Config.Estimator selects MPM and
// changes nothing. The whole existing detector suite runs with it nil, so
// this only pins the naming contract and the config default.
func TestNilEstimatorIsTheBuiltIn(t *testing.T) {
	d := NewDetector(DefaultConfig(testSR))
	if got, want := d.EstimatorName(), "mpm"; got != want {
		t.Errorf("EstimatorName() = %q with no estimator configured, want %q", got, want)
	}
	frames := feedAll(d, ksNote(45, 0.4), 480)
	if got := medianF0Of(t, frames, 8); nearestKey(got) != 45 {
		t.Errorf("detected key %d, want 45: the built-in path changed", nearestKey(got))
	}
}
