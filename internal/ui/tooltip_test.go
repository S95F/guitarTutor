package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// The tooltip's contract: it waits before appearing, it follows the
// cursor from one control to the next without carrying the old name, it
// forgets a control that stops being offered, and it never lands off the
// window.

const tipFrame = 1.0 / 60

func TestTipWaitsBeforeAppearing(t *testing.T) {
	// Sweeping across a toolbar must show nothing; stopping on something
	// must ask a question.
	var tp tips
	r := rect{100, 100, 32, 32}
	for i := 0; i < 5; i++ {
		tp.offer("a", "Hammer-on", "h", r, true, tipFrame)
		if tp.visible() {
			t.Fatalf("the tooltip appeared after %d frames, before the dwell", i+1)
		}
	}
	for i := 0; i < 40; i++ {
		tp.offer("a", "Hammer-on", "h", r, true, tipFrame)
	}
	if !tp.visible() {
		t.Error("the tooltip never appeared")
	}
}

func TestTipResetsWhenTheCursorMovesOn(t *testing.T) {
	var tp tips
	r := rect{100, 100, 32, 32}
	for i := 0; i < 40; i++ {
		tp.offer("a", "Hammer-on", "h", r, true, tipFrame)
	}
	if !tp.visible() {
		t.Fatal("the tooltip did not appear on the first control")
	}
	// Moving to the next control starts its dwell over rather than
	// carrying the previous name straight across.
	tp.offer("b", "Pull-off", "p", r, true, tipFrame)
	if tp.visible() {
		t.Error("the tooltip carried straight over to the next control")
	}
	if tp.text != "Pull-off" {
		t.Errorf("the tooltip says %q, want the control the cursor is on now", tp.text)
	}
}

func TestTipForgetsAControlTheCursorLeaves(t *testing.T) {
	var tp tips
	r := rect{100, 100, 32, 32}
	for i := 0; i < 40; i++ {
		tp.offer("a", "Hammer-on", "h", r, true, tipFrame)
	}
	tp.offer("a", "Hammer-on", "h", r, false, tipFrame)
	if tp.visible() {
		t.Error("the tooltip stayed up after the cursor left")
	}
	if tp.id != "" {
		t.Errorf("the tooltip still belongs to %q", tp.id)
	}
}

func TestTipGoesAwayWhenNothingIsOffered(t *testing.T) {
	// A frame in which no control was drawn under the cursor — the screen
	// switched views, say — must not leave a tooltip hanging over
	// something that is no longer there.
	var tp tips
	r := rect{100, 100, 32, 32}
	for i := 0; i < 40; i++ {
		tp.offer("a", "Hammer-on", "h", r, true, tipFrame)
	}
	if !tp.visible() {
		t.Fatal("the tooltip did not appear")
	}
	tp.seen = false // what draw does at the end of a frame
	if tp.visible() {
		t.Error("the tooltip survived a frame in which nothing was offered")
	}
}

func TestTipIgnoresAControlWithNoName(t *testing.T) {
	var tp tips
	for i := 0; i < 40; i++ {
		tp.offer("a", "", "h", rect{10, 10, 20, 20}, true, tipFrame)
	}
	if tp.visible() {
		t.Error("a control with no name produced a tooltip")
	}
}

// TestTipStaysOnScreen: the panel is placed under a control and pushed
// back inside the window at the edges. A tooltip that runs off the screen
// reads as a rendering fault rather than as a hint.
func TestTipStaysOnScreen(t *testing.T) {
	for _, at := range []rect{
		{10, 100, 32, 32},                    // hard left
		{screenW - 40, 100, 32, 32},          // hard right
		{600, screenH - 50, 32, 32},          // against the bottom
		{screenW - 40, screenH - 50, 32, 32}, // the corner
	} {
		var tp tips
		for i := 0; i < 40; i++ {
			tp.offer("a", "Save — there are unsaved changes", "ctrl+S", at, true, tipFrame)
		}
		if !tp.visible() {
			t.Fatalf("no tooltip for a control at %+v", at)
		}
		x, y, w, h := tipPlacement(tp)
		if x < uiPadX-0.01 || x+w > screenW-uiPadX+0.01 {
			t.Errorf("a tooltip for a control at %+v spans x %.1f..%.1f, outside the page", at, x, x+w)
		}
		if y < 0 || y+h > screenH {
			t.Errorf("a tooltip for a control at %+v spans y %.1f..%.1f, off the window", at, y, y+h)
		}
	}
}

// tipPlacement is the geometry draw uses, factored out so the placement
// can be checked without a graphics context.
func tipPlacement(t tips) (x, y, w, h float64) {
	w = textW(t.text) + 2*tipPadX
	if t.key != "" {
		w = textW(t.text) + textWSmall(t.key) + 2*tipPadX + 12
	}
	x = t.at.x
	if x+w > screenW-uiPadX {
		x = screenW - uiPadX - w
	}
	if x < uiPadX {
		x = uiPadX
	}
	y = t.at.y + t.at.h + tipGap
	if y+tipH > screenH-uiPadX {
		y = t.at.y - tipGap - tipH
	}
	return x, y, w, tipH
}

