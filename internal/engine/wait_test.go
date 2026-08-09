package engine

import (
	"testing"

	"github.com/S95F/guitarTutor/internal/score"
)

// waitDuetScore builds a two-track score for wait-mode tests: a user track
// with quarter notes on beats 1 and 3, and a backing track with quarter
// notes on beats 2 and 4, so backing events fall strictly between wait
// points. 120 BPM, 4/4, one bar.
func waitDuetScore(t *testing.T) *score.Score {
	t.Helper()
	s := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	user := &score.Track{Name: "Lead", Tuning: score.StandardTuning}
	back := &score.Track{Name: "Rhythm", Tuning: score.StandardTuning, Role: score.RoleBacking}
	s.Tracks = []*score.Track{user, back}
	ub := user.AppendBar(4, 4)
	ub.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0}) // tick 0, key 40
	ub.AddBeat(score.Quarter)                                 // rest
	ub.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3}) // tick 1920, key 43
	ub.AddBeat(score.Quarter)                                 // rest
	bb := back.AppendBar(4, 4)
	bb.AddBeat(score.Quarter)                                 // rest
	bb.AddBeat(score.Quarter, score.Note{String: 5, Fret: 0}) // tick 960, key 45
	bb.AddBeat(score.Quarter)                                 // rest
	bb.AddBeat(score.Quarter, score.Note{String: 5, Fret: 2}) // tick 2880, key 47
	if err := s.Validate(); err != nil {
		t.Fatalf("duet score does not validate: %v", err)
	}
	return s
}

// TestWaitModeHaltsAtFirstNote plays the fixture riff with wait mode on:
// the output is pure silence (no note ever fired, no tails to ring), the
// position freezes at tick 0, Waiting reports true, and WaitingOn returns
// a caller-owned copy of the tick-0 event.
func TestWaitModeHaltsAtFirstNote(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetWaitMode(true)
	e.Play()

	l := make([]float32, 4800)
	r := make([]float32, 4800)
	e.RenderFrames(l, r)
	for i, s := range l {
		if s != 0 {
			t.Fatalf("nonzero sample %v at frame %d while waiting on the first note", s, i)
		}
	}
	renderN(e, 43200, 480) // 48000 frames total, all waiting

	if !e.Waiting() {
		t.Fatal("Waiting = false, want true at the first user note")
	}
	if !e.Playing() {
		t.Error("Playing while waiting = false, want true (transport is running, position frozen)")
	}
	if got := e.PosTick(); got != 0 {
		t.Errorf("PosTick while waiting = %d, want 0", got)
	}
	if len(v.ons) != 0 {
		t.Errorf("NoteOns fired while waiting: %v", v.ons)
	}
	if tf := e.TotalFrames(); tf != 48000 {
		t.Errorf("TotalFrames = %d, want 48000 (frames keep flowing while waiting)", tf)
	}
	evs, ok := e.WaitingOn()
	if !ok || len(evs) != 1 {
		t.Fatalf("WaitingOn = (%v, %v), want one event and true", evs, ok)
	}
	if evs[0].Start != 0 || evs[0].Key != 40 {
		t.Errorf("WaitingOn event = (tick %d, key %d), want (tick 0, key 40)", evs[0].Start, evs[0].Key)
	}
	evs[0].Key = 999 // the returned slice is a copy: mutating it is harmless
	again, _ := e.WaitingOn()
	if again[0].Key != 40 {
		t.Errorf("WaitingOn after mutating a previous result = key %d, want 40 (stable copy)", again[0].Key)
	}
}

