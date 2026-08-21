package ui

import (
	"math"
	"testing"
)

func TestEllipseIsAnEllipse(t *testing.T) {
	for _, c := range []struct{ cx, cy, rx, ry, rot float64 }{
		{0.5, 0.5, 0.25, 0.25, 0},
		{headRX, headRY, headRX, headRY, 0},
		{0.5, 0.5, headRX, headRY, headRot},
		{0.5, 0.5, wholeRX, wholeRY, wholeRot},
		{0.5, 0.5, counterRX, counterRY, counterRot},
		{0.4, 0.6, 0.3, 0.1, 1.1},
	} {
		start, segs := ellipseArcs(c.cx, c.cy, c.rx, c.ry, c.rot)

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

			mx, my := cubicAt(from, s, 0.5)
			if got := on(mx, my); math.Abs(got-1) > 1e-3 {
				t.Errorf("%+v segment %d: the midpoint is at %.6f, want 1 — "+
					"the arc bows out of its own quadrant", c, i, got)
			}
			from = end
		}
	}
}

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

func TestStemMeetsTheNotehead(t *testing.T) {
	const headCX = 0.35
	x := stemX(headCX)

	reach := math.Hypot(headRX*math.Cos(headRot), headRY*math.Sin(headRot))
	if gap := x - (headCX + reach); gap > 0 {
		t.Errorf("the stem starts %.4f clear of the head", gap)
	}
	if overlap := (headCX + reach) - x; overlap > wStem {
		t.Errorf("the stem is buried %.4f into the head, more than its own width", overlap)
	}
}
