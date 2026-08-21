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
// Percussion tracks are the one whole-track omission: a drum kit has no
// string/fret spelling, so such a track is dropped with one warning
// naming it rather than imported as a bar-long row of rests. Import fails
// only if that leaves nothing behind.
//
// GP7+ spells notes on a non-fretted track with pitch properties —
// ConcertPitch, the sounding pitch, and TransposedPitch, the written one
// the player reads — instead of String/Fret. Such a track imports as a
// monophonic wind part (score.WindInstrument) only on explicit
// structural evidence, because a fretted track misread as wind would
// destroy its tab, which is worse than importing nothing: the track must
// carry no Tuning property anywhere, no note may carry a String or Fret
// property, and the registry instrument must resolve from the track's
// GeneralMidi Program (score.WindByProgram) or, when no program is
// declared, from a consistent written-minus-concert transposition plus a
// note range that together fit exactly one registry instrument. Anything
// less — a program the registry does not know as a wind, a missing or
// inconsistent transposition, zero or several matching instruments —
// keeps the fretted fallback: the notes are skipped with warnings plus
// one aggregate warning naming the missing evidence, never a guessed
// instrument. The model stores the sounding pitch; TransposedPitch is
// classification evidence and a cross-check only (a disagreement with
// the chosen instrument's transposition warns once per track, and the
// concert pitch wins).
//
// Clean-room note (docs/DECISIONS.md D3): the reference implementations
// for this format are MPL/LGPL licensed; nothing here is ported or
// paraphrased from their source. The importer is written from the
// publicly documented *structure* of the format and verified against
// fixtures this repository generates for itself (tools/gengp).
//
// Honesty note: because the test fixture is self-authored, the importer
// and its fixture embody one shared understanding of the format — files
// exported by Guitar Pro itself are the untested gap. Wind import in
// particular is clean-room from the public gpif documentation and has
// never been run against a real Guitar Pro wind export; the warning
// trail remains the evidence channel. Real .gp files that fail to import
// (or import wrongly) are wanted as bug reports; see
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

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// DefaultProgram is the General MIDI program assigned to imported
// fretted tracks: 25, steel-string acoustic guitar, matching the text
// format's default (docs/TEXTFORMAT.md). A track's <GeneralMidi><Program>
// is parsed, but only wind tracks consume it — fretted tracks keep the
// guitar default until sound assignments are imported properly.
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

// maxTimeSigPart bounds a master bar's time-signature numerator and
// denominator. Without it a numerator that is a multiple of 2^56 wraps
// int64(num)*3840 to exactly zero, making every bar zero-length while the
// score still passes Validate. 256 is far past any real signature and
// keeps the bar-length multiplication overflow-free.
const maxTimeSigPart = 256

// maxTuningStrings caps how many strings a Tuning property may declare.
// Real fretted instruments top out around 15-18 strings (a Warr guitar
// has 15, a theorbo ~14 courses), so 25 clears everything real while
// stopping a hostile file from installing a tuning that would pass
// Validate and then choke tab rendering. The cap matches mxlimport's.
const maxTuningStrings = 25

// maxImportFret is the highest fret a note may claim: 30 sits comfortably
// above any real 24-fret neck, and bounding it here keeps one absurd fret
// from pushing a pitch past MIDI 127 and failing the whole import in the
// final Validate.
const maxImportFret = 30

// maxCapoFret is the highest capo fret accepted; real capos live in the
// first handful of frets, so 12 is already generous.
const maxCapoFret = 12

// maxTupletPart bounds a PrimaryTuplet's num and den. Real tuplets never
// reach 13:8, and without a bound dur*int64(den) overflows int64 and
// wraps to an arbitrary duration that fillBar then quietly truncates to
// the bar — a wrong note with nothing said about it. 64 clears every real
// tuplet and keeps the multiplication exact.
const maxTupletPart = 64

// gmPercussionChannel is the wire number of General MIDI channel 10, the
// percussion channel. A track assigned to it plays a drum kit, where the
// key selects an instrument rather than a pitch; internal/midiimport
// applies the same rule to Standard MIDI Files.
const gmPercussionChannel = 9

