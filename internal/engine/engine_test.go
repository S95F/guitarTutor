package engine

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/synth"
)

// A noteRec is one recorded NoteOn or NoteOff with the exact frame index
// (frames the voice had rendered when the call arrived) it landed on.
type noteRec struct {
	frame int64
	key   int
}

// stubVoice is a deterministic offline test double: it records every event
// with its exact frame index and renders a signal derived only from its
// frame counter and active-note count, so its output is independent of how
// rendering is chunked. Record slices are preallocated so steady-state
// rendering does not allocate.
type stubVoice struct {
	frame   int64
	ons     []noteRec
	offs    []noteRec
	allOffs []int64
	active  int
}

func newStubFactory(reg *[]*stubVoice) synth.Factory {
	return func(sampleRate, program int) synth.Voice {
		v := &stubVoice{
			ons:     make([]noteRec, 0, 4096),
			offs:    make([]noteRec, 0, 4096),
			allOffs: make([]int64, 0, 256),
		}
		*reg = append(*reg, v)
		return v
	}
}

func (v *stubVoice) NoteOn(key int, velocity float64) {
	v.ons = append(v.ons, noteRec{v.frame, key})
	v.active++
}

func (v *stubVoice) NoteOff(key int) {
	v.offs = append(v.offs, noteRec{v.frame, key})
	if v.active > 0 {
		v.active--
	}
}

func (v *stubVoice) AllNotesOff() {
	v.allOffs = append(v.allOffs, v.frame)
	v.active = 0
}

func (v *stubVoice) Render(left, right []float32) {
	// Like a real voice, contribute nothing when idle — the engine mixes
	// voices even while the transport is stopped (release tails).
	for i := range left {
		if v.active > 0 {
			s := float32(v.active)*0.05 + float32(v.frame%101)*0.0001
			left[i] += s
			right[i] += s
		}
		v.frame++
	}
}

// fixtureScore builds the canonical fixture riff from docs/TEXTFORMAT.md:
// one standard-tuned track, 120 BPM, 4/4.
func fixtureScore(t *testing.T) *score.Score {
	t.Helper()
	s := &score.Score{
		Title:  "Fixture Riff",
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "Guitar", Tuning: score.StandardTuning, Program: 25}
	s.Tracks = []*score.Track{tr}

	b1 := tr.AppendBar(4, 4)
	for _, n := range []score.Note{
		{String: 6, Fret: 0}, {String: 6, Fret: 3}, {String: 5, Fret: 5}, {String: 6, Fret: 0},
		{String: 6, Fret: 3}, {String: 5, Fret: 5}, {String: 6, Fret: 3}, {String: 6, Fret: 0},
	} {
		b1.AddBeat(score.Eighth, n)
	}
	b2 := tr.AppendBar(4, 4)
	b2.AddBeat(score.Quarter, score.Note{String: 5, Fret: 2})
	b2.AddBeat(score.Quarter, score.Note{String: 5, Fret: 0})
	b2.AddBeat(score.Half, score.Note{String: 5, Fret: 2})
	b3 := tr.AppendBar(4, 4)
	b3.AddBeat(score.Half,
		score.Note{String: 6, Fret: 0}, score.Note{String: 5, Fret: 2}, score.Note{String: 4, Fret: 2})
	b3.AddBeat(score.Quarter) // rest
	b3.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3})
	b4 := tr.AppendBar(4, 4)
	b4.AddBeat(score.Half, score.Note{String: 6, Fret: 0})
	b4.AddBeat(score.Half, score.Note{String: 6, Fret: 0, Tied: true})

	if err := s.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	return s
}

// newFixtureEngine builds an engine over the fixture riff with stub voices,
// returning the engine and the single track's voice.
func newFixtureEngine(t *testing.T, opts Options) (*Engine, *stubVoice) {
	t.Helper()
	var reg []*stubVoice
	opts.Voices = newStubFactory(&reg)
	e := New(fixtureScore(t), opts)
	return e, reg[0]
}

