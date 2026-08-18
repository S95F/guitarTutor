package edit

import (
	"fmt"

	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
)

// SetFret puts a note at the cursor: on the cursor's string, in the
// cursor's beat, at this fret. A note already on that string is replaced,
// keeping its tie and techniques — retyping a fret is a correction, not a
// reset.
func (d *Doc) SetFret(fret int) error {
	if fret < 0 || fret > textfmt.MaxFret {
		return fmt.Errorf("fret %d is outside 0-%d", fret, textfmt.MaxFret)
	}
	tr := d.Track()
	str := d.cur.Str
	if key := tr.Tuning[str-1] + tr.Capo + fret; key > 127 {
		return fmt.Errorf("string %d fret %d sounds above the top of MIDI (key %d); lower the capo or the fret", str, fret, key)
	}
	return d.mutate(func() error {
		bt := d.Beat()
		for i := range bt.Notes {
			if bt.Notes[i].String == str {
				bt.Notes[i].Fret = fret
				return nil
			}
		}
		if len(bt.Notes) == 0 {
			// Typing onto a rest gives the note the length the palette is
			// set to, not the length of the silence it replaced. A fresh
			// 4/4 bar is one whole rest, and without this the first note of
			// every bar would be a whole note.
			bt.Dur = d.dur
		}
		bt.Notes = append(bt.Notes, score.Note{String: str, Fret: fret})
		sortNotes(bt)
		// The beat is content now, so whatever padding the bar had behind
		// it has to be laid again around it.
		return d.refitCurrentBar(-1)
	})
}

// ClearNote removes the note on the cursor's string. A beat left with no
// notes is a rest, which is what the bar's padding is made of, so clearing
// the last note of a trailing beat lets it merge back into the padding.
func (d *Doc) ClearNote() error {
	str := d.cur.Str
	return d.mutate(func() error {
		bt := d.Beat()
		for i := range bt.Notes {
			if bt.Notes[i].String == str {
				bt.Notes = append(bt.Notes[:i], bt.Notes[i+1:]...)
				return d.refitCurrentBar(-1)
			}
		}
		return nil
	})
}

// ClearBeat empties the beat under the cursor, making it a rest.
func (d *Doc) ClearBeat() error {
	return d.mutate(func() error {
		d.Beat().Notes = nil
		return d.refitCurrentBar(-1)
	})
}

// SetDuration sets the length of the beat under the cursor, and makes it
// the length new beats take. Lengthening a beat eats the bar's trailing
// padding; when there is not enough padding to eat, the edit is refused
// with what would not fit, rather than pushing a note into the next bar.
func (d *Doc) SetDuration(ticks int64) error {
	if _, ok := writableDuration(ticks); !ok {
		return fmt.Errorf("that length cannot be written down: note lengths are whole, half, quarter, eighth, sixteenth and thirty-second, plain, dotted or as triplets")
	}
	err := d.mutate(func() error {
		d.Beat().Dur = ticks
		// The cursor's beat is kept even if it is a rest: setting a length
		// on a rest is how a deliberate rest gets written.
		return d.refitCurrentBar(d.cur.Beat)
	})
	if err != nil {
		return fmt.Errorf("a %s does not fit in what is left of bar %d: %w",
			durationLabel(ticks), d.cur.Bar+1, err)
	}
	d.dur = ticks
	return nil
}

// SetNewBeatDuration changes the length new beats take without touching
// the beat under the cursor. It is the palette choice on its own — useful
// before inserting, where SetDuration's "change what is under the cursor"
// would be the wrong half of the gesture.
func (d *Doc) SetNewBeatDuration(ticks int64) error {
	if _, ok := writableDuration(ticks); !ok {
		return fmt.Errorf("that length cannot be written down: note lengths are whole, half, quarter, eighth, sixteenth and thirty-second, plain, dotted or as triplets")
	}
	d.dur = ticks
	return nil
}

// InsertBeat puts a rest of the current duration after the cursor and
// moves onto it, so typing a fret next fills it. It eats the bar's
// trailing padding; a bar with no padding left refuses.
func (d *Doc) InsertBeat() error {
	dur := d.dur
	err := d.mutate(func() error {
		bar := d.Bar()
		at := d.cur.Beat + 1
		bar.Beats = append(bar.Beats, nil)
		copy(bar.Beats[at+1:], bar.Beats[at:])
		bar.Beats[at] = &score.Beat{Dur: dur}
		d.cur.Beat = at
		return d.refitCurrentBar(at)
	})
	if err != nil {
		return fmt.Errorf("there is no room for another %s in bar %d: %w",
			durationLabel(dur), d.cur.Bar+1, err)
	}
	return nil
}

