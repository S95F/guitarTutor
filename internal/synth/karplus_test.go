package synth

import (
	"math"
	"testing"
)

// renderFrames drives v for frames samples in engine-sized chunks and
// returns the two channels (zeroed before mixing, as the engine would).
func renderFrames(v Voice, frames, chunk int) (left, right []float32) {
	left = make([]float32, frames)
	right = make([]float32, frames)
	for off := 0; off < frames; off += chunk {
		end := off + chunk
		if end > frames {
			end = frames
		}
		v.Render(left[off:end], right[off:end])
	}
	return left, right
}

// monoSum folds a stereo pair to mono by summing channels.
func monoSum(left, right []float32) []float32 {
	m := make([]float32, len(left))
	for i := range left {
		m[i] = left[i] + right[i]
	}
	return m
}

// energy returns the sum of squares of x.
func energy(x []float32) float64 {
	var e float64
	for _, s := range x {
		e += float64(s) * float64(s)
	}
	return e
}

// peak returns the largest absolute sample in x.
func peak(x []float32) float64 {
	var p float64
	for _, s := range x {
		if a := math.Abs(float64(s)); a > p {
			p = a
		}
	}
	return p
}

// estimateFundamental estimates the fundamental of x in Hz by normalized
// autocorrelation over lags spanning 30-1500 Hz. Every multiple of the true
// period correlates equally well, so the global maximum alone is
// octave-ambiguous: it takes the smallest local-maximum lag within 5% of
// the global maximum, then refines it by parabolic interpolation.
func estimateFundamental(t *testing.T, x []float32, sampleRate int) float64 {
	t.Helper()
	const window = 8192
	minLag := sampleRate / 1500
	maxLag := sampleRate / 30
	if len(x) < window+maxLag {
		t.Fatalf("estimateFundamental: need %d samples, have %d", window+maxLag, len(x))
	}
	var e0 float64
	for i := 0; i < window; i++ {
		e0 += float64(x[i]) * float64(x[i])
	}
	r := make([]float64, maxLag+1)
	for lag := minLag; lag <= maxLag; lag++ {
		var cross, eLag float64
		for i := 0; i < window; i++ {
			cross += float64(x[i]) * float64(x[i+lag])
			eLag += float64(x[i+lag]) * float64(x[i+lag])
		}
		if e0 > 0 && eLag > 0 {
			r[lag] = cross / math.Sqrt(e0*eLag)
		}
	}
	rmax := 0.0
	for lag := minLag; lag <= maxLag; lag++ {
		if r[lag] > rmax {
			rmax = r[lag]
		}
	}
	for lag := minLag + 1; lag < maxLag; lag++ {
		if r[lag] < 0.95*rmax || r[lag] < r[lag-1] || r[lag] < r[lag+1] {
			continue
		}
		// Parabolic interpolation over the peak and its neighbors gives
		// sub-sample lag resolution.
		denom := r[lag-1] - 2*r[lag] + r[lag+1]
		delta := 0.0
		if denom != 0 {
			delta = 0.5 * (r[lag-1] - r[lag+1]) / denom
		}
		return float64(sampleRate) / (float64(lag) + delta)
	}
	t.Fatal("estimateFundamental: no autocorrelation peak found")
	return 0
}

// centsBetween returns the musical interval from a to b in cents.
func centsBetween(a, b float64) float64 {
	return 1200 * math.Log2(b/a)
}

// bestDelayFreq returns the pitch nearest f (in cents) that an integer
// delay line can produce. The Karplus-Strong loop resonates at
// sampleRate/(n - 0.5) for integer n, so this is the best tuning any
// correctly tuned voice can achieve for f; at high keys it can sit well
// over 10 cents from f, which is why the high-key tests compare against it
// rather than against f directly.
func bestDelayFreq(sampleRate int, f float64) float64 {
	center := int(math.Round(float64(sampleRate)/f + 0.5))
	bestF, bestErr := 0.0, math.Inf(1)
	for n := center - 2; n <= center+2; n++ {
		if n < 2 {
			continue
		}
		fn := float64(sampleRate) / (float64(n) - 0.5)
		if e := math.Abs(centsBetween(f, fn)); e < bestErr {
			bestF, bestErr = fn, e
		}
	}
	return bestF
}

func TestPluckFundamental(t *testing.T) {
	const sr = 48000
	tests := []struct {
		name string
		key  int
		want float64 // Hz
		// cents > 0 asserts in cents against the best achievable
		// delay-line pitch for want (integer-length quantization makes an
		// absolute-Hz bound meaningless at high keys); cents == 0 asserts
		// want ±1 Hz.
		cents float64
	}{
		{"low E open", 40, 82.4069, 0},
		{"A open", 45, 110.0, 0},
		{"D open", 50, 146.832, 0},
		{"below range clamps to C1", 0, 32.7032, 0},
		// High keys are the regression guard for the loop-delay sign: the
		// averaging filter shortens the loop to n - 0.5 samples, and
		// tuning for n + 0.5 instead played ~24 cents sharp at key 76 and
		// ~59 cents sharp at key 86 (48 kHz).
		{"high E 12th fret", 76, 659.2551, 5},
		{"high E 22nd fret", 86, 1174.659, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewPluck(sr, 25)
			v.NoteOn(tt.key, 0.8)
			left, right := renderFrames(v, sr, 512)
			mono := monoSum(left, right)
			// Estimate on a late window: the excitation is a broadband
			// noise burst, so early samples are dominated by the noisy
			// attack; half a second in, only the periodic string
			// resonance remains.
			got := estimateFundamental(t, mono[sr/2:], sr)
			if tt.cents > 0 {
				best := bestDelayFreq(sr, tt.want)
				if off := centsBetween(best, got); math.Abs(off) > tt.cents {
					t.Errorf("fundamental = %.3f Hz, %+.1f cents from best achievable %.3f Hz (target %.3f Hz); want within %.1f cents",
						got, off, best, tt.want, tt.cents)
				}
			} else if math.Abs(got-tt.want) > 1.0 {
				t.Errorf("fundamental = %.3f Hz, want %.3f ±1 Hz", got, tt.want)
			}
		})
	}
}

