package mxlimport

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/S95F/guitarTutor/internal/score"
)

// xmlContainer is META-INF/container.xml inside a .mxl archive.
type xmlContainer struct {
	XMLName   xml.Name      `xml:"container"`
	RootFiles []xmlRootFile `xml:"rootfiles>rootfile"`
}

type xmlRootFile struct {
	FullPath string `xml:"full-path,attr"`
}

// xmlScorePartwise is the score-partwise document root. Unmarshal rejects
// any other root element name.
type xmlScorePartwise struct {
	XMLName xml.Name `xml:"score-partwise"`
	Work    struct {
		Title string `xml:"work-title"`
	} `xml:"work"`
	MovementTitle string `xml:"movement-title"`
	PartList      struct {
		ScoreParts []xmlScorePart `xml:"score-part"`
	} `xml:"part-list"`
	Parts []xmlPart `xml:"part"`
}

// title prefers work-title, falling back to movement-title.
func (d *xmlScorePartwise) title() string {
	if t := strings.TrimSpace(d.Work.Title); t != "" {
		return t
	}
	return strings.TrimSpace(d.MovementTitle)
}

type xmlScorePart struct {
	ID              string `xml:"id,attr"`
	PartName        string `xml:"part-name"`
	MidiInstruments []struct {
		Program int `xml:"midi-program"`
	} `xml:"midi-instrument"`
}

// midiProgram returns the declared General MIDI program converted from
// MusicXML's 1-based numbering to 0-based, or -1 when absent.
func (sp *xmlScorePart) midiProgram() int {
	for _, mi := range sp.MidiInstruments {
		if mi.Program >= 1 && mi.Program <= 128 {
			return mi.Program - 1
		}
	}
	return -1
}

type xmlPart struct {
	ID       string       `xml:"id,attr"`
	Measures []xmlMeasure `xml:"measure"`
}

// xmlMeasure preserves the ORDER of its children: <backup>, <forward>,
// <note>, and mid-measure <attributes>/<direction> only mean anything
// relative to the elements around them (the time cursor). Elements holds
// *xmlAttributes, *xmlNote, *xmlBackup, *xmlForward, *xmlDirection, and
// *xmlSound in document order; everything else is skipped.
type xmlMeasure struct {
	Number   string
	Elements []any
}

func (m *xmlMeasure) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		if a.Name.Local == "number" {
			m.Number = a.Value
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var el any
			switch t.Name.Local {
			case "attributes":
				el = new(xmlAttributes)
			case "note":
				el = new(xmlNote)
			case "backup":
				el = new(xmlBackup)
			case "forward":
				el = new(xmlForward)
			case "direction":
				el = new(xmlDirection)
			case "sound":
				el = new(xmlSound)
			default:
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			if err := d.DecodeElement(el, &t); err != nil {
				return err
			}
			m.Elements = append(m.Elements, el)
		case xml.EndElement:
			return nil
		}
	}
}

type xmlAttributes struct {
	Divisions    int               `xml:"divisions"`
	Times        []xmlTime         `xml:"time"`
	StaffDetails []xmlStaffDetails `xml:"staff-details"`
}

// firstTime returns the first <time> child, or nil.
func (a *xmlAttributes) firstTime() *xmlTime {
	if len(a.Times) == 0 {
		return nil
	}
	return &a.Times[0]
}

type xmlTime struct {
	Beats    string `xml:"beats"`
	BeatType string `xml:"beat-type"`
}

// parse returns the time signature as integers. Composite numerators
// ("3+2") and senza-misura are not in the subset.
func (t *xmlTime) parse() (num, den int, err error) {
	num, err = strconv.Atoi(strings.TrimSpace(t.Beats))
	if err != nil {
		return 0, 0, fmt.Errorf("unsupported <time> numerator %q", t.Beats)
	}
	den, err = strconv.Atoi(strings.TrimSpace(t.BeatType))
	if err != nil {
		return 0, 0, fmt.Errorf("unsupported <time> denominator %q", t.BeatType)
	}
	return num, den, nil
}

type xmlStaffDetails struct {
	StaffLines int              `xml:"staff-lines"`
	Tunings    []xmlStaffTuning `xml:"staff-tuning"`
	Capo       *int             `xml:"capo"`
}

// tuning converts <staff-tuning> lines to a score Tuning. MusicXML numbers
// tab staff lines from the BOTTOM: line 1 is the lowest string on the
// staff. The score model numbers strings from the top: string 1 is the
// highest-pitched. With n lines, line L is string n-L+1.
func (sd *xmlStaffDetails) tuning() (score.Tuning, bool, string) {
	if len(sd.Tunings) == 0 {
		return nil, false, ""
	}
	n := sd.StaffLines
	if n == 0 {
		for _, t := range sd.Tunings {
			if t.Line > n {
				n = t.Line
			}
		}
	}
	if len(sd.Tunings) != n {
		return nil, false, fmt.Sprintf("staff-details has %d staff-tuning lines for a %d-line staff", len(sd.Tunings), n)
	}
	out := make(score.Tuning, n)
	seen := make([]bool, n+1)
	for _, t := range sd.Tunings {
		if t.Line < 1 || t.Line > n || seen[t.Line] {
			return nil, false, fmt.Sprintf("staff-tuning line %d is out of range or repeated", t.Line)
		}
		seen[t.Line] = true
		key, ok := midiKey(t.Step, t.Alter, t.Octave)
		if !ok {
			return nil, false, fmt.Sprintf("staff-tuning line %d has unrecognized tuning-step %q", t.Line, t.Step)
		}
		out[n-t.Line] = key // string s = n-L+1, index s-1 = n-L
	}
	return out, true, ""
}

