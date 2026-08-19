package ui

// The raw .gtab view: the same piece, as the text that will be saved.
//
// It exists because the grid has controls for what a guitarist reaches for
// most and the format has more than that — a capo, a General MIDI program,
// an arbitrary tuning, a comment explaining a passage to yourself. Rather
// than grow a control for each, F2 shows the file. Nothing here is a
// second source of truth: switching in serialises the document, switching
// out parses the text back into one, and a text that will not parse simply
// does not switch — with the line and column the parser named.
//
// The editing is deliberately small: a caret, insertion, deletion, and
// movement. There is no selection and no clipboard, because Ebitengine
// offers no clipboard and a half-working copy-and-paste is worse than an
// absent one. The grid is where the real work happens; this is the escape
// hatch.

import (
	"errors"
	"fmt"
	"image/color"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/guitarTutor/internal/edit"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
)

// Layout of the text pane.
const (
	gtLineH  = 17.0
	gtPadX   = 12.0
	gtPadY   = 10.0
	gtGutter = 46.0 // line numbers
)

var (
	colGtGutter = color.RGBA{70, 70, 84, 255}
	colGtCaret  = color.RGBA{255, 220, 120, 255}
	colGtOK     = color.RGBA{110, 200, 140, 255}
)

// A gtabPane is the text view's own state: the lines, the caret, and what
// the parser last made of them.
type gtabPane struct {
	lines [][]rune
	cx    int // caret column, in runes
	cy    int // caret line
	top   int // first visible line

	// status is the parser's verdict on the text as it stands, refreshed
	// on every change so the answer is never stale, and ok says whether it
	// is good news.
	status string
	ok     bool
	// seed is the text the pane opened with, and fromFile marks a pane
	// seeded from a FILE rather than rendered from the document — see
	// NewEditorForText. Escape treats a file-seeded pane differently:
	// unchanged broken text is someone else's problem the user only came
	// to look at, and may be walked away from; edited text is theirs and
	// is kept — unless a second Escape says otherwise (escArmed).
	seed     string
	fromFile bool
	// escArmed remembers that the last Escape was refused for text that
	// will not parse, so the next one — with nothing typed in between —
	// may discard the typing and leave. Any edit disarms it.
	escArmed bool
	// bars and notes describe a text that parses, so the pane can say what
	// it would produce rather than only that it is valid.
	bars, notes int
}

// newGtabPane renders a document to text and readies it for editing.
func newGtabPane(doc *edit.Doc) (*gtabPane, error) {
	src, err := textfmt.Format(doc.Score())
	if err != nil {
		return nil, err
	}
	return newGtabPaneFromSource(src), nil
}

// newGtabPaneFromSource readies raw .gtab text for editing — including
// text that does not parse, which is the whole reason the pane can be
// seeded from a file rather than only from a document: a broken piece
// has no document to render, and this pane is where it gets repaired.
func newGtabPaneFromSource(src []byte) *gtabPane {
	p := &gtabPane{}
	for _, line := range strings.Split(strings.TrimRight(string(src), "\r\n"), "\n") {
		// A file broken by editing in Notepad arrives with \r\n endings;
		// an invisible \r at the end of every line would put the caret
		// behind a rune the user cannot see and shift every reported
		// column by one.
		p.lines = append(p.lines, []rune(strings.TrimRight(line, "\r")))
	}
	if len(p.lines) == 0 {
		p.lines = [][]rune{{}}
	}
	p.reparse()
	p.seed = p.text()
	return p
}

// text is the pane's content as one string.
func (p *gtabPane) text() string {
	out := make([]string, len(p.lines))
	for i, l := range p.lines {
		out[i] = string(l)
	}
	return strings.Join(out, "\n") + "\n"
}

