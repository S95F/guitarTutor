// Package gpimport reads Guitar Pro 7/8 .gp files into the score model.
//
// A .gp file is a plain ZIP archive whose musical payload is a single XML
// document, Content/score.gpif; any other archive entries (audio,
// stylesheets, layout state) are ignored. The GPIF model is
// id-referential: the master track lists track ids, master bars list one
// bar id per track, bars list voice ids, voices list beat ids, and beats
// reference a rhythm and list note ids. Import resolves those references
// into the canonical tick model (PPQ 960): tempo automations become the
// TempoMap, master-bar time signatures the MeterMap, and notes keep their
// authored string/fret fingering — Note.Inferred stays false, which is
// the point of importing .gp over MIDI. GP numbers strings from 0 at the
// lowest-pitched string; the score model numbers from 1 at the highest,
// and the conversion happens here.
//
// The importer is deliberately permissive: unknown or unsupported
// constructs (grace notes, bends and other note properties, extra voices,
// extra staves) are skipped with a human-readable warning, and import
// only fails when the file is structurally unreadable — not a zip, no
// gpif entry, unparsable XML, no tracks or bars, or a time signature the
// tick grid cannot represent. Underfull bars are padded with a trailing
// rest and overfull bars truncated, with warnings, so the result always
// passes score.Validate.
//
// Clean-room note (docs/DECISIONS.md D3): the reference implementations
// for this format are MPL/LGPL licensed; nothing here is ported or
// paraphrased from their source. The importer is written from the
// publicly documented *structure* of the format and verified against
// fixtures this repository generates for itself (tools/gengp).
//
// Honesty note: because the test fixture is self-authored, the importer
// and its fixture embody one shared understanding of the format — files
// exported by Guitar Pro itself are the untested gap. Real .gp files
// that fail to import (or import wrongly) are wanted as bug reports; see
// testdata/README-gp.txt.
package gpimport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/S95F/guitarTutor/internal/score"
)

// DefaultProgram is the General MIDI program assigned to imported tracks:
// 25, steel-string acoustic guitar, matching the text format's default
// (docs/TEXTFORMAT.md). GPIF sound assignments are not imported yet.
const DefaultProgram = 25

// gpifEntry is the archive path of the musical payload.
const gpifEntry = "Content/score.gpif"

// maxGPIFBytes caps how far the score.gpif entry may decompress: real
// GPIF documents are a few megabytes at most, so 64 MiB is comfortably
// past any legitimate score while stopping zip bombs from ballooning a
// small archive into gigabytes of memory. The io.LimitReader in readGPIF
// is the authoritative bound — the zip header's size field is forgeable
// and only serves as a fast-path reject.
const maxGPIFBytes = 64 << 20

// noteValues maps GPIF <NoteValue> names to base durations in ticks.
var noteValues = map[string]int64{
	"Whole":   score.Whole,
	"Half":    score.Half,
	"Quarter": score.Quarter,
	"Eighth":  score.Eighth,
	"16th":    score.Sixteenth,
	"32nd":    score.ThirtySec,
	"64th":    score.ThirtySec / 2,
}

// tempoUnits maps the beat-unit code in a tempo automation's value ("120
// 2" = 120 BPM in unit-2 beats) to a factor converting the automation's
// BPM to quarter-note BPM: 1 eighth, 2 quarter, 3 dotted quarter, 4 half,
// 5 dotted half.
var tempoUnits = map[int]float64{1: 0.5, 2: 1, 3: 1.5, 4: 2, 5: 3}

// Import parses a Guitar Pro 7/8 .gp file into a Score, returning
// human-readable warnings for everything the import skipped or changed.
// The first track becomes the RoleUser track, later ones RoleBacking.
// The result always passes score.Validate.
func Import(data []byte) (*score.Score, []string, error) {
	doc, err := readGPIF(data)
	if err != nil {
		return nil, nil, err
	}
	im := &importer{doc: doc, unknownProps: map[string]bool{}}
	return im.run()
}

