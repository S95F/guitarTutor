package textfmt

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/S95F/musicTutor/internal/score"
)

// The format's limits and defaults (see docs/TEXTFORMAT.md). These are
// exported because the editor authors pieces in this format and has to
// refuse — and default to — exactly what the format does; two copies of
// "the highest fret" that disagree is a piece that will not save.
const (
	// MaxFret is the highest fret number the format accepts, for a capo as
	// well as for a note.
	MaxFret = 30
	// DefaultBPM is the tempo a piece has when it states none.
	DefaultBPM = 120
	// DefaultProgram is the General MIDI program a track has when it states
	// none (25 = steel-string acoustic guitar).
	DefaultProgram = 25
)

// Unexported aliases, so the parser and writer below read as they did
// before these limits acquired a second audience.
const (
	maxFret        = MaxFret
	defaultBPM     = DefaultBPM
	defaultProgram = DefaultProgram
)

// A parser holds the state of one Parse call: the scanner, the score
// being built, the current track and open bar, and the pending mid-piece
// directives that take effect at the next bar.
type parser struct {
	sc    *scanner
	name  string
	score *score.Score

	track  *score.Track // current track; nil until a track is needed
	bar    *score.Bar   // open bar; nil at a bar boundary
	filled int64        // ticks filled so far in the open bar
	sticky int64        // sticky beat duration in ticks (initially a quarter)
	sawBar bool         // any bar begun anywhere in the piece

	// Whether the current track stated these explicitly, so \instrument
	// can tell an authored \program from the default it would otherwise
	// replace, and can refuse to follow string directives.
	progSet   bool
	tuningSet bool
	capoSet   bool

	pendingTempo     *float64 // \tempo awaiting the next bar, in BPM
	pendingTempoTick int64    // piece tick where the pending \tempo takes effect
	pendingMeter     *[2]int  // \time awaiting the next bar, as {num, den}
	pendingMeterTick int64    // piece tick where the pending \time takes effect

	// Source position of the last \tempo / \time directive per anchor
	// tick, so the end-of-piece check in run can point at the directive
	// that produced an unwritable entry rather than at end of input.
	tempoDirPos map[int64]pos
	meterDirPos map[int64]pos
}

// errAt builds a ParseError at position at.
func (p *parser) errAt(at pos, format string, args ...any) *ParseError {
	return &ParseError{Name: p.name, Line: at.line, Col: at.col, Msg: fmt.Sprintf(format, args...)}
}

// run drives the main token loop, then closes any open final bar and
// checks whole-score invariants.
func (p *parser) run() error {
	for {
		p.sc.skipSpace()
		if p.sc.eof() {
			break
		}
		var err error
		switch p.sc.peek() {
		case '\\':
			err = p.directive()
		case '|':
			at := p.sc.pos()
			p.sc.next()
			err = p.closeBar(at)
		case '(':
			err = p.beatChord(false, p.sc.pos())
		case ')':
			return p.errAt(p.sc.pos(), `unexpected ")"`)
		case '~':
			at := p.sc.pos()
			p.sc.next()
			if !p.sc.eof() && p.sc.peek() == '(' {
				err = p.beatChord(true, at)
			} else {
				err = p.beatWord(true)
			}
		default:
			err = p.beatWord(false)
		}
		if err != nil {
			return err
		}
	}
	if p.bar != nil {
		if err := p.closeBar(p.sc.pos()); err != nil {
			return err
		}
	}
	if !p.sawBar {
		return p.errAt(p.sc.pos(), "piece has no bars")
	}
	// A directive still pending at end of input takes effect at its anchor
	// like any other — beginBar just never came around to flushing it.
	// Dropping it instead would silently ignore authored text (a \tempo
	// between two tracks' bars would vanish from playback).
	if p.pendingMeter != nil {
		p.score.Meters = insertMeter(p.score.Meters, score.Meter{Tick: p.pendingMeterTick, Num: p.pendingMeter[0], Den: p.pendingMeter[1]})
		p.pendingMeter = nil
	}
	if p.pendingTempo != nil {
		p.score.Tempos = insertTempo(p.score.Tempos, score.Tempo{Tick: p.pendingTempoTick, USPerQuarter: score.USPerQuarter(*p.pendingTempo)})
		p.pendingTempo = nil
	}
	if err := p.checkMeterAlignment(); err != nil {
		return err
	}
	// An entry anchored at the very END of the piece changes nothing a
	// listener can hear (nothing sounds after it), and Format refuses to
	// write it — no bar starts there, and directives only exist before
	// bars — so accepting it would hand back a piece that can never be
	// saved. Any entry short of the end sits on a bar start of whichever
	// track reaches past it (all tracks share the meter map's barlines,
	// which checkMeterAlignment just enforced), so this is the one gap.
	end := p.score.End()
	for _, e := range p.score.Tempos {
		if e.Tick == end {
			return p.errAt(p.dirPos(p.tempoDirPos, end), `this \tempo takes effect at the very end of the piece, where it changes nothing and cannot be saved; delete it or add the bars it applies to`)
		}
	}
	for _, m := range p.score.Meters {
		if m.Tick == end {
			return p.errAt(p.dirPos(p.meterDirPos, end), `this \time takes effect at the very end of the piece, where it changes nothing and cannot be saved; delete it or add the bars it applies to`)
		}
	}
	if err := p.score.Validate(); err != nil {
		return p.errAt(p.sc.pos(), "internal: parsed score fails validation: %v", err)
	}
	return nil
}

