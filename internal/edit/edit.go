package edit

import (
	"fmt"
	"strconv"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

const undoDepth = 200

type Cursor struct {
	Bar  int
	Beat int
	Str  int
}

type Doc struct {
	sc    *score.Score
	track int
	cur   Cursor
	dur   int64

	undo  []*score.Score
	redo  []*score.Score
	dirty bool
}

type NewOptions struct {
	Title    string
	BPM      float64
	Num, Den int
	Tuning   score.Tuning

	Wind *score.WindInstrument
	Bars int
}

func New(opts NewOptions) *Doc {

	if !(opts.BPM >= 1 && opts.BPM <= 1000) {
		opts.BPM = textfmt.DefaultBPM
	}
	if opts.Num < 1 || opts.Num > 64 {
		opts.Num = 4
	}
	switch opts.Den {
	case 1, 2, 4, 8, 16, 32:
	default:
		opts.Den = 4
	}
	if opts.Wind == nil && len(opts.Tuning) == 0 {
		opts.Tuning = append(score.Tuning(nil), score.StandardTuning...)
	}
	if opts.Bars < 1 {
		opts.Bars = 4
	}
	sc := &score.Score{
		Title:  opts.Title,
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(opts.BPM)}},
		Meters: score.MeterMap{{Tick: 0, Num: opts.Num, Den: opts.Den}},
	}
	tr := &score.Track{Program: textfmt.DefaultProgram}
	if opts.Wind != nil {
		tr.Wind = opts.Wind
		tr.Program = opts.Wind.Program
	} else {
		tr.Tuning = append(score.Tuning(nil), opts.Tuning...)
	}
	sc.Tracks = append(sc.Tracks, tr)
	for i := 0; i < opts.Bars; i++ {

		refit(tr.AppendBar(opts.Num, opts.Den), -1)
	}
	d := &Doc{sc: sc, dur: score.Quarter}

	d.cur = Cursor{Str: laneCount(tr)}
	return d
}

func laneCount(tr *score.Track) int {
	if tr.Wind != nil {
		return 1
	}
	return len(tr.Tuning)
}

func Open(sc *score.Score) (*Doc, error) {
	if sc == nil {
		return nil, fmt.Errorf("edit: no piece to open")
	}
	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("edit: the piece is not valid: %w", err)
	}
	c := cloneScore(sc)
	if len(c.Tracks) == 0 {
		c.Tracks = append(c.Tracks, &score.Track{
			Tuning:  append(score.Tuning(nil), score.StandardTuning...),
			Program: textfmt.DefaultProgram,
		})
	}
	d := &Doc{sc: c, dur: score.Quarter}
	if err := d.squareUp(); err != nil {
		return nil, err
	}
	d.cur = Cursor{Str: laneCount(d.Track())}
	d.clampCursor()
	return d, nil
}

func (d *Doc) Score() *score.Score { return d.sc }

func (d *Doc) Wind() *score.WindInstrument {
	for _, tr := range d.sc.Tracks {
		if tr.Wind != nil {
			return tr.Wind
		}
	}
	return nil
}

func (d *Doc) TrackIndex() int { return d.track }

func (d *Doc) Track() *score.Track { return d.sc.Tracks[d.track] }

func (d *Doc) Cursor() Cursor { return d.cur }

func (d *Doc) Bar() *score.Bar { return d.Track().Bars[d.cur.Bar] }

func (d *Doc) Beat() *score.Beat { return d.Bar().Beats[d.cur.Beat] }

func (d *Doc) Duration() int64 { return d.dur }

func (d *Doc) Dirty() bool { return d.dirty }

func (d *Doc) MarkSaved() { d.dirty = false }

func (d *Doc) CanUndo() bool { return len(d.undo) > 0 }
func (d *Doc) CanRedo() bool { return len(d.redo) > 0 }

func (d *Doc) NoteAt(str int) (score.Note, bool) {
	for _, n := range d.Beat().Notes {
		if n.String == str {
			return n, true
		}
	}
	return score.Note{}, false
}

func (d *Doc) mutate(fn func() error) error {
	before := cloneScore(d.sc)
	beforeCur := d.cur
	if err := fn(); err != nil {
		d.sc = before
		d.cur = beforeCur
		d.clampCursor()
		return err
	}
	d.undo = append(d.undo, before)
	if len(d.undo) > undoDepth {
		d.undo = d.undo[len(d.undo)-undoDepth:]
	}
	d.redo = nil
	d.dirty = true
	d.clampCursor()
	return nil
}

func (d *Doc) Undo() bool {
	if len(d.undo) == 0 {
		return false
	}
	last := d.undo[len(d.undo)-1]
	d.undo = d.undo[:len(d.undo)-1]
	d.redo = append(d.redo, d.sc)
	d.sc = last
	d.dirty = true
	d.clampCursor()
	return true
}

func (d *Doc) Redo() bool {
	if len(d.redo) == 0 {
		return false
	}
	next := d.redo[len(d.redo)-1]
	d.redo = d.redo[:len(d.redo)-1]
	d.undo = append(d.undo, d.sc)
	d.sc = next
	d.dirty = true
	d.clampCursor()
	return true
}

func (d *Doc) MoveString(delta int) {
	d.cur.Str += delta
	d.clampCursor()
}

func (d *Doc) MoveBeat(delta int) {
	tr := d.Track()
	for ; delta > 0; delta-- {
		if d.cur.Beat+1 < len(tr.Bars[d.cur.Bar].Beats) {
			d.cur.Beat++
			continue
		}
		if d.cur.Bar+1 >= len(tr.Bars) {
			break
		}
		d.cur.Bar, d.cur.Beat = d.cur.Bar+1, 0
	}
	for ; delta < 0; delta++ {
		if d.cur.Beat > 0 {
			d.cur.Beat--
			continue
		}
		if d.cur.Bar == 0 {
			break
		}
		d.cur.Bar--
		d.cur.Beat = len(tr.Bars[d.cur.Bar].Beats) - 1
	}
	d.clampCursor()
}