// ImportFile reads path and imports it via Import.
func ImportFile(path string) (*score.Score, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return Import(data)
}

// readGPIF opens the zip container and unmarshals its score.gpif entry.
func readGPIF(data []byte) (*gpif, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("reading .gp container: %w", err)
	}
	var entry *zip.File
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.EqualFold(name, gpifEntry) {
			entry = f
			break
		}
		// Fall back to a score.gpif anywhere in the archive.
		if entry == nil && strings.EqualFold(path.Base(name), "score.gpif") {
			entry = f
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("no %s entry in archive (not a Guitar Pro 7/8 .gp file?)", gpifEntry)
	}
	if entry.UncompressedSize64 > maxGPIFBytes {
		return nil, fmt.Errorf("%s claims %d bytes uncompressed, over the %d MiB limit (possible zip bomb)",
			entry.Name, entry.UncompressedSize64, maxGPIFBytes>>20)
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", entry.Name, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxGPIFBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", entry.Name, err)
	}
	if len(raw) > maxGPIFBytes {
		return nil, fmt.Errorf("%s decompresses past the %d MiB limit (possible zip bomb)", entry.Name, maxGPIFBytes>>20)
	}
	var doc gpif
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", entry.Name, err)
	}
	return &doc, nil
}

// The GPIF document model. Only the subset the importer understands is
// declared; encoding/xml silently skips everything else, which is the
// permissiveness the format's evolution requires.
type gpif struct {
	XMLName     xml.Name      `xml:"GPIF"`
	GPRevision  string        `xml:"GPRevision"`
	Score       gpScoreInfo   `xml:"Score"`
	MasterTrack gpMasterTrack `xml:"MasterTrack"`
	Tracks      []gpTrack     `xml:"Tracks>Track"`
	MasterBars  []gpMasterBar `xml:"MasterBars>MasterBar"`
	Bars        []gpBar       `xml:"Bars>Bar"`
	Voices      []gpVoice     `xml:"Voices>Voice"`
	Beats       []gpBeat      `xml:"Beats>Beat"`
	Notes       []gpNote      `xml:"Notes>Note"`
	Rhythms     []gpRhythm    `xml:"Rhythms>Rhythm"`
}

type gpScoreInfo struct {
	Title string `xml:"Title"`
}

type gpMasterTrack struct {
	Tracks      string         `xml:"Tracks"` // space-separated track ids
	Automations []gpAutomation `xml:"Automations>Automation"`
}

type gpAutomation struct {
	Type     string  `xml:"Type"`
	Bar      int     `xml:"Bar"`
	Position float64 `xml:"Position"` // fraction of the bar
	Value    string  `xml:"Value"`    // for Tempo: "BPM unitCode"
}

type gpTrack struct {
	ID         int          `xml:"id,attr"`
	Name       string       `xml:"Name"`
	Staves     []gpStaff    `xml:"Staves>Staff"`
	Properties []gpProperty `xml:"Properties>Property"` // older single-staff layout
}

type gpStaff struct {
	Properties []gpProperty `xml:"Properties>Property"`
}

// gpProperty is a name-keyed property; which child element carries the
// payload depends on the name ("Tuning" uses Pitches, "Fret"/"CapoFret"
// use Fret, "String" uses String).
type gpProperty struct {
	Name    string `xml:"name,attr"`
	Pitches string `xml:"Pitches"` // space-separated MIDI notes, low to high
	Fret    *int   `xml:"Fret"`
	String  *int   `xml:"String"` // 0 = lowest-pitched string
}

type gpMasterBar struct {
	Time string `xml:"Time"` // "num/den"
	Bars string `xml:"Bars"` // space-separated bar ids, one per track
}

type gpBar struct {
	ID     int    `xml:"id,attr"`
	Voices string `xml:"Voices"` // space-separated voice ids, -1 = empty
}

type gpVoice struct {
	ID    int    `xml:"id,attr"`
	Beats string `xml:"Beats"` // space-separated beat ids
}

