package mxlimport

import (
	"archive/zip"
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/S95F/guitarTutor/internal/score"
)

// canonical is the fixture riff's exact flattened event table (see
// docs/TEXTFORMAT.md and ROADMAP Phase 0): Start/End in PPQ-960 ticks and
// derived MIDI key, in Events() order. Every fixture container must
// reproduce it exactly.
var canonical = []struct {
	start, end int64
	key        int
}{
	// Bar 1: eight eighth notes.
	{0, 480, 40}, {480, 960, 43}, {960, 1440, 50}, {1440, 1920, 40},
	{1920, 2400, 43}, {2400, 2880, 50}, {2880, 3360, 43}, {3360, 3840, 40},
	// Bar 2: quarter, quarter, half.
	{3840, 4800, 47}, {4800, 5760, 45}, {5760, 7680, 47},
	// Bar 3: E5 power chord, rest, quarter note.
	{7680, 9600, 40}, {7680, 9600, 47}, {7680, 9600, 52},
	{10560, 11520, 43},
	// Bar 4: whole-note low E.
	{11520, 15360, 40},
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return data
}

// checkCanonical asserts the full cross-format contract: the exact event
// table, the authored fingering (bar-3 chord on strings 6/5/4 at frets
// 0/2/2), and the score metadata.
func checkCanonical(t *testing.T, s *score.Score, warns []string) {
	t.Helper()
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.Title != "Fixture Riff" {
		t.Errorf("Title = %q, want %q", s.Title, "Fixture Riff")
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.Name != "Guitar" || tr.Role != score.RoleUser || tr.Program != DefaultProgram {
		t.Errorf("track = %q role %d program %d, want Guitar role %d program %d",
			tr.Name, tr.Role, tr.Program, score.RoleUser, DefaultProgram)
	}
	if len(tr.Tuning) != 6 {
		t.Fatalf("tuning has %d strings, want 6", len(tr.Tuning))
	}
	for i, want := range score.StandardTuning {
		if tr.Tuning[i] != want {
			t.Errorf("tuning[%d] = %d, want %d (staff-tuning line mapping)", i, tr.Tuning[i], want)
		}
	}
	if tr.Capo != 0 {
		t.Errorf("Capo = %d, want 0", tr.Capo)
	}
	if len(s.Tempos) != 1 || s.Tempos[0].Tick != 0 || s.Tempos[0].USPerQuarter != score.USPerQuarter(120) {
		t.Errorf("Tempos = %v, want single 120 BPM at tick 0", s.Tempos)
	}
	if len(s.Meters) != 1 || s.Meters[0] != (score.Meter{Tick: 0, Num: 4, Den: 4}) {
		t.Errorf("Meters = %v, want single 4/4 at tick 0", s.Meters)
	}

	evs := s.Events()
	if len(evs) != len(canonical) {
		t.Fatalf("got %d events, want %d", len(evs), len(canonical))
	}
	for i, want := range canonical {
		ev := evs[i]
		if ev.Start != want.start || ev.End != want.end || ev.Key != want.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, ev.Start, ev.End, ev.Key, want.start, want.end, want.key)
		}
	}
	// The bar-3 chord carries the authored fingering exactly: strings
	// 6/5/4 at frets 0/2/2 (string 1 = highest-pitched).
	chord := map[int][2]int{40: {6, 0}, 47: {5, 2}, 52: {4, 2}}
	seen := 0
	for _, ev := range evs {
		if ev.Start != 7680 {
			continue
		}
		seen++
		want, ok := chord[ev.Key]
		if !ok {
			t.Errorf("unexpected chord key %d", ev.Key)
			continue
		}
		if ev.String != want[0] || ev.Fret != want[1] {
			t.Errorf("chord key %d = string %d fret %d, want string %d fret %d",
				ev.Key, ev.String, ev.Fret, want[0], want[1])
		}
	}
	if seen != 3 {
		t.Errorf("found %d chord events at tick 7680, want 3", seen)
	}
	// Authored fingerings: nothing is Inferred.
	for _, bar := range tr.Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if n.Inferred {
					t.Errorf("note at tick %d string %d marked Inferred; fixture fingerings are authored", beat.Start, n.String)
				}
			}
		}
	}
}

