package ui

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/practice"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

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

func TestLoopKeysEmptyTrack(t *testing.T) {
	a := newApp(t, 0)
	if i := a.barAt(0); i != -1 {
		t.Fatalf("barAt(0) on a bar-less track = %d, want -1", i)
	}
	a.loopSetA()
	a.loopSetB()
	if _, _, on := a.eng.Loop(); on {
		t.Fatal("loop enabled on a track with no bars")
	}
}

func result(start int64, str int, v practice.Verdict) practice.NoteResult {
	return practice.NoteResult{Event: score.NoteEvent{Start: start, String: str}, Verdict: v}
}

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

func TestVerdictKeying(t *testing.T) {
	a := newApp(t, 1)
	a.OfferResults([]practice.NoteResult{
		result(960, 6, practice.VerdictHit),
		result(960, 5, practice.VerdictMiss),
	})
	a.syncLive()
	if v, ok := a.verdicts[noteKey{960, 6}]; !ok || v != practice.VerdictHit {
		t.Errorf("verdict(960, string 6) = %v, %v; want Hit", v, ok)
	}
	if v, ok := a.verdicts[noteKey{960, 5}]; !ok || v != practice.VerdictMiss {
		t.Errorf("verdict(960, string 5) = %v, %v; want Miss", v, ok)
	}

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

	if _, ok := a.verdictAt(1920, 6, 5000); ok {
		t.Error("verdict painted for a note that was never judged")
	}
}

func TestVerdictSurvivesLoopWrapUntilRejudged(t *testing.T) {
	a := newApp(t, 2)
	const first, second = int64(0), int64(3840)

	a.OfferResults([]practice.NoteResult{
		result(first, 6, practice.VerdictHit),
		result(second, 6, practice.VerdictMiss),
	})
	a.syncLive()

	if _, ok := a.verdictAt(second, 6, first); ok {
		t.Error("pass 1 verdict painted on a note pass 2 has not reached")
	}
	if v, ok := a.verdictAt(first, 6, first); !ok || v != practice.VerdictHit {
		t.Errorf("verdict under the playhead = %v, %v; want Hit, true", v, ok)
	}

	a.OfferResults([]practice.NoteResult{result(second, 6, practice.VerdictMiss)})
	a.syncLive()
	if _, ok := a.verdictAt(second, 6, first); ok {
		t.Error("late pass 1 verdict painted on a note ahead of the playhead")
	}

	a.OfferResults([]practice.NoteResult{result(second, 6, practice.VerdictHit)})
	a.syncLive()
	if v, ok := a.verdictAt(second, 6, second); !ok || v != practice.VerdictHit {
		t.Errorf("re-judged verdict = %v, %v; want Hit, true", v, ok)
	}
}

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
	a.syncLive()
	if a.stats.Hit != goroutines*per {
		t.Errorf("stats.Hit = %d, want %d (results lost or double-counted)", a.stats.Hit, goroutines*per)
	}

	if want := per * goroutines; len(a.verdicts) != want {
		t.Errorf("verdict keys = %d, want %d", len(a.verdicts), want)
	}
}

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

func TestLoopKeysSetLoop(t *testing.T) {
	a := newApp(t, 2)
	barLen := a.displayed().Bars[0].Len()

	a.loopSetA()
	la, lb, on := a.eng.Loop()
	if !on || la != 0 || lb != barLen {
		t.Fatalf("after A: loop = [%d, %d) on=%v, want [0, %d) on=true", la, lb, on, barLen)
	}

	a.eng.SeekTick(barLen)
	a.loopSetB()
	la, lb, on = a.eng.Loop()
	if !on || la != 0 || lb != 2*barLen {
		t.Fatalf("after B: loop = [%d, %d) on=%v, want [0, %d) on=true", la, lb, on, 2*barLen)
	}
}

func TestBPMEntryTyping(t *testing.T) {
	a := newApp(t, 1)
	if a.bpmEntry {
		t.Fatal("BPM entry open before it was asked for")
	}
	a.openBPMEntry()
	if !a.bpmEntry {
		t.Fatal("openBPMEntry did not open the entry")
	}
	for _, d := range []byte("0192") {
		a.bpmDigit(d)
	}
	if a.bpmDigits != "192" {
		t.Errorf("typed digits = %q, want %q", a.bpmDigits, "192")
	}
	a.bpmDigit('7')
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

	a.bpmDigit('5')
	if a.bpmDigits != "" {
		t.Errorf("digit accepted with the entry closed: %q", a.bpmDigits)
	}
}

