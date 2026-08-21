package main

import (
	"math"
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
)

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

func TestAdvanceLagCoversTheHeldNote(t *testing.T) {
	if got := advanceLagFor(oneBarScore(t), 0); got != advanceLagFrames {
		t.Errorf("fretted lag = %d frames, want the default %d", got, int64(advanceLagFrames))
	}
	want := int64((1.0/minScale + 1) * sampleRate)
	if got := advanceLagFor(windBarScore(t), 0); got != want {
		t.Errorf("wind lag = %d frames, want %d (1 s note at quarter speed, plus 1 s)", got, want)
	}
}

func TestAdvanceLagNeverShrinks(t *testing.T) {
	sc := windBarScore(t)

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

func TestPitchConfigFollowsTheInstrument(t *testing.T) {
	if got := pitchConfigFor(oneBarScore(t).Tracks[0]); got != (pitch.Config{}) {
		t.Errorf("fretted pitch config = %+v, want the zero config", got)
	}
	w := score.WindByName("soprano sax")
	got := pitchConfigFor(windBarScore(t).Tracks[0])

	lowHz := 440 * math.Pow(2, float64(w.LowSounding-69)/12)
	topHz := 440 * math.Pow(2, float64(w.LowSounding+w.Span-69)/12)
	if got.MinHz >= lowHz {
		t.Errorf("MinHz %.1f is not below the horn's lowest %.1f", got.MinHz, lowHz)
	}
	if got.MaxHz <= topHz {
		t.Errorf("MaxHz %.1f is not above the horn's top %.1f", got.MaxHz, topHz)
	}
}
