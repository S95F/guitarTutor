package textfmt

// Serialising a score back to .gtab source — the other half of the
// authoring loop the parser opens. The editor (internal/edit) keeps a
// score in memory and saves it through here, and the editor's text view
// shows exactly what this produces, so anything Format writes has to be
// something Parse reads back as the same score. write_test.go pins that
// round trip on every fixture in the tree.
//
// The format is smaller than the model, and this is where that shows.
// Three things a *score.Score can hold cannot be written down:
//
//   - a beat whose duration is not a plain, dotted, or triplet note value
//     (an importer that quantised to something else, most often);
//   - a tempo change that does not land on a barline (SMF places them
//     wherever it likes);
//   - two notes on one string in the same beat.
//
// Each is reported as an error naming the bar, rather than written out as
// something Parse would reject or — worse — read back as a different
// piece. A score that came from the editor can never contain any of them.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/S95F/guitarTutor/internal/score"
)

// Format renders sc as .gtab source. The returned bytes parse back to a
// score equal to sc (see the package comment for the three shapes that
// are refused instead).
func Format(sc *score.Score) ([]byte, error) {
	if sc == nil {
		return nil, fmt.Errorf("gtab: nil score")
	}
	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("gtab: score is not valid: %w", err)
	}
	w := &writer{sc: sc}
	if err := w.run(); err != nil {
		return nil, err
	}
	return []byte(w.b.String()), nil
}

