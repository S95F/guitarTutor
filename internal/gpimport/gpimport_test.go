package gpimport

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/score"
)

// canonical is the fixture riff's exact flattened event table (see
// docs/TEXTFORMAT.md and ROADMAP Phase 0): Start/End in PPQ-960 ticks,
// derived MIDI key, and — because .gp carries authored fingering — the
// exact string and fret, in Events() order. The string column pins the
// GP-to-score string-numbering conversion (GP counts 0 = lowest string;
// we count 1 = highest), the likeliest bug in this package.
var canonical = []struct {
	start, end int64
	key        int
	str, fret  int
}{
	// Bar 1: eight eighth notes on strings 6 and 5, as authored.
	{0, 480, 40, 6, 0}, {480, 960, 43, 6, 3}, {960, 1440, 50, 5, 5}, {1440, 1920, 40, 6, 0},
	{1920, 2400, 43, 6, 3}, {2400, 2880, 50, 5, 5}, {2880, 3360, 43, 6, 3}, {3360, 3840, 40, 6, 0},
	// Bar 2: quarter, quarter, half — all on string 5.
	{3840, 4800, 47, 5, 2}, {4800, 5760, 45, 5, 0}, {5760, 7680, 47, 5, 2},
	// Bar 3: E5 power chord (strings 6/5/4, frets 0/2/2), rest, quarter.
	{7680, 9600, 40, 6, 0}, {7680, 9600, 47, 5, 2}, {7680, 9600, 52, 4, 2},
	{10560, 11520, 43, 6, 3},
	// Bar 4: whole-note low E, authored as two tied halves and merged
	// back into one event by score.Events (the round-trip invariant).
	{11520, 15360, 40, 6, 0},
}

func TestImportFixtureRiff(t *testing.T) {
	s, warns, err := ImportFile("../../testdata/fixture_riff.gp")
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
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
	if tr.Name != "Guitar" || tr.Role != score.RoleUser || tr.Capo != 0 {
		t.Errorf("track = %q role %d capo %d, want Guitar role %d capo 0", tr.Name, tr.Role, tr.Capo, score.RoleUser)
	}
	if len(tr.Tuning) != len(score.StandardTuning) {
		t.Fatalf("tuning has %d strings, want %d", len(tr.Tuning), len(score.StandardTuning))
	}
	for i, want := range score.StandardTuning {
		if tr.Tuning[i] != want {
			t.Errorf("tuning[%d] = %d, want %d (standard EADGBE, string 1 = high E)", i, tr.Tuning[i], want)
		}
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
		if ev.String != want.str || ev.Fret != want.fret {
			t.Errorf("event %d fingering = string %d fret %d, want string %d fret %d",
				i, ev.String, ev.Fret, want.str, want.fret)
		}
	}

	// .gp fingering is authored, never heuristic.
	for _, tr := range s.Tracks {
		for _, bar := range tr.Bars {
			for _, beat := range bar.Beats {
				for _, n := range beat.Notes {
					if n.Inferred {
						t.Fatalf("note at tick %d on string %d is marked Inferred; .gp fingering is authored", beat.Start, n.String)
					}
				}
			}
		}
	}
}

// buildGP zips entries (name, content pairs in order) into an in-memory
// .gp archive.
func buildGP(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatalf("creating zip entry %s: %v", e[0], err)
		}
		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatalf("writing zip entry %s: %v", e[0], err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}

// gpifDoc wraps GPIF body fragments in the document frame: one track in
// standard tuning and a 120 BPM tick-0 tempo automation.
func gpifDoc(masterBars, bars, voices, beats, notes, rhythms string) string {
	return gpifDocTracks("0", trackXML(0, "G", ""), masterBars, bars, voices, beats, notes, rhythms)
}

// gpifDocTracks is gpifDoc with a caller-supplied <Tracks> block and
// master-track id list, for the multi-track cases (percussion filtering
// and the filtered-index bug).
func gpifDocTracks(masterTracks, tracks, masterBars, bars, voices, beats, notes, rhythms string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<GPIF>
  <GPRevision>7.0.0</GPRevision>
  <Score><Title>Test</Title></Score>
  <MasterTrack>
    <Tracks>` + masterTracks + `</Tracks>
    <Automations>
      <Automation><Type>Tempo</Type><Bar>0</Bar><Position>0</Position><Value>120 2</Value></Automation>
    </Automations>
  </MasterTrack>
  <Tracks>` + tracks + `</Tracks>
  <MasterBars>` + masterBars + `</MasterBars>
  <Bars>` + bars + `</Bars>
  <Voices>` + voices + `</Voices>
  <Beats>` + beats + `</Beats>
  <Notes>` + notes + `</Notes>
  <Rhythms>` + rhythms + `</Rhythms>
</GPIF>`
}

// trackXML emits a standard-tuning <Track>; extra is spliced in ahead of
// the staves, for the elements that mark a track as a drum kit.
func trackXML(id int, name, extra string) string {
	return `<Track id="` + itoa(id) + `">
      <Name>` + name + `</Name>` + extra + `
      <Staves><Staff><Properties>
        <Property name="Tuning"><Pitches>40 45 50 55 59 64</Pitches></Property>
        <Property name="CapoFret"><Fret>0</Fret></Property>
      </Properties></Staff></Staves>
    </Track>`
}

// note emits a GPIF note with the given id, GP string (0 = lowest), and fret.
func noteXML(id, gpString, fret int, extra string) string {
	return `<Note id="` + itoa(id) + `"><Properties>` +
		`<Property name="String"><String>` + itoa(gpString) + `</String></Property>` +
		`<Property name="Fret"><Fret>` + itoa(fret) + `</Fret></Property>` +
		`</Properties>` + extra + `</Note>`
}

func itoa(v int) string {
	var b bytes.Buffer
	if v < 0 {
		b.WriteByte('-')
		v = -v
	}
	var digits []byte
	for {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
		if v == 0 {
			break
		}
	}
	b.Write(digits)
	return b.String()
}

func importDoc(t *testing.T, doc string, extra ...[2]string) (*score.Score, []string, error) {
	t.Helper()
	entries := append([][2]string{{"Content/score.gpif", doc}}, extra...)
	return Import(buildGP(t, entries...))
}

func hasWarning(warns []string, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestSecondVoiceWarning: a bar whose second voice holds beats imports
// the first voice only, with a warning.
func TestSecondVoiceWarning(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice><Voice id="1"><Beats>1</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`,
		noteXML(0, 0, 0, "")+noteXML(1, 1, 5, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !hasWarning(warns, "voice") {
		t.Errorf("warnings = %v, want a second-voice warning", warns)
	}
	evs := s.Events()
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1 (first voice only)", len(evs))
	}
	if evs[0].Key != 40 || evs[0].String != 6 {
		t.Errorf("event = key %d string %d, want the first voice's low E (key 40 string 6)", evs[0].Key, evs[0].String)
	}
}

