package mxlimport

import (
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

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

func TestWindOutOfRangeDropped(t *testing.T) {
	m := attrs44div480 +
		note("G", 3, 480, -1, 0, "") +
		note("A", 9, 480, -1, 0, "") +
		note("C", 4, 960, -1, 0, "")
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

func TestWindNameNeverOverridesADeclaredProgram(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time></attributes>` +
		note("E", 4, 960, -1, 0, "") +
		note("C", 5, 960, -1, 0, `<chord/>`) +
		note("G", 4, 960, -1, 0, "") +
		note("G", 4, 960, -1, 0, "")
	s, _, err := Import(wrapWindPart("Flute", 26, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	tr := s.Tracks[0]
	if tr.Wind != nil {
		t.Fatalf("a declared guitar program was overridden by the part name: track is a %s", tr.Wind.Name)
	}
	if len(tr.Tuning) == 0 {
		t.Error("the fretted part lost its tuning")
	}
}

func TestWindChordKeepsHighestPlayable(t *testing.T) {

	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time></attributes>` +
		note("D", 5, 1920, -1, 0, "") +
		note("G", 3, 1920, -1, 0, `<chord/>`)
	s, warns, err := Import(wrapWindPart("Part 1", 65, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	tr := s.Tracks[0]
	var keys []int
	for _, bar := range tr.Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				keys = append(keys, tr.Pitch(n))
			}
		}
	}
	if len(keys) != 1 || keys[0] != 74 {
		t.Fatalf("kept keys %v, want just the playable 74 (D5)", keys)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "below the soprano sax's lowest note") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %v never explain the dropped G3", warns)
	}
}

func slurNote(step string, octave, dur int, extra, slurs string) string {
	return `<note>` + extra +
		`<pitch><step>` + step + `</step><octave>` + string(rune('0'+octave)) + `</octave></pitch>` +
		`<duration>` + itoa(dur) + `</duration><voice>1</voice>` +
		`<notations>` + slurs + `</notations></note>`
}

func eventTechs(t *testing.T, s *score.Score, want []score.Technique) {
	t.Helper()
	evs := s.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(evs), len(want), evs)
	}
	for i, ev := range evs {
		if ev.Tech != want[i] {
			t.Errorf("event %d (key %d) Tech = %v, want %v", i, ev.Key, ev.Tech, want[i])
		}
	}
}

func TestWindSlurPhrase(t *testing.T) {
	m := attrs44div480 +
		slurNote("C", 5, 480, "", `<slur type="start"/>`) +
		slurNote("D", 5, 480, "", "") +
		slurNote("E", 5, 960, "", `<slur type="stop"/>`)
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	eventTechs(t, s, []score.Technique{0, score.TechSlur, score.TechSlur})
}

func TestWindSeparateSlurs(t *testing.T) {
	m := attrs44div480 +
		slurNote("C", 5, 480, "", `<slur type="start"/>`) +
		slurNote("D", 5, 480, "", `<slur type="stop"/>`) +
		slurNote("E", 5, 480, "", `<slur type="start"/>`) +
		slurNote("F", 5, 480, "", `<slur type="stop"/>`)
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	eventTechs(t, s, []score.Technique{0, score.TechSlur, 0, score.TechSlur})
}

func TestWindOverlappingNumberedSlurs(t *testing.T) {
	m := attrs44div480 +
		slurNote("C", 5, 480, "", `<slur type="start" number="1"/>`) +
		slurNote("D", 5, 480, "", `<slur type="start" number="2"/>`) +
		slurNote("E", 5, 480, "", `<slur type="stop" number="1"/>`) +
		slurNote("F", 5, 240, "", `<slur type="stop" number="2"/>`) +
		slurNote("G", 5, 240, "", "")
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	eventTechs(t, s, []score.Technique{0, score.TechSlur, score.TechSlur, score.TechSlur, 0})
}