type gpBeat struct {
	ID         int    `xml:"id,attr"`
	Rhythm     *gpRef `xml:"Rhythm"`
	Notes      string `xml:"Notes"`      // space-separated note ids; absent = rest
	GraceNotes string `xml:"GraceNotes"` // non-empty marks a grace beat
}

type gpRef struct {
	Ref int `xml:"ref,attr"`
}

type gpNote struct {
	ID         int          `xml:"id,attr"`
	Properties []gpProperty `xml:"Properties>Property"`
	Tie        *gpTie       `xml:"Tie"`
}

type gpTie struct {
	Origin      string `xml:"origin,attr"`
	Destination string `xml:"destination,attr"`
}

type gpRhythm struct {
	ID        int       `xml:"id,attr"`
	NoteValue string    `xml:"NoteValue"`
	Dot       *gpDot    `xml:"AugmentationDot"`
	Tuplet    *gpTuplet `xml:"PrimaryTuplet"`
}

type gpDot struct {
	Count int `xml:"count,attr"`
}

type gpTuplet struct {
	Num int `xml:"num,attr"`
	Den int `xml:"den,attr"`
}

// An importer holds one import's state: the document, the id indexes,
// the per-master-bar layout, and the warnings accumulated so far.
type importer struct {
	doc   *gpif
	warns []string

	tracks  map[int]*gpTrack
	bars    map[int]*gpBar
	voices  map[int]*gpVoice
	beats   map[int]*gpBeat
	notes   map[int]*gpNote
	rhythms map[int]*gpRhythm

	barStarts []int64 // per master bar
	barLens   []int64
	barNums   []int
	barDens   []int

	unknownProps map[string]bool // note property names seen and skipped
	graceSkipped int
}

// warnf records one human-readable warning.
func (im *importer) warnf(format string, args ...any) {
	im.warns = append(im.warns, fmt.Sprintf(format, args...))
}

// run drives the import of a parsed GPIF document.
func (im *importer) run() (*score.Score, []string, error) {
	im.index()
	order := im.trackOrder()
	if len(order) == 0 {
		return nil, im.warns, fmt.Errorf("no tracks in file")
	}
	if len(im.doc.MasterBars) == 0 {
		return nil, im.warns, fmt.Errorf("no bars in file")
	}
	s := &score.Score{Title: im.doc.Score.Title}
	meters, err := im.layoutBars()
	if err != nil {
		return nil, im.warns, err
	}
	s.Meters = meters
	s.Tempos = im.tempos()
	for ti, gt := range order {
		role := score.RoleBacking
		if ti == 0 {
			role = score.RoleUser
		}
		s.Tracks = append(s.Tracks, im.buildTrack(ti, gt, role))
	}
	im.flushDeferredWarnings()
	if err := s.Validate(); err != nil {
		return nil, im.warns, fmt.Errorf("imported score failed validation: %w", err)
	}
	return s, im.warns, nil
}

// index builds the id lookup tables. Duplicate ids keep the last
// occurrence, matching "later overrides earlier" elsewhere in the app.
func (im *importer) index() {
	im.tracks = map[int]*gpTrack{}
	for i := range im.doc.Tracks {
		im.tracks[im.doc.Tracks[i].ID] = &im.doc.Tracks[i]
	}
	im.bars = map[int]*gpBar{}
	for i := range im.doc.Bars {
		im.bars[im.doc.Bars[i].ID] = &im.doc.Bars[i]
	}
	im.voices = map[int]*gpVoice{}
	for i := range im.doc.Voices {
		im.voices[im.doc.Voices[i].ID] = &im.doc.Voices[i]
	}
	im.beats = map[int]*gpBeat{}
	for i := range im.doc.Beats {
		im.beats[im.doc.Beats[i].ID] = &im.doc.Beats[i]
	}
	im.notes = map[int]*gpNote{}
	for i := range im.doc.Notes {
		im.notes[im.doc.Notes[i].ID] = &im.doc.Notes[i]
	}
	im.rhythms = map[int]*gpRhythm{}
	for i := range im.doc.Rhythms {
		im.rhythms[im.doc.Rhythms[i].ID] = &im.doc.Rhythms[i]
	}
}

