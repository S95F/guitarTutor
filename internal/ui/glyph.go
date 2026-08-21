package ui

import (
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type glyphID int

const (
	glyphNone glyphID = iota

	glyphNoteWhole
	glyphNoteHalf
	glyphNoteQuarter
	glyphNoteEighth
	glyphNoteSixteenth
	glyphNoteThirtySecond

	glyphDotted
	glyphTriplet

	glyphRest
	glyphRestWhole
	glyphRestHalf

	glyphTie
	glyphHammer
	glyphPull
	glyphSlide
	glyphBend
	glyphVibrato
	glyphDead

	glyphAddBeat
	glyphAddBar
	glyphDeleteBeat

	glyphUndo
	glyphRedo
	glyphSave
	glyphPlay
	glyphTextView
	glyphGridView
	glyphHelp
	glyphRecord
	glyphTuner

	glyphTrack
	glyphAddTrack
	glyphTuning
	glyphCapo
	glyphTempo
	glyphTitle

	glyphWind
	glyphSlur

	glyphFolder
	glyphNewPiece
	glyphPencil
	glyphSliders
)

const glyphBox = 20.0

const (
	wHair  = 0.050
	wStem  = 0.075
	wMark  = 0.100
	wHeavy = 0.135
)

const (
	headRX  = 0.170
	headRY  = 0.120
	headRot = -0.34

	counterRX  = headRX * 0.78
	counterRY  = headRY * 0.36
	counterRot = -0.62
)

const (
	wholeRX  = 0.235
	wholeRY  = 0.138
	wholeRot = 0.0

	wholeCounterRX  = 0.125
	wholeCounterRY  = 0.058
	wholeCounterRot = -0.75
)

type glyphCanvas struct {
	dst  *ebiten.Image
	box  rect
	col  color.RGBA
	path vector.Path
}

var glyphScratch glyphCanvas

func (g *glyphCanvas) unit(v float64) float64 { return v * g.box.w }

func (g *glyphCanvas) at(x, y float64) (float32, float32) {
	return float32(g.box.x + x*g.box.w), float32(g.box.y + y*g.box.h)
}

func (g *glyphCanvas) moveTo(x, y float64) { g.path.MoveTo(g.at(x, y)) }
func (g *glyphCanvas) lineTo(x, y float64) { g.path.LineTo(g.at(x, y)) }

func (g *glyphCanvas) quadTo(cx, cy, x, y float64) {
	x1, y1 := g.at(cx, cy)
	x2, y2 := g.at(x, y)
	g.path.QuadTo(x1, y1, x2, y2)
}

func (g *glyphCanvas) ellipse(cx, cy, rx, ry, rot float64) {
	start, segs := ellipseArcs(cx, cy, rx, ry, rot)
	g.path.MoveTo(g.at(start[0], start[1]))
	for _, s := range segs {
		p1x, p1y := g.at(s[0], s[1])
		p2x, p2y := g.at(s[2], s[3])
		p3x, p3y := g.at(s[4], s[5])
		g.path.CubicTo(p1x, p1y, p2x, p2y, p3x, p3y)
	}
	g.path.Close()
}

func ellipseArcs(cx, cy, rx, ry, rot float64) (start [2]float64, segs [4][6]float64) {
	const k = 0.5522847498307936
	sin, cos := math.Sin(rot), math.Cos(rot)

	rotate := func(dx, dy float64) (float64, float64) {
		return cx + dx*cos - dy*sin, cy + dx*sin + dy*cos
	}
	type pt struct{ x, y float64 }

	ends := [4]pt{{rx, 0}, {0, ry}, {-rx, 0}, {0, -ry}}
	ctrl := [4][2]pt{
		{{k * rx, -ry}, {rx, -k * ry}},
		{{rx, k * ry}, {k * rx, ry}},
		{{-k * rx, ry}, {-rx, k * ry}},
		{{-rx, -k * ry}, {-k * rx, -ry}},
	}
	sx, sy := rotate(ends[3].x, ends[3].y)
	start = [2]float64{sx, sy}
	for i := 0; i < 4; i++ {
		c1x, c1y := rotate(ctrl[i][0].x, ctrl[i][0].y)
		c2x, c2y := rotate(ctrl[i][1].x, ctrl[i][1].y)
		ex, ey := rotate(ends[i].x, ends[i].y)
		segs[i] = [6]float64{c1x, c1y, c2x, c2y, ex, ey}
	}
	return start, segs
}

func (g *glyphCanvas) stroke(width float64) {
	g.strokeCap(width, vector.LineCapRound)
}

func (g *glyphCanvas) strokeCap(width float64, cap vector.LineCap) {
	w := g.unit(width)
	if w < 1 {
		w = 1
	}
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(g.col)
	vector.StrokePath(g.dst, &g.path, &vector.StrokeOptions{
		Width:    float32(w),
		LineCap:  cap,
		LineJoin: vector.LineJoinRound,
	}, op)
	g.path.Reset()
}

func (g *glyphCanvas) fill() { g.fillRule(vector.FillRuleNonZero) }

func (g *glyphCanvas) fillHollow() { g.fillRule(vector.FillRuleEvenOdd) }

func (g *glyphCanvas) fillRule(rule vector.FillRule) {
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(g.col)
	vector.FillPath(g.dst, &g.path, &vector.FillOptions{FillRule: rule}, op)
	g.path.Reset()
}

func (g *glyphCanvas) line(x1, y1, x2, y2, width float64) {
	g.moveTo(x1, y1)
	g.lineTo(x2, y2)
	g.stroke(width)
}

func (g *glyphCanvas) dot(x, y, r float64) {
	g.ellipse(x, y, r, r, 0)
	g.fill()
}

func (g *glyphCanvas) ring(x, y, r, thick float64) {
	g.ellipse(x, y, r, r, 0)
	g.ellipse(x, y, r-thick, r-thick, 0)
	g.fillHollow()
}

func (g *glyphCanvas) noteHead(x, y float64, filled bool) {
	g.ellipse(x, y, headRX, headRY, headRot)
	if filled {
		g.fill()
		return
	}
	g.ellipse(x, y, counterRX, counterRY, counterRot)
	g.fillHollow()
}

func (g *glyphCanvas) wholeNoteHead(x, y float64) {
	g.ellipse(x, y, wholeRX, wholeRY, wholeRot)
	g.ellipse(x, y, wholeCounterRX, wholeCounterRY, wholeCounterRot)
	g.fillHollow()
}

func stemX(headCX float64) float64 {
	sin, cos := math.Sin(headRot), math.Cos(headRot)
	reach := math.Hypot(headRX*cos, headRY*sin)
	return headCX + reach - wStem/2
}

func (g *glyphCanvas) flags(x, top float64, n int) {
	for i := 0; i < n; i++ {
		y := top + float64(i)*0.15
		g.moveTo(x, y)
		g.quadTo(x+0.26, y+0.06, x+0.20, y+0.24)
		g.stroke(wStem)
	}
}

func (g *glyphCanvas) page(x0, y0, x1, y1, fold float64) {
	g.moveTo(x0, y0)
	g.lineTo(x1-fold, y0)
	g.lineTo(x1, y0+fold)
	g.lineTo(x1, y1)
	g.lineTo(x0, y1)
	g.path.Close()
	g.stroke(wHair)

	g.moveTo(x1-fold, y0)
	g.lineTo(x1-fold, y0+fold)
	g.lineTo(x1, y0+fold)
	g.stroke(wHair)
}

func (g *glyphCanvas) system(x, top, bottom, right float64, lines int) {
	g.line(x, top, x, bottom, wHeavy)
	span := bottom - top
	for i := 0; i < lines; i++ {
		y := top + span*float64(i)/float64(lines-1)
		g.line(x+0.06, y, right, y, wHair)
	}
}
