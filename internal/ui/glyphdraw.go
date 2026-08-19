package ui

// The glyph recipes: what each symbol actually looks like.
//
// Every one is written against the unit box (see glyph.go), and every one
// is a symbol a guitarist has already met — on a stave, in a tab book, or
// on the transport bar of any application that plays anything. Where a
// symbol is invented rather than inherited it says so, because an
// invented icon has to earn its place against a plain word, and mostly
// does not.

import (
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// drawGlyph paints a symbol, centred in box, in col.
//
// The box is SNAPPED to whole pixels first. Every straight in these
// recipes is placed on the half-pixel grid so that a one-pixel stroke
// resolves to one crisp row (see glyph.go), and that only holds if the
// box it is scaled into starts on a pixel. It also fixes something worse:
// a hovered control lifts by animLift pixels, which is not a whole
// number, so without snapping the whole glyph changed raster phase as the
// cursor arrived — crisp rails turned to half-alpha smears and back, and
// the icon appeared to change weight when you pointed at it.
//
// An unknown glyph draws nothing rather than a placeholder: a control
// that has lost its icon should look bare and wrong, not confidently
// wrong.
func drawGlyph(dst *ebiten.Image, id glyphID, box rect, col color.RGBA) {
	// Square and centred: the recipes assume equal axes.
	side := math.Round(math.Min(box.w, box.h))
	// One package-level canvas, reused: drawGlyph runs dozens of times
	// per frame (every toolbar icon, every rest in the tab), and a fresh
	// canvas per call re-grew the vector path's arrays from nil each
	// time. Ebitengine draws on one goroutine, and every recipe ends in
	// stroke or fill, which Reset the path — so the scratch is clean
	// between glyphs and its capacity is the only thing that survives.
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
		// A quarter note with its augmentation dot, which is exactly what
		// dotting a note does. The dot sits LEVEL with the head — raised,
		// it would read on a stave as a dot displaced into the space
		// above, which is a different thing that means the same thing and
		// only when the note is on a line.
		drawNote(g, 0, true, true)
		g.dot(0.62, noteHeadY, 0.048)
	case glyphTriplet:
		// The bracketed 3 written over a triplet group: hooks turned
		// toward the notes, and the numeral set IN the break in the
		// bracket rather than floating above an unbroken one.
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
		// A whole rest hangs UNDER its line; a half rest sits ON one. That
		// is the only thing that tells them apart, and getting it the
		// wrong way round is the sort of mistake a musician sees before
		// they read anything else on the screen.
		g.line(0.18, 0.425, 0.82, 0.425, wHair)
		drawRestBlock(g, 0.425, 0.62)
	case glyphRestHalf:
		g.line(0.18, 0.575, 0.82, 0.575, wHair)
		drawRestBlock(g, 0.575, 0.38)

	case glyphTie:
		// Two noteheads joined by the arc that means "do not strike the
		// second one". A tie is flatter than a slur and springs from the
		// heads themselves, which is what tells it from the hammer-on and
		// pull-off sitting beside it on the same toolbar row.
		g.noteHead(0.24, 0.70, true)
		g.noteHead(0.76, 0.70, true)
		g.moveTo(0.24, 0.55)
		g.quadTo(0.50, 0.33, 0.76, 0.55)
		g.stroke(wStem)
	case glyphHammer:
		// A hammer-on ASCENDS. The first version drew both heads at the
		// same height and left the whole distinction to a six-pixel
		// letter, so the two buttons were indistinguishable blobs; the
		// direction is the part a guitarist reads first, and the letter
		// only confirms it.
		drawSlurredPair(g, 0.82, 0.60)
		drawLetterH(g, 0.50, 0.22, 0.28)
	case glyphPull:
		drawSlurredPair(g, 0.60, 0.82)
		drawLetterP(g, 0.50, 0.22, 0.28)
	case glyphSlide:
		// Two noteheads with the straight line between them that a tab
		// slide is drawn as.
		g.noteHead(0.22, 0.74, true)
		g.noteHead(0.78, 0.34, true)
		g.line(0.38, 0.63, 0.62, 0.45, wStem)
	case glyphBend:
		// The arrow that curves up from the note, with its head.
		g.noteHead(0.24, 0.78, true)
		g.moveTo(0.40, 0.70)
		g.quadTo(0.76, 0.64, 0.78, 0.28)
		g.stroke(wStem)
		g.moveTo(0.68, 0.36)
		g.lineTo(0.78, 0.17)
		g.lineTo(0.88, 0.36)
		g.stroke(wStem)
	case glyphVibrato:
		// The wavy line written over a vibratoed note.
		g.moveTo(0.09, 0.50)
		g.quadTo(0.21, 0.26, 0.33, 0.50)
		g.quadTo(0.45, 0.74, 0.57, 0.50)
		g.quadTo(0.69, 0.26, 0.81, 0.50)
		g.quadTo(0.87, 0.61, 0.91, 0.55)
		g.stroke(wMark)
	case glyphDead:
		// The cross that replaces a fret number on a muted string — WITH
		// the string it replaces, entering on one side and leaving on the
		// other. Without it the mark is a bare X, and a bare X on a
		// toolbar means "close", which is the opposite of a note you play.
		// Butt caps, because an X notehead is cut to a point rather than
		// ending in a blob the width of its own stroke.
		g.moveTo(0.30, 0.33)
		g.lineTo(0.70, 0.67)
		g.strokeCap(wMark, vector.LineCapButt)
		g.moveTo(0.70, 0.33)
		g.lineTo(0.30, 0.67)
		g.strokeCap(wMark, vector.LineCapButt)
		g.line(0.04, 0.50, 0.24, 0.50, wHair)
		g.line(0.76, 0.50, 0.96, 0.50, wHair)

	case glyphAddBeat:
		// A COLUMN of the tab grid, plus. Not a notehead with a plus: a
		// notehead means "note value" eight buttons to the left in the
		// same toolbar, so a head here said "add a note" — and a note is
		// added by typing a fret, not by pressing this. What this inserts
		// is a column, so a column is what it draws.
		drawBeatColumn(g)
		drawPlus(g, 0.82, 0.30, 0.14)
	case glyphAddBar:
		// A barline pair with a plus: one vertical is a beat, two are a
		// bar, which makes the two structure glyphs a small language
		// rather than two unrelated pictures.
		g.line(0.20, 0.22, 0.20, 0.78, wMark)
		g.line(0.40, 0.22, 0.40, 0.78, wMark)
		drawPlus(g, 0.72, 0.50, 0.16)
	case glyphDeleteBeat:
		// The same column, STRUCK OUT. Against the plussed one it differs
		// by its whole silhouette; the first version differed by a single
		// one-and-a-half pixel tick of a plus, between two adjacent
		// controls one of which is destructive.
		drawBeatColumn(g)
		g.line(0.14, 0.50, 0.88, 0.50, wMark)

	case glyphUndo:
		drawUndo(g, false)
	case glyphRedo:
		drawUndo(g, true)
	case glyphSave:
		// An arrow into a tray: the modern save, and one that does not
		// depend on anybody remembering a floppy disk.
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
		// A filled disc inside a ring. The bare disc every recorder uses
		// is indistinguishable from a bullet at this size; the ring is
		// what makes it a button rather than a mark.
		g.ring(0.50, 0.50, 0.40, 0.075)
		g.dot(0.50, 0.50, 0.20)
	case glyphTuner:
		// A tuning fork: two tines, the shoulder they spring from, and
		// the stem you hold it by. The tines have to be WIDE and the
		// shoulder DEEP — drawn narrow and shallow the whole thing reads
		// as the letter Y.
		g.line(0.30, 0.10, 0.30, 0.48, wMark)
		g.line(0.70, 0.10, 0.70, 0.48, wMark)
		g.moveTo(0.30, 0.48)
		g.quadTo(0.50, 0.76, 0.70, 0.48)
		g.stroke(wMark)
		g.line(0.50, 0.62, 0.50, 0.92, wMark)
	case glyphTextView:
		// A PAGE, not a stack of bars. This control means "the text file
		// this piece saves to", and four bare lines carry no notion of a
		// file at all — while being the same drawing as the track, title
		// and settings marks that share the screen with it.
		g.page(0.22, 0.12, 0.82, 0.88, 0.18)
		g.line(0.33, 0.475, 0.71, 0.475, wHair)
		g.line(0.33, 0.625, 0.71, 0.625, wHair)
		g.line(0.33, 0.775, 0.57, 0.775, wHair)
	case glyphGridView:
		// The stave family's mark with notes on it, so the pair of view
		// toggles reads page against music rather than four bars against
		// four bars. Three lines rather than four, and the heads sit in
		// the SPACES: a head is 4.8 px tall against a 4.8 px gap, so one
		// centred on a line swallows the line and both gaps around it.
		g.system(0.14, 0.26, 0.74, 0.94, 3)
		g.noteHead(0.46, 0.38, true)
		g.noteHead(0.74, 0.62, true)
	case glyphHelp:
		drawQuestionMark(g, 0.5, 0.5, 0.62)

	case glyphTrack:
		// A stave hung on a system barline: one part of the piece.
		g.system(0.22, 0.28, 0.72, 0.90, 3)
	case glyphAddTrack:
		g.system(0.16, 0.28, 0.72, 0.62, 3)
		drawPlus(g, 0.82, 0.50, 0.15)
	case glyphTuning:
		// The nut, with six strings hanging off it. The first version
		// drew six strings of increasing gauge and nothing else — but
		// stroke() floors a stroke at one pixel, so the three thinnest
		// came out identical and the gauge idea, which was the whole
		// point, was invisible. One heavy element plus six hairlines
		// survives the raster; a graded comb does not.
		g.line(0.06, 0.22, 0.94, 0.22, wHeavy)
		for i := 0; i < 6; i++ {
			x := 0.175 + float64(i)*0.13
			g.line(x, 0.26, x, 0.90, wHair)
		}
	case glyphCapo:
		// A capo: the bar clamped across the strings, and the strings it
		// crosses. The bar is the heavy element, as it is on the guitar.
		for i := 0; i < 4; i++ {
			y := 0.225 + float64(i)*0.15
			g.line(0.10, y, 0.90, y, wHair)
		}
		g.line(0.62, 0.14, 0.62, 0.86, wHeavy)
	case glyphTempo:
		// A metronome: the tapered body and its pendulum.
		g.moveTo(0.32, 0.84)
		g.lineTo(0.43, 0.20)
		g.lineTo(0.57, 0.20)
		g.lineTo(0.68, 0.84)
		g.path.Close()
		g.stroke(wStem)
		g.line(0.50, 0.78, 0.63, 0.32, wHair)
	case glyphFolder:
		// A folder: the tab, then the body.
		g.moveTo(0.12, 0.76)
		g.lineTo(0.12, 0.26)
		g.lineTo(0.40, 0.26)
		g.lineTo(0.48, 0.38)
		g.lineTo(0.88, 0.38)
		g.lineTo(0.88, 0.76)
		g.path.Close()
		g.stroke(wStem)
	case glyphNewPiece:
		// A blank page with a plus. Against the add-track mark — which is
		// a stave — it now differs by silhouette rather than by one bar:
		// "start a song" and "add a part to this song" are very different
		// things to be told by the same picture.
		g.page(0.12, 0.12, 0.62, 0.88, 0.16)
		g.line(0.22, 0.475, 0.52, 0.475, wHair)
		g.line(0.22, 0.625, 0.52, 0.625, wHair)
		drawPlus(g, 0.80, 0.66, 0.15)
	case glyphPencil:
		// A pencil: an OUTLINED body with a filled nib on the end of it.
		// The first version was one 2.2 px diagonal whose round cap
		// swallowed the nib whole, so the button that means "write" was
		// drawn as a stray slash. Outlining the body is what gives the
		// mark a width — a solid diagonal slab is the same failure with
		// more ink — and the ferrule line across it is what stops the
		// outline reading as a rectangle standing on its corner.
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
		// Three sliders at different settings — the modern settings mark,
		// and one that says "adjustable" where a cogwheel says
		// "machinery". The rail BREAKS at each handle and the handle is a
		// vertical bar: filled dots on an unbroken rail read as a lumpy
		// line, which is to say as another stack of bars.
		for i, at := range [3]float64{0.30, 0.54, 0.74} {
			y := 0.275 + float64(i)*0.225
			g.line(0.10, y, at-0.07, y, wHair)
			g.line(at+0.07, y, 0.90, y, wHair)
			g.line(at, y-0.115, at, y+0.115, wMark)
		}
	case glyphTitle:
		// A heading over a system: one dominant rule above three
		// hairlines. Weight contrast, rather than three bars of ragged
		// length — which was the same picture as the track mark two
		// buttons away on the same row.
		g.line(0.14, 0.25, 0.86, 0.25, wHeavy)
		g.line(0.14, 0.525, 0.86, 0.525, wHair)
		g.line(0.14, 0.675, 0.86, 0.675, wHair)
		g.line(0.14, 0.825, 0.86, 0.825, wHair)
	}
}