// dirPos looks up the source position recorded for the directive anchored
// at tick, falling back to the scanner's position (end of input) for the
// entries no directive produced.
func (p *parser) dirPos(m map[int64]pos, tick int64) pos {
	if at, ok := m[tick]; ok {
		return at
	}
	return p.sc.pos()
}

// ensureTrack returns the current track, materializing the default
// unnamed track for pieces whose bars precede any \track directive.
func (p *parser) ensureTrack() *score.Track {
	if p.track == nil {
		p.track = p.newTrack("")
	}
	return p.track
}

// newTrack appends a track with the format's defaults: standard E
// tuning, no capo, steel-string acoustic program, user role. \instrument
// swaps the string defaults out again for a wind part.
func (p *parser) newTrack(name string) *score.Track {
	tr := &score.Track{
		Name:    name,
		Tuning:  append(score.Tuning(nil), score.StandardTuning...),
		Program: defaultProgram,
	}
	p.score.Tracks = append(p.score.Tracks, tr)
	p.progSet, p.tuningSet, p.capoSet = false, false, false
	return tr
}

// beginBar opens a new bar on the current track, applying any pending
// mid-piece \tempo and \time directives at their anchored ticks.
func (p *parser) beginBar() {
	tr := p.ensureTrack()
	start := int64(0)
	if n := len(tr.Bars); n > 0 {
		last := tr.Bars[n-1]
		start = last.Start + last.Len()
	}
	if p.pendingMeter != nil {
		p.score.Meters = insertMeter(p.score.Meters, score.Meter{Tick: p.pendingMeterTick, Num: p.pendingMeter[0], Den: p.pendingMeter[1]})
		p.pendingMeter = nil
	}
	m := p.score.Meters.At(start)
	p.bar = tr.AppendBar(m.Num, m.Den)
	p.filled = 0
	if p.pendingTempo != nil {
		p.score.Tempos = insertTempo(p.score.Tempos, score.Tempo{Tick: p.pendingTempoTick, USPerQuarter: score.USPerQuarter(*p.pendingTempo)})
		p.pendingTempo = nil
	}
	p.sawBar = true
}

// anchorTick returns the piece tick at which a mid-piece \tempo or \time
// directive takes effect: the end of the current track's last bar, or 0
// when the current track has no bars yet. Anchoring at directive-parse
// time keeps the change at the piece position where it was written even
// when a following \track directive rewinds the next bar's start to
// tick 0.
func (p *parser) anchorTick() int64 {
	if p.track != nil {
		if n := len(p.track.Bars); n > 0 {
			last := p.track.Bars[n-1]
			return last.Start + last.Len()
		}
	}
	return 0
}