func TestImportFixtureMusicXML(t *testing.T) {
	s, warns, err := Import(readFixture(t, "fixture_riff.musicxml"))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	checkCanonical(t, s, warns)
}

func TestImportFixtureMXL(t *testing.T) {
	s, warns, err := ImportFile("../../testdata/fixture_riff.mxl")
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	checkCanonical(t, s, warns)
}

// TestMXLMatchesMusicXML guards fixture drift: the committed .mxl must
// embed exactly the committed .musicxml.
func TestMXLMatchesMusicXML(t *testing.T) {
	want := readFixture(t, "fixture_riff.musicxml")
	data := readFixture(t, "fixture_riff.mxl")
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open .mxl: %v", err)
	}
	f := zipEntry(zr, "fixture_riff.musicxml")
	if f == nil {
		t.Fatal(".mxl does not contain fixture_riff.musicxml")
	}
	got, err := readZipEntry(f, maxRootfileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error(".mxl payload differs from fixture_riff.musicxml; regenerate the .mxl")
	}
}

// wrap builds a minimal one-part score-partwise document around measure
// bodies.
func wrap(measures ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><score-partwise version="4.0">`)
	b.WriteString(`<part-list><score-part id="P1"><part-name>T</part-name></score-part></part-list><part id="P1">`)
	for i, m := range measures {
		b.WriteString(`<measure number="` + string(rune('1'+i)) + `">` + m + `</measure>`)
	}
	b.WriteString(`</part></score-partwise>`)
	return []byte(b.String())
}

// wrapMeasures builds the same minimal document from COMPLETE <measure>
// elements, for fixtures that need measure attributes (implicit="yes") or
// barlines.
func wrapMeasures(measures ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><score-partwise version="4.0">`)
	b.WriteString(`<part-list><score-part id="P1"><part-name>T</part-name></score-part></part-list><part id="P1">`)
	for _, m := range measures {
		b.WriteString(m)
	}
	b.WriteString(`</part></score-partwise>`)
	return []byte(b.String())
}

const attrs44div480 = `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time></attributes>`

// note builds a <note> element. str/fret < 0 omits <technical>.
func note(step string, octave int, dur int, str, fret int, extra string) string {
	var b strings.Builder
	b.WriteString(`<note>`)
	b.WriteString(extra) // <chord/>, <grace/>, <tie/>... (leading children)
	b.WriteString(`<pitch><step>` + step + `</step><octave>` + string(rune('0'+octave)) + `</octave></pitch>`)
	b.WriteString(`<duration>` + itoa(dur) + `</duration><voice>1</voice>`)
	if str >= 0 {
		b.WriteString(`<notations><technical><string>` + itoa(str) + `</string><fret>` + itoa(fret) + `</fret></technical></notations>`)
	}
	b.WriteString(`</note>`)
	return b.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var d []byte
	for v > 0 {
		d = append([]byte{byte('0' + v%10)}, d...)
		v /= 10
	}
	if neg {
		return "-" + string(d)
	}
	return string(d)
}

// TestBackupExactCursor is the silent-corruption regression: <backup>
// must move the time cursor exactly, so voice-1 polyphony written with
// backup keeps its ticks, and the following measure does not drift.
func TestBackupExactCursor(t *testing.T) {
	m1 := attrs44div480 +
		// Half note E4 on string 1...
		note("E", 4, 960, 1, 0, "") +
		// ...then back up a half and lay two quarters under it on string 6.
		`<backup><duration>960</duration></backup>` +
		note("E", 2, 480, 6, 0, "") +
		note("G", 2, 480, 6, 3, "") +
		// Skip the empty second half of the measure.
		`<forward><duration>960</duration></forward>`
	m2 := `<note><pitch><step>E</step><octave>2</octave></pitch><duration>1920</duration><voice>1</voice>` +
		`<notations><technical><string>6</string><fret>0</fret></technical></notations></note>`
	s, warns, err := Import(wrap(m1, m2))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	want := []struct {
		start, end int64
		key        int
	}{
		{0, 960, 40},     // string 6 quarter under the half note
		{0, 1920, 64},    // string 1 half note
		{960, 1920, 43},  // string 6 second quarter
		{3840, 7680, 40}, // measure 2: whole note, no drift
	}
	evs := s.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events %v, want %d", len(evs), evs, len(want))
	}
	for i, w := range want {
		if evs[i].Start != w.start || evs[i].End != w.end || evs[i].Key != w.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, evs[i].Start, evs[i].End, evs[i].Key, w.start, w.end, w.key)
		}
	}
}

