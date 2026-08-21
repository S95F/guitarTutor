package mxlimport

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/S95F/musicTutor/internal/score"
)

type xmlContainer struct {
	XMLName   xml.Name      `xml:"container"`
	RootFiles []xmlRootFile `xml:"rootfiles>rootfile"`
}

type xmlRootFile struct {
	FullPath string `xml:"full-path,attr"`
}

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

type xmlMeasure struct {
	Number string

	Implicit bool
	Elements []any
	Barlines []xmlBarline
}

func (m *xmlMeasure) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "number":
			m.Number = a.Value
		case "implicit":
			m.Implicit = strings.EqualFold(strings.TrimSpace(a.Value), "yes")
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "barline" {
				var bl xmlBarline
				if err := d.DecodeElement(&bl, &t); err != nil {
					return err
				}
				m.Barlines = append(m.Barlines, bl)
				continue
			}
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

type xmlBarline struct {
	Location string     `xml:"location,attr"`
	Repeat   *xmlRepeat `xml:"repeat"`
	Ending   *xmlEnding `xml:"ending"`
}

type xmlRepeat struct {
	Direction string `xml:"direction,attr"`
	Times     int    `xml:"times,attr"`
}

type xmlEnding struct {
	Number string `xml:"number,attr"`
	Type   string `xml:"type,attr"`
}

func (e *xmlEnding) passes() []int {
	var out []int
	for _, f := range strings.Split(e.Number, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 {
			return nil
		}
		out = append(out, n)
	}
	return out
}

type xmlAttributes struct {
	Divisions    int               `xml:"divisions"`
	Times        []xmlTime         `xml:"time"`
	StaffDetails []xmlStaffDetails `xml:"staff-details"`
	Transposes   []xmlTranspose    `xml:"transpose"`
}

type xmlTranspose struct {
	Number       *int       `xml:"number,attr"`
	Diatonic     float64    `xml:"diatonic"`
	Chromatic    float64    `xml:"chromatic"`
	OctaveChange int        `xml:"octave-change"`
	Double       *xmlDouble `xml:"double"`
}

type xmlDouble struct {
	Above string `xml:"above,attr"`
}

func (t *xmlTranspose) semitones() int {
	sem := int(roundHalf(t.Chromatic)) + 12*t.OctaveChange
	if t.Double != nil {
		if strings.EqualFold(strings.TrimSpace(t.Double.Above), "yes") {
			sem += 12
		} else {
			sem -= 12
		}
	}
	return sem
}

func (a *xmlAttributes) transposeForStaff(n int) (*xmlTranspose, bool) {
	var unnumbered *xmlTranspose
	for i := range a.Transposes {
		t := &a.Transposes[i]
		if t.Number == nil {
			if unnumbered == nil {
				unnumbered = t
			}
			continue
		}
		if *t.Number == n {
			return t, true
		}
	}
	if unnumbered != nil {
		return unnumbered, true
	}
	return nil, false
}

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

const maxTuningLines = 25

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
	if n > maxTuningLines {
		return nil, false, fmt.Sprintf("staff-details declares a %d-line tab staff, more than the %d-string limit", n, maxTuningLines)
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

		if key < 0 || key > 127 {
			return nil, false, fmt.Sprintf("staff-tuning line %d is MIDI key %d, outside 0-127", t.Line, key)
		}
		out[n-t.Line] = key
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
	Unpitched *struct{}      `xml:"unpitched"`
	Duration  int            `xml:"duration"`
	Ties      []xmlTie       `xml:"tie"`
	Voice     string         `xml:"voice"`
	Staff     int            `xml:"staff"`
	Notations []xmlNotations `xml:"notations"`
}

func (n *xmlNote) tie() (stop, start bool, stopNum, startNum int) {
	for _, t := range n.Ties {
		switch t.Type {
		case "stop":
			stop = true
			if stopNum == 0 && t.Number > 0 {
				stopNum = t.Number
			}
		case "start":
			start = true
			if startNum == 0 && t.Number > 0 {
				startNum = t.Number
			}
		}
	}
	return stop, start, stopNum, startNum
}

func (n *xmlNote) slurs() (stops, starts []int) {
	for _, nt := range n.Notations {
		for _, sl := range nt.Slurs {
			num := sl.Number
			if num < 1 {
				num = 1
			}
			switch sl.Type {
			case "stop":
				stops = append(stops, num)
			case "start":
				starts = append(starts, num)
			}
		}
	}
	return stops, starts
}

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
	Type   string `xml:"type,attr"`
	Number int    `xml:"number,attr"`
}

type xmlNotations struct {
	Technical *xmlTechnical `xml:"technical"`
	Slurs     []xmlSlur     `xml:"slur"`
}

type xmlSlur struct {
	Type   string `xml:"type,attr"`
	Number int    `xml:"number,attr"`
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
	Segnos     []struct{}     `xml:"direction-type>segno"`
	Codas      []struct{}     `xml:"direction-type>coda"`
	Words      []string       `xml:"direction-type>words"`
}

func (d *xmlDirection) jumpMarks() []string {
	var out []string
	if d.Sound != nil {
		out = append(out, d.Sound.jumpMarks()...)
	}
	if len(d.Segnos) > 0 {
		out = append(out, "segno")
	}
	if len(d.Codas) > 0 {
		out = append(out, "coda")
	}
	for _, w := range d.Words {
		w = strings.ToLower(w)
		for _, phrase := range jumpPhrases {
			if strings.Contains(w, phrase) {
				out = append(out, phrase)
			}
		}
	}
	return out
}

var jumpPhrases = []string{"d.c", "d.s", "da capo", "dal segno", "to coda", "al coda", "al fine"}

type xmlSound struct {
	Tempo    float64 `xml:"tempo,attr"`
	DaCapo   string  `xml:"dacapo,attr"`
	DalSegno string  `xml:"dalsegno,attr"`
	ToCoda   string  `xml:"tocoda,attr"`
	Fine     string  `xml:"fine,attr"`
	Segno    string  `xml:"segno,attr"`
	Coda     string  `xml:"coda,attr"`
}

func (s *xmlSound) jumpMarks() []string {
	var out []string
	for _, m := range []struct {
		name, val string
	}{
		{"D.C.", s.DaCapo}, {"D.S.", s.DalSegno}, {"to coda", s.ToCoda},
		{"fine", s.Fine}, {"segno", s.Segno}, {"coda", s.Coda},
	} {
		if strings.TrimSpace(m.val) != "" && !strings.EqualFold(m.val, "no") {
			out = append(out, m.name)
		}
	}
	return out
}

type xmlMetronome struct {
	BeatUnit  string     `xml:"beat-unit"`
	Dots      []struct{} `xml:"beat-unit-dot"`
	PerMinute string     `xml:"per-minute"`
}

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

var stepSemitones = map[string]int{
	"C": 0, "D": 2, "E": 4, "F": 5, "G": 7, "A": 9, "B": 11,
}

func midiKey(step string, alter float64, octave int) (int, bool) {
	sem, ok := stepSemitones[strings.TrimSpace(step)]
	if !ok {
		return 0, false
	}
	return (octave+1)*12 + sem + int(roundHalf(alter)), true
}

func roundHalf(v float64) float64 {
	if v < 0 {
		return float64(int(v - 0.5))
	}
	return float64(int(v + 0.5))
}
