package main

import (
	"math"
	"strings"
	"testing"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/synth"
)

// oneBarScore builds a validated one-bar 4/4 piece at 120 BPM: exactly two
// seconds long at scale 1.0 (PPQ*4 ticks at 500000 us per quarter).
func oneBarScore(t *testing.T) *score.Score {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "guitar", Tuning: score.StandardTuning}
	sc.Tracks = append(sc.Tracks, tr)
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3})
	b.AddBeat(score.Half, score.Note{String: 5, Fret: 5})
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

func newEngine(sc *score.Score, opts engine.Options) *engine.Engine {
	opts.SampleRate = sampleRate
	opts.Voices = synth.NewPluck
	return engine.New(sc, opts)
}

// TestValidateScale is the regression test for finding A1's flag half:
// -scale 0 and negative values used to panic runRender (division by zero /
// negative allocation) and out-of-range values silently clamped.
func TestValidateScale(t *testing.T) {
	for _, s := range []float64{0.25, 0.7, 1.0, 2.0} {
		if err := validateScale(s); err != nil {
			t.Errorf("validateScale(%v) = %v, want nil", s, err)
		}
	}
	for _, s := range []float64{0, -1, 0.1, 0.24, 2.01, 3, math.NaN(), math.Inf(1)} {
		err := validateScale(s)
		if err == nil {
			t.Errorf("validateScale(%v) = nil, want error", s)
			continue
		}
		if !strings.Contains(err.Error(), "0.25") || !strings.Contains(err.Error(), "2") {
			t.Errorf("validateScale(%v) error %q does not name the accepted range", s, err)
		}
	}
}

// TestEnsureTracks is the regression test for finding A2: a valid track-less
// score (reachable via MIDI import) used to crash play instead of erroring.
func TestEnsureTracks(t *testing.T) {
	empty := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	if err := empty.Validate(); err != nil {
		t.Fatalf("track-less score should validate: %v", err)
	}
	if err := ensureTracks(empty, "play"); err == nil {
		t.Fatal("ensureTracks(track-less score) = nil, want error")
	}
	if err := ensureTracks(oneBarScore(t), "render"); err != nil {
		t.Fatalf("ensureTracks(one-track score) = %v, want nil", err)
	}
}

// TestRenderAllClampedScale is the regression test for finding A1: the old
// runRender sized its buffer by the raw -scale flag while the engine clamps
// to [0.25, 2.0]. renderAll must yield the full piece at the engine's actual
// speed plus the exact tail, for the extreme accepted scale.
func TestRenderAllClampedScale(t *testing.T) {
	sc := oneBarScore(t)
	eng := newEngine(sc, engine.Options{})
	eng.SetTempoScale(0.25) // 2 s piece -> 8 s of audio

	const tailSec = 2.0
	left, right, err := renderAll(eng, tailSec, maxRenderFrames)
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}
	if len(left) != len(right) {
		t.Fatalf("channel length mismatch: %d vs %d", len(left), len(right))
	}

	const chunk = 4800 // renderAll's block size: max overshoot per pass
	wantPlay := 8 * sampleRate
	wantTail := int(tailSec * sampleRate)
	if len(left) < wantPlay+wantTail {
		t.Fatalf("rendered %d frames, want at least %d (piece truncated)", len(left), wantPlay+wantTail)
	}
	if len(left) >= wantPlay+wantTail+chunk {
		t.Fatalf("rendered %d frames, want under %d (runaway)", len(left), wantPlay+wantTail+chunk)
	}
	if eng.Playing() {
		t.Fatal("engine still playing after renderAll")
	}

	// The last beat (a half note, second half of the piece) must actually
	// sound: the old truncation cut it off entirely at small scales.
	var sum float64
	for _, v := range left[wantPlay/2 : wantPlay] {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		t.Fatal("second half of the piece is pure silence; piece truncated")
	}
}

// TestRenderAllCountInAndTempoChange is the regression test for finding A4:
// the old count-in-seconds formula integrated tempo changes inside the first
// beat (the engine uses the constant tick-0 tempo) and divided by scale in
// the wrong place, truncating the tail. renderAll must include the full
// count-in, both tempo segments, and the exact tail.
func TestRenderAllCountInAndTempoChange(t *testing.T) {
	sc := oneBarScore(t)
	// Tempo doubles mid-first-beat: the exact case the formula got wrong.
	sc.Tempos = score.TempoMap{
		{Tick: 0, USPerQuarter: 500000},
		{Tick: 480, USPerQuarter: 250000},
	}
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	eng := newEngine(sc, engine.Options{CountInBeats: 2})

	const tailSec = 0.5
	left, _, err := renderAll(eng, tailSec, maxRenderFrames)
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}

	// Engine semantics: count-in beats use the tick-0 tempo (one beat =
	// PPQ ticks * 500000us/PPQ = 0.5 s = 24000 frames), then the piece is
	// 480 ticks at 500000 (12000 frames) + 3360 ticks at 250000 (42000
	// frames).
	const chunk = 4800
	wantPlay := 2*24000 + 12000 + 42000
	wantTail := int(tailSec * sampleRate)
	if len(left) < wantPlay+wantTail {
		t.Fatalf("rendered %d frames, want at least %d (count-in or tail truncated)", len(left), wantPlay+wantTail)
	}
	if len(left) >= wantPlay+wantTail+chunk {
		t.Fatalf("rendered %d frames, want under %d (runaway)", len(left), wantPlay+wantTail+chunk)
	}
}

// TestRenderAllRunawayGuard: an engine that never stops (loop enabled) must
// hit the frame cap and error instead of growing the buffers forever.
func TestRenderAllRunawayGuard(t *testing.T) {
	sc := oneBarScore(t)
	eng := newEngine(sc, engine.Options{})
	eng.SetLoop(0, sc.End())

	if _, _, err := renderAll(eng, 0, sampleRate); err == nil {
		t.Fatal("renderAll with a looping engine returned nil error, want runaway-guard error")
	}
}