// Import parses a Guitar Pro 7/8 .gp file into a Score, returning
// human-readable warnings for everything the import skipped or changed.
// Percussion tracks are dropped with a warning (see trackOrder); of what
// remains the first track becomes the RoleUser track, later ones
// RoleBacking. The result always passes score.Validate.
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
	ID   int    `xml:"id,attr"`
	Name string `xml:"Name"`
	// The three places a GPIF file can mark a track as a drum kit; see
	// gpTrack.percussion.
	Instrument    gpInstrumentRef `xml:"Instrument"`
	InstrumentSet gpInstrumentSet `xml:"InstrumentSet"`
	GeneralMidi   gpGeneralMidi   `xml:"GeneralMidi"`
	Staves        []gpStaff       `xml:"Staves>Staff"`
	Properties    []gpProperty    `xml:"Properties>Property"` // older single-staff layout
}

// gpInstrumentRef is the <Instrument ref="..."/> pointer to the track's
// instrument definition; drum kits use a ref naming the kit.
type gpInstrumentRef struct {
	Ref string `xml:"ref,attr"`
}

// gpInstrumentSet describes a track's instrument family; percussion
// tracks declare a drum-kit type.
type gpInstrumentSet struct {
	Name string `xml:"Name"`
	Type string `xml:"Type"`
}

// gpGeneralMidi carries a track's General MIDI assignment. Both fields
// are pointers so an absent element is distinguishable from 0 — channel 0
// is a real channel, and program 0 (piano) is a real program that must
// not be confused with "the file said nothing".
type gpGeneralMidi struct {
	PrimaryChannel *int `xml:"PrimaryChannel"`
	Program        *int `xml:"Program"` // 0-based General MIDI program
}

// drumKitNames are the InstrumentSet names that mean a kit, matched
// WHOLE. Substring matching was the first attempt and it was wrong in the
// expensive direction: General MIDI is full of pitched instruments whose
// names contain "drum" or "perc" — Steel Drums (115), Percussive Organ
// (18), Melodic Tom, vibraphone refs like "perc-vibraphone" — and GPIF's
// own family for those is literally "pitchedPercussion". Every one of
// them was being deleted from the score, silently taking the part the
// player opened the file for. A kit announces itself exactly; a pitched
// instrument merely mentions the word.
var drumKitNames = map[string]bool{
	"percussion": true,
	"drums":      true,
	"drumset":    true,
	"drum kit":   true,
	"drumkit":    true,
}

// percussion reports whether a track is a drum kit, and which marker said
// so (for the warning).
//
// GPIF records this in more than one place depending on which Guitar Pro
// version wrote the file, so every marker counts — but each is matched
// exactly. The two errors are not symmetric: an undetected drum track
// imports as rests, which is visible, warned about, and merely useless;
// a pitched track misread as a kit vanishes from the score with a warning
// that says it was drums. The second is worse, so precision wins.
func (gt *gpTrack) percussion() (bool, string) {
	// Channel 10 is the General MIDI percussion channel and is
	// unambiguous.
	if c := gt.GeneralMidi.PrimaryChannel; c != nil && *c == gmPercussionChannel {
		return true, "MIDI channel 10"
	}
	// GPIF's own instrument family. "pitchedPercussion" — vibraphone,
	// marimba, steel drums — is a DIFFERENT value and renders through the
	// ordinary pitched path like any other melodic part.
	if t := strings.ToLower(strings.TrimSpace(gt.InstrumentSet.Type)); t == "drumkit" {
		return true, fmt.Sprintf("instrument set %q", gt.InstrumentSet.Type)
	}
	if n := strings.ToLower(strings.TrimSpace(gt.InstrumentSet.Name)); drumKitNames[n] {
		return true, fmt.Sprintf("instrument set %q", gt.InstrumentSet.Name)
	}
	// "drmkt" is the instrument ref Guitar Pro gives a kit.
	if r := strings.ToLower(strings.TrimSpace(gt.Instrument.Ref)); strings.HasPrefix(r, "drmkt") {
		return true, fmt.Sprintf("instrument %q", gt.Instrument.Ref)
	}
	return false, ""
}

type gpStaff struct {
	Properties []gpProperty `xml:"Properties>Property"`
}

// gpProperty is a name-keyed property; which child element carries the
// payload depends on the name ("Tuning" uses Pitches, "Fret"/"CapoFret"
// use Fret, "String" uses String, "ConcertPitch"/"TransposedPitch" use
// Pitch).
type gpProperty struct {
	Name    string   `xml:"name,attr"`
	Pitches string   `xml:"Pitches"` // space-separated MIDI notes, low to high
	Fret    *int     `xml:"Fret"`
	String  *int     `xml:"String"` // 0 = lowest-pitched string
	Pitch   *gpPitch `xml:"Pitch"`
}

