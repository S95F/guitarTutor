package ui

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

type rect struct{ x, y, w, h float64 }

func (r rect) contains(px, py float64) bool {
	return px >= r.x && px < r.x+r.w && py >= r.y && py < r.y+r.h
}

func (r rect) empty() bool { return r.w <= 0 || r.h <= 0 }

type pointer struct {
	x, y float64

	down    bool
	pressed bool

	right bool

	wheel float64
}

var forcedCursor *pointer

func ForceCursor(x, y float64, down bool) {
	forcedCursor = &pointer{x: x, y: y, down: down}
}

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

func (p pointer) over(r rect) bool { return !r.empty() && r.contains(p.x, p.y) }

type hotspot struct {
	r rect

	on func()

	onRight func()
}

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

		return p.pressed || p.right
	}
	return false
}

func wheelSteps(acc float64) (steps int, rem float64) {
	steps = int(acc)
	return steps, acc - float64(steps)
}
