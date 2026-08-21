package engine

import (
	"math"
	"testing"
)

func rampBacking(n int) (l, r []float32) {
	l = make([]float32, n)
	r = make([]float32, n)
	for i := range l {
		l[i] = float32(i)
		r[i] = float32(i) + 0.5
	}
	return l, r
}

func newBackingEngine(t *testing.T, opts Options) (*Engine, *stubVoice) {
	t.Helper()
	e, v := newFixtureEngine(t, opts)
	e.SetTrackMuted(0, true)
	return e, v
}

func renderCollect(e *Engine, frames, block int) (l, r []float32) {
	l = make([]float32, frames)
	r = make([]float32, frames)
	for off := 0; off < frames; off += block {
		n := block
		if frames-off < n {
			n = frames - off
		}
		e.RenderFrames(l[off:off+n], r[off:off+n])
	}
	return l, r
}

func TestBackingExactFramesScale1(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(200000)
	e.SetBackingTrack(bl, br, 0)
	e.Play()
	l, r := renderCollect(e, 48000, 480)
	for k := range l {
		if l[k] != float32(k) || r[k] != float32(k)+0.5 {
			t.Fatalf("frame %d = (%v, %v), want exactly (%v, %v)", k, l[k], r[k], float32(k), float32(k)+0.5)
		}
	}
}

func TestBackingSeek(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(200000)
	e.SetBackingTrack(bl, br, 0)
	e.Play()
	renderCollect(e, 1000, 480)
	e.SeekTick(3840)
	l, _ := renderCollect(e, 1000, 480)
	for k := range l {
		if want := float32(96000 + k); l[k] != want {
			t.Fatalf("frame %d after seek = %v, want exactly %v", k, l[k], want)
		}
	}
}

func TestBackingLoopWrap(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(400000)
	e.SetBackingTrack(bl, br, 0)
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.Play()
	const pass = 96000
	l, _ := renderCollect(e, 3*pass, 480)
	if got := e.PassCount(); got != 3 {
		t.Fatalf("PassCount = %d, want 3", got)
	}
	for k := range l {
		if want := float32(pass + k%pass); l[k] != want {
			t.Fatalf("frame %d = %v, want exactly %v (pass restarts at file sample 96000)", k, l[k], want)
		}
	}
}

func TestBackingHalfSpeed(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(200000)
	e.SetBackingTrack(bl, br, 0)
	e.SetTempoScale(0.5)
	e.Play()
	l, _ := renderCollect(e, 48000, 480)
	for k := range l {
		if want := float32(k) * 0.5; l[k] != want {
			t.Fatalf("frame %d at half speed = %v, want exactly %v", k, l[k], want)
		}
	}
}

func TestBackingHalfSpeedFromLoopStart(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(400000)
	e.SetBackingTrack(bl, br, 0)
	e.SetTempoScale(0.5)
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.Play()
	const pass = 192000
	l, _ := renderCollect(e, pass+1000, 480)
	for k := range l {
		if want := float32(96000) + float32(k%pass)*0.5; l[k] != want {
			t.Fatalf("frame %d = %v, want exactly %v", k, l[k], want)
		}
	}
}

func TestBackingSilentDuringCountIn(t *testing.T) {
	eb, _ := newFixtureEngine(t, Options{CountInBeats: 4})
	e0, _ := newFixtureEngine(t, Options{CountInBeats: 4})
	bl, br := rampBacking(200000)
	eb.SetBackingTrack(bl, br, 0)
	eb.Play()
	e0.Play()
	const ci = 4 * 24000
	lb, _ := renderCollect(eb, ci+10000, 480)
	l0, _ := renderCollect(e0, ci+10000, 480)
	for k := 0; k < ci; k++ {
		if lb[k] != l0[k] {
			t.Fatalf("frame %d during count-in: %v with backing vs %v without, want identical (backing silent)", k, lb[k], l0[k])
		}
	}

	for k := ci; k < len(lb); k++ {
		want := float64(k - ci)
		tol := want/(1<<23) + 1e-6
		if diff := float64(lb[k] - l0[k]); diff < want-tol || diff > want+tol {
			t.Fatalf("frame %d after count-in: backing contribution = %v, want %v within %v", k, diff, want, tol)
		}
	}
}

