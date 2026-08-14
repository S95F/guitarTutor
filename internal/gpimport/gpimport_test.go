package gpimport

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	"github.com/S95F/guitarTutor/internal/score"
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
	return `<?xml version="1.0" encoding="UTF-8"?>
<GPIF>
  <GPRevision>7.0.0</GPRevision>
  <Score><Title>Test</Title></Score>
  <MasterTrack>
    <Tracks>0</Tracks>
    <Automations>
      <Automation><Type>Tempo</Type><Bar>0</Bar><Position>0</Position><Value>120 2</Value></Automation>
    </Automations>
  </MasterTrack>
  <Tracks>
    <Track id="0">
      <Name>G</Name>
      <Staves><Staff><Properties>
        <Property name="Tuning"><Pitches>40 45 50 55 59 64</Pitches></Property>
        <Property name="CapoFret"><Fret>0</Fret></Property>
      </Properties></Staff></Staves>
    </Track>
  </Tracks>
  <MasterBars>` + masterBars + `</MasterBars>
  <Bars>` + bars + `</Bars>
  <Voices>` + voices + `</Voices>
  <Beats>` + beats + `</Beats>
  <Notes>` + notes + `</Notes>
  <Rhythms>` + rhythms + `</Rhythms>
</GPIF>`
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
