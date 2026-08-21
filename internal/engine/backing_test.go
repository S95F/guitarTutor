package engine

import (
	"math"
	"testing"
)

// rampBacking builds a backing track whose sample values equal their file
// position: left[i] = i, right[i] = i + 0.5. Integer values up to 2^24
// are exact in float32, so at scale 1 output frames must reproduce file
// positions bit-exactly, and linear interpolation at scale 0.5 lands on
// exactly representable halves.
func rampBacking(n int) (l, r []float32) {
	l = make([]float32, n)
	r = make([]float32, n)
	for i := range l {
		l[i] = float32(i)
		r[i] = float32(i) + 0.5
	}
	return l, r
}

// newBackingEngine builds a fixture engine with the synth track muted and
// no metronome, so the output is the backing track alone.
func newBackingEngine(t *testing.T, opts Options) (*Engine, *stubVoice) {
	t.Helper()
	e, v := newFixtureEngine(t, opts)
	e.SetTrackMuted(0, true)
	return e, v
}

// renderCollect drives the engine and returns the full output.
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

// TestBackingExactFramesScale1 plays from tick 0 at scale 1 with a ramp
// backing: output frame k must be exactly file sample k on both channels.
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

// TestBackingSeek seeks to bar 2 (tick 3840 = 2 s = file sample 96000 at
// 120 BPM): the backing must jump exactly with the score position.
func TestBackingSeek(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(200000)
	e.SetBackingTrack(bl, br, 0)
	e.Play()
	renderCollect(e, 1000, 480) // move off tick 0 first
	e.SeekTick(3840)
	l, _ := renderCollect(e, 1000, 480)
	for k := range l {
		if want := float32(96000 + k); l[k] != want {
			t.Fatalf("frame %d after seek = %v, want exactly %v", k, l[k], want)
		}
	}
}

// TestBackingLoopWrap loops bar 2 [3840, 7680): every pass must restart
// the backing exactly at the loop start's score time (file sample 96000),
// with a 96000-frame pass period.
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

// TestBackingHalfSpeed plays at scale 0.5: output frame k reads file
// position k*0.5, linearly interpolated (pitch shifts — the documented
// limitation), so the ramp yields exactly k*0.5.
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

// TestBackingHalfSpeedFromLoopStart combines scale and a mid-score start:
// looping bar 2 at scale 0.5, frame k of each pass reads file position
// 96000 + k*0.5 (the loop start's score time is unscaled; only the
// advance rate is).
func TestBackingHalfSpeedFromLoopStart(t *testing.T) {
	e, _ := newBackingEngine(t, Options{})
	bl, br := rampBacking(400000)
	e.SetBackingTrack(bl, br, 0)
	e.SetTempoScale(0.5)
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.Play()
	const pass = 192000 // 96000 frames at scale 1, doubled at 0.5
	l, _ := renderCollect(e, pass+1000, 480)
	for k := range l {
		if want := float32(96000) + float32(k%pass)*0.5; l[k] != want {
			t.Fatalf("frame %d = %v, want exactly %v", k, l[k], want)
		}
	}
}

// TestBackingSilentDuringCountIn compares an engine with a backing track
// against an identical one without: during the count-in (position frozen)
// the outputs must be identical (backing silent while clicks sound), and
// from the count-in's end output frame 96000+k must differ by exactly
// backing sample k.
func TestBackingSilentDuringCountIn(t *testing.T) {
	eb, _ := newFixtureEngine(t, Options{CountInBeats: 4})
	e0, _ := newFixtureEngine(t, Options{CountInBeats: 4})
	bl, br := rampBacking(200000)
	eb.SetBackingTrack(bl, br, 0)
	eb.Play()
	e0.Play()
	const ci = 4 * 24000 // 4 beats at 120 BPM
	lb, _ := renderCollect(eb, ci+10000, 480)
	l0, _ := renderCollect(e0, ci+10000, 480)
	for k := 0; k < ci; k++ {
		if lb[k] != l0[k] {
			t.Fatalf("frame %d during count-in: %v with backing vs %v without, want identical (backing silent)", k, lb[k], l0[k])
		}
	}
	// After the count-in the difference is the backing ramp. The voices
	// sound in both engines, so subtracting them back out rounds in
	// float32; allow one ulp of the backing's magnitude (the bit-exact
	// contract is covered by the muted-track tests).
	for k := ci; k < len(lb); k++ {
		want := float64(k - ci)
		tol := want/(1<<23) + 1e-6
		if diff := float64(lb[k] - l0[k]); diff < want-tol || diff > want+tol {
			t.Fatalf("frame %d after count-in: backing contribution = %v, want %v within %v", k, diff, want, tol)
		}
	}
}

