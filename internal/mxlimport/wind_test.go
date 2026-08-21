package mxlimport

import (
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
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

// TestWindNameNeverOverridesADeclaredProgram: a part labelled "Flute"
// whose file explicitly declares a guitar program is a guitar — the
// name fallback exists only for files that carry no MIDI block at all.
func TestWindNameNeverOverridesADeclaredProgram(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time></attributes>` +
		note("E", 4, 960, -1, 0, "") +
		note("C", 5, 960, -1, 0, `<chord/>`) +
		note("G", 4, 960, -1, 0, "") +
		note("G", 4, 960, -1, 0, "")
	s, _, err := Import(wrapWindPart("Flute", 26, m)) // 1-based 26 = the guitar default
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

// TestWindChordKeepsHighestPlayable: range first, chords second — a chord
// whose top note is below the horn falls back to its highest playable
// note instead of losing the whole chord to the unplayable one.
func TestWindChordKeepsHighestPlayable(t *testing.T) {
	// Soprano sax, no transpose block: written = sounding. G3 (55) is
	// below the horn's Ab3 (56); D5 (74) is fine. The chord D5+G3 must
	// keep D5.
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

// slurNote builds an unfingered <note> whose trailing <notations> block —
// where real exporters put it — carries raw slur elements. extra is
// note()'s leading children (<chord/>, <grace/>, <tie/>...).
func slurNote(step string, octave, dur int, extra, slurs string) string {
	return `<note>` + extra +
		`<pitch><step>` + step + `</step><octave>` + string(rune('0'+octave)) + `</octave></pitch>` +
		`<duration>` + itoa(dur) + `</duration><voice>1</voice>` +
		`<notations>` + slurs + `</notations></note>`
}

// eventTechs flattens the events' techniques, in event order, for
// comparing which notes a slur covered.
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

// TestWindSlurPhrase: a slur over n1..n3 is one tongue stroke. TechSlur
// marks a note slurred INTO, so n2 and n3 carry it and n1 — which takes
// the attack — does not; the stop note is the arc's last covered note.
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

// TestWindSeparateSlurs: an arc's stop closes it AFTER the stop note is
// covered, so the note following one slur starts the next phrase with a
// fresh attack.
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

// TestWindOverlappingNumberedSlurs: arc 1 over n1..n3 and arc 2 over
// n2..n4 overlap, kept apart by their number attributes. A note is covered
// when ANY arc was open before it started, so n2..n4 slur and n5 — after
// both arcs closed — attacks fresh.
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

// TestWindSlurAcrossCollapsedChords: slur coverage belongs to the ONSET,
// not to one chord head, so it composes with the wind chord collapse in
// either direction. The first chord's head STARTS the arc — the chord is
// the arc's first note, so its surviving top note keeps its attack — and
// the second chord's head (not its surviving top note) carries the stop,
// yet the top note still slurs.
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

// TestWindSlurAcrossTieKeepsTie: ties and slurs are separate — a tied
// continuation merges in score.Events regardless, and a note both tied
// and slur-covered stays one merged event carrying TechSlur.
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

// TestWindSlurOnGraceNoteIgnored: grace notes are outside the import
// subset, so an arc touching one is an arc missing an endpoint — the
// main note keeps its attack (the state never saw the start), the
// dangling stop closes nothing, and later notes are unaffected.
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

// TestFrettedSlursUntouched: mapping a fretted part's slurs to hammer-ons
// and pull-offs is a separate decision, not taken by the importer — the
// arcs must not leak TechHammer onto a guitar line.
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

// TestWindSlurRoundTripsThroughTextFormat: the imported slurs survive
// Format and Parse through internal/score/textfmt — the wind alphabet
// spells TechSlur as the letter l on the covered notes' tokens — which
// proves the whole chain: parse, collapse, track build, writer, parser.
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
	// Written pitch on a B-flat soprano is sounding+2: C5 (72) writes as
	// D5 and takes no letter; the covered D5 and E5 write as E5 and F#5
	// with the slur letter on their tokens (the writer elides a repeated
	// duration, so the quarter-note E5 carries its letter bare).
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
