package pitch

import (
	"math"
	"testing"
)

func runPipeline(t *testing.T, x []float32) []Note {
	t.Helper()
	cfg := DefaultConfig(testSR)
	d := NewDetector(cfg)
	tr := NewTracker(cfg)
	var notes []Note
	for off := 0; off < len(x); off += 480 {
		end := off + 480
		if end > len(x) {
			end = len(x)
		}
		notes = append(notes, tr.Feed(d.Process(x[off:end]))...)
	}
	return append(notes, tr.Flush()...)
}

func TestTrackerDetunedSine(t *testing.T) {
	const key = 57
	freq := keyToFreq(key) * math.Pow(2, 20.0/1200)
	notes := runPipeline(t, sine(freq, 0.4, 0.5))
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1: %+v", len(notes), notes)
	}
	n := notes[0]
	if n.Key != key {
		t.Errorf("Key = %d, want %d", n.Key, key)
	}
	if math.Abs(n.Cents-20) > 5 {
		t.Errorf("Cents = %+.2f, want +20 ±5", n.Cents)
	}
	if n.End <= n.Start {
		t.Errorf("End %d not after Start %d", n.End, n.Start)
	}
}

func TestTrackerBendIsOneNote(t *testing.T) {
	cfg := DefaultConfig(testSR)
	d := NewDetector(cfg)
	tr := NewTracker(cfg)
	x := chirp(196, 220, 0.4, 0.5)

	var notes []Note
	var risingCents []float64
	for off := 0; off < len(x); off += 480 {
		end := off + 480
		if end > len(x) {
			end = len(x)
		}
		notes = append(notes, tr.Feed(d.Process(x[off:end]))...)

		if cur, ok := tr.Current(); ok {
			risingCents = append(risingCents, cur.Cents)
		}
	}
	notes = append(notes, tr.Flush()...)

	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1 (a bend must not split): %+v", len(notes), notes)
	}
	n := notes[0]
	if n.Key != 55 {
		t.Errorf("Key = %d, want 55 (the starting key)", n.Key)
	}

	if n.Cents < 40 || n.Cents > 160 {
		t.Errorf("Cents = %+.2f, want mid-trajectory (40..160) for a 0..200 cent bend", n.Cents)
	}

	if len(risingCents) < 4 {
		t.Fatalf("only %d Current() samples", len(risingCents))
	}
	first := risingCents[len(risingCents)/4]
	last := risingCents[len(risingCents)-1]
	if last-first < 60 {
		t.Errorf("cents trajectory rose only %.1f (%.1f -> %.1f), want a clear rise", last-first, first, last)
	}
}

func TestTrackerReportsTrajectory(t *testing.T) {
	notes := runPipeline(t, chirp(196, 220, 0.4, 0.5))
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1: %+v", len(notes), notes)
	}
	n := notes[0]
	if n.Key != 55 {
		t.Fatalf("Key = %d, want 55 (G3, where the sweep started)", n.Key)
	}

	if n.MinCents > 40 {
		t.Errorf("MinCents = %+.1f, want near 0 (the note opened on its key)", n.MinCents)
	}
	if n.MaxCents < 160 {
		t.Errorf("MaxCents = %+.1f, want near +200 (the sweep's destination)", n.MaxCents)
	}

	if n.EndCents < 160 {
		t.Errorf("EndCents = %+.1f, want near +200 (where the note settled); Cents is %+.1f", n.EndCents, n.Cents)
	}
	if n.EndCents <= n.Cents {
		t.Errorf("EndCents %+.1f not past the whole-note median %+.1f: the trajectory is being averaged away", n.EndCents, n.Cents)
	}
}

func TestTrackerSteadyNoteHasFlatTrajectory(t *testing.T) {
	const key = 57
	notes := runPipeline(t, sine(keyToFreq(key), 0.4, 0.5))
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1: %+v", len(notes), notes)
	}
	n := notes[0]
	for _, c := range []struct {
		name string
		v    float64
	}{{"MinCents", n.MinCents}, {"MaxCents", n.MaxCents}, {"EndCents", n.EndCents}} {
		if math.Abs(c.v) > 25 {
			t.Errorf("%s = %+.1f, want within ±25 of the key: a steady note swept nowhere", c.name, c.v)
		}
	}
}

