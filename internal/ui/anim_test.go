package ui

import "testing"

// The animator's contract: it eases toward what it is told, it arrives,
// it fires a flash on the press rather than needing to be told about the
// click separately, and it forgets controls that stop being drawn.

// step60 runs n frames at 60 Hz and returns the final values.
func step60(a *animator, id string, hovered, down bool, n int) animValues {
	var av animValues
	for i := 0; i < n; i++ {
		a.tick()
		av = a.step(id, hovered, down, 1.0/60)
	}
	return av
}

func TestAnimatorHoverRisesAndFalls(t *testing.T) {
	var a animator
	if av := step60(&a, "b", true, false, 20); av.hover < 0.95 {
		t.Errorf("after a third of a second of hovering, hover is %.3f; want it arrived", av.hover)
	}
	if av := step60(&a, "b", false, false, 20); av.hover > 0.05 {
		t.Errorf("after a third of a second away, hover is %.3f; want it gone", av.hover)
	}
}

func TestAnimatorPressIsFasterThanHover(t *testing.T) {
	// A control that lags the finger feels broken; one that lags the
	// cursor feels smooth. They are eased at different rates on purpose.
	var a animator
	// From a settled, un-hovered control — a fresh one born under the
	// cursor is deliberately already hovered, which would compare
	// nothing.
	step60(&a, "b", false, false, 2)
	av := step60(&a, "b", true, true, 3)
	if av.press <= av.hover {
		t.Errorf("after three frames press=%.3f hover=%.3f; press has to lead", av.press, av.hover)
	}
}

func TestAnimatorFlashesOnThePressEdge(t *testing.T) {
	var a animator
	// Hovering alone is not an activation.
	if av := step60(&a, "b", true, false, 5); av.flash != 0 {
		t.Errorf("hovering flashed (%.3f)", av.flash)
	}
	a.tick()
	av := a.step("b", true, true, 1.0/60)
	if av.flash < 0.9 {
		t.Fatalf("the press did not flash (%.3f)", av.flash)
	}
	// Holding does not re-fire it, and it decays on its own.
	held := step60(&a, "b", true, true, 6)
	if held.flash >= av.flash {
		t.Errorf("holding re-fired the flash (%.3f then %.3f)", av.flash, held.flash)
	}
	if gone := step60(&a, "b", true, true, 30); gone.flash != 0 {
		t.Errorf("the flash is still %.3f half a second later", gone.flash)
	}
}

func TestAnimatorPressOutsideTheControlDoesNothing(t *testing.T) {
	// The button being down somewhere else on the screen is not a press
	// of this control.
	var a animator
	av := step60(&a, "b", false, true, 5)
	if av.press != 0 || av.flash != 0 {
		t.Errorf("a press elsewhere reached this control: %+v", av)
	}
}

func TestAnimatorStartsUnderTheCursorAtFullHover(t *testing.T) {
	// A control that appears already under the pointer must not ease in
	// from nothing, which would read as it having just moved there.
	var a animator
	a.tick()
	if av := a.step("fresh", true, false, 1.0/60); av.hover < 0.9 {
		t.Errorf("a control born under the cursor starts at hover %.3f", av.hover)
	}
}

func TestAnimatorForgetsControlsThatStopBeingDrawn(t *testing.T) {
	var a animator
	step60(&a, "gone", true, false, 5)
	step60(&a, "kept", true, false, 5)
	if len(a.states) != 2 {
		t.Fatalf("the animator holds %d controls, want 2", len(a.states))
	}
	for i := 0; i < animForgetFrames+2; i++ {
		a.tick()
		a.step("kept", false, false, 1.0/60)
	}
	if _, ok := a.states["gone"]; ok {
		t.Error("a control that stopped being drawn was not forgotten")
	}
	if _, ok := a.states["kept"]; !ok {
		t.Error("a control that is still being drawn was forgotten")
	}
}

func TestEaseArrivesAndDoesNotOvershoot(t *testing.T) {
	// A dropped frame — or a window suspended for a minute — hands the
	// easing a huge dt. It has to arrive, not fly past.
	if got := ease(0, 1, 16, 10); got != 1 {
		t.Errorf("ease over a 10 second frame = %v, want it clamped to 1", got)
	}
	if got := ease(1, 0, 16, 10); got != 0 {
		t.Errorf("ease down over a 10 second frame = %v, want 0", got)
	}
	if got := ease(0.5, 0.5, 16, 1.0/60); got != 0.5 {
		t.Errorf("ease toward where it already is moved it to %v", got)
	}
}

func TestLerpColAndWithAlpha(t *testing.T) {
	a, b := colPanel, colNote
	if got := lerpCol(a, b, 0); got != a {
		t.Errorf("lerpCol at 0 = %v, want the start colour", got)
	}
	if got := lerpCol(a, b, 1); got != b {
		t.Errorf("lerpCol at 1 = %v, want the end colour", got)
	}
	mid := lerpCol(a, b, 0.5)
	if mid.R <= a.R || mid.R >= b.R {
		t.Errorf("lerpCol at 0.5 gave R=%d, outside (%d, %d)", mid.R, a.R, b.R)
	}
	// Alpha is premultiplied, so every channel scales with it — a colour
	// that only dimmed its alpha would draw as a bright wash (the fault
	// colLoop in ui.go carries a comment about).
	half := withAlpha(colNote, 0.5)
	if half.A == 0 {
		t.Error("withAlpha(0.5) is fully transparent")
	}
	if half.R > colNote.R/2+1 {
		t.Errorf("withAlpha(0.5) left R at %d, want it scaled with the alpha", half.R)
	}
	if got := withAlpha(colNote, 0); got.A != 0 || got.R != 0 {
		t.Errorf("withAlpha(0) = %v, want fully transparent", got)
	}
}

// TestAnimatedControlsStayWhereTheyAreHitTested: the lift and sink move
// what is DRAWN, and the hit testing reads the un-animated rectangle. The
// movement therefore has to stay far under a control's height, or the
// bottom of a hovered button stops being clickable.
func TestAnimatedControlsStayWhereTheyAreHitTested(t *testing.T) {
	if total := animLift + animSink; total > chipH/4 {
		t.Errorf("a control moves %v pixels, a quarter of the %v it is tall; the hit region would no longer line up",
			total, float64(chipH))
	}
	r := rect{10, 20, 100, chipH}
	full := animValues{hover: 1, press: 1}.animate(r)
	if full.x != r.x || full.w != r.w || full.h != r.h {
		t.Errorf("animating moved the control sideways or resized it: %+v", full)
	}
}
