package ui

// Windowing is untestable headlessly, so these tests drive the extracted
// input logic (loopSetA/loopSetB, barAt) directly against a real engine.

import (
	"testing"

	"github.com/S95F/guitarTutor/internal/engine"
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
