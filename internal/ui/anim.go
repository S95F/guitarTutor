package ui

// Control animation, shared by every screen.
//
// A button that changes state instantly is readable but dead: the eye gets
// no confirmation that the thing under the cursor is the thing that will
// be pressed, and a click that lands leaves nothing behind to say it
// landed. Both are worth a few frames of easing — and only a few, because
// a practice tool is used at speed and an interface that has to finish an
// animation before it answers is worse than one that never moved.
//
// Three states, eased separately because they mean different things:
//
//   - hover rises while the cursor is over a control and falls when it
//     leaves. It is the slowest, because it is ambient.
//   - press follows the button being held. It is the fastest: a control
//     that lags the finger feels broken rather than smooth.
//   - flash is fired by the press itself and decays on its own. It is the
//     receipt — the one part that outlives the gesture, so a click that
//     opened a dialog somewhere else still shows that it was the click
//     that did it.
//
// The animator keys everything by a caller-supplied id rather than by
// position, so a control that moves — a chip in a row that grew, a list
// that scrolled — keeps its own animation instead of inheriting whatever
// was last drawn where it now is. Ids that stop being drawn are forgotten
// after a couple of seconds, which is what keeps the map from growing for
// the life of the process.

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
)

const (
	// animHoverRate and animPressRate are the easing rates, per second.
	// Hover takes about a tenth of a second to arrive and press about a
	// twentieth: the press has to feel like it happened when the finger
	// moved, and the hover is allowed to be a glide.
	animHoverRate = 16.0
	animPressRate = 34.0
	// animFlashSeconds is how long the activation receipt lasts.
	animFlashSeconds = 0.28
	// animForgetFrames is how long a control that has stopped being drawn
	// keeps its animation state. Two seconds at 60 Hz: long enough that
	// tabbing away from a pane and back does not reset it, short enough
	// that nothing accumulates.
	animForgetFrames = 120
	// animLift and animSink are how far a control moves, in pixels, when
	// the cursor is over it and when it is held. Small on purpose: this is
	// meant to be felt rather than watched.
	animLift = 1.5
	animSink = 2.0
)

// animValues is one control's eased state this frame.
type animValues struct {
	hover float64 // 0 away, 1 under the cursor
	press float64 // 0 released, 1 held
	flash float64 // 1 at the moment of activation, decaying to 0
}

// animState is animValues plus what the animator needs to remember
// between frames.
type animState struct {
	animValues
	down bool   // the button was held on the previous frame
	seen uint64 // the animator frame this control was last drawn on
}

// An animator eases the controls of one screen. The zero value is ready
// to use.
type animator struct {
	states map[string]*animState
	frame  uint64
}

// tick starts a frame: it advances the clock and forgets controls that
// have stopped being drawn. Screens call it once, at the top of Draw.
func (a *animator) tick() {
	a.frame++
	for id, st := range a.states {
		if a.frame-st.seen > animForgetFrames {
			delete(a.states, id)
		}
	}
}

// step eases one control and returns its values. hovered is whether the
// cursor is over it and down whether the button is held there; the
// activation flash is fired by the transition into down, so no caller has
// to remember to report a click separately from drawing one.
func (a *animator) step(id string, hovered, down bool, dt float64) animValues {
	if a.states == nil {
		a.states = map[string]*animState{}
	}
	st, ok := a.states[id]
	if !ok {
		st = &animState{}
		a.states[id] = st
		// A control that appears already under the cursor starts there
		// rather than easing in from nothing, which would look like it had
		// just been moved under the pointer.
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

// forget drops every control's state. A screen that has changed what it
// is showing entirely — a different piece, a different pane — calls it so
// nothing inherits an animation from a control that no longer exists.
func (a *animator) forget() { a.states = nil }

// ease moves v toward target at rate per second, without overshooting for
// any sane frame time. The clamp is what makes a dropped frame — or a
// window that was suspended for a minute — arrive rather than fly past.
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

// boolTo is the target an eased value moves toward.
func boolTo(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// uiFrameSeconds is one display frame, for the easing. Ebitengine calls
// Draw at the display's rate and does not report a delta, so the nominal
// step is the honest one — and easing is not a clock, so a frame that ran
// long only makes one control arrive a frame later.
func uiFrameSeconds() float64 {
	tps := float64(ebiten.TPS())
	if tps <= 0 {
		tps = playheadFallbackTPS
	}
	return 1 / tps
}

// --- colour ----------------------------------------------------------

// lerpCol blends two colours. Every colour in the palette is opaque, so
// this is a plain per-channel mix; a translucent one would have to be
// blended in premultiplied form, which is what image/color.RGBA already
// holds (see colLoop in ui.go for what forgetting that looks like).
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

// withAlpha returns c at the given opacity, premultiplied — the form
// image/color.RGBA is defined in and Ebitengine draws with.
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

// animate applies a control's eased state to the rectangle it is drawn
// in: a hovered control lifts towards the viewer and a held one sinks
// past where it started, so pressing feels like pressing.
func (av animValues) animate(r rect) rect {
	r.y += -animLift*av.hover + (animLift+animSink)*av.press
	return r
}

// drawFlash paints the activation receipt: a ring just outside the
// control, expanding and fading. It is drawn after the control itself so
// it reads as light coming off it rather than as a border it has grown.
func drawFlash(dst *ebiten.Image, r rect, av animValues) {
	if av.flash <= 0 {
		return
	}
	// The ring expands as it fades, which is what makes it read as a
	// pulse rather than as a highlight being turned off.
	grow := 5 * (1 - av.flash)
	ring := rect{r.x - grow, r.y - grow, r.w + 2*grow, r.h + 2*grow}
	strokeRounded(dst, ring, withAlpha(colNote, 0.55*av.flash))
}