// gpPitch is the spelled pitch a ConcertPitch or TransposedPitch
// property carries: a note letter, an accidental, and a scientific
// octave (C4 = MIDI 60). Octave is a pointer so a property missing its
// octave reads as unparsable rather than as octave 0.
type gpPitch struct {
	Step       string `xml:"Step"`
	Accidental string `xml:"Accidental"`
	Octave     *int   `xml:"Octave"`
}

// stepSemitones maps a pitch step letter to its semitone offset within
// the octave.
var stepSemitones = map[string]int{"C": 0, "D": 2, "E": 4, "F": 5, "G": 7, "A": 9, "B": 11}

// accidentalOffsets maps the gpif accidental spellings to semitone
// offsets: "x" is a double sharp, "bb" a double flat.
var accidentalOffsets = map[string]int{"": 0, "#": 1, "b": -1, "x": 2, "bb": -2}

// pitchKey converts a spelled pitch to its MIDI key. The octave bound
// rejects hostile values before the multiplication can matter; it is
// deliberately one octave wider than MIDI on each side so a legally
// spelled note just past the keyboard (Cb-1, B#9) still converts and is
// then dropped by the range checks with a warning that names its key,
// instead of vanishing as "unparsable".
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
		if len(im.doc.Tracks) > 0 {
			// The warnings name each dropped track; say plainly why
			// there is nothing left rather than claiming the file has
			// no tracks at all.
			return nil, im.warns, fmt.Errorf("no importable tracks: every track in the file is percussion")
		}
		return nil, im.warns, fmt.Errorf("no tracks in file")
	}
	if len(im.doc.MasterBars) == 0 {
		return nil, im.warns, fmt.Errorf("no bars in file")
	}
	// A title holding a line break or the "//" comment marker would import
	// a piece the .gtab writer refuses to save; clean it like every other
	// unrepresentable detail — degrade, and say so.
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
		// ref.orig, never i: bars are indexed by the original order.
		s.Tracks = append(s.Tracks, im.buildTrack(ref.orig, ref.gt, role))
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

// A trackRef is one track the import keeps, paired with the position it
// occupied in the document's resolved track order.
//
// The two indexes diverge the moment any track is filtered out, and they
// must never be confused: <MasterBar><Bars> lists one bar id per track in
// the ORIGINAL order, so looking a bar up by the kept-slice index hands a
// track its neighbour's music — the user would be shown, and scored
// against, another instrument's part.
type trackRef struct {
	gt   *gpTrack
	orig int
}

// trackOrder resolves the master track's track-id list into the tracks to
// import, falling back to document order when that list resolves to
// nothing, and dropping percussion tracks along the way.
//
// Skipped tracks still occupy their slot in the resolved order, because
// that order is what <MasterBar><Bars> is indexed by; each kept track
// therefore carries its original position rather than relying on its
// position in the returned slice.
//
// Percussion is dropped rather than imported as silence: a drum part has
// no string/fret spelling at all, so the pitched path turns every hit
// into a rest — and an all-rest track in the first slot would become the
// user's practice part, teaching an empty song with no explanation.
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
		// nil keeps the slot so later tracks keep their original index.
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

