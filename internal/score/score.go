package score

import "fmt"

const PPQ = 960

const (
	Whole     = 4 * PPQ
	Half      = 2 * PPQ
	Quarter   = PPQ
	Eighth    = PPQ / 2
	Sixteenth = PPQ / 4
	ThirtySec = PPQ / 8
)

func Dotted(d int64) int64 { return d + d/2 }

func Triplet(d int64) int64 { return d * 2 / 3 }

type Score struct {
	Title  string
	Tempos TempoMap
	Meters MeterMap
	Tracks []*Track
}

type TrackRole int

const (
	RoleUser TrackRole = iota

	RoleBacking
)

type Track struct {
	Name string

	Tuning Tuning

	Capo int

	Program int

	Wind *WindInstrument
	Role TrackRole
	Bars []*Bar
}

type Tuning []int

var StandardTuning = Tuning{64, 59, 55, 50, 45, 40}

func (t Tuning) Equal(o Tuning) bool {
	if len(t) != len(o) {
		return false
	}
	for i := range t {
		if t[i] != o[i] {
			return false
		}
	}
	return true
}

var NamedTunings = []struct {
	Name   string
	Tuning Tuning
}{
	{"standard E", Tuning{64, 59, 55, 50, 45, 40}},
	{"drop D", Tuning{64, 59, 55, 50, 45, 38}},
	{"half step down", Tuning{63, 58, 54, 49, 44, 39}},
	{"full step down", Tuning{62, 57, 53, 48, 43, 38}},
	{"DADGAD", Tuning{62, 57, 55, 50, 45, 38}},
	{"open G", Tuning{62, 59, 55, 50, 43, 38}},
}

func TuningName(t Tuning) string {
	for _, p := range NamedTunings {
		if t.Equal(p.Tuning) {
			return p.Name
		}
	}
	if len(t) != len(StandardTuning) {
		return fmt.Sprintf("%d strings", len(t))
	}
	return "altered tuning"
}

func (t *Track) Pitch(n Note) int {
	if t.Wind != nil {
		return t.Wind.LowSounding + n.Fret
	}
	return t.Tuning[n.String-1] + t.Capo + n.Fret
}

type Bar struct {
	Start    int64
	Num, Den int
	Beats    []*Beat
}

func (b *Bar) Len() int64 { return int64(b.Num) * (4 * PPQ / int64(b.Den)) }

type Beat struct {
	Start int64
	Dur   int64
	Notes []Note
}

type Technique uint8

const (
	TechHammer Technique = 1 << iota
	TechPull
	TechSlide
	TechBend
	TechVibrato
	TechDead

	TechSlur = TechHammer
)

type Note struct {
	String int
	Fret   int

	Tied bool

	Inferred bool
	Tech     Technique
}

func (t *Track) AppendBar(num, den int) *Bar {
	start := int64(0)
	if n := len(t.Bars); n > 0 {
		last := t.Bars[n-1]
		start = last.Start + last.Len()
	}
	b := &Bar{Start: start, Num: num, Den: den}
	t.Bars = append(t.Bars, b)
	return b
}

func (b *Bar) AddBeat(dur int64, notes ...Note) *Beat {
	start := b.Start
	if n := len(b.Beats); n > 0 {
		last := b.Beats[n-1]
		start = last.Start + last.Dur
	}
	bt := &Beat{Start: start, Dur: dur, Notes: notes}
	b.Beats = append(b.Beats, bt)
	return bt
}

func (s *Score) End() int64 {
	var end int64
	for _, t := range s.Tracks {
		if n := len(t.Bars); n > 0 {
			last := t.Bars[n-1]
			if e := last.Start + last.Len(); e > end {
				end = e
			}
		}
	}
	return end
}