// TestConfirmWaitFiresOnReleaseFrame confirms a wait mid-stream: the held
// NoteOn fires exactly on the release frame (event tap and stub voice
// agree), the position resumes, and the next user note becomes the next
// wait point.
func TestConfirmWaitFiresOnReleaseFrame(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	type tapRec struct {
		key   int
		frame int64
	}
	var taps []tapRec
	e.SetEventTap(func(ev score.NoteEvent, f int64) {
		taps = append(taps, tapRec{ev.Key, f})
	})
	e.SetWaitMode(true)
	e.Play()
	renderN(e, 10000, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false before ConfirmWait")
	}

	e.ConfirmWait()
	if e.Waiting() {
		t.Fatal("Waiting = true immediately after ConfirmWait")
	}
	renderN(e, 12000, 480) // release frame plus the run up to the next note
	if len(v.ons) != 1 || len(taps) != 1 {
		t.Fatalf("got %d NoteOns and %d taps after release, want 1 and 1", len(v.ons), len(taps))
	}
	if g := v.ons[0]; g.frame != 10000 || g.key != 40 {
		t.Errorf("released NoteOn = (frame %d, key %d), want (frame 10000, key 40)", g.frame, g.key)
	}
	if g := taps[0]; g.frame != 10000 || g.key != 40 {
		t.Errorf("released tap = (frame %d, key %d), want (frame 10000, key 40)", g.frame, g.key)
	}

	// The next user note (tick 480, 12000 frames after release) waits anew.
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the next user note")
	}
	evs, ok := e.WaitingOn()
	if !ok || len(evs) != 1 || evs[0].Start != 480 || evs[0].Key != 43 {
		t.Errorf("WaitingOn at second wait = (%v, %v), want the tick-480 key-43 event", evs, ok)
	}
	if len(v.ons) != 1 {
		t.Errorf("second note fired without confirmation: %v", v.ons)
	}
}

// TestWaitChordAllEventsOneFrame waits on the bar-3 chord: WaitingOn
// returns all three events, and ConfirmWait fires all three on one frame.
func TestWaitChordAllEventsOneFrame(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetWaitMode(true)
	e.SeekTick(7680) // bar 3: the three-note chord
	e.Play()
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the chord")
	}
	evs, ok := e.WaitingOn()
	if !ok || len(evs) != 3 {
		t.Fatalf("WaitingOn at chord = %d events (%v), want 3", len(evs), ok)
	}
	wantKeys := []int{40, 47, 52}
	for i, w := range wantKeys {
		if evs[i].Start != 7680 || evs[i].Key != w {
			t.Errorf("chord event %d = (tick %d, key %d), want (tick 7680, key %d)",
				i, evs[i].Start, evs[i].Key, w)
		}
	}
	e.ConfirmWait()
	renderN(e, 480, 480)
	if len(v.ons) != 3 {
		t.Fatalf("got %d NoteOns after chord release, want 3: %v", len(v.ons), v.ons)
	}
	for i, w := range wantKeys {
		if g := v.ons[i]; g.frame != 480 || g.key != w {
			t.Errorf("chord NoteOn %d = (frame %d, key %d), want (frame 480, key %d)",
				i, g.frame, g.key, w)
		}
	}
}

// TestLoopWaitRearmsEachPass loops bar 2 with wait mode on, confirming
// through the bar's three notes: after the wrap it waits again at the
// bar's first note, and PassCount increments only when the pass completes.
func TestLoopWaitRearmsEachPass(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 480, 480) // waiting at tick 3840, 480 frames of tails
	if !e.Waiting() {
		t.Fatal("Waiting = false at the loop's first note")
	}
	e.ConfirmWait()
	renderN(e, 24480, 480) // fires at 480, runs 24000 frames, waits at tick 4800
	if evs, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 4800 || evs[0].Key != 45 {
		t.Fatalf("WaitingOn at second note = (%v, %v), want the tick-4800 key-45 event", evs, ok)
	}
	e.ConfirmWait()
	renderN(e, 24480, 480) // fires at 24960, waits at tick 5760
	if evs, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 5760 || evs[0].Key != 47 {
		t.Fatalf("WaitingOn at third note = (%v, %v), want the tick-5760 key-47 event", evs, ok)
	}
	if got := e.PassCount(); got != 0 {
		t.Errorf("PassCount before the pass completes = %d, want 0", got)
	}
	e.ConfirmWait()
	renderN(e, 48000, 480) // fires at 49440, half note runs to the loop end
	if got := e.PassCount(); got != 1 {
		t.Errorf("PassCount after the wrap = %d, want 1", got)
	}
	if e.Waiting() {
		t.Error("Waiting = true exactly at the wrap, want false until the next note's frame is reached")
	}
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false after the wrap, want a new wait at the bar's first note")
	}
	if evs, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 3840 || evs[0].Key != 47 {
		t.Errorf("WaitingOn after wrap = (%v, %v), want the tick-3840 key-47 event", evs, ok)
	}

	want := []noteRec{{480, 47}, {24960, 45}, {49440, 47}}
	if len(v.ons) != len(want) {
		t.Fatalf("got %d NoteOns, want %d: %v", len(v.ons), len(want), v.ons)
	}
	for i, w := range want {
		if g := v.ons[i]; g != w {
			t.Errorf("NoteOn %d = (frame %d, key %d), want (frame %d, key %d)",
				i, g.frame, g.key, w.frame, w.key)
		}
	}
}

