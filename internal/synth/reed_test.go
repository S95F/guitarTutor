package synth

// The wind voice's contract, property-tested the way tone_test.go pins the
// pluck's: in tune across the wind ranges, flat while blown, quickly quiet
// when the breath stops, slurred without a second attack, and level-matched
// to the window the practice pipeline's gates are calibrated against.

import (
	"math"
	"testing"
)

// rmsOf returns the root-mean-square of x.
func rmsOf(x []float32) float64 {
	if len(x) == 0 {
		return 0
	}
	return math.Sqrt(energy(x) / float64(len(x)))
}

func TestReedFundamental(t *testing.T) {
	const sr = 48000
	tests := []struct {
		name string
		key  int
		want float64
	}{
		{"soprano sax lowest (Ab3)", 56, 207.65},
		{"A4", 69, 440.0},
		{"A5", 81, 880.0},
		{"soprano sax top (E6)", 88, 1318.51},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := NewReed(sr, 64)
			v.NoteOn(tt.key, 1)
			l, r := renderFrames(v, sr, 2048)
			m := monoSum(l, r)
			// Skip the attack; measure the sustain.
			got := estimateFundamental(t, m[sr/4:], sr)
			if cents := math.Abs(centsBetween(tt.want, got)); cents > 3 {
				t.Errorf("key %d: %.2f Hz, want %.2f (off %.1f cents)", tt.key, got, tt.want, cents)
			}
		})
	}
}

// TestReedSustainsWhileHeld: a blown note holds its level for as long as
// the breath does — the property no delay-loop pluck can have.
func TestReedSustainsWhileHeld(t *testing.T) {
	const sr = 48000
	v := NewReed(sr, 64)
	v.NoteOn(69, 1)
	l, r := renderFrames(v, 3*sr, 2048)
	m := monoSum(l, r)
	early := rmsOf(m[sr/2 : sr/2+sr/10])
	late := rmsOf(m[5*sr/2 : 5*sr/2+sr/10])
	if early <= 0 {
		t.Fatal("no output while held")
	}
	if db := 20 * math.Log10(late/early); math.Abs(db) > 1 {
		t.Errorf("held level moved %.2f dB between 0.5 s and 2.5 s, want flat", db)
	}
}

// TestReedReleaseIsFast: NoteOff is the breath stopping, not a string
// ringing out — 60 dB down well inside a quarter second.
func TestReedReleaseIsFast(t *testing.T) {
	const sr = 48000
	v := NewReed(sr, 64)
	v.NoteOn(69, 1)
	l, r := renderFrames(v, sr/2, 2048)
	held := rmsOf(monoSum(l, r)[sr/4:])
	v.NoteOff(69)
	l, r = renderFrames(v, sr/2, 2048)
	m := monoSum(l, r)
	tail := rmsOf(m[sr/4 : sr/4+sr/10]) // 250-350 ms after the release
	if held <= 0 {
		t.Fatal("no held output")
	}
	if db := 20 * math.Log10((tail+1e-12)/held); db > -60 {
		t.Errorf("release is %.1f dB down after 250 ms, want at least -60", db)
	}
}

// TestReedLevelWindow: velocity-1 peak lands where the pluck's does,
// because internal/pitch's input gate and onset thresholds are calibrated
// against the built-in voices' output level.
func TestReedLevelWindow(t *testing.T) {
	const sr = 48000
	v := NewReed(sr, 64)
	v.NoteOn(69, 1)
	l, r := renderFrames(v, sr, 2048)
	p := peak(monoSum(l, r))
	if p < 0.1 || p > 1.0 {
		t.Errorf("velocity-1 peak = %.3f, want within [0.1, 1.0]", p)
	}
}

