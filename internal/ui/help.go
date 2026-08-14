package ui

// One control table per screen, two renderings each: the one-line hint
// in the footer and the full overlay behind ?/F1.
//
// The practice view already worked this way — a single table generating
// both, so the hint line and the overlay could not drift apart — but the
// machinery was private to it, and the start screen and the settings
// screen each hand-wrote a hint string with no overlay at all. Pressing ?
// on two of the three screens did nothing, which is the worst possible
// answer for a key whose whole job is to tell you what the keys do.
//
// The rendering lives here and the tables stay with their screens, since
// only a screen knows which of its bindings are available right now.

import (
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// A helpBinding is one resolved row of a control table: what the key is,
// how the footer says it, what the overlay says about it, and whether it
// currently does anything. Screens produce these from their own tables,
// so "currently" is answered where the answer is known.
type helpBinding struct {
	// Group is the overlay's section heading. Consecutive rows sharing a
	// group are drawn under one heading, in table order.
	Group string
	// Keys is the key label for the overlay, e.g. "shift+1..9".
	Keys string
	// Hint is the complete fragment for the one-line footer, e.g.
	// "space play/pause". Empty leaves the binding out of the footer; the
	// overlay still lists it.
	Hint string
	// Desc is the overlay's description.
	Desc string
	// Off marks a binding that does nothing right now. It is greyed in
	// the overlay rather than hidden — a key that vanishes is a mystery,
	// a key that is visibly unavailable is an explanation — and dropped
	// from the footer, which has no room to explain itself.
	Off bool
}

// A helpSection is one heading of the overlay with its rows.
type helpSection struct {
	Name string
	Rows []helpBinding
}

// helpSections slices a resolved table into the overlay's sections, in
// table order.
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

// hintLineOf builds the footer summary from a resolved table, dropping
// bindings that have no short form or are not currently available.
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

// helpKeyPressed reports whether this frame asks for the overlay. Shift
// is not required for "?" here: on the practice view the plain slash is
// already free, and demanding the exact shifted glyph is a good way to
// make a help key feel broken.
func helpKeyPressed() bool {
	return inpututil.IsKeyJustPressed(ebiten.KeyF1) || inpututil.IsKeyJustPressed(ebiten.KeySlash)
}

// helpDismissed reports whether this frame closes an open overlay: any of
// the keys that open it, escape, or a click anywhere. The click matters —
// an overlay reachable by mouse and dismissible only by keyboard is a
// trap for the person most likely to have opened it.
func helpDismissed(p pointer) bool {
	return helpKeyPressed() || inpututil.IsKeyJustPressed(ebiten.KeyEscape) || p.pressed
}

// Overlay layout. The practice table is the biggest — 24 rows in seven
// groups — and it has to clear the dismiss line at the bottom of a 720
// pixel window, so these are tight by necessity: a row is one pixel
// taller than a line of body text, and a heading gets a shade less than
// a full line. Anything more generous and the longest table runs off the
// screen; anything less and rows touch.
const (
	helpX = 200.0
	// helpKeysX and helpDescX are the two columns. The gap between them
	// fits the widest key label in the tables ("drag and drop").
	helpKeysX  = 220.0
	helpDescX  = 420.0
	helpTopY   = 72.0
	helpRowH   = 17.0
	helpHeadH  = 16.0 // a group heading's own line
	helpGroupH = 9.0  // extra air between groups
)

// helpCard is the panel the table sits on, centred in the window.
func helpCard() rect {
	return rect{160, 12, screenW - 320, screenH - 24}
}

// drawHelpOverlay paints a resolved table over everything else. footnote
// is an optional line under the table for anything the rows cannot say —
// the practice view uses it to explain its track chips.
func drawHelpOverlay(dst *ebiten.Image, title string, rows []helpBinding, footnote string) {
	// A dimmed screen behind an opaque card, rather than one translucent
	// wash over everything. A wash alone never quite hides bright things:
	// at any alpha low enough to read as an overlay, the title and the
	// lit chips underneath still ghost through the table, and a modal
	// that owns the whole keyboard should not look half-there. The card
	// also matches the rounded panels every other screen is built from.
	vector.DrawFilledRect(dst, 0, 0, screenW, screenH, colHelpDim, false)
	drawPanel(dst, helpCard(), colPanel, colPanelEdge)
	drawTextScaled(dst, title, helpX, 24, uiTitleScl, colNote)

	y := helpTopY
	for _, g := range helpSections(rows) {
		drawText(dst, strings.ToUpper(g.Name), helpX, y, colSounding)
		y += helpHeadH
		for _, b := range g.Rows {
			col, desc := colNote, b.Desc
			if b.Off {
				col, desc = colBarline, b.Desc+"  (not available now)"
			}
			drawText(dst, b.Keys, helpKeysX, y, col)
			drawText(dst, truncateW(desc, screenW-uiPadX-helpDescX), helpDescX, y, col)
			y += helpRowH
		}
		y += helpGroupH
	}
	if footnote != "" {
		drawText(dst, footnote, helpX, y+6, colHUD)
		y += 6 + uiTextH
	}
	// The dismiss line sits at the foot of the window, but never on top
	// of a table that ran long: it flows instead once the content
	// reaches it.
	dismissY := screenH - 40.0
	if y+8 > dismissY {
		dismissY = y + 8
	}
	drawText(dst, "esc, F1, ? or a click closes this", helpX, dismissY, colHUD)
}
