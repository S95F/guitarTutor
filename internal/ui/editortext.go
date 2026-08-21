package ui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/edit"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

const (
	gtLineH  = 17.0
	gtPadX   = 12.0
	gtPadY   = 10.0
	gtGutter = 46.0
)

var (
	colGtGutter = color.RGBA{70, 70, 84, 255}
	colGtCaret  = color.RGBA{255, 220, 120, 255}
	colGtOK     = color.RGBA{110, 200, 140, 255}
)

type gtabPane struct {
	lines [][]rune
	cx    int
	cy    int
	top   int

	status string
	ok     bool

	seed     string
	fromFile bool

	escArmed bool

	bars, notes int
}

func newGtabPane(doc *edit.Doc) (*gtabPane, error) {
	src, err := textfmt.Format(doc.Score())
	if err != nil {
		return nil, err
	}
	return newGtabPaneFromSource(src), nil
}

func newGtabPaneFromSource(src []byte) *gtabPane {
	p := &gtabPane{}
	for _, line := range strings.Split(strings.TrimRight(string(src), "\r\n"), "\n") {

		p.lines = append(p.lines, []rune(strings.TrimRight(line, "\r")))
	}
	if len(p.lines) == 0 {
		p.lines = [][]rune{{}}
	}
	p.reparse()
	p.seed = p.text()
	return p
}

func (p *gtabPane) text() string {
	out := make([]string, len(p.lines))
	for i, l := range p.lines {
		out[i] = string(l)
	}
	return strings.Join(out, "\n") + "\n"
}

