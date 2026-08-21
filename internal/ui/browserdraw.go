package ui

// The start screen's geometry and painting.
//
// Every rectangle the screen uses comes out of layout(), and both the
// drawing and the hit testing read it — so what the eye sees and what the
// mouse finds are the same numbers by construction rather than by two
// people remembering to change two files. It is computed per frame
// because it is not constant: the hint strip collapses, and the panes
// take back the room it gives up.

import (
	"fmt"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// A browserAction is one button on the action row.
type browserAction struct {
	id      string
	glyph   glyphID
	label   string
	key     string
	primary bool
	enabled bool
	act     func(*Browser)
}

// browserLayout is one frame's geometry.
type browserLayout struct {
	hint    rect   // the getting-started strip, or its collapsed stub
	hintBtn rect   // hide / show
	steps   []rect // the strip's clickable steps
	actions []rect // one per entry of actionList
	panes   []rect // one per entry of paneOrder
	rows    int    // rows that fit in a pane
	// statusY is the top of the band reserved for errors and import
	// warnings, which the panes stop above whether or not it is in use.
	statusY float64
	// paneWidth is every pane's width; they divide the page evenly.
	paneWidth float64
}

// layout computes the frame's geometry.
func (b *Browser) layout() browserLayout {
	var l browserLayout
	y := uiBodyTop

	hintH := brwHintShutH
	if b.hintOpen {
		hintH = brwHintOpenH
	}
	l.hint = rect{uiPadX, y, screenW - 2*uiPadX, hintH}
	l.hintBtn = rect{l.hint.x + l.hint.w - 108, y + 6, 100, 24}
	if b.hintOpen {
		steps := b.stepList()
		if n := len(steps); n > 0 {
			w := (l.hint.w - 24 - float64(n-1)*10) / float64(n)
			for i := range steps {
				l.steps = append(l.steps, rect{l.hint.x + 12 + float64(i)*(w+10), y + 34, w, 40})
			}
		}
	}
	y += hintH + 12

	acts := b.actionList()
	x := uiPadX
	for _, a := range acts {
		w := textW(a.label) + 34
		if a.glyph != glyphNone {
			w += glyphBox + 8
		}
		if kw := textWSmall(a.key) + 34; kw > w {
			w = kw
		}
		l.actions = append(l.actions, rect{x, y, w, brwActionH})
		x += w + 10
	}
	y += brwActionH + 14

	// The status stack sits between the panes and the footer, and always
	// has its room reserved: panes that grew into it when there was
	// nothing to report would jump every time an import warned.
	l.statusY = uiFooterY - 10 - brwStatusLines*18
	paneH := l.statusY - 12 - y
	l.rows = int((paneH - brwPaneHeadH) / brwRowH)
	if l.rows < 1 {
		l.rows = 1
	}

	order := b.paneOrder()
	n := float64(len(order))
	l.paneWidth = (screenW - 2*uiPadX - (n-1)*brwPaneGap) / n
	for i := range order {
		l.panes = append(l.panes, rect{
			uiPadX + float64(i)*(l.paneWidth+brwPaneGap), y, l.paneWidth, paneH,
		})
	}
	return l
}

// rowsPerPane is how many rows a pane shows, which the selection clamping
// needs outside a draw.
func (b *Browser) rowsPerPane() int { return b.layout().rows }

// actionList is the row of buttons under the hint: everything the screen
// can do that is not "open the thing I have selected".
func (b *Browser) actionList() []browserAction {
	_, canEdit := b.editableSelection()
	return []browserAction{
		{id: "open", glyph: glyphFolder, label: "Open a file…", key: "O", primary: true, enabled: b.openDialog != nil,
			act: func(b *Browser) { b.launchOpenDialog("") }},
		{id: "new", glyph: glyphNewPiece, label: "Write a new piece", key: "N", enabled: b.newPiece != nil,
			act: (*Browser).startNewPiece},
		{id: "edit", glyph: glyphPencil, label: "Edit selected", key: "E", enabled: canEdit,
			act: (*Browser).editSelected},
		{id: "settings", glyph: glyphSliders, label: "Settings", key: "S", enabled: b.settings != nil,
			act: func(b *Browser) { b.settings() }},
	}
}

// --- hit testing --------------------------------------------------------

// paneHit maps a cursor position to a pane and a row index within it,
// reporting whether it landed on one at all. A click in a pane's blank
// space still lands ON the pane — it moves the focus there without
// changing which row is selected, which is what makes clicking a heading
// to switch panes work.
func (b *Browser) paneHit(l browserLayout, x, y float64) (pane browserPane, row int, onRow, onPane bool) {
	order := b.paneOrder()
	for i, r := range l.panes {
		if !r.contains(x, y) {
			continue
		}
		p := order[i]
		rel := y - (r.y + brwPaneHeadH)
		if rel < 0 {
			return p, 0, false, true
		}
		row = b.saved[p].top + int(rel/brwRowH)
		if p == b.focus {
			row = b.top + int(rel/brwRowH)
		}
		if row < 0 || row >= len(b.panes[p]) {
			return p, 0, false, true
		}
		return p, row, true, true
	}
	return 0, 0, false, false
}

// handleMouse routes one frame's pointer state.
func (b *Browser) handleMouse(pt pointer) {
	l := b.layout()
	b.hoverIdx = -1
	b.hoverPane = -1
	if p, row, onRow, onPane := b.paneHit(l, pt.x, pt.y); onPane {
		b.hoverPane = p
		if onRow {
			b.hoverIdx = row
		}
		if pt.wheel != 0 {
			b.wheelAcc += pt.wheel
			var steps int
			steps, b.wheelAcc = wheelSteps(b.wheelAcc)
			if steps != 0 {
				// Scrolling a pane the keyboard is not on scrolls THAT
				// pane: the wheel follows the cursor, which is the whole
				// point of having one.
				if p != b.focus {
					b.focusPane(p)
				}
				b.scrollBy(-steps * 3)
			}
		}
		if pt.pressed {
			// A press on another pane is one gesture doing two things: it
			// moves the focus there and picks the row under the cursor. It
			// must not ALSO open that row — focusPane restores the pane's
			// parked selection, and when the click happened to land on it
			// (row 0 of a never-visited pane, say), click() would read one
			// press as the promised two.
			entered := p != b.focus
			if entered {
				b.focusPane(p)
			}
			if onRow {
				if entered {
					b.setSel(row)
				} else {
					b.click(row)
				}
			}
		}
		return
	}
	if !pt.pressed {
		return
	}
	if pt.over(l.hintBtn) {
		b.toggleHint()
		return
	}
	for i, r := range l.steps {
		if pt.over(r) {
			b.activateStep(i)
			return
		}
	}
	for i, a := range b.actionList() {
		if i < len(l.actions) && pt.over(l.actions[i]) {
			if a.enabled && a.act != nil {
				a.act(b)
			}
			return
		}
	}
}

// click selects a row, and opens it when it was already selected — the
// second click of a double-click, without the timing.
func (b *Browser) click(i int) {
	if i == b.sel {
		b.activate()
		return
	}
	b.setSel(i)
}

// --- drawing ------------------------------------------------------------

func (b *Browser) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)
	b.anim.tick()
	l := b.layout()
	drawHeader(screen, "guitarTutor", b.headerStatus(), colDim)
	b.drawHint(screen, l)
	b.drawActions(screen, l)
	b.drawPanes(screen, l)
	b.drawStatus(screen, l)
	drawFooter(screen, b.hintLine())

	if b.helpOpen {
		drawHelpOverlay(screen, "START SCREEN KEYS", b.browserBindings(), browserHelpFootnote)
	}
}