func TestBPMEntryConversion(t *testing.T) {
	a := newApp(t, 1)
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
		{240, 2.0, 240, false},
		{300, 2.0, 240, true},
		{30, 0.25, 30, false},
		{20, 0.25, 30, true},
	} {
		scale, actual, clamped := a.scaleForBPM(c.target)
		if scale != c.scale || actual != c.actual || clamped != c.wantClamped {
			t.Errorf("scaleForBPM(%v) = scale %v actual %v clamped %v; want %v, %v, %v",
				c.target, scale, actual, clamped, c.scale, c.actual, c.wantClamped)
		}
	}
}

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

	if scale, _, _ := a.scaleForBPM(90); scale != 1.5 {
		t.Errorf("scaleForBPM(90) in bar 2 = %v, want 1.5", scale)
	}
}

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

	a.openBPMEntry()
	a.commitBPMEntry()
	if got := a.eng.TempoScale(); got != 2.0 {
		t.Errorf("empty commit changed the tempo scale to %v", got)
	}
}

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

func TestSoloRestoresUserMutes(t *testing.T) {
	a := newAppTracks(t, 4, 1)
	a.toggleMute(0)
	if want := []bool{true, false, false, false}; !sameBools(muteState(a), want) {
		t.Fatalf("after muting track 1: %v, want %v", muteState(a), want)
	}

	a.toggleSolo(2)
	if want := []bool{true, true, false, true}; !sameBools(muteState(a), want) {
		t.Fatalf("under solo of track 3: %v, want %v", muteState(a), want)
	}
	if a.solo != 3 {
		t.Errorf("solo = %d, want 3 (1-based)", a.solo)
	}

	a.toggleSolo(2)
	if want := []bool{true, false, false, false}; !sameBools(muteState(a), want) {
		t.Fatalf("after releasing solo: %v, want the user's own mutes %v", muteState(a), want)
	}
	if a.solo != 0 {
		t.Errorf("solo = %d after release, want 0", a.solo)
	}
}

func TestSoloMovesAndMarks(t *testing.T) {
	a := newAppTracks(t, 3, 1)
	a.toggleMute(0)
	a.toggleSolo(1)
	for i, want := range []string{"muted", "solo", "muted by solo"} {
		if got := a.trackStateText(i); got != want {
			t.Errorf("trackStateText(%d) under solo of track 2 = %q, want %q", i, got, want)
		}
	}

	a.toggleSolo(2)
	if a.solo != 3 {
		t.Fatalf("solo = %d after soloing track 3, want 3", a.solo)
	}
	if want := []bool{true, true, false}; !sameBools(muteState(a), want) {
		t.Errorf("after moving the solo: %v, want %v", muteState(a), want)
	}
	for i, want := range []string{"muted", "muted by solo", "solo"} {
		if got := a.trackStateText(i); got != want {
			t.Errorf("trackStateText(%d) after moving the solo = %q, want %q", i, got, want)
		}
	}

	a.toggleSolo(2)
	for i, want := range []string{"muted", "audible", "audible"} {
		if got := a.trackStateText(i); got != want {
			t.Errorf("trackStateText(%d) after release = %q, want %q", i, got, want)
		}
	}
}

func TestMuteWhileSoloed(t *testing.T) {
	a := newAppTracks(t, 3, 1)
	a.toggleSolo(0)
	a.toggleMute(1)
	if want := []bool{false, true, true}; !sameBools(muteState(a), want) {
		t.Fatalf("muting a solo-silenced track changed what is audible: %v, want %v", muteState(a), want)
	}
	if got := a.trackStateText(1); got != "muted" {
		t.Errorf("trackStateText(1) = %q, want %q - the user's choice is recorded", got, "muted")
	}
	a.toggleMute(0)
	if want := []bool{true, true, true}; !sameBools(muteState(a), want) {
		t.Errorf("muting the soloed track: %v, want %v", muteState(a), want)
	}
	a.toggleMute(0)
	a.toggleSolo(0)
	if want := []bool{false, true, false}; !sameBools(muteState(a), want) {
		t.Errorf("after release: %v, want %v", muteState(a), want)
	}
}

func TestMuteStateSeededFromEngine(t *testing.T) {
	a := newAppTracks(t, 3, 1)
	a.eng.SetTrackMuted(2, true)

	a.toggleSolo(0)
	a.toggleSolo(0)
	if want := []bool{false, false, true}; !sameBools(muteState(a), want) {
		t.Errorf("after a solo round trip: %v, want the startup mutes %v", muteState(a), want)
	}
}

func TestMuteSoloOutOfRange(t *testing.T) {
	a := newAppTracks(t, 2, 1)
	a.toggleMute(5)
	a.toggleSolo(-1)
	if want := []bool{false, false}; !sameBools(muteState(a), want) {
		t.Errorf("out-of-range keys changed the mutes: %v", muteState(a))
	}
	if got := a.trackStateText(9); got != "audible" {
		t.Errorf("trackStateText out of range = %q, want the inert %q", got, "audible")
	}
}

