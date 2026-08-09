package ui

// Windowing is untestable headlessly, so these tests drive the extracted
// input logic (loopSetA/loopSetB, barAt) directly against a real engine.

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/practice"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/synth"
)

// newApp builds an App over a validated score whose single track holds the
// given bars.
func newApp(t *testing.T, bars int) *App {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "guitar", Tuning: score.StandardTuning}
	sc.Tracks = append(sc.Tracks, tr)
	for i := 0; i < bars; i++ {
		b := tr.AppendBar(4, 4)
		b.AddBeat(score.Whole, score.Note{String: 6, Fret: 0})
	}
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	eng := engine.New(sc, engine.Options{Voices: synth.NewPluck})
	return New(eng, sc, 0)
}

// newAppTracks builds an App over a score with n identically-shaped
// tracks, for the mute/solo tests.
func newAppTracks(t *testing.T, tracks, bars int) *App {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	for ti := 0; ti < tracks; ti++ {
		tr := &score.Track{Name: fmt.Sprintf("track%d", ti+1), Tuning: score.StandardTuning}
		sc.Tracks = append(sc.Tracks, tr)
		for i := 0; i < bars; i++ {
			b := tr.AppendBar(4, 4)
			b.AddBeat(score.Whole, score.Note{String: 6, Fret: 0})
		}
	}
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	eng := engine.New(sc, engine.Options{Voices: synth.NewPluck})
	return New(eng, sc, 0)
}

// TestLoopKeysEmptyTrack is the regression test for finding A3: with a
// displayed track that has zero bars, barAt returns -1 and the A/B loop
// handlers used to index bars[-1] and panic.
func TestLoopKeysEmptyTrack(t *testing.T) {
	a := newApp(t, 0)
	if i := a.barAt(0); i != -1 {
		t.Fatalf("barAt(0) on a bar-less track = %d, want -1", i)
	}
	a.loopSetA() // panicked before the i >= 0 guard
	a.loopSetB()
	if _, _, on := a.eng.Loop(); on {
		t.Fatal("loop enabled on a track with no bars")
	}
}

// result builds a NoteResult keyed at (start, str) with verdict v.
func result(start int64, str int, v practice.Verdict) practice.NoteResult {
	return practice.NoteResult{Event: score.NoteEvent{Start: start, String: str}, Verdict: v}
}

// TestNilSafety: with no feeds set, the state merge and Update run clean
// and the app reports Phase 1 behavior — not live, no verdicts, W inert.
func TestNilSafety(t *testing.T) {
	a := newApp(t, 1)
	a.syncLive()
	a.syncLive()
	if err := a.Update(); err != nil {
		t.Fatalf("Update with no feeds: %v", err)
	}
	if a.live {
		t.Error("live without a status fn")
	}
	if a.waitCtl {
		t.Error("wait control enabled by default")
	}
	if len(a.verdicts) != 0 {
		t.Errorf("verdicts with no results: %v", a.verdicts)
	}
	if a.tunerSounding {
		t.Error("tuner sounding with no feed")
	}
	if st := (practice.Stats{}); a.stats != st {
		t.Errorf("stats with no results: %+v", a.stats)
	}
}