// TestSecondVoiceSkippedWithoutShift: a MuseScore-style two-voice measure
// (voice 1, backup, voice 2). Voice 2 is skipped with a warning, and —
// because backup was honored — voice-1 timing in this and the next
// measure is unshifted.
func TestSecondVoiceSkippedWithoutShift(t *testing.T) {
	v2 := `<note><pitch><step>C</step><octave>3</octave></pitch><duration>960</duration><voice>2</voice></note>`
	m1 := attrs44div480 +
		note("E", 2, 480, 6, 0, "") +
		note("G", 2, 480, 6, 3, "") +
		note("A", 2, 480, 5, 0, "") +
		note("B", 2, 480, 5, 2, "") +
		`<backup><duration>1920</duration></backup>` +
		v2 + v2
	m2 := note("E", 2, 1920, 6, 0, "")
	s, warns, err := Import(wrap(m1, m2))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "voice") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one mentioning skipped voices", warns)
	}
	want := []struct {
		start, end int64
		key        int
	}{
		{0, 960, 40}, {960, 1920, 43}, {1920, 2880, 45}, {2880, 3840, 47},
		{3840, 7680, 40},
	}
	evs := s.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events %v, want %d (voice 2 excluded)", len(evs), evs, len(want))
	}
	for i, w := range want {
		if evs[i].Start != w.start || evs[i].End != w.end || evs[i].Key != w.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, evs[i].Start, evs[i].End, evs[i].Key, w.start, w.end, w.key)
		}
	}
}

// TestDivisionsRescale: divisions=3 makes a duration-1 note a quarter
// triplet eighth (320 score ticks); everything must rescale to PPQ 960
// exactly.
func TestDivisionsRescale(t *testing.T) {
	m := `<attributes><divisions>3</divisions><time><beats>4</beats><beat-type>4</beat-type></time></attributes>` +
		note("E", 2, 1, 6, 0, "") +
		note("G", 2, 1, 6, 3, "") +
		note("A", 2, 1, 5, 0, "") +
		note("E", 2, 3, 6, 0, "") +
		note("G", 2, 3, 6, 3, "") +
		note("A", 2, 3, 5, 0, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none (3 divides 960)", warns)
	}
	want := []int64{0, 320, 640, 960, 1920, 2880}
	evs := s.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d", len(evs), len(want))
	}
	for i, ws := range want {
		if evs[i].Start != ws {
			t.Errorf("event %d start = %d, want %d", i, evs[i].Start, ws)
		}
	}
	if evs[2].End != 960 || evs[5].End != 3840 {
		t.Errorf("ends = %d, %d, want 960, 3840", evs[2].End, evs[5].End)
	}
}

// TestDivisionsRounding: divisions=7 cannot represent every boundary in
// PPQ-960 ticks; the import must warn that it rounded.
func TestDivisionsRounding(t *testing.T) {
	m := `<attributes><divisions>7</divisions><time><beats>4</beats><beat-type>4</beat-type></time></attributes>` +
		note("E", 2, 7, 6, 0, "") + // exact: one quarter
		note("G", 2, 3, 6, 3, "") + // 3*960/7 rounds
		note("A", 2, 4, 5, 0, "") +
		`<forward><duration>14</duration></forward>`
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "rounded") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one mentioning rounding", warns)
	}
	evs := s.Events()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	// scale(7)=960, scale(10)=round(9600/7)=1371, scale(14)=1920.
	want := []struct{ start, end int64 }{{0, 960}, {960, 1371}, {1371, 1920}}
	for i, w := range want {
		if evs[i].Start != w.start || evs[i].End != w.end {
			t.Errorf("event %d = (%d,%d), want (%d,%d)", i, evs[i].Start, evs[i].End, w.start, w.end)
		}
	}
}