func (p *gtabPane) reparse() {
	sc, err := textfmt.Parse([]byte(p.text()), "piece")
	if err != nil {
		p.ok, p.status, p.bars, p.notes = false, textfmt.ProblemLine(err), 0, 0
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

func (e *Editor) applyText() bool {
	current, err := textfmt.Format(e.doc.Score())
	if err == nil && string(current) == e.text.text() {
		return true
	}

	name := "piece"
	if e.path != "" {
		name = strings.TrimSuffix(filepath.Base(e.path), filepath.Ext(e.path))
	}
	sc, perr := textfmt.Parse([]byte(e.text.text()), name)
	if perr != nil {
		e.report(fmt.Errorf("%s", textfmt.ProblemLine(perr)))
		return false
	}
	doc, derr := edit.Open(sc)
	if derr != nil {
		e.report(derr)
		return false
	}

	e.doc = doc
	e.doc.MarkSaved()
	if e.text != nil && e.text.fromFile && e.text.text() == e.text.seed {
		return true
	}
	e.markDirtyFromText()
	return true
}

func (e *Editor) markDirtyFromText() {
	_ = e.doc.SetTitle(e.doc.Score().Title)
}

func (e *Editor) applyTextThen(act func()) {
	if !e.applyText() {
		return
	}
	e.text = nil
	act()
}

func (e *Editor) escapeText() error {
	if e.applyText() {
		e.text = nil
		return nil
	}
	if !e.text.fromFile {
		return nil
	}

	if e.text.text() == e.text.seed || e.text.escArmed {
		return e.leave()
	}
	e.text.escArmed = true
	e.report(fmt.Errorf("the text does not parse and was not applied — esc again discards your typing and leaves"))
	return nil
}

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

func (e *Editor) textKeys() error {
	p := e.text
	m := readMods()

	if m.ctrl {
		switch {
		case inpututil.IsKeyJustPressed(ebiten.KeyS):

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

		e.helpOpen = true
		return nil
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		return e.escapeText()
	}

	changed := false
	for _, r := range ebiten.AppendInputChars(nil) {
		if r == '\r' || r == '\n' {
			continue
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

		p.escArmed = false
	}
	return nil
}

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

func (p *gtabPane) visibleLines() int {
	n := int((gtabPaneRect().h - 2*gtPadY) / gtLineH)
	if n < 1 {
		n = 1
	}
	return n
}

func (p *gtabPane) clickAt(px, py float64) {
	r := gtabPaneRect()
	if !r.contains(px, py) {
		return
	}
	p.cy = p.top + int((py-r.y-gtPadY)/gtLineH)
	p.clampCaret()

	col := int((px - r.x - gtPadX - gtGutter) / textWMono(" "))
	p.cx = col
	p.clampCaret()
}

const gtLegendW = 340.0

func gtabPaneRect() rect {
	return rect{uiPadX, edGridTop, screenW - 2*uiPadX - gtLegendW - 16, gridBottom() - edGridTop - 22}
}

func gtLegendRect() rect {
	p := gtabPaneRect()
	return rect{p.x + p.w + 16, p.y, gtLegendW, p.h}
}

var gtLegend = []struct{ example, means string }{
	{"", "NOTES"},
	{"0.6", "fret 0, string 6 (1 is the thinnest)"},
	{"3.5.8", "fret 3, string 5, an eighth"},
	{"5.4", "length sticks until you change it"},
	{"(0.6 2.5)", "struck together: a chord"},
	{"D5.8", "on a wind part: written pitch"},
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
	{"D5l", "slurred, on a wind part (s b v too)"},

	{"", "THE PIECE"},
	{"\\title", "what it is called"},
	{"\\tempo 120", "beats per minute"},
	{"\\time 3/4", "time signature"},
	{"\\tuning", "open strings, low to high"},
	{"\\capo 2", "capo fret"},
	{"\\track", "starts another part"},

	{"\\instrument", "a wind part: soprano sax, flute…"},
	{"\\backing", "this part is accompaniment, not yours"},
	{"\\program 25", "instrument voice (General MIDI)"},
	{"//", "a note to yourself"},
}

const gtLegendCol = 84.0

const (
	gtLegendTop    = 34.0
	gtLegendRowH   = 14.5
	gtLegendSecGap = 1.0
)

type gtLegendLine struct {
	example, means string
	y              float64
}

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

var gtLegendLines = gtLegendLayout(gtLegendRect())

func (e *Editor) drawGtabLegend(screen *ebiten.Image) {
	r := gtLegendRect()
	drawPanel(screen, r, colPanel, colPanelEdge)
	drawText(screen, "WHAT THE TEXT MEANS", r.x+12, r.y+8, colInferred)

	for _, l := range gtLegendLines {
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
		drawTextRightMono(screen, strconv.Itoa(ln+1), r.x+gtPadX+gtGutter-8, y, colGtGutter)
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

	y := r.y + r.h + 4
	if p.ok {
		drawText(screen, "looks good — "+strconv.Itoa(p.bars)+" bars, "+strconv.Itoa(p.notes)+" notes", uiPadX, y, colGtOK)
		return
	}
	drawText(screen, truncateW(p.status, screenW-2*uiPadX), uiPadX, y, colMiss)
}

func drawTextRightMono(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawTextMono(dst, s, x-textWMono(s), y, col)
}

func (e *Editor) textBindings() []helpBinding {

	back := helpBinding{Group: "session", Keys: "F2 or esc", Hint: "F2 back to the notation", Desc: "Read the text back and return to the staff"}
	if e.text != nil && e.text.fromFile {
		back.Desc = "F2 reads the repaired text onto the staff; esc leaves the piece while it will not parse"
	}
	return []helpBinding{
		{Group: "editing", Keys: "type", Hint: "type to edit", Desc: "The text is the file that will be saved"},
		{Group: "editing", Keys: "arrows / home / end", Desc: "Move the caret"},
		{Group: "editing", Keys: "enter / backspace / del", Desc: "Split a line, and delete either way"},
		{Group: "editing", Keys: "click", Desc: "Put the caret where you click"},
		back,
		{Group: "session", Keys: "ctrl+S", Hint: "ctrl+S save", Desc: "Parse the text and save the piece"},

		{Group: "session", Keys: "F1", Hint: "F1 help", Desc: "This key-binding list"},
	}
}
