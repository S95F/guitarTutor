package engine

import (
	"testing"

	"github.com/S95F/musicTutor/internal/score"
)

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
	ub.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	ub.AddBeat(score.Quarter)
	ub.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3})
	ub.AddBeat(score.Quarter)
	bb := back.AppendBar(4, 4)
	bb.AddBeat(score.Quarter)
	bb.AddBeat(score.Quarter, score.Note{String: 5, Fret: 0})
	bb.AddBeat(score.Quarter)
	bb.AddBeat(score.Quarter, score.Note{String: 5, Fret: 2})
	if err := s.Validate(); err != nil {
		t.Fatalf("duet score does not validate: %v", err)
	}
	return s
}

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
	renderN(e, 43200, 480)

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
	evs, _, ok := e.WaitingOn()
	if !ok || len(evs) != 1 {
		t.Fatalf("WaitingOn = (%v, %v), want one event and true", evs, ok)
	}
	if evs[0].Start != 0 || evs[0].Key != 40 {
		t.Errorf("WaitingOn event = (tick %d, key %d), want (tick 0, key 40)", evs[0].Start, evs[0].Key)
	}
	evs[0].Key = 999
	again, _, _ := e.WaitingOn()
	if again[0].Key != 40 {
		t.Errorf("WaitingOn after mutating a previous result = key %d, want 40 (stable copy)", again[0].Key)
	}
}

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
	renderN(e, 12000, 480)
	if len(v.ons) != 1 || len(taps) != 1 {
		t.Fatalf("got %d NoteOns and %d taps after release, want 1 and 1", len(v.ons), len(taps))
	}
	if g := v.ons[0]; g.frame != 10000 || g.key != 40 {
		t.Errorf("released NoteOn = (frame %d, key %d), want (frame 10000, key 40)", g.frame, g.key)
	}
	if g := taps[0]; g.frame != 10000 || g.key != 40 {
		t.Errorf("released tap = (frame %d, key %d), want (frame 10000, key 40)", g.frame, g.key)
	}

	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the next user note")
	}
	evs, _, ok := e.WaitingOn()
	if !ok || len(evs) != 1 || evs[0].Start != 480 || evs[0].Key != 43 {
		t.Errorf("WaitingOn at second wait = (%v, %v), want the tick-480 key-43 event", evs, ok)
	}
	if len(v.ons) != 1 {
		t.Errorf("second note fired without confirmation: %v", v.ons)
	}
}

func TestWaitChordAllEventsOneFrame(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetWaitMode(true)
	e.SeekTick(7680)
	e.Play()
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the chord")
	}
	evs, _, ok := e.WaitingOn()
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

func TestLoopWaitRearmsEachPass(t *testing.T) {
	e, v := newFixtureEngine(t, Options{})
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the loop's first note")
	}
	e.ConfirmWait()
	renderN(e, 24480, 480)
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 4800 || evs[0].Key != 45 {
		t.Fatalf("WaitingOn at second note = (%v, %v), want the tick-4800 key-45 event", evs, ok)
	}
	e.ConfirmWait()
	renderN(e, 24480, 480)
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 5760 || evs[0].Key != 47 {
		t.Fatalf("WaitingOn at third note = (%v, %v), want the tick-5760 key-47 event", evs, ok)
	}
	if got := e.PassCount(); got != 0 {
		t.Errorf("PassCount before the pass completes = %d, want 0", got)
	}
	e.ConfirmWait()
	renderN(e, 48000, 480)
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
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 3840 || evs[0].Key != 47 {
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
	renderN(e, 12001, 480)
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
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 3840 || evs[0].Key != 47 {
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
	renderN(e, 960, 480)
	if len(v.ons) != 0 {
		t.Fatalf("NoteOns fired while paused: %v", v.ons)
	}
	e.Play()
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false after resuming, want re-armed at the next user note")
	}
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 0 || evs[0].Key != 40 {
		t.Fatalf("WaitingOn after resume = (%v, %v), want the tick-0 key-40 event", evs, ok)
	}
	e.ConfirmWait()
	renderN(e, 480, 480)
	if len(v.ons) != 1 || v.ons[0] != (noteRec{2400, 40}) {
		t.Errorf("NoteOns after post-resume confirm = %v, want [(frame 2400, key 40)]", v.ons)
	}
}

func TestWaitBackingTrackPlaysBetweenWaits(t *testing.T) {
	var reg []*stubVoice
	e := New(waitDuetScore(t), Options{Voices: newStubFactory(&reg)})
	user, back := reg[0], reg[1]
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the user's first note")
	}
	if len(back.ons) != 0 {
		t.Fatalf("backing notes fired while frozen at tick 0: %v", back.ons)
	}
	e.ConfirmWait()
	renderN(e, 48480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the user's second note")
	}
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 1920 || evs[0].Key != 43 {
		t.Fatalf("WaitingOn = (%v, %v), want the tick-1920 key-43 event", evs, ok)
	}
	if len(user.ons) != 1 || user.ons[0] != (noteRec{480, 40}) {
		t.Errorf("user NoteOns = %v, want [(frame 480, key 40)]", user.ons)
	}
	if len(back.ons) != 1 || back.ons[0] != (noteRec{24480, 45}) {
		t.Errorf("backing NoteOns = %v, want [(frame 24480, key 45)]: beat 2 plays without waiting", back.ons)
	}
	e.ConfirmWait()
	renderN(e, 24480, 480)
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