// TestStructuralErrors: only structurally unreadable files error.
func TestStructuralErrors(t *testing.T) {
	if _, _, err := Import([]byte("this is not a zip archive")); err == nil {
		t.Error("garbage bytes: want an error")
	}
	if _, _, err := Import(buildGP(t, [2]string{"VERSION", "8.0"})); err == nil || !strings.Contains(err.Error(), "score.gpif") {
		t.Errorf("zip without gpif: err = %v, want a missing score.gpif error", err)
	}
	if _, _, err := Import(buildGP(t, [2]string{"Content/score.gpif", "<html>not gpif"})); err == nil {
		t.Error("garbage XML: want an error")
	}
	if _, _, err := Import(buildGP(t, [2]string{"Content/score.gpif", "<NotGPIF></NotGPIF>"})); err == nil {
		t.Error("wrong root element: want an error")
	}
	empty := `<?xml version="1.0"?><GPIF><Score><Title>x</Title></Score></GPIF>`
	if _, _, err := Import(buildGP(t, [2]string{"Content/score.gpif", empty})); err == nil {
		t.Error("GPIF with no tracks: want an error")
	}
}

// TestPermissive: unknown XML elements, unknown note properties, and
// extra archive entries never fail an import — the unknown note property
// warns, everything else is ignored.
func TestPermissive(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><SomeNewMasterBarThing x="1"/><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Clef>G2</Clef><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Arpeggio>Up</Arpeggio><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, `<InstrumentArticulation>0</InstrumentArticulation>`),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	// Splice an unknown note property in (a bend carried as properties).
	doc = strings.Replace(doc,
		`<Property name="Fret"><Fret>0</Fret></Property>`,
		`<Property name="Fret"><Fret>0</Fret></Property><Property name="Bended"><Enable /></Property>`,
		1)
	s, warns, err := importDoc(t, doc,
		[2]string{"VERSION", "8.0"},
		[2]string{"Content/BinaryStylesheet", "\x00\x01binary junk"},
	)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !hasWarning(warns, "Bended") {
		t.Errorf("warnings = %v, want an unsupported-property warning naming Bended", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 40 || evs[0].Start != 0 || evs[0].End != score.Whole {
		t.Fatalf("events = %+v, want the single whole-note low E", evs)
	}
}

// TestBarPadAndTruncate: underfull bars gain a trailing rest, overfull
// bars are truncated, both with warnings, and the result validates.
func TestBarPadAndTruncate(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>1</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`+
			`<Bar id="1"><Voices>1 -1 -1 -1</Voices></Bar>`,
		// Bar 1: a lone quarter (underfull). Bar 2: half then whole (overfull).
		`<Voice id="0"><Beats>0</Beats></Voice><Voice id="1"><Beats>1 2</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="1" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="2" /><Notes>2</Notes></Beat>`,
		noteXML(0, 0, 0, "")+noteXML(1, 0, 3, "")+noteXML(2, 1, 0, ""),
		`<Rhythm id="0"><NoteValue>Quarter</NoteValue></Rhythm>`+
			`<Rhythm id="1"><NoteValue>Half</NoteValue></Rhythm>`+
			`<Rhythm id="2"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasWarning(warns, "padded with a rest") {
		t.Errorf("warnings = %v, want an underfull-bar padding warning", warns)
	}
	if !hasWarning(warns, "truncated") {
		t.Errorf("warnings = %v, want an overfull-bar truncation warning", warns)
	}
	evs := s.Events()
	want := []struct {
		start, end int64
		key        int
	}{
		{0, 960, 40},            // the lone quarter
		{3840, 5760, 43},        // the half
		{5760, 3840 + 3840, 45}, // the whole, truncated to the bar end
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(want), evs)
	}
	for i, wv := range want {
		if evs[i].Start != wv.start || evs[i].End != wv.end || evs[i].Key != wv.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, evs[i].Start, evs[i].End, evs[i].Key, wv.start, wv.end, wv.key)
		}
	}
}

