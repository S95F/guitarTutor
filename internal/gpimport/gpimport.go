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

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

const DefaultProgram = 25

const gpifEntry = "Content/score.gpif"

const maxGPIFBytes = 64 << 20

var noteValues = map[string]int64{
	"Whole":   score.Whole,
	"Half":    score.Half,
	"Quarter": score.Quarter,
	"Eighth":  score.Eighth,
	"16th":    score.Sixteenth,
	"32nd":    score.ThirtySec,
	"64th":    score.ThirtySec / 2,
}

var tempoUnits = map[int]float64{1: 0.5, 2: 1, 3: 1.5, 4: 2, 5: 3}

const maxTimeSigPart = 256

const maxTuningStrings = 25

const maxImportFret = 30

const maxCapoFret = 12

const maxTupletPart = 64

const gmPercussionChannel = 9

func Import(data []byte) (*score.Score, []string, error) {
	doc, err := readGPIF(data)
	if err != nil {
		return nil, nil, err
	}
	im := &importer{doc: doc, unknownProps: map[string]bool{}}
	return im.run()
}

func ImportFile(path string) (*score.Score, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return Import(data)
}

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

type gpif struct {
	XMLName     xml.Name      `xml:"GPIF"`
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
	Tracks      string         `xml:"Tracks"`
	Automations []gpAutomation `xml:"Automations>Automation"`
}

type gpAutomation struct {
	Type     string  `xml:"Type"`
	Bar      int     `xml:"Bar"`
	Position float64 `xml:"Position"`
	Value    string  `xml:"Value"`
}

type gpTrack struct {
	ID   int    `xml:"id,attr"`
	Name string `xml:"Name"`

	Instrument    gpInstrumentRef `xml:"Instrument"`
	InstrumentSet gpInstrumentSet `xml:"InstrumentSet"`
	GeneralMidi   gpGeneralMidi   `xml:"GeneralMidi"`
	Staves        []gpStaff       `xml:"Staves>Staff"`
	Properties    []gpProperty    `xml:"Properties>Property"`
}

type gpInstrumentRef struct {
	Ref string `xml:"ref,attr"`
}

type gpInstrumentSet struct {
	Name string `xml:"Name"`
	Type string `xml:"Type"`
}

type gpGeneralMidi struct {
	PrimaryChannel *int `xml:"PrimaryChannel"`
	Program        *int `xml:"Program"`
}

var drumKitNames = map[string]bool{
	"percussion": true,
	"drums":      true,
	"drumset":    true,
	"drum kit":   true,
	"drumkit":    true,
}

func (gt *gpTrack) percussion() (bool, string) {

	if c := gt.GeneralMidi.PrimaryChannel; c != nil && *c == gmPercussionChannel {
		return true, "MIDI channel 10"
	}

	if t := strings.ToLower(strings.TrimSpace(gt.InstrumentSet.Type)); t == "drumkit" {
		return true, fmt.Sprintf("instrument set %q", gt.InstrumentSet.Type)
	}
	if n := strings.ToLower(strings.TrimSpace(gt.InstrumentSet.Name)); drumKitNames[n] {
		return true, fmt.Sprintf("instrument set %q", gt.InstrumentSet.Name)
	}

	if r := strings.ToLower(strings.TrimSpace(gt.Instrument.Ref)); strings.HasPrefix(r, "drmkt") {
		return true, fmt.Sprintf("instrument %q", gt.Instrument.Ref)
	}
	return false, ""
}

type gpStaff struct {
	Properties []gpProperty `xml:"Properties>Property"`
}

type gpProperty struct {
	Name    string   `xml:"name,attr"`
	Pitches string   `xml:"Pitches"`
	Fret    *int     `xml:"Fret"`
	String  *int     `xml:"String"`
	Pitch   *gpPitch `xml:"Pitch"`
}

type gpPitch struct {
	Step       string `xml:"Step"`
	Accidental string `xml:"Accidental"`
	Octave     *int   `xml:"Octave"`
}

var stepSemitones = map[string]int{"C": 0, "D": 2, "E": 4, "F": 5, "G": 7, "A": 9, "B": 11}

var accidentalOffsets = map[string]int{"": 0, "#": 1, "b": -1, "x": 2, "bb": -2}