func TestTrackerRepickSplitsNotes(t *testing.T) {
	const key = 45
	v := ksVoice()
	x := silence(0.2)
	v.NoteOn(key, 0.8)
	x = ksRender(v, 0.3, x)
	v.NoteOff(key)
	x = ksRender(v, 0.15, x)
	repick := int64(len(x))
	v.NoteOn(key, 0.8)
	x = ksRender(v, 0.3, x)

	d := NewDetector(DefaultConfig(testSR))
	sawOnset := false
	for _, f := range feedAll(d, x, 480) {
		if f.Onset && f.Frame > repick-4800 && f.Frame < repick+4800 {
			sawOnset = true
		}
	}
	if !sawOnset {
		t.Fatal("no onset detected near the re-pick; the split would not be onset-driven")
	}

	notes := runPipeline(t, x)
	if len(notes) != 2 {
		t.Fatalf("got %d notes, want 2 (onset split on re-pick): %+v", len(notes), notes)
	}
	for i, n := range notes {
		if n.Key != key {
			t.Errorf("note %d: Key = %d, want %d", i, n.Key, key)
		}
	}
	if notes[1].Start <= notes[0].End {
		t.Errorf("note 1 starts at %d, before note 0 ends at %d", notes[1].Start, notes[0].End)
	}
}

func TestTrackerKeyChangeSurvivesUnvoicedFlicker(t *testing.T) {
	cfg := DefaultConfig(testSR)
	tr := NewTracker(cfg)

	stamp := int64(cfg.Window / 2)
	next := func(f0, clarity float64) Frame {
		f := Frame{Frame: stamp, F0: f0, Clarity: clarity, RMS: 0.1}
		stamp += int64(cfg.Hop)
		return f
	}

	var closed []Note
	feed := func(f Frame) {
		closed = append(closed, tr.Feed([]Frame{f})...)
	}

	const oldKey, newKey = 57, 64

	for i := 0; i < 10; i++ {
		feed(next(keyToFreq(oldKey), 0.95))
	}
	if cur, ok := tr.Current(); !ok || cur.Key != oldKey {
		t.Fatalf("Current = %+v, %v; want open note on key %d", cur, ok, oldKey)
	}

	for i := 0; i < 12; i++ {
		feed(next(keyToFreq(newKey), 0.9))
		feed(next(0, 0))
	}

	if len(closed) != 1 || closed[0].Key != oldKey {
		t.Fatalf("closed notes = %+v, want exactly one on key %d (the old note must close)", closed, oldKey)
	}
	if n := closed[0]; n.End <= n.Start {
		t.Errorf("old note End %d not after Start %d", n.End, n.Start)
	}
	cur, ok := tr.Current()
	if !ok || cur.Key != newKey {
		t.Fatalf("Current = %+v, %v; want open note on key %d (the new key must be recognized)", cur, ok, newKey)
	}
}

func TestTrackerClosesOnSilence(t *testing.T) {
	x := append(sine(220, 0.4, 0.3), silence(0.3)...)
	cfg := DefaultConfig(testSR)
	d := NewDetector(cfg)
	tr := NewTracker(cfg)
	notes := append([]Note(nil), tr.Feed(d.Process(x))...)
	if len(notes) != 1 {
		t.Fatalf("got %d notes before Flush, want 1: %+v", len(notes), notes)
	}
	n := notes[0]
	sounding := int64(0.3 * testSR)
	if n.End <= n.Start || n.End > sounding+int64(cfg.Window) {
		t.Errorf("note [%d, %d] not closed inside the sounding region (0..%d)", n.Start, n.End, sounding)
	}
	if left := tr.Flush(); len(left) != 0 {
		t.Errorf("Flush returned %d extra notes, want 0", len(left))
	}
}

func TestTrackerCurrentAndFlush(t *testing.T) {
	cfg := DefaultConfig(testSR)
	d := NewDetector(cfg)
	tr := NewTracker(cfg)
	tr.Feed(d.Process(sine(220, 0.4, 0.3)))

	cur, ok := tr.Current()
	if !ok {
		t.Fatal("Current: no open note while the sine sounds")
	}
	if cur.End != 0 {
		t.Errorf("Current End = %d, want 0 for a still-sounding note", cur.End)
	}
	if cur.Key != 57 {
		t.Errorf("Current Key = %d, want 57", cur.Key)
	}

	notes := tr.Flush()
	if len(notes) != 1 {
		t.Fatalf("Flush returned %d notes, want 1", len(notes))
	}
	if notes[0].End == 0 {
		t.Error("flushed note still has End 0")
	}
	if _, ok := tr.Current(); ok {
		t.Error("Current still reports a note after Flush")
	}
}