// renderN drives the engine through frames total frames in blocks.
func renderN(e *Engine, frames, block int) {
	l := make([]float32, block)
	r := make([]float32, block)
	for done := 0; done < frames; {
		n := block
		if frames-done < n {
			n = frames - done
		}
		e.RenderFrames(l[:n], r[:n])
		done += n
	}
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestNoteOnFramesConstantTempo checks that every NoteOn of the canonical
// riff lands on its exact frame at 48 kHz / 120 BPM (frame = tick * 25),
// allowing at most off-by-one for rounding, and that the transport stops
// at the score end.
func TestNoteOnFramesConstantTempo(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.Play()
	for i := 0; i < 900 && e.Playing(); i++ {
		renderN(e, 480, 480)
	}
	if e.Playing() {
		t.Fatal("engine still playing after rendering the whole score")
	}
	if got := e.PosTick(); got != 15360 {
		t.Errorf("PosTick after score end = %d, want 15360", got)
	}
	want := []noteRec{
		{0, 40}, {12000, 43}, {24000, 50}, {36000, 40},
		{48000, 43}, {60000, 50}, {72000, 43}, {84000, 40},
		{96000, 47}, {120000, 45}, {144000, 47},
		{192000, 40}, {192000, 47}, {192000, 52},
		{264000, 43},
		{288000, 40},
	}
	if len(v.ons) != len(want) {
		t.Fatalf("got %d NoteOns, want %d: %v", len(v.ons), len(want), v.ons)
	}
	for i, w := range want {
		g := v.ons[i]
		if g.key != w.key || abs64(g.frame-w.frame) > 1 {
			t.Errorf("NoteOn %d = (frame %d, key %d), want (frame %d, key %d)",
				i, g.frame, g.key, w.frame, w.key)
		}
	}
}

// TestTempoChangeExactFrames checks event frames on both sides of a
// mid-score tempo change: 120 BPM (25 frames/tick), then 60 BPM
// (50 frames/tick) from bar 2.
func TestTempoChangeExactFrames(t *testing.T) {
	s := &score.Score{
		Tempos: score.TempoMap{
			{Tick: 0, USPerQuarter: score.USPerQuarter(120)},
			{Tick: 3840, USPerQuarter: score.USPerQuarter(60)},
		},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Tuning: score.StandardTuning}
	s.Tracks = []*score.Track{tr}
	for b := 0; b < 2; b++ {
		bar := tr.AppendBar(4, 4)
		for i := 0; i < 4; i++ {
			bar.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
		}
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("score does not validate: %v", err)
	}

	var reg []*stubVoice
	e := New(s, Options{Voices: newStubFactory(&reg)})
	e.Play()
	for i := 0; i < 700 && e.Playing(); i++ {
		renderN(e, 480, 480)
	}
	// Bar 1 at 25 frames/tick, bar 2 at 50 frames/tick from frame 96000.
	want := []int64{0, 24000, 48000, 72000, 96000, 144000, 192000, 240000}
	v := reg[0]
	if len(v.ons) != len(want) {
		t.Fatalf("got %d NoteOns, want %d: %v", len(v.ons), len(want), v.ons)
	}
	for i, w := range want {
		if g := v.ons[i]; g.key != 40 || abs64(g.frame-w) > 1 {
			t.Errorf("NoteOn %d = (frame %d, key %d), want (frame %d, key 40)", i, g.frame, g.key, w)
		}
	}
}

// TestTempoScaleDoublesSpacing checks that half speed exactly doubles
// inter-onset frame distances.
func TestTempoScaleDoublesSpacing(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetTempoScale(0.5)
	if got := e.TempoScale(); got != 0.5 {
		t.Fatalf("TempoScale = %v, want 0.5", got)
	}
	e.Play()
	renderN(e, 200000, 512) // through bar 1 and into bar 2 at 50 frames/tick
	if len(v.ons) < 9 {
		t.Fatalf("got %d NoteOns, want at least 9", len(v.ons))
	}
	for i := 1; i < 9; i++ {
		if d := v.ons[i].frame - v.ons[i-1].frame; d != 24000 {
			t.Errorf("inter-onset %d = %d frames, want exactly 24000", i, d)
		}
	}
}

// TestLoopGapless loops bar 2 [3840, 7680) and checks that the NoteOn
// pattern repeats with an exact 96000-frame period, that the first note of
// every pass is present (the MuseScore bug class), and that PassCount
// increments per wrap.
func TestLoopGapless(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.Play()
	renderN(e, 3*96000, 480)

	if len(v.ons) != 9 {
		t.Fatalf("got %d NoteOns over 3 passes, want 9: %v", len(v.ons), v.ons)
	}
	for pass := 0; pass < 3; pass++ {
		base := int64(pass) * 96000
		want := []noteRec{{base, 47}, {base + 24000, 45}, {base + 48000, 47}}
		for i, w := range want {
			if g := v.ons[pass*3+i]; g != w {
				t.Errorf("pass %d NoteOn %d = (frame %d, key %d), want (frame %d, key %d)",
					pass, i, g.frame, g.key, w.frame, w.key)
			}
		}
	}
	if got := e.PassCount(); got != 3 {
		t.Errorf("PassCount = %d, want 3", got)
	}
	// Ringing notes are silenced at each wrap.
	wantOffs := map[int64]bool{96000: false, 192000: false, 288000: false}
	for _, f := range v.allOffs {
		if _, ok := wantOffs[f]; ok {
			wantOffs[f] = true
		}
	}
	for f, seen := range wantOffs {
		if !seen {
			t.Errorf("no AllNotesOff at loop wrap frame %d (got %v)", f, v.allOffs)
		}
	}
}

// TestRampShrinksPeriod runs the speed trainer over the bar-2 loop: start
// at 0.5, +0.1 per pass toward 1.0. Each pass's frame period must match the
// engine's frames-per-tick conversion at that pass's scale, and the scale
// must never exceed the target.
func TestRampShrinksPeriod(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetTempoScale(0.5)
	e.SetRamp(RampConfig{Enabled: true, Increment: 0.1, Target: 1.0})
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.Play()

	// Replicate the engine's arithmetic exactly: scale per pass and the
	// pass period ceil((B-A) * framesPerTick).
	const passes = 7
	scales := make([]float64, passes)
	s := 0.5
	for i := range scales {
		scales[i] = s
		next := s + 0.1
		if next > 1.0 {
			next = 1.0
		}
		s = next
	}
	period := func(sc float64) int64 {
		fpt := float64(48000) * 500000 / (1e6 * 960 * sc)
		return int64(math.Ceil(3840 * fpt))
	}
	total := 0
	for _, sc := range scales {
		total += int(period(sc))
	}
	renderN(e, total, 512)

	if len(v.ons) != passes*3 {
		t.Fatalf("got %d NoteOns, want %d", len(v.ons), passes*3)
	}
	start := int64(0)
	for k := 0; k < passes; k++ {
		if g := v.ons[k*3]; g.frame != start || g.key != 47 {
			t.Errorf("pass %d first NoteOn = (frame %d, key %d), want (frame %d, key 47)",
				k, g.frame, g.key, start)
		}
		start += period(scales[k])
	}
	if got := e.TempoScale(); got != 1.0 {
		t.Errorf("TempoScale after ramp = %v, want exactly 1.0 (never above target)", got)
	}
	if got := e.PassCount(); got != passes {
		t.Errorf("PassCount = %d, want %d", got, passes)
	}
}

// TestCountIn checks that four count-in beats at 120 BPM delay the first
// NoteOn by exactly 4 * 0.5 s * 48000 frames, that CountingIn reports
// during (not after), and that the position does not advance meanwhile.
func TestCountIn(t *testing.T) {
	e, v := newFixtureEngine(t, Options{CountInBeats: 4})
	e.Play()
	if on, left := e.CountingIn(); !on || left != 4 {
		t.Fatalf("CountingIn after Play = (%v, %d), want (true, 4)", on, left)
	}
	renderN(e, 30000, 480) // one beat is 24000 frames; now inside beat 2
	if on, left := e.CountingIn(); !on || left != 3 {
		t.Errorf("CountingIn mid count-in = (%v, %d), want (true, 3)", on, left)
	}
	if got := e.PosTick(); got != 0 {
		t.Errorf("PosTick during count-in = %d, want 0", got)
	}
	if !e.Playing() {
		t.Error("Playing during count-in = false, want true (count-in included)")
	}
	renderN(e, 78000, 480) // through the count-in end at 96000, into the riff
	if on, left := e.CountingIn(); on || left != 0 {
		t.Errorf("CountingIn after count-in = (%v, %d), want (false, 0)", on, left)
	}
	if len(v.ons) == 0 {
		t.Fatal("no NoteOns after count-in")
	}
	if g := v.ons[0]; g.frame != 96000 || g.key != 40 {
		t.Errorf("first NoteOn = (frame %d, key %d), want (frame 96000, key 40)", g.frame, g.key)
	}
	if got := e.PosTick(); got != 480 {
		t.Errorf("PosTick after 12000 post-count-in frames = %d, want 480", got)
	}
}

// TestCountInEveryPass loops bar 2 with a 2-beat count-in between passes:
// each pass starts one count-in (48000 frames at 120 BPM) after the
// previous pass ends, position frozen during the clicks.
func TestCountInEveryPass(t *testing.T) {
	e, v := newFixtureEngine(t, Options{CountInBeats: 2, CountInEveryPass: true})
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.Play()
	const ci, pass = 2 * 24000, 96000
	renderN(e, 2*(ci+pass), 480)
	if len(v.ons) != 6 {
		t.Fatalf("got %d NoteOns over 2 passes, want 6: %v", len(v.ons), v.ons)
	}
	for k := 0; k < 2; k++ {
		base := int64(k)*(ci+pass) + ci
		want := []noteRec{{base, 47}, {base + 24000, 45}, {base + 48000, 47}}
		for i, w := range want {
			if g := v.ons[k*3+i]; g != w {
				t.Errorf("pass %d NoteOn %d = (frame %d, key %d), want (frame %d, key %d)",
					k, i, g.frame, g.key, w.frame, w.key)
			}
		}
	}
	if got := e.PassCount(); got != 2 {
		t.Errorf("PassCount = %d, want 2", got)
	}
}

// TestPauseResume pauses mid-note: ringing notes are silenced, Read yields
// silence, and Play resumes from the same tick with later events on their
// original frames.
func TestPauseResume(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.Play()
	renderN(e, 30000, 480) // inside note 3 (tick 1200)
	e.Pause()
	if e.Playing() {
		t.Error("Playing after Pause = true, want false")
	}
	if len(v.allOffs) == 0 || v.allOffs[len(v.allOffs)-1] != 30000 {
		t.Errorf("AllNotesOff frames = %v, want silencing at frame 30000", v.allOffs)
	}
	if got := e.PosTick(); got != 1200 {
		t.Errorf("PosTick after Pause = %d, want 1200", got)
	}
	p := make([]byte, 4096)
	n, err := e.Read(p)
	if n != len(p) || err != nil {
		t.Fatalf("Read while paused = (%d, %v), want (%d, nil)", n, err, len(p))
	}
	for i, b := range p {
		if b != 0 {
			t.Fatalf("Read while paused: nonzero byte at %d", i)
		}
	}
	onsBefore := len(v.ons)
	e.Play()
	// The paused Read above still rendered 512 voice frames (voices mix
	// while stopped so release tails ring), so the voice frame counter
	// sits at 30512; note 4 fires 6000 transport frames after resume.
	renderN(e, 12000, 480)
	if got := e.PosTick(); got != 1680 {
		t.Errorf("PosTick after resume = %d, want 1680", got)
	}
	if len(v.ons) != onsBefore+1 {
		t.Fatalf("got %d NoteOns after resume, want %d", len(v.ons), onsBefore+1)
	}
	if g := v.ons[onsBefore]; g.frame != 36512 || g.key != 40 {
		t.Errorf("first NoteOn after resume = (frame %d, key %d), want (frame 36512, key 40)",
			g.frame, g.key)
	}
}

// TestSeekSilencesAndContinues seeks mid-playback: ringing notes get
// AllNotesOff and events continue exactly from the seek target.
func TestSeekSilencesAndContinues(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.Play()
	renderN(e, 30000, 480)
	e.SeekTick(7680) // bar 3: the chord
	if len(v.allOffs) == 0 || v.allOffs[len(v.allOffs)-1] != 30000 {
		t.Errorf("AllNotesOff frames = %v, want silencing at frame 30000", v.allOffs)
	}
	if got := e.PosTick(); got != 7680 {
		t.Errorf("PosTick after seek = %d, want 7680", got)
	}
	onsBefore := len(v.ons)
	renderN(e, 73000, 480)
	// The chord fires immediately at the seek point (voice frame 30000),
	// then the bar-3 quarter note 2880 ticks later (72000 frames).
	want := []noteRec{{30000, 40}, {30000, 47}, {30000, 52}, {102000, 43}}
	if len(v.ons) != onsBefore+len(want) {
		t.Fatalf("got %d NoteOns after seek, want %d: %v", len(v.ons)-onsBefore, len(want), v.ons[onsBefore:])
	}
	for i, w := range want {
		if g := v.ons[onsBefore+i]; g != w {
			t.Errorf("NoteOn %d after seek = (frame %d, key %d), want (frame %d, key %d)",
				i, g.frame, g.key, w.frame, w.key)
		}
	}
}

// TestMuteUnmute checks that a muted track's voice receives no NoteOns
// (with ringing notes silenced at mute time) and that unmuting mid-play
// delivers subsequent events normally.
func TestMuteUnmute(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.Play()
	renderN(e, 6000, 480) // note 1 (frame 0) is ringing
	e.SetTrackMuted(0, true)
	if !e.TrackMuted(0) {
		t.Fatal("TrackMuted = false after mute")
	}
	if len(v.allOffs) == 0 || v.allOffs[len(v.allOffs)-1] != 6000 {
		t.Errorf("AllNotesOff frames = %v, want silencing at mute (frame 6000)", v.allOffs)
	}
	renderN(e, 42000, 480) // through events at 12000..36000, to frame 48000
	if len(v.ons) != 1 {
		t.Fatalf("muted voice got NoteOns: %v", v.ons[1:])
	}
	e.SetTrackMuted(0, false)
	renderN(e, 480, 480) // the event at tick 1920 lands on frame 48000
	if len(v.ons) != 2 {
		t.Fatalf("got %d NoteOns after unmute, want 2", len(v.ons))
	}
	if g := v.ons[1]; g.frame != 48000 || g.key != 43 {
		t.Errorf("first NoteOn after unmute = (frame %d, key %d), want (frame 48000, key 43)",
			g.frame, g.key)
	}
}

// TestRenderFramesDoesNotAllocate drives a steady-state render (playing,
// looping, metronome on) and requires zero allocations per RenderFrames.
func TestRenderFramesDoesNotAllocate(t *testing.T) {
	var reg []*stubVoice
	sc := fixtureScore(t)
	e := New(sc, Options{Voices: newStubFactory(&reg), Metronome: true})
	e.SetLoop(0, sc.End())
	e.Play()
	l := make([]float32, 512)
	r := make([]float32, 512)
	for i := 0; i < 50; i++ {
		e.RenderFrames(l, r) // reach steady state
	}
	if allocs := testing.AllocsPerRun(100, func() { e.RenderFrames(l, r) }); allocs != 0 {
		t.Errorf("RenderFrames allocates %v times per run, want 0", allocs)
	}
}

// TestSetMetronomeMidPlayNoStaleClicks enables the metronome after playing
// past several beats with it off. Regression: nextBeat only advances while
// the click is on, so a stale beat schedule fired every missed beat since
// the segment anchor as one stacked click blast. The track is muted so the
// output is click-only: the block up to the next real beat boundary must be
// silent, and that boundary must sound exactly one click.
func TestSetMetronomeMidPlayNoStaleClicks(t *testing.T) {
	e, _ := newFixtureEngine(t, Options{})
	e.SetTrackMuted(0, true) // click-only output
	e.Play()
	renderN(e, 60000, 480) // 2.5 beats at 24000 frames/beat, metronome off
	e.SetMetronome(true)

	// Frames 60000..71999 precede the next beat boundary (frame 72000):
	// no click may sound in them.
	l := make([]float32, 12000)
	r := make([]float32, 12000)
	e.RenderFrames(l, r)
	for i, s := range l {
		if s != 0 {
			t.Fatalf("stale click: nonzero sample %v at frame %d, before the next beat boundary (72000)",
				s, 60000+i)
		}
	}

	// The beat boundary itself must click — a single click, not a stack.
	l2 := make([]float32, 300)
	r2 := make([]float32, 300)
	e.RenderFrames(l2, r2)
	peak := 0.0
	for _, s := range l2 {
		if a := math.Abs(float64(s)); a > peak {
			peak = a
		}
	}
	if peak < 0.05 {
		t.Fatalf("no click at the beat boundary after SetMetronome(true): peak %v", peak)
	}
	if peak > 0.45 {
		t.Errorf("stacked clicks at the beat boundary: peak %v, want one click (max ~0.4)", peak)
	}
}

// TestSetLoopClampsBeyondScoreEnd sets a loop whose end point lies past the
// score end. Regression: buildSegment never classified the boundary as the
// loop end, so the transport silently stopped at the score end with zero
// passes. The points must clamp to the score and the loop must wrap.
func TestSetLoopClampsBeyondScoreEnd(t *testing.T) {
	var reg []*stubVoice
	sc := fixtureScore(t)
	e := New(sc, Options{Voices: newStubFactory(&reg)})
	e.SetLoop(-960, sc.End()+3840)
	if a, b, on := e.Loop(); !on || a != 0 || b != sc.End() {
		t.Fatalf("Loop after clamping = (%d, %d, %v), want (0, %d, true)", a, b, on, sc.End())
	}
	e.Play()
	renderN(e, 390000, 480) // past the score end at frame 384000
	if !e.Playing() {
		t.Fatal("Playing = false after the clamped loop end: transport stopped instead of looping")
	}
	if got := e.PassCount(); got < 1 {
		t.Errorf("PassCount = %d, want >= 1", got)
	}
	// Pass 2 restarts the riff exactly at the wrap frame.
	v := reg[0]
	if len(v.ons) < 17 {
		t.Fatalf("got %d NoteOns, want at least 17 (16 in pass 1 plus the pass-2 restart)", len(v.ons))
	}
	if g := v.ons[16]; g.frame != 384000 || g.key != 40 {
		t.Errorf("pass-2 first NoteOn = (frame %d, key %d), want (frame 384000, key 40)", g.frame, g.key)
	}
}

// TestTempoScaleDuringCountIn halves the tempo one-and-a-quarter beats into
// a four-beat count-in. Regression: ciFPB kept the old tempo, so the user
// was counted in at one tempo and the piece started at another. The elapsed
// fraction of the current beat carries over (6000/24000 becomes 12000/48000),
// the remaining beats consume 48000 frames each, and the first NoteOn lands
// where the new tempo predicts.
func TestTempoScaleDuringCountIn(t *testing.T) {
	e, v := newFixtureEngine(t, Options{CountInBeats: 4})
	e.Play()
	renderN(e, 30000, 480) // beat 1 (24000 frames) + 6000 into beat 2
	if on, left := e.CountingIn(); !on || left != 3 {
		t.Fatalf("CountingIn before rescale = (%v, %d), want (true, 3)", on, left)
	}
	e.SetTempoScale(0.5) // beats are now 48000 frames; 12000 of beat 2 elapsed
	renderN(e, 35999, 480)
	if on, left := e.CountingIn(); !on || left != 3 {
		t.Errorf("CountingIn one frame before rescaled beat 2 ends = (%v, %d), want (true, 3)", on, left)
	}
	renderN(e, 1, 1) // beat 2 ends exactly 36000 frames after the rescale
	if on, left := e.CountingIn(); !on || left != 2 {
		t.Errorf("CountingIn at rescaled beat 2 end = (%v, %d), want (true, 2)", on, left)
	}
	if got := e.PosTick(); got != 0 {
		t.Errorf("PosTick during count-in = %d, want 0", got)
	}
	renderN(e, 96000, 480) // beats 3 and 4 at 48000 frames each
	if on, left := e.CountingIn(); on || left != 0 {
		t.Errorf("CountingIn after count-in = (%v, %d), want (false, 0)", on, left)
	}
	renderN(e, 24001, 480)
	if len(v.ons) < 2 {
		t.Fatalf("got %d NoteOns after count-in, want at least 2", len(v.ons))
	}
	if g := v.ons[0]; g.frame != 162000 || g.key != 40 {
		t.Errorf("first NoteOn = (frame %d, key %d), want (frame 162000, key 40)", g.frame, g.key)
	}
	if g := v.ons[1]; g.frame != 186000 || g.key != 43 {
		t.Errorf("second NoteOn = (frame %d, key %d), want (frame 186000, key 43): tick spacing must use the new tempo", g.frame, g.key)
	}
}

// TestReadMatchesRenderFrames renders the same score twice — once through
// RenderFrames, once through Read with a non-multiple-of-8 byte length —
// and requires bit-identical sample streams.
func TestReadMatchesRenderFrames(t *testing.T) {
	const frames = 60000
	var regA, regB []*stubVoice
	ea := New(fixtureScore(t), Options{Voices: newStubFactory(&regA), Metronome: true})
	eb := New(fixtureScore(t), Options{Voices: newStubFactory(&regB), Metronome: true})
	ea.Play()
	eb.Play()

	la := make([]float32, frames)
	ra := make([]float32, frames)
	for off := 0; off < frames; off += 480 {
		ea.RenderFrames(la[off:off+480], ra[off:off+480])
	}

	buf := make([]byte, 0, frames*8+1024)
	chunk := make([]byte, 999) // never frame-aligned: exercises the remainder path
	for len(buf) < frames*8 {
		n, err := eb.Read(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("Read = (%d, %v), want (%d, nil)", n, err, len(chunk))
		}
		buf = append(buf, chunk...)
	}

	for i := 0; i < frames; i++ {
		gl := math.Float32frombits(binary.LittleEndian.Uint32(buf[i*8:]))
		gr := math.Float32frombits(binary.LittleEndian.Uint32(buf[i*8+4:]))
		if gl != la[i] || gr != ra[i] {
			t.Fatalf("frame %d: Read = (%v, %v), RenderFrames = (%v, %v)", i, gl, gr, la[i], ra[i])
		}
	}
}
