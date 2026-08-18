package ui

// Drawn symbols.
//
// The editor used to label its controls with the letters of the file
// format it saves: h, p, s, b, v, x for the techniques, "trip" for a
// triplet, "1/4" for a note value. Those are the right letters for a text
// file and the wrong ones for a button — they are a vocabulary you have
// to have read the format documentation to hold, and a guitarist who has
// never opened docs/TEXTFORMAT.md has no way in. Every one of them is a
// thing with a SYMBOL, and the symbols are five hundred years old: a
// quarter note is a filled notehead with a stem, a tie is an arc between
// two of them, a slide is a line between two of them, a bend is an arrow
// that goes up.
//
// So this file draws them. It is deliberately not a font: the glyphs are
// a few dozen line and curve segments each, Ebitengine's vector package
// takes paths directly, and shipping a music font to draw fifteen symbols
// would be a megabyte to solve a problem that is a hundred lines of
// geometry.
//
// Every glyph is written in a UNIT BOX — coordinates from 0 to 1, origin
// top left — and scaled into whatever rectangle it is drawn in. That is
// what lets the same recipe be a 12-pixel mark in a crowded row and a
// 24-pixel one on a toolbar button without a second set of numbers, and
// it is why the recipes below read as descriptions of a shape rather than
// as pixel arithmetic.