func TestBackingSilentWhileWaiting(t *testing.T) {
	e, v := newBackingEngine(t, Options{})
	bl, br := rampBacking(200000)
	e.SetBackingTrack(bl, br, 0)
	e.SetWaitMode(true)
	e.Play()
	l, _ := renderCollect(e, 1000, 480)
	if !e.Waiting() {
		t.Fatal("engine not waiting at the first user note")
	}
	for k := range l {
		if l[k] != 0 {
			t.Fatalf("frame %d while waiting = %v, want silence from the backing", k, l[k])
		}
	}

	if v.frame != 1000 {
		t.Fatalf("voice rendered %d frames during the wait, want 1000", v.frame)
	}
	e.ConfirmWait()

	l2, _ := renderCollect(e, 12000, 480)
	for k := range l2 {
		if want := float32(k); l2[k] != want {
			t.Fatalf("frame %d after release = %v, want exactly %v (file resumes at the frozen score time)", k, l2[k], want)
		}
	}
}

func TestBackingOffsets(t *testing.T) {
	bl, br := rampBacking(200000)

	e, _ := newBackingEngine(t, Options{})
	e.SetBackingTrack(bl, br, 0.5)
	e.Play()
	l, _ := renderCollect(e, 10000, 480)
	for k := range l {
		if want := float32(24000 + k); l[k] != want {
			t.Fatalf("offset +0.5s: frame %d = %v, want exactly %v", k, l[k], want)
		}
	}

	e2, _ := newBackingEngine(t, Options{})
	e2.SetBackingTrack(bl, br, -0.5)
	e2.Play()
	l2, _ := renderCollect(e2, 30000, 480)
	for k := 0; k < 24000; k++ {
		if l2[k] != 0 {
			t.Fatalf("offset -0.5s: frame %d = %v, want silence before the file starts", k, l2[k])
		}
	}
	for k := 24000; k < len(l2); k++ {
		if want := float32(k - 24000); l2[k] != want {
			t.Fatalf("offset -0.5s: frame %d = %v, want exactly %v", k, l2[k], want)
		}
	}
}

func TestBackingPastEOFSilent(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(100)
	e.SetBackingTrack(bl, br, 0)
	e.Play()
	l, _ := renderCollect(e, 1000, 480)
	for k := 0; k < 100; k++ {
		if l[k] != float32(k) {
			t.Fatalf("frame %d = %v, want exactly %v", k, l[k], float32(k))
		}
	}
	for k := 100; k < len(l); k++ {
		if l[k] != 0 {
			t.Fatalf("frame %d past the file end = %v, want silence", k, l[k])
		}
	}
}

func TestBackingGainAndClear(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(200000)
	e.SetBackingTrack(bl, br, 0)
	e.SetBackingGain(0.5)
	e.Play()
	l, _ := renderCollect(e, 1000, 480)
	for k := range l {
		if want := 0.5 * float32(k); l[k] != want {
			t.Fatalf("frame %d at gain 0.5 = %v, want exactly %v", k, l[k], want)
		}
	}
	e.ClearBackingTrack()
	l2, _ := renderCollect(e, 1000, 480)
	for k := range l2 {
		if l2[k] != 0 {
			t.Fatalf("frame %d after ClearBackingTrack = %v, want silence", k, l2[k])
		}
	}
}

func TestBackingPauseSilent(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(200000)
	e.SetBackingTrack(bl, br, 0)
	e.Play()
	renderCollect(e, 12000, 480)
	e.Pause()
	l, _ := renderCollect(e, 1000, 480)
	for k := range l {
		if l[k] != 0 {
			t.Fatalf("frame %d while paused = %v, want silence", k, l[k])
		}
	}
	e.Play()
	l2, _ := renderCollect(e, 1000, 480)
	for k := range l2 {
		if want := float32(12000 + k); l2[k] != want {
			t.Fatalf("frame %d after resume = %v, want exactly %v", k, l2[k], want)
		}
	}
}