// trackOrder resolves the master track's track-id list, falling back to
// document order when the list is absent.
func (im *importer) trackOrder() []*gpTrack {
	var order []*gpTrack
	for _, id := range im.ids(im.doc.MasterTrack.Tracks, "master track <Tracks>") {
		gt := im.tracks[id]
		if gt == nil {
			im.warnf("master track references track %d, which does not exist; skipped", id)
			continue
		}
		order = append(order, gt)
	}
	if len(order) == 0 {
		for i := range im.doc.Tracks {
			order = append(order, &im.doc.Tracks[i])
		}
	}
	return order
}

// ids parses a space-separated id list, warning about unparsable tokens.
func (im *importer) ids(list, ctx string) []int {
	var out []int
	for _, f := range strings.Fields(list) {
		v, err := strconv.Atoi(f)
		if err != nil {
			im.warnf("%s: unparsable id %q; ignored", ctx, f)
			continue
		}
		out = append(out, v)
	}
	return out
}

// layoutBars walks the master bars, resolving each one's time signature
// (inherited from the previous bar when absent) into contiguous tick
// spans, and returns the resulting meter map.
func (im *importer) layoutBars() (score.MeterMap, error) {
	var meters score.MeterMap
	num, den := 4, 4
	tick := int64(0)
	for mi, mb := range im.doc.MasterBars {
		if t := strings.TrimSpace(mb.Time); t != "" {
			if n, d, ok := parseTime(t); ok {
				num, den = n, d
			} else {
				im.warnf("master bar %d: unparsable time signature %q; keeping %d/%d", mi+1, t, num, den)
			}
		} else if mi == 0 {
			im.warnf("master bar 1: no time signature; assuming 4/4")
		}
		if num <= 0 || den <= 0 || (4*score.PPQ)%int64(den) != 0 {
			return nil, fmt.Errorf("unsupported time signature %d/%d", num, den)
		}
		if len(meters) == 0 || meters[len(meters)-1].Num != num || meters[len(meters)-1].Den != den {
			meters = append(meters, score.Meter{Tick: tick, Num: num, Den: den})
		}
		l := int64(num) * (4 * score.PPQ / int64(den))
		im.barStarts = append(im.barStarts, tick)
		im.barLens = append(im.barLens, l)
		im.barNums = append(im.barNums, num)
		im.barDens = append(im.barDens, den)
		tick += l
	}
	return meters, nil
}

// parseTime parses "num/den".
func parseTime(s string) (num, den int, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0, false
	}
	n, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	d, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return n, d, true
}