// noteHeadY is where a stemmed note's head sits. Named because the
// augmentation dot has to line up with it exactly.
const noteHeadY = 0.74

// drawNote draws a note value: a head, optionally a stem, and n flags.
func drawNote(g *glyphCanvas, flags int, stem, filled bool) {
	const headCX = 0.35
	if !stem {
		// A whole note is a head and nothing else, so it sits in the
		// middle of the box.
		g.wholeNoteHead(0.5, 0.5)
		return
	}
	g.noteHead(headCX, noteHeadY, filled)
	// The stem rises from the right of the head, as it does on any note
	// below the middle line.
	x := stemX(headCX)
	g.line(x, noteHeadY-0.05, x, 0.11, wStem)
	g.flags(x, 0.11, flags)
}

// drawSlurredPair is the shape a hammer-on and a pull-off share: two
// noteheads under a slur, at the heights that say which way the movement
// goes. The letter over it only confirms what the direction already said.
func drawSlurredPair(g *glyphCanvas, fromY, toY float64) {
	g.noteHead(0.24, fromY, true)
	g.noteHead(0.76, toY, true)
	peak := math.Min(fromY, toY) - 0.16
	g.moveTo(0.24, fromY-0.14)
	g.quadTo(0.50, peak, 0.76, toY-0.14)
	g.stroke(wStem)
}

