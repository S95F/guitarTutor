package ui

import (
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"
)

const (
	brwHintOpenH = 84.0
	brwHintShutH = 26.0
)

type onboardStep struct {
	title  string
	detail string
	done   bool

	act func()
}

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

		steps = append(steps, onboardStep{
			title:  "Playback only on this machine",
			detail: "no audio capture backend, so the app cannot score your playing",
		})
	} else {
		detail := "the interface your guitar or mic plugs into"
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

func (b *Browser) hasAnyPiece() bool {
	for _, p := range b.paneOrder() {
		if len(b.panes[p]) > 0 {
			return true
		}
	}
	return false
}

func (b *Browser) activateStep(i int) {
	steps := b.stepList()
	if i < 0 || i >= len(steps) {
		return
	}
	if act := steps[i].act; act != nil {
		act()
	}
}

func (b *Browser) toggleHint() {
	b.hintOpen = !b.hintOpen
	if pr := b.prefs(); pr != nil {
		pr.SetHintHidden(!b.hintOpen)

		_ = pr.Save()
	}
}

func (b *Browser) drawHint(screen *ebiten.Image, l browserLayout) {
	dt := uiFrameSeconds()
	if !b.hintOpen {
		drawTextSmall(screen, "getting started", l.hint.x, l.hint.y+4, colHint)
		av := b.anim.step("hint:toggle", b.ptr.over(l.hintBtn), b.ptr.down, dt)
		drawButton(screen, l.hintBtn, glyphNone, "show  (H)", "", btnNormal, av)
		return
	}

	drawPanel(screen, l.hint, colPanel, colPanelEdge)
	drawText(screen, "GETTING STARTED", l.hint.x+12, l.hint.y+7, colInferred)
	av := b.anim.step("hint:toggle", b.ptr.over(l.hintBtn), b.ptr.down, dt)
	drawButton(screen, l.hintBtn, glyphNone, "hide  (H)", "", btnNormal, av)

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