func TestWindSlurAcrossCollapsedChords(t *testing.T) {
	m := attrs44div480 +
		slurNote("D", 5, 960, "", `<slur type="start"/>`) +
		slurNote("A", 4, 960, "<chord/>", "") +
		slurNote("C", 5, 960, "", `<slur type="stop"/>`) +
		slurNote("E", 5, 960, "<chord/>", "")
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := findWarn(warns, "plays one note at a time"); len(got) != 1 {
		t.Errorf("warnings = %v, want one about the resolved chords", warns)
	}
	evs := s.Events()
	if len(evs) != 2 || evs[0].Key != 74 || evs[1].Key != 76 {
		t.Fatalf("events = %v, want the chords' top notes 74 and 76", evs)
	}
	eventTechs(t, s, []score.Technique{0, score.TechSlur})
}

func TestWindSlurAcrossTieKeepsTie(t *testing.T) {
	m := attrs44div480 +
		slurNote("C", 5, 480, "", `<slur type="start"/>`) +
		slurNote("D", 5, 480, `<tie type="start"/>`, "") +
		slurNote("D", 5, 480, `<tie type="stop"/>`, "") +
		slurNote("C", 5, 480, "", `<slur type="stop"/>`)
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	evs := s.Events()
	if len(evs) != 3 || evs[1].End-evs[1].Start != 2*score.PPQ {
		t.Fatalf("events = %v, want three with the tied pair merged into one half note", evs)
	}
	eventTechs(t, s, []score.Technique{0, score.TechSlur, score.TechSlur})
}

func TestWindSlurOnGraceNoteIgnored(t *testing.T) {
	m := attrs44div480 +
		slurNote("D", 5, 0, "<grace/>", `<slur type="start"/>`) +
		slurNote("C", 5, 960, "", `<slur type="stop"/>`) +
		slurNote("E", 5, 960, "", "")
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 1 || len(findWarn(warns, "grace")) != 1 {
		t.Errorf("warnings = %v, want exactly the grace-note skip", warns)
	}
	eventTechs(t, s, []score.Technique{0, 0})
}

func TestFrettedSlursUntouched(t *testing.T) {
	m := `<measure number="1">` + attrs44div480 +
		slurNote("E", 4, 480, "", `<slur type="start"/>`) +
		slurNote("G", 4, 480, "", "") +
		slurNote("A", 4, 960, "", `<slur type="stop"/>`) +
		`</measure>`
	s, _, err := Import(wrapMeasures(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if s.Tracks[0].Wind != nil {
		t.Fatalf("the default part imported as a %s, want fretted", s.Tracks[0].Wind.Name)
	}
	eventTechs(t, s, []score.Technique{0, 0, 0})
}

func TestWindSlurRoundTripsThroughTextFormat(t *testing.T) {
	m := attrs44div480 +
		slurNote("C", 5, 480, "", `<slur type="start"/>`) +
		slurNote("D", 5, 480, "", "") +
		slurNote("E", 5, 960, "", `<slur type="stop"/>`)
	s, _, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	text, err := textfmt.Format(s)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	for _, tok := range []string{"E5l", "F#5.2l"} {
		if !strings.Contains(string(text), tok) {
			t.Errorf("formatted text lacks %q:\n%s", tok, text)
		}
	}
	rt, err := textfmt.Parse(text, "roundtrip")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	evs, rtevs := s.Events(), rt.Events()
	if len(rtevs) != len(evs) {
		t.Fatalf("round trip has %d events, want %d", len(rtevs), len(evs))
	}
	for i := range evs {
		if rtevs[i].Key != evs[i].Key || rtevs[i].Tech != evs[i].Tech {
			t.Errorf("round-trip event %d = key %d tech %v, want key %d tech %v",
				i, rtevs[i].Key, rtevs[i].Tech, evs[i].Key, evs[i].Tech)
		}
	}
	eventTechs(t, rt, []score.Technique{0, score.TechSlur, score.TechSlur})
}