// closeBar ends the open bar, rejecting one whose beats do not exactly
// fill the meter. at is the position of the "|" (or of end of input).
func (p *parser) closeBar(at pos) error {
	if p.bar == nil {
		return p.errAt(at, "empty bar")
	}
	if p.filled != p.bar.Len() {
		return p.errAt(at, "bar underfull: the notes add up to %s of the %d beats a %d/%d bar holds",
			beatsIn(p.filled, p.bar.Den), p.bar.Num, p.bar.Num, p.bar.Den)
	}
	p.bar = nil
	return nil
}

// addBeat appends one parsed beat, opening a new bar if none is open and
// enforcing the bar's capacity under the meter in effect. at is the
// beat's source position; no notes means a rest.
func (p *parser) addBeat(at pos, dur int64, notes ...score.Note) error {
	if p.bar == nil {
		p.beginBar()
	}
	if p.filled+dur > p.bar.Len() {
		return p.errAt(at, "bar overfull: the notes add up to %s beats, and a %d/%d bar holds %d",
			beatsIn(p.filled+dur, p.bar.Den), p.bar.Num, p.bar.Den, p.bar.Num)
	}
	p.bar.AddBeat(dur, notes...)
	p.filled += dur
	p.sticky = dur
	return nil
}

// beatsIn renders a span of ticks as beats of a meter, for the messages a
// person reads. Ticks are the model's unit — PPQ 960, defined nowhere a
// user can see — and a bar being short by 960 of them means nothing to
// somebody counting in beats.
func beatsIn(ticks int64, den int) string {
	beat := 4 * score.PPQ / int64(den)
	if beat <= 0 {
		return "0"
	}
	return strconv.FormatFloat(float64(ticks)/float64(beat), 'g', 3, 64)
}

// beatWord parses one non-chord beat token: a note "fret.string" with
// optional duration and technique suffixes, or a rest "r" with optional
// duration. tied is true when the main loop consumed a "~" prefix.
func (p *parser) beatWord(tied bool) error {
	p.ensureTrack()
	tok, tp := p.sc.word()
	if tok == "" {
		return p.errAt(tp, `"~" must be followed by a note or a chord`)
	}
	t := &tokScan{tok: tok, base: tp}
	if t.peek() == 'r' {
		if tied {
			return p.errAt(tp, "cannot tie a rest")
		}
		t.i++
		dur := p.sticky
		if t.peek() == '.' {
			var err error
			if dur, err = p.duration(t); err != nil {
				return err
			}
		}
		if t.i < len(tok) {
			return p.errAt(t.pos(), "unexpected %q after rest", t.rest())
		}
		return p.addBeat(tp, dur)
	}
	n, err := p.noteCore(t, tied)
	if err != nil {
		return err
	}
	dur := p.sticky
	if t.peek() == '.' {
		if dur, err = p.duration(t); err != nil {
			return err
		}
	}
	if err := p.techniques(t, &n); err != nil {
		return err
	}
	return p.addBeat(tp, dur, n)
}

// beatChord parses "(note note ...)" plus an optional ".duration" and
// technique suffix attached directly to the ")". tied ties every chord
// note (from a "~" prefix on the whole chord); individual chord notes may
// also carry their own "~". start is the beat's source position.
func (p *parser) beatChord(tied bool, start pos) error {
	p.ensureTrack()
	if w := p.track.Wind; w != nil {
		return p.errAt(start, "chord on a %s, which plays one note at a time", w.Name)
	}
	p.sc.next() // consume "("
	var notes []score.Note
	seen := map[int]bool{} // string numbers already used in this chord
	for {
		p.sc.skipSpace()
		if p.sc.eof() {
			return p.errAt(start, "unterminated chord")
		}
		c := p.sc.peek()
		if c == ')' {
			p.sc.next()
			break
		}
		if c == '|' || c == '(' || c == '\\' {
			return p.errAt(p.sc.pos(), "unexpected %q inside a chord (missing \")\"?)", string(rune(c)))
		}
		noteTied := tied
		if c == '~' {
			p.sc.next()
			noteTied = true
		}
		tok, tp := p.sc.word()
		if tok == "" {
			return p.errAt(tp, "expected a note inside the chord")
		}
		t := &tokScan{tok: tok, base: tp}
		n, err := p.noteCore(t, noteTied)
		if err != nil {
			return err
		}
		if t.peek() == '.' {
			return p.errAt(t.pos(), "chord notes take no duration (put it after the \")\")")
		}
		if err := p.techniques(t, &n); err != nil {
			return err
		}
		if seen[n.String] {
			return p.errAt(tp, "string %d played twice in one chord", n.String)
		}
		seen[n.String] = true
		notes = append(notes, n)
	}
	if len(notes) == 0 {
		return p.errAt(start, "empty chord")
	}
	// An optional ".duration" and technique suffix attaches directly to
	// the ")"; the techniques apply to every note of the chord.
	dur := p.sticky
	switch c := p.sc.peek(); c {
	case 0, ' ', '\t', '\r', '\n', '|', '(', ')':
		// No suffix.
	default:
		tok, tp := p.sc.word()
		t := &tokScan{tok: tok, base: tp}
		var err error
		if t.peek() == '.' {
			if dur, err = p.duration(t); err != nil {
				return err
			}
		}
		var all score.Note
		if err := p.techniques(t, &all); err != nil {
			return err
		}
		for i := range notes {
			notes[i].Tech |= all.Tech
		}
	}
	return p.addBeat(start, dur, notes...)
}

