package mxlimport

import (
	"strings"
	"testing"

	"github.com/S95F/guitarTutor/internal/score"
)

// wrapWindPart builds a one-part document whose declaration carries a
// part name and, when program > 0, a 1-based <midi-program> — the two
// signals wind classification reads.
func wrapWindPart(name string, program int, measures ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><score-partwise version="4.0">`)
	b.WriteString(`<part-list><score-part id="P1"><part-name>` + name + `</part-name>`)
	if program > 0 {
		b.WriteString(`<midi-instrument id="P1-I1"><midi-program>` + itoa(program) + `</midi-program></midi-instrument>`)
	}
	b.WriteString(`</score-part></part-list><part id="P1">`)
	for i, m := range measures {
		b.WriteString(`<measure number="` + itoa(i+1) + `">` + m + `</measure>`)
	}
	b.WriteString(`</part></score-partwise>`)
	return []byte(b.String())
}

// noWindInferred fails on any Inferred note in the track: a wind lane is
// arithmetic, not a heuristic, so nothing on it is ever a guess.
func noWindInferred(t *testing.T, tr *score.Track) {
	t.Helper()
	for _, bar := range tr.Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if n.Inferred {
					t.Errorf("wind note at tick %d marked Inferred; the lane is arithmetic", beat.Start)
				}
			}
		}
	}
}

// TestWindPartByProgram: <midi-program>65</midi-program> is soprano sax
// (MusicXML programs are 1-based, the model's 0-based). The B-flat
// transposition arrives through <transpose> like any other part — <pitch>
// is the WRITTEN pitch and the model stores what SOUNDS — so written D5
// (74) under chromatic -2 lands at concert C5 (72): string 1, fret
// 72-56=16. No extra wind-specific transposition may apply on top;
// WindInstrument.Transpose is display-only.
func TestWindPartByProgram(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<transpose><diatonic>-1</diatonic><chromatic>-2</chromatic></transpose></attributes>` +
		note("D", 5, 1920, -1, 0, "")
	s, warns, err := Import(wrapWindPart("Part 1", 65, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	tr := s.Tracks[0]
	if tr.Wind != score.WindByName("soprano sax") {
		t.Fatalf("Wind = %v, want the registry's soprano sax", tr.Wind)
	}
	if tr.Tuning != nil || tr.Capo != 0 {
		t.Errorf("Tuning = %v Capo = %d, want nil and 0 on a wind track", tr.Tuning, tr.Capo)
	}
	if tr.Program != 64 {
		t.Errorf("Program = %d, want the file's declared 64", tr.Program)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 72 || evs[0].String != 1 || evs[0].Fret != 16 {
		t.Fatalf("events = %v, want one note (key 72, string 1, fret 16)", evs)
	}
	noWindInferred(t, tr)
}

// TestWindPartByName: no <midi-program> at all — files that carry no MIDI
// block — so the part name is the classifier, and the track takes the
// instrument's own program rather than the importer's guitar default.
func TestWindPartByName(t *testing.T) {
	m := attrs44div480 + note("C", 5, 1920, -1, 0, "")
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	tr := s.Tracks[0]
	if tr.Wind != score.WindByName("soprano sax") {
		t.Fatalf("Wind = %v, want the registry's soprano sax (classified by name)", tr.Wind)
	}
	if tr.Program != 64 {
		t.Errorf("Program = %d, want the soprano sax default 64 (the file declared none)", tr.Program)
	}
	if tr.Tuning != nil {
		t.Errorf("Tuning = %v, want nil", tr.Tuning)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 72 || evs[0].String != 1 || evs[0].Fret != 16 {
		t.Fatalf("events = %v, want one note (key 72, string 1, fret 16)", evs)
	}
}

// TestWindStaffTuningOverridesClassification: a wind program plus an
// explicit tab-staff tuning. Someone authored string lines — stronger
// evidence than a program number — so the part stays fretted, with a
// warning naming the conflict.
func TestWindStaffTuningOverridesClassification(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<staff-details><staff-lines>6</staff-lines>` + staffTunings(6) + `</staff-details></attributes>` +
		note("E", 2, 1920, 6, 0, "")
	s, warns, err := Import(wrapWindPart("Sax", 65, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := findWarn(warns, "the tab staff wins"); len(got) != 1 {
		t.Errorf("warnings = %v, want one naming the program/staff-tuning conflict", warns)
	}
	tr := s.Tracks[0]
	if tr.Wind != nil {
		t.Errorf("Wind = %v, want nil (the tab staff overrides)", tr.Wind)
	}
	if len(tr.Tuning) != 6 {
		t.Errorf("tuning has %d strings, want the declared 6", len(tr.Tuning))
	}
}

// TestWindChordKeepsHighest: a monophonic instrument cannot voice a
// <chord>; the highest note is kept — the melody rides on top of a
// voicing — and the resolution is warned about once.
func TestWindChordKeepsHighest(t *testing.T) {
	m := attrs44div480 +
		note("A", 4, 1920, -1, 0, "") +
		note("D", 5, 1920, -1, 0, "<chord/>")
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := findWarn(warns, "plays one note at a time"); len(got) != 1 {
		t.Errorf("warnings = %v, want one about the resolved chord", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 74 || evs[0].String != 1 || evs[0].Fret != 18 {
		t.Fatalf("events = %v, want only the chord's top note (key 74, fret 18)", evs)
	}
}

// TestWindTechnicalIgnored: an authored guitar fingering on a wind part
// has no strings to land on, so the pitch alone decides the lane position
// — honoring the authored fret 0 here would move the note — and the
// deviation is warned about.
func TestWindTechnicalIgnored(t *testing.T) {
	m := attrs44div480 + note("E", 4, 1920, 1, 0, "")
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := findWarn(warns, "ignored authored <technical>"); len(got) != 1 {
		t.Errorf("warnings = %v, want one about the ignored fingering", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 64 || evs[0].String != 1 || evs[0].Fret != 8 {
		t.Fatalf("events = %v, want the pitch-derived placement (key 64, string 1, fret 8)", evs)
	}
}

// TestWindOutOfRangeDropped: MusicXML pitch (written plus <transpose>) is
// authoritative, and octave-rewriting an out-of-range note would silently
// change the music — so it is dropped and named. Below, the floor is the
// instrument's lowest note; above, MIDI 127 is the only ceiling
// (altissimo imports fine, so nothing between LowSounding+Span and 127 is
// rejected).
func TestWindOutOfRangeDropped(t *testing.T) {
	m := attrs44div480 +
		note("G", 3, 480, -1, 0, "") + // 55: a semitone under the soprano sax's low A-flat (56)
		note("A", 9, 480, -1, 0, "") + // 129: past MIDI's ceiling
		note("C", 4, 960, -1, 0, "") // 60: in range; keeps the part alive
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	below := findWarn(warns, "below the soprano sax's lowest note (key 56)")
	if len(below) != 1 || !strings.Contains(below[0], "(key 55)") {
		t.Errorf("warnings = %v, want one naming the dropped key 55 and the floor 56", warns)
	}
	if got := findWarn(warns, "past MIDI 127"); len(got) != 1 {
		t.Errorf("warnings = %v, want one about the note past MIDI 127", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 60 || evs[0].Fret != 4 {
		t.Fatalf("events = %v, want only the in-range note (key 60, fret 4)", evs)
	}
}