func TestPluckDecayMonotonic(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25)
	v.NoteOn(40, 0.8)
	left, right := renderFrames(v, sr, 512)
	mono := monoSum(left, right)

	// 200 ms windows: several periods of even the lowest key, so window
	// phase barely ripples the energy, while the loop decay dominates.
	const win = sr / 5
	prev := math.Inf(1)
	for off := 0; off+win <= len(mono); off += win {
		e := energy(mono[off : off+win])
		if off == 0 && e == 0 {
			t.Fatal("no signal after NoteOn")
		}
		if e >= prev {
			t.Errorf("window at %d: energy %g did not decay (prev %g)", off, e, prev)
		}
		prev = e
	}
}

func TestPluckNoteOffFastensDecay(t *testing.T) {
	const sr = 48000
	const win = sr / 10

	// Same seed, same note: the two renders are identical until NoteOff.
	held := NewPluck(sr, 25)
	held.NoteOn(40, 0.8)
	hl, hr := renderFrames(held, 2*sr/5, 512)
	hm := monoSum(hl, hr)

	released := NewPluck(sr, 25)
	released.NoteOn(40, 0.8)
	rl, rr := renderFrames(released, sr/5, 512)
	released.NoteOff(40)
	rl2, rr2 := renderFrames(released, sr/5, 512)
	rm := append(monoSum(rl, rr), monoSum(rl2, rr2)...)

	// Compare energy ratios across the same two windows after the 0.2 s
	// mark: the released note must decay faster than the held one.
	heldRatio := energy(hm[3*win:4*win]) / energy(hm[2*win:3*win])
	relRatio := energy(rm[3*win:4*win]) / energy(rm[2*win:3*win])
	if relRatio >= heldRatio {
		t.Errorf("released decay ratio %g not faster than held %g", relRatio, heldRatio)
	}
}

func TestPluckAllNotesOffSilences(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25)
	v.NoteOn(40, 0.8)
	v.NoteOn(45, 0.8)
	renderFrames(v, sr/10, 512) // ring for 100 ms
	v.AllNotesOff()
	left, right := renderFrames(v, sr/10, 512)

	// Click-free: the cut is a short ramp, not an instant zero, so the
	// first couple of milliseconds still carry signal.
	if peak(left[:120]) == 0 && peak(right[:120]) == 0 {
		t.Error("output hard-zeroed immediately after AllNotesOff; want a ramp")
	}

	// Silent below -60 dBFS within 50 ms.
	const floor = 0.001 // -60 dBFS
	if p := peak(left[sr/20:]); p > floor {
		t.Errorf("left peak %g after 50 ms, want <= %g", p, floor)
	}
	if p := peak(right[sr/20:]); p > floor {
		t.Errorf("right peak %g after 50 ms, want <= %g", p, floor)
	}
}

func TestPluckChordPeakSafe(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25)
	// All six open strings at full velocity: the worst realistic case.
	for _, key := range []int{40, 45, 50, 55, 59, 64} {
		v.NoteOn(key, 1.0)
	}
	left, right := renderFrames(v, sr/2, 512)
	if p := peak(left); p >= 1.0 {
		t.Errorf("left peak %g, want < 1.0", p)
	}
	if p := peak(right); p >= 1.0 {
		t.Errorf("right peak %g, want < 1.0", p)
	}

	// Per-note stereo placement: the channels must differ audibly.
	var diff float64
	for i := range left {
		if d := math.Abs(float64(left[i] - right[i])); d > diff {
			diff = d
		}
	}
	if diff < 1e-3 {
		t.Errorf("max channel difference %g; want stereo width on a chord", diff)
	}
}

func TestPluckRenderDoesNotAllocate(t *testing.T) {
	v := NewPluck(48000, 25)
	v.NoteOn(40, 0.8)
	v.NoteOn(47, 0.9)
	left := make([]float32, 512)
	right := make([]float32, 512)
	if allocs := testing.AllocsPerRun(100, func() {
		v.Render(left, right)
	}); allocs != 0 {
		t.Errorf("Render allocates %v times per call, want 0", allocs)
	}
	// The control calls share the realtime path with Render.
	if allocs := testing.AllocsPerRun(100, func() {
		v.NoteOn(52, 0.7)
		v.NoteOff(52)
		v.Render(left, right)
		v.AllNotesOff()
	}); allocs != 0 {
		t.Errorf("control+Render allocates %v times per run, want 0", allocs)
	}
}

func TestPluckPolyphonyStealsOldest(t *testing.T) {
	v := NewPluck(48000, 25)
	p := v.(*pluck)
	for i := 0; i < pluckPolyphony; i++ {
		v.NoteOn(40+i, 0.8)
	}
	v.NoteOn(90, 0.8) // pool full: must steal the oldest (key 40)

	active := 0
	sawOldest, sawNewest := false, false
	for i := range p.voices {
		vc := &p.voices[i]
		if !vc.active {
			continue
		}
		active++
		if vc.key == 40 {
			sawOldest = true
		}
		if vc.key == 90 {
			sawNewest = true
		}
	}
	if active != pluckPolyphony {
		t.Errorf("active voices = %d, want %d", active, pluckPolyphony)
	}
	if sawOldest {
		t.Error("oldest note still sounding; want it stolen")
	}
	if !sawNewest {
		t.Error("newest note not sounding; want it to take the stolen slot")
	}
}
