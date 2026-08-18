package ui

// Tooltips: the name of the thing under the cursor.
//
// An icon without a name is a rebus. It is worth having anyway — a row of
// symbols is read at a glance where a row of words has to be parsed, and
// once learned an icon is faster forever — but the learning has to happen
// somewhere, and "somewhere" is a small panel that appears when you rest
// on it. That is the trade every drawing application makes, and it is why
// none of them label their brushes.
//
// The tooltip also carries the KEY. That is the part most applications
// leave out and the part that matters most here: the toolbar exists to
// teach the keyboard, because a piece is written at typing speed and
// nobody writes one by clicking. A tooltip that says "Hammer-on" and
// stops has taught you where a button is; one that says "Hammer-on · h"
// has taught you not to need it.

import (
	"github.com/hajimehoshi/ebiten/v2"
)

const (
	// tipDelay is how long the cursor rests before the name appears. Long
	// enough that sweeping across a toolbar shows nothing, short enough
	// that stopping on something feels like asking.
	tipDelay = 0.35
	// tipGap is the space between the control and the panel under it.
	tipGap  = 8.0
	tipPadX = 9.0
	tipH    = 30.0
)

// A tips tracks what the cursor is resting on. The zero value shows
// nothing; a screen offers each control as it draws it and then draws the
// winner last, over everything.
type tips struct {
	// id, text and key describe the control the cursor is currently on.
	id, text, key string
	at            rect
	// dwell is how long it has been there, in seconds.
	dwell float64
	// seen is set by offer and cleared by draw, so a frame in which
	// nothing was offered forgets the tooltip rather than leaving it
	// hanging over a control that is no longer there.
	seen bool
}

// offer reports that a control is under the cursor. The first one offered
// in a frame wins, which matches the drawing order: a control drawn later
// is on top, and screens offer as they draw, so the last one offered is
// the topmost — hence the check for an id already claimed this frame.
func (t *tips) offer(id, text, key string, at rect, hovered bool, dt float64) {
	if !hovered || text == "" {
		if t.id == id {
			// The cursor left the control the tooltip belongs to.
			t.id, t.dwell = "", 0
		}
		return
	}
	if t.id != id {
		t.id, t.dwell = id, 0
	}
	t.text, t.key, t.at = text, key, at
	t.dwell += dt
	t.seen = true
}

// hide forgets whatever the cursor was resting on. A screen calls it when
// something modal has taken over, so the tooltip does not come straight
// back mid-dwell when the modal closes.
func (t *tips) hide() { t.id, t.dwell, t.seen = "", 0, false }

// visible reports whether the cursor has rested long enough.
func (t *tips) visible() bool { return t.id != "" && t.dwell >= tipDelay && t.seen }

// draw paints the panel, and clears the per-frame flag. It has to be the
// last thing a screen draws: a tooltip under anything is a tooltip nobody
// can read.
func (t *tips) draw(dst *ebiten.Image) {
	show := t.visible()
	t.seen = false
	if !show {
		return
	}
	label := t.text
	if t.key != "" {
		label += "   " + t.key
	}
	w := textW(t.text) + 2*tipPadX
	if t.key != "" {
		w = textW(t.text) + textWSmall(t.key) + 2*tipPadX + 12
	}

	// Under the control, and nudged back on screen at the right edge
	// rather than clipped: a tooltip that runs off the window is worse
	// than no tooltip, because it looks like a rendering fault.
	x := t.at.x
	if x+w > screenW-uiPadX {
		x = screenW - uiPadX - w
	}
	if x < uiPadX {
		x = uiPadX
	}
	y := t.at.y + t.at.h + tipGap
	if y+tipH > screenH-uiPadX {
		y = t.at.y - tipGap - tipH
	}

	r := rect{x, y, w, tipH}
	drawPanel(dst, r, colTipBG, colTipEdge)
	drawText(dst, t.text, r.x+tipPadX, r.y+6, colNote)
	if t.key != "" {
		drawTextSmall(dst, t.key, r.x+tipPadX+textW(t.text)+12, r.y+8, colSounding)
	}
}