func TestWaitGenerationSeekRewaitSameTick(t *testing.T) {
	e, _ := newFixtureEngine(t, Options{})
	if g := e.WaitGeneration(); g != 0 {
		t.Fatalf("WaitGeneration before any wait = %d, want 0", g)
	}
	e.SetWaitMode(true)
	e.Play()
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false at the first note")
	}
	g1 := e.WaitGeneration()
	if g1 != 1 {
		t.Fatalf("WaitGeneration at the first wait = %d, want 1", g1)
	}
	e.SeekTick(0)
	if e.Waiting() {
		t.Fatal("Waiting = true after SeekTick, want cleared")
	}
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false after the seek, want a new wait at tick 0")
	}
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 0 {
		t.Fatalf("WaitingOn after seek = (%v, %v), want the same tick-0 event", evs, ok)
	}
	if g2 := e.WaitGeneration(); g2 != g1+1 {
		t.Errorf("WaitGeneration after seek re-wait = %d, want %d (a distinct wait)", g2, g1+1)
	}
}

func TestWaitGenerationLoopWrap(t *testing.T) {
	e, _ := newFixtureEngine(t, Options{})
	e.SetLoop(3840, 7680)
	e.SeekTick(3840)
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 480, 480)
	g1 := e.WaitGeneration()
	if g1 != 1 {
		t.Fatalf("WaitGeneration at the loop's first wait = %d, want 1", g1)
	}
	e.ConfirmWait()
	renderN(e, 24480, 480)
	e.ConfirmWait()
	renderN(e, 24480, 480)
	if g := e.WaitGeneration(); g != 3 {
		t.Fatalf("WaitGeneration at the third wait = %d, want 3", g)
	}
	e.ConfirmWait()
	renderN(e, 48000, 480)
	renderN(e, 480, 480)
	if !e.Waiting() {
		t.Fatal("Waiting = false after the wrap, want a new wait at the bar's first note")
	}
	if evs, _, ok := e.WaitingOn(); !ok || len(evs) != 1 || evs[0].Start != 3840 {
		t.Fatalf("WaitingOn after wrap = (%v, %v), want the tick-3840 event again", evs, ok)
	}
	if g := e.WaitGeneration(); g != 4 {
		t.Errorf("WaitGeneration after the wrap = %d, want 4 (the re-wait is a distinct wait)", g)
	}
}

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
		e.RenderFrames(l, r)
	}
	if !e.Waiting() {
		t.Fatal("Waiting = false, want a steady waiting state")
	}
	if allocs := testing.AllocsPerRun(100, func() { e.RenderFrames(l, r) }); allocs != 0 {
		t.Errorf("RenderFrames while waiting allocates %v times per run, want 0", allocs)
	}
}

func TestWaitPositionExactAtFractionalScale(t *testing.T) {
	var reg []*stubVoice
	e := New(waitDuetScore(t), Options{Voices: newStubFactory(&reg)})
	e.SetWaitMode(true)
	e.SetTempoScale(0.95)
	e.Play()

	renderN(e, 4800, 480)
	if !e.Waiting() || e.PosTick() != 0 {
		t.Fatalf("first wait: Waiting=%v PosTick=%d, want waiting at 0", e.Waiting(), e.PosTick())
	}
	e.ConfirmWait()

	renderN(e, 96000, 480)
	if !e.Waiting() {
		t.Fatal("never reached the second wait point")
	}
	if got := e.PosTick(); got != 1920 {
		t.Errorf("PosTick while waiting = %d, want exactly the wait tick 1920", got)
	}
}

func twoUserScore(t *testing.T) *score.Score {
	t.Helper()
	s := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	first := &score.Track{Name: "Lead", Tuning: score.StandardTuning}
	second := &score.Track{Name: "Harmony", Tuning: score.StandardTuning}
	s.Tracks = []*score.Track{first, second}
	fb := first.AppendBar(4, 4)
	fb.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	fb.AddBeat(score.Quarter)
	fb.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3})
	fb.AddBeat(score.Quarter)
	sb := second.AppendBar(4, 4)
	sb.AddBeat(score.Quarter)
	sb.AddBeat(score.Quarter, score.Note{String: 5, Fret: 0})
	sb.AddBeat(score.Quarter)
	sb.AddBeat(score.Quarter)
	if err := s.Validate(); err != nil {
		t.Fatalf("two-user score does not validate: %v", err)
	}
	return s
}