// TestTieAcrossBarline: a tie chain across the barline merges into one
// event, with the continuation stored as a Tied beat note.
func TestTieAcrossBarline(t *testing.T) {
	m1 := attrs44div480 +
		`<note><rest/><duration>960</duration><voice>1</voice></note>` +
		note("E", 2, 960, 6, 0, `<tie type="start"/>`)
	m2 := note("E", 2, 960, 6, 0, `<tie type="stop"/><tie type="start"/>`) +
		note("E", 2, 960, 6, 0, `<tie type="stop"/>`)
	s, warns, err := Import(wrap(m1, m2))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	evs := s.Events()
	if len(evs) != 1 {
		t.Fatalf("got %d events %v, want 1 merged tie chain", len(evs), evs)
	}
	if evs[0].Start != 1920 || evs[0].End != 7680 || evs[0].Key != 40 {
		t.Errorf("event = (%d,%d,key %d), want (1920,7680,key 40)", evs[0].Start, evs[0].End, evs[0].Key)
	}
	// The bar-2 half is a Tied continuation in the stored score.
	tr := s.Tracks[0]
	foundTied := false
	for _, beat := range tr.Bars[1].Beats {
		for _, n := range beat.Notes {
			if n.Tied {
				foundTied = true
			}
		}
	}
	if !foundTied {
		t.Error("no Tied note stored in bar 2; tie continuation lost")
	}
}

// TestTieStopWithoutStart degrades to a fresh attack with a warning.
func TestTieStopWithoutStart(t *testing.T) {
	m := attrs44div480 +
		note("E", 2, 960, 6, 0, "") +
		note("E", 2, 960, 6, 0, `<tie type="stop"/>`)
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "tie") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one mentioning the dangling tie", warns)
	}
	if evs := s.Events(); len(evs) != 2 {
		t.Errorf("got %d events, want 2 separate attacks", len(evs))
	}
}

// TestFrettingFallback strips every <notations> block (and with it all
// <technical> fingering) from the canonical fixture: the events must
// still match, with every fingering inferred — and the heuristic finds
// the canonical chord shape.
func TestFrettingFallback(t *testing.T) {
	data := readFixture(t, "fixture_riff.musicxml")
	stripped := regexp.MustCompile(`(?s)<notations>.*?</notations>`).ReplaceAll(data, nil)
	if bytes.Equal(stripped, data) {
		t.Fatal("stripping <notations> changed nothing; fixture or regexp broken")
	}
	s, warns, err := Import(stripped)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	evs := s.Events()
	if len(evs) != len(canonical) {
		t.Fatalf("got %d events, want %d", len(evs), len(canonical))
	}
	for i, want := range canonical {
		if evs[i].Start != want.start || evs[i].End != want.end || evs[i].Key != want.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, evs[i].Start, evs[i].End, evs[i].Key, want.start, want.end, want.key)
		}
	}
	for _, bar := range s.Tracks[0].Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if !n.Inferred {
					t.Errorf("note at tick %d string %d not marked Inferred", beat.Start, n.String)
				}
			}
		}
	}
}

