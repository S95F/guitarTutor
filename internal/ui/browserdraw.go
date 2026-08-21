package ui

import (
	"fmt"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type browserAction struct {
	id      string
	glyph   glyphID
	label   string
	key     string
	primary bool
	enabled bool
	act     func(*Browser)
}

type browserLayout struct {
	hint    rect
	hintBtn rect
	steps   []rect
	actions []rect
	panes   []rect
	rows    int

	statusY float64

	paneWidth float64
}

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

func (b *Browser) rowsPerPane() int { return b.layout().rows }

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

				if p != b.focus {
					b.focusPane(p)
				}
				b.scrollBy(-steps * 3)
			}
		}
		if pt.pressed {

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

func (b *Browser) click(i int) {
	if i == b.sel {
		b.activate()
		return
	}
	b.setSel(i)
}

func (b *Browser) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)
	b.anim.tick()
	l := b.layout()
	drawHeader(screen, "musicTutor", b.headerStatus(), colDim)
	b.drawHint(screen, l)
	b.drawActions(screen, l)
	b.drawPanes(screen, l)
	b.drawStatus(screen, l)
	drawFooter(screen, b.hintLine())

	if b.helpOpen {
		drawHelpOverlay(screen, "START SCREEN KEYS", b.browserBindings(), browserHelpFootnote)
	}
}

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

func (b *Browser) warnsHeading() string {
	if len(b.warns) == 0 || b.warnsFrom == "" {
		return ""
	}
	if b.warnsFailed {
		return "warnings from " + b.warnsFrom + ":"
	}
	return b.warnsFrom + " opened with warnings:"
}