func TestWaitingOnCarriesItsOwnGeneration(t *testing.T) {
	e := New(waitDuetScore(t), Options{SampleRate: 48000, Voices: newStubFactory(new([]*stubVoice))})
	e.SetWaitMode(true)
	e.Play()

	want := []struct {
		tick int64
		key  int
	}{{0, 40}, {1920, 43}}
	var lastGen uint64
	for i, w := range want {
		renderN(e, 96000, 480)
		evs, gen, ok := e.WaitingOn()
		if !ok || len(evs) != 1 {
			t.Fatalf("wait %d: WaitingOn = (%v, %d, %v), want one event", i+1, evs, gen, ok)
		}
		if evs[0].Start != w.tick || evs[0].Key != w.key {
			t.Errorf("wait %d: event = (tick %d, key %d), want (tick %d, key %d)",
				i+1, evs[0].Start, evs[0].Key, w.tick, w.key)
		}
		if gen <= lastGen {
			t.Errorf("wait %d: generation = %d, want > the previous wait's %d", i+1, gen, lastGen)
		}
		if live := e.WaitGeneration(); gen != live {
			t.Errorf("wait %d: WaitingOn reported generation %d, WaitGeneration %d — they must agree", i+1, gen, live)
		}
		lastGen = gen
		e.ConfirmWait()
	}
}

func TestWaitingOnSnapshotIsSingleValued(t *testing.T) {
	e := New(waitDuetScore(t), Options{SampleRate: 48000, Voices: newStubFactory(new([]*stubVoice))})
	e.SetWaitMode(true)
	e.SetLoop(0, 3840)
	e.Play()

	done := make(chan struct{})
	go func() {
		defer close(done)
		l := make([]float32, 256)
		r := make([]float32, 256)
		for i := 0; i < 4000; i++ {
			e.RenderFrames(l, r)
			if e.Waiting() {
				e.ConfirmWait()
			}
		}
	}()

	seen := map[uint64]int64{}
	observed := 0
	for {
		select {
		case <-done:
			if observed == 0 {
				t.Skip("the render loop never overlapped an observation; nothing was exercised")
			}
			return
		default:
		}
		evs, gen, ok := e.WaitingOn()
		if !ok || len(evs) == 0 {
			continue
		}
		observed++
		if prev, dup := seen[gen]; dup && prev != evs[0].Start {
			t.Fatalf("generation %d reported wait tick %d and %d — one generation must mean one wait point",
				gen, prev, evs[0].Start)
		}
		seen[gen] = evs[0].Start
	}
}

func TestWaitTrackRestrictsWaitsToOneTrack(t *testing.T) {
	e := New(twoUserScore(t), Options{SampleRate: 48000, Voices: newStubFactory(new([]*stubVoice))})
	e.SetWaitTrack(0)
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 96000, 480)
	if _, _, ok := e.WaitingOn(); !ok || e.PosTick() != 0 {
		t.Fatalf("first wait: waiting=%v PosTick=%d, want waiting at tick 0", ok, e.PosTick())
	}
	e.ConfirmWait()

	renderN(e, 96000, 480)
	evs, _, ok := e.WaitingOn()
	if !ok {
		t.Fatal("never reached a second wait point")
	}
	if evs[0].Start != 1920 {
		t.Errorf("second wait is on tick %d, want 1920 — tick 960 belongs to track 1, which is not being practiced", evs[0].Start)
	}
}

func TestWaitTrackDefaultWaitsOnEveryUserTrack(t *testing.T) {
	e := New(twoUserScore(t), Options{SampleRate: 48000, Voices: newStubFactory(new([]*stubVoice))})
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 96000, 480)
	e.ConfirmWait()
	renderN(e, 96000, 480)
	evs, _, ok := e.WaitingOn()
	if !ok || evs[0].Start != 960 {
		t.Errorf("second wait = (%v, %v), want the track-1 note at tick 960", evs, ok)
	}
}

func TestWaitTrackOutOfRangeFallsBackToEveryUserTrack(t *testing.T) {
	e := New(twoUserScore(t), Options{SampleRate: 48000, Voices: newStubFactory(new([]*stubVoice))})
	e.SetWaitTrack(7)
	e.SetWaitMode(true)
	e.Play()
	renderN(e, 96000, 480)
	if _, _, ok := e.WaitingOn(); !ok {
		t.Error("an out-of-range wait track stopped wait mode engaging at all")
	}
}

func TestWaitTrackChangeKeepsThePendingRelease(t *testing.T) {
	e := New(twoUserScore(t), Options{SampleRate: 48000, Voices: newStubFactory(new([]*stubVoice))})
	e.SetWaitTrack(1)
	e.SetWaitMode(true)
	e.Play()

	renderN(e, 96000, 480)
	evs, _, ok := e.WaitingOn()
	if !ok || evs[0].Start != 960 {
		t.Fatalf("first wait = (%v, %v), want the track-1 note at tick 960", evs, ok)
	}

	e.ConfirmWait()
	e.SetWaitTrack(0)

	renderN(e, 96000, 480)
	evs, _, ok = e.WaitingOn()
	if !ok {
		t.Fatal("never halted again; the pending release was spent on track 0's wait point instead of track 1's fired note")
	}
	if evs[0].Start != 1920 {
		t.Errorf("second wait is on tick %d, want track 0's note at 1920", evs[0].Start)
	}
}
