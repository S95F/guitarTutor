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
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Glyph metrics of the one face the application draws with. basicfont is
// a fixed-width 7x13 bitmap, so a character count is a pixel count — that
// is what makes every centring and wrapping calculation here exact rather
// than approximate.
const (
	glyphW = 7.0
	glyphH = 13.0
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

// textW is the pixel width of a string at scale 1.
func textW(s string) float64 { return glyphW * float64(len(s)) }

// textWScaled is the pixel width of a string drawn at scale.
func textWScaled(s string, scale float64) float64 { return textW(s) * scale }

// centreX is the x that centres s within [x, x+w) at scale 1.
func centreX(s string, x, w float64) float64 { return x + (w-textW(s))/2 }

// centreXScaled is centreX for text drawn at scale.
func centreXScaled(s string, x, w, scale float64) float64 {
	return x + (w-textWScaled(s, scale))/2
}

// fitChars is how many characters fit in w pixels at scale 1.
func fitChars(w float64) int { return int(w / glyphW) }

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

// drawPanel paints a control-sized background with a border.
func drawPanel(dst *ebiten.Image, r rect, fill, edge color.RGBA) {
	vector.DrawFilledRect(dst, float32(r.x), float32(r.y), float32(r.w), float32(r.h), fill, false)
	vector.StrokeRect(dst, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 1, edge, false)
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

// chip metrics. The height fits a label and its key hint; widths are
// computed per chip from the longer of the two strings.
const (
	chipH    = 34.0
	chipPadX = 10.0
	chipGap  = 8.0
)

// chipW is the width a chip needs for its two lines of text.
func chipW(c chipState) float64 {
	w := textW(c.label)
	if kw := textW(c.key); kw > w {
		w = kw
	}
	return w + 2*chipPadX
}

// drawChip paints one toggle: filled and outlined when engaged, hollow
// when not, and greyed when the binding is unavailable. hover lifts the
// fill so the control the next click would reach is obvious.
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
	drawText(dst, c.label, centreX(c.label, r.x, r.w), r.y+5, text)
	if c.key != "" {
		kc := colBarline
		if hover && !c.disabled {
			kc = colDim
		}
		drawText(dst, c.key, centreX(c.key, r.x, r.w), r.y+19, kc)
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

// wrapText breaks s into lines of at most width characters, on spaces.
// basicfont is fixed-width, so a character count is a pixel count.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			out = append(out, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(out, line)
}

// truncate shortens a string to max characters, keeping the front — the
// right choice for names.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// ellipsize shortens a string to max characters, keeping the tail — the
// right choice for paths, where the last folders identify it.
func ellipsize(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[len(s)-max:]
	}
	return "..." + s[len(s)-(max-3):]
}