// TestSetWaitModeOffMidWaitReleases disables wait mode during a wait: the
// held note fires on the next rendered frame and playback continues with
// no further waits.
func TestSetWaitModeOffMidWaitReleases(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetWaitMode(true)
	e.Play()
	renderN(e, 5000, 500)
	if !e.Waiting() {
		t.Fatal("Waiting = false before disabling wait mode")
	}
	e.SetWaitMode(false)
	if e.Waiting() {
		t.Fatal("Waiting = true after SetWaitMode(false)")
	}
	renderN(e, 12001, 480) // release frame, then through the second note
	want := []noteRec{{5000, 40}, {17000, 43}}
	if len(v.ons) != len(want) {
		t.Fatalf("got %d NoteOns, want %d: %v", len(v.ons), len(want), v.ons)
	}
	for i, w := range want {
		if g := v.ons[i]; g != w {
			t.Errorf("NoteOn %d = (frame %d, key %d), want (frame %d, key %d)",
				i, g.frame, g.key, w.frame, w.key)
		}
	}
}

// TestSeekClearsWait seeks during a wait: the held note is dropped without
// firing, and wait mode re-arms at the first user note at the seek target.
func TestSeekClearsWait(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetWaitMode(true)
	e.Play()
	renderN(e, 960, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false before the seek")
	}
	e.SeekTick(3840)
	if e.Waiting() {
		t.Fatal("Waiting = true after SeekTick, want cleared")
	}
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false after seeking to a user note, want re-armed")
	}
	if evs, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 3840 || evs[0].Key != 47 {
		t.Fatalf("WaitingOn after seek = (%v, %v), want the tick-3840 key-47 event", evs, ok)
	}
	if len(v.ons) != 0 {
		t.Fatalf("the pre-seek held note fired: %v", v.ons)
	}
	e.ConfirmWait()
	renderN(e, 480, 480)
	if len(v.ons) != 1 || v.ons[0] != (noteRec{1440, 47}) {
		t.Errorf("NoteOns after post-seek confirm = %v, want [(frame 1440, key 47)]", v.ons)
	}
}

// TestPauseClearsWaitAndPlayRearms pauses during a wait: the wait clears
// without firing, paused rendering is silent, and Play re-arms the wait at
// the same (still next) user note.
func TestPauseClearsWaitAndPlayRearms(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetWaitMode(true)
	e.Play()
	renderN(e, 960, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false before Pause")
	}
	e.Pause()
	if e.Waiting() {
		t.Fatal("Waiting = true after Pause, want cleared")
	}
	renderN(e, 960, 480) // paused: silence, no events
	if len(v.ons) != 0 {
		t.Fatalf("NoteOns fired while paused: %v", v.ons)
	}
	e.Play()
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false after resuming, want re-armed at the next user note")
	}
	if evs, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 0 || evs[0].Key != 40 {
		t.Fatalf("WaitingOn after resume = (%v, %v), want the tick-0 key-40 event", evs, ok)
	}
	e.ConfirmWait()
	renderN(e, 480, 480)
	if len(v.ons) != 1 || v.ons[0] != (noteRec{2400, 40}) {
		t.Errorf("NoteOns after post-resume confirm = %v, want [(frame 2400, key 40)]", v.ons)
	}
}