type xmlStaffTuning struct {
	Line   int     `xml:"line,attr"`
	Step   string  `xml:"tuning-step"`
	Alter  float64 `xml:"tuning-alter"`
	Octave int     `xml:"tuning-octave"`
}

type xmlNote struct {
	Grace     *struct{}      `xml:"grace"`
	Chord     *struct{}      `xml:"chord"`
	Rest      *struct{}      `xml:"rest"`
	Pitch     *xmlPitch      `xml:"pitch"`
	Duration  int            `xml:"duration"`
	Ties      []xmlTie       `xml:"tie"`
	Voice     string         `xml:"voice"`
	Staff     int            `xml:"staff"`
	Notations []xmlNotations `xml:"notations"`
}

// tie reports the note's sound ties: stop (it continues an earlier note)
// and start (a later note continues it). A mid-chain note carries both.
func (n *xmlNote) tie() (stop, start bool) {
	for _, t := range n.Ties {
		switch t.Type {
		case "stop":
			stop = true
		case "start":
			start = true
		}
	}
	return stop, start
}

// fingering returns the authored <technical> string and fret, if both are
// present. MusicXML's <string> numbering (1 = highest-pitched) matches the
// score model's, so the value is used directly.
func (n *xmlNote) fingering() (str, fret int, ok bool) {
	for _, nt := range n.Notations {
		if t := nt.Technical; t != nil && t.String != nil && t.Fret != nil {
			return *t.String, *t.Fret, true
		}
	}
	return 0, 0, false
}

type xmlPitch struct {
	Step   string  `xml:"step"`
	Alter  float64 `xml:"alter"`
	Octave int     `xml:"octave"`
}

type xmlTie struct {
	Type string `xml:"type,attr"`
}

type xmlNotations struct {
	Technical *xmlTechnical `xml:"technical"`
}

type xmlTechnical struct {
	String *int `xml:"string"`
	Fret   *int `xml:"fret"`
}

type xmlBackup struct {
	Duration int `xml:"duration"`
}

type xmlForward struct {
	Duration int `xml:"duration"`
}

type xmlDirection struct {
	Sound      *xmlSound      `xml:"sound"`
	Metronomes []xmlMetronome `xml:"direction-type>metronome"`
}

type xmlSound struct {
	Tempo float64 `xml:"tempo,attr"`
}

type xmlMetronome struct {
	BeatUnit  string     `xml:"beat-unit"`
	Dots      []struct{} `xml:"beat-unit-dot"`
	PerMinute string     `xml:"per-minute"`
}

// quarterBPM converts a metronome marking to quarter-note BPM. Non-numeric
// per-minute text ("ca. 120") and unknown beat units are unsupported.
func (m *xmlMetronome) quarterBPM() (float64, bool, string) {
	pm, err := strconv.ParseFloat(strings.TrimSpace(m.PerMinute), 64)
	if err != nil || pm <= 0 {
		return 0, false, fmt.Sprintf("unsupported metronome per-minute %q; tempo ignored", m.PerMinute)
	}
	quarters, ok := beatUnitQuarters[m.BeatUnit]
	if !ok {
		return 0, false, fmt.Sprintf("unsupported metronome beat-unit %q; tempo ignored", m.BeatUnit)
	}
	for range m.Dots {
		quarters *= 1.5
	}
	return pm * quarters, true, ""
}

// beatUnitQuarters maps a metronome beat-unit to its length in quarters.
var beatUnitQuarters = map[string]float64{
	"breve":   8,
	"whole":   4,
	"half":    2,
	"quarter": 1,
	"eighth":  0.5,
	"16th":    0.25,
	"32nd":    0.125,
	"64th":    0.0625,
}

// stepSemitones maps a pitch/tuning step letter to semitones above C.
var stepSemitones = map[string]int{
	"C": 0, "D": 2, "E": 4, "F": 5, "G": 7, "A": 9, "B": 11,
}

// midiKey converts step/alter/octave to a MIDI key (C4 = 60).
func midiKey(step string, alter float64, octave int) (int, bool) {
	sem, ok := stepSemitones[strings.TrimSpace(step)]
	if !ok {
		return 0, false
	}
	return (octave+1)*12 + sem + int(roundHalf(alter)), true
}

// roundHalf rounds to nearest, halves away from zero.
func roundHalf(v float64) float64 {
	if v < 0 {
		return float64(int(v - 0.5))
	}
	return float64(int(v + 0.5))
}