// TestEditorOffersATooltipForEveryControl: an icon with no name is a
// rebus, so nothing on the toolbar is allowed to be one.
func TestEditorOffersATooltipForEveryControl(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)
	for _, b := range editorButtons(e) {
		if b.name == "" {
			t.Errorf("control %q has no tooltip name", b.id)
		}
		// A control with a symbol and no words anywhere is only findable
		// by pressing it, which is what the tooltip is for.
		if b.glyph == glyphNone && b.label == "" {
			t.Errorf("control %q draws neither a symbol nor a label", b.id)
		}
	}
}

// TestGtabProblemIsReadable: the parser reports positions the way a
// compiler does, which is the wrong shape for a pane a guitarist is
// looking at. The position is the useful half and it is said in words.
func TestGtabProblemIsReadable(t *testing.T) {
	_, err := textfmt.Parse([]byte("\time 4/4\n0.6.4 |\n"), "piece")
	if err == nil {
		t.Fatal("that piece should not parse: the bar is short")
	}
	got := textfmt.ProblemLine(err)
	if strings.HasPrefix(got, "piece:") {
		t.Errorf("the problem reads %q, which starts with a filename that does not exist", got)
	}
	if !strings.HasPrefix(got, "line ") {
		t.Errorf("the problem reads %q, want it to start with the line", got)
	}
	if !strings.Contains(got, "column") {
		t.Errorf("the problem reads %q, want the column named rather than punctuated", got)
	}
	// A failure that is not a parse error passes through unchanged rather
	// than being mangled into a position it does not have.
	plain := errors.New("the disk is full")
	if got := textfmt.ProblemLine(plain); got != "the disk is full" {
		t.Errorf("a non-parse error came back as %q", got)
	}
}

// TestTooltipIsSuppressedByAModal: while a prompt is up the toolbar
// cannot be pressed, so a name floating over the dimmed screen would be
// pointing at a control that does nothing.
func TestTooltipIsSuppressedByAModal(t *testing.T) {
	e := newTestEditor()
	if e.modalUp() {
		t.Fatal("a plain editor reports a modal")
	}
	for _, open := range []func(){
		func() { e.openEntry(edEntryTempo) },
		func() { e.entry = nil; e.leaving = true },
		func() { e.leaving = false; e.helpOpen = true },
	} {
		open()
		if !e.modalUp() {
			t.Fatal("a modal is up but modalUp says otherwise")
		}
	}
	// And hiding really forgets, so the tooltip does not come back
	// mid-dwell the moment the modal closes.
	for i := 0; i < 40; i++ {
		e.tip.offer("a", "Save", "ctrl+S", rect{10, 10, 32, 32}, true, tipFrame)
	}
	if !e.tip.visible() {
		t.Fatal("the tooltip did not appear")
	}
	e.tip.hide()
	if e.tip.visible() || e.tip.id != "" {
		t.Error("hide left the tooltip up")
	}
}

// TestToolbarLayoutMatchesWhatIsDrawn: the rects and the controls they
// belong to are built together, so every parallel index is in range by
// construction rather than by the two builds happening to agree.
func TestToolbarLayoutMatchesWhatIsDrawn(t *testing.T) {
	e := newTestEditor()
	for i := 0; i < 6; i++ {
		if err := e.doc.AddTrack("Track"); err != nil {
			t.Fatal(err)
		}
	}
	l := e.layoutToolbar()
	if len(l.groups) != len(l.rects) || len(l.groups) != len(l.caps) {
		t.Fatalf("%d groups, %d rect rows, %d captions", len(l.groups), len(l.rects), len(l.caps))
	}
	for gi, g := range l.groups {
		if len(g.buttons) != len(l.rects[gi]) {
			t.Errorf("group %q has %d buttons and %d rects", g.caption, len(g.buttons), len(l.rects[gi]))
		}
	}
	if len(l.piece) != len(l.pieceAt) {
		t.Errorf("%d piece controls, %d rects", len(l.piece), len(l.pieceAt))
	}
	if len(l.files) != len(l.filesAt) {
		t.Errorf("%d file controls, %d rects", len(l.files), len(l.filesAt))
	}
	// Even with more tracks than fit, nothing is laid out over the file
	// row or off the page.
	limit := screenW - uiPadX
	if len(l.filesAt) > 0 {
		limit = l.filesAt[0].x
	}
	for i, r := range l.pieceAt {
		if r.x+r.w > limit {
			t.Errorf("piece control %d ends at %.0f, into the file row at %.0f", i, r.x+r.w, limit)
		}
	}
	for gi, rects := range l.rects {
		for bi, r := range rects {
			if r.x < 0 || r.x+r.w > screenW {
				t.Errorf("group %d button %d spans %.0f..%.0f, off the page", gi, bi, r.x, r.x+r.w)
			}
		}
	}
}
