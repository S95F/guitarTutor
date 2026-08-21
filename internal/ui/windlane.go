package ui

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/score"
)

const windStaffLines = 6

func (a *App) laneLines() int {
	if a.displayed().Wind != nil {
		return windStaffLines
	}
	return len(a.displayed().Tuning)
}

type windLadder struct {
	lo, hi int
}

func windLadderFor(tr *score.Track) windLadder {
	w := tr.Wind
	lo, hi := 0, 0
	first := true
	for _, bar := range tr.Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				k := w.Written(tr.Pitch(n))
				if first {
					lo, hi, first = k, k, false
					continue
				}
				if k < lo {
					lo = k
				}
				if k > hi {
					hi = k
				}
			}
		}
	}
	if first {

		lo, hi = w.Written(w.LowSounding), w.Written(w.LowSounding+w.Span)
	}
	lo, hi = lo-1, hi+1
	for hi-lo < 12 {
		lo--
		hi++
	}
	return windLadder{lo: lo, hi: hi}
}

func (l windLadder) y(written int, top, bottom float64) float64 {
	return bottom - float64(written-l.lo)/float64(l.hi-l.lo)*(bottom-top)
}

func (a *App) drawWindGrid(screen *ebiten.Image, l windLadder, top, bottom float64) {

	for c := (l.lo + 11) / 12 * 12; c <= l.hi; c += 12 {
		y := float32(l.y(c, top, bottom))
		vector.StrokeLine(screen, 0, y, screenW, y, 1, colString, false)
		drawTextSmall(screen, keyName(c), 4, float64(y)-14, colHint)
	}
}

func windNoteLabel(tr *score.Track, n score.Note) string {
	label := keyName(tr.Wind.Written(tr.Pitch(n)))
	if n.Tied {
		label = "~" + label
	}
	return label
}

func (a *App) displayName(key int) string {
	if w := a.displayed().Wind; w != nil {
		return keyName(w.Written(key))
	}
	return keyName(key)
}
