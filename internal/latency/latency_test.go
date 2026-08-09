package latency

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

const sr = 48000

// loopback builds a synthetic capture the way the wizard's hardware loop
// would: the train scaled by scale, delayed by delay frames, over a bed of
// Gaussian noise of RMS noiseRMS with a constant DC offset, followed by
// tail extra frames. Deterministic per seed.
func loopback(train []float32, delay int, scale, noiseRMS, dc float64, tail int, seed uint64) []float32 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	out := make([]float32, delay+len(train)+tail)
	for i := range out {
		out[i] = float32(noiseRMS*rng.NormFloat64() + dc)
	}
	for i, s := range train {
		out[delay+i] += float32(float64(s) * scale)
	}
	return out
}

func TestClickTrainLayout(t *testing.T) {
	const n, spacing = 4, 9600
	train := ClickTrain(sr, n, spacing)
	if len(train) != n*spacing {
		t.Fatalf("len = %d, want %d", len(train), n*spacing)
	}
	var peak float64
	for _, s := range train {
		if a := math.Abs(float64(s)); a > peak {
			peak = a
		}
	}
	if peak < 0.79 || peak > 0.81 {
		t.Errorf("peak = %g, want ~0.8", peak)
	}
	burstLen := clickLen(sr)
	for i := 0; i < n; i++ {
		var e float64
		for _, s := range train[i*spacing : i*spacing+burstLen] {
			e += float64(s) * float64(s)
		}
		if e == 0 {
			t.Errorf("click %d: burst slot is silent", i)
		}
		for j := i*spacing + burstLen; j < (i+1)*spacing; j++ {
			if train[j] != 0 {
				t.Errorf("click %d: gap sample %d = %g, want silence between bursts", i, j, train[j])
				break
			}
		}
	}
}

func TestClickTrainInvalidArgs(t *testing.T) {
	tests := []struct {
		name                   string
		sampleRate, n, spacing int
	}{
		{"zero sample rate", 0, 4, 9600},
		{"zero clicks", sr, 0, 9600},
		{"negative spacing", sr, 4, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClickTrain(tt.sampleRate, tt.n, tt.spacing); got != nil {
				t.Errorf("ClickTrain = %d samples, want nil", len(got))
			}
		})
	}
}

func TestEstimateRecoversKnownDelay(t *testing.T) {
	const n, spacing = 6, 24000 // 0.5 s spacing: above every delay under test
	train := ClickTrain(sr, n, spacing)
	noise := math.Pow(10, -30.0/20) // -30 dBFS RMS white noise

	tests := []struct {
		name  string
		delay int
		dc    float64
	}{
		{"tiny delay", 7, 0},
		{"typical interface delay", 480, 0},
		{"long delay", 4801, 0},
		{"DC offset in capture", 480, 0.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 50% amplitude, noise floor, optional DC — the messy but
			// workable capture a real loopback produces.
			captured := loopback(train, tt.delay, 0.5, noise, tt.dc, clickLen(sr), 1)
			off, conf, err := Estimate(sr, train, captured, spacing, n)
			if err != nil {
				t.Fatalf("Estimate: %v (confidence %.3f)", err, conf)
			}
			if d := off - tt.delay; d < -1 || d > 1 {
				t.Errorf("offset = %d, want %d +/- 1", off, tt.delay)
			}
			if conf < 0.8 {
				t.Errorf("confidence = %.3f, want >= 0.8", conf)
			}
		})
	}
}

func TestEstimateRejectsGarbage(t *testing.T) {
	const n, spacing = 6, 24000
	train := ClickTrain(sr, n, spacing)
	tests := []struct {
		name     string
		captured []float32
	}{
		{"silence", make([]float32, len(train)+sr/2)},
		// scale 0 drops the train, leaving -20 dBFS noise only.
		{"pure noise", loopback(train, 0, 0, 0.1, 0, sr/2, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, conf, err := Estimate(sr, train, tt.captured, spacing, n)
			if err == nil {
				t.Fatal("Estimate succeeded on garbage; want an error")
			}
			if conf >= minConfidence {
				t.Errorf("confidence = %.3f, want < %.2f", conf, minConfidence)
			}
		})
	}
}

func TestEstimateRejectsAliasedDelay(t *testing.T) {
	const n, spacing = 6, 9600 // 200 ms spacing
	train := ClickTrain(sr, n, spacing)
	noise := math.Pow(10, -30.0/20)

	// True delay one spacing plus 10 ms: each click's search window
	// holds only the PREVIOUS click's arrival, so clicks 1..n-1 cluster
	// tightly at the aliased lag while click 0 matches nothing. This
	// used to return the aliased 480-frame offset with high confidence
	// (the unmatched first click cost only 1/n).
	delay := spacing + 480
	captured := loopback(train, delay, 0.5, noise, 0, clickLen(sr), 4)
	off, conf, err := Estimate(sr, train, captured, spacing, n)
	if err == nil {
		t.Fatalf("Estimate = (%d, %.3f, nil) for a %d-frame delay beyond the %d-frame spacing; want an error", off, conf, delay, spacing)
	}
	if !strings.Contains(err.Error(), "spacing") {
		t.Errorf("error %q does not point at the click spacing", err)
	}

	// A delay just inside the spacing still estimates normally.
	delay = spacing - 100
	captured = loopback(train, delay, 0.5, noise, 0, clickLen(sr), 5)
	off, conf, err = Estimate(sr, train, captured, spacing, n)
	if err != nil {
		t.Fatalf("Estimate: %v (confidence %.3f)", err, conf)
	}
	if d := off - delay; d < -1 || d > 1 {
		t.Errorf("offset = %d, want %d +/- 1", off, delay)
	}
	if conf < 0.8 {
		t.Errorf("confidence = %.3f, want >= 0.8", conf)
	}
}

func TestEstimatePartialCapture(t *testing.T) {
	const n, spacing, delay = 8, 24000, 480
	train := ClickTrain(sr, n, spacing)
	full := loopback(train, delay, 0.5, 0.01, 0, clickLen(sr), 3)
	// Recording stopped right after the third click's arrival: clicks
	// 3..7 are gone, but the three present clicks still carry the vote.
	captured := full[:2*spacing+delay+clickLen(sr)]
	off, conf, err := Estimate(sr, train, captured, spacing, n)
	if err != nil {
		t.Fatalf("Estimate: %v (confidence %.3f)", err, conf)
	}
	if d := off - delay; d < -1 || d > 1 {
		t.Errorf("offset = %d, want %d +/- 1", off, delay)
	}
	if conf < 0.8 {
		t.Errorf("confidence = %.3f, want >= 0.8", conf)
	}
}

func TestEstimateInputValidation(t *testing.T) {
	train := ClickTrain(sr, 4, 24000)
	captured := make([]float32, sr)
	tests := []struct {
		name string
		call func() (int, float64, error)
	}{
		{"zero sample rate", func() (int, float64, error) {
			return Estimate(0, train, captured, 24000, 4)
		}},
		{"zero clicks", func() (int, float64, error) {
			return Estimate(sr, train, captured, 24000, 0)
		}},
		{"played shorter than one click", func() (int, float64, error) {
			return Estimate(sr, train[:10], captured, 24000, 4)
		}},
		{"silent played signal", func() (int, float64, error) {
			return Estimate(sr, make([]float32, len(train)), captured, 24000, 4)
		}},
		{"captured too short for any click", func() (int, float64, error) {
			return Estimate(sr, train, captured[:10], 24000, 4)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := tt.call(); err == nil {
				t.Error("Estimate succeeded; want an error")
			}
		})
	}
}