func pitchKey(p *gpPitch) (int, bool) {
	if p == nil || p.Octave == nil {
		return 0, false
	}
	sem, ok := stepSemitones[strings.TrimSpace(p.Step)]
	if !ok {
		return 0, false
	}
	acc, ok := accidentalOffsets[strings.TrimSpace(p.Accidental)]
	if !ok {
		return 0, false
	}
	oct := *p.Octave
	if oct < -2 || oct > 10 {
		return 0, false
	}
	return (oct+1)*12 + sem + acc, true
}

type gpMasterBar struct {
	Time string `xml:"Time"`
	Bars string `xml:"Bars"`
}

type gpBar struct {
	ID     int    `xml:"id,attr"`
	Voices string `xml:"Voices"`
}

type gpVoice struct {
	ID    int    `xml:"id,attr"`
	Beats string `xml:"Beats"`
}

type gpBeat struct {
	ID         int    `xml:"id,attr"`
	Rhythm     *gpRef `xml:"Rhythm"`
	Notes      string `xml:"Notes"`
	GraceNotes string `xml:"GraceNotes"`
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

type importer struct {
	doc   *gpif
	warns []string

	tracks  map[int]*gpTrack
	bars    map[int]*gpBar
	voices  map[int]*gpVoice
	beats   map[int]*gpBeat
	notes   map[int]*gpNote
	rhythms map[int]*gpRhythm

	barStarts []int64
	barLens   []int64
	barNums   []int
	barDens   []int

	unknownProps map[string]bool
	graceSkipped int
}

func (im *importer) warnf(format string, args ...any) {
	im.warns = append(im.warns, fmt.Sprintf(format, args...))
}

func (im *importer) run() (*score.Score, []string, error) {
	im.index()
	order := im.trackOrder()
	if len(order) == 0 {
		if len(im.doc.Tracks) > 0 {

			return nil, im.warns, fmt.Errorf("no importable tracks: every track in the file is percussion")
		}
		return nil, im.warns, fmt.Errorf("no tracks in file")
	}
	if len(im.doc.MasterBars) == 0 {
		return nil, im.warns, fmt.Errorf("no bars in file")
	}

	title, changed := textfmt.CleanLabel(im.doc.Score.Title)
	if changed {
		im.warnf("title %q holds text a saved .gtab cannot (a line break or \"//\"); imported as %q", im.doc.Score.Title, title)
	}
	s := &score.Score{Title: title}
	meters, err := im.layoutBars()
	if err != nil {
		return nil, im.warns, err
	}
	s.Meters = meters
	s.Tempos = im.tempos()
	for i, ref := range order {
		role := score.RoleBacking
		if i == 0 {
			role = score.RoleUser
		}

		s.Tracks = append(s.Tracks, im.buildTrack(ref.orig, ref.gt, role))
	}
	im.flushDeferredWarnings()
	if err := s.Validate(); err != nil {
		return nil, im.warns, fmt.Errorf("imported score failed validation: %w", err)
	}
	return s, im.warns, nil
}

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

type trackRef struct {
	gt   *gpTrack
	orig int
}

func (im *importer) trackOrder() []trackRef {
	var resolved []*gpTrack
	found := 0
	for _, id := range im.ids(im.doc.MasterTrack.Tracks, "master track <Tracks>") {
		gt := im.tracks[id]
		if gt == nil {
			im.warnf("master track references track %d, which does not exist; skipped", id)
		} else {
			found++
		}

		resolved = append(resolved, gt)
	}
	if found == 0 {
		resolved = resolved[:0]
		for i := range im.doc.Tracks {
			resolved = append(resolved, &im.doc.Tracks[i])
		}
	}
	var order []trackRef
	for i, gt := range resolved {
		if gt == nil {
			continue
		}
		if perc, why := gt.percussion(); perc {
			im.warnf("%s is a percussion part (%s); skipped, because drum notation has no string/fret spelling this importer can render",
				trackLabel(gt, i), why)
			continue
		}
		order = append(order, trackRef{gt: gt, orig: i})
	}
	return order
}

func trackLabel(gt *gpTrack, orig int) string {
	if strings.TrimSpace(gt.Name) == "" {
		return fmt.Sprintf("track %d", orig+1)
	}
	return fmt.Sprintf("track %d (%s)", orig+1, gt.Name)
}

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
		if num > maxTimeSigPart {
			return nil, fmt.Errorf("time signature %d/%d: numerator above the %d limit", num, den, maxTimeSigPart)
		}
		if den > maxTimeSigPart {
			return nil, fmt.Errorf("time signature %d/%d: denominator above the %d limit", num, den, maxTimeSigPart)
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

	sort.SliceStable(tempos, func(i, j int) bool { return tempos[i].Tick < tempos[j].Tick })

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

type trackConv struct {
	tuning score.Tuning
	capo   int
	wind   *score.WindInstrument

	chords     int
	mismatched int
}

func (im *importer) buildTrack(orig int, gt *gpTrack, role score.TrackRole) *score.Track {
	tc := &trackConv{wind: im.classifyWind(orig, gt)}

	name, changed := textfmt.CleanLabel(gt.Name)
	if changed {
		im.warnf("%s: the track name holds text a saved .gtab cannot (a line break or \"//\"); imported as %q", trackLabel(gt, orig), name)
	}
	tr := &score.Track{Name: name, Role: role}
	if tc.wind != nil {

		tr.Wind = tc.wind
		tr.Program = tc.wind.Program
		if p := gt.GeneralMidi.Program; p != nil {
			tr.Program = *p
		}
	} else {
		tc.tuning, tc.capo = im.trackSetup(orig, gt)
		tr.Tuning, tr.Capo = tc.tuning, tc.capo
		tr.Program = DefaultProgram
	}
	for mi := range im.doc.MasterBars {
		bar := tr.AppendBar(im.barNums[mi], im.barDens[mi])
		im.fillBar(orig, mi, bar, tc)
	}

	if tc.chords > 0 {
		im.warnf("%s: kept only the highest note of %d chord(s); a %s plays one note at a time",
			trackLabel(gt, orig), tc.chords, tc.wind.Name)
	}
	if tc.mismatched > 0 {
		im.warnf("%s: the written pitch on %d note(s) disagrees with the %s's transposition; the concert pitch wins",
			trackLabel(gt, orig), tc.mismatched, tc.wind.Name)
	}
	return tr
}

func hasTuningProperty(gt *gpTrack) bool {
	for _, p := range gt.Properties {
		if p.Name == "Tuning" {
			return true
		}
	}
	for _, st := range gt.Staves {
		for _, p := range st.Properties {
			if p.Name == "Tuning" {
				return true
			}
		}
	}
	return false
}

func quietIDs(list string) []int {
	var out []int
	for _, f := range strings.Fields(list) {
		if v, err := strconv.Atoi(f); err == nil {
			out = append(out, v)
		}
	}
	return out
}

type windEvidence struct {
	concert        int
	fretted        int
	minKey, maxKey int
	deltas         map[int]bool
}

func (im *importer) scanWind(orig int) windEvidence {
	ev := windEvidence{deltas: map[int]bool{}}
	for mi := range im.doc.MasterBars {
		barIDs := quietIDs(im.doc.MasterBars[mi].Bars)
		if orig >= len(barIDs) || barIDs[orig] < 0 {
			continue
		}
		gb := im.bars[barIDs[orig]]
		if gb == nil {
			continue
		}
		first := -1
		for _, vid := range quietIDs(gb.Voices) {
			if vid >= 0 {
				first = vid
				break
			}
		}
		voice := im.voices[first]
		if voice == nil {
			continue
		}
		for _, bid := range quietIDs(voice.Beats) {
			gbt := im.beats[bid]
			if gbt == nil || strings.TrimSpace(gbt.GraceNotes) != "" {
				continue
			}
			for _, nid := range quietIDs(gbt.Notes) {
				gn := im.notes[nid]
				if gn == nil {
					continue
				}
				var concert, transposed *gpPitch
				for _, p := range gn.Properties {
					switch p.Name {
					case "ConcertPitch":
						concert = p.Pitch
					case "TransposedPitch":
						transposed = p.Pitch
					case "String", "Fret":
						ev.fretted++
					}
				}
				key, ok := pitchKey(concert)
				if !ok {
					continue
				}
				if ev.concert == 0 || key < ev.minKey {
					ev.minKey = key
				}
				if ev.concert == 0 || key > ev.maxKey {
					ev.maxKey = key
				}
				ev.concert++
				if tk, ok := pitchKey(transposed); ok {
					ev.deltas[tk-key] = true
				}
			}
		}
	}
	return ev
}

func (im *importer) classifyWind(orig int, gt *gpTrack) *score.WindInstrument {
	if hasTuningProperty(gt) {
		return nil
	}
	ev := im.scanWind(orig)
	if ev.concert == 0 || ev.fretted > 0 {
		return nil
	}
	skip := func(reason string) *score.WindInstrument {
		im.warnf("%s: notes carry concert pitch instead of string/fret (a wind part), but %s; the notes are skipped rather than imported as a guess",
			trackLabel(gt, orig), reason)
		return nil
	}
	if p := gt.GeneralMidi.Program; p != nil {
		if w := score.WindByProgram(*p); w != nil {
			return w
		}
		return skip(fmt.Sprintf("the track's General MIDI program %d names no wind instrument this app knows", *p))
	}
	if len(ev.deltas) == 0 {
		return skip("the track declares no General MIDI program and no note carries a transposed pitch to reveal the transposition")
	}
	if len(ev.deltas) > 1 {
		return skip("the track declares no General MIDI program and the notes' written-minus-concert transposition is inconsistent")
	}
	var delta int
	for d := range ev.deltas {
		delta = d
	}
	var matches []*score.WindInstrument
	for i := range score.WindInstruments {
		w := &score.WindInstruments[i]
		if w.Transpose == delta && ev.minKey >= w.LowSounding && ev.maxKey <= 127-w.Transpose {
			matches = append(matches, w)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0]
	case 0:
		return skip(fmt.Sprintf("no wind instrument this app knows matches the notes' transposition (%+d semitones written) and range", delta))
	default:
		names := make([]string, len(matches))
		for i, w := range matches {
			names[i] = w.Name
		}
		return skip(fmt.Sprintf("the pitch evidence fits more than one wind instrument (%s)", strings.Join(names, ", ")))
	}
}

func (im *importer) trackSetup(orig int, gt *gpTrack) (score.Tuning, int) {
	if len(gt.Staves) > 1 {
		im.warnf("track %d (%s): %d staves; only the first is imported", orig+1, gt.Name, len(gt.Staves))
	}
	props := gt.Properties
	if len(gt.Staves) > 0 {
		props = append(append([]gpProperty{}, gt.Staves[0].Properties...), gt.Properties...)
	}
	var tuning score.Tuning
	badTuning := false
	capo := 0
	for _, p := range props {
		switch p.Name {
		case "Tuning":
			if tuning != nil || strings.TrimSpace(p.Pitches) == "" {
				continue
			}
			tn, err := parseTuning(p.Pitches)
			if err != nil {
				im.warnf("track %d (%s): bad tuning %q (%v); assuming standard", orig+1, gt.Name, p.Pitches, err)
				badTuning = true
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

		if !badTuning {
			im.warnf("track %d (%s): no tuning property; assuming standard EADGBE", orig+1, gt.Name)
		}
		tuning = append(score.Tuning{}, score.StandardTuning...)
	}
	if capo < 0 || capo > maxCapoFret {

		im.warnf("track %d (%s): capo fret %d outside 0-%d; using no capo", orig+1, gt.Name, capo, maxCapoFret)
		capo = 0
	}
	return tuning, capo
}

func parseTuning(pitches string) (score.Tuning, error) {
	fields := strings.Fields(pitches)
	if len(fields) > maxTuningStrings {
		return nil, fmt.Errorf("%d strings, more than the %d-string limit", len(fields), maxTuningStrings)
	}
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

type beatData struct {
	dur   int64
	notes []score.Note
}

func (im *importer) fillBar(orig, mi int, bar *score.Bar, tc *trackConv) {
	beats := im.barBeats(orig, mi, tc)
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
		im.warnf("track %d bar %d: beats overfill the %d/%d bar; truncated", orig+1, mi+1, bar.Num, bar.Den)
	}
	if filled < barLen {
		bar.AddBeat(barLen - filled)
		if len(beats) > 0 {
			im.warnf("track %d bar %d: beats fill %d of %d ticks; padded with a rest", orig+1, mi+1, filled, barLen)
		}
	}
}

func (im *importer) barBeats(orig, mi int, tc *trackConv) []beatData {
	mb := im.doc.MasterBars[mi]
	barIDs := im.ids(mb.Bars, fmt.Sprintf("master bar %d <Bars>", mi+1))
	if orig >= len(barIDs) {
		if len(barIDs) > 0 {
			im.warnf("master bar %d lists no bar for track %d; treated as empty", mi+1, orig+1)
		}
		return nil
	}
	barID := barIDs[orig]
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
		im.warnf("track %d bar %d: %d additional voice(s) hold beats; only the first voice is imported", orig+1, mi+1, extra)
	}
	if first < 0 {
		return nil
	}
	voice := im.voices[first]
	if voice == nil {
		im.warnf("track %d bar %d: voice %d does not exist; treated as empty", orig+1, mi+1, first)
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
			notes: im.beatNotes(orig, mi, gbt, tc),
		})
	}
	return out
}

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
		num, den := rh.Tuplet.Num, rh.Tuplet.Den
		switch {
		case num <= 0 || den <= 0:
			im.warnf("rhythm %d: bad tuplet %d:%d; ignored", rh.ID, num, den)
		case num > maxTupletPart || den > maxTupletPart:

			im.warnf("rhythm %d: implausible tuplet %d:%d, past the %d limit; ignored", rh.ID, num, den, maxTupletPart)
		default:
			dur = dur * int64(den) / int64(num)
		}
	}
	if dur <= 0 {
		im.warnf("rhythm %d resolves to a non-positive duration; assuming a quarter note", rh.ID)
		dur = score.Quarter
	}
	return dur
}

