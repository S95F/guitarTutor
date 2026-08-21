// Package edit is the model behind musicTutor's tablature editor: a
// score being written by hand, a cursor pointing into it, and the
// operations that change it. It knows nothing about Ebitengine — the
// screen in internal/ui is a projection of what is here, and every rule
// about what an edit is allowed to do is testable without a window.
//
// Two invariants hold after every operation, because the format the
// editor saves to insists on both and a piece that cannot be saved is not
// worth having in memory:
//
//   - Every bar is exactly full. Beats that do not reach the end of the
//     bar are followed by rests that do; an edit that would overfill a bar
//     is refused with a message rather than silently spilling into the
//     next one.
//   - Every track shares one bar grid. Bars are added, removed and
//     re-metered across the whole piece at once, so the barlines line up
//     between the lead and the rhythm part the way they do on paper — and
//     so a time-signature change always lands on a barline of every track,
//     which is what .gtab requires of one.
//
// Refusing an edit is a first-class outcome here, not an error path: a
// half note does not fit in the two beats left in a bar, and saying so is
// more useful than any of the things an editor could do about it.
package edit

import (
	"fmt"
	"strconv"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// undoDepth is how many operations can be taken back. Snapshots are whole
// scores, but a score is a few hundred kilobytes at the very worst, and an
// editor that forgets what you did five minutes ago is the sort of thing
// that loses an evening's work.
const undoDepth = 200

// A Cursor is where the next edit lands: a beat within a bar of the track
// being edited, on one string.
type Cursor struct {
	Bar  int // index into the edited track's bars
	Beat int // index into that bar's beats
	Str  int // string number, 1 = highest-pitched
}

// A Doc is one piece being edited.
type Doc struct {
	sc    *score.Score
	track int    // index of the track being edited
	cur   Cursor //
	dur   int64  // the length new beats take, in ticks

	undo  []*score.Score
	redo  []*score.Score
	dirty bool
}

// NewOptions describes a piece to start from scratch.
type NewOptions struct {
	Title    string
	BPM      float64
	Num, Den int
	Tuning   score.Tuning
	Bars     int
}

// New starts a fresh piece: one track of empty bars in the requested
// meter, ready to be typed into. Zero fields take the format's own
// defaults, so New(NewOptions{}) is a valid four-bar 4/4 piece in standard
// tuning at 120 BPM.
func New(opts NewOptions) *Doc {
	if opts.BPM < 1 || opts.BPM > 1000 {
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
	if len(opts.Tuning) == 0 {
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
	tr := &score.Track{
		Tuning:  append(score.Tuning(nil), opts.Tuning...),
		Program: textfmt.DefaultProgram,
	}
	sc.Tracks = append(sc.Tracks, tr)
	for i := 0; i < opts.Bars; i++ {
		bar := tr.AppendBar(opts.Num, opts.Den)
		fillBar(bar)
	}
	d := &Doc{sc: sc, dur: score.Quarter}
	d.cur = Cursor{Str: len(tr.Tuning)} // the low string, where riffs start
	return d
}

// Open takes an existing score into the editor. The score is copied, so
// the caller's own is never edited underneath it, and then brought up to
// the invariants: shorter tracks are padded with rests so every track
// shares one bar grid, and any bar that does not fill its meter is filled.
// An imported piece that was never written in this format is therefore
// editable — and saveable — from the moment it opens.
func Open(sc *score.Score) (*Doc, error) {
	if sc == nil {
		return nil, fmt.Errorf("edit: no piece to open")
	}
	// The grid is a strings-by-frets surface and a wind part has neither;
	// refusing here, by name, is what routes a wind piece to the text
	// view (the caller's fallback) with a message that says so instead of
	// a complaint about a zero-string tuning.
	for _, tr := range sc.Tracks {
		if tr.Wind != nil {
			return nil, fmt.Errorf("edit: the notation editor cannot edit a %s part yet — the text view (F2) is where wind parts are written", tr.Wind.Name)
		}
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
	d.cur = Cursor{Str: len(d.Track().Tuning)}
	d.clampCursor()
	return d, nil
}

// --- reading ---------------------------------------------------------

// Score is the piece as it stands. The caller may read it — to render the
// tab, or to hand it to the engine for an audition — but must not modify
// it: every change goes through this package so the invariants and the
// undo history stay true.
func (d *Doc) Score() *score.Score { return d.sc }

// TrackIndex is which track the cursor is editing.
func (d *Doc) TrackIndex() int { return d.track }

// Track is the track the cursor is editing.
func (d *Doc) Track() *score.Track { return d.sc.Tracks[d.track] }

// Cursor is where the next edit lands.
func (d *Doc) Cursor() Cursor { return d.cur }

// Bar is the bar the cursor is in.
func (d *Doc) Bar() *score.Bar { return d.Track().Bars[d.cur.Bar] }

// Beat is the beat the cursor is on.
func (d *Doc) Beat() *score.Beat { return d.Bar().Beats[d.cur.Beat] }

// Duration is the length new beats take, in ticks.
func (d *Doc) Duration() int64 { return d.dur }

// Dirty reports whether there are changes since the last MarkSaved.
func (d *Doc) Dirty() bool { return d.dirty }

// MarkSaved records the current state as the saved one.
func (d *Doc) MarkSaved() { d.dirty = false }

// CanUndo and CanRedo report whether the history has anywhere to go, so a
// view can grey out what would do nothing.
func (d *Doc) CanUndo() bool { return len(d.undo) > 0 }
func (d *Doc) CanRedo() bool { return len(d.redo) > 0 }

// NoteAt returns the note on a string of the cursor's beat, and whether
// there is one.
func (d *Doc) NoteAt(str int) (score.Note, bool) {
	for _, n := range d.Beat().Notes {
		if n.String == str {
			return n, true
		}
	}
	return score.Note{}, false
}

// --- history ---------------------------------------------------------

// mutate runs one edit under the history: the piece is snapshotted first,
// and a refused edit is rolled back whole rather than left half-applied.
// Every operation that can change the score goes through here, which is
// what makes "refused" mean the same thing everywhere — nothing happened.
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

// Undo takes back the last edit, reporting whether there was one.
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

// Redo re-applies the last undone edit, reporting whether there was one.
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

// --- movement --------------------------------------------------------
//
// Moving the cursor is not an edit: it changes nothing, takes no undo
// slot, and can never fail. The methods clamp instead of wrapping, except
// where crossing a barline is the obvious continuation of the gesture.

// MoveString moves the cursor by delta strings, positive towards the low
// (bass) strings, matching the way the tab is drawn.
func (d *Doc) MoveString(delta int) {
	d.cur.Str += delta
	d.clampCursor()
}

// MoveBeat moves by delta beats, crossing barlines: at the end of a bar it
// continues into the next one, and at the end of the piece it stops.
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

// MoveBar moves by delta bars, landing on the first beat.
func (d *Doc) MoveBar(delta int) {
	d.cur.Bar += delta
	d.cur.Beat = 0
	d.clampCursor()
}

// GoTo puts the cursor at a specific place, clamped into the piece. The
// view uses it for clicks on the tab.
func (d *Doc) GoTo(c Cursor) {
	d.cur = c
	d.clampCursor()
}

// GoToStart and GoToEnd jump to the first and last beat of the piece.
func (d *Doc) GoToStart() { d.cur.Bar, d.cur.Beat = 0, 0; d.clampCursor() }
func (d *Doc) GoToEnd() {
	tr := d.Track()
	d.cur.Bar = len(tr.Bars) - 1
	d.cur.Beat = len(tr.Bars[d.cur.Bar].Beats) - 1
	d.clampCursor()
}

// SelectTrack moves the cursor to another track, reporting whether the
// index named one.
func (d *Doc) SelectTrack(i int) bool {
	if i < 0 || i >= len(d.sc.Tracks) {
		return false
	}
	d.track = i
	d.clampCursor()
	return true
}

// clampCursor brings the cursor back inside the piece after anything that
// may have moved the ground under it. Every bar has at least one beat and
// the piece has at least one bar, so there is always somewhere to land.
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
	if n := len(tr.Tuning); d.cur.Str > n {
		d.cur.Str = n
	}
}

// --- invariants ------------------------------------------------------

// squareUp brings a score up to the two invariants: one shared bar grid,
// and every bar exactly full. It is what lets an imported piece — ragged
// track lengths, bars the importer left short — be edited at all.
func (d *Doc) squareUp() error {
	// The grid is as long as the longest track, with each bar's meter
	// taken from the map so a track that stops early still gets the right
	// meters for the bars it gains.
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
			fillBar(tr.AppendBar(m.Num, m.Den))
		}
	}
	for ti, tr := range d.sc.Tracks {
		if len(tr.Tuning) == 0 {
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

// refit restores a bar's "exactly full" invariant: trailing rests are
// padding, not content, so they are dropped and re-laid to reach the end
// of the bar. A bar whose real content overflows the meter is reported
// rather than truncated — the notes are the user's, and the operation that
// caused it is rolled back around this.
//
// keep is the index of a beat that counts as content whatever is in it, or
// -1 for none. It exists because "a trailing rest is padding" is otherwise
// true of the rest a user has just deliberately made: inserting a beat at
// the end of a bar, or turning the last note into a rest, would be undone
// by the very call meant to tidy up after it — silently, since re-padding
// leaves the bar full either way. The beat under the cursor is what the
// user is pointing at, so that is what is kept.
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

// beatsText renders a span of ticks as beats of the bar's own meter,
// which is the unit the person reading the message is counting in. Ticks
// are the model's unit and have no business in anything a user reads.
func beatsText(ticks int64, bar *score.Bar) string {
	beat := 4 * score.PPQ / int64(bar.Den)
	if beat <= 0 {
		return "0"
	}
	v := float64(ticks) / float64(beat)
	return strconv.FormatFloat(v, 'g', 3, 64)
}

// fillBar makes an empty bar a bar of rests.
func fillBar(bar *score.Bar) {
	bar.Beats = bar.Beats[:0]
	if err := refit(bar, -1); err != nil {
		// Unreachable: an empty bar's remainder is its own length, and
		// every meter the format allows is a whole number of 32nd notes.
		panic("edit: cannot fill an empty bar: " + err.Error())
	}
}

// retick recomputes every bar's and beat's start tick from the durations
// in front of it. Ticks are denormalized onto both (see internal/score),
// so anything that changes a length has to put them back.
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

// --- copying ---------------------------------------------------------

// cloneScore deep-copies a score. The undo history holds whole snapshots,
// so this has to reach every pointer: a shallow copy would leave the
// history aliasing the live piece and quietly rewriting its own past.
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

// cloneTrack deep-copies one track.
func cloneTrack(tr *score.Track) *score.Track {
	c := &score.Track{
		Name:    tr.Name,
		Tuning:  append(score.Tuning(nil), tr.Tuning...),
		Capo:    tr.Capo,
		Program: tr.Program,
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