func TestCountInToggle(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(4)
	if b := a.CountInBeats(); b != 4 {
		t.Fatalf("CountInBeats after SetCountIn(4) = %d, want 4", b)
	}
	if a.countInStale {
		t.Error("count-in reported stale before anything was changed")
	}
	if a.bpmMessage() != "" {
		t.Error("a transient message is showing before anything was changed")
	}

	a.toggleCountIn()
	if b := a.CountInBeats(); b != 0 {
		t.Errorf("CountInBeats after toggling off = %d, want 0", b)
	}
	if !a.countInStale {
		t.Error("with no applier the change must be reported as pending a re-open")
	}

	if got := a.bpmMessage(); !strings.Contains(got, "next opened") {
		t.Errorf("transient message = %q, want it to say the change applies on the next open", got)
	}

	a.toggleCountIn()
	if b := a.CountInBeats(); b != 4 {
		t.Errorf("CountInBeats after toggling back on = %d, want 4", b)
	}
}

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
	if msg := a.bpmMessage(); msg != "" {
		t.Errorf("an accepted change should raise no caveat, got %q", msg)
	}

	a.SetCountInApplier(func(int) bool { return false })
	a.SetReloader(func() {})
	a.toggleCountIn()
	if !a.countInStale {
		t.Error("an applier returning false must leave the change pending")
	}
	if a.reloadPrompt() == "" {
		t.Error("a pending count-in change with a reloader wired should offer F5")
	}
}

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

