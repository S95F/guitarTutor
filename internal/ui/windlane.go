package ui

// The wind track's notation band. A wind part has no strings to draw, so
// the band the tab uses for string lanes becomes a pitch ladder: height is
// written pitch — the note names the player actually reads on their
// transposing instrument — with a faint reference line at each octave's C.
// The band keeps exactly a six-string staff's height (windStaffLines), so
// every overlay, band-collision test and control anchored to the tab's
// bottom holds for a wind track without knowing one exists.

import (
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/score"
)

// windStaffLines is how many string-lines' worth of height the wind
// ladder occupies: a standard six-string staff's, so the notation band's
// geometry — and everything positioned from tabBottom — is identical to
// the layout every existing test pins.
const windStaffLines = 6

// laneLines is the number of string lines the notation band is sized for:
// the track's string count on a fretted track, the fixed staff height on
// a wind track (whose Tuning is nil).
func (a *App) laneLines() int {
	if a.displayed().Wind != nil {
		return windStaffLines
	}
	return len(a.displayed().Tuning)
}

// A windLadder maps written pitch to a height in the notation band: lo
// sits on the band's bottom line, hi on its top.
type windLadder struct {
	lo, hi int // written MIDI keys
}

// windLadderFor surveys the notes a track actually uses and pads the
// compass to at least an octave, so a three-note exercise is not smeared
// across 130 pixels and a wide line still fits. One semitone of margin
// keeps the extreme notes off the band's edges.
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
		// An empty track: show the instrument's standard range.
		lo, hi = w.Written(w.LowSounding), w.Written(w.LowSounding+w.Span)
	}
	lo, hi = lo-1, hi+1
	for hi-lo < 12 {
		lo--
		hi++
	}
	return windLadder{lo: lo, hi: hi}
}

// y places a written key in the band: lo on the bottom line, hi on the
// top, higher pitch higher on screen.
func (l windLadder) y(written int, top, bottom float64) float64 {
	return bottom - float64(written-l.lo)/float64(l.hi-l.lo)*(bottom-top)
}

// drawWindGrid paints the ladder's reference lines in place of string
// lines: one per octave C in the compass, labelled with its name in the
// left margin, so height reads as pitch at a glance.
func (a *App) drawWindGrid(screen *ebiten.Image, l windLadder, top, bottom float64) {
	for c := (l.lo/12 + 1) * 12; c <= l.hi; c += 12 {
		if c < l.lo {
			continue
		}
		y := float32(l.y(c, top, bottom))
		vector.StrokeLine(screen, 0, y, screenW, y, 1, colString, false)
		drawTextSmall(screen, keyName(c), 4, float64(y)-14, colHint)
	}
}

// windNoteLabel is the glyph a wind note carries: its written pitch name,
// with the same "~" tie prefix the tab uses.
func windNoteLabel(tr *score.Track, n score.Note) string {
	label := keyName(tr.Wind.Written(tr.Pitch(n)))
	if n.Tied {
		label = "~" + label
	}
	return label
}

// displayName names a sounding key the way the practiced player reads it:
// written pitch on a transposing wind track, concert pitch otherwise. The
// tuner goes through it — telling a soprano sax player they are sounding
// A-flat when they are fingering a B-flat is technically true and
// pedagogically wrong.
func (a *App) displayName(key int) string {
	if w := a.displayed().Wind; w != nil {
		return keyName(w.Written(key))
	}
	return keyName(key)
}