// TestRhythmMath: augmentation dots and primary tuplets shape durations.
// One 4/4 bar: dotted quarter + eighth + three quarter-note triplets =
// 1440 + 480 + 3*640 = 3840 ticks exactly.
func TestRhythmMath(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0 1 2 3 4</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="1" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="2" /><Notes>2</Notes></Beat>`+
			`<Beat id="3"><Rhythm ref="2" /><Notes>3</Notes></Beat>`+
			`<Beat id="4"><Rhythm ref="2" /><Notes>4</Notes></Beat>`,
		noteXML(0, 0, 0, "")+noteXML(1, 0, 0, "")+noteXML(2, 0, 0, "")+
			noteXML(3, 0, 0, "")+noteXML(4, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Quarter</NoteValue><AugmentationDot count="1" /></Rhythm>`+
			`<Rhythm id="1"><NoteValue>Eighth</NoteValue></Rhythm>`+
			`<Rhythm id="2"><NoteValue>Quarter</NoteValue><PrimaryTuplet num="3" den="2" /></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	evs := s.Events()
	wantStarts := []int64{0, 1440, 1920, 2560, 3200}
	wantEnds := []int64{1440, 1920, 2560, 3200, 3840}
	if len(evs) != len(wantStarts) {
		t.Fatalf("got %d events, want %d", len(evs), len(wantStarts))
	}
	for i := range evs {
		if evs[i].Start != wantStarts[i] || evs[i].End != wantEnds[i] {
			t.Errorf("event %d = (%d,%d), want (%d,%d)", i, evs[i].Start, evs[i].End, wantStarts[i], wantEnds[i])
		}
	}
}

// TestTempoUnitsAndPosition: a mid-bar automation in half-note units
// lands at the right tick with the right quarter-note BPM.
func TestTempoUnitsAndPosition(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	// 60 BPM counted in half notes = 120 quarter-note BPM, halfway
	// through bar 2: tick 3840 + 1920 = 5760.
	doc = strings.Replace(doc, `</Automations>`,
		`<Automation><Type>Tempo</Type><Bar>1</Bar><Position>0.5</Position><Value>60 4</Value></Automation></Automations>`, 1)
	s, _, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	want := score.TempoMap{
		{Tick: 0, USPerQuarter: score.USPerQuarter(120)},
		{Tick: 5760, USPerQuarter: score.USPerQuarter(120)},
	}
	if len(s.Tempos) != len(want) {
		t.Fatalf("Tempos = %v, want %v", s.Tempos, want)
	}
	for i := range want {
		if s.Tempos[i] != want[i] {
			t.Errorf("Tempos[%d] = %v, want %v", i, s.Tempos[i], want[i])
		}
	}
}

// TestHostileDotCount: the AugmentationDot count attribute is attacker
// controlled — a huge count must not wedge the import (the old loop
// iterated count times), and negative or implausibly large counts are
// clamped to 3 with a warning. count=3 is the legitimate maximum and
// imports without a dot warning.
func TestHostileDotCount(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>1</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>2</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`+
			`<Bar id="1"><Voices>1 -1 -1 -1</Voices></Bar>`+
			`<Bar id="2"><Voices>2 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`+
			`<Voice id="1"><Beats>1</Beats></Voice>`+
			`<Voice id="2"><Beats>2</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="1" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="2" /><Notes>2</Notes></Beat>`,
		noteXML(0, 0, 0, "")+noteXML(1, 0, 0, "")+noteXML(2, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Quarter</NoteValue><AugmentationDot count="3" /></Rhythm>`+
			`<Rhythm id="1"><NoteValue>Quarter</NoteValue><AugmentationDot count="-5" /></Rhythm>`+
			`<Rhythm id="2"><NoteValue>Quarter</NoteValue><AugmentationDot count="9000000000000000000" /></Rhythm>`,
	)
	data := buildGP(t, [2]string{"Content/score.gpif", doc})

	type result struct {
		s     *score.Score
		warns []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		s, warns, err := Import(data)
		ch <- result{s, warns, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-time.After(time.Minute):
		t.Fatal("Import did not finish within a minute; a hostile augmentation dot count wedged it")
	}
	if res.err != nil {
		t.Fatalf("Import: %v", res.err)
	}
	if err := res.s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasWarning(res.warns, "dot count -5") {
		t.Errorf("warnings = %v, want a dot-count warning for -5", res.warns)
	}
	if !hasWarning(res.warns, "dot count 9000000000000000000") {
		t.Errorf("warnings = %v, want a dot-count warning for 9000000000000000000", res.warns)
	}
	if hasWarning(res.warns, "dot count 3") {
		t.Errorf("warnings = %v, count=3 is legitimate and must not warn", res.warns)
	}
	// A triple-dotted quarter is 960+480+240+120 = 1800 ticks; the
	// hostile counts clamp to the same duration.
	evs := res.s.Events()
	want := []struct{ start, end int64 }{{0, 1800}, {3840, 5640}, {7680, 9480}}
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(want), evs)
	}
	for i, wv := range want {
		if evs[i].Start != wv.start || evs[i].End != wv.end {
			t.Errorf("event %d = (%d,%d), want (%d,%d)", i, evs[i].Start, evs[i].End, wv.start, wv.end)
		}
	}
}

// bombGP zips the given deflate stream into a Content/score.gpif entry
// whose header claims `claimed` uncompressed bytes. CreateRaw writes the
// header fields verbatim, so claimed is free to lie about the stream.
func bombGP(t *testing.T, compressed []byte, crc uint32, claimed uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "Content/score.gpif",
		Method:             zip.Deflate,
		CRC32:              crc,
		CompressedSize64:   uint64(len(compressed)),
		UncompressedSize64: claimed,
	})
	if err != nil {
		t.Fatalf("CreateRaw: %v", err)
	}
	if _, err := w.Write(compressed); err != nil {
		t.Fatalf("writing raw entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}

// TestZipBombRejected: a small archive whose score.gpif inflates past
// the 64 MiB cap errors out instead of ballooning memory, whether the
// zip header admits the size or lies about it.
func TestZipBombRejected(t *testing.T) {
	// Deflate 65 MiB of zeros — just past the cap — into ~65 KB.
	const bombSize = 65 << 20
	chunk := make([]byte, 64<<10)
	var comp bytes.Buffer
	fw, err := flate.NewWriter(&comp, flate.BestCompression)
	if err != nil {
		t.Fatalf("flate.NewWriter: %v", err)
	}
	crc := crc32.NewIEEE()
	for written := 0; written < bombSize; written += len(chunk) {
		if _, err := fw.Write(chunk); err != nil {
			t.Fatalf("compressing bomb: %v", err)
		}
		crc.Write(chunk)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("closing flate writer: %v", err)
	}
	if comp.Len() > 1<<20 {
		t.Fatalf("test bomb is %d bytes compressed, want under 1 MiB", comp.Len())
	}

	// Honest header: the declared size alone is over the cap, and the
	// import must say so without decompressing anything.
	start := time.Now()
	_, _, err = Import(bombGP(t, comp.Bytes(), crc.Sum32(), bombSize))
	if err == nil || !strings.Contains(err.Error(), "zip bomb") {
		t.Errorf("honest 65 MiB bomb: err = %v, want a possible-zip-bomb error", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("honest bomb took %v to reject, want a fast failure", elapsed)
	}

	// Forged header: claims 1 KiB but the stream inflates to 65 MiB.
	// The header must not be trusted alone — the read has to stay
	// bounded and fail, whether the zip layer or the importer's
	// LimitReader cap notices first.
	_, _, err = Import(bombGP(t, comp.Bytes(), crc.Sum32(), 1024))
	if err == nil {
		t.Error("forged-header bomb: import succeeded, want an error")
	}
}

// TestHostileTempoPosition: tempo automation positions of -1, NaN, and
// 1e18 must warn and clamp — never produce a negative or overflowed
// tick that unsorts the tempo map and fails the whole import.
func TestHostileTempoPosition(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	doc = strings.Replace(doc, `</Automations>`,
		`<Automation><Type>Tempo</Type><Bar>0</Bar><Position>-1</Position><Value>100 2</Value></Automation>`+
			`<Automation><Type>Tempo</Type><Bar>1</Bar><Position>NaN</Position><Value>90 2</Value></Automation>`+
			`<Automation><Type>Tempo</Type><Bar>2</Bar><Position>1e18</Position><Value>60 2</Value></Automation>`+
			`</Automations>`, 1)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasWarning(warns, "outside [0,1]") {
		t.Errorf("warnings = %v, want an out-of-range position warning", warns)
	}
	if !hasWarning(warns, "not a number") {
		t.Errorf("warnings = %v, want a NaN position warning", warns)
	}
	// Position -1 clamps to the bar 1 start (tick 0, overriding the
	// frame's 120 there), NaN to the bar 2 start, 1e18 to the bar 3 end.
	want := score.TempoMap{
		{Tick: 0, USPerQuarter: score.USPerQuarter(100)},
		{Tick: 3840, USPerQuarter: score.USPerQuarter(90)},
		{Tick: 11520, USPerQuarter: score.USPerQuarter(60)},
	}
	if len(s.Tempos) != len(want) {
		t.Fatalf("Tempos = %v, want %v", s.Tempos, want)
	}
	for i := range want {
		if s.Tempos[i] != want[i] {
			t.Errorf("Tempos[%d] = %v, want %v", i, s.Tempos[i], want[i])
		}
	}
}

// TestAbsurdTempoSkipped: a BPM so large it rounds to zero microseconds
// per quarter (and a NaN BPM) is skipped with a warning instead of
// planting an invalid tempo that fails validation.
func TestAbsurdTempoSkipped(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	doc = strings.Replace(doc, `</Automations>`,
		`<Automation><Type>Tempo</Type><Bar>0</Bar><Position>0.5</Position><Value>1e18 2</Value></Automation>`+
			`<Automation><Type>Tempo</Type><Bar>0</Bar><Position>0.5</Position><Value>NaN 2</Value></Automation>`+
			`</Automations>`, 1)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasWarning(warns, "absurd tempo") {
		t.Errorf("warnings = %v, want an absurd-tempo warning", warns)
	}
	if len(s.Tempos) != 1 || s.Tempos[0] != (score.Tempo{Tick: 0, USPerQuarter: score.USPerQuarter(120)}) {
		t.Errorf("Tempos = %v, want only the frame's 120 BPM at tick 0", s.Tempos)
	}
}

// TestImportTuningTooManyStrings: a Tuning property declaring 30 strings
// is past the 25-string limit — warned, and standard tuning assumed —
// because an absurd tuning passes Validate and then chokes tab rendering.
func TestImportTuningTooManyStrings(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	pitches := strings.TrimSpace(strings.Repeat("40 ", 30))
	doc = strings.Replace(doc, `<Pitches>40 45 50 55 59 64</Pitches>`, `<Pitches>`+pitches+`</Pitches>`, 1)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !hasWarning(warns, "more than the 25-string limit") {
		t.Errorf("warnings = %v, want one naming the 25-string limit", warns)
	}
	if !hasWarning(warns, "assuming standard") {
		t.Errorf("warnings = %v, want the standard-tuning fallback", warns)
	}
	tr := s.Tracks[0]
	if len(tr.Tuning) != len(score.StandardTuning) {
		t.Fatalf("tuning has %d strings, want the standard %d", len(tr.Tuning), len(score.StandardTuning))
	}
	for i, want := range score.StandardTuning {
		if tr.Tuning[i] != want {
			t.Errorf("tuning[%d] = %d, want %d (standard EADGBE)", i, tr.Tuning[i], want)
		}
	}
}

// TestImportHugeTimeSignatureRejected: a numerator that is a multiple of
// 2^56 used to wrap the bar-length multiplication to exactly zero,
// making every bar zero-length while still passing Validate. This is a
// structural absurdity — not a droppable note — so the import must fail,
// naming the limit.
func TestImportHugeTimeSignatureRejected(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>72057594037927936/1</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	_, _, err := importDoc(t, doc)
	if err == nil || !strings.Contains(err.Error(), "numerator above the 256 limit") {
		t.Errorf("err = %v, want a numerator-limit error", err)
	}
}

// TestImportOutOfRangeFretDropped: one fret-64 note must not make the
// final Validate reject the whole score ("string 1 fret 64 sounds MIDI
// key 128, outside 0-127") — the note drops with a warning and the rest
// of the file imports intact.
func TestImportOutOfRangeFretDropped(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0 1</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`,
		noteXML(0, 0, 0, "")+noteXML(1, 0, 64, ""),
		`<Rhythm id="0"><NoteValue>Half</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v (one absurd fret must not fail the import)", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasWarning(warns, "fret 64") {
		t.Errorf("warnings = %v, want one naming fret 64", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 40 || evs[0].Start != 0 || evs[0].End != score.Half {
		t.Fatalf("events = %+v, want only the surviving half note at key 40", evs)
	}
}

// TestCapoAndTuning: a capo and a drop-D tuning shift derived pitches.
func TestCapoAndTuning(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""), // open lowest string
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	doc = strings.Replace(doc, `<Pitches>40 45 50 55 59 64</Pitches>`, `<Pitches>38 45 50 55 59 64</Pitches>`, 1)
	doc = strings.Replace(doc, `<Property name="CapoFret"><Fret>0</Fret></Property>`, `<Property name="CapoFret"><Fret>2</Fret></Property>`, 1)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	tr := s.Tracks[0]
	if tr.Capo != 2 {
		t.Errorf("Capo = %d, want 2", tr.Capo)
	}
	if tr.Tuning[5] != 38 || tr.Tuning[0] != 64 {
		t.Errorf("Tuning = %v, want drop D low string (38) and high E (64)", tr.Tuning)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 40 { // 38 open + capo 2
		t.Fatalf("events = %+v, want one note at key 40 (drop D + capo 2)", evs)
	}
}

// percNoteXML emits a GPIF percussion note. A drum hit is spelled with
// Element/Variation — which kit piece, which articulation — and never
// with String/Fret, which is precisely why the pitched conversion path
// cannot map one.
func percNoteXML(id, element int) string {
	return `<Note id="` + itoa(id) + `"><Properties>` +
		`<Property name="Element"><Element>` + itoa(element) + `</Element></Property>` +
		`<Property name="Variation"><Variation>0</Variation></Property>` +
		`</Properties></Note>`
}

// percussionDoc builds a two-track document: track 1 is a drum kit marked
// by mark, holding four hits, and track 2 is a guitar holding one whole
// note at fret 5 on the low E.
func percussionDoc(mark string) string {
	return gpifDocTracks("0 1",
		trackXML(0, "Drums", mark)+trackXML(1, "G", ""),
		`<MasterBar><Time>4/4</Time><Bars>0 1</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`+
			`<Bar id="1"><Voices>1 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0 1 2 3</Beats></Voice><Voice id="1"><Beats>4</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="0" /><Notes>2</Notes></Beat>`+
			`<Beat id="3"><Rhythm ref="0" /><Notes>3</Notes></Beat>`+
			`<Beat id="4"><Rhythm ref="1" /><Notes>4</Notes></Beat>`,
		percNoteXML(0, 36)+percNoteXML(1, 38)+percNoteXML(2, 42)+percNoteXML(3, 38)+
			noteXML(4, 0, 5, ""),
		`<Rhythm id="0"><NoteValue>Quarter</NoteValue></Rhythm>`+
			`<Rhythm id="1"><NoteValue>Whole</NoteValue></Rhythm>`)
}

// trackNames lists a score's track names, for failure messages.
func trackNames(s *score.Score) []string {
	var names []string
	for _, tr := range s.Tracks {
		names = append(names, tr.Name)
	}
	return names
}

// TestPercussionTrackSkipped (audit G1): a drum track must not be run
// through the pitched string/fret path. That path can map nothing, so
// every hit became a rest — with one warning PER NOTE — and because the
// drums were the file's first track the resulting empty part was handed
// to the user as the thing to practise. The track must be dropped
// instead, with exactly one warning naming it, leaving the guitar as
// RoleUser.
func TestPercussionTrackSkipped(t *testing.T) {
	s, warns, err := importDoc(t, percussionDoc(`<InstrumentSet><Name>Drumkit</Name><Type>drumKit</Type></InstrumentSet>`))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks %v, want 1 (the drum track dropped)", len(s.Tracks), trackNames(s))
	}
	tr := s.Tracks[0]
	if tr.Name != "G" || tr.Role != score.RoleUser {
		t.Errorf("practice track = %q role %d, want G role %d — a percussion track must never be the user's part",
			tr.Name, tr.Role, score.RoleUser)
	}
	if len(warns) != 1 {
		t.Fatalf("warnings = %v, want exactly one (per track, not per note)", warns)
	}
	if !hasWarning(warns, "percussion") || !hasWarning(warns, "Drums") {
		t.Errorf("warning = %q, want one naming the track and saying it is percussion", warns[0])
	}
	if hasWarning(warns, "String/Fret") {
		t.Errorf("warnings = %v, want no per-note String/Fret complaints", warns)
	}
	// The guitar's own music survives intact: low E (40) at fret 5.
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 45 || evs[0].String != 6 || evs[0].Fret != 5 {
		t.Fatalf("events = %+v, want the guitar's single whole note (key 45, string 6 fret 5)", evs)
	}
}

// TestPercussionMarkers (audit G1): GPIF flags a drum kit in more than
// one place depending on the writing version, and every marker must be
// honoured — while an ordinary fretted track must not be mistaken for one.
func TestPercussionMarkers(t *testing.T) {
	tests := []struct {
		name string
		mark string
		perc bool
	}{
		{"instrument set type", `<InstrumentSet><Type>drumKit</Type></InstrumentSet>`, true},
		{"instrument set name", `<InstrumentSet><Name>Percussion</Name></InstrumentSet>`, true},
		{"instrument ref", `<Instrument ref="drmkt" />`, true},
		{"general midi channel 10", `<GeneralMidi><PrimaryChannel>9</PrimaryChannel></GeneralMidi>`, true},
		{"guitar instrument ref", `<Instrument ref="e-gtr6" />`, false},
		{"general midi channel 1", `<GeneralMidi><PrimaryChannel>0</PrimaryChannel></GeneralMidi>`, false},
		{"no marker at all", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, warns, err := importDoc(t, percussionDoc(tt.mark))
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			wantTracks := 2
			if tt.perc {
				wantTracks = 1
			}
			if len(s.Tracks) != wantTracks {
				t.Fatalf("got %d tracks %v, want %d", len(s.Tracks), trackNames(s), wantTracks)
			}
			if got := hasWarning(warns, "percussion"); got != tt.perc {
				t.Errorf("percussion warning = %v, want %v (warnings %v)", got, tt.perc, warns)
			}
		})
	}
}

// TestAllPercussionTracksError (audit G1): a file that is nothing but
// drums has no part this importer can teach. It must say so rather than
// hand back a score of silence.
func TestAllPercussionTracksError(t *testing.T) {
	doc := gpifDocTracks("0",
		trackXML(0, "Drums", `<InstrumentSet><Type>drumKit</Type></InstrumentSet>`),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		percNoteXML(0, 36),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`)
	_, warns, err := importDoc(t, doc)
	if err == nil || !strings.Contains(err.Error(), "every track in the file is percussion") {
		t.Fatalf("err = %v, want an all-percussion error", err)
	}
	if !hasWarning(warns, "percussion") {
		t.Errorf("warnings = %v, want the per-track percussion warning kept alongside the error", warns)
	}
}

