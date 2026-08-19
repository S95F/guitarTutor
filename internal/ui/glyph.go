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
// takes paths directly, and shipping a music font to draw twenty symbols
// would be a megabyte to solve a problem that is a hundred lines of
// geometry.
//
// Every glyph is written in a UNIT BOX — coordinates from 0 to 1, origin
// top left — and scaled into whatever rectangle it is drawn in. That is
// what lets the same recipe be a 14-pixel mark in a crowded row and a
// 28-pixel one on a specimen sheet without a second set of numbers, and
// it is why the recipes in glyphdraw.go read as descriptions of a shape
// rather than as pixel arithmetic.
//
// Three rules make the set a SYSTEM rather than a pile of drawings, and
// all three were learnt by measuring the first version's pixels:
//
//   - ONE WEIGHT SCALE. The first set used ten different stroke widths
//     across thirty-seven glyphs — 1.0 px for a slur, 2.2 px for a
//     pencil that sat two buttons away from it. Everything now picks from
//     wHair/wStem/wMark/wHeavy below, and a glyph gets at most one wHeavy
//     stroke: the one that carries its silhouette.
//
//   - PIXEL FIT. At glyphBox a unit is twenty pixels, so a coordinate
//     ending in a whole tenth lands on a pixel BOUNDARY and a one-pixel
//     stroke centred there smears across two rows at half alpha. Straight
//     horizontals and verticals therefore sit on the half-pixel grid
//     (odd multiples of 0.025 for a hair stroke, multiples of 0.05 for a
//     two-pixel one). Diagonals and curves are exempt: they are
//     antialiased whatever you do.
//
//   - DISTINCT SILHOUETTES. Seven of the first set's glyphs were a stack
//     of horizontal bars in a 20 px box, four of them on screen at once,
//     and covering the captions they all said "menu". A glyph is now
//     identified by its OUTLINE — a page, a stave with a system barline,
//     a heavy rule over hairlines, a column — never by a bar count.

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
	glyphRecord
	glyphTuner

	// Piece-wide settings.
	glyphTrack
	glyphAddTrack
	glyphTuning
	glyphCapo
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

// The stroke-weight scale, in unit-box widths. At glyphBox these are 1.0,
// 1.5, 2.0 and 2.7 pixels.
//
// Only wHair and wMark are used for straight horizontals and verticals,
// because only whole-pixel weights resolve cleanly on the half-pixel
// grid. wStem is for stems, slurs and arrows — strokes that are diagonal
// or curved, and so antialiased regardless. wHeavy is the accent: at most
// one per glyph, and it is what the eye finds first.
const (
	wHair  = 0.050
	wStem  = 0.075
	wMark  = 0.100
	wHeavy = 0.135
)

// The notehead. Written as constants rather than inline because eight
// glyphs share it and a head that changed size in one of them would break
// the family resemblance that makes the notation marks readable.
//
// The proportions are an ICON's, not an engraver's. A real notehead is
// one staff space tall against a stem of three and a half, which at
// twenty pixels puts the head at four pixels and the counter of a hollow
// one under a pixel — correct, and illegible. These are as close to
// engraved proportion as survives the raster.
const (
	headRX  = 0.170
	headRY  = 0.120
	headRot = -0.34 // about twenty degrees, as engraved

	// The counter of a hollow head is NOT a scaled copy of the outer
	// oval. A real minim's white is cut at a different angle from its
	// black, which leaves the ring thin at the two ends of the long axis
	// and thick along the flanks; a concentric counter gives a ring of
	// even thickness, and an even ring is a typographic O rather than a
	// notehead. This is the single detail that makes a half note read as
	// a half note at this size.
	counterRX  = headRX * 0.78
	counterRY  = headRY * 0.36
	counterRot = -0.62
)

// The semibreve is a different shape, not a bigger notehead: its outer
// oval is HORIZONTAL — the tilt of a whole note lives entirely in its
// counter — and it is proportionally wider than a stemmed head.
const (
	wholeRX  = 0.235
	wholeRY  = 0.138
	wholeRot = 0.0

	wholeCounterRX  = 0.125
	wholeCounterRY  = 0.058
	wholeCounterRot = -0.75
)

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

// glyphScratch is drawGlyph's reused canvas; see the note there. Package
// level so the path keeps its grown capacity across calls and frames.
var glyphScratch glyphCanvas

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