// tempos converts the master track's Tempo automations into the tempo
// map. A tempo value is "BPM unitCode"; the position is a fraction of
// the automation's bar, clamped into [0,1] with a warning when it is
// NaN or out of range so a hostile position can never place a tempo
// before tick 0 or overflow the tick math. A BPM so large that it
// rounds to zero microseconds per quarter is skipped with a warning.
// A missing tick-0 tempo gets the 120 BPM default.
func (im *importer) tempos() score.TempoMap {
	var tempos score.TempoMap
	for _, a := range im.doc.MasterTrack.Automations {
		if !strings.EqualFold(strings.TrimSpace(a.Type), "Tempo") {
			continue
		}
		fields := strings.Fields(a.Value)
		if len(fields) == 0 {
			im.warnf("tempo automation at bar %d: empty value; skipped", a.Bar+1)
			continue
		}
		bpm, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || bpm <= 0 {
			im.warnf("tempo automation at bar %d: bad value %q; skipped", a.Bar+1, a.Value)
			continue
		}
		factor := 1.0
		if len(fields) > 1 {
			unit, err := strconv.Atoi(fields[1])
			if err != nil {
				im.warnf("tempo automation at bar %d: bad beat unit in %q; assuming quarter note", a.Bar+1, a.Value)
			} else if f, ok := tempoUnits[unit]; ok {
				factor = f
			} else {
				im.warnf("tempo automation at bar %d: unknown beat unit %d; assuming quarter note", a.Bar+1, unit)
			}
		}
		if a.Bar < 0 || a.Bar >= len(im.barStarts) {
			im.warnf("tempo automation references bar %d, outside the piece; skipped", a.Bar+1)
			continue
		}
		usq := score.USPerQuarter(bpm * factor)
		if usq <= 0 {
			im.warnf("tempo automation at bar %d: absurd tempo %q; skipped", a.Bar+1, a.Value)
			continue
		}
		pos := a.Position
		if math.IsNaN(pos) {
			im.warnf("tempo automation at bar %d: position is not a number; using the bar start", a.Bar+1)
			pos = 0
		} else if pos < 0 || pos > 1 {
			im.warnf("tempo automation at bar %d: position %v outside [0,1]; clamped", a.Bar+1, pos)
			pos = math.Min(math.Max(pos, 0), 1)
		}
		tick := im.barStarts[a.Bar] + int64(pos*float64(im.barLens[a.Bar])+0.5)
		tempos = append(tempos, score.Tempo{Tick: tick, USPerQuarter: usq})
	}
	// Every tick above is non-negative (positions are clamped), so after
	// this sort the tick-0 default insertion below keeps the map sorted.
	sort.SliceStable(tempos, func(i, j int) bool { return tempos[i].Tick < tempos[j].Tick })
	// Later automations at the same tick override earlier ones.
	var deduped score.TempoMap
	for i, t := range tempos {
		if i+1 < len(tempos) && tempos[i+1].Tick == t.Tick {
			continue
		}
		deduped = append(deduped, t)
	}
	if len(deduped) == 0 || deduped[0].Tick != 0 {
		if len(deduped) == 0 {
			im.warnf("no tempo automation; assuming 120 BPM")
		}
		deduped = append(score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}}, deduped...)
	}
	return deduped
}

// buildTrack converts one GPIF track: tuning and capo from its (first)
// staff's properties, then one score bar per master bar.
func (im *importer) buildTrack(ti int, gt *gpTrack, role score.TrackRole) *score.Track {
	tuning, capo := im.trackSetup(ti, gt)
	tr := &score.Track{
		Name:    gt.Name,
		Tuning:  tuning,
		Capo:    capo,
		Program: DefaultProgram,
		Role:    role,
	}
	for mi := range im.doc.MasterBars {
		bar := tr.AppendBar(im.barNums[mi], im.barDens[mi])
		im.fillBar(ti, mi, bar, len(tuning))
	}
	return tr
}

// trackSetup extracts a track's tuning (GPIF stores open-string pitches
// low to high; the score model wants highest first) and capo fret.
func (im *importer) trackSetup(ti int, gt *gpTrack) (score.Tuning, int) {
	if len(gt.Staves) > 1 {
		im.warnf("track %d (%s): %d staves; only the first is imported", ti+1, gt.Name, len(gt.Staves))
	}
	props := gt.Properties
	if len(gt.Staves) > 0 {
		props = append(append([]gpProperty{}, gt.Staves[0].Properties...), gt.Properties...)
	}
	var tuning score.Tuning
	capo := 0
	for _, p := range props {
		switch p.Name {
		case "Tuning":
			if tuning != nil || strings.TrimSpace(p.Pitches) == "" {
				continue
			}
			tn, err := parseTuning(p.Pitches)
			if err != nil {
				im.warnf("track %d (%s): bad tuning %q (%v); assuming standard", ti+1, gt.Name, p.Pitches, err)
				continue
			}
			tuning = tn
		case "CapoFret":
			if p.Fret != nil {
				capo = *p.Fret
			}
		}
	}
	if tuning == nil {
		im.warnf("track %d (%s): no tuning property; assuming standard EADGBE", ti+1, gt.Name)
		tuning = append(score.Tuning{}, score.StandardTuning...)
	}
	if capo < 0 {
		im.warnf("track %d (%s): negative capo fret %d; using 0", ti+1, gt.Name, capo)
		capo = 0
	}
	return tuning, capo
}