// drawRestBlock draws the filled rectangle of a whole or half rest,
// hanging from its line or sitting on it. depth is the far edge.
func drawRestBlock(g *glyphCanvas, line, depth float64) {
	g.moveTo(0.34, line)
	g.lineTo(0.34, depth)
	g.lineTo(0.66, depth)
	g.lineTo(0.66, line)
	g.path.Close()
	g.fill()
}

// drawBeatColumn draws one column of the tab grid: the strings it crosses
// and the column itself. It is what the add-beat and remove-beat marks
// are built on, so the two say "a column of the grid" before they say
// what is being done to it.
func drawBeatColumn(g *glyphCanvas) {
	g.line(0.06, 0.30, 0.64, 0.30, wHair)
	g.line(0.06, 0.70, 0.64, 0.70, wHair)
	g.line(0.38, 0.20, 0.38, 0.80, wMark)
}

// drawQuarterRest draws the zigzag of a quarter rest. It is the one glyph
// here that is genuinely hard to reduce — the engraved shape is a
// calligraphic squiggle — so this is its skeleton: the strokes that make
// it recognisable at twenty pixels.
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

// drawPlus draws a plus sign centred at (x, y) with the given half-width.
func drawPlus(g *glyphCanvas, x, y, r float64) {
	g.line(x-r, y, x+r, y, wStem)
	g.line(x, y-r, x, y+r, wStem)
}