func TestMixBackingNonFinitePositionSilent(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(1000)
	e.SetBackingTrack(bl, br, 0)
	cases := []struct {
		name        string
		base, scale float64
	}{
		{"NaN base", math.NaN(), 1},
		{"+Inf base", math.Inf(1), 1},
		{"-Inf base", math.Inf(-1), 1},
		{"NaN scale", 0, math.NaN()},
		{"+Inf scale", 0, math.Inf(1)},
		{"-Inf scale", 0, math.Inf(-1)},
	}
	for _, tc := range cases {
		l := make([]float32, 16)
		r := make([]float32, 16)
		e.mu.Lock()
		e.backBase, e.scale = tc.base, tc.scale
		e.mixBacking(l, r, 0)
		e.mu.Unlock()
		for k := range l {
			if l[k] != 0 || r[k] != 0 {
				t.Errorf("%s: frame %d = (%v, %v), want silence for a non-finite file position", tc.name, k, l[k], r[k])
				break
			}
		}
	}
}

func TestBackingNonFiniteOffsetRefused(t *testing.T) {
	for _, off := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		e, _ := newBackingEngine(t, Options{})
		bl, br := rampBacking(200000)
		e.SetBackingTrack(bl, br, off)
		e.Play()
		l, r := renderCollect(e, 2000, 480)
		for k := range l {
			if l[k] != float32(k) || r[k] != float32(k)+0.5 {
				t.Fatalf("offset %v: frame %d = (%v, %v), want exactly (%v, %v) — a non-finite offset must be refused, not stored", off, k, l[k], r[k], float32(k), float32(k)+0.5)
			}
		}
	}
}

func TestBackingNonFiniteGainRefused(t *testing.T) {
	for _, g := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		e, _ := newBackingEngine(t, Options{})
		bl, br := rampBacking(200000)
		e.SetBackingTrack(bl, br, 0)
		e.SetBackingGain(g)
		e.Play()
		l, r := renderCollect(e, 2000, 480)
		for k := range l {
			if l[k] != 0 || r[k] != 0 {
				t.Fatalf("gain %v: frame %d = (%v, %v), want silence", g, k, l[k], r[k])
			}
		}
	}
}

func TestBackingRenderFramesDoesNotAllocate(t *testing.T) {
	var reg []*stubVoice
	sc := fixtureScore(t)
	e := New(sc, Options{Voices: newStubFactory(&reg), Metronome: true})
	bl, br := rampBacking(400000)
	e.SetBackingTrack(bl, br, 0.25)
	e.SetBackingGain(0.8)
	e.SetLoop(0, sc.End())
	e.Play()
	l := make([]float32, 512)
	r := make([]float32, 512)
	for i := 0; i < 50; i++ {
		e.RenderFrames(l, r)
	}
	if allocs := testing.AllocsPerRun(100, func() { e.RenderFrames(l, r) }); allocs != 0 {
		t.Errorf("RenderFrames with a backing track allocates %v times per run, want 0", allocs)
	}
}

func TestBackingNonFiniteSamplesSilenced(t *testing.T) {
	e, _ := newFixtureEngine(t, Options{})
	back := make([]float32, 4800)
	for i := range back {
		back[i] = 0.25
	}
	back[0] = float32(math.NaN())
	back[10] = float32(math.Inf(1))
	back[20] = float32(math.Inf(-1))
	e.SetBackingTrack(back, back, 0)
	e.Play()

	l, r := renderCollect(e, 4800, 480)
	for i := range l {
		if f := float64(l[i]); math.IsNaN(f) || math.IsInf(f, 0) {
			t.Fatalf("left[%d] = %v: one bad backing sample poisoned the mix", i, l[i])
		}
		if f := float64(r[i]); math.IsNaN(f) || math.IsInf(f, 0) {
			t.Fatalf("right[%d] = %v", i, r[i])
		}
	}
}