// parseTuning parses space-separated MIDI notes low-to-high and reverses
// them into the score convention (string 1 = highest-pitched).
func parseTuning(pitches string) (score.Tuning, error) {
	fields := strings.Fields(pitches)
	tn := make(score.Tuning, len(fields))
	for i, f := range fields {
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("pitch %q is not a MIDI note number", f)
		}
		if v < 0 || v > 127 {
			return nil, fmt.Errorf("pitch %d outside MIDI range", v)
		}
		tn[len(fields)-1-i] = v
	}
	return tn, nil
}

// A beatData is one resolved beat before bar-fill accounting.
type beatData struct {
	dur   int64
	notes []score.Note
}

// fillBar resolves a track's bar in master bar mi and lays its beats
// into the score bar, padding underfull bars with a trailing rest and
// truncating overfull ones, so the bar always exactly fills its meter.
func (im *importer) fillBar(ti, mi int, bar *score.Bar, nStrings int) {
	beats := im.barBeats(ti, mi, nStrings)
	barLen := bar.Len()
	var filled int64
	over := false
	for _, bd := range beats {
		if filled >= barLen {
			over = true
			break
		}
		dur := bd.dur
		if filled+dur > barLen {
			dur = barLen - filled
			over = true
		}
		bar.AddBeat(dur, bd.notes...)
		filled += dur
	}
	if over {
		im.warnf("track %d bar %d: beats overfill the %d/%d bar; truncated", ti+1, mi+1, bar.Num, bar.Den)
	}
	if filled < barLen {
		bar.AddBeat(barLen - filled)
		if len(beats) > 0 {
			im.warnf("track %d bar %d: beats fill %d of %d ticks; padded with a rest", ti+1, mi+1, filled, barLen)
		}
	}
}

// barBeats resolves master bar mi's bar for track ti down to beats:
// picks the first voice (warning when other voices hold beats), skips
// grace beats, and resolves each beat's rhythm and notes.
func (im *importer) barBeats(ti, mi, nStrings int) []beatData {
	mb := im.doc.MasterBars[mi]
	barIDs := im.ids(mb.Bars, fmt.Sprintf("master bar %d <Bars>", mi+1))
	if ti >= len(barIDs) {
		if len(barIDs) > 0 {
			im.warnf("master bar %d lists no bar for track %d; treated as empty", mi+1, ti+1)
		}
		return nil
	}
	barID := barIDs[ti]
	if barID < 0 {
		return nil
	}
	gb := im.bars[barID]
	if gb == nil {
		im.warnf("master bar %d: bar %d does not exist; treated as empty", mi+1, barID)
		return nil
	}

	voiceIDs := im.ids(gb.Voices, fmt.Sprintf("bar %d <Voices>", barID))
	first := -1
	extra := 0
	for _, vid := range voiceIDs {
		if vid < 0 {
			continue
		}
		if first < 0 {
			first = vid
			continue
		}
		if v := im.voices[vid]; v != nil && strings.TrimSpace(v.Beats) != "" {
			extra++
		}
	}
	if extra > 0 {
		im.warnf("track %d bar %d: %d additional voice(s) hold beats; only the first voice is imported", ti+1, mi+1, extra)
	}
	if first < 0 {
		return nil
	}
	voice := im.voices[first]
	if voice == nil {
		im.warnf("track %d bar %d: voice %d does not exist; treated as empty", ti+1, mi+1, first)
		return nil
	}

	var out []beatData
	for _, bid := range im.ids(voice.Beats, fmt.Sprintf("voice %d <Beats>", first)) {
		gbt := im.beats[bid]
		if gbt == nil {
			im.warnf("voice %d: beat %d does not exist; skipped", first, bid)
			continue
		}
		if strings.TrimSpace(gbt.GraceNotes) != "" {
			im.graceSkipped++
			continue
		}
		out = append(out, beatData{
			dur:   im.rhythmDur(gbt),
			notes: im.beatNotes(gbt, nStrings),
		})
	}
	return out
}