// drawUndo draws the curved arrow every application uses for undo, and
// its mirror for redo.
func drawUndo(g *glyphCanvas, mirrored bool) {
	m := func(x float64) float64 {
		if mirrored {
			return 1 - x
		}
		return x
	}
	// The arc: back over the top and down to the far side.
	g.moveTo(m(0.20), 0.46)
	g.quadTo(m(0.52), 0.10, m(0.80), 0.44)
	g.quadTo(m(0.88), 0.62, m(0.70), 0.80)
	g.stroke(wStem)
	// The head, on the tail the arc starts from.
	g.moveTo(m(0.36), 0.30)
	g.lineTo(m(0.17), 0.47)
	g.lineTo(m(0.38), 0.60)
	g.stroke(wStem)
}

// drawDigit3 draws the 3 of a triplet bracket: two bowls, at a size the
// text renderer cannot reach without dropping a different typeface into
// the middle of a symbol.
func drawDigit3(g *glyphCanvas, x, y, h float64) {
	top, bot := y-h/2, y+h/2
	g.moveTo(x-h*0.32, top)
	g.quadTo(x+h*0.42, top-h*0.06, x, y)
	g.quadTo(x+h*0.48, y+h*0.08, x-h*0.32, bot)
	g.stroke(wStem)
}

// The two letters the symbols need are drawn as strokes rather than set
// as text.
//
// They have to obey the unit box like everything else here: the text
// renderer anchors at the top of a LINE BOX, which is taller than the ink
// and taller than the glyph box these are drawn in, so a letter placed
// that way lands wherever the face's ascent happens to put it and does
// not scale with the symbol around it. The H of a hammer-on was being
// drawn straight through the slur and into both noteheads because of it.
// Two strokes each are cheaper than the metrics arithmetic that would fix
// the alternative, and they match the weight of the marks beside them.

// drawLetterH draws an H centred at x with its cap height h, the ink
// vertically centred on y.
func drawLetterH(g *glyphCanvas, x, y, h float64) {
	half := h / 2
	left, right := x-h*0.32, x+h*0.32
	g.line(left, y-half, left, y+half, wStem)
	g.line(right, y-half, right, y+half, wStem)
	g.line(left, y, right, y, wStem)
}

// drawLetterP draws a P the same way: a stem with a bowl that closes
// BELOW the midline, which is what stops it reading as a flag on a pole.
func drawLetterP(g *glyphCanvas, x, y, h float64) {
	half := h / 2
	stem := x - h*0.32
	g.line(stem, y-half, stem, y+half, wStem)
	g.moveTo(stem, y-half)
	g.quadTo(stem+h*1.05, y-half*0.55, stem, y+h*0.08)
	g.stroke(wStem)
}

// drawQuestionMark draws the help symbol: the hook and its dot.
func drawQuestionMark(g *glyphCanvas, x, y, h float64) {
	top := y - h/2
	g.moveTo(x-h*0.26, top+h*0.16)
	g.quadTo(x+h*0.36, top-h*0.14, x+h*0.20, top+h*0.34)
	g.quadTo(x+h*0.06, top+h*0.50, x, top+h*0.66)
	g.stroke(wStem)
	g.dot(x, y+h*0.42, 0.055)
}