// reparse runs the parser over the current text and records what it said.
// It is cheap — the format is tiny and pieces are a few kilobytes — so it
// runs on every keystroke rather than on a timer, which is what makes the
// error line move as you fix it.
func (p *gtabPane) reparse() {
	sc, err := textfmt.Parse([]byte(p.text()), "piece")
	if err != nil {
		p.ok, p.status, p.bars, p.notes = false, gtabProblem(err), 0, 0
		return
	}
	p.ok, p.status = true, ""
	p.bars, p.notes = 0, 0
	for _, tr := range sc.Tracks {
		if n := len(tr.Bars); n > p.bars {
			p.bars = n
		}
		for _, bar := range tr.Bars {
			for _, bt := range bar.Beats {
				p.notes += len(bt.Notes)
			}
		}
	}
}

// gtabProblem turns a parse failure into a line somebody can act on.
//
// The parser reports "piece:4:12: bar underfull: ...", which is the right
// shape for a compiler and the wrong one for a pane: "piece" is a
// filename that does not exist, and the colons read as punctuation in the
// message rather than as a position. The position is the useful half, so
// it is said in words and the rest is left alone — the parser's own
// wording is already plain, and rewriting it here would put the
// explanation of the format two files away from the format.
func gtabProblem(err error) string {
	var pe *textfmt.ParseError
	if !errors.As(err, &pe) {
		return err.Error()
	}
	return fmt.Sprintf("line %d, column %d — %s", pe.Line, pe.Col, pe.Msg)
}

// toggleText swaps the grid for the text and back. Going out of the text
// view parses it; a text that will not parse keeps the view open with the
// parser's own message, because the alternative is throwing away what was
// typed.
func (e *Editor) toggleText() {
	if e.text == nil {
		pane, err := newGtabPane(e.doc)
		if err != nil {
			e.report(err)
			return
		}
		e.text = pane
		return
	}
	if !e.applyText() {
		return
	}
	e.text = nil
}

// applyText parses the text view back into the document, reporting whether
// it could. An unchanged text is not re-applied, so switching views does
// not fill the undo history with edits nobody made.
func (e *Editor) applyText() bool {
	current, err := textfmt.Format(e.doc.Score())
	if err == nil && string(current) == e.text.text() {
		return true
	}
	// The source name seeds Score.Title when the text has no \title
	// directive, so it must be the piece's own name where one exists:
	// parsing a title-less file under a placeholder would write
	// "\title piece" back into it on the next save.
	name := "piece"
	if e.path != "" {
		name = strings.TrimSuffix(filepath.Base(e.path), filepath.Ext(e.path))
	}
	sc, perr := textfmt.Parse([]byte(e.text.text()), name)
	if perr != nil {
		e.report(fmt.Errorf("%s", gtabProblem(perr)))
		return false
	}
	doc, derr := edit.Open(sc)
	if derr != nil {
		e.report(derr)
		return false
	}
	// A document opened from text starts its own history, so the grid's
	// undo stack does not offer to take back edits made to a different
	// text. The dirty flag is set by hand: edit.Open produces a clean
	// document, and the text really is a change against what is on disk.
	e.doc = doc
	e.doc.MarkSaved()
	e.markDirtyFromText()
	return true
}

// markDirtyFromText records that the piece differs from its file. It is
// spelled as an edit that changes nothing observable rather than as a flag,
// so there is exactly one thing in the system that can set the dirty bit.
func (e *Editor) markDirtyFromText() {
	_ = e.doc.SetTitle(e.doc.Score().Title)
}

// applyTextThen applies the text view and, when it parses, returns to the
// notation and runs act. It is the ONE path a file action may take while
// the text is showing — ctrl+S and the toolbar's save and practice
// controls all go through it, so none of them can act on the document the
// on-screen text has diverged from. Text that will not parse keeps the
// view open with the parser's complaint and does nothing else.
func (e *Editor) applyTextThen(act func()) {
	if !e.applyText() {
		return
	}
	e.text = nil
	act()
}