// threeTrackBarsDoc builds a three-slot document whose middle slot is
// filtered out: the master track lists three tracks, each master bar
// lists three bar ids, and the three bars carry frets 1, 3 and 7 so a
// track reading the wrong slot is immediately visible.
//
// middleID is the master-track id in the middle slot (either one that
// resolves to nothing, or a percussion track's), and middleTrack the
// <Track> element backing it, if any.
func threeTrackBarsDoc(middleID, middleTrack string) string {
	return gpifDocTracks("0 "+middleID+" 2",
		trackXML(0, "A", "")+middleTrack+trackXML(2, "B", ""),
		`<MasterBar><Time>4/4</Time><Bars>0 1 2</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`+
			`<Bar id="1"><Voices>1 -1 -1 -1</Voices></Bar>`+
			`<Bar id="2"><Voices>2 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`+
			`<Voice id="1"><Beats>1</Beats></Voice>`+
			`<Voice id="2"><Beats>2</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="0" /><Notes>2</Notes></Beat>`,
		noteXML(0, 0, 1, "")+noteXML(1, 0, 3, "")+noteXML(2, 0, 7, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`)
}

// assertOwnBars checks that the two surviving tracks kept the bars
// belonging to their own slots (frets 1 and 7), not the filtered middle
// slot's (fret 3).
func assertOwnBars(t *testing.T, s *score.Score) {
	t.Helper()
	if len(s.Tracks) != 2 {
		t.Fatalf("got %d tracks %v, want 2", len(s.Tracks), trackNames(s))
	}
	want := []struct {
		name string
		fret int
	}{{"A", 1}, {"B", 7}}
	for i, w := range want {
		tr := s.Tracks[i]
		if tr.Name != w.name {
			t.Errorf("track %d = %q, want %q", i, tr.Name, w.name)
		}
		notes := tr.Bars[0].Beats[0].Notes
		if len(notes) != 1 {
			t.Fatalf("track %q bar 1 has %d notes, want 1", tr.Name, len(notes))
		}
		if notes[0].Fret != w.fret {
			t.Errorf("track %q plays fret %d, want %d — it was handed another slot's bars",
				tr.Name, notes[0].Fret, w.fret)
		}
	}
}

// TestFilteredTrackKeepsItsOwnBars (audit G2): <MasterBar><Bars> is
// indexed by the ORIGINAL track order, but the lookup used the track's
// index in the filtered slice. Drop the middle track and every later
// track silently reads a preceding slot's bars — here B would be taught
// the missing track's fret 3 instead of its own fret 7.
func TestFilteredTrackKeepsItsOwnBars(t *testing.T) {
	// The master track's middle slot references id 99, which does not exist.
	s, warns, err := importDoc(t, threeTrackBarsDoc("99", ""))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !hasWarning(warns, "does not exist") {
		t.Errorf("warnings = %v, want one about the missing track", warns)
	}
	assertOwnBars(t, s)
}

// TestPercussionFilterKeepsLaterTrackBars (audit G1 + G2): dropping
// percussion is the filtering that makes the G2 index mismatch common, so
// the two fixes are pinned together — a drum track between two guitars
// must not shift the second guitar onto the drums' bars.
func TestPercussionFilterKeepsLaterTrackBars(t *testing.T) {
	drums := trackXML(1, "Drums", `<InstrumentSet><Type>drumKit</Type></InstrumentSet>`)
	s, warns, err := importDoc(t, threeTrackBarsDoc("1", drums))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !hasWarning(warns, "percussion") {
		t.Errorf("warnings = %v, want the percussion warning", warns)
	}
	assertOwnBars(t, s)
	if s.Tracks[0].Role != score.RoleUser {
		t.Errorf("track A role = %d, want RoleUser %d", s.Tracks[0].Role, score.RoleUser)
	}
}

// TestHostileTuplet: a PrimaryTuplet denominator of 1e18 overflowed
// dur*int64(den), wrapping the beat to a length of roughly 7.7e17 ticks
// that fillBar then truncated to the bar — the note came out wrong and
// only an unrelated "overfill" warning mentioned it at all. Implausible
// tuplets must be rejected by name, leaving the plain note value.
func TestHostileTuplet(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Quarter</NoteValue><PrimaryTuplet num="1" den="1000000000000000000" /></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasWarning(warns, "implausible tuplet") {
		t.Errorf("warnings = %v, want one naming the implausible tuplet", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != score.Quarter {
		t.Fatalf("events = %+v, want one plain quarter note spanning [0,960)", evs)
	}
}

// TestPitchedInstrumentsAreNotPercussion is the fix-verification
// regression for the percussion filter. Detecting kits by substring
// deleted every pitched instrument whose name merely contains the word:
// General MIDI has Steel Drums (115), Percussive Organ (18) and Melodic
// Tom, GPIF's own family for tuned mallets is literally
// "pitchedPercussion", and refs like "perc-vibraphone" are ordinary
// fretted-renderable parts. Each vanished from the score with a warning
// claiming it was drums — the player opened a file and their part was
// simply gone.
func TestPitchedInstrumentsAreNotPercussion(t *testing.T) {
	tests := []struct {
		name string
		mark string
	}{
		{"steel drums (GM 115)", `<InstrumentSet><Name>Steel Drums</Name></InstrumentSet>`},
		{"pitched percussion family", `<InstrumentSet><Type>pitchedPercussion</Type></InstrumentSet>`},
		{"percussive organ (GM 18)", `<InstrumentSet><Name>Percussive Organ</Name></InstrumentSet>`},
		{"vibraphone ref", `<Instrument ref="perc-vibraphone" />`},
		{"melodic tom", `<InstrumentSet><Name>Melodic Tom</Name></InstrumentSet>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, warns, err := importDoc(t, percussionDoc(tt.mark))
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if len(s.Tracks) != 2 {
				t.Fatalf("got %d tracks %v, want 2 — a pitched part was dropped as percussion (warnings %v)",
					len(s.Tracks), trackNames(s), warns)
			}
			if hasWarning(warns, "percussion") {
				t.Errorf("a pitched instrument was reported as percussion: %v", warns)
			}
		})
	}
}