// TestCapo: <staff-details><capo> shifts derived pitch; the file's written
// pitch is authored consistently so no mismatch warning fires.
func TestCapo(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<staff-details><capo>2</capo></staff-details></attributes>` +
		`<note><pitch><step>F</step><alter>1</alter><octave>2</octave></pitch><duration>1920</duration><voice>1</voice>` +
		`<notations><technical><string>6</string><fret>0</fret></technical></notations></note>`
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if s.Tracks[0].Capo != 2 {
		t.Errorf("Capo = %d, want 2", s.Tracks[0].Capo)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 42 {
		t.Fatalf("events = %v, want one key-42 note (open low E, capo 2)", evs)
	}
}

// TestUnderfullMeasurePadded: a measure whose content stops early is
// padded with a rest, with a warning, and still validates.
func TestUnderfullMeasurePadded(t *testing.T) {
	m := attrs44div480 + note("E", 2, 480, 6, 0, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "does not fill") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want an underfull-measure warning", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 960 {
		t.Fatalf("events = %v, want one note at (0,960)", evs)
	}
	if got := s.End(); got != 3840 {
		t.Errorf("score end = %d, want 3840 (full 4/4 bar)", got)
	}
}

// TestGraceNoteSkipped: grace notes are outside the subset — warned,
// skipped, and the cursor untouched.
func TestGraceNoteSkipped(t *testing.T) {
	m := attrs44div480 +
		`<note><grace/><pitch><step>D</step><octave>3</octave></pitch><voice>1</voice></note>` +
		note("E", 2, 1920, 6, 0, "") +
		`<forward><duration>960</duration></forward>`
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "grace") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one mentioning grace notes", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 3840 {
		t.Fatalf("events = %v, want one half note at (0,3840)", evs)
	}
}

// TestMetronomeTempo: without <sound tempo>, the metronome marking sets
// the tempo — converted to quarter BPM through the beat unit.
func TestMetronomeTempo(t *testing.T) {
	m := attrs44div480 +
		`<direction><direction-type><metronome><beat-unit>half</beat-unit><per-minute>60</per-minute></metronome></direction-type></direction>` +
		note("E", 2, 1920, 6, 0, "") +
		`<forward><duration>960</duration></forward>`
	s, _, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	// half = 60 -> quarter = 120.
	if len(s.Tempos) != 1 || s.Tempos[0].USPerQuarter != score.USPerQuarter(120) {
		t.Errorf("Tempos = %v, want single 120 quarter-BPM entry", s.Tempos)
	}
}

// TestMidPieceTempoChange: a direction later in the piece lands at its
// cursor position, not the measure start.
func TestMidPieceTempoChange(t *testing.T) {
	m := attrs44div480 +
		note("E", 2, 960, 6, 0, "") +
		`<direction><sound tempo="90"/></direction>` +
		note("G", 2, 960, 6, 3, "")
	s, _, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	want := score.TempoMap{
		{Tick: 0, USPerQuarter: score.USPerQuarter(120)},
		{Tick: 1920, USPerQuarter: score.USPerQuarter(90)},
	}
	if len(s.Tempos) != 2 || s.Tempos[0] != want[0] || s.Tempos[1] != want[1] {
		t.Errorf("Tempos = %v, want %v", s.Tempos, want)
	}
}

func TestImportGarbage(t *testing.T) {
	if _, _, err := Import([]byte("not xml at all")); err == nil {
		t.Error("expected an error for non-XML input")
	}
}

func TestImportTimewiseRejected(t *testing.T) {
	doc := []byte(`<?xml version="1.0"?><score-timewise version="4.0"></score-timewise>`)
	_, _, err := Import(doc)
	if err == nil || !strings.Contains(err.Error(), "score-timewise") {
		t.Errorf("err = %v, want a score-timewise rejection", err)
	}
}

func TestImportNoNotes(t *testing.T) {
	m := attrs44div480 + `<note><rest/><duration>1920</duration><voice>1</voice></note><forward><duration>960</duration></forward>`
	_, _, err := Import(wrap(m))
	if err == nil || !strings.Contains(err.Error(), "no playable notes") {
		t.Errorf("err = %v, want no-playable-notes", err)
	}
}

// TestZipWithoutContainer: a .mxl missing META-INF/container.xml falls
// back to the first XML member, with a warning.
func TestZipWithoutContainer(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("piece.musicxml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(wrap(attrs44div480 + note("E", 2, 1920, 6, 0, "") + `<forward><duration>960</duration></forward>`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	s, warns, err := Import(buf.Bytes())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "container.xml") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one about the missing container.xml", warns)
	}
	if evs := s.Events(); len(evs) != 1 {
		t.Errorf("got %d events, want 1", len(evs))
	}
}

// TestHostileNoteDurationSkipped: a single absurd <duration> must not
// blow up the bar layout (one barSpec per bar out to the score's end) —
// the note is skipped with a warning and the rest imports normally.
func TestHostileNoteDurationSkipped(t *testing.T) {
	m := attrs44div480 +
		note("E", 2, 480, 6, 0, "") +
		note("E", 2, 9000000000000000000, -1, 0, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "score limit") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one about the tick score limit", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 960 || evs[0].Key != 40 {
		t.Fatalf("events = %v, want only the sane note at (0,960,key 40)", evs)
	}
}

// TestHostileForwardPositionSkipped: a hostile <forward> pushes the
// cursor to an absurd position; the note placed there is skipped with a
// warning instead of stretching the score extent (or overflowing the
// rescale).
func TestHostileForwardPositionSkipped(t *testing.T) {
	m := attrs44div480 +
		note("E", 2, 480, 6, 0, "") +
		`<forward><duration>9000000000000000000</duration></forward>` +
		note("G", 2, 480, 6, 3, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "score limit") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one about the tick score limit", warns)
	}
	if evs := s.Events(); len(evs) != 1 || evs[0].Key != 40 {
		t.Fatalf("events = %v, want only the sane note", evs)
	}
}

// TestHostileMeterScoreTooLong: a hostile <beats> value makes even one
// measure exceed the score-extent limit; the import errors cleanly
// ("score too long") instead of laying out millions of bars.
func TestHostileMeterScoreTooLong(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>2000000</beats><beat-type>4</beat-type></time></attributes>` +
		note("E", 2, 1920, 6, 0, "")
	_, _, err := Import(wrap(m))
	if err == nil || !strings.Contains(err.Error(), "score too long") {
		t.Errorf("err = %v, want a score-too-long error", err)
	}
}

