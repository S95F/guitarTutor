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

var canonical = []struct {
	start, end int64
	key        int
	str, fret  int
}{

	{0, 480, 40, 6, 0}, {480, 960, 43, 6, 3}, {960, 1440, 50, 5, 5}, {1440, 1920, 40, 6, 0},
	{1920, 2400, 43, 6, 3}, {2400, 2880, 50, 5, 5}, {2880, 3360, 43, 6, 3}, {3360, 3840, 40, 6, 0},

	{3840, 4800, 47, 5, 2}, {4800, 5760, 45, 5, 0}, {5760, 7680, 47, 5, 2},

	{7680, 9600, 40, 6, 0}, {7680, 9600, 47, 5, 2}, {7680, 9600, 52, 4, 2},
	{10560, 11520, 43, 6, 3},

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

func gpifDoc(masterBars, bars, voices, beats, notes, rhythms string) string {
	return gpifDocTracks("0", trackXML(0, "G", ""), masterBars, bars, voices, beats, notes, rhythms)
}

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

func trackXML(id int, name, extra string) string {
	return `<Track id="` + itoa(id) + `">
      <Name>` + name + `</Name>` + extra + `
      <Staves><Staff><Properties>
        <Property name="Tuning"><Pitches>40 45 50 55 59 64</Pitches></Property>
        <Property name="CapoFret"><Fret>0</Fret></Property>
      </Properties></Staff></Staves>
    </Track>`
}

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

func TestPermissive(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><SomeNewMasterBarThing x="1"/><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Clef>G2</Clef><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Arpeggio>Up</Arpeggio><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, `<InstrumentArticulation>0</InstrumentArticulation>`),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)

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

func TestBarPadAndTruncate(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`+
			`<MasterBar><Time>4/4</Time><Bars>1</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`+
			`<Bar id="1"><Voices>1 -1 -1 -1</Voices></Bar>`,

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
		{0, 960, 40},
		{3840, 5760, 43},
		{5760, 3840 + 3840, 45},
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

func TestZipBombRejected(t *testing.T) {

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

	start := time.Now()
	_, _, err = Import(bombGP(t, comp.Bytes(), crc.Sum32(), bombSize))
	if err == nil || !strings.Contains(err.Error(), "zip bomb") {
		t.Errorf("honest 65 MiB bomb: err = %v, want a possible-zip-bomb error", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("honest bomb took %v to reject, want a fast failure", elapsed)
	}

	_, _, err = Import(bombGP(t, comp.Bytes(), crc.Sum32(), 1024))
	if err == nil {
		t.Error("forged-header bomb: import succeeded, want an error")
	}
}

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

func TestCapoAndTuning(t *testing.T) {
	doc := gpifDoc(
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
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
	if len(evs) != 1 || evs[0].Key != 40 {
		t.Fatalf("events = %+v, want one note at key 40 (drop D + capo 2)", evs)
	}
}

func percNoteXML(id, element int) string {
	return `<Note id="` + itoa(id) + `"><Properties>` +
		`<Property name="Element"><Element>` + itoa(element) + `</Element></Property>` +
		`<Property name="Variation"><Variation>0</Variation></Property>` +
		`</Properties></Note>`
}

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

func trackNames(s *score.Score) []string {
	var names []string
	for _, tr := range s.Tracks {
		names = append(names, tr.Name)
	}
	return names
}

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

	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 45 || evs[0].String != 6 || evs[0].Fret != 5 {
		t.Fatalf("events = %+v, want the guitar's single whole note (key 45, string 6 fret 5)", evs)
	}
}

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

func TestFilteredTrackKeepsItsOwnBars(t *testing.T) {

	s, warns, err := importDoc(t, threeTrackBarsDoc("99", ""))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !hasWarning(warns, "does not exist") {
		t.Errorf("warnings = %v, want one about the missing track", warns)
	}
	assertOwnBars(t, s)
}

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

func countWarnings(warns []string, substr string) int {
	n := 0
	for _, w := range warns {
		if strings.Contains(w, substr) {
			n++
		}
	}
	return n
}

func pitchPropXML(name, step, accidental string, octave int) string {
	return `<Property name="` + name + `"><Pitch><Step>` + step + `</Step>` +
		`<Accidental>` + accidental + `</Accidental>` +
		`<Octave>` + itoa(octave) + `</Octave></Pitch></Property>`
}

func windNoteXML(id int, props, extra string) string {
	return `<Note id="` + itoa(id) + `"><Properties>` + props + `</Properties>` + extra + `</Note>`
}

func windTrackXML(id int, name, extra string) string {
	return `<Track id="` + itoa(id) + `"><Name>` + name + `</Name>` + extra +
		`<Staves><Staff><Properties></Properties></Staff></Staves></Track>`
}

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
		{3840, 7680, 68},
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

func TestImportWindRangeDrops(t *testing.T) {
	doc := gpifDocTracks("0",
		windTrackXML(0, "Sax", `<GeneralMidi><Program>64</Program></GeneralMidi>`),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0 1 2</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`+
			`<Beat id="1"><Rhythm ref="0" /><Notes>1</Notes></Beat>`+
			`<Beat id="2"><Rhythm ref="1" /><Notes>2</Notes></Beat>`,
		windNoteXML(0, pitchPropXML("ConcertPitch", "D", "", 3), "")+
			windNoteXML(1, pitchPropXML("ConcertPitch", "G", "", 9), "")+
			windNoteXML(2, pitchPropXML("ConcertPitch", "A", "b", 4), ""),
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

func TestImportWindTransposeMismatchWarns(t *testing.T) {
	doc := gpifDocTracks("0",
		windTrackXML(0, "Sax", `<GeneralMidi><Program>64</Program></GeneralMidi>`),
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0 -1 -1 -1</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,

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

func TestBadTuningWarnsOnce(t *testing.T) {
	doc := gpifDocTracks("0",
		`<Track id="0"><Name>G</Name>
      <Staves><Staff><Properties>
        <Property name="Tuning"><Pitches>40 45 bogus 55 59 64</Pitches></Property>
      </Properties></Staff></Staves>
    </Track>`,
		`<MasterBar><Time>4/4</Time><Bars>0</Bars></MasterBar>`,
		`<Bar id="0"><Voices>0</Voices></Bar>`,
		`<Voice id="0"><Beats>0</Beats></Voice>`,
		`<Beat id="0"><Rhythm ref="0" /><Notes>0</Notes></Beat>`,
		noteXML(0, 0, 0, ""),
		`<Rhythm id="0"><NoteValue>Whole</NoteValue></Rhythm>`,
	)
	s, warns, err := importDoc(t, doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if countWarnings(warns, "bad tuning") != 1 {
		t.Errorf("warnings = %v, want exactly one bad-tuning warning", warns)
	}
	if hasWarning(warns, "no tuning property") {
		t.Errorf("warnings = %v; the track HAS a tuning property (a bad one), so the no-tuning warning lies", warns)
	}

	tr := s.Tracks[0]
	if !tr.Tuning.Equal(score.StandardTuning) {
		t.Errorf("Tuning = %v, want standard", tr.Tuning)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 40 || evs[0].String != 6 {
		t.Fatalf("events = %+v, want one low-E note on string 6", evs)
	}
}