// escapeText is what Escape means in the text view: back to the grid
// rather than out of the editor — the way out of the application is one
// more Escape, and losing a screenful of typing to a mistaken keypress is
// not a way out. Text that will not parse holds the view open with the
// complaint — except when it is exactly the broken file this editor was
// opened ON (see NewEditorForText) and nothing has been typed over it:
// the user came to look, decided not to fix it today, and holding them
// hostage to a parse error they did not make protects nothing.
func (e *Editor) escapeText() error {
	if e.applyText() {
		e.text = nil
		return nil
	}
	if !e.text.fromFile {
		return nil
	}
	if e.text.text() == e.text.seed {
		return e.leave()
	}
	// Edited-and-broken in a file-seeded pane: the first Escape is
	// refused with the way out named, the second — with nothing typed in
	// between — takes it. Without this, one exploratory keystroke in
	// someone else's broken file re-arms the trap the walk-away above
	// exists to release, and the pane has no undo to escape with.
	if e.text.escArmed {
		e.text = newGtabPaneFromSource([]byte(e.text.seed))
		e.text.fromFile = true
		return e.leave()
	}
	e.text.escArmed = true
	e.report(fmt.Errorf("the text does not parse and was not applied — esc again discards your typing and leaves"))
	return nil
}

// updateText runs the text view for one frame.
func (e *Editor) updateText() error {
	if err := e.textKeys(); err != nil {
		return err
	}
	if e.text == nil {
		return nil
	}
	if s, rem := wheelSteps(e.ptr.wheel); s != 0 {
		e.text.top -= s * 3
		e.ptr.wheel = rem
	}
	if e.ptr.hit(e.hotspots()) {
		return nil
	}
	if e.ptr.pressed {
		e.text.clickAt(e.ptr.x, e.ptr.y)
	}
	e.text.clampCaret()
	return nil
}

// textKeys applies this frame's typing. Escape and F2 leave, and
// everything else edits.
func (e *Editor) textKeys() error {
	p := e.text
	m := readMods()

	if m.ctrl {
		switch {
		case inpututil.IsKeyJustPressed(ebiten.KeyS):
			// Saving from the text view has to apply it first, or the file
			// would be written from the document the text has diverged from.
			e.applyTextThen(func() { e.save() })
			return nil
		}
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF2) {
		e.toggleText()
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF1) {
		// F1 only: the ? key is a printable character here, and a help key
		// that also typed itself into the piece would be worse than none.
		e.helpOpen = true
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		return e.escapeText()
	}

	changed := false
	for _, r := range ebiten.AppendInputChars(nil) {
		if r == '\r' || r == '\n' {
			continue // the Enter key is handled below, once
		}
		if r < 32 && r != '\t' {
			continue
		}
		p.insertRune(r)
		changed = true
	}
	for _, k := range []ebiten.Key{
		ebiten.KeyEnter, ebiten.KeyNumpadEnter, ebiten.KeyBackspace, ebiten.KeyDelete,
		ebiten.KeyLeft, ebiten.KeyRight, ebiten.KeyUp, ebiten.KeyDown,
		ebiten.KeyHome, ebiten.KeyEnd, ebiten.KeyPageUp, ebiten.KeyPageDown,
	} {
		d := inpututil.KeyPressDuration(k)
		if d == 0 || !editorKeyFires(true, d) {
			continue
		}
		if p.applyKey(k) {
			changed = true
		}
	}
	if changed {
		p.reparse()
		// New typing withdraws the second-Escape discard offer: it must
		// only ever throw away exactly the keystrokes the refusal named.
		p.escArmed = false
	}
	return nil
}

