package gpimport

// Round-2 sweep regressions: one string sounds once per beat, and
// imported labels always save.

import (
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// TestDuplicateStringInBeatSkipped: a beat listing the same note id twice
// — or two note ids claiming one string — imported two attacks on one
// string at one tick. score.Events treats the pair as one ringing note
// overwriting its own bookkeeping, and textfmt.Format refuses to write
// the beat, so the piece could never save. Later duplicates are now
// skipped with a warning.
func TestDuplicateStringInBeatSkipped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		beats string
		notes string
	}{
		{
			name:  "same note id twice",
			beats: `<Beat id="0"><Rhythm ref="0" /><Notes>0 0</Notes></Beat>`,
			notes: noteXML(0, 0, 5, ""),
		},
		{
			name:  "two ids on one string",
			beats: `<Beat id="0"><Rhythm ref="0" /><Notes>0 1</Notes></Beat>`,
			notes: noteXML(0, 0, 5, "") + noteXML(1, 0, 7, ""),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := gpifDoc(
				`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
				`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
				`<Voice id="0"><Beats>0</Beats></Voice>`,
				tc.beats,
				tc.notes,
				`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
			)
			s, warns, err := importDoc(t, doc)
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if !hasWarning(warns, "twice") {
				t.Errorf("warnings = %v, want one naming the duplicated string", warns)
			}
			beat := s.Tracks[0].Bars[0].Beats[0]
			if len(beat.Notes) != 1 {
				t.Fatalf("beat holds %d notes %v, want the first note only", len(beat.Notes), beat.Notes)
			}
			if beat.Notes[0].Fret != 5 {
				t.Errorf("kept fret %d, want the FIRST note's fret 5", beat.Notes[0].Fret)
			}
			if _, err := textfmt.Format(s); err != nil {
				t.Errorf("Format: %v, want the imported piece to save", err)
			}
		})
	}
}

// TestDuplicateStringSecondSurvivesFirstDrop: the dedupe must track only
// notes actually kept — when the first claim on a string is dropped for
// its own reasons (an absurd fret), the second is not a duplicate.
func TestDuplicateStringSecondSurvivesFirstDrop(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0 1</Notes></Beat>`,
		noteXML(0, 0, 64, "")+noteXML(1, 0, 5, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if hasWarning(warns, "twice") {
		t.Errorf("warnings = %v, want no duplicate warning when the first claim was dropped", warns)
	}
	beat := s.Tracks[0].Bars[0].Beats[0]
	if len(beat.Notes) != 1 || beat.Notes[0].Fret != 5 {
		t.Fatalf("beat = %v, want the surviving fret-5 note", beat.Notes)
	}
}

// TestImportedLabelsAlwaysSave: a GPIF title or track name holding "//"
// or a line break flowed verbatim into the score, and textfmt.Format
// refuses both — an import that plays but can never be saved. Labels are
// now cleaned with a warning.
func TestImportedLabelsAlwaysSave(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	doc = strings.Replace(doc, "<Score><Title>Test</Title></Score>",
		"<Score><Title>Song // Take 2</Title></Score>", 1)
	doc = strings.Replace(doc, "<Name>G</Name>", "<Name>AC&#10;DC</Name>", 1)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := s.Title; got != "Song / / Take 2" {
		t.Errorf("Title = %q, want the comment marker broken", got)
	}
	if got := s.Tracks[0].Name; got != "AC DC" {
		t.Errorf("Name = %q, want the line break replaced", got)
	}
	if !hasWarning(warns, "cannot") {
		t.Errorf("warnings = %v, want the cleaned labels reported", warns)
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
}
