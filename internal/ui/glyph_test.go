package ui

// The drawn symbols are geometry written by hand. Most of it can only be
// judged by looking at it (tools/uishot -screen glyphs), but the parts
// that are arithmetic are checked here, because the one arithmetic fault
// the set had was invisible for as long as every notehead was a solid
// blob four pixels across.

import (
	"math"
	"testing"
)

// TestEllipseIsAnEllipse: the four cubic segments used to be paired with
// the NEXT quadrant's control points, so each one bowed a quarter turn
// round from where it belonged and the curve came out as a four-cusped
// lozenge. Every notehead in the application was drawn with it. The fault
// only became visible when a hollow head was cut with a real counter, at
// which point a whole note rendered as a four-pointed star.
func TestEllipseIsAnEllipse(t *testing.T) {
	for _, c := range []struct{ cx, cy, rx, ry, rot float64 }{
		{0.5, 0.5, 0.25, 0.25, 0},              // a circle
		{headRX, headRY, headRX, headRY, 0},    // axis-aligned, off centre
		{0.5, 0.5, headRX, headRY, headRot},    // a notehead
		{0.5, 0.5, wholeRX, wholeRY, wholeRot}, // a semibreve
		{0.5, 0.5, counterRX, counterRY, counterRot},
		{0.4, 0.6, 0.3, 0.1, 1.1}, // a steep one, for luck
	} {
		start, segs := ellipseArcs(c.cx, c.cy, c.rx, c.ry, c.rot)
		// On(p) is 1 when p is exactly on the ellipse: the quadratic form
		// of the rotated ellipse, evaluated at p.
		on := func(x, y float64) float64 {
			dx, dy := x-c.cx, y-c.cy
			sin, cos := math.Sin(-c.rot), math.Cos(-c.rot)
			u, v := dx*cos-dy*sin, dx*sin+dy*cos
			return (u/c.rx)*(u/c.rx) + (v/c.ry)*(v/c.ry)
		}
		if got := on(start[0], start[1]); math.Abs(got-1) > 1e-9 {
			t.Errorf("%+v: the start point is at %.6f, want 1", c, got)
		}
		from := start
		for i, s := range segs {
			end := [2]float64{s[4], s[5]}
			if got := on(end[0], end[1]); math.Abs(got-1) > 1e-9 {
				t.Errorf("%+v segment %d: the end point is at %.6f, want 1", c, i, got)
			}
			// The midpoint of a cubic quarter-arc built with the circle
			// constant sits on the curve to about 2.7 parts in 10,000.
			// A segment bowing into the wrong quadrant misses by more
			// than a whole radius, so the tolerance is not delicate.
			mx, my := cubicAt(from, s, 0.5)
			if got := on(mx, my); math.Abs(got-1) > 1e-3 {
				t.Errorf("%+v segment %d: the midpoint is at %.6f, want 1 — "+
					"the arc bows out of its own quadrant", c, i, got)
			}
			from = end
		}
	}
}

// cubicAt evaluates one segment at t.
func cubicAt(from [2]float64, s [6]float64, t float64) (float64, float64) {
	p := [4][2]float64{from, {s[0], s[1]}, {s[2], s[3]}, {s[4], s[5]}}
	u := 1 - t
	w := [4]float64{u * u * u, 3 * u * u * t, 3 * u * t * t, t * t * t}
	var x, y float64
	for i := range p {
		x += w[i] * p[i][0]
		y += w[i] * p[i][1]
	}
	return x, y
}

// TestStemMeetsTheNotehead: the stem is placed by arithmetic on the head's
// own constants, so a change to the head cannot leave the stem floating
// beside it or buried in it.
func TestStemMeetsTheNotehead(t *testing.T) {
	const headCX = 0.35
	x := stemX(headCX)
	// The head's reach along its long axis, which is where a stem joins.
	reach := math.Hypot(headRX*math.Cos(headRot), headRY*math.Sin(headRot))
	if gap := x - (headCX + reach); gap > 0 {
		t.Errorf("the stem starts %.4f clear of the head", gap)
	}
	if overlap := (headCX + reach) - x; overlap > wStem {
		t.Errorf("the stem is buried %.4f into the head, more than its own width", overlap)
	}
}