// trackLabel names a track in a warning by its 1-based position in the
// document's track order, plus its name when the file gave it one.
func trackLabel(gt *gpTrack, orig int) string {
	if strings.TrimSpace(gt.Name) == "" {
		return fmt.Sprintf("track %d", orig+1)
	}
	return fmt.Sprintf("track %d (%s)", orig+1, gt.Name)
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

// A trackConv is one kept track's conversion context: which family the
// track resolved to — the wind instrument, or the fretted tuning and
// capo — plus the per-track counters the wind path reports as one
// aggregate warning each instead of note by note.
type trackConv struct {
	tuning score.Tuning
	capo   int
	wind   *score.WindInstrument

	chords     int // beats collapsed to their highest note
	mismatched int // notes whose written pitch disagrees with wind.Transpose
}

// buildTrack converts one GPIF track: wind classification first, then —
// for the fretted majority — tuning and capo from its (first) staff's
// properties, then one score bar per master bar.
//
// orig is the track's position in the document's track order, not its
// position among the tracks being kept — every bar lookup below is
// indexed by the original order, and warnings quote it so a number in a
// warning matches the track number in the file.
func (im *importer) buildTrack(orig int, gt *gpTrack, role score.TrackRole) *score.Track {
	tc := &trackConv{wind: im.classifyWind(orig, gt)}
	// Like the title above: a name \track cannot hold would make the
	// imported piece refuse to save.
	name, changed := textfmt.CleanLabel(gt.Name)
	if changed {
		im.warnf("%s: the track name holds text a saved .gtab cannot (a line break or \"//\"); imported as %q", trackLabel(gt, orig), name)
	}
	tr := &score.Track{Name: name, Role: role}
	if tc.wind != nil {
		// One representation per family (Validate rejects a mix): a wind
		// track carries its instrument and no strings, no capo. The
		// file's declared program is kept when present — the file said
		// something, so it wins — and a track resolved by pitch evidence
		// alone takes the instrument's own program rather than the
		// importer's guitar default.
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
	// Only the wind path counts into these; both stay 0 on a fretted track.
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

// hasTuningProperty reports whether any staff or track property declares
// a tuning. Presence alone counts — even one that would fail to parse —
// because for wind classification the property is a fretted track's
// signature, and misreading a fretted track as wind destroys its tab.
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

// quietIDs parses a space-separated id list without warning about
// unparsable tokens: it exists for the classification pre-pass, which
// walks the same references fillBar will walk again — the real pass owns
// every warning, so this one only reads.
func quietIDs(list string) []int {
	var out []int
	for _, f := range strings.Fields(list) {
		if v, err := strconv.Atoi(f); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// windEvidence is what the classification pre-pass learns from one
// track's notes.
type windEvidence struct {
	concert        int          // notes carrying a parsable ConcertPitch
	fretted        int          // notes carrying a String or Fret property
	minKey, maxKey int          // sounding range of the ConcertPitch notes
	deltas         map[int]bool // TransposedPitch − ConcertPitch, where both parse
}

// scanWind walks one track's notes collecting wind-classification
// evidence. The walk mirrors barBeats exactly — same bar slot by
// original index, same first-voice rule, same grace-beat skip — so every
// note it reads is a note the import would place, and no note the import
// skips can sway the classification. It is silent throughout: fillBar
// re-walks the same references and owns the warnings.
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

// classifyWind decides whether a track imports as a wind part, and as
// which instrument. Classification is structural and conservative — a
// fretted track misread as wind loses its authored tab, which is worse
// than importing nothing — so a track is a wind candidate only when it
// carries no Tuning property at all, none of its notes carry a String or
// Fret property, and its notes do carry concert pitch. Names are never
// consulted: the percussion filter learned that lesson the expensive way
// (see drumKitNames).
//
// A candidate still needs a registry instrument, resolved in order: the
// track's GeneralMidi Program via score.WindByProgram — an explicitly
// declared program the registry does not know as a wind is contradictory
// evidence, not a license to fall through, because the file named some
// other instrument and importing it under a wind's name would
// misrepresent it — then, with no program declared, the one consistent
// written-minus-concert delta combined with the notes fitting the
// candidate's sounding range (LowSounding up to written MIDI 127; Span
// caps only what the editor offers, so altissimo does not disqualify).
// Anything ambiguous or contradictory returns nil with one aggregate
// warning naming the missing evidence; the notes then take today's
// skip-with-warnings path rather than a guessed instrument.
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

// trackSetup extracts a track's tuning (GPIF stores open-string pitches
// low to high; the score model wants highest first) and capo fret.
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
		// A bad tuning already warned "assuming standard" above; saying
		// "no tuning property" on top of it would be false.
		if !badTuning {
			im.warnf("track %d (%s): no tuning property; assuming standard EADGBE", orig+1, gt.Name)
		}
		tuning = append(score.Tuning{}, score.StandardTuning...)
	}
	if capo < 0 || capo > maxCapoFret {
		// A wrong capo shifts every pitch on the track, so clamping a
		// nonsense value to "no capo" is the least-wrong recovery: the
		// authored frets stay intact and only the sounding octave-ish
		// offset is lost.
		im.warnf("track %d (%s): capo fret %d outside 0-%d; using no capo", orig+1, gt.Name, capo, maxCapoFret)
		capo = 0
	}
	return tuning, capo
}

// parseTuning parses space-separated MIDI notes low-to-high and reverses
// them into the score convention (string 1 = highest-pitched). An error
// here feeds trackSetup's warn-and-assume-standard recovery, so a bad or
// absurd tuning degrades the track instead of failing the import.
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

// A beatData is one resolved beat before bar-fill accounting.
type beatData struct {
	dur   int64
	notes []score.Note
}

// fillBar resolves a track's bar in master bar mi and lays its beats
// into the score bar, padding underfull bars with a trailing rest and
// truncating overfull ones, so the bar always exactly fills its meter.
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

// barBeats resolves master bar mi's bar for the track at original index
// orig down to beats: picks the first voice (warning when other voices
// hold beats), skips grace beats, and resolves each beat's rhythm and
// notes.
//
// <Bars> holds one bar id per track in the document's track order, so orig
// — not the track's index among the kept tracks — is the only correct
// subscript here. Indexing by the kept position gives a track the bars of
// whichever track precedes it by the number of tracks filtered out ahead
// of it, silently teaching another instrument's part.
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
		num, den := rh.Tuplet.Num, rh.Tuplet.Den
		switch {
		case num <= 0 || den <= 0:
			im.warnf("rhythm %d: bad tuplet %d:%d; ignored", rh.ID, num, den)
		case num > maxTupletPart || den > maxTupletPart:
			// Unbounded, dur*int64(den) wraps int64 and the beat ends up
			// an arbitrary length that fillBar truncates to the bar
			// without ever naming the tuplet as the cause.
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

// beatNotes resolves a beat's note ids. A GP string number counts from 0
// at the lowest-pitched string; the score model counts from 1 at the
// highest, so string s becomes nStrings-s. Fingering is authored, so
// Inferred stays false. A note whose Tie destination flag is set is the
// continuation of the previous beat's note (score.Events merges them).
// A wind track resolves through windBeatNotes instead: pitch arithmetic
// on the instrument's single lane, no strings involved.
//
// Out-of-range notes are dropped here, one at a time with a warning,
// because the package contract says import fails only when the file is
// structurally unreadable: a single fret-64 note (or a weird-but-legal
// tuning+fret sum past MIDI 127) must not make the final Validate reject
// the whole score.
func (im *importer) beatNotes(orig, mi int, gbt *gpBeat, tc *trackConv) []score.Note {
	if tc.wind != nil {
		return im.windBeatNotes(orig, mi, gbt, tc)
	}
	nStrings := len(tc.tuning)
	var out []score.Note
	// A string sounds once per beat: the same note id listed twice, or two
	// note ids claiming one string, would import two attacks on one string
	// at one tick — a beat score.Events mis-merges, textfmt.Format refuses
	// to write, and no hand can play. Later duplicates are skipped with a
	// warning; internal/mxlimport collapses the same shape.
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
				// Recognized, not unknown. On a fretted track the authored
				// fingering wins and pitch stays derived (tuning+capo+fret),
				// so the spelled pitch is redundant here; and on a track
				// that failed wind classification the aggregate warning
				// from classifyWind has already explained why the pitch
				// went unused.
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
		// Belt and suspenders: string, fret, and capo are each in range,
		// but their sum is the sounding pitch, and an odd tuning can push
		// it past MIDI 127 anyway. Drop the note rather than let Validate
		// kill the import.
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

// windBeatNotes resolves a beat's note ids on a wind track: the
// ConcertPitch spelling becomes the sounding key, and the key becomes
// String 1, Fret key−LowSounding — arithmetic, not a heuristic, so
// nothing is marked Inferred. The maxImportFret cap is a fretboard
// bound and does not apply to the lane: a flute's high notes sit far
// past fret 30 and are legitimate.
//
// Range first, chords second, mirroring internal/mxlimport: a chord
// whose top note is outside the instrument falls back to its highest
// playable note rather than losing the whole beat to the unplayable one.
// Below the instrument's lowest note a key is dropped, never
// octave-rewritten — the file's pitch is authoritative. The ceiling is
// the WRITTEN pitch: a transposing instrument reads above what it
// sounds, and a sounding key past 127−Transpose has no written note
// name, so the text format could never save the piece. Span is not a
// ceiling — altissimo imports as written.
//
// The instrument is monophonic, so a beat listing several notes keeps
// only its highest sounding one — melody on top, the convention
// arrangers write by — counted into one aggregate warning per track.
// The written pitch, when present, is only cross-checked against the
// instrument's transposition; disagreements are counted for one warning
// per track and the concert pitch wins.
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
				// Cannot appear — classifyWind refuses wind for a track
				// with any fretted spelling — but must never be reported
				// as an unknown property if a future edit lets one through.
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
