package ui

// Windowing is untestable headlessly, so these tests drive the extracted
// input logic (loopSetA/loopSetB, barAt) directly against a real engine.

import (
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