func TestHelpGroupsCoverTable(t *testing.T) {
	a := newApp(t, 1)
	var flat []helpBinding
	seen := map[string]bool{}

	for _, g := range helpSections(a.helpRows()) {
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

func TestHintLineFromTable(t *testing.T) {
	a := newApp(t, 1)
	line := a.hintLine()

	for _, b := range a.helpRows() {
		if b.Hint == "" || b.Off {
			continue
		}
		if !strings.Contains(line, b.Hint) {
			t.Errorf("hint line %q is missing %q", line, b.Hint)
		}
	}

	if !strings.Contains(line, "esc quit") {
		t.Errorf("hint line %q should say esc quits with no shell behind it", line)
	}
	a.SetQuitAll(func() {})
	if !strings.Contains(a.hintLine(), "esc back") {
		t.Errorf("under a shell the hint line should say esc goes back, got %q", a.hintLine())
	}
	if strings.Contains(line, "W wait") {
		t.Error("hint line offers W with no detector to confirm waits")
	}
	a.SetWaitControl(true)
	a.syncLive()
	if !strings.Contains(a.hintLine(), "W wait") {
		t.Error("hint line omits W once a detector is present")
	}

	if w := uiPadX + textW(a.hintLine()); w > screenW-uiPadX {
		t.Errorf("hint line ends at %.0fpx, past the %.0fpx margin", w, float64(screenW-uiPadX))
	}
}

func TestStatusLineCountsPassesOnlyInsideALoop(t *testing.T) {
	a := newApp(t, 2)
	if got := a.statusLine(); strings.Contains(got, "pass") {
		t.Errorf("status %q counts passes with no loop armed", got)
	}
	a.eng.SetLoop(0, 3840)
	if got := a.statusLine(); !strings.Contains(got, "pass 0") {
		t.Errorf("status %q should count passes once a loop is armed", got)
	}
}

func TestRampExplainsWhenItCannotAct(t *testing.T) {
	a := newApp(t, 2)

	a.toggleRamp()
	if !a.ramp {
		t.Fatal("the explanation must not block the toggle itself")
	}
	if got := a.bpmMessage(); !strings.Contains(got, "set a loop first") {
		t.Errorf("ramp with no loop said %q, want it to ask for a loop", got)
	}

	a.frame += bpmMsgFrames
	a.toggleRamp()
	if got := a.bpmMessage(); got != "" {
		t.Errorf("toggling ramp off posted %q", got)
	}

	a.eng.SetLoop(0, 3840)
	a.toggleRamp()
	if got := a.bpmMessage(); !strings.Contains(got, "full speed") {
		t.Errorf("ramp at full speed said %q, want it to say so", got)
	}

	a.frame += bpmMsgFrames
	a.toggleRamp()
	a.eng.SetTempoScale(0.75)
	a.toggleRamp()
	if got := a.bpmMessage(); got != "" {
		t.Errorf("an actionable ramp posted the caveat %q", got)
	}
}

func TestHelpRowsNameTheirRemedies(t *testing.T) {
	a := newApp(t, 1)
	row := func(keys string) helpBinding {
		t.Helper()
		for _, b := range a.helpRows() {
			if b.Keys == keys {
				return b
			}
		}
		t.Fatalf("no binding row for %q", keys)
		return helpBinding{}
	}

	w := row("W")
	if !w.Off || !strings.Contains(w.Desc, "live input") {
		t.Errorf("the unavailable W row is %+v, want it off and naming live input", w)
	}
	if got := overlayDesc(w); strings.Contains(got, "not available now") {
		t.Errorf("W's overlay line %q stamps the generic marker over its own explanation", got)
	}

	s := row("S")
	if !s.Off || !strings.Contains(s.Desc, "full app") {
		t.Errorf("the standalone S row is %+v, want it off and naming the full app", s)
	}

	tr := row("T")
	if tr.Off {
		t.Error("T must stay active without live input; the overlay it opens explains itself")
	}
	if !strings.Contains(tr.Desc, "live input") {
		t.Errorf("T's row %q should carry the live-input caveat", tr.Desc)
	}

	f5 := row("F5")
	if !f5.Off {
		t.Fatal("the fixture has no reloader, so F5 should be off")
	}
	if got := overlayDesc(f5); !strings.Contains(got, "not available now") {
		t.Errorf("the unexplained F5 row lost its marker: %q", got)
	}

	a.SetWaitControl(true)
	a.SetLiveStatus(func() (float64, int64) { return -20, 0 })
	a.syncLive()
	a.SetSettingsOpener(func() {})
	if w := row("W"); w.Off || strings.Contains(w.Desc, "live input") {
		t.Errorf("with a detector the W row is %+v, want it plain", w)
	}
	if tr := row("T"); strings.Contains(tr.Desc, "live input") {
		t.Errorf("in a live session the T row still carries the caveat: %q", tr.Desc)
	}
	if s := row("S"); s.Off || strings.Contains(s.Desc, "full app") {
		t.Errorf("with an opener the S row is %+v, want it plain", s)
	}
}

func TestNoteCueKeepsBothChannels(t *testing.T) {
	a := newApp(t, 2)

	if col, sounding := a.noteCue(3840, 3840, 6, false, 0, 0, nil); col != colNote || sounding {
		t.Errorf("an unjudged note ahead of the playhead = %v sounding=%v, want plain and silent", col, sounding)
	}
	if col, _ := a.noteCue(3840, 3840, 6, true, 0, 0, nil); col != colInferred {
		t.Errorf("an inferred note = %v, want colInferred", col)
	}
	if col, sounding := a.noteCue(0, 3840, 6, false, 100, 100, nil); col != colNote || !sounding {
		t.Errorf("a sounding unjudged note = %v sounding=%v; the position cue lives on its own channel now", col, sounding)
	}

	a.OfferResults([]practice.NoteResult{result(0, 6, practice.VerdictHit)})
	a.syncLive()
	col, sounding := a.noteCue(0, 3840, 6, false, 100, 100, nil)
	if col != colHit {
		t.Errorf("a judged note's glyph = %v, want the verdict tint", col)
	}
	if !sounding {
		t.Error("the verdict displaced the sounding cue; both must show at once")
	}

	waiting := map[noteKey]bool{{0, 6}: true}
	if col, _ := a.noteCue(0, 3840, 6, false, 100, 100, waiting); col != a.pulseCol() {
		t.Errorf("a waited-on note = %v, want the wait pulse", col)
	}
}

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
	a.SetLiveWarning("capture and playback are different devices")
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

func TestNewNarrowsWaitModeToTheDisplayedTrack(t *testing.T) {
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}

	first := &score.Track{Name: "lead", Tuning: score.StandardTuning}
	second := &score.Track{Name: "harmony", Tuning: score.StandardTuning}
	sc.Tracks = []*score.Track{first, second}
	fb := first.AppendBar(4, 4)
	fb.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	fb.AddBeat(score.Quarter)
	fb.AddBeat(score.Quarter)
	fb.AddBeat(score.Quarter)
	sb := second.AppendBar(4, 4)
	sb.AddBeat(score.Quarter)
	sb.AddBeat(score.Quarter, score.Note{String: 5, Fret: 0})
	sb.AddBeat(score.Quarter)
	sb.AddBeat(score.Quarter)
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}

	eng := engine.New(sc, engine.Options{Voices: synth.NewPluck})
	New(eng, sc, 0)
	eng.SetWaitMode(true)
	eng.Play()

	l := make([]float32, 480)
	r := make([]float32, 480)
	for i := 0; i < 200 && !eng.Waiting(); i++ {
		eng.RenderFrames(l, r)
	}
	if !eng.Waiting() {
		t.Fatal("wait mode never engaged")
	}
	eng.ConfirmWait()

	for i := 0; i < 400; i++ {
		eng.RenderFrames(l, r)
	}
	if evs, _, ok := eng.WaitingOn(); ok {
		t.Errorf("halted again at tick %d on track %d, want to play through track 1's note", evs[0].Start, evs[0].Track)
	}
}