func (im *importer) beatNotes(orig, mi int, gbt *gpBeat, tc *trackConv) []score.Note {
	if tc.wind != nil {
		return im.windBeatNotes(orig, mi, gbt, tc)
	}
	nStrings := len(tc.tuning)
	var out []score.Note

	seen := map[int]bool{}
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
			case "ConcertPitch", "TransposedPitch":

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
		if *fret < 0 || *fret > maxImportFret {
			im.warnf("track %d bar %d: string %d fret %d outside the 0-%d fret range; note skipped",
				orig+1, mi+1, str, *fret, maxImportFret)
			continue
		}

		if k := tc.tuning[str-1] + tc.capo + *fret; k < 0 || k > 127 {
			im.warnf("track %d bar %d: string %d fret %d sounds MIDI key %d, outside 0-127; note skipped",
				orig+1, mi+1, str, *fret, k)
			continue
		}
		if seen[str] {
			im.warnf("track %d bar %d: beat %d sounds string %d twice; the duplicate note is skipped",
				orig+1, mi+1, gbt.ID, str)
			continue
		}
		seen[str] = true
		out = append(out, score.Note{
			String: str,
			Fret:   *fret,
			Tied:   gn.Tie != nil && strings.EqualFold(gn.Tie.Destination, "true"),
		})
	}
	return out
}

