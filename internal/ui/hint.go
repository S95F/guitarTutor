package ui

// The getting-started strip: what the start screen says to somebody who
// has just downloaded the application, and stops saying the moment they
// are done with it.
//
// This used to be a checklist that owned the whole left pane until a
// piece had been opened. It earned that space once and then stopped: the
// two things it points at — choosing the audio interface, and measuring
// its round trip — are both rows in settings, and a screen that keeps
// insisting on them after they are done is a tutorial that will not end.
// So it is a strip across the top now, it still ticks itself off against
// the real configuration rather than merely listing instructions, and it
// can be put away for good.
//
// "For good" is the part that matters. Hiding it writes to preferences,
// so it stays hidden across restarts — a dismissal that has to be
// repeated every launch is not a dismissal.

import (
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"
)

// Strip metrics: the open height fits a line of prose over a row of
// steps, and the collapsed one a single line offering it back.
const (
	brwHintOpenH = 84.0
	brwHintShutH = 26.0
)

// An onboardStep is one step of the getting-started strip.
type onboardStep struct {
	title  string
	detail string
	done   bool
	// act runs on a click. A nil act makes the step a statement rather
	// than something to do — which is what an unavailable audio backend
	// leaves behind.
	act func()
}

// stepList builds the strip against the configuration as it stands right
// now, so coming back from the settings screen shows the step just
// completed already ticked.
func (b *Browser) stepList() []onboardStep {
	var svc Services
	if b.sh != nil {
		svc = b.sh.Services()
	}
	var capID, playID string
	if svc.Prefs != nil {
		capID, playID = svc.Prefs.Devices()
	}

	var steps []onboardStep
	if svc.Audio == nil {
		// Not a step the user can take: say so plainly rather than
		// leaving an instruction that cannot be followed.
		steps = append(steps, onboardStep{
			title:  "Playback only on this machine",
			detail: "no audio capture backend, so the app cannot score your playing",
		})
	} else {
		detail := "the interface your guitar is plugged into"
		if capID != "" {
			detail = "chosen; change it in settings whenever you move interfaces"
		}
		steps = append(steps, onboardStep{
			title:  "Choose your audio interface",
			detail: detail,
			done:   capID != "",
			act:    b.settings,
		})

		calibrated := false
		if capID != "" && playID != "" {
			_, calibrated = svc.Audio.CalibratedOffset(capID, playID)
		}
		steps = append(steps, onboardStep{
			title:  "Measure the round trip",
			detail: "one pass, so scoring knows when you actually played",
			done:   calibrated,
			act:    b.settings,
		})
	}
	steps = append(steps, onboardStep{
		title:  "Open a piece, or write one",
		detail: "import a file, or start from a blank staff",
		done:   b.hasAnyPiece(),
		act:    func() { b.launchOpenDialog("") },
	})
	return steps
}

// hasAnyPiece reports whether there is anything at all in the three
// panes. It is what ticks the last step off, and the honest signal that
// the first run is over.
func (b *Browser) hasAnyPiece() bool {
	for _, p := range b.paneOrder() {
		if len(b.panes[p]) > 0 {
			return true
		}
	}
	return false
}

// activateStep runs one step of the strip. A step with nothing to do is a
// no-op rather than an error: it is there to be read.
func (b *Browser) activateStep(i int) {
	steps := b.stepList()
	if i < 0 || i >= len(steps) {
		return
	}
	if act := steps[i].act; act != nil {
		act()
	}
}

// toggleHint shows or hides the strip, and remembers which.
func (b *Browser) toggleHint() {
	b.hintOpen = !b.hintOpen
	if pr := b.prefs(); pr != nil {
		pr.SetHintHidden(!b.hintOpen)
		// A failed write must not block the screen; settings surfaces
		// persistence problems.
		_ = pr.Save()
	}
}

// drawHint paints the strip, or the one line that offers it back.
func (b *Browser) drawHint(screen *ebiten.Image, l browserLayout) {
	dt := uiFrameSeconds()
	if !b.hintOpen {
		drawTextSmall(screen, "getting started", l.hint.x, l.hint.y+4, colHint)
		av := b.anim.step("hint:toggle", b.ptr.over(l.hintBtn), b.ptr.down, dt)
		drawButton(screen, l.hintBtn, "show  (H)", "", btnNormal, av)
		return
	}

	drawPanel(screen, l.hint, colPanel, colPanelEdge)
	drawText(screen, "GETTING STARTED", l.hint.x+12, l.hint.y+7, colInferred)
	av := b.anim.step("hint:toggle", b.ptr.over(l.hintBtn), b.ptr.down, dt)
	drawButton(screen, l.hintBtn, "hide  (H)", "", btnNormal, av)

	steps := b.stepList()
	for i, st := range steps {
		if i >= len(l.steps) {
			break
		}
		r := l.steps[i]
		hovered := st.act != nil && b.ptr.over(r)
		sv := b.anim.step("hint:step"+strconv.Itoa(i), hovered, b.ptr.down, dt)
		fill, edge := colBG, colBarline
		if st.act != nil {
			fill = lerpCol(colBG, colHover, sv.hover)
			edge = lerpCol(colPanelEdge, colDim, sv.hover)
		}
		rr := sv.animate(r)
		drawPanel(screen, rr, fill, edge)

		mark, markCol := "○", colDim
		if st.done {
			mark, markCol = "●", colHit
		}
		if st.act == nil {
			mark, markCol = "–", colBarline
		}
		titleCol := colNote
		if st.done {
			titleCol = colDim
		}
		drawText(screen, mark, rr.x+10, rr.y+2, markCol)
		drawText(screen, truncateW(st.title, rr.w-42), rr.x+30, rr.y+2, titleCol)
		drawTextSmall(screen, truncateW(st.detail, rr.w-42), rr.x+30, rr.y+21, colDim)
		drawFlash(screen, rr, sv)
	}
}
