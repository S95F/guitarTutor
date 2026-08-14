package pitch

import (
	"math"
	"testing"
)

// runPipeline drives detector + tracker over x and returns all closed
// notes, including those closed by the final Flush.
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

// TestTrackerDetunedSine: a sine +20 cents sharp of A3 tracks as one note
// on key 57 whose Cents lands within ±5 of +20.
func TestTrackerDetunedSine(t *testing.T) {
	const key = 57 // A3
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

// TestTrackerBendIsOneNote pins the documented bend semantics: a sine
// sweeping 196 Hz (G3) to 220 Hz (A3) over 0.5 s — a full-tone bend that
// reaches the next key and beyond — stays ONE note whose cents trajectory
// rose. Only an onset or a discontinuous jump splits; a monotonic drift
// never does.
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
		// Sample the live cents trajectory the way wait mode would.
		if cur, ok := tr.Current(); ok {
			risingCents = append(risingCents, cur.Cents)
		}
	}
	notes = append(notes, tr.Flush()...)

	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1 (a bend must not split): %+v", len(notes), notes)
	}
	n := notes[0]
	if n.Key != 55 { // G3, where the bend started
		t.Errorf("Key = %d, want 55 (the starting key)", n.Key)
	}
	// The 196→220 sweep spans 200 cents; the median deviation sits near
	// the middle of the trajectory.
	if n.Cents < 40 || n.Cents > 160 {
		t.Errorf("Cents = %+.2f, want mid-trajectory (40..160) for a 0..200 cent bend", n.Cents)
	}
	// The live trajectory rose.
	if len(risingCents) < 4 {
		t.Fatalf("only %d Current() samples", len(risingCents))
	}
	first := risingCents[len(risingCents)/4]
	last := risingCents[len(risingCents)-1]
	if last-first < 60 {
		t.Errorf("cents trajectory rose only %.1f (%.1f -> %.1f), want a clear rise", last-first, first, last)
	}
}

// TestTrackerReportsTrajectory is the slide regression. A legato slide is
// one continuously sounded note that keeps its ORIGIN key (the case above
// pins that, deliberately), so Note.Cents — a median over the whole note —
// is the only pitch figure a caller used to get, and it reports neither
// where the note started nor where it arrived. A scorer therefore could not
// recognize a slide DESTINATION, which the score writes at the destination
// pitch, and every slide in a piece was an expectation that could not be
// matched however well it was played.
//
// The same 196→220 Hz sweep must report a trajectory that starts at the
// opening key, reaches a full tone above it, and SETTLES there.
func TestTrackerReportsTrajectory(t *testing.T) {
	notes := runPipeline(t, chirp(196, 220, 0.4, 0.5))
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1: %+v", len(notes), notes)
	}
	n := notes[0]
	if n.Key != 55 {
		t.Fatalf("Key = %d, want 55 (G3, where the sweep started)", n.Key)
	}
	// The sweep spans 0..+200 cents off the opening key.
	if n.MinCents > 40 {
		t.Errorf("MinCents = %+.1f, want near 0 (the note opened on its key)", n.MinCents)
	}
	if n.MaxCents < 160 {
		t.Errorf("MaxCents = %+.1f, want near +200 (the sweep's destination)", n.MaxCents)
	}
	// EndCents is what tells a slide that ARRIVED from one still moving:
	// the final hops sit at the destination, not mid-journey like Cents.
	if n.EndCents < 160 {
		t.Errorf("EndCents = %+.1f, want near +200 (where the note settled); Cents is %+.1f", n.EndCents, n.Cents)
	}
	if n.EndCents <= n.Cents {
		t.Errorf("EndCents %+.1f not past the whole-note median %+.1f: the trajectory is being averaged away", n.EndCents, n.Cents)
	}
}

// TestTrackerSteadyNoteHasFlatTrajectory: the trajectory fields must not
// invent movement. A note played straight reports its own key at every
// rung, so nothing downstream can mistake it for a slide.
func TestTrackerSteadyNoteHasFlatTrajectory(t *testing.T) {
	const key = 57 // A3
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

// TestTrackerRepickSplitsNotes: two Karplus-Strong plucks of the SAME key
// back to back must yield TWO notes — the second attack's onset splits
// them even though the pitch never changes.
func TestTrackerRepickSplitsNotes(t *testing.T) {
	const key = 45 // A2
	v := ksVoice()
	x := silence(0.2)
	v.NoteOn(key, 0.8)
	x = ksRender(v, 0.3, x)
	v.NoteOff(key)
	x = ksRender(v, 0.15, x) // ring down, still audible
	repick := int64(len(x))
	v.NoteOn(key, 0.8) // re-pick
	x = ksRender(v, 0.3, x)

	// The ring-down stays above the noise floor, so the only thing that
	// can split the notes is the second attack's onset — verify it fired.
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

// TestTrackerKeyChangeSurvivesUnvoicedFlicker is the regression test for
// the hysteresis dead-end: an unvoiced hop used to reset the key-change
// (jump) candidacy while every voiced hop reset the unvoiced-run counter,
// so a re-fretted note flickering around the voicing threshold — voiced
// hops on the NEW key alternating with unvoiced dropouts — left the OLD
// note open forever and the new key was never recognized. An unvoiced hop
// carries no contradicting pitch evidence, so it must not cancel the
// jump; the new key has to win.
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

	const oldKey, newKey = 57, 64 // A3 -> E4
	// Sustain the old key long enough to open it and fill the median
	// filter.
	for i := 0; i < 10; i++ {
		feed(next(keyToFreq(oldKey), 0.95))
	}
	if cur, ok := tr.Current(); !ok || cur.Key != oldKey {
		t.Fatalf("Current = %+v, %v; want open note on key %d", cur, ok, oldKey)
	}
	// Re-fret to the new key, but weakly: every voiced hop on the new
	// key is followed by an unvoiced dropout.
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

// TestTrackerClosesOnSilence: a note followed by silence closes on the
// unvoiced run without needing Flush, stamped inside the sounding region.
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

// TestTrackerCurrentAndFlush: Current exposes the open note with End 0
// while it sounds; Flush closes it and Current goes empty.
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