// TestBackingSilentWhileWaiting freezes at the first user note in wait
// mode: while waiting the backing is silent (no DC hold), voices still
// render, and the release resumes the file exactly where the score froze.
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
	// Voices still render while waiting: the voice's frame clock advanced
	// through the frozen frames (its Render is called for tails).
	if v.frame != 1000 {
		t.Fatalf("voice rendered %d frames during the wait, want 1000", v.frame)
	}
	e.ConfirmWait()
	// Wait mode re-arms at the next user note (tick 480 = frame 12000
	// after the release); render only up to it.
	l2, _ := renderCollect(e, 12000, 480)
	for k := range l2 {
		if want := float32(k); l2[k] != want {
			t.Fatalf("frame %d after release = %v, want exactly %v (file resumes at the frozen score time)", k, l2[k], want)
		}
	}
}

// TestBackingOffsets checks both offset signs at scale 1 from tick 0:
// +0.5 s starts 24000 samples into the file; -0.5 s holds silence for
// 24000 frames and then starts the file's beginning.
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

// TestBackingPastEOFSilent plays a backing shorter than the score: reads
// past its last sample are silent.
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

// TestBackingGainAndClear scales the backing by SetBackingGain and
// removes it with ClearBackingTrack.
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

// TestBackingPauseSilent pauses mid-play: the backing contributes silence
// while paused (no DC hold at the frozen file position) and resumes from
// the frozen score time.
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

// TestMixBackingNonFinitePositionSilent drives the render loop's bounds
// guard directly. Regression: the guard was `p < 0 || p > last`, and every
// comparison involving NaN is false, so a NaN file position fell through
// to j := int(p) — MinInt64 on amd64 — and indexed backL out of range,
// panicking the audio thread ("index out of range
// [-9223372036854775808]"). A position that is not a real number names no
// sample, so every non-finite position must read as silence. scale is
// covered as well as backBase: SetTempoScale's clamp has the same NaN
// blindness, so a NaN can still reach this loop from there.
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
		e.mixBacking(l, r, 0) // must not panic
		e.mu.Unlock()
		for k := range l {
			if l[k] != 0 || r[k] != 0 {
				t.Errorf("%s: frame %d = (%v, %v), want silence for a non-finite file position", tc.name, k, l[k], r[k])
				break
			}
		}
	}
}

// TestBackingNonFiniteOffsetRefused pins the boundary check that keeps a
// non-finite position out of the engine in the first place. Regression:
// `render -backing-offset NaN` reached SetBackingTrack unfiltered, stored
// NaN in backOffset, and made every file position NaN — a panic on the
// audio thread from processFrames. The offset is refused (treated as 0),
// so playback must match an aligned backing exactly; +Inf is refused for
// the same reason, having pinned the position outside the file forever
// and made the backing silently inaudible.
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

// TestBackingNonFiniteGainRefused pins the gain clamp. Regression: the
// clamp was `if g < 0`, false for NaN, so `render -backing-gain NaN`
// stored NaN; mixBacking's `backGain == 0` early-out is false for NaN
// too, so every backing sample became NaN, the whole mix followed, and
// the rendered WAV was eight seconds of full-scale noise (every sample at
// the -1.0 rail). +Inf is the same hazard by multiplication. The engine
// must hold only a gain it can multiply by, so all of these store 0, and
// with the track muted the output must be silence — not NaN, which the
// exact comparison below also rejects.
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

// TestBackingRenderFramesDoesNotAllocate is the steady-state allocation
// contract with a backing track in the mix: still zero.
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
		e.RenderFrames(l, r) // reach steady state
	}
	if allocs := testing.AllocsPerRun(100, func() { e.RenderFrames(l, r) }); allocs != 0 {
		t.Errorf("RenderFrames with a backing track allocates %v times per run, want 0", allocs)
	}
}

// TestBackingNonFiniteSamplesSilenced is the fix-verification regression
// for the backing mixer. Guarding the POSITION and the GAIN still left the
// samples themselves: mixBacking adds `gain * backL[j]` into the output,
// so one NaN in the buffer makes the entire mix non-finite from that frame
// on, and the live path writes float32 straight to the device — a
// full-scale rail in the player's headphones.
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