// countWarnings counts the warnings containing substr, for the cases
// where "exactly one aggregate warning" is itself the contract.
func countWarnings(warns []string, substr string) int {
	n := 0
	for _, w := range warns {
		if strings.Contains(w, substr) {
			n++
		}
	}
	return n
}

// pitchPropXML emits one GP7+ pitch property (ConcertPitch or
// TransposedPitch): a spelled step, an accidental ("", "#", "b", "x",
// "bb"), and a scientific octave (C4 = MIDI 60).
func pitchPropXML(name, step, accidental string, octave int) string {
	return `<Property name="` + name + `"><Pitch><Step>` + step + `</Step>` +
		`<Accidental>` + accidental + `</Accidental>` +
		`<Octave>` + itoa(octave) + `</Octave></Pitch></Property>`
}

// windNoteXML emits a GP7+ non-fretted note: pitch properties only,
// no String/Fret.
func windNoteXML(id int, props, extra string) string {
	return `<Note id="` + itoa(id) + `"><Properties>` + props + `</Properties>` + extra + `</Note>`
}

// windTrackXML emits a <Track> with no Tuning property — the shape GP
// gives a non-fretted staff; extra carries GeneralMidi or other markers.
func windTrackXML(id int, name, extra string) string {
	return `<Track id="` + itoa(id) + `"><Name>` + name + `</Name>` + extra +
		`<Staves><Staff><Properties></Properties></Staff></Staves></Track>`
}