// TestWaitBackingTrackPlaysBetweenWaits runs the duet score: backing-track
// notes never create wait points and play normally while the position
// moves between waits, but a frozen position holds them back too.
func TestWaitBackingTrackPlaysBetweenWaits(t *testing.T) {
	var reg []*stubVoice
	e := New(waitDuetScore(t), Options{Voices: newStubFactory(&reg)})
	user, back := reg[0], reg[1]
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 480, 480) // waiting at the user's tick-0 note
	if !e.Waiting() {
		t.Fatal("Waiting = false at the user's first note")
	}
	if len(back.ons) != 0 {
		t.Fatalf("backing notes fired while frozen at tick 0: %v", back.ons)
	}
	e.ConfirmWait()
	renderN(e, 48480, 480) // user note at 480, backing at 24480, wait at tick 1920
	if !e.Waiting() {
		t.Fatal("Waiting = false at the user's second note")
	}
	if evs, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 1920 || evs[0].Key != 43 {
		t.Fatalf("WaitingOn = (%v, %v), want the tick-1920 key-43 event", evs, ok)
	}
	if len(user.ons) != 1 || user.ons[0] != (noteRec{480, 40}) {
		t.Errorf("user NoteOns = %v, want [(frame 480, key 40)]", user.ons)
	}
	if len(back.ons) != 1 || back.ons[0] != (noteRec{24480, 45}) {
		t.Errorf("backing NoteOns = %v, want [(frame 24480, key 45)]: beat 2 plays without waiting", back.ons)
	}
	e.ConfirmWait()
	renderN(e, 24480, 480) // user note at 48960, backing beat 4 at 72960... not yet
	if len(user.ons) != 2 || user.ons[1] != (noteRec{48960, 43}) {
		t.Errorf("user NoteOns = %v, want the release at frame 48960", user.ons)
	}
	renderN(e, 24000, 480)
	if len(back.ons) != 2 || back.ons[1] != (noteRec{72960, 47}) {
		t.Errorf("backing NoteOns = %v, want beat 4 at frame 72960", back.ons)
	}
	if e.Waiting() {
		t.Error("Waiting = true after the last user note, want false")
	}
}

// TestWaitMutedUserTrackStillWaits mutes the user track: wait mode is
// about the musical part, so the wait still engages, and ConfirmWait fires
// the tap (the schedule) while the muted voice stays silent.
func TestWaitMutedUserTrackStillWaits(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	var taps []noteRec
	e.SetEventTap(func(ev score.NoteEvent, f int64) {
		taps = append(taps, noteRec{f, ev.Key})
	})
	e.SetTrackMuted(0, true)
	e.SetWaitMode(true)
	e.Play()
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false with the user track muted, want true (waits follow the part, not audibility)")
	}
	e.ConfirmWait()
	renderN(e, 480, 480)
	if len(taps) != 1 || taps[0] != (noteRec{480, 40}) {
		t.Errorf("taps after muted release = %v, want [(frame 480, key 40)]", taps)
	}
	if len(v.ons) != 0 {
		t.Errorf("muted voice got NoteOns: %v", v.ons)
	}
}

// TestWaitingRenderDoesNotAllocate holds the engine in a waiting steady
// state (wait mode on, loop and metronome armed) and requires zero
// allocations per RenderFrames — the same bar the playing path clears.
func TestWaitingRenderDoesNotAllocate(t *testing.T) {
	var reg []*stubVoice
	sc := fixtureScore(t)
	e := New(sc, Options{Voices: newStubFactory(&reg), Metronome: true})
	e.SetLoop(0, sc.End())
	e.SetWaitMode(true)
	e.Play()
	l := make([]float32, 512)
	r := make([]float32, 512)
	for i := 0; i < 50; i++ {
		e.RenderFrames(l, r) // reach the waiting steady state
	}
	if !e.Waiting() {
		t.Fatal("Waiting = false, want a steady waiting state")
	}
	if allocs := testing.AllocsPerRun(100, func() { e.RenderFrames(l, r) }); allocs != 0 {
		t.Errorf("RenderFrames while waiting allocates %v times per run, want 0", allocs)
	}
}