// TestVerdictKeying: results key tab notes by (Event.Start, Event.String),
// so two strings sounding at the same tick map independently, and a
// re-judgement of the same key (loop pass) replaces the old verdict while
// still accumulating stats.
func TestVerdictKeying(t *testing.T) {
	a := newApp(t, 1)
	a.OfferResults([]practice.NoteResult{
		result(960, 6, practice.VerdictHit),
		result(960, 5, practice.VerdictMiss), // same tick, other string
	})
	a.syncLive()
	if v, ok := a.verdicts[noteKey{960, 6}]; !ok || v != practice.VerdictHit {
		t.Errorf("verdict(960, string 6) = %v, %v; want Hit", v, ok)
	}
	if v, ok := a.verdicts[noteKey{960, 5}]; !ok || v != practice.VerdictMiss {
		t.Errorf("verdict(960, string 5) = %v, %v; want Miss", v, ok)
	}

	// Second pass re-judges string 5: latest verdict wins.
	a.OfferResults([]practice.NoteResult{result(960, 5, practice.VerdictClose)})
	a.syncLive()
	if v := a.verdicts[noteKey{960, 5}]; v != practice.VerdictClose {
		t.Errorf("re-judged verdict = %v, want Close", v)
	}
	if v := a.verdicts[noteKey{960, 6}]; v != practice.VerdictHit {
		t.Errorf("untouched verdict = %v, want Hit", v)
	}
	want := practice.Stats{Hit: 1, Close: 1, Miss: 1}
	if a.stats != want {
		t.Errorf("stats = %+v, want %+v", a.stats, want)
	}
}

// TestVerdictNotPaintedAheadOfPlayhead is the regression test for the
// loop-pass staleness bug: verdicts are keyed by tick and never cleared,
// so the tab used to paint pass 1's verdict on a note pass 2 had not
// reached yet — an upcoming note rendering green before it was played.
func TestVerdictNotPaintedAheadOfPlayhead(t *testing.T) {
	a := newApp(t, 2)
	a.OfferResults([]practice.NoteResult{result(960, 6, practice.VerdictHit)})
	a.syncLive()

	for _, c := range []struct {
		name string
		pos  int64
		want bool
	}{
		{"playhead at the loop start, note still ahead", 0, false},
		{"playhead one tick short of the note", 959, false},
		{"playhead on the note", 960, true},
		{"playhead past the note", 2000, true},
	} {
		v, ok := a.verdictAt(960, 6, c.pos)
		if ok != c.want {
			t.Errorf("%s: verdictAt painted = %v, want %v", c.name, ok, c.want)
		}
		if ok && v != practice.VerdictHit {
			t.Errorf("%s: verdict = %v, want Hit", c.name, v)
		}
	}

	// A note that was never judged stays unpainted wherever the playhead is.
	if _, ok := a.verdictAt(1920, 6, 5000); ok {
		t.Error("verdict painted for a note that was never judged")
	}
}

// TestVerdictSurvivesLoopWrapUntilRejudged: wrapping to the top of a loop
// must not paint the previous pass's verdicts on the notes ahead, but the
// note the playhead has already passed keeps its verdict until this pass's
// own result replaces it. Results arrive seconds late, so the previous
// pass's verdicts are still landing after the wrap.
func TestVerdictSurvivesLoopWrapUntilRejudged(t *testing.T) {
	a := newApp(t, 2)
	const first, second = int64(0), int64(3840) // bar 1 and bar 2 downbeats

	// Pass 1 judges both notes.
	a.OfferResults([]practice.NoteResult{
		result(first, 6, practice.VerdictHit),
		result(second, 6, practice.VerdictMiss),
	})
	a.syncLive()

	// Pass 2 has wrapped and is sitting on the first note. The second
	// note is ahead: its pass-1 miss must not show.
	if _, ok := a.verdictAt(second, 6, first); ok {
		t.Error("pass 1 verdict painted on a note pass 2 has not reached")
	}
	if v, ok := a.verdictAt(first, 6, first); !ok || v != practice.VerdictHit {
		t.Errorf("verdict under the playhead = %v, %v; want Hit, true", v, ok)
	}

	// A late pass-1 result landing after the wrap is still gated.
	a.OfferResults([]practice.NoteResult{result(second, 6, practice.VerdictMiss)})
	a.syncLive()
	if _, ok := a.verdictAt(second, 6, first); ok {
		t.Error("late pass 1 verdict painted on a note ahead of the playhead")
	}

	// Once the playhead reaches it and this pass re-judges it, the new
	// verdict shows.
	a.OfferResults([]practice.NoteResult{result(second, 6, practice.VerdictHit)})
	a.syncLive()
	if v, ok := a.verdictAt(second, 6, second); !ok || v != practice.VerdictHit {
		t.Errorf("re-judged verdict = %v, %v; want Hit, true", v, ok)
	}
}

