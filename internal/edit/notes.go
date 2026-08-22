package edit

import (
	"fmt"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func (d *Doc) SetFret(fret int) error {
	if w := d.Track().Wind; w != nil {

		return fmt.Errorf("%s takes note names (A-G), not fret numbers", score.An(w.Name))
	}
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

			bt.Dur = d.dur
		}
		bt.Notes = append(bt.Notes, score.Note{String: str, Fret: fret})
		sortNotes(bt)

		return d.refitCurrentBar(-1)
	})
}

func (d *Doc) SetWindPitch(written int) error {
	w := d.Track().Wind
	if w == nil {
		return fmt.Errorf("this track has strings; type a fret instead")
	}
	if low := w.Written(w.LowSounding); written < low {
		return fmt.Errorf("%s is below the %s's lowest note, %s", noteName(written), w.Name, noteName(low))
	}
	if written > 127 {

		return fmt.Errorf("%s is past %s, the top of what a note name can hold", noteName(written), noteName(127))
	}
	n := w.NoteFor(w.Sounding(written))
	return d.mutate(func() error {
		bt := d.Beat()
		if len(bt.Notes) > 0 {
			bt.Notes[0].Fret = n.Fret
			return nil
		}

		bt.Dur = d.dur
		bt.Notes = append(bt.Notes, n)
		return d.refitCurrentBar(-1)
	})
}

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

func noteName(key int) string {
	names := [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}
	return fmt.Sprintf("%s%d", names[((key%12)+12)%12], key/12-1)
}

func (d *Doc) ClearNote() error {
	str := d.cur.Str
	if _, ok := d.NoteAt(str); !ok {

		return nil
	}
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

func (d *Doc) ClearBeat() error {
	return d.mutate(func() error {
		d.Beat().Notes = nil
		return d.refitCurrentBar(-1)
	})
}

func (d *Doc) SetDuration(ticks int64) error {
	if _, ok := writableDuration(ticks); !ok {
		return fmt.Errorf("that length cannot be written down: note lengths are whole, half, quarter, eighth, sixteenth and thirty-second, plain, dotted or as triplets")
	}
	err := d.mutate(func() error {
		d.Beat().Dur = ticks

		return d.refitCurrentBar(d.cur.Beat)
	})
	if err != nil {
		return fmt.Errorf("%s does not fit in what is left of bar %d: %w",
			score.An(durationLabel(ticks)), d.cur.Bar+1, err)
	}
	d.dur = ticks
	return nil
}

func (d *Doc) SetNewBeatDuration(ticks int64) error {
	if _, ok := writableDuration(ticks); !ok {
		return fmt.Errorf("that length cannot be written down: note lengths are whole, half, quarter, eighth, sixteenth and thirty-second, plain, dotted or as triplets")
	}
	d.dur = ticks
	return nil
}

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

func (d *Doc) DeleteBeat() error {
	return d.mutate(func() error {
		bar := d.Bar()
		if len(bar.Beats) <= 1 {

			bar.Beats[0].Notes = nil
			return d.refitCurrentBar(-1)
		}
		at := d.cur.Beat
		bar.Beats = append(bar.Beats[:at], bar.Beats[at+1:]...)
		return d.refitCurrentBar(-1)
	})
}

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

func (d *Doc) ToggleTech(t score.Technique) error {
	if w := d.Track().Wind; w != nil && t&(score.TechPull|score.TechDead) != 0 {

		return fmt.Errorf("pull-offs and dead notes do not exist on %s", score.An(w.Name))
	}
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

func (d *Doc) refitCurrentBar(keep int) error {
	if err := refit(d.Bar(), keep); err != nil {
		return err
	}
	retick(d.Track())
	return nil
}

func sortNotes(bt *score.Beat) {
	for i := 1; i < len(bt.Notes); i++ {
		for j := i; j > 0 && bt.Notes[j].String < bt.Notes[j-1].String; j-- {
			bt.Notes[j], bt.Notes[j-1] = bt.Notes[j-1], bt.Notes[j]
		}
	}
}

func writableDuration(ticks int64) (string, bool) {
	for _, d := range textfmt.DurationNames() {
		if d.Ticks == ticks {
			return d.Name, true
		}
	}
	return "", false
}

var durationWords = map[string]string{
	"1": "whole note", "2": "half note", "4": "quarter note",
	"8": "eighth note", "16": "sixteenth note", "32": "thirty-second note",
}

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
