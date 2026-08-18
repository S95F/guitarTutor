package ui

// Pointer input, shared by every screen.
//
// Ebitengine reports the mouse through package-level functions that only
// answer inside a running game loop, so nothing that reads them can be
// driven from a test. The screens therefore never consult them while
// deciding anything: once per Update they fill a pointer value from the
// loop, and every decision after that is a plain method taking that
// value. A test builds a pointer by hand and gets exactly the behaviour
// the game loop would have produced — the same split ui.go and browser.go
// already use for the keyboard.

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// A rect is a hit region in logical screen pixels. Screens build them in
// the same places they draw, so what the user sees and what the mouse
// finds cannot drift apart.
type rect struct{ x, y, w, h float64 }

// contains reports whether a point is inside r. The right and bottom
// edges are exclusive, so two rects that share an edge never both claim
// the same pixel.
func (r rect) contains(px, py float64) bool {
	return px >= r.x && px < r.x+r.w && py >= r.y && py < r.y+r.h
}

// empty reports whether r can never contain anything. The zero rect is
// the "no region" value, and hit tests must not match it.
func (r rect) empty() bool { return r.w <= 0 || r.h <= 0 }

// A pointer is one frame's mouse state, as the game loop saw it. All
// fields are plain data so a test can construct any situation directly.
type pointer struct {
	x, y float64
	// down is the left button's held state and pressed is its down edge
	// this frame. Screens act on the press so that the same gesture can
	// begin a drag; the release is observed as down going false.
	down    bool
	pressed bool
	// right is the right button's press edge. It is the second way to
	// solo a track, for people who do not reach for shift.
	right bool
	// wheel is this frame's vertical scroll, positive away from the user.
	wheel float64
}

// forcedCursor pins the pointer, for the screenshot tool.
//
// Hover states, the animation and the tooltips are the parts of this
// interface that only exist while a cursor is over something, which makes
// them the parts that cannot be reviewed from a screenshot — and they are
// exactly the parts most likely to be drawn in the wrong place, since
// nothing else in a frame depends on where the mouse is. Eight lines to
// make them photographable is a better trade than shipping a tooltip
// nobody has ever seen.
var forcedCursor *pointer

// ForceCursor pins the pointer at a position for every following frame,
// as though the mouse were resting there. It exists for tools/uishot and
// has no effect on a real run, which never calls it.
func ForceCursor(x, y float64, down bool) {
	forcedCursor = &pointer{x: x, y: y, down: down}
}

// readPointer samples Ebitengine's mouse for this frame. It is the only
// function here that touches the game loop, and it makes no decisions.
func readPointer() pointer {
	if forcedCursor != nil {
		return *forcedCursor
	}
	x, y := ebiten.CursorPosition()
	_, wy := ebiten.Wheel()
	return pointer{
		x:       float64(x),
		y:       float64(y),
		down:    ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft),
		pressed: inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft),
		right:   inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonRight),
		wheel:   wy,
	}
}

// over reports whether the cursor is inside r.
func (p pointer) over(r rect) bool { return !r.empty() && r.contains(p.x, p.y) }

// A hotspot is one clickable region with the action it performs. Screens
// build a slice of these while drawing and then hand the slice to the
// pointer, which keeps hit testing in one place rather than repeating a
// contains check per control.
type hotspot struct {
	r rect
	// on fires on a left press inside r.
	on func()
	// onRight fires on a right press inside r; nil means the right button
	// does nothing here.
	onRight func()
}

// hit dispatches p against the first hotspot that contains it and reports
// whether one did. First match wins, so a slice built front-to-back —
// overlays appended after the controls they cover — resolves overlaps the
// way the drawing order implies.
func (p pointer) hit(spots []hotspot) bool {
	for _, s := range spots {
		if !p.over(s.r) {
			continue
		}
		switch {
		case p.pressed && s.on != nil:
			s.on()
			return true
		case p.right && s.onRight != nil:
			s.onRight()
			return true
		}
		// A hotspot under the cursor swallows the click even when it has
		// no action for this button: the control is there, it just does
		// not answer that press, and whatever is behind it must not.
		return p.pressed || p.right
	}
	return false
}

// wheelSteps splits an accumulated wheel delta into whole notches and the
// remainder to carry into the next frame, so a trackpad reporting
// fractional deltas still scrolls smoothly. It is the shared form of what
// the browser did on its own.
func wheelSteps(acc float64) (steps int, rem float64) {
	steps = int(acc)
	return steps, acc - float64(steps)
}
