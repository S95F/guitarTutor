package mxlimport

import (
	"archive/zip"
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

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
	got, err := readZipEntry(f)
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
