package mxlimport

import (
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func TestCapoBeyondLimitUsesNone(t *testing.T) {

	build := func(capo string, octave int) []byte {
		m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
			`<staff-details><capo>` + capo + `</capo></staff-details></attributes>` +
			note("E", octave, 1920, 6, 0, "")
		return wrap(m)
	}

	s, warns, err := Import(build("40", 2))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(findWarn(warns, "outside 0-12")) != 1 {
		t.Errorf("warnings = %v, want one clamping the capo", warns)
	}
	if got := s.Tracks[0].Capo; got != 0 {
		t.Errorf("Capo = %d, want 0 after the clamp", got)
	}
	if evs := s.Events(); len(evs) != 1 || evs[0].Key != 40 {
		t.Fatalf("events = %+v, want the open E2 with no capo shift", evs)
	}

	s, warns, err = Import(build("12", 3))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none for the boundary capo 12", warns)
	}
	if got := s.Tracks[0].Capo; got != 12 {
		t.Errorf("Capo = %d, want the boundary 12 kept", got)
	}
}

func restNote(dur int, slurs string) string {
	return `<note><rest/><duration>` + itoa(dur) + `</duration><voice>1</voice>` +
		`<notations>` + slurs + `</notations></note>`
}

func TestRestSlurStopClosesArc(t *testing.T) {
	m := attrs44div480 +
		slurNote("C", 5, 480, "", `<slur type="start"/>`) +
		restNote(480, `<slur type="stop"/>`) +
		slurNote("D", 5, 480, "", "") +
		slurNote("E", 5, 480, "", "")
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	eventTechs(t, s, []score.Technique{0, 0, 0})
}

func TestRestInsideArcKeepsItOpen(t *testing.T) {
	m := attrs44div480 +
		slurNote("C", 5, 480, "", `<slur type="start"/>`) +
		slurNote("D", 5, 480, "", "") +
		restNote(480, "") +
		slurNote("E", 5, 480, "", `<slur type="stop"/>`)
	s, warns, err := Import(wrapWindPart("Soprano Sax", 0, m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	eventTechs(t, s, []score.Technique{0, score.TechSlur, score.TechSlur})
}

func TestTieChainRenumberedMidNote(t *testing.T) {
	m1 := attrs44div480 +
		note("B", 3, 1920, -1, 0, `<tie type="start" number="1"/>`)
	m2 := note("B", 3, 1920, -1, 0, `<tie type="stop" number="1"/><tie type="start" number="2"/>`)
	m3 := note("B", 3, 1920, -1, 0, `<tie type="stop" number="2"/>`)
	s, warns, err := Import(wrap(m1, m2, m3))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := findWarn(warns, "tie"); len(got) != 0 {
		t.Errorf("warnings = %v, want the renumbered chain to resolve", got)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 3*3840 {
		t.Fatalf("events = %+v, want one chain merged across all three bars", evs)
	}
}

func TestAuthoredFretPastFormatLimitInferred(t *testing.T) {
	m := attrs44div480 + note("B", 4, 1920, 6, 31, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(findWarn(warns, "out-of-range <technical> string/fret")) != 1 {
		t.Errorf("warnings = %v, want the fret-31 fingering discounted", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 71 {
		t.Fatalf("events = %+v, want the B4 kept at key 71", evs)
	}
	if evs[0].String == 6 && evs[0].Fret == 31 {
		t.Fatalf("fingering = 6/31: the unwritable authored fret was honored")
	}
	if _, err := textfmt.Format(s); err != nil {
		t.Errorf("Format: %v, want the imported piece to save", err)
	}
}

func TestImportedLabelsAlwaysSave(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?><score-partwise version="4.0">` +
		`<work><work-title>Song // Take 2</work-title></work>` +
		`<part-list><score-part id="P1"><part-name>AC` + "\n" + `DC</part-name></score-part></part-list>` +
		`<part id="P1"><measure number="1">` + attrs44div480 + note("E", 2, 1920, 6, 0, "") + `</measure></part>` +
		`</score-partwise>`
	s, warns, err := Import([]byte(doc))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := s.Title; got != "Song / / Take 2" {
		t.Errorf("Title = %q, want the comment marker broken", got)
	}
	if got := s.Tracks[0].Name; got != "AC DC" {
		t.Errorf("Name = %q, want the line break replaced", got)
	}
	if len(findWarn(warns, "cannot")) != 2 {
		t.Errorf("warnings = %v, want one for the title and one for the part name", warns)
	}
	src, err := textfmt.Format(s)
	if err != nil {
		t.Fatalf("Format: %v, want the imported piece to save", err)
	}
	back, err := textfmt.Parse(src, "back")
	if err != nil {
		t.Fatalf("Parse of the saved piece: %v", err)
	}
	if back.Title != s.Title || back.Tracks[0].Name != s.Tracks[0].Name {
		t.Errorf("round trip changed labels: %q/%q -> %q/%q", s.Title, s.Tracks[0].Name, back.Title, back.Tracks[0].Name)
	}
	if !strings.Contains(string(src), "\\title Song / / Take 2") {
		t.Errorf("saved source lacks the cleaned title:\n%s", src)
	}
}
