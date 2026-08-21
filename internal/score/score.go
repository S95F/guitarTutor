// Package score defines musicTutor's canonical music representation.
//
// Ticks are authoritative: every position and duration is expressed in
// MIDI-style ticks at PPQ pulses per quarter note, and wall-clock time is
// always derived through the TempoMap (see ROADMAP.md "Guiding principles").
// Pitch is always derived, never stored: from tuning + capo + fret on a
// fretted track, and from the instrument's lowest note + a semitone offset
// on a wind track — so altered tunings and transposition come for free.
package score

import "fmt"

// PPQ is the tick resolution: pulses (ticks) per quarter note. 960 divides
// cleanly for triplets, dotted notes, and 32nds.
const PPQ = 960

// Common durations in ticks.
const (
	Whole     = 4 * PPQ
	Half      = 2 * PPQ
	Quarter   = PPQ
	Eighth    = PPQ / 2
	Sixteenth = PPQ / 4
	ThirtySec = PPQ / 8
)

// Dotted returns d extended by half its length.
func Dotted(d int64) int64 { return d + d/2 }

// Triplet returns the length of one note of a triplet spanning what two
// notes of duration d would normally span.
func Triplet(d int64) int64 { return d * 2 / 3 }

// A Score is one piece of music: shared tempo and meter maps plus one or
// more tracks.
type Score struct {
	Title  string
	Tempos TempoMap
	Meters MeterMap
	Tracks []*Track
}

// TrackRole distinguishes the part the user practices from accompaniment.
type TrackRole int

const (
	// RoleUser is the part shown as tablature and (in Phase 2) scored.
	RoleUser TrackRole = iota
	// RoleBacking is accompaniment: played, not shown as the practice part.
	RoleBacking
)

// A Track is one instrument part.
type Track struct {
	Name string
	// Tuning holds the open-string MIDI note per string, indexed by
	// string number - 1. String 1 is the highest-pitched string (standard
	// tab convention), so index 0 is high E on a standard-tuned guitar.
	// A wind track has no strings: its Tuning is nil.
	Tuning Tuning
	// Capo is the capo fret; it shifts every note's pitch up. Always 0 on
	// a wind track.
	Capo int
	// Program is a General MIDI program hint for synthesis (0-127).
	Program int
	// Wind identifies the track as a monophonic wind part — nil means a
	// fretted instrument. A wind track's notes live on one chromatic
	// lane: String is always 1 and Fret counts semitones above the
	// instrument's lowest note (see WindInstrument).
	Wind *WindInstrument
	Role TrackRole
	Bars []*Bar
}

// Tuning is a set of open-string MIDI notes, highest-pitched string first.
type Tuning []int

// StandardTuning is EADGBE, written here from string 1 (high E4) down to
// string 6 (low E2).
var StandardTuning = Tuning{64, 59, 55, 50, 45, 40}

// Equal reports whether two tunings are the same notes on the same number
// of strings.
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

// NamedTunings are the tunings the app calls by name — the ones a
// guitarist actually retunes to. The editor cycles through them; the
// library and the editor's toolbar both name tunings from this one table
// so no two screens can disagree about what a piece is in. Anything else
// is reachable by editing the \tuning line in the text view.
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

// TuningName names a tuning in a guitarist's words: a preset's name when
// it is one, otherwise whatever can honestly be said about it.
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

// Pitch derives a note's sounding MIDI key: from the track's tuning and
// capo on a fretted track, from the instrument's lowest note on a wind
// track. Wind pitch is sounding (concert) pitch — the written pitch a
// player reads is a display concern, reached through WindInstrument.
func (t *Track) Pitch(n Note) int {
	if t.Wind != nil {
		return t.Wind.LowSounding + n.Fret
	}
	return t.Tuning[n.String-1] + t.Capo + n.Fret
}

// A Bar is one measure. Start and the meter are denormalized onto the bar
// (they are derivable from the maps) because every consumer needs them.
type Bar struct {
	Start    int64 // tick at which the bar begins
	Num, Den int   // meter in effect
	Beats    []*Beat
}

// Len returns the bar's length in ticks under its meter.
func (b *Bar) Len() int64 { return int64(b.Num) * (4 * PPQ / int64(b.Den)) }

// A Beat is one rhythmic event: zero notes (a rest), one note, or several
// struck together (a chord).
type Beat struct {
	Start int64 // tick
	Dur   int64 // ticks
	Notes []Note
}

// Technique is a bitmask of playing techniques on a note.
type Technique uint8

const (
	TechHammer  Technique = 1 << iota // hammer-on
	TechPull                          // pull-off
	TechSlide                         // slide into this note
	TechBend                          // bend
	TechVibrato                       // vibrato
	TechDead                          // dead/muted note (x)

	// TechSlur marks a wind note slurred from its predecessor: the pitch
	// changes without a fresh attack (no new tongue). It is deliberately
	// the same bit as TechHammer — both mean exactly that to the engine's
	// articulation, and one bit with two instrument-appropriate names
	// beats two bits with one meaning. TechPull and TechDead never appear
	// on wind tracks (Validate refuses them).
	TechSlur = TechHammer
)

// A Note is one string+fret event within a beat.
type Note struct {
	String int // 1 = highest-pitched string
	Fret   int
	// Tied marks this note as the continuation of the previous beat's
	// note on the same string: it sustains, it is not attacked again.
	Tied bool
	// Inferred marks a fingering assigned by heuristic (e.g. MIDI
	// import, which carries no string/fret data) rather than authored.
	// The UI renders inferred fingerings distinctly.
	Inferred bool
	Tech     Technique
}

// AppendBar adds a bar to the track with its start tick computed from the
// previous bar (or 0 for the first), under meter num/den.
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

// AddBeat appends a beat of duration dur to the bar, with its start tick
// computed from the beats already in the bar. No notes means a rest.
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

// End returns the tick just past the last beat of the last bar across all
// tracks (the score's length).
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
