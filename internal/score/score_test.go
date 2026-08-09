package score

import (
	"math"
	"testing"
)

func TestDurations(t *testing.T) {
	if Dotted(Eighth) != 720 {
		t.Errorf("dotted eighth = %d, want 720", Dotted(Eighth))
	}
	if Triplet(Quarter) != 640 {
		t.Errorf("quarter triplet = %d, want 640", Triplet(Quarter))
	}
}

func TestTempoMapConstant(t *testing.T) {
	m := TempoMap{{Tick: 0, USPerQuarter: USPerQuarter(120)}}
	// At 120 BPM one quarter (PPQ ticks) is 0.5 s.
	if got := m.TimeAt(PPQ); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("TimeAt(PPQ) = %v, want 0.5", got)
	}
	if got := m.TimeAt(4 * PPQ); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("TimeAt(4*PPQ) = %v, want 2.0", got)
	}
	if got := m.TickAt(0.5); got != PPQ {
		t.Errorf("TickAt(0.5) = %d, want %d", got, PPQ)
	}
}

func TestTempoMapChange(t *testing.T) {
	// 120 BPM for 2 quarters, then 60 BPM.
	m := TempoMap{
		{Tick: 0, USPerQuarter: USPerQuarter(120)},
		{Tick: 2 * PPQ, USPerQuarter: USPerQuarter(60)},
	}
	// 2 quarters at 120 (1.0 s) + 1 quarter at 60 (1.0 s).
	if got := m.TimeAt(3 * PPQ); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("TimeAt(3*PPQ) = %v, want 2.0", got)
	}
	if got := m.At(2*PPQ - 1); got != USPerQuarter(120) {
		t.Errorf("At before change = %d, want 120 BPM", got)
	}
	if got := m.At(2 * PPQ); got != USPerQuarter(60) {
		t.Errorf("At at change = %d, want 60 BPM", got)
	}
	// Round-trip a spread of ticks through TimeAt/TickAt.
	for _, tick := range []int64{0, 1, PPQ, 2*PPQ - 1, 2 * PPQ, 3 * PPQ, 10 * PPQ} {
		if got := m.TickAt(m.TimeAt(tick)); got != tick {
			t.Errorf("round trip %d -> %d", tick, got)
		}
	}
}

func TestMeterMap(t *testing.T) {
	m := MeterMap{{Tick: 0, Num: 4, Den: 4}, {Tick: 4 * PPQ, Num: 6, Den: 8}}
	if got := m.At(0); got.Num != 4 || got.Den != 4 {
		t.Errorf("At(0) = %d/%d, want 4/4", got.Num, got.Den)
	}
	got := m.At(4 * PPQ)
	if got.Num != 6 || got.Den != 8 {
		t.Errorf("At(4*PPQ) = %d/%d, want 6/8", got.Num, got.Den)
	}
	if got.BeatLen() != 480 {
		t.Errorf("6/8 beat len = %d, want 480", got.BeatLen())
	}
}

func TestPitchDerivation(t *testing.T) {
	tr := &Track{Tuning: StandardTuning}
	if p := tr.Pitch(Note{String: 6, Fret: 0}); p != 40 {
		t.Errorf("low E open = %d, want 40", p)
	}
	if p := tr.Pitch(Note{String: 1, Fret: 5}); p != 69 {
		t.Errorf("high E fret 5 = %d, want 69 (A4)", p)
	}
	tr.Capo = 2
	if p := tr.Pitch(Note{String: 6, Fret: 0}); p != 42 {
		t.Errorf("low E open with capo 2 = %d, want 42", p)
	}
}

func TestBuildersAndValidate(t *testing.T) {
	s := &Score{
		Tempos: TempoMap{{Tick: 0, USPerQuarter: USPerQuarter(120)}},
		Meters: MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &Track{Name: "Guitar", Tuning: StandardTuning}
	s.Tracks = []*Track{tr}

	b1 := tr.AppendBar(4, 4)
	for i := 0; i < 4; i++ {
		b1.AddBeat(Quarter, Note{String: 6, Fret: 0})
	}
	b2 := tr.AppendBar(4, 4)
	b2.AddBeat(Half, Note{String: 5, Fret: 2})
	b2.AddBeat(Half, Note{String: 5, Fret: 2, Tied: true})

	if b2.Start != Whole {
		t.Errorf("bar 2 start = %d, want %d", b2.Start, int64(Whole))
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if end := s.End(); end != 2*Whole {
		t.Errorf("End = %d, want %d", end, int64(2*Whole))
	}

	// Break it: overfill a bar.
	b3 := tr.AppendBar(4, 4)
	b3.AddBeat(Whole, Note{String: 1, Fret: 0})
	b3.AddBeat(Quarter, Note{String: 1, Fret: 0})
	if err := s.Validate(); err == nil {
		t.Error("Validate accepted an overfilled bar")
	}
}

func TestEventsTieMerge(t *testing.T) {
	s := &Score{
		Tempos: TempoMap{{Tick: 0, USPerQuarter: USPerQuarter(120)}},
		Meters: MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &Track{Tuning: StandardTuning}
	s.Tracks = []*Track{tr}
	b := tr.AppendBar(4, 4)
	b.AddBeat(Half, Note{String: 5, Fret: 2})
	b.AddBeat(Half, Note{String: 5, Fret: 2, Tied: true})

	evs := s.Events()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1 (tie should merge)", len(evs))
	}
	if evs[0].Start != 0 || evs[0].End != Whole {
		t.Errorf("merged event spans [%d,%d), want [0,%d)", evs[0].Start, evs[0].End, int64(Whole))
	}
	if evs[0].Key != 47 {
		t.Errorf("key = %d, want 47 (B2)", evs[0].Key)
	}
}

func TestEventsTieMismatchDegrades(t *testing.T) {
	s := &Score{
		Tempos: TempoMap{{Tick: 0, USPerQuarter: USPerQuarter(120)}},
		Meters: MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &Track{Tuning: StandardTuning}
	s.Tracks = []*Track{tr}
	b := tr.AppendBar(4, 4)
	b.AddBeat(Half, Note{String: 5, Fret: 2})
	// Tie marked but fret differs: must become a fresh attack, not merge.
	b.AddBeat(Half, Note{String: 5, Fret: 4, Tied: true})

	evs := s.Events()
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 (mismatched tie must not merge)", len(evs))
	}
}

func TestEventsChordAndOrder(t *testing.T) {
	s := &Score{
		Tempos: TempoMap{{Tick: 0, USPerQuarter: USPerQuarter(120)}},
		Meters: MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &Track{Tuning: StandardTuning}
	s.Tracks = []*Track{tr}
	b := tr.AppendBar(4, 4)
	b.AddBeat(Half, Note{String: 6, Fret: 0}, Note{String: 5, Fret: 2}, Note{String: 4, Fret: 2})
	b.AddBeat(Half) // rest

	evs := s.Events()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	// E5 power chord: E2 B2 E3.
	want := []int{40, 47, 52}
	for i, ev := range evs {
		if ev.Key != want[i] && i < len(want) {
			t.Errorf("event %d key = %d, want %d", i, ev.Key, want[i])
		}
		if ev.Start != 0 || ev.End != Half {
			t.Errorf("event %d spans [%d,%d), want [0,%d)", i, ev.Start, ev.End, int64(Half))
		}
	}
}