// noteCore parses one note at the token cursor: "fret.string" on a
// fretted track, a written pitch name on a wind track.
func (p *parser) noteCore(t *tokScan, tied bool) (score.Note, error) {
	if w := p.track.Wind; w != nil {
		return p.windNote(t, w, tied)
	}
	fp := t.pos()
	fret, ok := t.uint()
	if !ok {
		return score.Note{}, p.errAt(fp, "malformed beat %q: expected a fret number", t.tok)
	}
	if fret > maxFret {
		return score.Note{}, p.errAt(fp, "fret %d out of range (0-%d)", fret, maxFret)
	}
	if t.peek() != '.' {
		return score.Note{}, p.errAt(t.pos(), "malformed beat %q: expected \".\" after the fret", t.tok)
	}
	t.i++
	sp := t.pos()
	str, ok := t.uint()
	if !ok {
		return score.Note{}, p.errAt(sp, "malformed beat %q: expected a string number", t.tok)
	}
	if ns := len(p.track.Tuning); str < 1 || str > ns {
		return score.Note{}, p.errAt(sp, "string %d out of range (track has %d strings)", str, ns)
	}
	// Tuning, capo, and fret are each in range, but their sum is the
	// sounding pitch and must fit MIDI (the components are all >= 0, so
	// only the top can be exceeded).
	if key := p.track.Tuning[str-1] + p.track.Capo + fret; key > 127 {
		return score.Note{}, p.errAt(fp, "note sounds MIDI key %d (open %d + capo %d + fret %d), above 127",
			key, p.track.Tuning[str-1], p.track.Capo, fret)
	}
	return score.Note{String: str, Fret: fret, Tied: tied}, nil
}

// windNote parses a written pitch name ("D5", "Bb4", "F#5") at the token
// cursor — the note a wind player reads — and converts it through the
// instrument's transposition onto the track's one chromatic lane:
// String 1, Fret counting semitones above the instrument's lowest note.
// The instrument's Span is deliberately not enforced here: notes above
// the standard range are altissimo, which is hard, not unwritable.
func (p *parser) windNote(t *tokScan, w *score.WindInstrument, tied bool) (score.Note, error) {
	np := t.pos()
	written, ok := t.pitch()
	if !ok {
		return score.Note{}, p.errAt(np, "malformed beat %q: a %s note is a written pitch name like %s", t.tok, w.Name, pitchName(w.Written(w.LowSounding)))
	}
	name := t.tok[:t.i]
	sounding := w.Sounding(written)
	if sounding < w.LowSounding {
		return score.Note{}, p.errAt(np, "%s is below the %s's lowest note, %s", name, w.Name, pitchName(w.Written(w.LowSounding)))
	}
	if sounding > 127 {
		return score.Note{}, p.errAt(np, "%s sounds MIDI key %d, above 127", name, sounding)
	}
	return score.Note{String: 1, Fret: sounding - w.LowSounding, Tied: tied}, nil
}