// TestTinyBarsCapped: an extreme meter (1/3840 = one-tick bars) slices
// a legal extent into an absurd number of bars; the bar-count cap
// errors instead of allocating without bound.
func TestTinyBarsCapped(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>1</beats><beat-type>3840</beat-type></time></attributes>` +
		`<forward><duration>100000</duration></forward>` +
		note("E", 2, 480, 6, 0, "")
	_, _, err := Import(wrap(m))
	if err == nil || !strings.Contains(err.Error(), "score too long") {
		t.Errorf("err = %v, want a score-too-long error (bar-count cap)", err)
	}
}

// TestZipBombRootfileRejected: a .mxl whose rootfile decompresses far
// past the cap (a zip bomb) errors quickly with bounded memory instead
// of inflating the whole payload.
func TestZipBombRootfileRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("META-INF/container.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`<?xml version="1.0"?><container><rootfiles><rootfile full-path="music.xml"/></rootfiles></container>`)); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("music.xml")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := 0; i < 65; i++ { // 65 MiB of zeros: tiny compressed, over the 64 MiB cap
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = Import(buf.Bytes())
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %v, want a decompressed-size-limit error", err)
	}
}

// TestZipBombContainerRejected: META-INF/container.xml has its own,
// much smaller cap.
func TestZipBombContainerRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("META-INF/container.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 2<<20)); err != nil { // 2 MiB, over the 1 MiB cap
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = Import(buf.Bytes())
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %v, want a decompressed-size-limit error", err)
	}
}

// TestAbsurdTempoSkipped: tempos whose USPerQuarter degenerates (huge
// BPM rounds to 0, tiny BPM overflows) degrade to a warning with the
// tick-0 120 BPM default — never a hard Validate failure.
func TestAbsurdTempoSkipped(t *testing.T) {
	m := attrs44div480 +
		`<direction><sound tempo="1e300"/></direction>` +
		`<direction><sound tempo="1e-300"/></direction>` +
		note("E", 2, 1920, 6, 0, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := 0
	for _, w := range warns {
		if strings.Contains(w, "BPM") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("warnings = %v, want two skipped-tempo warnings", warns)
	}
	if len(s.Tempos) != 1 || s.Tempos[0] != (score.Tempo{Tick: 0, USPerQuarter: score.USPerQuarter(120)}) {
		t.Errorf("Tempos = %v, want single default 120 BPM at tick 0", s.Tempos)
	}
}

// TestFingeringBeyondMIDIRange: an authored <technical> fingering whose
// derived pitch exceeds MIDI 127 (string 1 fret 90) falls into the
// fretting fallback with a warning — the import must not hard-fail
// Validate over it.
func TestFingeringBeyondMIDIRange(t *testing.T) {
	m := attrs44div480 + note("E", 2, 1920, 1, 90, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "string/fret") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one about the out-of-range fingering", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 3840 || evs[0].Key != 40 {
		t.Fatalf("events = %v, want one note at (0,3840,key 40)", evs)
	}
	if evs[0].String != 6 || evs[0].Fret != 0 {
		t.Errorf("fingering = string %d fret %d, want the inferred 6/0", evs[0].String, evs[0].Fret)
	}
	for _, bar := range s.Tracks[0].Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if !n.Inferred {
					t.Errorf("note at tick %d not marked Inferred after the fingering fallback", beat.Start)
				}
			}
		}
	}
}

// staffTunings builds n <staff-tuning> lines, every string tuned to E2.
func staffTunings(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(`<staff-tuning line="` + itoa(i) + `"><tuning-step>E</tuning-step><tuning-octave>2</tuning-octave></staff-tuning>`)
	}
	return b.String()
}

// TestImportTuningOverLimitFallsBack: a staff-details tuning declaring
// more lines than the 25-string limit is rejected wholesale — warned, and
// the current (default 6-string standard) tuning kept — never truncated
// into an invented instrument.
func TestImportTuningOverLimitFallsBack(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<staff-details><staff-lines>26</staff-lines>` + staffTunings(26) + `</staff-details></attributes>` +
		note("E", 2, 1920, 6, 0, "") +
		`<forward><duration>1920</duration></forward>`
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	foundLimit, foundKeep := false, false
	for _, w := range warns {
		if strings.Contains(w, "more than the 25-string limit") {
			foundLimit = true
		}
		if strings.Contains(w, "keeping the current tuning") {
			foundKeep = true
		}
	}
	if !foundLimit || !foundKeep {
		t.Errorf("warnings = %v, want the over-limit staff rejected and the current tuning kept", warns)
	}
	tr := s.Tracks[0]
	if len(tr.Tuning) != 6 {
		t.Fatalf("tuning has %d strings, want the 6-string default", len(tr.Tuning))
	}
	for i, want := range score.StandardTuning {
		if tr.Tuning[i] != want {
			t.Errorf("tuning[%d] = %d, want %d (standard)", i, tr.Tuning[i], want)
		}
	}
}

