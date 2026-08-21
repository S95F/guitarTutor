package ui

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	uiPadX    = 24.0
	uiHeaderY = 18.0

	uiHeaderH  = 64.0
	uiTitleScl = 2.0
	uiFooterY  = screenH - 26.0
	uiBodyTop  = uiHeaderH + 8.0
	uiLineH    = 18.0
	uiRowH     = 26.0
	uiSectionH = 26.0
)

var (
	colPanel     = color.RGBA{24, 24, 32, 255}
	colPanelEdge = color.RGBA{46, 46, 60, 255}
	colHover     = color.RGBA{38, 40, 52, 255}
	colFocus     = color.RGBA{38, 66, 104, 255}
	colDim       = color.RGBA{132, 132, 148, 255}

	colHint   = color.RGBA{124, 124, 138, 255}
	colOn     = color.RGBA{46, 96, 66, 255}
	colOnEdge = color.RGBA{80, 200, 130, 255}

	colOnHover = color.RGBA{62, 126, 88, 255}

	colTipBG   = color.RGBA{14, 14, 20, 255}
	colTipEdge = color.RGBA{78, 78, 96, 255}

	colGroupCap = color.RGBA{110, 110, 128, 255}

	colDisabled = color.RGBA{98, 98, 112, 255}
)

func centreX(s string, x, w float64) float64 { return x + (w-textW(s))/2 }

func centreXScaled(s string, x, w, scale float64) float64 {
	return x + (w-textWScaled(s, scale))/2
}

func drawTextRight(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawText(dst, s, x-textW(s), y, col)
}

func drawHeader(dst *ebiten.Image, title, status string, statusCol color.RGBA) {
	drawTextScaled(dst, title, uiPadX, uiHeaderY, uiTitleScl, colNote)
	if status != "" {
		drawTextRight(dst, status, screenW-uiPadX, uiHeaderY+8, statusCol)
	}
	vector.StrokeLine(dst, uiPadX, uiHeaderH-10, screenW-uiPadX, uiHeaderH-10, 1, colPanelEdge, false)
}

func drawFooter(dst *ebiten.Image, hint string) {
	drawText(dst, hint, uiPadX, uiFooterY, colHint)
}

func drawSection(dst *ebiten.Image, y *float64, title string) {
	drawText(dst, title, uiPadX, *y, colInferred)
	vector.StrokeLine(dst, uiPadX, float32(*y)+16, screenW-uiPadX, float32(*y)+16, 1, colBarline, false)
	*y += uiSectionH
}

const uiCornerRadius = 5.0

func roundedRectPath(r rect) *vector.Path {
	rad := float32(uiCornerRadius)
	if half := float32(r.w) / 2; rad > half {
		rad = half
	}
	if half := float32(r.h) / 2; rad > half {
		rad = half
	}
	x0, y0 := float32(r.x), float32(r.y)
	x1, y1 := float32(r.x+r.w), float32(r.y+r.h)
	var p vector.Path
	p.MoveTo(x0+rad, y0)
	p.ArcTo(x1, y0, x1, y1, rad)
	p.ArcTo(x1, y1, x0, y1, rad)
	p.ArcTo(x0, y1, x0, y0, rad)
	p.ArcTo(x0, y0, x1, y0, rad)
	p.Close()
	return &p
}

func fillRounded(dst *ebiten.Image, r rect, col color.RGBA) {
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(col)
	vector.FillPath(dst, roundedRectPath(r), nil, op)
}

func strokeRounded(dst *ebiten.Image, r rect, col color.RGBA) {
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(col)
	so := &vector.StrokeOptions{Width: 1}
	vector.StrokePath(dst, roundedRectPath(r), so, op)
}

func drawPanel(dst *ebiten.Image, r rect, fill, edge color.RGBA) {
	fillRounded(dst, r, fill)
	strokeRounded(dst, r, edge)
}

type chipState struct {
	label string

	key      string
	on       bool
	disabled bool
}

const (
	chipH    = 36.0
	chipPadX = 11.0
	chipGap  = 8.0
)

func chipW(c chipState) float64 {
	w := textW(c.label)
	if kw := textWSmall(c.key); kw > w {
		w = kw
	}
	return w + 2*chipPadX
}

func drawChip(dst *ebiten.Image, r rect, c chipState, av animValues) {
	fill, edge, text := colPanel, colPanelEdge, colHUD
	switch {
	case c.disabled:
		drawPanel(dst, r, colBG, colBarline)
		drawText(dst, c.label, centreX(c.label, r.x, r.w), r.y+3, colBarline)
		if c.key != "" {
			drawTextSmall(dst, c.key, r.x+(r.w-textWSmall(c.key))/2, r.y+20, colBarline)
		}
		return
	case c.on:
		fill, edge, text = colOn, colOnEdge, colNote
		fill = lerpCol(fill, colOnHover, av.hover)
	default:
		fill = lerpCol(fill, colHover, av.hover)
		edge = lerpCol(edge, colDim, av.hover)
	}
	r = av.animate(r)
	drawPanel(dst, r, fill, edge)
	drawText(dst, c.label, centreX(c.label, r.x, r.w), r.y+3, text)
	if c.key != "" {

		kc := colDim
		if c.on {
			kc = colHUD
		}
		drawTextSmall(dst, c.key, r.x+(r.w-textWSmall(c.key))/2, r.y+20,
			lerpCol(kc, colNote, av.hover))
	}
	drawFlash(dst, r, av)
}

const (
	iconBtnSize = 32.0
	iconBtnGap  = 4.0
	iconGrpGap  = 22.0
	iconCapH    = 14.0
)