// DeleteBeat removes the beat under the cursor; the space it leaves
// becomes part of the bar's trailing padding.
func (d *Doc) DeleteBeat() error {
	return d.mutate(func() error {
		bar := d.Bar()
		if len(bar.Beats) <= 1 {
			// The only beat in the bar: empty it instead of removing it, so
			// the bar keeps somewhere for the cursor to be.
			bar.Beats[0].Notes = nil
			return d.refitCurrentBar(-1)
		}
		at := d.cur.Beat
		bar.Beats = append(bar.Beats[:at], bar.Beats[at+1:]...)
		return d.refitCurrentBar(-1)
	})
}

// ToggleTie marks (or unmarks) the note under the cursor as a
// continuation of the same string's note in the previous beat: it sustains
// instead of being struck again.
func (d *Doc) ToggleTie() error {
	str := d.cur.Str
	return d.mutate(func() error {
		bt := d.Beat()
		for i := range bt.Notes {
			if bt.Notes[i].String == str {
				bt.Notes[i].Tied = !bt.Notes[i].Tied
				return nil
			}
		}
		return fmt.Errorf("put a fret number on string %d first, then tie it", str)
	})
}

// ToggleTech turns one technique on or off for the note under the cursor.
func (d *Doc) ToggleTech(t score.Technique) error {
	str := d.cur.Str
	return d.mutate(func() error {
		bt := d.Beat()
		for i := range bt.Notes {
			if bt.Notes[i].String == str {
				bt.Notes[i].Tech ^= t
				return nil
			}
		}
		return fmt.Errorf("put a fret number on string %d first, then mark it", str)
	})
}

// refitCurrentBar restores the bar under the cursor and puts the ticks
// back across the whole track. keep names a beat that counts as content
// however empty it is (see refit): the operations that deliberately make a
// rest — setting a length, inserting a beat — pass their own beat, and the
// ones that merely leave one behind pass -1 so it merges back into the
// bar's padding. Called from inside mutate, so a bar that cannot be made
// to fit rolls the whole edit back.
func (d *Doc) refitCurrentBar(keep int) error {
	if err := refit(d.Bar(), keep); err != nil {
		return err
	}
	retick(d.Track())
	return nil
}

// sortNotes puts a beat's notes in string order, highest-pitched string
// first. Nothing depends on the order — but a chord that reorders itself
// as it is typed reads as though the editor is shuffling the strings, and
// the saved file's diff moves lines that did not change.
func sortNotes(bt *score.Beat) {
	for i := 1; i < len(bt.Notes); i++ {
		for j := i; j > 0 && bt.Notes[j].String < bt.Notes[j-1].String; j-- {
			bt.Notes[j], bt.Notes[j-1] = bt.Notes[j-1], bt.Notes[j]
		}
	}
}

// writableDuration reports whether a length is one the format can spell,
// and how.
func writableDuration(ticks int64) (string, bool) {
	for _, d := range textfmt.DurationNames() {
		if d.Ticks == ticks {
			return d.Name, true
		}
	}
	return "", false
}

// durationWords is how each written note length is said out loud. The
// spellings on the left are the FILE's; every message a user reads uses
// the one on the right.
var durationWords = map[string]string{
	"1": "whole note", "2": "half note", "4": "quarter note",
	"8": "eighth note", "16": "sixteenth note", "32": "thirty-second note",
}

// durationLabel names a length the way a musician would: "eighth note",
// "dotted quarter note", "sixteenth triplet".
func durationLabel(ticks int64) string {
	name, ok := writableDuration(ticks)
	if !ok {
		return "note of that length"
	}
	switch {
	case len(name) > 1 && name[len(name)-1] == '.':
		return "dotted " + durationWords[name[:len(name)-1]]
	case len(name) > 1 && name[len(name)-1] == 't':
		return durationWords[name[:len(name)-1]] + " triplet"
	}
	return durationWords[name]
}
