package synth

import (
	"math"
	"testing"
)

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

			got := estimateFundamental(t, m[sr/4:], sr)
			if cents := math.Abs(centsBetween(tt.want, got)); cents > 3 {
				t.Errorf("key %d: %.2f Hz, want %.2f (off %.1f cents)", tt.key, got, tt.want, cents)
			}
		})
	}
}

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

func TestReedReleaseIsFast(t *testing.T) {
	const sr = 48000
	v := NewReed(sr, 64)
	v.NoteOn(69, 1)
	l, r := renderFrames(v, sr/2, 2048)
	held := rmsOf(monoSum(l, r)[sr/4:])
	v.NoteOff(69)
	l, r = renderFrames(v, sr/2, 2048)
	m := monoSum(l, r)
	tail := rmsOf(m[sr/4 : sr/4+sr/10])
	if held <= 0 {
		t.Fatal("no held output")
	}
	if db := 20 * math.Log10((tail+1e-12)/held); db > -60 {
		t.Errorf("release is %.1f dB down after 250 ms, want at least -60", db)
	}
}

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

func TestReedSlurDoesNotReattack(t *testing.T) {
	const sr = 48000
	r := NewReed(sr, 64).(*reed)
	r.NoteOn(69, 1)
	l, rr := renderFrames(r, sr/2, 2048)
	steady := rmsOf(monoSum(l, rr)[sr/4:])

	r.NoteOnSpec(NoteSpec{Key: 71, Velocity: 1, Attack: AttackLegato, From: 69})
	l, rr = renderFrames(r, sr/2, 2048)
	m := monoSum(l, rr)

	dip := rmsOf(m[:sr/20])
	if dip < steady/2 {
		t.Errorf("slur dipped to %.4f RMS (steady %.4f): that is a re-attack", dip, steady)
	}
	got := estimateFundamental(t, m[sr/8:], sr)
	if cents := math.Abs(centsBetween(493.88, got)); cents > 3 {
		t.Errorf("after the slur: %.2f Hz, want B4 (off %.1f cents)", got, cents)
	}
}

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

func TestNewBuiltinDispatch(t *testing.T) {
	const sr = 48000
	for prog, wantReed := range map[int]bool{
		25: false,
		55: false,
		56: true,
		64: true,
		71: true,
		73: true,
		79: true,
		80: false,
	} {
		_, isReed := NewBuiltin(sr, prog).(*reed)
		if isReed != wantReed {
			t.Errorf("program %d: reed = %v, want %v", prog, isReed, wantReed)
		}
	}
}

func TestReedSoundsTheWholeChord(t *testing.T) {
	const sr = 48000
	v := NewReed(sr, 64)
	chord := []int{40, 45, 52, 56, 59, 64}
	for _, k := range chord {
		v.NoteOn(k, 1)
	}
	l, r := renderFrames(v, sr/2, 2048)
	base := energy(monoSum(l, r))

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