// headerStatus is the line on the right of the header: what the screen is
// for, or how much is in it.
func (b *Browser) headerStatus() string {
	total := 0
	for _, p := range b.paneOrder() {
		total += len(b.panes[p])
	}
	if total == 0 {
		return "a practice companion for guitarists and horn players"
	}
	return fmt.Sprintf("%d pieces to hand", total)
}

func (b *Browser) drawActions(screen *ebiten.Image, l browserLayout) {
	dt := uiFrameSeconds()
	for i, a := range b.actionList() {
		if i >= len(l.actions) {
			break
		}
		r := l.actions[i]
		style := btnNormal
		switch {
		case !a.enabled:
			style = btnDisabled
		case a.primary:
			style = btnPrimary
		}
		av := b.anim.step("action:"+a.id, b.ptr.over(r) && a.enabled, b.ptr.down, dt)
		drawButton(screen, r, a.glyph, a.label, a.key, style, av)
	}
}

func (b *Browser) drawPanes(screen *ebiten.Image, l browserLayout) {
	order := b.paneOrder()
	for i, p := range order {
		b.drawPane(screen, l, l.panes[i], p)
	}
}

// drawPane paints one list: its heading, its rows, and whatever it has to
// say when it has none.
func (b *Browser) drawPane(screen *ebiten.Image, l browserLayout, r rect, p browserPane) {
	focused := p == b.focus
	fillRounded(screen, r, colPanel)
	edge := colPanelEdge
	if focused {
		edge = colInferred
	}
	strokeRounded(screen, r, edge)

	titleCol := colDim
	if focused {
		titleCol = colInferred
	}
	drawText(screen, paneTitles[p], r.x+12, r.y+7, titleCol)
	if n := len(b.panes[p]); n > 0 {
		drawTextRight(screen, fmt.Sprintf("%d", n), r.x+r.w-12, r.y+7, colHint)
	}
	vector.StrokeLine(screen, float32(r.x+10), float32(r.y+brwPaneHeadH-4),
		float32(r.x+r.w-10), float32(r.y+brwPaneHeadH-4), 1, colPanelEdge, false)

	entries := b.panes[p]
	if len(entries) == 0 {
		for i, line := range wrapTextW(paneEmpty[p], r.w-28) {
			drawTextSmall(screen, line, r.x+14, r.y+brwPaneHeadH+8+float64(i)*16, colHint)
		}
		b.drawPaneNote(screen, r, p)
		return
	}

	top, sel := b.saved[p].top, b.saved[p].sel
	if focused {
		top, sel = b.top, b.sel
	}
	for row := 0; row < l.rows; row++ {
		i := top + row
		if i >= len(entries) {
			break
		}
		e := entries[i]
		y := r.y + brwPaneHeadH + float64(row)*brwRowH
		rowRect := rect{r.x + 6, y, r.w - 12, brwRowH - 4}
		if bg, ok := b.rowBG(p, i, sel); ok {
			fillRounded(screen, rowRect, bg)
		}
		nameCol, sub := colNote, e.sub
		switch {
		case e.missing:
			nameCol, sub = colMissing, "missing — press Del to forget"
		case e.problem:
			nameCol = colClose
		case !focused:
			nameCol = colHUD
		}
		drawTextScaled(screen, truncateWScaled(e.name, rowRect.w-16, brwNameScale),
			rowRect.x+8, y+1, brwNameScale, nameCol)
		drawTextSmall(screen, ellipsizeWSmall(sub, rowRect.w-16), rowRect.x+8, y+22, colDim)
	}
	if len(entries) > l.rows {
		drawTextSmall(screen, fmt.Sprintf("%d–%d of %d", top+1,
			min(top+l.rows, len(entries)), len(entries)),
			r.x+12, r.y+r.h-18, colHint)
	}
	b.drawPaneNote(screen, r, p)
}