// duration parses ".N" (plain), ".N." (dotted), or ".Nt" (triplet) at
// the token cursor and returns the length in ticks.
func (p *parser) duration(t *tokScan) (int64, error) {
	t.i++ // consume "."
	dp := t.pos()
	n, ok := t.uint()
	if !ok {
		return 0, p.errAt(dp, "expected a duration after \".\"")
	}
	var base int64
	switch n {
	case 1:
		base = score.Whole
	case 2:
		base = score.Half
	case 4:
		base = score.Quarter
	case 8:
		base = score.Eighth
	case 16:
		base = score.Sixteenth
	case 32:
		base = score.ThirtySec
	default:
		return 0, p.errAt(dp, "invalid duration %d (valid: 1 2 4 8 16 32)", n)
	}
	switch t.peek() {
	case '.':
		t.i++
		return score.Dotted(base), nil
	case 't':
		t.i++
		return score.Triplet(base), nil
	}
	return base, nil
}

// techniques parses the technique letters that end a beat token, or-ing
// them into the note's bitmask. The letters are the current track's
// instrument family's: a wind part slurs and scoops where a guitar
// hammers and mutes, and accepting a letter the instrument cannot play
// would be writing down something nobody can do.
func (p *parser) techniques(t *tokScan, n *score.Note) error {
	wind := p.track.Wind != nil
	for t.i < len(t.tok) {
		tech, ok := techFor(t.tok[t.i], wind)
		if !ok {
			r, _ := utf8.DecodeRuneInString(t.tok[t.i:])
			valid := "h p s b v x"
			if wind {
				valid = "l s b v"
			}
			return p.errAt(t.pos(), "unknown technique %q (valid: %s)", string(r), valid)
		}
		n.Tech |= tech
		t.i++
	}
	return nil
}

// techFor maps a technique letter to its score.Technique bit, in the
// current instrument family's alphabet. Both alphabets are mirrored by
// write.go's techString tables — the four lists must stay in step.
func techFor(c byte, wind bool) (score.Technique, bool) {
	if wind {
		switch c {
		case 'l':
			return score.TechSlur, true
		case 's':
			return score.TechSlide, true
		case 'b':
			return score.TechBend, true
		case 'v':
			return score.TechVibrato, true
		}
		return 0, false
	}
	switch c {
	case 'h':
		return score.TechHammer, true
	case 'p':
		return score.TechPull, true
	case 's':
		return score.TechSlide, true
	case 'b':
		return score.TechBend, true
	case 'v':
		return score.TechVibrato, true
	case 'x':
		return score.TechDead, true
	}
	return 0, false
}

