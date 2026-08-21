package ui

import (
	"github.com/hajimehoshi/ebiten/v2"
)

const (
	tipDelay = 0.35

	tipGap  = 8.0
	tipPadX = 9.0
	tipH    = 30.0
)

type tips struct {
	id, text, key string
	at            rect

	dwell float64

	seen bool
}

func (t *tips) offer(id, text, key string, at rect, hovered bool, dt float64) {
	if !hovered || text == "" {
		if t.id == id {

			t.id, t.dwell = "", 0
		}
		return
	}
	if t.id != id {
		t.id, t.dwell = id, 0
	}
	t.text, t.key, t.at = text, key, at
	t.dwell += dt
	t.seen = true
}

func (t *tips) hide() { t.id, t.dwell, t.seen = "", 0, false }

func (t *tips) visible() bool { return t.id != "" && t.dwell >= tipDelay && t.seen }

func (t *tips) draw(dst *ebiten.Image) {
	show := t.visible()
	t.seen = false
	if !show {
		return
	}
	label := t.text
	if t.key != "" {
		label += "   " + t.key
	}
	w := textW(t.text) + 2*tipPadX
	if t.key != "" {
		w = textW(t.text) + textWSmall(t.key) + 2*tipPadX + 12
	}

	x := t.at.x
	if x+w > screenW-uiPadX {
		x = screenW - uiPadX - w
	}
	if x < uiPadX {
		x = uiPadX
	}
	y := t.at.y + t.at.h + tipGap
	if y+tipH > screenH-uiPadX {
		y = t.at.y - tipGap - tipH
	}

	r := rect{x, y, w, tipH}
	drawPanel(dst, r, colTipBG, colTipEdge)
	drawText(dst, t.text, r.x+tipPadX, r.y+6, colNote)
	if t.key != "" {
		drawTextSmall(dst, t.key, r.x+tipPadX+textW(t.text)+12, r.y+8, colSounding)
	}
}