// drawPaneNote paints the footer a pane carries: for the library, where
// the folder is or what went wrong reading it.
func (b *Browser) drawPaneNote(screen *ebiten.Image, r rect, p browserPane) {
	if p != paneLibrary {
		return
	}
	note, isErr := b.libraryNote()
	if note == "" {
		return
	}
	col := colHint
	if isErr {
		col = colMiss
	}
	drawTextSmall(screen, ellipsizeWSmall(note, r.w-24), r.x+12, r.y+r.h-34, col)
}

// rowBG is a row's background: the selection in the focused pane, a
// dimmer one in the pane that is merely remembered, and the hover.
func (b *Browser) rowBG(p browserPane, i, sel int) (color.RGBA, bool) {
	if i == sel && len(b.panes[p]) > 0 {
		if p == b.focus {
			return colFocus, true
		}
		return colHover, true
	}
	if p == b.hoverPane && i == b.hoverIdx {
		return colHover, true
	}
	return color.RGBA{}, false
}

// drawStatus paints the last open's error and importer warnings under the
// panes.
func (b *Browser) drawStatus(screen *ebiten.Image, l browserLayout) {
	const maxWarn = brwStatusWarnings
	width := float64(screenW - 2*uiPadX)
	y := l.statusY
	if b.errMsg != "" {
		drawText(screen, ellipsizeW(b.errMsg, width), uiPadX, y, colMiss)
		y += 20
	}
	if head := b.warnsHeading(); head != "" {
		drawText(screen, truncateW(head, width), uiPadX, y, colClose)
		y += 18
	}
	for i, w := range b.warns {
		if i == maxWarn {
			drawText(screen, fmt.Sprintf("… and %d more warnings", len(b.warns)-maxWarn), uiPadX, y, colClose)
			break
		}
		drawText(screen, truncateW("warning: "+w, width), uiPadX, y, colClose)
		y += 18
	}
}

// warnsHeading names the piece over the warning lines, or "" when there
// is nothing to head. The warnings can still be on screen minutes after
// the open, when the user comes back from practising; without the
// piece's name over them they are orphaned text about nothing in
// particular. An open can also fail WITH warnings (the importer read
// enough to complain before the error), and then the piece did not open,
// so the heading must not say it did — warnsFailed records that at open
// time, because errMsg is cleared by unrelated actions while the
// warnings stay.
func (b *Browser) warnsHeading() string {
	if len(b.warns) == 0 || b.warnsFrom == "" {
		return ""
	}
	if b.warnsFailed {
		return "warnings from " + b.warnsFrom + ":"
	}
	return b.warnsFrom + " opened with warnings:"
}