func (d *Doc) MoveBar(delta int) {
	d.cur.Bar += delta
	d.cur.Beat = 0
	d.clampCursor()
}

func (d *Doc) GoTo(c Cursor) {
	d.cur = c
	d.clampCursor()
}

func (d *Doc) GoToStart() { d.cur.Bar, d.cur.Beat = 0, 0; d.clampCursor() }
func (d *Doc) GoToEnd() {
	tr := d.Track()
	d.cur.Bar = len(tr.Bars) - 1
	d.cur.Beat = len(tr.Bars[d.cur.Bar].Beats) - 1
	d.clampCursor()
}

func (d *Doc) SelectTrack(i int) bool {
	if i < 0 || i >= len(d.sc.Tracks) {
		return false
	}
	d.track = i
	d.clampCursor()
	return true
}

func (d *Doc) clampCursor() {
	if d.track < 0 {
		d.track = 0
	}
	if d.track >= len(d.sc.Tracks) {
		d.track = len(d.sc.Tracks) - 1
	}
	tr := d.Track()
	if d.cur.Bar < 0 {
		d.cur.Bar = 0
	}
	if d.cur.Bar >= len(tr.Bars) {
		d.cur.Bar = len(tr.Bars) - 1
	}
	beats := tr.Bars[d.cur.Bar].Beats
	if d.cur.Beat < 0 {
		d.cur.Beat = 0
	}
	if d.cur.Beat >= len(beats) {
		d.cur.Beat = len(beats) - 1
	}
	if d.cur.Str < 1 {
		d.cur.Str = 1
	}
	if n := laneCount(tr); d.cur.Str > n {
		d.cur.Str = n
	}
}

func (d *Doc) squareUp() error {

	bars := 0
	for _, tr := range d.sc.Tracks {
		if n := len(tr.Bars); n > bars {
			bars = n
		}
	}
	if bars == 0 {
		bars = 1
	}
	for _, tr := range d.sc.Tracks {
		for len(tr.Bars) < bars {
			start := int64(0)
			if n := len(tr.Bars); n > 0 {
				last := tr.Bars[n-1]
				start = last.Start + last.Len()
			}
			m := d.sc.Meters.At(start)

			tr.AppendBar(m.Num, m.Den)
		}
	}
	for ti, tr := range d.sc.Tracks {

		if tr.Wind == nil && len(tr.Tuning) == 0 {
			tr.Tuning = append(score.Tuning(nil), score.StandardTuning...)
		}
		retick(tr)
		for bi, bar := range tr.Bars {
			if err := refit(bar, -1); err != nil {
				return fmt.Errorf("edit: track %d bar %d: %w", ti+1, bi+1, err)
			}
		}
		retick(tr)
	}
	return d.sc.Validate()
}

func refit(bar *score.Bar, keep int) error {
	end := len(bar.Beats)
	for end > keep+1 && end > 0 && len(bar.Beats[end-1].Notes) == 0 {
		end--
	}
	bar.Beats = bar.Beats[:end]
	var filled int64
	for _, bt := range bar.Beats {
		filled += bt.Dur
	}
	if filled > bar.Len() {
		return fmt.Errorf("the notes add up to %s beats, and a %d/%d bar holds %d",
			beatsText(filled, bar), bar.Num, bar.Den, bar.Num)
	}
	rests, ok := restsFor(bar.Len() - filled)
	if !ok {
		return fmt.Errorf("%s of a beat is left over, and no combination of note lengths fills it",
			beatsText(bar.Len()-filled, bar))
	}
	for _, r := range rests {
		bar.AddBeat(r)
	}
	return nil
}

func beatsText(ticks int64, bar *score.Bar) string {
	beat := 4 * score.PPQ / int64(bar.Den)
	if beat <= 0 {
		return "0"
	}
	v := float64(ticks) / float64(beat)
	return strconv.FormatFloat(v, 'g', 3, 64)
}

func retick(tr *score.Track) {
	var t int64
	for _, bar := range tr.Bars {
		bar.Start = t
		bt := t
		for _, beat := range bar.Beats {
			beat.Start = bt
			bt += beat.Dur
		}
		t += bar.Len()
	}
}

func cloneScore(sc *score.Score) *score.Score {
	out := &score.Score{
		Title:  sc.Title,
		Tempos: append(score.TempoMap(nil), sc.Tempos...),
		Meters: append(score.MeterMap(nil), sc.Meters...),
		Tracks: make([]*score.Track, len(sc.Tracks)),
	}
	for i, tr := range sc.Tracks {
		out.Tracks[i] = cloneTrack(tr)
	}
	return out
}

func cloneTrack(tr *score.Track) *score.Track {
	c := &score.Track{
		Name:    tr.Name,
		Tuning:  append(score.Tuning(nil), tr.Tuning...),
		Capo:    tr.Capo,
		Program: tr.Program,
		Wind:    tr.Wind,
		Role:    tr.Role,
		Bars:    make([]*score.Bar, len(tr.Bars)),
	}
	for i, bar := range tr.Bars {
		b := &score.Bar{Start: bar.Start, Num: bar.Num, Den: bar.Den, Beats: make([]*score.Beat, len(bar.Beats))}
		for j, beat := range bar.Beats {
			b.Beats[j] = &score.Beat{
				Start: beat.Start,
				Dur:   beat.Dur,
				Notes: append([]score.Note(nil), beat.Notes...),
			}
		}
		c.Bars[i] = b
	}
	return c
}