// WriteFile renders sc and writes it to path, replacing whatever was
// there. The write goes to a temporary file in the same directory and is
// renamed over the target, so an interrupted save cannot leave a piece
// half-written — losing the previous version of a piece to a crash
// mid-save is exactly the accident a practice log is meant to survive.
func WriteFile(path string, sc *score.Score) error {
	src, err := Format(sc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gtab-*.tmp")
	if err != nil {
		return fmt.Errorf("gtab: %w", err)
	}
	name := tmp.Name()
	// Any failure from here on removes the temporary file; the target is
	// only touched by the rename, which is the last thing that happens.
	defer os.Remove(name)
	if _, err := tmp.Write(src); err != nil {
		tmp.Close()
		return fmt.Errorf("gtab: writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gtab: writing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("gtab: writing %s: %w", path, err)
	}
	return nil
}

// A writer renders one score. curNum/curDen and sticky mirror the state
// the PARSER will be in at the same point in the output — the meter it
// believes is in effect, and the duration a bare beat inherits — which is
// what lets both be left out of the text whenever they have not changed.
type writer struct {
	sc  *score.Score
	b   strings.Builder
	ctx string // the bar being written, for error messages
	tr  *score.Track // the track being written, for its instrument

	sticky int64

	// tempoDone and meterDone mark map entries already written. Both maps
	// are piece-wide but the directives that build them live in one track's
	// bar stream, so an entry is written by the FIRST track that has a bar
	// starting on its tick and skipped by every later one — restating it
	// would be harmless (the parser replaces the entry at that tick) but it
	// is noise in a format whose whole point is being readable. These are
	// also what notices, at the end, that an entry found no barline
	// anywhere and cannot be expressed at all.
	tempoDone []bool
	meterDone []bool
}

func (w *writer) run() error {
	if err := w.header(); err != nil {
		return err
	}
	w.tempoDone = make([]bool, len(w.sc.Tempos))
	w.meterDone = make([]bool, len(w.sc.Meters))
	// The opening tempo and meter are in the header.
	w.tempoDone[0], w.meterDone[0] = true, true
	for i, tr := range w.sc.Tracks {
		if err := w.track(i, tr); err != nil {
			return err
		}
	}
	for i, t := range w.sc.Tempos {
		if !w.tempoDone[i] {
			return fmt.Errorf("gtab: the tempo change at tick %d does not fall on a barline of any track; .gtab can only change tempo between bars", t.Tick)
		}
	}
	for i, m := range w.sc.Meters {
		if !w.meterDone[i] {
			return fmt.Errorf("gtab: the %d/%d time signature at tick %d does not fall on a barline of any track; .gtab can only change the meter between bars", m.Num, m.Den, m.Tick)
		}
	}
	return nil
}

// header writes the piece-wide directives: the title and the tempo and
// meter in effect at tick 0.
func (w *writer) header() error {
	if title := strings.TrimSpace(w.sc.Title); title != "" {
		if strings.ContainsAny(title, "\r\n") {
			return fmt.Errorf("gtab: the title contains a line break, which \\title cannot hold")
		}
		fmt.Fprintf(&w.b, "\\title %s\n", title)
	}
	bpm, err := bpmString(w.sc.Tempos.At(0))
	if err != nil {
		return err
	}
	m := w.sc.Meters.At(0)
	fmt.Fprintf(&w.b, "\\tempo %s\n", bpm)
	fmt.Fprintf(&w.b, "\\time %d/%d\n", m.Num, m.Den)
	return nil
}

// track writes one track: its header directives, then its bars.
func (w *writer) track(i int, tr *score.Track) error {
	w.tr = tr
	w.b.WriteString("\n")
	// Where the track's own directives begin, so the blank line that
	// separates them from its bars is only written when there are any: an
	// anonymous single-track piece has no header block at all, and an empty
	// one would read as a missing directive rather than as breathing room.
	headStart := w.b.Len()
	// The first track may stay anonymous — that is the implicit track the
	// parser materialises for a piece whose bars precede any \track. Every
	// later one needs a name to exist at all, so an unnamed one is given a
	// positional one rather than being silently merged into its
	// predecessor.
	name := tr.Name
	if i > 0 && strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("Track %d", i+1)
	}
	if strings.TrimSpace(name) != "" {
		if strings.ContainsAny(name, "\r\n") {
			return fmt.Errorf("gtab: track %d: the name contains a line break, which \\track cannot hold", i+1)
		}
		fmt.Fprintf(&w.b, "\\track %s\n", name)
	}
	if tr.Wind != nil {
		// A wind track's pitch reference is its instrument; Validate has
		// already refused one carrying a tuning or a capo.
		fmt.Fprintf(&w.b, "\\instrument %s\n", tr.Wind.Name)
	} else if !sameTuning(tr.Tuning, score.StandardTuning) {
		// The file lists open strings low to high; the model stores the
		// highest-pitched string first.
		names := make([]string, len(tr.Tuning))
		for j, key := range tr.Tuning {
			if key < 0 || key > 127 {
				return fmt.Errorf("gtab: track %d: open string %d is MIDI key %d, outside 0-127", i+1, len(tr.Tuning)-j, key)
			}
			names[len(tr.Tuning)-1-j] = pitchName(key)
		}
		fmt.Fprintf(&w.b, "\\tuning %s\n", strings.Join(names, " "))
	}
	if tr.Capo != 0 {
		if tr.Capo < 0 || tr.Capo > maxFret {
			return fmt.Errorf("gtab: track %d: capo %d is outside 0-%d", i+1, tr.Capo, maxFret)
		}
		fmt.Fprintf(&w.b, "\\capo %d\n", tr.Capo)
	}
	// The program a track's instrument implies is the one worth leaving
	// out: for a fretted track the format's steel-string default, for a
	// wind track the instrument's own.
	trackDefaultProgram := defaultProgram
	if tr.Wind != nil {
		trackDefaultProgram = tr.Wind.Program
	}
	if tr.Program != trackDefaultProgram {
		if tr.Program < 0 || tr.Program > 127 {
			return fmt.Errorf("gtab: track %d: program %d is outside 0-127", i+1, tr.Program)
		}
		fmt.Fprintf(&w.b, "\\program %d\n", tr.Program)
	}
	if tr.Role == score.RoleBacking {
		w.b.WriteString("\\backing\n")
	}

	if w.b.Len() > headStart {
		w.b.WriteString("\n")
	}
	// A \track directive resets the parser's sticky duration to a quarter;
	// so does the start of the implicit first track.
	w.sticky = score.Quarter
	for bi, bar := range tr.Bars {
		w.ctx = fmt.Sprintf("track %d bar %d", i+1, bi+1)
		if err := w.betweenBars(bar); err != nil {
			return err
		}
		if err := w.bar(bar); err != nil {
			return err
		}
	}
	return nil
}

// betweenBars writes the map changes anchored exactly at a bar's start.
// Both directives are legal only between bars, which is where this is
// called from.
//
// The meter written is the MAP's, not the bar's own denormalized copy,
// because the map is what the parser reads a bar's meter out of. When the
// two disagree the score is internally inconsistent — the bar would come
// back a different length than it validated as — so that is reported here
// rather than written out as a piece that changes on the way to disk.
func (w *writer) betweenBars(bar *score.Bar) error {
	if m := w.sc.Meters.At(bar.Start); m.Num != bar.Num || m.Den != bar.Den {
		return fmt.Errorf("gtab: %s is written as %d/%d but the meter map says %d/%d at tick %d; the two have to agree before the piece can be written",
			w.ctx, bar.Num, bar.Den, m.Num, m.Den, bar.Start)
	}
	for i, m := range w.sc.Meters {
		if m.Tick != bar.Start {
			continue
		}
		if !w.meterDone[i] {
			fmt.Fprintf(&w.b, "\\time %d/%d\n", m.Num, m.Den)
			w.meterDone[i] = true
		}
	}
	for i, t := range w.sc.Tempos {
		if t.Tick != bar.Start {
			continue
		}
		if !w.tempoDone[i] {
			bpm, err := bpmString(t.USPerQuarter)
			if err != nil {
				return err
			}
			fmt.Fprintf(&w.b, "\\tempo %s\n", bpm)
			w.tempoDone[i] = true
		}
	}
	return nil
}

// bar writes one bar's beats and the barline that closes it.
func (w *writer) bar(bar *score.Bar) error {
	for i, beat := range bar.Beats {
		if i > 0 {
			w.b.WriteString(" ")
		}
		tok, err := w.beat(beat)
		if err != nil {
			return err
		}
		w.b.WriteString(tok)
	}
	w.b.WriteString(" |\n")
	return nil
}

// beat renders one beat: a rest, a note, or a chord, with the duration
// left out whenever it is the one the previous beat already set.
func (w *writer) beat(bt *score.Beat) (string, error) {
	dur, err := w.durSuffix(bt.Dur)
	if err != nil {
		return "", err
	}
	switch len(bt.Notes) {
	case 0:
		return "r" + dur, nil
	case 1:
		n := bt.Notes[0]
		s, err := w.noteToken(n)
		if err != nil {
			return "", err
		}
		return s + dur + techString(n.Tech, w.tr.Wind != nil), nil
	}

	// A chord every note of which is tied is written with one "~" in front
	// of the whole thing, which is how a guitarist reads it — the chord is
	// still ringing — rather than as a tie marker on each note.
	allTied := true
	for _, n := range bt.Notes {
		if !n.Tied {
			allTied = false
			break
		}
	}

	seen := map[int]bool{}
	parts := make([]string, 0, len(bt.Notes))
	for _, n := range bt.Notes {
		if seen[n.String] {
			return "", fmt.Errorf("gtab: %s: string %d is played twice in one beat, which .gtab cannot write", w.ctx, n.String)
		}
		seen[n.String] = true
		if allTied {
			n.Tied = false // carried by the chord's own prefix
		}
		s, err := w.noteToken(n)
		if err != nil {
			return "", err
		}
		parts = append(parts, s+techString(n.Tech, false))
	}
	prefix := "("
	if allTied {
		prefix = "~("
	}
	// The duration attaches directly to the ")", with no space: that is
	// how the parser knows the suffix belongs to the chord.
	return prefix + strings.Join(parts, " ") + ")" + dur, nil
}

// noteToken renders a note's tie marker and pitch — "fret.string" on a
// fretted track, a written pitch name on a wind track — everything but
// its duration and techniques, which sit in different places for a chord
// than for a lone note.
func (w *writer) noteToken(n score.Note) (string, error) {
	if wind := w.tr.Wind; wind != nil {
		written := wind.Written(w.tr.Pitch(n))
		if written < 0 || written > 127 {
			return "", fmt.Errorf("gtab: %s: the note's written pitch is MIDI key %d, which no note name can hold", w.ctx, written)
		}
		s := pitchName(written)
		if n.Tied {
			s = "~" + s
		}
		return s, nil
	}
	if n.Fret < 0 || n.Fret > maxFret {
		return "", fmt.Errorf("gtab: %s: fret %d is outside 0-%d", w.ctx, n.Fret, maxFret)
	}
	s := strconv.Itoa(n.Fret) + "." + strconv.Itoa(n.String)
	if n.Tied {
		s = "~" + s
	}
	return s, nil
}

// techString renders a note's techniques in the instrument family's own
// letter order, so the same set of bits always writes the same way. Both
// tables mirror the parser's techFor alphabets — the four lists must stay
// in step.
func techString(t score.Technique, wind bool) string {
	table := []struct {
		bit  score.Technique
		char byte
	}{
		{score.TechHammer, 'h'},
		{score.TechPull, 'p'},
		{score.TechSlide, 's'},
		{score.TechBend, 'b'},
		{score.TechVibrato, 'v'},
		{score.TechDead, 'x'},
	}
	if wind {
		table = []struct {
			bit  score.Technique
			char byte
		}{
			{score.TechSlur, 'l'},
			{score.TechSlide, 's'},
			{score.TechBend, 'b'},
			{score.TechVibrato, 'v'},
		}
	}
	var b strings.Builder
	for _, e := range table {
		if t&e.bit != 0 {
			b.WriteByte(e.char)
		}
	}
	return b.String()
}

// durSuffix returns the ".N" suffix a beat needs, or "" when the sticky
// duration already carries it, and updates the sticky duration to match
// what the parser will hold after reading this beat.
func (w *writer) durSuffix(dur int64) (string, error) {
	if dur == w.sticky {
		return "", nil
	}
	name, ok := durationName(dur)
	if !ok {
		return "", fmt.Errorf("gtab: %s: a beat of %d ticks is not a note value .gtab can write (it holds plain, dotted and triplet 1 2 4 8 16 32)", w.ctx, dur)
	}
	w.sticky = dur
	return "." + name, nil
}

// durationNames maps every length the format can express to its spelling.
// It is built from the same constants the parser converts back, so the
// two cannot drift.
var durationNames = func() map[int64]string {
	out := map[int64]string{}
	for _, d := range []struct {
		base int64
		name string
	}{
		{score.Whole, "1"},
		{score.Half, "2"},
		{score.Quarter, "4"},
		{score.Eighth, "8"},
		{score.Sixteenth, "16"},
		{score.ThirtySec, "32"},
	} {
		out[d.base] = d.name
		out[score.Dotted(d.base)] = d.name + "."
		out[score.Triplet(d.base)] = d.name + "t"
	}
	return out
}()

// durationName spells a length in ticks, reporting whether the format can
// express it at all.
func durationName(dur int64) (string, bool) {
	s, ok := durationNames[dur]
	return s, ok
}

// DurationNames returns every beat length .gtab can write, shortest
// first, paired with its spelling. The editor builds its duration picker
// from this, so the palette it offers and the file it saves cannot
// disagree about what exists.
func DurationNames() []struct {
	Ticks int64
	Name  string
} {
	out := make([]struct {
		Ticks int64
		Name  string
	}, 0, len(durationNames))
	for t, n := range durationNames {
		out = append(out, struct {
			Ticks int64
			Name  string
		}{t, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ticks < out[j].Ticks })
	return out
}

// bpmString spells microseconds-per-quarter as the BPM the parser will
// convert back to exactly that value. Tempo is stored SMF-style and BPM
// is its reciprocal, so most tempos are not round numbers; rather than
// pick a fixed precision and hope, this widens the precision until the
// text round-trips, which is the only definition of "enough digits" that
// is actually true.
func bpmString(usPerQuarter int64) (string, error) {
	if usPerQuarter <= 0 {
		return "", fmt.Errorf("gtab: tempo of %d microseconds per quarter is not positive", usPerQuarter)
	}
	bpm := 60e6 / float64(usPerQuarter)
	if !(bpm >= 1 && bpm <= 1000) {
		return "", fmt.Errorf("gtab: tempo of %.4g BPM is outside the 1-1000 the format accepts", bpm)
	}
	// Plain decimal only, never an exponent: 'g' turns 120 BPM into
	// "1.2e+02" at the first precision that happens to round-trip, which is
	// a correct number and an unreadable tempo marking.
	for dec := 0; dec <= 12; dec++ {
		s := strconv.FormatFloat(bpm, 'f', dec, 64)
		v, err := strconv.ParseFloat(s, 64)
		if err == nil && v >= 1 && v <= 1000 && score.USPerQuarter(v) == usPerQuarter {
			return s, nil
		}
	}
	// Unreachable for any int64 tempo: 17 significant digits reproduce any
	// float64 exactly, and the value came from one. Reported rather than
	// silently approximated all the same.
	return "", fmt.Errorf("gtab: tempo of %d microseconds per quarter cannot be written as a BPM that reads back exactly", usPerQuarter)
}

// sameTuning reports whether two tunings are identical, so the default
// can be left out of the file.
func sameTuning(a, b score.Tuning) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pitchClassNames spells the twelve pitch classes with sharps. The parser
// reads flats too; the writer only ever needs one spelling per pitch.
var pitchClassNames = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// pitchName renders a MIDI key in scientific pitch notation, middle C =
// C4 = 60 — the notation \tuning reads.
func pitchName(key int) string {
	return pitchClassNames[key%12] + strconv.Itoa(key/12-1)
}