// TestImportWindSopranoSax: a track with no Tuning property, a
// GeneralMidi program of 64, and ConcertPitch/TransposedPitch notes (the
// soprano sax's +2 written delta) imports as the soprano sax — no
// strings, no capo, every note on lane 1 at Fret = key − LowSounding,
// nothing Inferred, and no warnings. Key 92 sits above the standard Span
// (56+32 = 88) and its lane fret (36) above maxImportFret: altissimo is
// real and the fretboard cap must not apply to a wind lane. Bar 2 is two
// tied halves that score.Events merges back into one event.
func TestImportWindSopranoSax(t *testing.T) {
	doc := gpifDocTracks("0",
		windTrackXML(0, "Sax", `<GeneralMidi><Program>64</Program></GeneralMidi>`),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>1</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`+
			`<Bar id="1"><Voices>1 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0 1 2 3</Beats></Voice><Voice id="1"><Beats>4 5</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="0" /><Notes>2</Notes></Beat>`+
			`<Beat id="3"><Rhythm ref="0" /><Notes>3</Notes></Beat>`+
			`<Beat id="4"><Rhythm ref="1" /><Notes>4</Notes></Beat>`+
			`<Beat id="5"><Rhythm ref="1" /><Notes>5</Notes></Beat>`,
		// G#3 (56), Ab4 (68), Cbb5 (70, exercising "bb"), G#6 (92,
		// altissimo), then the tied Ab4 pair; every written pitch is the
		// concert pitch plus 2.
		windNoteXML(0, pitchPropXML("ConcertPitch", "G", "#", 3)+pitchPropXML("TransposedPitch", "A", "#", 3), "")+
			windNoteXML(1, pitchPropXML("ConcertPitch", "A", "b", 4)+pitchPropXML("TransposedPitch", "B", "b", 4), "")+
			windNoteXML(2, pitchPropXML("ConcertPitch", "C", "bb", 5)+pitchPropXML("TransposedPitch", "C", "", 5), "")+
			windNoteXML(3, pitchPropXML("ConcertPitch", "G", "#", 6)+pitchPropXML("TransposedPitch", "A", "#", 6), "")+
			windNoteXML(4, pitchPropXML("ConcertPitch", "A", "b", 4)+pitchPropXML("TransposedPitch", "B", "b", 4),
				`<Tie origin="true" destination="false" />`)+
			windNoteXML(5, pitchPropXML("ConcertPitch", "A", "b", 4)+pitchPropXML("TransposedPitch", "B", "b", 4),
				`<Tie origin="false" destination="true" />`),
		`<Rhythm id="0"><NoteValue>Quarter</NoteValue></Rhythm>`+
			`<Rhythm id="1"><NoteValue>Half</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.Wind == nil || tr.Wind.Name != "soprano sax" {
		t.Fatalf("Wind = %v, want the soprano sax", tr.Wind)
	}
	if tr.Tuning != nil || tr.Capo != 0 {
		t.Errorf("Tuning = %v Capo = %d, want no strings and no capo on a wind track", tr.Tuning, tr.Capo)
	}
	if tr.Name != "Sax" || tr.Program != 64 || tr.Role != score.RoleUser {
		t.Errorf("track = %q program %d role %d, want Sax program 64 role %d", tr.Name, tr.Program, tr.Role, score.RoleUser)
	}
	evs := s.Events()
	want := []struct {
		start, end int64
		key        int
	}{
		{0, 960, 56}, {960, 1920, 68}, {1920, 2880, 70}, {2880, 3840, 92},
		{3840, 7680, 68}, // the tied halves, merged back by Events
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events %+v, want %d", len(evs), evs, len(want))
	}
	for i, wv := range want {
		ev := evs[i]
		if ev.Start != wv.start || ev.End != wv.end || ev.Key != wv.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, ev.Start, ev.End, ev.Key, wv.start, wv.end, wv.key)
		}
		if ev.String != 1 || ev.Fret != wv.key-56 {
			t.Errorf("event %d = string %d fret %d, want the lane (string 1 fret %d)",
				i, ev.String, ev.Fret, wv.key-56)
		}
	}
	for _, bar := range tr.Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if n.Inferred {
					t.Fatalf("note at tick %d is marked Inferred; a wind lane is arithmetic, not a guess", beat.Start)
				}
			}
		}
	}
}