import (
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// A glyphID names one symbol.
type glyphID int

const (
	glyphNone glyphID = iota

	// Note values. Whole and half are hollow, the rest filled; the flag
	// count is what tells an eighth from a sixteenth.
	glyphNoteWhole
	glyphNoteHalf
	glyphNoteQuarter
	glyphNoteEighth
	glyphNoteSixteenth
	glyphNoteThirtySecond

	// Modifiers on a note value.
	glyphDotted
	glyphTriplet

	glyphRest
	glyphRestWhole
	glyphRestHalf

	// Tab technique marks, as they are written on a stave.
	glyphTie
	glyphHammer
	glyphPull
	glyphSlide
	glyphBend
	glyphVibrato
	glyphDead

	// Structure.
	glyphAddBeat
	glyphAddBar
	glyphDeleteBeat

	// Actions.
	glyphUndo
	glyphRedo
	glyphSave
	glyphPlay
	glyphTextView
	glyphGridView
	glyphHelp

	// Piece-wide settings.
	glyphTrack
	glyphAddTrack
	glyphTuning
	glyphTempo
	glyphTitle

	// The start screen's actions.
	glyphFolder
	glyphNewPiece
	glyphPencil
	glyphSliders
)

// glyphBox is the side of the square a glyph is drawn in, inside a
// control. Everything is designed at this size; smaller reads as a smudge
// and larger crowds the label beside it.
const glyphBox = 20.0

// A glyphCanvas builds one symbol. Coordinates are in the unit box and
// are mapped onto the target rectangle as they are used, so a recipe
// never mentions a pixel.
//
// It carries its own path and is reset between shapes, which is what lets
// a glyph be several strokes of different weights and a fill or two
// without allocating a path per stroke.
type glyphCanvas struct {
	dst  *ebiten.Image
	box  rect
	col  color.RGBA
	path vector.Path
}

// unit converts a length in the unit box to pixels. Glyphs are drawn in a
// square, so one number is enough.
func (g *glyphCanvas) unit(v float64) float64 { return v * g.box.w }

// at maps a unit coordinate onto the target rectangle.
func (g *glyphCanvas) at(x, y float64) (float32, float32) {
	return float32(g.box.x + x*g.box.w), float32(g.box.y + y*g.box.h)
}

func (g *glyphCanvas) moveTo(x, y float64) { g.path.MoveTo(g.at(x, y)) }
func (g *glyphCanvas) lineTo(x, y float64) { g.path.LineTo(g.at(x, y)) }

// quadTo curves to (x, y) bending toward the control point.
func (g *glyphCanvas) quadTo(cx, cy, x, y float64) {
	x1, y1 := g.at(cx, cy)
	x2, y2 := g.at(x, y)
	g.path.QuadTo(x1, y1, x2, y2)
}

// ellipse adds a closed ellipse, rotated by rot radians about its centre.
// Four cubic segments with the standard circle constant: the error
// against a true ellipse is under a thousandth of the radius, which at
// twenty pixels is invisible and at any size is cheaper than an arc
// approximation that has to be re-derived per glyph.
//
// The rotation is what makes a NOTEHEAD rather than a circle — a real one
// is an oval tilted about twenty degrees, and drawing it upright is the
// single most obvious way to get music notation wrong.
func (g *glyphCanvas) ellipse(cx, cy, rx, ry, rot float64) {
	const k = 0.5522847498307936 // 4/3 * (sqrt(2) - 1)
	sin, cos := math.Sin(rot), math.Cos(rot)
	// rotate maps a point given relative to the centre, unrotated.
	rotate := func(dx, dy float64) (float64, float64) {
		return cx + dx*cos - dy*sin, cy + dx*sin + dy*cos
	}
	type pt struct{ x, y float64 }
	// The four quadrant ends and the control offsets that reach them.
	ends := [4]pt{{rx, 0}, {0, ry}, {-rx, 0}, {0, -ry}}
	ctrl := [4][2]pt{
		{{rx, k * ry}, {k * rx, ry}},
		{{-k * rx, ry}, {-rx, k * ry}},
		{{-rx, -k * ry}, {-k * rx, -ry}},
		{{k * rx, -ry}, {rx, -k * ry}},
	}
	sx, sy := rotate(ends[3].x, ends[3].y)
	g.path.MoveTo(g.at(sx, sy))
	for i := 0; i < 4; i++ {
		c1x, c1y := rotate(ctrl[i][0].x, ctrl[i][0].y)
		c2x, c2y := rotate(ctrl[i][1].x, ctrl[i][1].y)
		ex, ey := rotate(ends[i].x, ends[i].y)
		p1x, p1y := g.at(c1x, c1y)
		p2x, p2y := g.at(c2x, c2y)
		p3x, p3y := g.at(ex, ey)
		g.path.CubicTo(p1x, p1y, p2x, p2y, p3x, p3y)
	}
	g.path.Close()
}

// stroke draws the path built so far at the given unit width and clears
// it. Round caps and joins: every one of these symbols is a pen stroke,
// and mitred corners on a two-pixel line read as burrs.
func (g *glyphCanvas) stroke(width float64) {
	w := g.unit(width)
	if w < 1 {
		w = 1 // under a pixel a stroke fades out rather than thinning
	}
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(g.col)
	vector.StrokePath(g.dst, &g.path, &vector.StrokeOptions{
		Width:    float32(w),
		LineCap:  vector.LineCapRound,
		LineJoin: vector.LineJoinRound,
	}, op)
	g.path.Reset()
}

// fill fills the path built so far and clears it.
func (g *glyphCanvas) fill() {
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(g.col)
	vector.FillPath(g.dst, &g.path, nil, op)
	g.path.Reset()
}

// line is the common case: one straight stroke.
func (g *glyphCanvas) line(x1, y1, x2, y2, width float64) {
	g.moveTo(x1, y1)
	g.lineTo(x2, y2)
	g.stroke(width)
}

// dot fills a small circle — the augmentation dot, and the head of an
// arrow that has nowhere to point.
func (g *glyphCanvas) dot(x, y, r float64) {
	g.ellipse(x, y, r, r, 0)
	g.fill()
}

// noteHead draws a notehead centred at (x, y): filled for a quarter and
// shorter, hollow above that. A hollow head is drawn as a fill with the
// middle knocked back out in the control's own colour, because the
// vector package has no even-odd fill rule to hollow a shape with — and
// stroking an oval outline at this size closes up into a blob.
func (g *glyphCanvas) noteHead(x, y float64, filled bool, bg color.RGBA) {
	const (
		rx  = 0.16
		ry  = 0.115
		rot = -0.34 // about twenty degrees, as engraved
	)
	g.ellipse(x, y, rx, ry, rot)
	g.fill()
	if filled {
		return
	}
	// The hollow is knocked out at nearly two thirds of the head. A
	// smaller one leaves a ring so thin that at twenty pixels a half note
	// is indistinguishable from a quarter — which is the one distinction
	// the two glyphs exist to make.
	was := g.col
	g.col = bg
	g.ellipse(x, y, rx*0.62, ry*0.52, rot)
	g.fill()
	g.col = was
}

// wideNoteHead is noteHead for a note with no stem — a whole note, which
// is engraved wider than the rest anyway, and which needs the extra size
// here to have a hollow anybody can see.
func (g *glyphCanvas) wideNoteHead(x, y float64, filled bool, bg color.RGBA) {
	const (
		rx  = 0.215
		ry  = 0.145
		rot = -0.34
	)
	g.ellipse(x, y, rx, ry, rot)
	g.fill()
	if filled {
		return
	}
	was := g.col
	g.col = bg
	g.ellipse(x, y, rx*0.58, ry*0.48, rot)
	g.fill()
	g.col = was
}

// flags draws n flags hanging off the top of a stem at (x, top), the way
// an eighth note carries one and a sixteenth two.
func (g *glyphCanvas) flags(x, top float64, n int) {
	for i := 0; i < n; i++ {
		y := top + float64(i)*0.15
		g.moveTo(x, y)
		g.quadTo(x+0.26, y+0.06, x+0.20, y+0.24)
		g.stroke(0.075)
	}
}