// TestReedSlurDoesNotReattack: a slurred note change re-pitches the
// standing tone. The level through the transition never collapses (no gap,
// no restart from silence) and the pitch afterwards is the new note's.
func TestReedSlurDoesNotReattack(t *testing.T) {
	const sr = 48000
	r := NewReed(sr, 64).(*reed)
	r.NoteOn(69, 1)
	l, rr := renderFrames(r, sr/2, 2048)
	steady := rmsOf(monoSum(l, rr)[sr/4:])

	r.NoteOnSpec(NoteSpec{Key: 71, Velocity: 1, Attack: AttackLegato, From: 69})
	l, rr = renderFrames(r, sr/2, 2048)
	m := monoSum(l, rr)
	// The 50 ms straddling the change: a fresh attack would pass through
	// (near) zero on its way up from env 0.
	dip := rmsOf(m[:sr/20])
	if dip < steady/2 {
		t.Errorf("slur dipped to %.4f RMS (steady %.4f): that is a re-attack", dip, steady)
	}
	got := estimateFundamental(t, m[sr/8:], sr)
	if cents := math.Abs(centsBetween(493.88, got)); cents > 3 {
		t.Errorf("after the slur: %.2f Hz, want B4 (off %.1f cents)", got, cents)
	}
}

// TestReedSlideArrives: a scoop travels and comes to rest exactly on the
// destination pitch.
func TestReedSlideArrives(t *testing.T) {
	const sr = 48000
	r := NewReed(sr, 64).(*reed)
	r.NoteOn(69, 1)
	renderFrames(r, sr/4, 2048)
	r.NoteOnSpec(NoteSpec{Key: 74, Velocity: 1, Attack: AttackSlide, From: 69})
	l, rr := renderFrames(r, sr, 2048)
	m := monoSum(l, rr)
	got := estimateFundamental(t, m[sr/4:], sr)
	if cents := math.Abs(centsBetween(587.33, got)); cents > 3 {
		t.Errorf("slide landed at %.2f Hz, want D5 (off %.1f cents)", got, cents)
	}
}

// TestReedAllNotesOffIsImmediate: the seek/loop silence ramp reaches
// nothing within a couple of engine blocks, click-free but effectively at
// once.
func TestReedAllNotesOffIsImmediate(t *testing.T) {
	const sr = 48000
	v := NewReed(sr, 64)
	v.NoteOn(69, 1)
	renderFrames(v, sr/4, 2048)
	v.AllNotesOff()
	l, r := renderFrames(v, sr/10, 2048)
	m := monoSum(l, r)
	if p := peak(m[sr/50:]); p > 1e-6 {
		t.Errorf("output %.2g still sounding 20 ms after AllNotesOff", p)
	}
}

// TestNewBuiltinDispatch: the built-in factory reads the General MIDI map
// — brass, reeds and pipes sustain; everything else plucks.
func TestNewBuiltinDispatch(t *testing.T) {
	const sr = 48000
	for prog, wantReed := range map[int]bool{
		25: false, // steel guitar
		55: false, // orchestra hit: the last non-wind before brass
		56: true,  // trumpet
		64: true,  // soprano sax
		71: true,  // clarinet
		73: true,  // flute
		79: true,  // ocarina: the last pipe
		80: false, // square lead
	} {
		_, isReed := NewBuiltin(sr, prog).(*reed)
		if isReed != wantReed {
			t.Errorf("program %d: reed = %v, want %v", prog, isReed, wantReed)
		}
	}
}

// TestReedSoundsTheWholeChord: a fretted track pointed at a wind program
// can strum — six NoteOns on one frame — and every note must sound. The
// pool used to be small enough that the fifth and sixth notes stole the
// first two before they rendered a sample.
func TestReedSoundsTheWholeChord(t *testing.T) {
	const sr = 48000
	v := NewReed(sr, 64)
	chord := []int{40, 45, 52, 56, 59, 64} // open E major
	for _, k := range chord {
		v.NoteOn(k, 1)
	}
	l, r := renderFrames(v, sr/2, 2048)
	base := energy(monoSum(l, r))
	// Releasing any one note must audibly change the mix: a note whose
	// NoteOff changes nothing never sounded.
	for _, k := range chord {
		v2 := NewReed(sr, 64)
		for _, kk := range chord {
			v2.NoteOn(kk, 1)
		}
		v2.NoteOff(k)
		l2, r2 := renderFrames(v2, sr/2, 2048)
		if e := energy(monoSum(l2, r2)); math.Abs(e-base)/base < 0.01 {
			t.Errorf("releasing key %d changed the mix by %.4f%%: that note never sounded", k, 100*math.Abs(e-base)/base)
		}
	}
}
