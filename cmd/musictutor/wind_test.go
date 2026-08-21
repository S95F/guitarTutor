package main

// The per-instrument live wiring: a wind session's miss-finalization lag
// covers the longest held note at the slowest practice speed, and its
// detector search range follows the horn rather than the guitar defaults.

import (
	"math"
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
)

// windBarScore is a one-bar soprano sax piece whose longest note is a
// half note — 1 s at the fixture's 120 BPM.
func windBarScore(t *testing.T) *score.Score {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	w := score.WindByName("soprano sax")
	tr := &score.Track{Name: "sax", Wind: w, Program: w.Program}
	sc.Tracks = append(sc.Tracks, tr)
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 1, Fret: 16})
	b.AddBeat(score.Quarter, score.Note{String: 1, Fret: 18})
	b.AddBeat(score.Half, score.Note{String: 1, Fret: 21})
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

// TestAdvanceLagCoversTheHeldNote: on a wind track the lag is the longest
// note at the slowest transport speed plus a settling second — a half
// note at 120 BPM is 1 s, held for 4 s at quarter speed, so 5 s of lag —
// while a fretted track keeps the four-second decay-backed default.
func TestAdvanceLagCoversTheHeldNote(t *testing.T) {
	if got := advanceLagFor(oneBarScore(t), 0); got != advanceLagFrames {
		t.Errorf("fretted lag = %d frames, want the default %d", got, int64(advanceLagFrames))
	}
	want := int64((1.0/minScale + 1) * sampleRate)
	if got := advanceLagFor(windBarScore(t), 0); got != want {
		t.Errorf("wind lag = %d frames, want %d (1 s note at quarter speed, plus 1 s)", got, want)
	}
}

// TestAdvanceLagNeverShrinks: a wind piece of only short notes keeps at
// least the default — a shorter lag would finalize misses before the
// tracker can close anything.
func TestAdvanceLagNeverShrinks(t *testing.T) {
	sc := windBarScore(t)
	// Rewrite the bar as quarters only.
	tr := sc.Tracks[0]
	tr.Bars = nil
	b := tr.AppendBar(4, 4)
	for i := 0; i < 4; i++ {
		b.AddBeat(score.Quarter, score.Note{String: 1, Fret: 16})
	}
	if got := advanceLagFor(sc, 0); got != advanceLagFrames {
		t.Errorf("short-note wind lag = %d, want the %d floor", got, int64(advanceLagFrames))
	}
}

// TestPitchConfigFollowsTheInstrument: a wind session's search range is
// fitted to the horn's sounding compass; a guitar session passes the zero
// config so internal/live applies the defaults at the negotiated rate.
func TestPitchConfigFollowsTheInstrument(t *testing.T) {
	if got := pitchConfigFor(oneBarScore(t).Tracks[0]); got != (pitch.Config{}) {
		t.Errorf("fretted pitch config = %+v, want the zero config", got)
	}
	w := score.WindByName("soprano sax")
	got := pitchConfigFor(windBarScore(t).Tracks[0])
	// The floor sits under the horn's low Ab3 (207.65 Hz), the ceiling
	// over its top E6 (1318.5 Hz) — the guitar default's 1500 Hz ceiling
	// leaves altissimo unvoiced, which is why this is fitted at all.
	lowHz := 440 * math.Pow(2, float64(w.LowSounding-69)/12)
	topHz := 440 * math.Pow(2, float64(w.LowSounding+w.Span-69)/12)
	if got.MinHz >= lowHz {
		t.Errorf("MinHz %.1f is not below the horn's lowest %.1f", got.MinHz, lowHz)
	}
	if got.MaxHz <= topHz {
		t.Errorf("MaxHz %.1f is not above the horn's top %.1f", got.MaxHz, topHz)
	}
}