// rhythmDur resolves a beat's rhythm reference to a duration in ticks:
// base note value, augmentation dots, then the primary tuplet.
func (im *importer) rhythmDur(gbt *gpBeat) int64 {
	if gbt.Rhythm == nil {
		im.warnf("beat %d has no rhythm reference; assuming a quarter note", gbt.ID)
		return score.Quarter
	}
	rh := im.rhythms[gbt.Rhythm.Ref]
	if rh == nil {
		im.warnf("beat %d references rhythm %d, which does not exist; assuming a quarter note", gbt.ID, gbt.Rhythm.Ref)
		return score.Quarter
	}
	dur, ok := noteValues[rh.NoteValue]
	if !ok {
		im.warnf("rhythm %d: unknown note value %q; assuming a quarter note", rh.ID, rh.NoteValue)
		dur = score.Quarter
	}
	if rh.Dot != nil {
		// Real GP files never carry more than three augmentation dots;
		// clamp hostile counts so the attribute cannot drive the loop,
		// and stop once further dots would add nothing.
		count := rh.Dot.Count
		if count < 0 || count > 3 {
			im.warnf("rhythm %d: implausible augmentation dot count %d; treating as 3", rh.ID, count)
			count = 3
		}
		add := dur / 2
		for i := 0; i < count && add > 0; i++ {
			dur += add
			add /= 2
		}
	}
	if rh.Tuplet != nil {
		if rh.Tuplet.Num > 0 && rh.Tuplet.Den > 0 {
			dur = dur * int64(rh.Tuplet.Den) / int64(rh.Tuplet.Num)
		} else {
			im.warnf("rhythm %d: bad tuplet %d:%d; ignored", rh.ID, rh.Tuplet.Num, rh.Tuplet.Den)
		}
	}
	if dur <= 0 {
		im.warnf("rhythm %d resolves to a non-positive duration; assuming a quarter note", rh.ID)
		dur = score.Quarter
	}
	return dur
}

// beatNotes resolves a beat's note ids. A GP string number counts from 0
// at the lowest-pitched string; the score model counts from 1 at the
// highest, so string s becomes nStrings-s. Fingering is authored, so
// Inferred stays false. A note whose Tie destination flag is set is the
// continuation of the previous beat's note (score.Events merges them).
func (im *importer) beatNotes(gbt *gpBeat, nStrings int) []score.Note {
	var out []score.Note
	for _, nid := range im.ids(gbt.Notes, fmt.Sprintf("beat %d <Notes>", gbt.ID)) {
		gn := im.notes[nid]
		if gn == nil {
			im.warnf("beat %d references note %d, which does not exist; skipped", gbt.ID, nid)
			continue
		}
		var gpString, fret *int
		for _, p := range gn.Properties {
			switch p.Name {
			case "String":
				gpString = p.String
			case "Fret":
				fret = p.Fret
			default:
				im.unknownProps[p.Name] = true
			}
		}
		if gpString == nil || fret == nil {
			im.warnf("note %d: no String/Fret properties; skipped", nid)
			continue
		}
		str := nStrings - *gpString
		if str < 1 || str > nStrings {
			im.warnf("note %d: string %d outside the %d-string tuning; skipped", nid, *gpString, nStrings)
			continue
		}
		if *fret < 0 {
			im.warnf("note %d: negative fret %d; skipped", nid, *fret)
			continue
		}
		out = append(out, score.Note{
			String: str,
			Fret:   *fret,
			Tied:   gn.Tie != nil && strings.EqualFold(gn.Tie.Destination, "true"),
		})
	}
	return out
}

// flushDeferredWarnings emits the aggregated warnings collected during
// the walk: skipped grace beats and unsupported note property names.
func (im *importer) flushDeferredWarnings() {
	if im.graceSkipped > 0 {
		im.warnf("skipped %d grace-note beat(s): grace notes are not supported yet", im.graceSkipped)
	}
	var names []string
	for n := range im.unknownProps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		im.warnf("note property %q is not supported; ignored", n)
	}
}