func (im *importer) windBeatNotes(orig, mi int, gbt *gpBeat, tc *trackConv) []score.Note {
	w := tc.wind
	var best *score.Note
	bestKey, kept := 0, 0
	for _, nid := range im.ids(gbt.Notes, fmt.Sprintf("beat %d <Notes>", gbt.ID)) {
		gn := im.notes[nid]
		if gn == nil {
			im.warnf("beat %d references note %d, which does not exist; skipped", gbt.ID, nid)
			continue
		}
		var concert, transposed *gpPitch
		for _, p := range gn.Properties {
			switch p.Name {
			case "ConcertPitch":
				concert = p.Pitch
			case "TransposedPitch":
				transposed = p.Pitch
			case "String", "Fret":

			default:
				im.unknownProps[p.Name] = true
			}
		}
		if concert == nil {
			im.warnf("note %d: no ConcertPitch property; skipped", nid)
			continue
		}
		key, ok := pitchKey(concert)
		if !ok {
			im.warnf("note %d: unparsable ConcertPitch; skipped", nid)
			continue
		}
		if transposed != nil {
			if tk, ok := pitchKey(transposed); ok && tk-key != w.Transpose {
				tc.mismatched++
			}
		}
		if key < w.LowSounding {
			im.warnf("track %d bar %d: dropped note (key %d): below the %s's lowest note (key %d)",
				orig+1, mi+1, key, w.Name, w.LowSounding)
			continue
		}
		if key > 127-w.Transpose {
			im.warnf("track %d bar %d: dropped note (key %d): its written pitch on a %s is past MIDI 127",
				orig+1, mi+1, key, w.Name)
			continue
		}
		kept++
		if best == nil || key > bestKey {
			n := w.NoteFor(key)
			n.Tied = gn.Tie != nil && strings.EqualFold(gn.Tie.Destination, "true")
			best, bestKey = &n, key
		}
	}
	if best == nil {
		return nil
	}
	if kept > 1 {
		tc.chords++
	}
	return []score.Note{*best}
}

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
