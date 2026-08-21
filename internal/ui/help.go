package ui

import (
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type helpBinding struct {
	Group string

	Keys string

	Hint string

	Desc string

	Off bool

	Explained bool
}

type helpSection struct {
	Name string
	Rows []helpBinding
}

func helpSections(rows []helpBinding) []helpSection {
	var out []helpSection
	for _, b := range rows {
		if n := len(out); n > 0 && out[n-1].Name == b.Group {
			out[n-1].Rows = append(out[n-1].Rows, b)
			continue
		}
		out = append(out, helpSection{Name: b.Group, Rows: []helpBinding{b}})
	}
	return out
}

func hintLineOf(rows []helpBinding) string {
	parts := make([]string, 0, len(rows))
	for _, b := range rows {
		if b.Hint == "" || b.Off {
			continue
		}
		parts = append(parts, b.Hint)
	}
	return strings.Join(parts, "   ")
}

func helpKeyPressed() bool {
	return inpututil.IsKeyJustPressed(ebiten.KeyF1) || inpututil.IsKeyJustPressed(ebiten.KeySlash)
}

func helpDismissed(p pointer) bool {
	return helpKeyPressed() || inpututil.IsKeyJustPressed(ebiten.KeyEscape) || p.pressed
}

const (
	helpX = 200.0

	helpKeysX  = 220.0
	helpDescX  = 420.0
	helpTopY   = 72.0
	helpRowH   = 17.0
	helpHeadH  = 16.0
	helpGroupH = 7.0

	helpFootGap = 4.0
)

func helpCard() rect {
	return rect{160, 12, screenW - 320, screenH - 24}
}

const (
	editorHelpFootnote   = "rest the cursor on any toolbar symbol and it names itself, and gives its key"
	browserHelpFootnote  = "tab and the arrow keys move between the three lists; the wheel scrolls whichever one the cursor is over"
	practiceHelpFootnote = "track chips:  click to mute    right-click to solo    the blue edge marks the track the notation is showing"
)

type helpFlow struct {
	footY    float64
	dismissY float64
	bottom   float64
}

func helpLayout(rows []helpBinding, footnote string) helpFlow {
	y := helpTopY
	for _, g := range helpSections(rows) {
		y += helpHeadH + float64(len(g.Rows))*helpRowH + helpGroupH
	}
	f := helpFlow{footY: -1}
	if footnote != "" {
		f.footY = y + helpFootGap
		y += helpFootGap + uiTextH
	}

	f.dismissY = screenH - 40
	if y+8 > f.dismissY {
		f.dismissY = y + 8
	}
	f.bottom = f.dismissY + uiTextH
	return f
}

func overlayDesc(b helpBinding) string {
	if b.Off && !b.Explained {
		return b.Desc + "  (not available now)"
	}
	return b.Desc
}

func drawHelpOverlay(dst *ebiten.Image, title string, rows []helpBinding, footnote string) {

	vector.DrawFilledRect(dst, 0, 0, screenW, screenH, colHelpDim, false)
	drawPanel(dst, helpCard(), colPanel, colPanelEdge)
	drawTextScaled(dst, title, helpX, 24, uiTitleScl, colNote)

	y := helpTopY
	for _, g := range helpSections(rows) {
		drawText(dst, strings.ToUpper(g.Name), helpX, y, colSounding)
		y += helpHeadH
		for _, b := range g.Rows {
			col, desc := colNote, overlayDesc(b)
			if b.Off {
				col = colBarline
			}
			drawText(dst, b.Keys, helpKeysX, y, col)
			drawText(dst, truncateW(desc, screenW-uiPadX-helpDescX), helpDescX, y, col)
			y += helpRowH
		}
		y += helpGroupH
	}
	f := helpLayout(rows, footnote)
	if footnote != "" {
		drawText(dst, footnote, helpX, f.footY, colHUD)
	}
	drawText(dst, "esc, F1, ? or a click closes this", helpX, f.dismissY, colHUD)
}