// directive dispatches one "\name ..." directive; the directive's
// arguments run to the end of its line.
func (p *parser) directive() error {
	tok, tp := p.sc.word()
	name := tok[1:] // tok starts with the dispatching "\"
	if name == "" {
		return p.errAt(tp, `expected a directive name after "\"`)
	}
	switch name {
	case "title":
		if p.sawBar {
			return p.errAt(tp, `\title must appear before the first bar`)
		}
		text := p.sc.restOfLine()
		if text == "" {
			return p.errAt(tp, `\title requires text`)
		}
		p.score.Title = text
		return nil

	case "tempo":
		if p.bar != nil {
			return p.errAt(tp, `\tempo may only appear between bars (after a "|")`)
		}
		arg, err := p.oneArg(tp, name)
		if err != nil {
			return err
		}
		bpm, perr := strconv.ParseFloat(arg.text, 64)
		// The negated range form also rejects NaN (every comparison with
		// NaN is false) and infinities, not just ordinary out-of-range
		// values; anything outside [1, 1000] would over- or underflow
		// score.USPerQuarter's int64.
		if perr != nil || !(bpm >= 1 && bpm <= 1000) {
			return p.errAt(arg.pos, "invalid tempo %q (want BPM in 1-1000)", arg.text)
		}
		// A pending \tempo holds one directive. Two in a row at the SAME
		// anchor are last-wins — the map replaces same-tick entries anyway,
		// so nothing is lost — but when a \track switch moved the anchor
		// the two apply at different piece ticks, and keeping only the
		// second would silently erase the first from the piece.
		tick := p.anchorTick()
		if p.pendingTempo != nil && p.pendingTempoTick != tick {
			return p.errAt(tp, `this \tempo would silently discard the \tempo written before the last \track, which takes effect at a different point in the piece; keep one, or move this one to where it should apply`)
		}
		p.pendingTempo = &bpm
		p.pendingTempoTick = tick
		if p.tempoDirPos == nil {
			p.tempoDirPos = map[int64]pos{}
		}
		p.tempoDirPos[tick] = tp
		return nil

	case "time":
		if p.bar != nil {
			return p.errAt(tp, `\time may only appear between bars (after a "|")`)
		}
		arg, err := p.oneArg(tp, name)
		if err != nil {
			return err
		}
		num, den, ok := parseTimeSig(arg.text)
		if !ok {
			return p.errAt(arg.pos, "invalid time signature %q (want n/d with d one of 1 2 4 8 16 32)", arg.text)
		}
		// Same pending-slot rule as \tempo above: replacing a \time that
		// anchors at a different piece tick would silently lose it.
		tick := p.anchorTick()
		if p.pendingMeter != nil && p.pendingMeterTick != tick {
			return p.errAt(tp, `this \time would silently discard the \time written before the last \track, which takes effect at a different point in the piece; keep one, or move this one to where it should apply`)
		}
		p.pendingMeter = &[2]int{num, den}
		p.pendingMeterTick = tick
		if p.meterDirPos == nil {
			p.meterDirPos = map[int64]pos{}
		}
		p.meterDirPos[tick] = tp
		return nil

	case "track":
		if p.bar != nil {
			return p.errAt(tp, `\track may only appear between bars (after a "|")`)
		}
		tname := p.sc.restOfLine()
		if tname == "" {
			return p.errAt(tp, `\track requires a name`)
		}
		p.track = p.newTrack(tname)
		p.sticky = score.Quarter
		return nil

	case "instrument":
		if err := p.trackDirective(tp, `\instrument`); err != nil {
			return err
		}
		iname := p.sc.restOfLine()
		if iname == "" {
			return p.errAt(tp, `\instrument requires an instrument name`)
		}
		w := score.WindByName(iname)
		if w == nil {
			return p.errAt(tp, "unknown instrument %q (this app knows: %s)", iname, strings.Join(score.WindNames(), ", "))
		}
		if p.track.Wind != nil {
			return p.errAt(tp, "the track is already a %s", p.track.Wind.Name)
		}
		if p.tuningSet || p.capoSet {
			return p.errAt(tp, `\instrument cannot follow \tuning or \capo: a %s has no strings`, w.Name)
		}
		p.track.Wind = w
		p.track.Tuning = nil
		p.track.Capo = 0
		if !p.progSet {
			p.track.Program = w.Program
		}
		return nil

	case "tuning":
		if err := p.trackDirective(tp, `\tuning`); err != nil {
			return err
		}
		if w := p.track.Wind; w != nil {
			return p.errAt(tp, `\tuning on a %s, which has no strings`, w.Name)
		}
		p.tuningSet = true
		args := p.sc.args()
		if len(args) == 0 {
			return p.errAt(tp, `\tuning requires at least one note`)
		}
		tuning := make(score.Tuning, len(args))
		for i, a := range args {
			key, err := parsePitch(a.text)
			if err != nil {
				return p.errAt(a.pos, "invalid tuning note %q: %v", a.text, err)
			}
			// The file lists open strings low to high; the model stores
			// the highest-pitched string first.
			tuning[len(args)-1-i] = key
		}
		p.track.Tuning = tuning
		return nil

	case "capo":
		if err := p.trackDirective(tp, `\capo`); err != nil {
			return err
		}
		if w := p.track.Wind; w != nil {
			return p.errAt(tp, `\capo on a %s, which has no capo`, w.Name)
		}
		p.capoSet = true
		arg, err := p.oneArg(tp, name)
		if err != nil {
			return err
		}
		fret, perr := strconv.Atoi(arg.text)
		if perr != nil || fret < 0 || fret > maxFret {
			return p.errAt(arg.pos, "invalid capo %q (want 0-%d)", arg.text, maxFret)
		}
		p.track.Capo = fret
		return nil

	case "program":
		if err := p.trackDirective(tp, `\program`); err != nil {
			return err
		}
		p.progSet = true
		arg, err := p.oneArg(tp, name)
		if err != nil {
			return err
		}
		prog, perr := strconv.Atoi(arg.text)
		if perr != nil || prog < 0 || prog > 127 {
			return p.errAt(arg.pos, "invalid program %q (want 0-127)", arg.text)
		}
		p.track.Program = prog
		return nil

	case "backing":
		if err := p.trackDirective(tp, `\backing`); err != nil {
			return err
		}
		if args := p.sc.args(); len(args) > 0 {
			return p.errAt(args[0].pos, `\backing takes no arguments`)
		}
		p.track.Role = score.RoleBacking
		return nil
	}
	return p.errAt(tp, `unknown directive \%s`, name)
}