// ellipseArcs is the geometry of ellipse, split out so it can be checked
// without a window: it returns the start point and the four cubic
// segments, each as {c1x, c1y, c2x, c2y, endx, endy}, all in unit-box
// coordinates.
func ellipseArcs(cx, cy, rx, ry, rot float64) (start [2]float64, segs [4][6]float64) {
	const k = 0.5522847498307936 // 4/3 * (sqrt(2) - 1)
	sin, cos := math.Sin(rot), math.Cos(rot)
	// rotate maps a point given relative to the centre, unrotated.
	rotate := func(dx, dy float64) (float64, float64) {
		return cx + dx*cos - dy*sin, cy + dx*sin + dy*cos
	}
	type pt struct{ x, y float64 }
	// The four quadrant ends, and for each one the two control offsets
	// that reach it FROM THE PREVIOUS END. Segment i runs ends[i-1] to
	// ends[i], so ctrl[i] must be the pair that bows through the quadrant
	// between them — pairing each segment with the NEXT quadrant's
	// controls instead put every bulge one quadrant round, which turned
	// the ellipse into a four-cusped lozenge. It went unnoticed for as
	// long as noteheads were solid blobs a few pixels across; the moment
	// one was cut hollow it read as a four-pointed star.
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

// stroke draws the path built so far at the given unit width and clears
// it. Round caps and joins: most of these symbols are a pen stroke, and
// mitred corners on a two-pixel line read as burrs.
func (g *glyphCanvas) stroke(width float64) {
	g.strokeCap(width, vector.LineCapRound)
}

// strokeCap is stroke with the terminal spelt out, for the few marks that
// are cut rather than drawn — a dead note's cross ends in a point, not in
// a blob the width of the stroke.
func (g *glyphCanvas) strokeCap(width float64, cap vector.LineCap) {
	w := g.unit(width)
	if w < 1 {
		w = 1 // under a pixel a stroke fades out rather than thinning
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

// fill fills the path built so far and clears it.
func (g *glyphCanvas) fill() { g.fillRule(vector.FillRuleNonZero) }

// fillHollow fills the path built so far with the EVEN-ODD rule, so a
// shape containing another shape comes out as a ring.
//
// This is how every hollow notehead here is cut. The alternative — fill
// the outer shape, then fill the inner one again in whatever colour is
// behind — was what the first version did, and it was wrong twice over:
// it made a hollow head need to be told the colour behind it (which a
// hovered button changes every frame, and a rounded corner or a flash
// ring makes a lie), and the two fills' antialiased edges met in a seam
// that at twenty pixels ate most of the ring.
func (g *glyphCanvas) fillHollow() { g.fillRule(vector.FillRuleEvenOdd) }

func (g *glyphCanvas) fillRule(rule vector.FillRule) {
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(g.col)
	vector.FillPath(g.dst, &g.path, &vector.FillOptions{FillRule: rule}, op)
	g.path.Reset()
}

// line is the common case: one straight stroke.
func (g *glyphCanvas) line(x1, y1, x2, y2, width float64) {
	g.moveTo(x1, y1)
	g.lineTo(x2, y2)
	g.stroke(width)
}

// dot fills a small circle — the augmentation dot, the record light, and
// the head of an arrow that has nowhere to point.
func (g *glyphCanvas) dot(x, y, r float64) {
	g.ellipse(x, y, r, r, 0)
	g.fill()
}

// ring draws a circle outline as a true annulus rather than as a stroke,
// so its weight is set in the same units as everything else here.
func (g *glyphCanvas) ring(x, y, r, thick float64) {
	g.ellipse(x, y, r, r, 0)
	g.ellipse(x, y, r-thick, r-thick, 0)
	g.fillHollow()
}

// noteHead draws a notehead centred at (x, y): filled for a quarter and
// shorter, hollow above that.
func (g *glyphCanvas) noteHead(x, y float64, filled bool) {
	g.ellipse(x, y, headRX, headRY, headRot)
	if filled {
		g.fill()
		return
	}
	g.ellipse(x, y, counterRX, counterRY, counterRot)
	g.fillHollow()
}

// wholeNoteHead draws the semibreve: a horizontal oval with an obliquely
// cut counter, and no stem.
func (g *glyphCanvas) wholeNoteHead(x, y float64) {
	g.ellipse(x, y, wholeRX, wholeRY, wholeRot)
	g.ellipse(x, y, wholeCounterRX, wholeCounterRY, wholeCounterRot)
	g.fillHollow()
}

// stemX is where a stem meets a notehead: the far edge of the head along
// its own long axis, pulled back by half the stem's width so the two
// overlap rather than butt.
func stemX(headCX float64) float64 {
	sin, cos := math.Sin(headRot), math.Cos(headRot)
	reach := math.Hypot(headRX*cos, headRY*sin)
	return headCX + reach - wStem/2
}

// flags draws n flags hanging off the top of a stem at (x, top), the way
// an eighth note carries one and a sixteenth two.
func (g *glyphCanvas) flags(x, top float64, n int) {
	for i := 0; i < n; i++ {
		y := top + float64(i)*0.15
		g.moveTo(x, y)
		g.quadTo(x+0.26, y+0.06, x+0.20, y+0.24)
		g.stroke(wStem)
	}
}

// page strokes the outline of a sheet of paper with its top-right corner
// turned down — the shape that says "a file" in every interface anybody
// has used, and the one silhouette in this set that is not made of
// horizontal bars.
func (g *glyphCanvas) page(x0, y0, x1, y1, fold float64) {
	g.moveTo(x0, y0)
	g.lineTo(x1-fold, y0)
	g.lineTo(x1, y0+fold)
	g.lineTo(x1, y1)
	g.lineTo(x0, y1)
	g.path.Close()
	g.stroke(wHair)
	// The turned corner, drawn as the two edges of the fold.
	g.moveTo(x1-fold, y0)
	g.lineTo(x1-fold, y0+fold)
	g.lineTo(x1, y0+fold)
	g.stroke(wHair)
}

// system draws the stave family's mark: a heavy vertical system barline
// with hairline staves running off it. One thick vertical against three
// thin horizontals is a silhouette no bar stack can be mistaken for,
// which is what separates a track from a text file from a title.
func (g *glyphCanvas) system(x, top, bottom, right float64, lines int) {
	g.line(x, top, x, bottom, wHeavy)
	span := bottom - top
	for i := 0; i < lines; i++ {
		y := top + span*float64(i)/float64(lines-1)
		g.line(x+0.06, y, right, y, wHair)
	}
}