// TestTunerFeed: OfferTuner publishes latest-wins tuner state.
func TestTunerFeed(t *testing.T) {
	a := newApp(t, 1)
	a.OfferTuner(pitch.Note{Key: 40, Cents: 12}, true)
	a.syncLive()
	if !a.tunerSounding || a.tunerNote.Key != 40 || a.tunerNote.Cents != 12 {
		t.Errorf("tuner state = %+v sounding=%v, want key 40 +12 sounding", a.tunerNote, a.tunerSounding)
	}
	a.OfferTuner(pitch.Note{}, false)
	a.syncLive()
	if a.tunerSounding {
		t.Error("tuner still sounding after a sounding=false offer")
	}
}

// TestLiveStatus: the status fn is polled once per merge, and clearing it
// with nil turns the live HUD off again.
func TestLiveStatus(t *testing.T) {
	a := newApp(t, 1)
	polls := 0
	a.SetLiveStatus(func() (float64, int64) {
		polls++
		return -12.5, 3
	})
	a.syncLive()
	a.syncLive()
	if polls != 2 {
		t.Errorf("status polled %d times over 2 merges, want 2", polls)
	}
	if !a.live || a.levelDB != -12.5 || a.dropped != 3 {
		t.Errorf("live state = live=%v level=%v dropped=%v", a.live, a.levelDB, a.dropped)
	}
	a.SetLiveStatus(nil)
	a.syncLive()
	if a.live {
		t.Error("still live after SetLiveStatus(nil)")
	}
}

// TestWaitControl: SetWaitControl gates the W key's action, and
// toggleWait mirrors the engine wait mode for the HUD.
func TestWaitControl(t *testing.T) {
	a := newApp(t, 1)
	a.SetWaitControl(true)
	a.syncLive()
	if !a.waitCtl {
		t.Fatal("waitCtl not merged")
	}
	a.toggleWait()
	if !a.wait {
		t.Error("wait mirror not set after toggle")
	}
	a.toggleWait()
	if a.wait {
		t.Error("wait mirror not cleared after second toggle")
	}
}