// applyKey handles one editing key and reports whether the text changed.
func (p *gtabPane) applyKey(k ebiten.Key) bool {
	switch k {
	case ebiten.KeyEnter, ebiten.KeyNumpadEnter:
		p.splitLine()
		return true
	case ebiten.KeyBackspace:
		return p.backspace()
	case ebiten.KeyDelete:
		return p.deleteForward()
	case ebiten.KeyLeft:
		if p.cx > 0 {
			p.cx--
		} else if p.cy > 0 {
			p.cy--
			p.cx = len(p.lines[p.cy])
		}
	case ebiten.KeyRight:
		if p.cx < len(p.lines[p.cy]) {
			p.cx++
		} else if p.cy < len(p.lines)-1 {
			p.cy, p.cx = p.cy+1, 0
		}
	case ebiten.KeyUp:
		if p.cy > 0 {
			p.cy--
		}
	case ebiten.KeyDown:
		if p.cy < len(p.lines)-1 {
			p.cy++
		}
	case ebiten.KeyHome:
		p.cx = 0
	case ebiten.KeyEnd:
		p.cx = len(p.lines[p.cy])
	case ebiten.KeyPageUp:
		p.cy -= p.visibleLines()
	case ebiten.KeyPageDown:
		p.cy += p.visibleLines()
	}
	p.clampCaret()
	return false
}

func (p *gtabPane) insertRune(r rune) {
	line := p.lines[p.cy]
	out := make([]rune, 0, len(line)+1)
	out = append(out, line[:p.cx]...)
	out = append(out, r)
	out = append(out, line[p.cx:]...)
	p.lines[p.cy] = out
	p.cx++
}

func (p *gtabPane) splitLine() {
	line := p.lines[p.cy]
	head := append([]rune(nil), line[:p.cx]...)
	tail := append([]rune(nil), line[p.cx:]...)
	p.lines[p.cy] = head
	p.lines = append(p.lines, nil)
	copy(p.lines[p.cy+2:], p.lines[p.cy+1:])
	p.lines[p.cy+1] = tail
	p.cy, p.cx = p.cy+1, 0
}

func (p *gtabPane) backspace() bool {
	switch {
	case p.cx > 0:
		line := p.lines[p.cy]
		p.lines[p.cy] = append(line[:p.cx-1], line[p.cx:]...)
		p.cx--
		return true
	case p.cy > 0:
		prev := p.lines[p.cy-1]
		p.cx = len(prev)
		p.lines[p.cy-1] = append(prev, p.lines[p.cy]...)
		p.lines = append(p.lines[:p.cy], p.lines[p.cy+1:]...)
		p.cy--
		return true
	}
	return false
}

func (p *gtabPane) deleteForward() bool {
	line := p.lines[p.cy]
	switch {
	case p.cx < len(line):
		p.lines[p.cy] = append(line[:p.cx], line[p.cx+1:]...)
		return true
	case p.cy < len(p.lines)-1:
		p.lines[p.cy] = append(line, p.lines[p.cy+1]...)
		p.lines = append(p.lines[:p.cy+1], p.lines[p.cy+2:]...)
		return true
	}
	return false
}

// clampCaret keeps the caret on a real character and scrolls to it.
func (p *gtabPane) clampCaret() {
	if len(p.lines) == 0 {
		p.lines = [][]rune{{}}
	}
	if p.cy < 0 {
		p.cy = 0
	}
	if p.cy >= len(p.lines) {
		p.cy = len(p.lines) - 1
	}
	if p.cx < 0 {
		p.cx = 0
	}
	if n := len(p.lines[p.cy]); p.cx > n {
		p.cx = n
	}
	vis := p.visibleLines()
	if p.top > p.cy {
		p.top = p.cy
	}
	if p.top < p.cy-vis+1 {
		p.top = p.cy - vis + 1
	}
	if max := len(p.lines) - vis; p.top > max {
		p.top = max
	}
	if p.top < 0 {
		p.top = 0
	}
}

// visibleLines is how many lines the pane shows.
func (p *gtabPane) visibleLines() int {
	n := int((gtabPaneRect().h - 2*gtPadY) / gtLineH)
	if n < 1 {
		n = 1
	}
	return n
}