// trackDirective prepares a track-scoped directive: it materializes the
// default track if needed and rejects the directive once the current
// track has bars.
func (p *parser) trackDirective(at pos, dir string) error {
	if tr := p.ensureTrack(); len(tr.Bars) > 0 {
		return p.errAt(at, "%s must appear before the current track's first bar", dir)
	}
	return nil
}

// oneArg reads the directive's arguments and requires exactly one.
func (p *parser) oneArg(at pos, dir string) (argument, error) {
	args := p.sc.args()
	if len(args) == 0 {
		return argument{}, p.errAt(at, `\%s requires an argument`, dir)
	}
	if len(args) > 1 {
		return argument{}, p.errAt(args[1].pos, `\%s takes one argument`, dir)
	}
	return args[0], nil
}

// checkMeterAlignment verifies, once every track is parsed, that each bar
// agrees with the final meter map: a mid-piece \time change must land on
// a bar boundary of every track.
func (p *parser) checkMeterAlignment() error {
	for ti, tr := range p.score.Tracks {
		for bi, bar := range tr.Bars {
			if m := p.score.Meters.At(bar.Start); m.Num != bar.Num || m.Den != bar.Den {
				return p.errAt(p.sc.pos(),
					"track %d (%q) bar %d was parsed as %d/%d but a later \\time change makes tick %d %d/%d; \\time changes must align with every track's bars",
					ti+1, tr.Name, bi+1, bar.Num, bar.Den, bar.Start, m.Num, m.Den)
			}
			end := bar.Start + bar.Len()
			for _, m := range p.score.Meters {
				if m.Tick > bar.Start && m.Tick < end {
					return p.errAt(p.sc.pos(),
						"track %d (%q) bar %d spans ticks %d-%d, crossing the \\time change at tick %d; \\time changes must align with every track's bars",
						ti+1, tr.Name, bi+1, bar.Start, end, m.Tick)
				}
			}
		}
	}
	return nil
}

// parseTimeSig parses "n/d" with a power-of-two denominator up to 32.
func parseTimeSig(s string) (num, den int, ok bool) {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return 0, 0, false
	}
	num, err1 := strconv.Atoi(s[:i])
	den, err2 := strconv.Atoi(s[i+1:])
	if err1 != nil || err2 != nil || num < 1 || num > 64 {
		return 0, 0, false
	}
	switch den {
	case 1, 2, 4, 8, 16, 32:
		return num, den, true
	}
	return 0, 0, false
}

// parsePitch parses one tuning note: scientific pitch notation (a letter,
// an optional # or b, and an octave; middle C = C4 = MIDI 60) or a bare
// MIDI note number 0-127.
func parsePitch(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty note")
	}
	if c := s[0]; c >= '0' && c <= '9' {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("not a note name or MIDI number")
		}
		if n < 0 || n > 127 {
			return 0, fmt.Errorf("MIDI note %d out of range (0-127)", n)
		}
		return n, nil
	}
	c := s[0]
	if c >= 'a' && c <= 'g' {
		c -= 'a' - 'A'
	}
	var base int
	switch c {
	case 'C':
		base = 0
	case 'D':
		base = 2
	case 'E':
		base = 4
	case 'F':
		base = 5
	case 'G':
		base = 7
	case 'A':
		base = 9
	case 'B':
		base = 11
	default:
		return 0, fmt.Errorf("not a note name or MIDI number")
	}
	i, acc := 1, 0
	if i < len(s) {
		switch s[i] {
		case '#':
			acc, i = 1, i+1
		case 'b':
			acc, i = -1, i+1
		}
	}
	oct, err := strconv.Atoi(s[i:])
	if err != nil {
		return 0, fmt.Errorf("bad octave")
	}
	key := (oct+1)*12 + base + acc
	if key < 0 || key > 127 {
		return 0, fmt.Errorf("MIDI note %d out of range (0-127)", key)
	}
	return key, nil
}