// TestImportWindDeltaResolvesClarinet: with no GeneralMidi program the
// transposition delta plus the note range must pick the instrument, and
// only when they pick exactly one. Delta +2 alone fits the soprano sax,
// the clarinet, and the trumpet; a note at D3 (50) sits below the sax
// (56) and the trumpet (52), leaving the clarinet as the single match.
func TestImportWindDeltaResolvesClarinet(t *testing.T) {
	doc := gpifDocTracks("0",
		windTrackXML(0, "Horn", ""),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0 1</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`,
		windNoteXML(0, pitchPropXML("ConcertPitch", "D", "", 3)+pitchPropXML("TransposedPitch", "E", "", 3), "")+
			windNoteXML(1, pitchPropXML("ConcertPitch", "D", "", 4)+pitchPropXML("TransposedPitch", "E", "", 4), ""),
		`<Rhythm id="0"><NoteValue>Half</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	tr := s.Tracks[0]
	if tr.Wind == nil || tr.Wind.Name != "clarinet" {
		t.Fatalf("Wind = %v, want the clarinet (the only +2 instrument reaching D3)", tr.Wind)
	}
	if tr.Program != 71 {
		t.Errorf("Program = %d, want the clarinet's 71 (the file declared none)", tr.Program)
	}
	evs := s.Events()
	if len(evs) != 2 || evs[0].Key != 50 || evs[0].Fret != 0 || evs[1].Key != 62 || evs[1].Fret != 12 {
		t.Fatalf("events = %+v, want D3 on fret 0 and D4 on fret 12", evs)
	}
}

// TestImportWindChordKeepsHighest: a beat listing three notes cannot all
// sound on a monophonic instrument; the highest sounding note survives —
// melody on top — with one aggregate warning, not one per dropped note.
// The top note is spelled Fx4 (double sharp), exercising "x".
func TestImportWindChordKeepsHighest(t *testing.T) {
	doc := gpifDocTracks("0",
		windTrackXML(0, "Sax", `<GeneralMidi><Program>64</Program></GeneralMidi>`),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0 1 2</Notes></Beat>`,
		windNoteXML(0, pitchPropXML("ConcertPitch", "C", "", 4), "")+
			windNoteXML(1, pitchPropXML("ConcertPitch", "E", "", 4), "")+
			windNoteXML(2, pitchPropXML("ConcertPitch", "F", "x", 4), ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 67 || evs[0].String != 1 || evs[0].Fret != 11 {
		t.Fatalf("events = %+v, want only the chord's highest note (key 67) on the lane", evs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "kept only the highest note of 1 chord(s); a soprano sax plays one note at a time") {
		t.Errorf("warnings = %v, want exactly the aggregate chord warning", warns)
	}
}

// TestImportWindRangeDrops: below the instrument a note is dropped, never
// octave-rewritten; above, the ceiling is the WRITTEN pitch — G9 (127)
// sounds fine but a soprano sax player would read 129, which has no MIDI
// note name, so the text format could never save it. Each drop warns and
// the surviving note imports intact.
func TestImportWindRangeDrops(t *testing.T) {
	doc := gpifDocTracks("0",
		windTrackXML(0, "Sax", `<GeneralMidi><Program>64</Program></GeneralMidi>`),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0 1 2</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="1" /><Notes>2</Notes></Beat>`,
		windNoteXML(0, pitchPropXML("ConcertPitch", "D", "", 3), "")+ // 50, below 56
			windNoteXML(1, pitchPropXML("ConcertPitch", "G", "", 9), "")+ // 127, written 129
			windNoteXML(2, pitchPropXML("ConcertPitch", "A", "b", 4), ""), // 68, fine
		`<Rhythm id="0"><NoteValue>Quarter</NoteValue></Rhythm>`+
			`<Rhythm id="1"><NoteValue>Half</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !hasWarning(warns, "dropped note (key 50): below the soprano sax's lowest note (key 56)") {
		t.Errorf("warnings = %v, want the below-range drop naming key 50 and the floor", warns)
	}
	if !hasWarning(warns, "dropped note (key 127): its written pitch on a soprano sax is past MIDI 127") {
		t.Errorf("warnings = %v, want the written-ceiling drop naming key 127", warns)
	}
	if len(warns) != 2 {
		t.Errorf("warnings = %v, want exactly the two range drops", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 68 || evs[0].Start != 1920 || evs[0].End != 3840 {
		t.Fatalf("events = %+v, want only the surviving Ab4 half note", evs)
	}
}

// TestImportWindTransposeMismatchWarns: the chosen instrument comes from
// the declared program; a TransposedPitch that contradicts its
// transposition is reported once per track and the concert pitch wins —
// the model stores sounding pitch, and the written pitch is only
// evidence.
func TestImportWindTransposeMismatchWarns(t *testing.T) {
	doc := gpifDocTracks("0",
		windTrackXML(0, "Sax", `<GeneralMidi><Program>64</Program></GeneralMidi>`),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		// Ab4 concert (68) but C5 written (72): a +4 delta on a +2 horn.
		windNoteXML(0, pitchPropXML("ConcertPitch", "A", "b", 4)+pitchPropXML("TransposedPitch", "C", "", 5), ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 68 {
		t.Fatalf("events = %+v, want the concert Ab4 kept", evs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0],
		"the written pitch on 1 note(s) disagrees with the soprano sax's transposition; the concert pitch wins") {
		t.Errorf("warnings = %v, want exactly the one transposition cross-check warning", warns)
	}
}

// TestImportTuningAndPitchStaysFretted: a Tuning property is a fretted
// track's signature, and destroying a tab is worse than importing
// nothing — pitch properties on its notes must never flip the track to
// wind, whether they ride alongside the authored fingering or stand
// alone.
func TestImportTuningAndPitchStaysFretted(t *testing.T) {
	frame := func(notes string) string {
		return gpifDoc(
			`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
			`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
			`<Voice id="0"><Beats>0</Beats></Voice>`,
			`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
			notes,
			`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
		)
	}
	t.Run("pitch alongside authored fingering", func(t *testing.T) {
		// Low E string fret 5 sounds A2 (45); the pitch properties
		// restate it and are redundant — the import stays clean.
		doc := frame(`<Note id="0"><Properties>` +
			`<Property name="String"><String>0</String></Property>` +
			`<Property name="Fret"><Fret>5</Fret></Property>` +
			pitchPropXML("ConcertPitch", "A", "", 2) +
			pitchPropXML("TransposedPitch", "A", "", 2) +
			`</Properties></Note>`)
		s, warns, err := importDoc(t, doc)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if len(warns) != 0 {
			t.Errorf("warnings = %v, want none", warns)
		}
		tr := s.Tracks[0]
		if tr.Wind != nil {
			t.Fatalf("Wind = %v, want a fretted track — a Tuning property must veto wind classification", tr.Wind)
		}
		if len(tr.Tuning) != len(score.StandardTuning) {
			t.Fatalf("tuning has %d strings, want %d", len(tr.Tuning), len(score.StandardTuning))
		}
		evs := s.Events()
		if len(evs) != 1 || evs[0].Key != 45 || evs[0].String != 6 || evs[0].Fret != 5 {
			t.Fatalf("events = %+v, want the authored string 6 fret 5 (key 45)", evs)
		}
	})
	t.Run("pitch only", func(t *testing.T) {
		doc := frame(windNoteXML(0, pitchPropXML("ConcertPitch", "A", "", 2)+pitchPropXML("TransposedPitch", "A", "", 2), ""))
		s, warns, err := importDoc(t, doc)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if s.Tracks[0].Wind != nil {
			t.Fatalf("Wind = %v, want a fretted track", s.Tracks[0].Wind)
		}
		if len(s.Events()) != 0 {
			t.Fatalf("events = %+v, want none (the fingerless note skips)", s.Events())
		}
		if !hasWarning(warns, "no String/Fret properties; skipped") {
			t.Errorf("warnings = %v, want today's per-note skip", warns)
		}
		if hasWarning(warns, "guess") {
			t.Errorf("warnings = %v, want no wind-classification warning — the Tuning property settled it", warns)
		}
		if hasWarning(warns, "is not supported") {
			t.Errorf("warnings = %v, pitch properties are recognized and must not be reported unknown", warns)
		}
	})
}

// TestImportWindAmbiguousSkipped: a wind candidate whose instrument
// cannot be pinned to exactly one registry entry keeps today's behavior
// — the notes skip with per-note warnings and the standard-tuning
// fallback stands — plus exactly one aggregate warning naming the
// evidence that was missing. Guessing is never on the table.
func TestImportWindAmbiguousSkipped(t *testing.T) {
	buildDoc := func(trackExtra string, noteProps []string) string {
		rhythm := `<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`
		var beats, notes, beatIDs string
		for i, p := range noteProps {
			if i > 0 {
				beatIDs += " "
			}
			beatIDs += itoa(i)
			beats += `<Beat id="` + itoa(i) + `"><Rhythm ref="0" /><Notes>` + itoa(i) + `</Notes></Beat>`
			notes += windNoteXML(i, p, "")
		}
		if len(noteProps) == 2 {
			rhythm = `<Rhythm id="0"><NoteValue>Half</NoteValue></Rhythm>`
		}
		return gpifDocTracks("0",
			windTrackXML(0, "Horn", trackExtra),
			`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
			`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
			`<Voice id="0"><Beats>`+beatIDs+`</Beats></Voice>`,
			beats, notes, rhythm)
	}
	c4 := pitchPropXML("ConcertPitch", "C", "", 4)
	tests := []struct {
		name       string
		trackExtra string
		noteProps  []string
		reason     string
	}{
		{"no transposed pitch", "", []string{c4},
			"no note carries a transposed pitch to reveal the transposition"},
		{"inconsistent transposition", "", []string{
			c4 + pitchPropXML("TransposedPitch", "D", "", 4),
			pitchPropXML("ConcertPitch", "D", "", 4) + pitchPropXML("TransposedPitch", "F", "", 4)},
			"written-minus-concert transposition is inconsistent"},
		{"transposition matches nothing", "", []string{
			c4 + pitchPropXML("TransposedPitch", "F", "", 4)},
			"no wind instrument this app knows matches the notes' transposition (+5 semitones written) and range"},
		{"several instruments fit", "", []string{
			c4 + pitchPropXML("TransposedPitch", "D", "", 4),
			pitchPropXML("ConcertPitch", "B", "b", 4) + pitchPropXML("TransposedPitch", "C", "", 5)},
			"the pitch evidence fits more than one wind instrument (soprano sax, clarinet, trumpet)"},
		{"program is not a wind", `<GeneralMidi><Program>0</Program></GeneralMidi>`, []string{
			c4 + pitchPropXML("TransposedPitch", "D", "", 4)},
			"the track's General MIDI program 0 names no wind instrument this app knows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, warns, err := importDoc(t, buildDoc(tt.trackExtra, tt.noteProps))
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if err := s.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			tr := s.Tracks[0]
			if tr.Wind != nil {
				t.Fatalf("Wind = %v, want no wind classification without conclusive evidence", tr.Wind)
			}
			if len(tr.Tuning) != len(score.StandardTuning) {
				t.Errorf("tuning has %d strings, want the standard fallback", len(tr.Tuning))
			}
			if len(s.Events()) != 0 {
				t.Errorf("events = %+v, want none (the notes skip)", s.Events())
			}
			if !hasWarning(warns, tt.reason) {
				t.Errorf("warnings = %v, want the aggregate reason %q", warns, tt.reason)
			}
			if n := countWarnings(warns, "skipped rather than imported as a guess"); n != 1 {
				t.Errorf("warnings = %v, want exactly one aggregate wind warning, got %d", warns, n)
			}
			if !hasWarning(warns, "no String/Fret properties; skipped") {
				t.Errorf("warnings = %v, want today's per-note skip kept", warns)
			}
			if !hasWarning(warns, "assuming standard EADGBE") {
				t.Errorf("warnings = %v, want today's tuning fallback kept", warns)
			}
			if hasWarning(warns, "is not supported") {
				t.Errorf("warnings = %v, pitch properties are recognized and must not be reported unknown", warns)
			}
		})
	}
}

// TestImportWindBandMixed: a guitar and a sax in one file keep their own
// families and their own bars — classification evidence from one track's
// notes must not leak into the other, and the wind track's bar lookup
// still runs on the original track index.
func TestImportWindBandMixed(t *testing.T) {
	doc := gpifDocTracks("0 1",
		trackXML(0, "G", "")+windTrackXML(1, "Sax", `<GeneralMidi><Program>64</Program></GeneralMidi>`),
		`<MasterBar><Time>4/4</Time><Bars>0 1</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`+
			`<Bar id="1"><Voices>1 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice><Voice id="1"><Beats>1</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`,
		noteXML(0, 0, 5, "")+
			windNoteXML(1, pitchPropXML("ConcertPitch", "A", "b", 4)+pitchPropXML("TransposedPitch", "B", "b", 4), ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if len(s.Tracks) != 2 {
		t.Fatalf("got %d tracks %v, want 2", len(s.Tracks), trackNames(s))
	}
	g, sax := s.Tracks[0], s.Tracks[1]
	if g.Wind != nil || len(g.Tuning) != 6 || g.Role != score.RoleUser {
		t.Errorf("guitar = wind %v tuning %v role %d, want a fretted RoleUser track", g.Wind, g.Tuning, g.Role)
	}
	if sax.Wind == nil || sax.Wind.Name != "soprano sax" || sax.Role != score.RoleBacking {
		t.Errorf("sax = wind %v role %d, want the soprano sax as RoleBacking", sax.Wind, sax.Role)
	}
	notes := g.Bars[0].Beats[0].Notes
	if len(notes) != 1 || notes[0].String != 6 || notes[0].Fret != 5 {
		t.Errorf("guitar notes = %+v, want its own string 6 fret 5", notes)
	}
	notes = sax.Bars[0].Beats[0].Notes
	if len(notes) != 1 || notes[0].String != 1 || notes[0].Fret != 12 {
		t.Errorf("sax notes = %+v, want its own lane note (fret 12 = Ab4)", notes)
	}
}
