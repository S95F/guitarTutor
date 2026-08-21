package ui

import (
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

func drawGlyph(dst *ebiten.Image, id glyphID, box rect, col color.RGBA) {

	side := math.Round(math.Min(box.w, box.h))

	g := &glyphScratch
	g.dst = dst
	g.box = rect{
		math.Round(box.x + (box.w-side)/2),
		math.Round(box.y + (box.h-side)/2),
		side, side,
	}
	g.col = col
	switch id {
	case glyphNoteWhole:
		drawNote(g, 0, false, false)
	case glyphNoteHalf:
		drawNote(g, 0, true, false)
	case glyphNoteQuarter:
		drawNote(g, 0, true, true)
	case glyphNoteEighth:
		drawNote(g, 1, true, true)
	case glyphNoteSixteenth:
		drawNote(g, 2, true, true)
	case glyphNoteThirtySecond:
		drawNote(g, 3, true, true)

	case glyphDotted:

		drawNote(g, 0, true, true)
		g.dot(0.62, noteHeadY, 0.048)
	case glyphTriplet:

		g.moveTo(0.14, 0.78)
		g.lineTo(0.14, 0.60)
		g.lineTo(0.34, 0.60)
		g.stroke(wHair)
		g.moveTo(0.66, 0.60)
		g.lineTo(0.86, 0.60)
		g.lineTo(0.86, 0.78)
		g.stroke(wHair)
		drawDigit3(g, 0.50, 0.44, 0.36)

	case glyphRest:
		drawQuarterRest(g)
	case glyphRestWhole:

		g.line(0.18, 0.425, 0.82, 0.425, wHair)
		drawRestBlock(g, 0.425, 0.62)
	case glyphRestHalf:
		g.line(0.18, 0.575, 0.82, 0.575, wHair)
		drawRestBlock(g, 0.575, 0.38)

	case glyphTie:

		g.noteHead(0.24, 0.70, true)
		g.noteHead(0.76, 0.70, true)
		g.moveTo(0.24, 0.55)
		g.quadTo(0.50, 0.33, 0.76, 0.55)
		g.stroke(wStem)
	case glyphHammer:

		drawSlurredPair(g, 0.82, 0.60)
		drawLetterH(g, 0.50, 0.22, 0.28)
	case glyphPull:
		drawSlurredPair(g, 0.60, 0.82)
		drawLetterP(g, 0.50, 0.22, 0.28)
	case glyphSlide:

		g.noteHead(0.22, 0.74, true)
		g.noteHead(0.78, 0.34, true)
		g.line(0.38, 0.63, 0.62, 0.45, wStem)
	case glyphBend:

		g.noteHead(0.24, 0.78, true)
		g.moveTo(0.40, 0.70)
		g.quadTo(0.76, 0.64, 0.78, 0.28)
		g.stroke(wStem)
		g.moveTo(0.68, 0.36)
		g.lineTo(0.78, 0.17)
		g.lineTo(0.88, 0.36)
		g.stroke(wStem)
	case glyphVibrato:

		g.moveTo(0.09, 0.50)
		g.quadTo(0.21, 0.26, 0.33, 0.50)
		g.quadTo(0.45, 0.74, 0.57, 0.50)
		g.quadTo(0.69, 0.26, 0.81, 0.50)
		g.quadTo(0.87, 0.61, 0.91, 0.55)
		g.stroke(wMark)
	case glyphDead:

		g.moveTo(0.30, 0.33)
		g.lineTo(0.70, 0.67)
		g.strokeCap(wMark, vector.LineCapButt)
		g.moveTo(0.70, 0.33)
		g.lineTo(0.30, 0.67)
		g.strokeCap(wMark, vector.LineCapButt)
		g.line(0.04, 0.50, 0.24, 0.50, wHair)
		g.line(0.76, 0.50, 0.96, 0.50, wHair)

	case glyphAddBeat:

		drawBeatColumn(g)
		drawPlus(g, 0.82, 0.30, 0.14)
	case glyphAddBar:

		g.line(0.20, 0.22, 0.20, 0.78, wMark)
		g.line(0.40, 0.22, 0.40, 0.78, wMark)
		drawPlus(g, 0.72, 0.50, 0.16)
	case glyphDeleteBeat:

		drawBeatColumn(g)
		g.line(0.14, 0.50, 0.88, 0.50, wMark)

	case glyphUndo:
		drawUndo(g, false)
	case glyphRedo:
		drawUndo(g, true)
	case glyphSave:

		g.line(0.50, 0.15, 0.50, 0.52, wStem)
		g.moveTo(0.34, 0.40)
		g.lineTo(0.50, 0.58)
		g.lineTo(0.66, 0.40)
		g.stroke(wStem)
		g.moveTo(0.20, 0.65)
		g.lineTo(0.20, 0.80)
		g.lineTo(0.80, 0.80)
		g.lineTo(0.80, 0.65)
		g.stroke(wStem)
	case glyphPlay:
		g.moveTo(0.30, 0.18)
		g.lineTo(0.30, 0.82)
		g.lineTo(0.82, 0.50)
		g.path.Close()
		g.fill()
	case glyphRecord:

		g.ring(0.50, 0.50, 0.40, 0.075)
		g.dot(0.50, 0.50, 0.20)
	case glyphTuner:

		g.line(0.30, 0.10, 0.30, 0.48, wMark)
		g.line(0.70, 0.10, 0.70, 0.48, wMark)
		g.moveTo(0.30, 0.48)
		g.quadTo(0.50, 0.76, 0.70, 0.48)
		g.stroke(wMark)
		g.line(0.50, 0.62, 0.50, 0.92, wMark)
	case glyphTextView:

		g.page(0.22, 0.12, 0.82, 0.88, 0.18)
		g.line(0.33, 0.475, 0.71, 0.475, wHair)
		g.line(0.33, 0.625, 0.71, 0.625, wHair)
		g.line(0.33, 0.775, 0.57, 0.775, wHair)
	case glyphGridView:

		g.system(0.14, 0.26, 0.74, 0.94, 3)
		g.noteHead(0.46, 0.38, true)
		g.noteHead(0.74, 0.62, true)
	case glyphHelp:
		drawQuestionMark(g, 0.5, 0.5, 0.62)

	case glyphTrack:

		g.system(0.22, 0.28, 0.72, 0.90, 3)
	case glyphAddTrack:
		g.system(0.16, 0.28, 0.72, 0.62, 3)
		drawPlus(g, 0.82, 0.50, 0.15)
	case glyphTuning:

		g.line(0.06, 0.22, 0.94, 0.22, wHeavy)
		for i := 0; i < 6; i++ {
			x := 0.175 + float64(i)*0.13
			g.line(x, 0.26, x, 0.90, wHair)
		}
	case glyphSlur:

		drawSlurredPair(g, 0.72, 0.72)
	case glyphWind:

		g.moveTo(0.10, 0.78)
		g.quadTo(0.55, 0.70, 0.72, 0.40)
		g.stroke(wHeavy)
		g.moveTo(0.72, 0.40)
		g.lineTo(0.62, 0.16)
		g.moveTo(0.72, 0.40)
		g.lineTo(0.92, 0.28)
		g.stroke(wStem)
		g.line(0.06, 0.84, 0.14, 0.72, wStem)
	case glyphCapo:

		for i := 0; i < 4; i++ {
			y := 0.225 + float64(i)*0.15
			g.line(0.10, y, 0.90, y, wHair)
		}
		g.line(0.62, 0.14, 0.62, 0.86, wHeavy)
	case glyphTempo:

		g.moveTo(0.32, 0.84)
		g.lineTo(0.43, 0.20)
		g.lineTo(0.57, 0.20)
		g.lineTo(0.68, 0.84)
		g.path.Close()
		g.stroke(wStem)
		g.line(0.50, 0.78, 0.63, 0.32, wHair)
	case glyphFolder:

		g.moveTo(0.12, 0.76)
		g.lineTo(0.12, 0.26)
		g.lineTo(0.40, 0.26)
		g.lineTo(0.48, 0.38)
		g.lineTo(0.88, 0.38)
		g.lineTo(0.88, 0.76)
		g.path.Close()
		g.stroke(wStem)
	case glyphNewPiece:

		g.page(0.12, 0.12, 0.62, 0.88, 0.16)
		g.line(0.22, 0.475, 0.52, 0.475, wHair)
		g.line(0.22, 0.625, 0.52, 0.625, wHair)
		drawPlus(g, 0.80, 0.66, 0.15)
	case glyphPencil:

		g.moveTo(0.22, 0.66)
		g.lineTo(0.76, 0.12)
		g.lineTo(0.88, 0.24)
		g.lineTo(0.34, 0.78)
		g.path.Close()
		g.stroke(wHair)
		g.line(0.32, 0.56, 0.44, 0.68, wHair)
		g.moveTo(0.14, 0.86)
		g.lineTo(0.34, 0.78)
		g.lineTo(0.22, 0.66)
		g.path.Close()
		g.fill()
	case glyphSliders:

		for i, at := range [3]float64{0.30, 0.54, 0.74} {
			y := 0.275 + float64(i)*0.225
			g.line(0.10, y, at-0.07, y, wHair)
			g.line(at+0.07, y, 0.90, y, wHair)
			g.line(at, y-0.115, at, y+0.115, wMark)
		}
	case glyphTitle:

		g.line(0.14, 0.25, 0.86, 0.25, wHeavy)
		g.line(0.14, 0.525, 0.86, 0.525, wHair)
		g.line(0.14, 0.675, 0.86, 0.675, wHair)
		g.line(0.14, 0.825, 0.86, 0.825, wHair)
	}
}

const noteHeadY = 0.74

func drawNote(g *glyphCanvas, flags int, stem, filled bool) {
	const headCX = 0.35
	if !stem {

		g.wholeNoteHead(0.5, 0.5)
		return
	}
	g.noteHead(headCX, noteHeadY, filled)

	x := stemX(headCX)
	g.line(x, noteHeadY-0.05, x, 0.11, wStem)
	g.flags(x, 0.11, flags)
}

func drawSlurredPair(g *glyphCanvas, fromY, toY float64) {
	g.noteHead(0.24, fromY, true)
	g.noteHead(0.76, toY, true)
	peak := math.Min(fromY, toY) - 0.16
	g.moveTo(0.24, fromY-0.14)
	g.quadTo(0.50, peak, 0.76, toY-0.14)
	g.stroke(wStem)
}

func drawRestBlock(g *glyphCanvas, line, depth float64) {
	g.moveTo(0.34, line)
	g.lineTo(0.34, depth)
	g.lineTo(0.66, depth)
	g.lineTo(0.66, line)
	g.path.Close()
	g.fill()
}

func drawBeatColumn(g *glyphCanvas) {
	g.line(0.06, 0.30, 0.64, 0.30, wHair)
	g.line(0.06, 0.70, 0.64, 0.70, wHair)
	g.line(0.38, 0.20, 0.38, 0.80, wMark)
}

func drawQuarterRest(g *glyphCanvas) {
	g.moveTo(0.38, 0.14)
	g.lineTo(0.64, 0.36)
	g.lineTo(0.38, 0.56)
	g.lineTo(0.64, 0.74)
	g.stroke(wMark)
	g.moveTo(0.64, 0.74)
	g.quadTo(0.38, 0.70, 0.46, 0.90)
	g.stroke(wStem)
}

func drawPlus(g *glyphCanvas, x, y, r float64) {
	g.line(x-r, y, x+r, y, wStem)
	g.line(x, y-r, x, y+r, wStem)
}

func drawUndo(g *glyphCanvas, mirrored bool) {
	m := func(x float64) float64 {
		if mirrored {
			return 1 - x
		}
		return x
	}

	g.moveTo(m(0.20), 0.46)
	g.quadTo(m(0.52), 0.10, m(0.80), 0.44)
	g.quadTo(m(0.88), 0.62, m(0.70), 0.80)
	g.stroke(wStem)

	g.moveTo(m(0.36), 0.30)
	g.lineTo(m(0.17), 0.47)
	g.lineTo(m(0.38), 0.60)
	g.stroke(wStem)
}

func drawDigit3(g *glyphCanvas, x, y, h float64) {
	top, bot := y-h/2, y+h/2
	g.moveTo(x-h*0.32, top)
	g.quadTo(x+h*0.42, top-h*0.06, x, y)
	g.quadTo(x+h*0.48, y+h*0.08, x-h*0.32, bot)
	g.stroke(wStem)
}

func drawLetterH(g *glyphCanvas, x, y, h float64) {
	half := h / 2
	left, right := x-h*0.32, x+h*0.32
	g.line(left, y-half, left, y+half, wStem)
	g.line(right, y-half, right, y+half, wStem)
	g.line(left, y, right, y, wStem)
}

func drawLetterP(g *glyphCanvas, x, y, h float64) {
	half := h / 2
	stem := x - h*0.32
	g.line(stem, y-half, stem, y+half, wStem)
	g.moveTo(stem, y-half)
	g.quadTo(stem+h*1.05, y-half*0.55, stem, y+h*0.08)
	g.stroke(wStem)
}

func drawQuestionMark(g *glyphCanvas, x, y, h float64) {
	top := y - h/2
	g.moveTo(x-h*0.26, top+h*0.16)
	g.quadTo(x+h*0.36, top-h*0.14, x+h*0.20, top+h*0.34)
	g.quadTo(x+h*0.06, top+h*0.50, x, top+h*0.66)
	g.stroke(wStem)
	g.dot(x, y+h*0.42, 0.055)
}