// insertTempo inserts a tempo change keeping the map sorted, replacing
// any existing entry at the same tick.
func insertTempo(m score.TempoMap, e score.Tempo) score.TempoMap {
	i := sort.Search(len(m), func(i int) bool { return m[i].Tick >= e.Tick })
	if i < len(m) && m[i].Tick == e.Tick {
		m[i] = e
		return m
	}
	m = append(m, score.Tempo{})
	copy(m[i+1:], m[i:])
	m[i] = e
	return m
}

// insertMeter inserts a time-signature change keeping the map sorted,
// replacing any existing entry at the same tick.
func insertMeter(m score.MeterMap, e score.Meter) score.MeterMap {
	i := sort.Search(len(m), func(i int) bool { return m[i].Tick >= e.Tick })
	if i < len(m) && m[i].Tick == e.Tick {
		m[i] = e
		return m
	}
	m = append(m, score.Meter{})
	copy(m[i+1:], m[i:])
	m[i] = e
	return m
}

// A tokScan walks a single token, converting byte offsets back to source
// columns for error positions.
type tokScan struct {
	tok  string
	i    int // byte offset of the cursor within tok
	base pos // source position of tok's first character
}

// pos returns the source position of the cursor.
func (t *tokScan) pos() pos {
	return pos{line: t.base.line, col: t.base.col + utf8.RuneCountInString(t.tok[:t.i])}
}

// peek returns the byte at the cursor, or 0 at the end of the token.
func (t *tokScan) peek() byte {
	if t.i >= len(t.tok) {
		return 0
	}
	return t.tok[t.i]
}

// uint consumes a run of decimal digits at the cursor.
func (t *tokScan) uint() (int, bool) {
	start := t.i
	for t.i < len(t.tok) && t.tok[t.i] >= '0' && t.tok[t.i] <= '9' {
		t.i++
	}
	if t.i == start {
		return 0, false
	}
	n, err := strconv.Atoi(t.tok[start:t.i])
	if err != nil {
		return 0, false // overflow
	}
	return n, true
}

// pitch consumes a scientific pitch name at the cursor — a letter A-G in
// either case, an optional # or b, and an octave (middle C = C4 = MIDI
// 60) — and returns its MIDI key. Unlike parsePitch it does not accept a
// bare MIDI number: inside a beat token digits are frets and durations,
// and a number that meant a pitch on one track and a fret on another
// would be a trap.
func (t *tokScan) pitch() (int, bool) {
	if t.i >= len(t.tok) {
		return 0, false
	}
	c := t.tok[t.i]
	if c >= 'a' && c <= 'g' {
		c -= 'a' - 'A'
	}
	var base int
	switch c {
	case 'C':
		base = 0
	case 'D':
		base = 2
	case 'E':
		base = 4
	case 'F':
		base = 5
	case 'G':
		base = 7
	case 'A':
		base = 9
	case 'B':
		base = 11
	default:
		return 0, false
	}
	t.i++
	acc := 0
	if t.i < len(t.tok) {
		switch t.tok[t.i] {
		case '#':
			acc, t.i = 1, t.i+1
		case 'b':
			acc, t.i = -1, t.i+1
		}
	}
	oct, ok := t.uint()
	if !ok {
		return 0, false
	}
	key := (oct+1)*12 + base + acc
	if key < 0 || key > 127 {
		return 0, false
	}
	return key, true
}

// rest returns the unconsumed remainder of the token.
func (t *tokScan) rest() string { return t.tok[t.i:] }
