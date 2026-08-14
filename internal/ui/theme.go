package ui

// The shared look: metrics, text measurement, and the handful of drawing
// primitives every screen is built from.
//
// Before this file each screen invented its own margins, its own section
// headings and its own "how wide is this string" arithmetic (7*len(s),
// spelt out at a dozen call sites). Moving from the start screen to
// settings to the practice view therefore meant three different layouts
// of the same idea. Everything here exists so that a heading, a row, a
// toggle or a page margin looks and behaves the same wherever it appears.

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Page metrics. Every screen lays out against these, so the eye finds the
// same left edge and the same footer line on all of them.
const (
	uiPadX     = 24.0            // page margin, left and right
	uiHeaderY  = 18.0            // title baseline
	uiHeaderH  = 56.0            // height of the header band
	uiTitleScl = 2.0             // title text scale
	uiFooterY  = screenH - 26.0  // key-hint line baseline
	uiBodyTop  = uiHeaderH + 8.0 // where a screen's own content may start
	uiLineH    = 18.0            // one line of body text
	uiRowH     = 26.0            // one focusable row
	uiSectionH = 26.0            // a section heading plus its rule
)

var (
	colPanel     = color.RGBA{24, 24, 32, 255}    // pane and control fill
	colPanelEdge = color.RGBA{46, 46, 60, 255}    // pane and control border
	colHover     = color.RGBA{38, 40, 52, 255}    // control under the cursor
	colFocus     = color.RGBA{38, 66, 104, 255}   // focused row or selection
	colFocusDim  = color.RGBA{34, 38, 48, 255}    // selection in an unfocused pane
	colDim       = color.RGBA{132, 132, 148, 255} // secondary text
	colOn        = color.RGBA{46, 96, 66, 255}    // an engaged toggle's fill
	colOnEdge    = color.RGBA{80, 200, 130, 255}  // an engaged toggle's border
)

// centreX is the x that centres body text within [x, x+w). The width is
// the shaper's real advance (font.go), so centring survives the move to
// a proportional face.
func centreX(s string, x, w float64) float64 { return x + (w-textW(s))/2 }

// centreXScaled is centreX for heading text drawn at scale.
func centreXScaled(s string, x, w, scale float64) float64 {
	return x + (w-textWScaled(s, scale))/2
}

// drawTextRight draws s ending at x.
func drawTextRight(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawText(dst, s, x-textW(s), y, col)
}

// drawHeader paints the band every screen starts with: the screen's name
// on the left, a status line on the right, and a rule under both. Passing
// the same shape everywhere is what makes moving between screens feel like
// staying in one application.
func drawHeader(dst *ebiten.Image, title, status string, statusCol color.RGBA) {
	drawTextScaled(dst, title, uiPadX, uiHeaderY, uiTitleScl, colNote)
	if status != "" {
		drawTextRight(dst, status, screenW-uiPadX, uiHeaderY+8, statusCol)
	}
	vector.StrokeLine(dst, uiPadX, uiHeaderH-10, screenW-uiPadX, uiHeaderH-10, 1, colPanelEdge, false)
}

// drawFooter paints the one-line key hint at the bottom of every screen.
func drawFooter(dst *ebiten.Image, hint string) {
	drawText(dst, hint, uiPadX, uiFooterY, colBarline)
}

// drawSection paints a section heading with its rule and advances the
// layout cursor past it.
func drawSection(dst *ebiten.Image, y *float64, title string) {
	drawText(dst, title, uiPadX, *y, colInferred)
	vector.StrokeLine(dst, uiPadX, float32(*y)+16, screenW-uiPadX, float32(*y)+16, 1, colBarline, false)
	*y += uiSectionH
}

// uiCornerRadius rounds every control the UI draws. One constant, so
// chips, buttons, panes and rows all carry the same curvature and read
// as one family.
const uiCornerRadius = 5.0

// roundedRectPath traces r with uiCornerRadius corners. The radius is
// clamped so degenerate rects (thinner than two radii) stay valid.
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

// fillRounded fills a rounded rectangle.
func fillRounded(dst *ebiten.Image, r rect, col color.RGBA) {
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(col)
	vector.FillPath(dst, roundedRectPath(r), nil, op)
}

// strokeRounded outlines a rounded rectangle.
func strokeRounded(dst *ebiten.Image, r rect, col color.RGBA) {
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(col)
	so := &vector.StrokeOptions{Width: 1}
	vector.StrokePath(dst, roundedRectPath(r), so, op)
}

// drawPanel paints a control-sized background with a border — the one
// shape every chip, button and row is made of.
func drawPanel(dst *ebiten.Image, r rect, fill, edge color.RGBA) {
	fillRounded(dst, r, fill)
	strokeRounded(dst, r, edge)
}

// A chipState is how a toggle chip renders: what it says, whether it is
// engaged, and whether it can be used at all right now.
type chipState struct {
	label string
	// key is the keyboard binding, drawn small under the label so the
	// mouse teaches the keyboard rather than replacing it.
	key      string
	on       bool
	disabled bool
}

// chip metrics. The height fits a body-size label over a small key hint;
// widths are computed per chip from the longer of the two strings.
const (
	chipH    = 36.0
	chipPadX = 11.0
	chipGap  = 8.0
)

// chipW is the width a chip needs for its two lines of text.
func chipW(c chipState) float64 {
	w := textW(c.label)
	if kw := textWSmall(c.key); kw > w {
		w = kw
	}
	return w + 2*chipPadX
}

// drawChip paints one toggle: filled and outlined when engaged, hollow
// when not, and greyed when the binding is unavailable. hover lifts the
// fill so the control the next click would reach is obvious. The key
// hint sits under the label at caption size — two full body lines do not
// fit a chip, which is the point: the hint is a whisper, not a label.
func drawChip(dst *ebiten.Image, r rect, c chipState, hover bool) {
	fill, edge, text := colPanel, colPanelEdge, colHUD
	switch {
	case c.disabled:
		fill, edge, text = colBG, colBarline, colBarline
	case c.on:
		fill, edge, text = colOn, colOnEdge, colNote
	case hover:
		fill, edge = colHover, colDim
	}
	drawPanel(dst, r, fill, edge)
	drawText(dst, c.label, centreX(c.label, r.x, r.w), r.y+3, text)
	if c.key != "" {
		kc := colBarline
		if hover && !c.disabled {
			kc = colDim
		}
		drawTextSmall(dst, c.key, r.x+(r.w-textWSmall(c.key))/2, r.y+20, kc)
	}
}

// iconKind names the transport glyphs. They are drawn as shapes rather
// than letters because basicfont has no arrows, and a row of "<<" and ">"
// reads as text to be parsed instead of buttons to be pressed.
type iconKind int

const (
	iconPlay iconKind = iota
	iconPause
	iconPrevBar
	iconNextBar
	iconToStart
)

// drawIconButton paints one transport button: a panel with a glyph in it.
func drawIconButton(dst *ebiten.Image, r rect, k iconKind, hover bool) {
	fill, edge := colPanel, colPanelEdge
	if hover {
		fill, edge = colHover, colDim
	}
	drawPanel(dst, r, fill, edge)
	cx, cy := r.x+r.w/2, r.y+r.h/2
	drawIcon(dst, k, cx, cy, colNote)
}

// drawIcon paints a transport glyph centred on (cx, cy).
func drawIcon(dst *ebiten.Image, k iconKind, cx, cy float64, col color.RGBA) {
	const s = 6.0 // half-height of a glyph
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

// fillTriangle fills the triangle through three points. vector.FillPath
// draws white and multiplies by the colour scale, so the colour is applied
// there rather than passed in.
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