// TestImportPathologicalChordCompletes is the end-to-end denial-of-service
// regression: a 40-line tab staff plus a 15-note unfingered chord used to
// hand the fretting heuristic a ~5e22-leaf search (effectively an infinite
// hang). The over-limit tuning is now rejected at the XML boundary and the
// fretting search is budgeted, so the import must finish in well under a
// second, with warnings, keeping whatever notes fit.
func TestImportPathologicalChordCompletes(t *testing.T) {
	var notes strings.Builder
	notes.WriteString(note("E", 2, 1920, -1, 0, ""))
	for i := 0; i < 14; i++ {
		notes.WriteString(note("E", 2, 1920, -1, 0, "<chord/>"))
	}
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<staff-details><staff-lines>40</staff-lines>` + staffTunings(40) + `</staff-details></attributes>` +
		notes.String() +
		`<forward><duration>1920</duration></forward>`
	start := time.Now()
	s, warns, err := Import(wrap(m))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Import took %v, want under 1s (pathological chord not defused?)", elapsed)
	}
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) == 0 {
		t.Error("want warnings (rejected tuning, dropped chord notes), got none")
	}
	if evs := s.Events(); len(evs) == 0 {
		t.Error("no events survived; want the playable part of the chord kept")
	}
}

// TestZipWithoutMusic: a ZIP with no MusicXML document errors.
func TestZipWithoutMusic(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Import(buf.Bytes()); err == nil {
		t.Error("expected an error for a ZIP with no MusicXML document")
	}
}