// TestFeedsConcurrent hammers the feed methods from several goroutines
// while the "game loop" merges — the race detector is the real assertion;
// the count check proves no result is lost or double-counted.
func TestFeedsConcurrent(t *testing.T) {
	a := newApp(t, 1)
	const goroutines, per = 4, 250
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				a.OfferResults([]practice.NoteResult{result(int64(i), g+1, practice.VerdictHit)})
				a.OfferTuner(pitch.Note{Key: 40 + g, Cents: float64(i)}, i%2 == 0)
				if i%50 == 0 {
					a.SetLiveStatus(func() (float64, int64) { return -20, 0 })
					a.SetWaitControl(i%100 == 0)
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for merging := true; merging; {
		select {
		case <-done:
			merging = false
		default:
			a.syncLive()
		}
	}
	a.syncLive() // pick up anything offered after the last merge
	if a.stats.Hit != goroutines*per {
		t.Errorf("stats.Hit = %d, want %d (results lost or double-counted)", a.stats.Hit, goroutines*per)
	}
	// Each goroutine keyed its own string, so every offer is distinct.
	if want := per * goroutines; len(a.verdicts) != want {
		t.Errorf("verdict keys = %d, want %d", len(a.verdicts), want)
	}
}

// TestKeyName pins the MIDI-to-name mapping used by the tuner overlay.
func TestKeyName(t *testing.T) {
	for _, c := range []struct {
		key  int
		want string
	}{{40, "E2"}, {45, "A2"}, {60, "C4"}, {69, "A4"}, {61, "C#4"}} {
		if got := keyName(c.key); got != c.want {
			t.Errorf("keyName(%d) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestLoopKeysSetLoop checks the guards did not change normal behavior:
// A then B on a two-bar piece loops the whole piece.
func TestLoopKeysSetLoop(t *testing.T) {
	a := newApp(t, 2)
	barLen := a.displayed().Bars[0].Len()

	a.loopSetA() // at tick 0: loop bar 1
	la, lb, on := a.eng.Loop()
	if !on || la != 0 || lb != barLen {
		t.Fatalf("after A: loop = [%d, %d) on=%v, want [0, %d) on=true", la, lb, on, barLen)
	}

	a.eng.SeekTick(barLen) // into bar 2
	a.loopSetB()           // extend the end to bar 2's end
	la, lb, on = a.eng.Loop()
	if !on || la != 0 || lb != 2*barLen {
		t.Fatalf("after B: loop = [%d, %d) on=%v, want [0, %d) on=true", la, lb, on, 2*barLen)
	}
}

// --- Phase 5: fixed-BPM entry ---

// TestBPMEntryTyping drives the numeric entry's state machine: digits
// accumulate up to the cap, backspace deletes, a leading zero is absorbed,
// and both endings close the box.
func TestBPMEntryTyping(t *testing.T) {
	a := newApp(t, 1)
	if a.bpmEntry {
		t.Fatal("BPM entry open before it was asked for")
	}
	a.openBPMEntry()
	if !a.bpmEntry {
		t.Fatal("openBPMEntry did not open the entry")
	}
	for _, d := range []byte("0192") { // leading zero absorbed, then capped at 3
		a.bpmDigit(d)
	}
	if a.bpmDigits != "192" {
		t.Errorf("typed digits = %q, want %q", a.bpmDigits, "192")
	}
	a.bpmDigit('7') // past bpmEntryMaxDigits
	if a.bpmDigits != "192" {
		t.Errorf("digits past the cap = %q, want %q", a.bpmDigits, "192")
	}
	a.bpmBackspace()
	if a.bpmDigits != "19" {
		t.Errorf("after backspace = %q, want %q", a.bpmDigits, "19")
	}
	a.cancelBPMEntry()
	if a.bpmEntry || a.bpmDigits != "" {
		t.Errorf("after cancel: open=%v digits=%q, want closed and empty", a.bpmEntry, a.bpmDigits)
	}
	if got := a.eng.TempoScale(); got != 1 {
		t.Errorf("cancel changed the tempo scale to %v, want 1", got)
	}
	// Digits typed while the entry is closed are ignored. The entry is
	// the only thing that consumes them: Update returns early while
	// a.bpmEntry is set, so they never reach the mute/solo bindings.
	a.bpmDigit('5')
	if a.bpmDigits != "" {
		t.Errorf("digit accepted with the entry closed: %q", a.bpmDigits)
	}
}

// TestBPMEntryConversion checks the BPM-to-tempo-scale conversion against
// the score's own tempo, including both clamp ends.
func TestBPMEntryConversion(t *testing.T) {
	a := newApp(t, 1) // fixture is 500000 us/quarter = 120 BPM
	if got := a.baseBPM(); got != 120 {
		t.Fatalf("fixture base BPM = %v, want 120", got)
	}
	for _, c := range []struct {
		target      float64
		scale       float64
		actual      float64
		wantClamped bool
	}{
		{60, 0.5, 60, false},
		{90, 0.75, 90, false},
		{240, 2.0, 240, false}, // exactly the ceiling: not a clamp
		{300, 2.0, 240, true},  // above it
		{30, 0.25, 30, false},  // exactly the floor
		{20, 0.25, 30, true},   // below it
	} {
		scale, actual, clamped := a.scaleForBPM(c.target)
		if scale != c.scale || actual != c.actual || clamped != c.wantClamped {
			t.Errorf("scaleForBPM(%v) = scale %v actual %v clamped %v; want %v, %v, %v",
				c.target, scale, actual, clamped, c.scale, c.actual, c.wantClamped)
		}
	}
}

// TestBPMEntryUsesTempoAtPlayhead: the target is relative to the score's
// tempo where the playhead is, not to the piece's opening tempo, so a
// typed BPM means the same thing in a piece with tempo changes.
func TestBPMEntryUsesTempoAtPlayhead(t *testing.T) {
	a := newApp(t, 2)
	barLen := a.displayed().Bars[0].Len()
	a.sc.Tempos = append(a.sc.Tempos, score.Tempo{Tick: barLen, USPerQuarter: score.USPerQuarter(60)})

	if got := a.baseBPM(); got != 120 {
		t.Fatalf("base BPM in bar 1 = %v, want 120", got)
	}
	a.eng.SeekTick(barLen)
	if got := a.baseBPM(); got != 60 {
		t.Fatalf("base BPM in bar 2 = %v, want 60", got)
	}
	// 90 BPM is x0.75 of bar 1's tempo but x1.5 of bar 2's.
	if scale, _, _ := a.scaleForBPM(90); scale != 1.5 {
		t.Errorf("scaleForBPM(90) in bar 2 = %v, want 1.5", scale)
	}
}

// TestBPMEntryCommit applies the typed target to the engine and reports
// what happened, naming the clamped result when the target was out of
// reach rather than silently doing something else.
func TestBPMEntryCommit(t *testing.T) {
	a := newApp(t, 1)

	a.openBPMEntry()
	for _, d := range []byte("90") {
		a.bpmDigit(d)
	}
	a.commitBPMEntry()
	if a.bpmEntry {
		t.Error("entry still open after commit")
	}
	if got := a.eng.TempoScale(); got != 0.75 {
		t.Errorf("tempo scale after committing 90 BPM = %v, want 0.75", got)
	}
	if msg := a.bpmMessage(); !strings.Contains(msg, "90 BPM") {
		t.Errorf("commit message = %q, want it to name 90 BPM", msg)
	}

	a.openBPMEntry()
	for _, d := range []byte("400") {
		a.bpmDigit(d)
	}
	a.commitBPMEntry()
	if got := a.eng.TempoScale(); got != 2.0 {
		t.Errorf("tempo scale after committing 400 BPM = %v, want the 2.0 ceiling", got)
	}
	msg := a.bpmMessage()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "240 BPM") {
		t.Errorf("clamp message = %q, want it to name both the target and the 240 BPM it got", msg)
	}

	// Committing nothing is a no-op, not a tempo change.
	a.openBPMEntry()
	a.commitBPMEntry()
	if got := a.eng.TempoScale(); got != 2.0 {
		t.Errorf("empty commit changed the tempo scale to %v", got)
	}
}

// TestBPMMessageExpires: the result line is transient, so it does not sit
// under the tab for the rest of the session.
func TestBPMMessageExpires(t *testing.T) {
	a := newApp(t, 1)
	a.setBPMMessage("hello")
	if a.bpmMessage() != "hello" {
		t.Fatal("message not shown immediately after being set")
	}
	a.frame += bpmMsgFrames
	if got := a.bpmMessage(); got != "" {
		t.Errorf("message still showing after %d frames: %q", bpmMsgFrames, got)
	}
}

// --- Phase 5: mute and solo ---

// muteState reads the engine's per-track mute flags.
func muteState(a *App) []bool {
	m := make([]bool, len(a.sc.Tracks))
	for i := range m {
		m[i] = a.eng.TrackMuted(i)
	}
	return m
}

func sameBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSoloRestoresUserMutes is the core of the mute/solo contract: solo
// mutes the other tracks, and releasing it restores exactly the mutes the
// user had chosen, not "everything unmuted".
func TestSoloRestoresUserMutes(t *testing.T) {
	a := newAppTracks(t, 4, 1)
	a.toggleMute(0) // the user's own choice: track 1 muted
	if want := []bool{true, false, false, false}; !sameBools(muteState(a), want) {
		t.Fatalf("after muting track 1: %v, want %v", muteState(a), want)
	}

	a.toggleSolo(2) // solo track 3
	if want := []bool{true, true, false, true}; !sameBools(muteState(a), want) {
		t.Fatalf("under solo of track 3: %v, want %v", muteState(a), want)
	}
	if a.solo != 3 {
		t.Errorf("solo = %d, want 3 (1-based)", a.solo)
	}

	a.toggleSolo(2) // release
	if want := []bool{true, false, false, false}; !sameBools(muteState(a), want) {
		t.Fatalf("after releasing solo: %v, want the user's own mutes %v", muteState(a), want)
	}
	if a.solo != 0 {
		t.Errorf("solo = %d after release, want 0", a.solo)
	}
}

// TestSoloMovesAndMarks: soloing another track moves the solo, and the HUD
// badges distinguish a mute the user asked for from one solo implies.
func TestSoloMovesAndMarks(t *testing.T) {
	a := newAppTracks(t, 3, 1)
	a.toggleMute(0)
	a.toggleSolo(1)
	for i, want := range []string{"M ", " S", "m "} {
		if got := a.trackMarks(i); got != want {
			t.Errorf("trackMarks(%d) under solo of track 2 = %q, want %q", i, got, want)
		}
	}

	a.toggleSolo(2) // move the solo to track 3
	if a.solo != 3 {
		t.Fatalf("solo = %d after soloing track 3, want 3", a.solo)
	}
	if want := []bool{true, true, false}; !sameBools(muteState(a), want) {
		t.Errorf("after moving the solo: %v, want %v", muteState(a), want)
	}
	for i, want := range []string{"M ", "m ", " S"} {
		if got := a.trackMarks(i); got != want {
			t.Errorf("trackMarks(%d) after moving the solo = %q, want %q", i, got, want)
		}
	}

	a.toggleSolo(2)
	for i, want := range []string{"M ", "  ", "  "} {
		if got := a.trackMarks(i); got != want {
			t.Errorf("trackMarks(%d) after release = %q, want %q", i, got, want)
		}
	}
}

// TestMuteWhileSoloed: mute keys pressed under an active solo record the
// user's intent (the badge changes) and take effect when the solo is
// released. Muting the soloed track itself is audible immediately.
func TestMuteWhileSoloed(t *testing.T) {
	a := newAppTracks(t, 3, 1)
	a.toggleSolo(0)
	a.toggleMute(1) // a track solo already silences
	if want := []bool{false, true, true}; !sameBools(muteState(a), want) {
		t.Fatalf("muting a solo-silenced track changed what is audible: %v, want %v", muteState(a), want)
	}
	if got := a.trackMarks(1); got != "M " {
		t.Errorf("trackMarks(1) = %q, want %q - the user's choice is recorded", got, "M ")
	}
	a.toggleMute(0) // mute the soloed track: everything goes quiet
	if want := []bool{true, true, true}; !sameBools(muteState(a), want) {
		t.Errorf("muting the soloed track: %v, want %v", muteState(a), want)
	}
	a.toggleMute(0)
	a.toggleSolo(0) // release: track 2's mute survives
	if want := []bool{false, true, false}; !sameBools(muteState(a), want) {
		t.Errorf("after release: %v, want %v", muteState(a), want)
	}
}

// TestMuteStateSeededFromEngine: a piece opened with tracks already muted
// (the cmd layer mutes the player's own part for play-along) keeps that as
// the user's baseline instead of being unmuted by the first solo release.
func TestMuteStateSeededFromEngine(t *testing.T) {
	a := newAppTracks(t, 3, 1)
	a.eng.SetTrackMuted(2, true) // as the app layer configured it

	a.toggleSolo(0)
	a.toggleSolo(0)
	if want := []bool{false, false, true}; !sameBools(muteState(a), want) {
		t.Errorf("after a solo round trip: %v, want the startup mutes %v", muteState(a), want)
	}
}

// TestMuteSoloOutOfRange: the digit keys are bound to nine tracks whatever
// the piece has, so the handlers must ignore indexes past the end.
func TestMuteSoloOutOfRange(t *testing.T) {
	a := newAppTracks(t, 2, 1)
	a.toggleMute(5)
	a.toggleSolo(-1)
	if want := []bool{false, false}; !sameBools(muteState(a), want) {
		t.Errorf("out-of-range keys changed the mutes: %v", muteState(a))
	}
	if got := a.trackMarks(9); got != "  " {
		t.Errorf("trackMarks out of range = %q, want two spaces", got)
	}
}

// --- Phase 5: count-in ---

// TestCountInToggle: C flips the count-in for the next Play, and with no
// way to reach the running engine the state says so rather than lying.
func TestCountInToggle(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(4)
	if b := a.CountInBeats(); b != 4 {
		t.Fatalf("CountInBeats after SetCountIn(4) = %d, want 4", b)
	}
	if a.countInStale {
		t.Error("count-in reported stale before anything was changed")
	}
	if got := a.countInLabel(); got != "count-in 4" {
		t.Errorf("label = %q, want %q", got, "count-in 4")
	}

	a.toggleCountIn()
	if b := a.CountInBeats(); b != 0 {
		t.Errorf("CountInBeats after toggling off = %d, want 0", b)
	}
	if !a.countInStale {
		t.Error("with no applier the change must be reported as pending a re-open")
	}
	if got := a.countInLabel(); !strings.Contains(got, "off") || !strings.Contains(got, "re-open") {
		t.Errorf("label = %q, want it to say off and pending a re-open", got)
	}

	a.toggleCountIn()
	if b := a.CountInBeats(); b != 4 {
		t.Errorf("CountInBeats after toggling back on = %d, want 4", b)
	}
}

// TestCountInFromNoneUsesDefault: a piece opened without a count-in gets
// the four-beat default when the user turns one on.
func TestCountInFromNoneUsesDefault(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(0)
	if b := a.CountInBeats(); b != 0 {
		t.Fatalf("CountInBeats = %d, want 0", b)
	}
	a.toggleCountIn()
	if b := a.CountInBeats(); b != defaultCountInBeats {
		t.Errorf("CountInBeats after turning one on = %d, want %d", b, defaultCountInBeats)
	}
}

// TestCountInApplier: with a hook that reaches the engine, the change is
// pushed through and the HUD drops the caveat.
func TestCountInApplier(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(2)
	var got []int
	a.SetCountInApplier(func(beats int) bool {
		got = append(got, beats)
		return true
	})
	a.toggleCountIn()
	a.toggleCountIn()
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("applier saw %v, want [0 2]", got)
	}
	if a.countInStale {
		t.Error("count-in reported stale even though the applier accepted it")
	}
	if lbl := a.countInLabel(); lbl != "count-in 2" {
		t.Errorf("label = %q, want %q", lbl, "count-in 2")
	}

	// An applier that cannot make it stick must not be believed.
	a.SetCountInApplier(func(int) bool { return false })
	a.toggleCountIn()
	if !a.countInStale {
		t.Error("an applier returning false must leave the change pending")
	}
}

// --- Phase 5: help overlay and the binding table ---

// TestHelpOverlayOpenClose: the overlay is a plain flag so Update can route
// the whole keyboard to it.
func TestHelpOverlayOpenClose(t *testing.T) {
	a := newApp(t, 1)
	if a.helpOpen {
		t.Fatal("help overlay open on a fresh view")
	}
	a.openHelp()
	if !a.helpOpen {
		t.Fatal("openHelp did not open it")
	}
	a.closeHelp()
	if a.helpOpen {
		t.Fatal("closeHelp did not close it")
	}
}

// TestHelpGroupsCoverTable: the overlay is generated from the binding
// table, so every row appears exactly once and the groups keep table
// order - this is what stops the overlay drifting from the hint line.
func TestHelpGroupsCoverTable(t *testing.T) {
	a := newApp(t, 1)
	var flat []practiceBinding
	seen := map[string]bool{}
	for _, g := range a.helpGroups() {
		if g.Name == "" {
			t.Error("help group with no name")
		}
		if seen[g.Name] {
			t.Errorf("group %q appears twice: a group's rows must be contiguous in the table", g.Name)
		}
		seen[g.Name] = true
		for _, b := range g.Rows {
			if b.Group != g.Name {
				t.Errorf("binding %q filed under %q, not its own group %q", b.Keys, g.Name, b.Group)
			}
			flat = append(flat, b)
		}
	}
	if len(flat) != len(practiceBindings) {
		t.Fatalf("overlay lists %d bindings, table has %d", len(flat), len(practiceBindings))
	}
	for i, b := range flat {
		if b.Keys != practiceBindings[i].Keys {
			t.Errorf("row %d = %q, want %q (table order not preserved)", i, b.Keys, practiceBindings[i].Keys)
		}
		if b.Keys == "" || b.Desc == "" {
			t.Errorf("binding %d has an empty key label or description: %+v", i, b)
		}
	}
}

// TestHintLineFromTable: the HUD hint is generated from the same table and
// honours the availability gates, so it never advertises a dead key.
func TestHintLineFromTable(t *testing.T) {
	a := newApp(t, 1)
	line := a.hintLine()
	for _, b := range practiceBindings {
		if b.Hint == "" || !b.enabled(a) {
			continue
		}
		if !strings.Contains(line, b.Hint) {
			t.Errorf("hint line %q is missing %q", line, b.Hint)
		}
	}
	if strings.Contains(line, "W wait") {
		t.Error("hint line offers W with no detector to confirm waits")
	}
	a.SetWaitControl(true)
	a.syncLive()
	if !strings.Contains(a.hintLine(), "W wait") {
		t.Error("hint line omits W once a detector is present")
	}
	// The hint must fit the window: 7px per basicfont cell from x=16.
	if w := 16 + 7*len(a.hintLine()); w > screenW {
		t.Errorf("hint line is %dpx wide, past the %dpx window", w, screenW)
	}
}

// --- Phase 5: live warning banner ---

// TestLiveWarning: the app layer raises and clears the banner, the user
// dismisses it, and re-reporting the same condition does not push it back
// in their face - but a different one does.
func TestLiveWarning(t *testing.T) {
	a := newApp(t, 1)
	if a.warningVisible() {
		t.Fatal("warning visible with none set")
	}

	a.SetLiveWarning("capture and playback are different devices")
	a.syncLive()
	if !a.warningVisible() {
		t.Fatal("warning not visible after being set")
	}

	a.dismissWarning()
	if a.warningVisible() {
		t.Fatal("warning still visible after dismissal")
	}
	a.SetLiveWarning("capture and playback are different devices") // same condition, re-reported
	a.syncLive()
	if a.warningVisible() {
		t.Error("a re-report of the dismissed message raised the banner again")
	}

	a.SetLiveWarning("input stream stopped")
	a.syncLive()
	if !a.warningVisible() || a.warnMsg != "input stream stopped" {
		t.Errorf("new warning not raised: visible=%v msg=%q", a.warningVisible(), a.warnMsg)
	}

	a.SetLiveWarning("")
	a.syncLive()
	if a.warningVisible() {
		t.Error("warning still visible after being cleared")
	}
}

// --- Phase 5: shell citizenship ---

// TestQuitAll: Q leaves the whole application when the integrator wires
// that up, and keeps the standalone behaviour of finishing the screen when
// it does not. Escape always finishes only this screen.
func TestQuitAll(t *testing.T) {
	a := newApp(t, 1)
	if err := a.quitApp(); err != errQuit {
		t.Errorf("Q with no quit-all action returned %v, want errQuit", err)
	}
	quits := 0
	a.SetQuitAll(func() { quits++ })
	if err := a.quitApp(); err != nil {
		t.Errorf("Q with a quit-all action returned %v, want nil (the action decides)", err)
	}
	if quits != 1 {
		t.Errorf("quit-all action called %d times, want 1", quits)
	}
}
