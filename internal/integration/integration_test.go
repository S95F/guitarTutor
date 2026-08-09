// Package integration holds cross-package tests: the cross-format fixture
// corpus invariant (ROADMAP Phase 0) and an end-to-end offline render
// through the real engine and synth.
package integration

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/midiimport"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/S95F/guitarTutor/internal/synth"
)

func testdata(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("missing fixture %s: %v", name, err)
	}
	return p
}

// TestCrossFormatFixture asserts the corpus invariant: the canonical riff
// parsed from .gtab and imported from .mid produce identical flattened
// events (pitch and timing; fingering differs — MIDI's is inferred).
func TestCrossFormatFixture(t *testing.T) {
	fromText, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatalf("parse .gtab: %v", err)
	}
	fromMIDI, warns, err := midiimport.ImportFile(testdata(t, "fixture_riff.mid"))
	if err != nil {
		t.Fatalf("import .mid: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("MIDI import warnings on the canonical fixture: %v", warns)
	}

	te, me := fromText.Events(), fromMIDI.Events()
	if len(te) != len(me) {
		t.Fatalf("event count: .gtab %d, .mid %d", len(te), len(me))
	}
	for i := range te {
		a, b := te[i], me[i]
		if a.Start != b.Start || a.End != b.End || a.Key != b.Key {
			t.Errorf("event %d: .gtab (start %d end %d key %d) != .mid (start %d end %d key %d)",
				i, a.Start, a.End, a.Key, b.Start, b.End, b.Key)
		}
	}
}

// TestEndToEndRender plays the canonical riff through the real engine and
// Karplus-Strong synth and checks the audio is sane: silence before the
// first onset, energy after it, and a decaying tail past the last note.
func TestEndToEndRender(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	const sr = 48000
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})
	eng.Play()

	// The riff is 16 quarters at 120 BPM = 8 s; render 10 s for the tail.
	total := 10 * sr
	left := make([]float32, total)
	right := make([]float32, total)
	const chunk = 1024
	for off := 0; off < total; off += chunk {
		n := off + chunk
		if n > total {
			n = total
		}
		eng.RenderFrames(left[off:n], right[off:n])
	}

	rms := func(from, to int) float64 {
		var sum float64
		for i := from; i < to; i++ {
			sum += float64(left[i])*float64(left[i]) + float64(right[i])*float64(right[i])
		}
		return math.Sqrt(sum / float64(2*(to-from)))
	}

	// First onset is at tick 0 — audio from the very start.
	if got := rms(0, sr/2); got < 1e-4 {
		t.Errorf("first half-second is silent (rms %g); expected the riff", got)
	}
	// Between 8 s and 8.5 s the last note (whole-note E) is still decaying.
	tail := rms(8*sr, 8*sr+sr/2)
	if tail <= 0 {
		t.Error("tail is dead silent; expected KS decay")
	}
	// By 9.5-10 s it should have decayed well below the sounding level.
	late := rms(int(9.5*sr), total)
	if late > tail {
		t.Errorf("tail is not decaying: late rms %g > early tail rms %g", late, tail)
	}
	// Nothing should clip.
	for i := 0; i < total; i++ {
		if left[i] > 1 || left[i] < -1 || right[i] > 1 || right[i] < -1 {
			t.Fatalf("sample %d clips: L=%g R=%g", i, left[i], right[i])
		}
	}
	// Playback must have reached the end and stopped (no loop set).
	if eng.Playing() {
		t.Error("engine still playing 2 s past the score end")
	}
}

// TestLoopedRenderIsPeriodic loops bar 2 of the canonical riff at half
// speed and asserts the engine actually loops (pass count advances) — the
// audio-level sample-accuracy assertions live in the engine's own tests.
func TestLoopedRenderIsPeriodic(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	const sr = 48000
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})
	eng.SetLoop(4*score.PPQ, 8*score.PPQ) // bar 2
	eng.SetTempoScale(0.5)
	eng.SeekTick(4 * score.PPQ)
	eng.Play()

	// One pass = 4 quarters at 60 effective BPM = 4 s. Render 13 s ≈ 3 passes.
	left := make([]float32, 13*sr)
	right := make([]float32, 13*sr)
	eng.RenderFrames(left, right)

	if got := eng.PassCount(); got < 3 {
		t.Errorf("pass count after 13 s of half-speed bar-2 looping = %d, want >= 3", got)
	}
	if !eng.Playing() {
		t.Error("looped playback stopped by itself")
	}
}
