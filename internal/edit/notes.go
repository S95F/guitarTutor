package edit

import (
	"fmt"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
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

// SetWindPitch puts a note at the cursor of a wind track, given the
// WRITTEN pitch its player reads (the model stores the sounding offset;
// the conversion happens here, at the edge, like everywhere else). A note
// already in the beat is replaced, keeping its tie and techniques —
// retyping a pitch is a correction, not a reset.
func (d *Doc) SetWindPitch(written int) error {
	w := d.Track().Wind
	if w == nil {
		return fmt.Errorf("this track has strings; type a fret instead")
	}
	if low := w.Written(w.LowSounding); written < low {
		return fmt.Errorf("%s is below the %s's lowest note, %s", noteName(written), w.Name, noteName(low))
	}
	if written > 127 {
		// The sounding pitch would fit MIDI, but the written one has no
		// note name, so the piece could never be saved.
		return fmt.Errorf("%s is past the top of what a note name can hold", noteName(127))
	}
	n := w.NoteFor(w.Sounding(written))
	return d.mutate(func() error {
		bt := d.Beat()
		if len(bt.Notes) > 0 {
			bt.Notes[0].Fret = n.Fret
			return nil
		}
		// Typing onto a rest gives the note the length the palette is set
		// to, not the length of the silence it replaced (see SetFret).
		bt.Dur = d.dur
		bt.Notes = append(bt.Notes, n)
		return d.refitCurrentBar(-1)
	})
}

// NudgePitch moves the wind note under the cursor by delta semitones —
// the chromatic step the arrow keys make, an octave with shift.
func (d *Doc) NudgePitch(delta int) error {
	w := d.Track().Wind
	if w == nil {
		return fmt.Errorf("this track has strings; arrows choose the string instead")
	}
	bt := d.Beat()
	if len(bt.Notes) == 0 {
		return fmt.Errorf("put a note here first (A-G), then move it")
	}
	fret := bt.Notes[0].Fret + delta
	if fret < 0 {
		return fmt.Errorf("that is below the %s's lowest note, %s", w.Name, noteName(w.Written(w.LowSounding)))
	}
	if written := w.Written(w.LowSounding + fret); written > 127 {
		return fmt.Errorf("that is past the top of what a note name can hold")
	}
	return d.mutate(func() error {
		d.Beat().Notes[0].Fret = fret
		return nil
	})
}

// WindReference is the sounding key a freshly typed note letter lands
// nearest to: the note under the cursor, else the closest note before it,
// else the middle of the instrument's standard range. It is what makes
// typing "D" after a G5 give the D beside that G rather than one at the
// bottom of the horn.
func (d *Doc) WindReference() int {
	w := d.Track().Wind
	if w == nil {
		return 0
	}
	tr := d.Track()
	c := d.cur
	for bar := c.Bar; bar >= 0; bar-- {
		beats := tr.Bars[bar].Beats
		start := len(beats) - 1
		if bar == c.Bar {
			start = c.Beat
		}
		for bi := start; bi >= 0; bi-- {
			if ns := beats[bi].Notes; len(ns) > 0 {
				return tr.Pitch(ns[0])
			}
		}
	}
	return w.LowSounding + w.Span/2
}

// noteName spells a MIDI key in scientific pitch, sharps only — the same
// one-spelling-per-pitch convention the text format writes with.
func noteName(key int) string {
	names := [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	return fmt.Sprintf("%s%d", names[((key%12)+12)%12], key/12-1)
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