// clickAt puts the caret where the pane was clicked.
func (p *gtabPane) clickAt(px, py float64) {
	r := gtabPaneRect()
	if !r.contains(px, py) {
		return
	}
	p.cy = p.top + int((py-r.y-gtPadY)/gtLineH)
	p.clampCaret()
	// The face is monospaced here, so a column is one advance wide.
	col := int((px - r.x - gtPadX - gtGutter) / textWMono(" "))
	p.cx = col
	p.clampCaret()
}

// gtLegendW is the column beside the text that explains the format.
//
// It is the whole reason this view is usable by anybody who has not read
// docs/TEXTFORMAT.md. The pane shows a file written in a small language,
// the language is five rules long, and printing those five rules next to
// it is the difference between an escape hatch and a wall. The pane gives
// up the width without complaint: .gtab lines are short, and the longest
// one in a real piece is a bar of sixteenths at about sixty characters.
const gtLegendW = 340.0

func gtabPaneRect() rect {
	return rect{uiPadX, edGridTop, screenW - 2*uiPadX - gtLegendW - 16, gridBottom() - edGridTop - 22}
}

// gtLegendRect is the explanation column, to the right of the text.
func gtLegendRect() rect {
	p := gtabPaneRect()
	return rect{p.x + p.w + 16, p.y, gtLegendW, p.h}
}

// gtLegend is what the column says: every piece of the format, with what
// it means in a guitarist's words. An empty example starts a section.
//
// One line per entry, with the example in a fixed column, because the
// whole list has to fit beside the text without scrolling — a legend you
// have to scroll is a legend you go and read somewhere else, which is the
// documentation this column exists to save you from opening.
var gtLegend = []struct{ example, means string }{
	{"", "NOTES"},
	{"0.6", "fret 0, string 6 (1 is the thinnest)"},
	{"3.5.8", "fret 3, string 5, an eighth"},
	{"5.4", "length sticks until you change it"},
	{"(0.6 2.5)", "struck together: a chord"},
	{"r", "a rest"},
	{"~0.6", "tied: hold, do not strike again"},
	{"|", "ends a bar"},

	{"", "LENGTHS"},
	{"8", "an eighth (1 2 4 8 16 32)"},
	{"4.", "dotted: half as long again"},
	{"8t", "one of an eighth triplet"},

	{"", "MARKS ON A NOTE"},
	{"5.3h", "hammer-on (p s b v too)"},
	{"7.3x", "dead note, muted"},

	{"", "THE PIECE"},
	{"\\title", "what it is called"},
	{"\\tempo 120", "beats per minute"},
	{"\\time 3/4", "time signature"},
	{"\\tuning", "open strings, low to high"},
	{"\\capo 2", "capo fret"},
	{"\\track", "starts another part"},
	// The two directives the text view is the ONLY place to set. A legend
	// that omits them makes the one control for them undiscoverable.
	{"\\backing", "this part is accompaniment, not yours"},
	{"\\program 25", "instrument voice (General MIDI)"},
	{"//", "a note to yourself"},
}

// gtLegendCol is where the meanings start, so every one of them lines up
// however wide its example is.
const gtLegendCol = 84.0

// Legend spacing. A row is a shade tighter than it used to be because the
// list now carries every one of the parser's directives — \backing and
// \program were missing, and the text view is the only place either can
// be set — and the whole list still has to fit beside the text without
// scrolling.
const (
	gtLegendTop    = 34.0 // first row, under the column's own heading
	gtLegendRowH   = 16.0
	gtLegendSecGap = 2.0 // extra air before a section heading
)

// A gtLegendLine is one placed legend row: a section heading when example
// is empty.
type gtLegendLine struct {
	example, means string
	y              float64
}

// gtLegendLayout places every legend row inside r, stopping at rows that
// would run into the footnote at the bottom. Drawing and the fit test
// both read it, so the test measures what is actually drawn rather than a
// copy of the arithmetic that can drift away from it.
func gtLegendLayout(r rect) []gtLegendLine {
	var out []gtLegendLine
	y := r.y + gtLegendTop
	for _, row := range gtLegend {
		if y > r.y+r.h-14 {
			break
		}
		if row.example == "" {
			y += gtLegendSecGap
			out = append(out, gtLegendLine{means: row.means, y: y})
			y += gtLegendRowH
			continue
		}
		out = append(out, gtLegendLine{example: row.example, means: row.means, y: y})
		y += gtLegendRowH
	}
	return out
}

