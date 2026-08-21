package edit

import (
	"fmt"
	"strings"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func (d *Doc) BarCount() int { return len(d.sc.Tracks[0].Bars) }

func (d *Doc) AppendBar() error {
	err := d.mutate(func() error {
		return d.rebuildWith(d.insertBarAt(d.BarCount()))
	})
	if err != nil {
		return err
	}
	d.cur.Bar, d.cur.Beat = d.BarCount()-1, 0
	d.clampCursor()
	return nil
}

func (d *Doc) InsertBar() error {
	at := d.cur.Bar
	return d.mutate(func() error {
		return d.rebuildWith(d.insertBarAt(at))
	})
}

func (d *Doc) DeleteBar() error {
	if d.BarCount() <= 1 {
		return fmt.Errorf("a piece needs at least one bar")
	}
	at := d.cur.Bar
	return d.mutate(func() error {
		tempos := d.tempoPerBar()
		tempos = append(tempos[:at], tempos[at+1:]...)
		for _, tr := range d.sc.Tracks {
			tr.Bars = append(tr.Bars[:at], tr.Bars[at+1:]...)
		}
		return d.rebuildWith(tempos)
	})
}

func (d *Doc) insertBarAt(at int) []int64 {
	tempos := d.tempoPerBar()

	ref := at
	if ref >= len(tempos) {
		ref = len(tempos) - 1
	}
	refTempo := tempos[ref]

	tempos = append(tempos, 0)
	copy(tempos[at+1:], tempos[at:])
	tempos[at] = refTempo

	for _, tr := range d.sc.Tracks {
		src := at
		if src >= len(tr.Bars) {
			src = len(tr.Bars) - 1
		}

		bar := &score.Bar{Num: tr.Bars[src].Num, Den: tr.Bars[src].Den}
		tr.Bars = append(tr.Bars, nil)
		copy(tr.Bars[at+1:], tr.Bars[at:])
		tr.Bars[at] = bar
	}
	return tempos
}

func (d *Doc) SetMeter(num, den int) error {
	if num < 1 || num > 64 {
		return fmt.Errorf("a time signature's top number has to be 1-64, not %d", num)
	}
	switch den {
	case 1, 2, 4, 8, 16, 32:
	default:
		return fmt.Errorf("a time signature's bottom number has to be 1, 2, 4, 8, 16 or 32, not %d", den)
	}
	at := d.cur.Bar
	return d.mutate(func() error {

		tempos := d.tempoPerBar()
		ref := d.sc.Tracks[0].Bars[at]
		oldNum, oldDen := ref.Num, ref.Den
		for i := at; i < d.BarCount(); i++ {
			if i > at {
				b := d.sc.Tracks[0].Bars[i]
				if b.Num != oldNum || b.Den != oldDen {
					break
				}
			}
			for _, tr := range d.sc.Tracks {
				tr.Bars[i].Num, tr.Bars[i].Den = num, den
			}
		}
		return d.rebuildWith(tempos)
	})
}

func (d *Doc) SetTempo(bpm float64) error {
	if !(bpm >= 1 && bpm <= 1000) {
		return fmt.Errorf("a tempo has to be between 1 and 1000 BPM, not %g", bpm)
	}
	at := d.cur.Bar
	return d.mutate(func() error {
		tempos := d.tempoPerBar()
		old := tempos[at]
		next := score.USPerQuarter(bpm)
		for i := at; i < len(tempos) && tempos[i] == old; i++ {
			tempos[i] = next
		}
		return d.rebuildWith(tempos)
	})
}

func (d *Doc) TempoAtCursor() float64 {
	us := d.sc.Tempos.At(d.Bar().Start)
	if us <= 0 {
		return textfmt.DefaultBPM
	}
	return 60e6 / float64(us)
}

func (d *Doc) SetTitle(title string) error {
	if hasLineBreak(title) {
		return fmt.Errorf("a title has to be one line")
	}
	if hasComment(title) {
		return fmt.Errorf(`a title cannot contain "//", which starts a comment in the file`)
	}
	return d.mutate(func() error {
		d.sc.Title = title
		return nil
	})
}

func (d *Doc) SetTrackName(name string) error {
	if hasLineBreak(name) {
		return fmt.Errorf("a track name has to be one line")
	}
	if hasComment(name) {
		return fmt.Errorf(`a track name cannot contain "//", which starts a comment in the file`)
	}
	return d.mutate(func() error {
		d.Track().Name = name
		return nil
	})
}

func (d *Doc) SetCapo(fret int) error {
	if w := d.Track().Wind; w != nil {

		return fmt.Errorf("a %s has no capo", w.Name)
	}
	if fret < 0 || fret > textfmt.MaxFret {
		return fmt.Errorf("a capo has to be at fret 0-%d, not %d", textfmt.MaxFret, fret)
	}
	return d.mutate(func() error {
		tr := d.Track()
		tr.Capo = fret

		return checkPitches(tr)
	})
}

func (d *Doc) SetProgram(program int) error {
	if program < 0 || program > 127 {
		return fmt.Errorf("a General MIDI program is 0-127, not %d", program)
	}
	return d.mutate(func() error {
		d.Track().Program = program
		return nil
	})
}

func (d *Doc) SetRole(role score.TrackRole) error {
	return d.mutate(func() error {
		d.Track().Role = role
		return nil
	})
}

func (d *Doc) SetTuning(tuning score.Tuning) error {
	if w := d.Track().Wind; w != nil {

		return fmt.Errorf("a %s has no strings to tune", w.Name)
	}
	if len(tuning) == 0 {
		return fmt.Errorf("a track needs at least one string")
	}
	for i, key := range tuning {
		if key < 0 || key > 127 {
			return fmt.Errorf("string %d is tuned to MIDI key %d, outside 0-127", len(tuning)-i, key)
		}
	}
	return d.mutate(func() error {
		tr := d.Track()
		tr.Tuning = append(score.Tuning(nil), tuning...)

		return checkPitches(tr)
	})
}

func (d *Doc) AddTrack(name string, wind *score.WindInstrument) error {
	if hasLineBreak(name) {
		return fmt.Errorf("a track name has to be one line")
	}
	if hasComment(name) {
		return fmt.Errorf(`a track name cannot contain "//", which starts a comment in the file`)
	}
	err := d.mutate(func() error {
		tr := &score.Track{Name: name, Program: textfmt.DefaultProgram}
		if wind != nil {
			tr.Wind = wind
			tr.Program = wind.Program
		} else {
			tr.Tuning = append(score.Tuning(nil), score.StandardTuning...)
		}
		for bi, ref := range d.sc.Tracks[0].Bars {
			bar := tr.AppendBar(ref.Num, ref.Den)

			if err := refit(bar, -1); err != nil {
				return fmt.Errorf("bar %d of the new track: %w", bi+1, err)
			}
		}
		retick(tr)
		d.sc.Tracks = append(d.sc.Tracks, tr)
		return nil
	})
	if err != nil {
		return err
	}
	d.track = len(d.sc.Tracks) - 1
	d.cur.Bar, d.cur.Beat = 0, 0
	d.cur.Str = laneCount(d.Track())
	d.clampCursor()
	return nil
}

func (d *Doc) DeleteTrack() error {
	if len(d.sc.Tracks) <= 1 {
		return fmt.Errorf("a piece needs at least one track")
	}
	at := d.track
	return d.mutate(func() error {
		d.sc.Tracks = append(d.sc.Tracks[:at], d.sc.Tracks[at+1:]...)
		if d.track >= len(d.sc.Tracks) {
			d.track = len(d.sc.Tracks) - 1
		}
		return nil
	})
}

func (d *Doc) tempoPerBar() []int64 {
	bars := d.sc.Tracks[0].Bars
	out := make([]int64, len(bars))
	for i, b := range bars {
		out[i] = d.sc.Tempos.At(b.Start)
	}
	return out
}

func (d *Doc) rebuildWith(tempos []int64) error {
	for _, tr := range d.sc.Tracks {
		retick(tr)
	}
	bars := d.sc.Tracks[0].Bars
	if len(tempos) != len(bars) {
		return fmt.Errorf("edit: internal: %d tempos for %d bars", len(tempos), len(bars))
	}

	meters := make(score.MeterMap, 0, 4)
	tempoMap := make(score.TempoMap, 0, 4)
	for i, b := range bars {
		if i == 0 || b.Num != bars[i-1].Num || b.Den != bars[i-1].Den {
			meters = append(meters, score.Meter{Tick: b.Start, Num: b.Num, Den: b.Den})
		}
		if i == 0 || tempos[i] != tempos[i-1] {
			tempoMap = append(tempoMap, score.Tempo{Tick: b.Start, USPerQuarter: tempos[i]})
		}
	}
	d.sc.Meters, d.sc.Tempos = meters, tempoMap

	for ti, tr := range d.sc.Tracks {
		for bi, bar := range tr.Bars {
			if err := refit(bar, -1); err != nil {
				name := tr.Name
				if name == "" {
					name = fmt.Sprintf("track %d", ti+1)
				}
				return fmt.Errorf("bar %d of %s: %w", bi+1, name, err)
			}
		}
		retick(tr)
	}
	return nil
}

func checkPitches(tr *score.Track) error {
	for bi, bar := range tr.Bars {
		for _, bt := range bar.Beats {
			for _, n := range bt.Notes {
				if tr.Wind != nil {
					if n.String != 1 {
						return fmt.Errorf("bar %d has a note on string %d, and a %s has one lane, string 1", bi+1, n.String, tr.Wind.Name)
					}
					if n.Fret < 0 {
						return fmt.Errorf("bar %d has a note below the %s's lowest note", bi+1, tr.Wind.Name)
					}
					if k := tr.Pitch(n); k > 127 {
						return fmt.Errorf("bar %d: a note sounds MIDI key %d, above 127", bi+1, k)
					}
					continue
				}
				if n.String < 1 || n.String > len(tr.Tuning) {
					return fmt.Errorf("bar %d has a note on string %d, and this tuning has %d strings", bi+1, n.String, len(tr.Tuning))
				}
				if k := tr.Pitch(n); k < 0 || k > 127 {
					return fmt.Errorf("bar %d: string %d fret %d would sound MIDI key %d, outside 0-127", bi+1, n.String, n.Fret, k)
				}
			}
		}
	}
	return nil
}

func hasLineBreak(s string) bool { return strings.ContainsAny(s, "\r\n") }

func hasComment(s string) bool { return strings.Contains(s, "//") }
