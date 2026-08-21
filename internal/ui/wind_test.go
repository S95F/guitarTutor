package ui

// The wind practice view: the pitch ladder that replaces string lanes, the
// written-pitch names a transposing player reads, and the proof that the
// band geometry every layout test pins for guitars holds identically for a
// track with no strings at all.

import (
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/practice"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

// newAppWind builds an App over a validated soprano sax score: one note a
// bar walking up from the horn's low written C5 (sounding Bb4).
func newAppWind(t *testing.T, bars int) *App {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	w := score.WindByName("soprano sax")
	if w == nil {
		t.Fatal("no soprano sax in the registry")
	}
	tr := &score.Track{Name: "sax", Wind: w, Program: w.Program}
	sc.Tracks = append(sc.Tracks, tr)
	for i := 0; i < bars; i++ {
		b := tr.AppendBar(4, 4)
		b.AddBeat(score.Whole, score.Note{String: 1, Fret: 16 + i})
	}
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	eng := engine.New(sc, engine.Options{Voices: synth.NewBuiltin})
	return New(eng, sc, 0)
}

// TestWindBandMatchesSixStringGeometry: the wind band borrows exactly a
// six-string staff's height, so every overlay and control positioned from
// tabBottom sits where the guitar layout tests already prove it safe.
func TestWindBandMatchesSixStringGeometry(t *testing.T) {
	wind := newAppWind(t, 4)
	guitar := newApp(t, 4)
	if got, want := wind.tabBottom(), guitar.tabBottom(); got != want {
		t.Errorf("wind tabBottom = %.0f, six-string = %.0f; the two bands must agree", got, want)
	}
	bands := practiceBands(wind)
	for i := 1; i < len(bands); i++ {
		if bands[i].top < bands[i-1].bottom {
			t.Errorf("wind track: %q overlaps %q", bands[i-1].name, bands[i].name)
		}
	}
}

// TestWindLadderPlacesPitch: higher written pitch sits higher on screen,
// every used note stays inside the band, and the compass never collapses
// below an octave.
func TestWindLadderPlacesPitch(t *testing.T) {
	a := newAppWind(t, 4)
	tr := a.displayed()
	l := windLadderFor(tr)
	if l.hi-l.lo < 12 {
		t.Errorf("ladder compass %d..%d spans %d semitones, want at least an octave", l.lo, l.hi, l.hi-l.lo)
	}
	top, bottom := float64(tabTop), a.tabBottom()
	prevY := bottom + 1
	for i := 0; i < 4; i++ {
		written := tr.Wind.Written(tr.Pitch(score.Note{String: 1, Fret: 16 + i}))
		y := l.y(written, top, bottom)
		if y < top || y > bottom {
			t.Errorf("written %d drawn at %.1f, outside the band %.0f..%.0f", written, y, top, bottom)
		}
		if y >= prevY {
			t.Errorf("written %d drawn at %.1f, not above the lower note at %.1f", written, y, prevY)
		}
		prevY = y
	}
}

// TestWindLadderEmptyTrackShowsTheInstrument: a bar-less wind track still
// has a compass — the instrument's own standard range.
func TestWindLadderEmptyTrackShowsTheInstrument(t *testing.T) {
	a := newAppWind(t, 0)
	tr := a.displayed()
	l := windLadderFor(tr)
	w := tr.Wind
	if l.lo > w.Written(w.LowSounding) || l.hi < w.Written(w.LowSounding+w.Span) {
		t.Errorf("empty track ladder %d..%d does not cover the %s's written range %d..%d",
			l.lo, l.hi, w.Name, w.Written(w.LowSounding), w.Written(w.LowSounding+w.Span))
	}
}

// TestWindNoteLabelIsWrittenPitch: the glyph is the name the player reads
// — written pitch, a major second above what a B-flat soprano sounds —
// with the tab's own tie prefix.
func TestWindNoteLabelIsWrittenPitch(t *testing.T) {
	tr := newAppWind(t, 1).displayed()
	// Fret 16 sounds C5 (72); a soprano sax player reads D5.
	if got := windNoteLabel(tr, score.Note{String: 1, Fret: 16}); got != "D5" {
		t.Errorf("label = %q, want D5", got)
	}
	if got := windNoteLabel(tr, score.Note{String: 1, Fret: 16, Tied: true}); got != "~D5" {
		t.Errorf("tied label = %q, want ~D5", got)
	}
}

// TestDisplayNameTransposes: the tuner names notes as the practiced
// player reads them — written on a wind track, concert on a guitar.
func TestDisplayNameTransposes(t *testing.T) {
	wind := newAppWind(t, 1)
	if got := wind.displayName(72); got != "D5" {
		t.Errorf("wind displayName(72) = %q, want the written D5", got)
	}
	guitar := newApp(t, 1)
	if got := guitar.displayName(72); got != "C5" {
		t.Errorf("guitar displayName(72) = %q, want the concert C5", got)
	}
}

// TestWindVerdictKeying: wind events carry String 1, and the verdict
// pipeline keys them exactly as it keys a fretted note.
func TestWindVerdictKeying(t *testing.T) {
	a := newAppWind(t, 1)
	a.OfferResults([]practice.NoteResult{result(0, 1, practice.VerdictHit)})
	a.syncLive()
	if v, ok := a.verdictAt(0, 1, 0); !ok || v != practice.VerdictHit {
		t.Errorf("verdictAt(0, 1) = %v, %v; want the offered Hit", v, ok)
	}
}

// TestNewEditorForRefusesWindByName: the notation grid cannot hold a wind
// part, and the refusal must name the instrument and the fallback so the
// caller can route the piece to the text view.
func TestNewEditorForRefusesWindByName(t *testing.T) {
	sc := newAppWind(t, 1).sc
	if _, err := NewEditorFor(nil, sc, ""); err == nil {
		t.Fatal("NewEditorFor accepted a wind piece the grid cannot draw")
	} else {
		for _, want := range []string{"soprano sax", "text view"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not mention %q", err, want)
			}
		}
	}
}

// TestWindTextPaneIsTheWholeEditor: a wind piece in the text view can run
// file actions (applyTextThen) and toggle F2 without ever landing on the
// grid, and Escape leaves cleanly when the text is still exactly the file.
func TestWindTextPaneIsTheWholeEditor(t *testing.T) {
	src := "\\instrument soprano sax\nD5.4 E5.4 G5.2 |\n"
	e := NewEditorForText(nil, []byte(src), "sax.gtab")

	acted := false
	e.applyTextThen(func() { acted = true })
	if !acted {
		t.Fatal("applyTextThen never ran the action on a valid wind text")
	}
	if e.text == nil {
		t.Fatal("applyTextThen closed the text view; a wind piece has no other editor")
	}
	if w := e.doc.Wind(); w == nil {
		t.Fatal("the applied document lost its instrument")
	}

	e.toggleText()
	if e.text == nil {
		t.Fatal("F2 closed the text view on a wind piece")
	}
	if msg, _ := e.message(); !strings.Contains(msg, "soprano sax") {
		t.Errorf("staying in the text view said %q, want it to name the instrument", msg)
	}

	// The text is exactly the file's own, so nothing is unsaved: Escape
	// leaves, no prompt.
	if err := e.escapeText(); err != errQuit {
		t.Errorf("escapeText = %v, want errQuit (nothing typed, nothing to lose)", err)
	}
}