// drawGtabLegend paints the explanation column.
func (e *Editor) drawGtabLegend(screen *ebiten.Image) {
	r := gtLegendRect()
	drawPanel(screen, r, colPanel, colPanelEdge)
	drawText(screen, "WHAT THE TEXT MEANS", r.x+12, r.y+8, colInferred)

	for _, l := range gtLegendLayout(r) {
		if l.example == "" {
			drawTextSmall(screen, l.means, r.x+12, l.y, colGroupCap)
			continue
		}
		drawTextSmall(screen, l.example, r.x+12, l.y, colSounding)
		drawTextSmall(screen, truncateW(l.means, r.w-gtLegendCol-22), r.x+gtLegendCol, l.y, colDim)
	}
	drawTextSmall(screen, "the notation view has controls for the common ones",
		r.x+12, r.y+r.h-18, colHint)
}

func (e *Editor) drawTextPane(screen *ebiten.Image) {
	p := e.text
	r := gtabPaneRect()
	drawPanel(screen, r, colPanel, colPanelEdge)
	e.drawGtabLegend(screen)

	vis := p.visibleLines()
	adv := textWMono(" ")
	for i := 0; i < vis && p.top+i < len(p.lines); i++ {
		ln := p.top + i
		y := r.y + gtPadY + float64(i)*gtLineH
		drawTextRightMono(screen, itoa(ln+1), r.x+gtPadX+gtGutter-8, y, colGtGutter)
		line := string(p.lines[ln])
		col := colNote
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
			col = colDim
		} else if strings.HasPrefix(trimmed, "\\") {
			col = colInferred
		}
		drawTextMono(screen, line, r.x+gtPadX+gtGutter, y, col)
		if ln == p.cy {
			cx := r.x + gtPadX + gtGutter + float64(p.cx)*adv
			vector.DrawFilledRect(screen, float32(cx), float32(y), 1.5, float32(gtLineH-2), colGtCaret, false)
		}
	}

	// The parser's verdict, under the pane. It is the whole point of
	// editing the text here rather than in a text editor: the answer to
	// "did I break it" is always on screen.
	y := r.y + r.h + 4
	if p.ok {
		drawText(screen, "looks good — "+itoa(p.bars)+" bars, "+itoa(p.notes)+" notes", uiPadX, y, colGtOK)
		return
	}
	drawText(screen, truncateW(p.status, screenW-2*uiPadX), uiPadX, y, colMiss)
}

// drawTextRightMono draws mono text ending at x — the line-number gutter.
func drawTextRightMono(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawTextMono(dst, s, x-textWMono(s), y, col)
}

// itoa is strconv.Itoa under a shorter name, for the drawing code.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// textBindings is the control table while the text view is showing.
func (e *Editor) textBindings() []helpBinding {
	return []helpBinding{
		{Group: "editing", Keys: "type", Hint: "type to edit", Desc: "The text is the file that will be saved"},
		{Group: "editing", Keys: "arrows / home / end", Desc: "Move the caret"},
		{Group: "editing", Keys: "enter / backspace / del", Desc: "Split a line, and delete either way"},
		{Group: "editing", Keys: "click", Desc: "Put the caret where you click"},
		{Group: "session", Keys: "F2 or esc", Hint: "F2 back to the notation", Desc: "Read the text back and return to the staff"},
		{Group: "session", Keys: "ctrl+S", Hint: "ctrl+S save", Desc: "Parse the text and save the piece"},
		// F1 alone: ? is a printable character in this view and just types
		// itself, so advertising it here would be a lie.
		{Group: "session", Keys: "F1", Hint: "F1 help", Desc: "This key-binding list"},
	}
}
