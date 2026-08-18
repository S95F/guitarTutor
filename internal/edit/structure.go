package edit

// Everything that changes the shape of the piece rather than the notes in
// it: bars, the meter and tempo maps, and the tracks themselves.
//
// The maps are the fiddly part. internal/score stores tempo and meter
// changes at TICKS, and inserting a bar moves every tick after it — so a
// change written "at bar 5" would slide onto bar 4 the moment a bar was
// added in front of it. Every structural operation here therefore works in
// BARS: the maps are expanded to one value per bar, the bars are changed,
// and the maps are rebuilt by compressing the per-bar values back into
// change points. Nothing has to reason about which ticks moved.

import (
	"fmt"

	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
)

// BarCount is how many bars the piece has. Every track has the same
// number — that is the shared grid.
func (d *Doc) BarCount() int { return len(d.sc.Tracks[0].Bars) }

// AppendBar adds an empty bar to the end of the piece, in the meter the
// last bar is in, and moves the cursor into it.
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

// InsertBar puts an empty bar in front of the cursor's bar, across every
// track.
func (d *Doc) InsertBar() error {
	at := d.cur.Bar
	return d.mutate(func() error {
		return d.rebuildWith(d.insertBarAt(at))
	})
}

// DeleteBar removes the cursor's bar from every track. The last bar of a
// piece is refused: an editor with nowhere to put the cursor is not a
// state worth being able to reach.
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

// insertBarAt adds an empty bar at index at in every track and returns the
// per-bar tempos for the piece that results. The new bar takes the meter
// and tempo of the bar it displaces — or of the bar before it, when
// appending past the end, where there is nothing to displace. Caller is
// inside mutate and passes the result to rebuildWith.
func (d *Doc) insertBarAt(at int) []int64 {
	tempos := d.tempoPerBar()
	// The bar whose meter and tempo the new one copies.
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
		fillBar(bar)
		tr.Bars = append(tr.Bars, nil)
		copy(tr.Bars[at+1:], tr.Bars[at:])
		tr.Bars[at] = bar
	}
	return tempos
}

// SetMeter changes the time signature from the cursor's bar up to (but not
// including) the next bar that already changes it, across every track —
// which is what a time-signature change means on paper. Bars that would
// become too short for the notes already in them are reported, and nothing
// changes.
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
		// Read the tempos out before the bars change lengths: per-bar
		// tempos are looked up by tick, and the ticks are about to move.
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

// SetTempo changes the tempo from the cursor's bar up to the next bar that
// already changes it.
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

// TempoAtCursor is the tempo in force where the cursor is, in BPM.
func (d *Doc) TempoAtCursor() float64 {
	us := d.sc.Tempos.At(d.Bar().Start)
	if us <= 0 {
		return textfmt.DefaultBPM
	}
	return 60e6 / float64(us)
}

// --- the piece and its tracks ----------------------------------------

// SetTitle names the piece.
func (d *Doc) SetTitle(title string) error {
	if hasLineBreak(title) {
		return fmt.Errorf("a title has to be one line")
	}
	return d.mutate(func() error {
		d.sc.Title = title
		return nil
	})
}

// SetTrackName names the track being edited. Only the first track may be
// nameless — every later one needs a name to be written down at all.
func (d *Doc) SetTrackName(name string) error {
	if hasLineBreak(name) {
		return fmt.Errorf("a track name has to be one line")
	}
	return d.mutate(func() error {
		d.Track().Name = name
		return nil
	})
}

// SetCapo moves the capo on the track being edited.
func (d *Doc) SetCapo(fret int) error {
	if fret < 0 || fret > textfmt.MaxFret {
		return fmt.Errorf("a capo has to be at fret 0-%d, not %d", textfmt.MaxFret, fret)
	}
	return d.mutate(func() error {
		tr := d.Track()
		was := tr.Capo
		tr.Capo = fret
		if err := checkPitches(tr); err != nil {
			tr.Capo = was
			return err
		}
		return nil
	})
}

// SetProgram sets the track's General MIDI program hint, which is what a
// SoundFont voice picks its instrument from.
func (d *Doc) SetProgram(program int) error {
	if program < 0 || program > 127 {
		return fmt.Errorf("a General MIDI program is 0-127, not %d", program)
	}
	return d.mutate(func() error {
		d.Track().Program = program
		return nil
	})
}

// SetRole marks the track as the part being practised or as accompaniment.
func (d *Doc) SetRole(role score.TrackRole) error {
	return d.mutate(func() error {
		d.Track().Role = role
		return nil
	})
}

// SetTuning re-tunes the track being edited. Tuning is given
// highest-string-first, the way the model stores it. A tuning that would
// put an existing note out of MIDI range, or take away a string a note is
// written on, is refused — the notes are the user's work and a re-tune is
// not a licence to lose them.
func (d *Doc) SetTuning(tuning score.Tuning) error {
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
		was := tr.Tuning
		tr.Tuning = append(score.Tuning(nil), tuning...)
		if err := checkPitches(tr); err != nil {
			tr.Tuning = was
			return err
		}
		return nil
	})
}

// AddTrack appends a track of empty bars on the same grid and moves the
// cursor to it.
func (d *Doc) AddTrack(name string) error {
	if hasLineBreak(name) {
		return fmt.Errorf("a track name has to be one line")
	}
	err := d.mutate(func() error {
		tr := &score.Track{
			Name:    name,
			Tuning:  append(score.Tuning(nil), score.StandardTuning...),
			Program: textfmt.DefaultProgram,
		}
		for _, ref := range d.sc.Tracks[0].Bars {
			fillBar(tr.AppendBar(ref.Num, ref.Den))
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
	d.cur.Str = len(d.Track().Tuning)
	d.clampCursor()
	return nil
}

// DeleteTrack removes the track being edited. The last one is refused.
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

// --- map rebuilding --------------------------------------------------

// tempoPerBar expands the tempo map to one value per bar: the tempo in
// force at that bar. This is the form structural edits work in, because a
// bar index survives an insertion in front of it and a tick does not.
func (d *Doc) tempoPerBar() []int64 {
	bars := d.sc.Tracks[0].Bars
	out := make([]int64, len(bars))
	for i, b := range bars {
		out[i] = d.sc.Tempos.At(b.Start)
	}
	return out
}

// rebuildWith puts the ticks back across every track and compresses the
// per-bar tempos and the bars' own meters into the two maps, then refits
// every bar whose meter changed. Bars that no longer fit their meter are
// reported, and the operation around this rolls the piece back.
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

// checkPitches reports the first note of a track that its current tuning
// and capo cannot sound.
func checkPitches(tr *score.Track) error {
	for bi, bar := range tr.Bars {
		for _, bt := range bar.Beats {
			for _, n := range bt.Notes {
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

// hasLineBreak reports whether text would break a one-line directive.
func hasLineBreak(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			return true
		}
	}
	return false
}
