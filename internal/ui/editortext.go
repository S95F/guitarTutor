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
	"image/color"
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
	p := &gtabPane{}
	for _, line := range strings.Split(strings.TrimRight(string(src), "\n"), "\n") {
		p.lines = append(p.lines, []rune(line))
	}
	if len(p.lines) == 0 {
		p.lines = [][]rune{{}}
	}
	p.reparse()
	return p, nil
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
		p.ok, p.status, p.bars, p.notes = false, err.Error(), 0, 0
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
	sc, perr := textfmt.Parse([]byte(e.text.text()), "piece")
	if perr != nil {
		e.report(perr)
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
	e.say("applied the text")
	return true
}

// markDirtyFromText records that the piece differs from its file. It is
// spelled as an edit that changes nothing observable rather than as a flag,
// so there is exactly one thing in the system that can set the dirty bit.
func (e *Editor) markDirtyFromText() {
	_ = e.doc.SetTitle(e.doc.Score().Title)
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
			if e.applyText() {
				e.text = nil
				e.save()
			}
			return nil
		}
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF2) {
		e.toggleText()
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		// Escape leaves the text view for the grid rather than the editor:
		// the way out of the application is one more Escape, and losing a
		// screenful of typing to a mistaken keypress is not a way out.
		if e.applyText() {
			e.text = nil
		}
		return nil
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

func gtabPaneRect() rect {
	return rect{uiPadX, edGridTop, screenW - 2*uiPadX, gridBottom() - edGridTop - 22}
}

func (e *Editor) drawTextPane(screen *ebiten.Image) {
	p := e.text
	r := gtabPaneRect()
	drawPanel(screen, r, colPanel, colPanelEdge)

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
		drawText(screen, itoa(p.bars)+" bars, "+itoa(p.notes)+" notes — this parses", uiPadX, y, colGtOK)
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
		{Group: "session", Keys: "F2 or esc", Hint: "F2 back to the grid", Desc: "Parse the text and go back to the notation"},
		{Group: "session", Keys: "ctrl+S", Hint: "ctrl+S save", Desc: "Parse the text and save the piece"},
		{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
	}
}
