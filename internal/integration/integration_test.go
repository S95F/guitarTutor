package integration

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/gpimport"
	"github.com/S95F/musicTutor/internal/midiimport"
	"github.com/S95F/musicTutor/internal/mxlimport"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/synth"
)

func testdata(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("missing fixture %s: %v", name, err)
	}
	return p
}

func TestCrossFormatFixture(t *testing.T) {
	ref, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatalf("parse .gtab: %v", err)
	}
	re := ref.Events()

	type imported struct {
		name      string
		events    []score.NoteEvent
		warns     []string
		fingering bool
	}
	var all []imported
	for _, c := range []struct {
		file      string
		fingering bool
		load      func(string) (*score.Score, []string, error)
	}{
		{"fixture_riff.mid", false, midiimport.ImportFile},
		{"fixture_riff.gp", true, gpimport.ImportFile},
		{"fixture_riff.musicxml", true, mxlimport.ImportFile},
		{"fixture_riff.mxl", true, mxlimport.ImportFile},
	} {
		sc, warns, err := c.load(testdata(t, c.file))
		if err != nil {
			t.Fatalf("import %s: %v", c.file, err)
		}
		all = append(all, imported{c.file, sc.Events(), warns, c.fingering})
	}

	for _, im := range all {
		if len(im.warns) != 0 {
			t.Errorf("%s: warnings on the canonical fixture: %v", im.name, im.warns)
		}
		if len(im.events) != len(re) {
			t.Fatalf("%s: %d events, .gtab has %d", im.name, len(im.events), len(re))
		}
		for i := range re {
			a, b := re[i], im.events[i]
			if a.Start != b.Start || a.End != b.End || a.Key != b.Key {
				t.Errorf("%s event %d: (start %d end %d key %d), .gtab has (start %d end %d key %d)",
					im.name, i, b.Start, b.End, b.Key, a.Start, a.End, a.Key)
			}
			if im.fingering && (a.String != b.String || a.Fret != b.Fret) {
				t.Errorf("%s event %d: fingering %d/%d, .gtab has %d/%d",
					im.name, i, b.String, b.Fret, a.String, a.Fret)
			}
		}
	}
}

func TestEndToEndRender(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	const sr = 48000
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})
	eng.Play()

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

	if got := rms(0, sr/2); got < 1e-4 {
		t.Errorf("first half-second is silent (rms %g); expected the riff", got)
	}

	tail := rms(8*sr, 8*sr+sr/2)
	if tail <= 0 {
		t.Error("tail is dead silent; expected KS decay")
	}

	late := rms(int(9.5*sr), total)
	if late > tail {
		t.Errorf("tail is not decaying: late rms %g > early tail rms %g", late, tail)
	}

	for i := 0; i < total; i++ {
		if left[i] > 1 || left[i] < -1 || right[i] > 1 || right[i] < -1 {
			t.Fatalf("sample %d clips: L=%g R=%g", i, left[i], right[i])
		}
	}

	if eng.Playing() {
		t.Error("engine still playing 2 s past the score end")
	}
}

func TestLoopedRenderIsPeriodic(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	const sr = 48000
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})
	eng.SetLoop(4*score.PPQ, 8*score.PPQ)
	eng.SetTempoScale(0.5)
	eng.SeekTick(4 * score.PPQ)
	eng.Play()

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
