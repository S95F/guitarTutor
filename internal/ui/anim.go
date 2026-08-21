package ui

import (
	"image/color"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

const (
	animHoverRate = 16.0
	animPressRate = 34.0

	animFlashSeconds = 0.28

	animForgetFrames = 120

	animLift = 1.5
	animSink = 2.0
)

type animValues struct {
	hover float64
	press float64
	flash float64
}

type animState struct {
	animValues
	down bool
	seen uint64
}

type animator struct {
	states map[string]*animState
	frame  uint64
}

func (a *animator) tick() {
	measureFrame()
	a.frame++
	for id, st := range a.states {
		if a.frame-st.seen > animForgetFrames {
			delete(a.states, id)
		}
	}
}

func (a *animator) step(id string, hovered, down bool, dt float64) animValues {
	if a.states == nil {
		a.states = map[string]*animState{}
	}
	st, ok := a.states[id]
	if !ok {
		st = &animState{}
		a.states[id] = st

		if hovered {
			st.hover = 1
		}
	}
	st.seen = a.frame

	pressed := hovered && down
	if pressed && !st.down {
		st.flash = 1
	}
	st.down = pressed

	st.hover = ease(st.hover, boolTo(hovered), animHoverRate, dt)
	st.press = ease(st.press, boolTo(pressed), animPressRate, dt)
	if st.flash > 0 && animFlashSeconds > 0 {
		st.flash -= dt / animFlashSeconds
		if st.flash < 0 {
			st.flash = 0
		}
	}
	return st.animValues
}

func ease(v, target, rate, dt float64) float64 {
	k := rate * dt
	if k > 1 {
		k = 1
	}
	if k < 0 {
		return v
	}
	return v + (target-v)*k
}

func boolTo(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

var (
	uiFrameDT   = 1.0 / playheadFallbackTPS
	uiFrameLast time.Time
)

func uiFrameSeconds() float64 { return uiFrameDT }

func measureFrame() {
	now := time.Now()
	if uiFrameLast.IsZero() {
		uiFrameLast = now
		return
	}
	dt := now.Sub(uiFrameLast).Seconds()
	uiFrameLast = now

	if dt < 1.0/480 {
		dt = 1.0 / 480
	}
	if dt > 1.0/20 {
		dt = 1.0 / 20
	}
	uiFrameDT = dt
}

func lerpCol(a, b color.RGBA, t float64) color.RGBA {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), mix(a.A, b.A)}
}

func withAlpha(c color.RGBA, alpha float64) color.RGBA {
	if alpha <= 0 {
		return color.RGBA{}
	}
	if alpha > 1 {
		alpha = 1
	}
	scale := func(v uint8) uint8 { return uint8(float64(v) * alpha) }
	return color.RGBA{scale(c.R), scale(c.G), scale(c.B), scale(uint8(255 * alpha))}
}

func (av animValues) animate(r rect) rect {
	r.y += -animLift*av.hover + (animLift+animSink)*av.press
	return r
}

func drawFlash(dst *ebiten.Image, r rect, av animValues) {
	if av.flash <= 0 {
		return
	}

	grow := 5 * (1 - av.flash)
	ring := rect{r.x - grow, r.y - grow, r.w + 2*grow, r.h + 2*grow}
	strokeRounded(dst, ring, withAlpha(colNote, 0.55*av.flash))
}