func drawIconGlyphButton(dst *ebiten.Image, r rect, id glyphID, on, disabled bool, av animValues) {
	fill, edge, ink := colPanel, colPanelEdge, colHUD
	switch {
	case disabled:
		drawPanel(dst, r, colBG, colBarline)
		drawGlyph(dst, id, glyphInset(r), colDisabled)
		return
	case on:
		fill, edge, ink = colOn, colOnEdge, colNote
		fill = lerpCol(fill, colOnHover, av.hover)
	default:
		fill = lerpCol(fill, colHover, av.hover)
		edge = lerpCol(edge, colDim, av.hover)
		ink = lerpCol(ink, colNote, av.hover)
	}
	r = av.animate(r)
	drawPanel(dst, r, fill, edge)
	drawGlyph(dst, id, glyphInset(r), ink)
	drawFlash(dst, r, av)
}

func glyphInset(r rect) rect {
	m := (r.h - glyphBox) / 2
	return rect{r.x + (r.w-glyphBox)/2, r.y + m, glyphBox, glyphBox}
}

func drawGroupCaption(dst *ebiten.Image, text string, x, y float64) {
	drawTextSmall(dst, text, x, y, colGroupCap)
}

type buttonStyle int

const (
	btnNormal buttonStyle = iota
	btnPrimary
	btnDisabled
)

func drawButton(dst *ebiten.Image, r rect, id glyphID, label, key string, style buttonStyle, av animValues) {

	textX := func(r rect, s string, scale float64) float64 {
		w := textW(s)
		if id == glyphNone {
			return r.x + (r.w-w)/2
		}
		return r.x + (r.w-w-glyphBox-8)/2 + glyphBox + 8
	}

	avail := r.w - 22
	if id != glyphNone {
		avail -= glyphBox
	}
	if style == btnDisabled {
		drawPanel(dst, r, colBG, colBarline)
		l := truncateW(label, avail)
		x := textX(r, l, 1)
		if id != glyphNone {
			drawGlyph(dst, id, rect{x - glyphBox - 8, r.y + 6, glyphBox, glyphBox}, colDisabled)
		}
		drawText(dst, l, x, r.y+7, colBarline)
		if key != "" {
			drawTextSmall(dst, key, r.x+(r.w-textWSmall(key))/2, r.y+24, colBarline)
		}
		return
	}
	fill, edge, text := colPanel, colPanelEdge, colHUD
	if style == btnPrimary {
		fill, edge, text = colFocus, colInferred, colNote
	}
	fill = lerpCol(fill, lerpCol(fill, colNote, 0.16), av.hover)
	edge = lerpCol(edge, colNote, av.hover)
	r = av.animate(r)
	drawPanel(dst, r, fill, edge)

	label = truncateW(label, avail)
	x := textX(r, label, 1)
	if key == "" {
		if id != glyphNone {
			drawGlyph(dst, id, rect{x - glyphBox - 8, r.y + (r.h-glyphBox)/2, glyphBox, glyphBox}, text)
		}
		drawText(dst, label, x, r.y+(r.h-uiTextH)/2+1, text)
	} else {
		if id != glyphNone {
			drawGlyph(dst, id, rect{x - glyphBox - 8, r.y + 5, glyphBox, glyphBox}, text)
		}
		drawText(dst, label, x, r.y+6, text)
		drawTextSmall(dst, key, r.x+(r.w-textWSmall(key))/2, r.y+23, lerpCol(colDim, colNote, av.hover))
	}
	drawFlash(dst, r, av)
}

type iconKind int

const (
	iconPlay iconKind = iota
	iconPause
	iconPrevBar
	iconNextBar
	iconToStart
)

func drawIconButton(dst *ebiten.Image, r rect, k iconKind, av animValues) {
	fill := lerpCol(colPanel, colHover, av.hover)
	edge := lerpCol(colPanelEdge, colDim, av.hover)
	r = av.animate(r)
	drawPanel(dst, r, fill, edge)
	cx, cy := r.x+r.w/2, r.y+r.h/2
	drawIcon(dst, k, cx, cy, colNote)
	drawFlash(dst, r, av)
}

func drawIcon(dst *ebiten.Image, k iconKind, cx, cy float64, col color.RGBA) {
	const s = 6.0
	switch k {
	case iconPlay:
		fillTriangle(dst, cx-s*0.7, cy-s, cx-s*0.7, cy+s, cx+s*0.9, cy, col)
	case iconPause:
		vector.DrawFilledRect(dst, float32(cx-s+1), float32(cy-s), 3, float32(2*s), col, false)
		vector.DrawFilledRect(dst, float32(cx+2), float32(cy-s), 3, float32(2*s), col, false)
	case iconPrevBar:
		fillTriangle(dst, cx+s*0.9, cy-s, cx+s*0.9, cy+s, cx-s*0.3, cy, col)
		vector.DrawFilledRect(dst, float32(cx-s), float32(cy-s), 2, float32(2*s), col, false)
	case iconNextBar:
		fillTriangle(dst, cx-s*0.9, cy-s, cx-s*0.9, cy+s, cx+s*0.3, cy, col)
		vector.DrawFilledRect(dst, float32(cx+s-2), float32(cy-s), 2, float32(2*s), col, false)
	case iconToStart:
		vector.DrawFilledRect(dst, float32(cx-s), float32(cy-s), 2, float32(2*s), col, false)
		fillTriangle(dst, cx+s*0.6, cy-s, cx+s*0.6, cy+s, cx-s*0.6, cy, col)
	}
}

func fillTriangle(dst *ebiten.Image, x0, y0, x1, y1, x2, y2 float64, col color.RGBA) {
	var p vector.Path
	p.MoveTo(float32(x0), float32(y0))
	p.LineTo(float32(x1), float32(y1))
	p.LineTo(float32(x2), float32(y2))
	p.Close()
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(col)
	vector.FillPath(dst, &p, nil, op)
}
