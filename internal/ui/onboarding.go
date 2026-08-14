package ui

// The first run: what the start screen shows before there is anything to
// be recent.
//
// An empty "RECENT PIECES" pane is the least useful thing this
// application can put in front of somebody who has just downloaded it. It
// says nothing about the two things that have to happen before the app
// can hear a guitar — choosing the interface, and measuring its round
// trip — and nothing about where pieces come from. So until the first
// piece has been opened, the pane is a checklist instead, and each step
// reports whether it is actually done rather than merely listed. A
// checklist that ticks itself off as the configuration changes is worth
// having; one that just enumerates instructions is a leaflet.

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// An onboardStep is one line of the getting-started checklist.
type onboardStep struct {
	title  string
	detail string
	done   bool
	// act runs on Enter or a click. A nil act makes the step a statement
	// rather than something to do — which is what an unavailable audio
	// backend leaves behind.
	act func()
}

// onboarding reports whether the start screen is showing the checklist in
// place of the recents pane. Having opened a piece is the honest signal
// that the first run is over: it is the one step that cannot be faked by
// configuration.
func (b *Browser) onboarding() bool { return len(b.recents) == 0 }

// stepList builds the checklist against the configuration as it stands
// right now, so coming back from the settings screen shows the step just
// completed already ticked.
func (b *Browser) stepList() []onboardStep {
	if !b.onboarding() {
		return nil
	}
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
		detail := "the interface your guitar is plugged into: settings, capture device"
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
			detail: "one calibration pass, so scoring knows when you actually played",
			done:   calibrated,
			act:    b.settings,
		})
	}
	steps = append(steps, onboardStep{
		title:  "Open a piece",
		detail: "browse on the right, or drop a .gp, .musicxml or .mid file here",
		act:    func() { b.focus = browserPaneBrowse },
	})
	return steps
}

// activateStep runs the selected checklist step. A step with nothing to
// do is a no-op rather than an error: it is there to be read.
func (b *Browser) activateStep() {
	steps := b.stepList()
	if b.recentSel < 0 || b.recentSel >= len(steps) {
		return
	}
	if act := steps[b.recentSel].act; act != nil {
		act()
	}
}

// drawSteps paints the checklist in the recents pane's place.
func (b *Browser) drawSteps(screen *ebiten.Image) {
	steps := b.stepList()
	for i, st := range steps {
		y := float32(brwListTop + i*brwRecentRowH)
		if bg, ok := b.rowBG(browserPaneRecent, i); ok {
			vector.DrawFilledRect(screen, brwRecentX-4, y-2, brwRecentW+8, brwRecentRowH-2, bg, false)
		}
		mark, markCol := "[ ]", colDim
		if st.done {
			mark, markCol = "[x]", colHit
		}
		if st.act == nil {
			mark, markCol = " - ", colBarline
		}
		titleCol := colNote
		if st.done {
			titleCol = colDim
		}
		drawText(screen, mark, brwRecentX+4, float64(y)+2, markCol)
		drawText(screen, truncateW(st.title, brwRecentW-40), brwRecentX+34, float64(y)+2, titleCol)
		// The detail is drawn at colDim rather than the dimmer colBarline
		// the rest of the screen uses for asides: on the selected row it
		// would otherwise be all but invisible against the highlight.
		drawTextSmall(screen, truncateW(st.detail, brwRecentW-40), brwRecentX+34, float64(y)+19, colDim)
	}
	y := float64(brwListTop + len(steps)*brwRecentRowH + 14)
	drawText(screen, "your pieces will be listed here once you have opened one", brwRecentX+4, y, colBarline)
}
