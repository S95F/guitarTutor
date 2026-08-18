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

	"github.com/hajimehoshi/ebiten/v2"
)

// drawGlyph paints a symbol, centred in box, in col. bg is the colour
// behind it, which the hollow noteheads need in order to be hollow.
//
// An unknown glyph draws nothing rather than a placeholder: a control
// that has lost its icon should look bare and wrong, not confidently
// wrong.
func drawGlyph(dst *ebiten.Image, id glyphID, box rect, col, bg color.RGBA) {
	// Square and centred: the recipes assume equal axes.
	side := box.w
	if box.h < side {
		side = box.h
	}
	g := &glyphCanvas{
		dst: dst,
		box: rect{box.x + (box.w-side)/2, box.y + (box.h-side)/2, side, side},
		col: col,
	}
	switch id {
	case glyphNoteWhole:
		drawNote(g, bg, 0, false, false)
	case glyphNoteHalf:
		drawNote(g, bg, 0, true, false)
	case glyphNoteQuarter:
		drawNote(g, bg, 0, true, true)
	case glyphNoteEighth:
		drawNote(g, bg, 1, true, true)
	case glyphNoteSixteenth:
		drawNote(g, bg, 2, true, true)
	case glyphNoteThirtySecond:
		drawNote(g, bg, 3, true, true)

	case glyphDotted:
		// A quarter note with its augmentation dot, which is exactly what
		// dotting a note does.
		drawNote(g, bg, 0, true, true)
		g.dot(0.70, 0.66, 0.055)
	case glyphTriplet:
		// The bracketed 3 written over a triplet group.
		g.moveTo(0.16, 0.62)
		g.lineTo(0.16, 0.74)
		g.lineTo(0.84, 0.74)
		g.lineTo(0.84, 0.62)
		g.stroke(0.06)
		drawDigit3(g, 0.5, 0.32, 0.30)

	case glyphRest:
		drawQuarterRest(g)
	case glyphRestWhole:
		// A whole rest hangs UNDER its line; a half rest sits ON one. That
		// is the only thing that tells them apart, and getting it the
		// wrong way round is the sort of mistake a musician sees before
		// they read anything else on the screen.
		g.line(0.20, 0.44, 0.80, 0.44, 0.05)
		g.moveTo(0.34, 0.44)
		g.lineTo(0.34, 0.60)
		g.lineTo(0.66, 0.60)
		g.lineTo(0.66, 0.44)
		g.path.Close()
		g.fill()
	case glyphRestHalf:
		g.line(0.20, 0.60, 0.80, 0.60, 0.05)
		g.moveTo(0.34, 0.60)
		g.lineTo(0.34, 0.44)
		g.lineTo(0.66, 0.44)
		g.lineTo(0.66, 0.60)
		g.path.Close()
		g.fill()

	case glyphTie:
		// Two noteheads joined by the arc that means "do not strike the
		// second one" — the tie, drawn as it is engraved.
		g.noteHead(0.24, 0.64, true, bg)
		g.noteHead(0.76, 0.64, true, bg)
		g.moveTo(0.24, 0.46)
		g.quadTo(0.50, 0.18, 0.76, 0.46)
		g.stroke(0.055)
	case glyphHammer:
		// A slur with an H, which is how a hammer-on is written in tab.
		drawSlurredPair(g, bg)
		drawLetterH(g, 0.5, 0.28, 0.30)
	case glyphPull:
		drawSlurredPair(g, bg)
		drawLetterP(g, 0.5, 0.28, 0.30)
	case glyphSlide:
		// Two noteheads with the straight line between them that a tab
		// slide is drawn as.
		g.noteHead(0.22, 0.70, true, bg)
		g.noteHead(0.78, 0.34, true, bg)
		g.line(0.37, 0.60, 0.63, 0.44, 0.06)
	case glyphBend:
		// The arrow that curves up from the note, with its head.
		g.noteHead(0.24, 0.76, true, bg)
		g.moveTo(0.38, 0.68)
		g.quadTo(0.76, 0.62, 0.78, 0.26)
		g.stroke(0.06)
		g.moveTo(0.69, 0.34)
		g.lineTo(0.78, 0.16)
		g.lineTo(0.88, 0.34)
		g.stroke(0.06)
	case glyphVibrato:
		// The wavy line written over a vibratoed note.
		g.moveTo(0.10, 0.50)
		g.quadTo(0.22, 0.28, 0.34, 0.50)
		g.quadTo(0.46, 0.72, 0.58, 0.50)
		g.quadTo(0.70, 0.28, 0.82, 0.50)
		g.quadTo(0.88, 0.60, 0.92, 0.54)
		g.stroke(0.07)
	case glyphDead:
		// The cross that replaces a fret number on a muted string.
		g.line(0.26, 0.26, 0.74, 0.74, 0.09)
		g.line(0.74, 0.26, 0.26, 0.74, 0.09)

	case glyphAddBeat:
		// A notehead with a plus: one more of these.
		g.noteHead(0.32, 0.64, true, bg)
		drawPlus(g, 0.76, 0.28, 0.15)
	case glyphAddBar:
		// A barline pair with a plus.
		g.line(0.20, 0.24, 0.20, 0.76, 0.07)
		g.line(0.40, 0.24, 0.40, 0.76, 0.07)
		drawPlus(g, 0.72, 0.50, 0.17)
	case glyphDeleteBeat:
		// A notehead with a minus, against the plus of adding one.
		g.noteHead(0.32, 0.64, true, bg)
		g.line(0.61, 0.28, 0.91, 0.28, 0.085)

	case glyphUndo:
		drawUndo(g, false)
	case glyphRedo:
		drawUndo(g, true)
	case glyphSave:
		// An arrow into a tray: the modern save, and one that does not
		// depend on anybody remembering a floppy disk.
		g.line(0.50, 0.16, 0.50, 0.54, 0.08)
		g.moveTo(0.34, 0.42)
		g.lineTo(0.50, 0.60)
		g.lineTo(0.66, 0.42)
		g.stroke(0.08)
		g.moveTo(0.20, 0.66)
		g.lineTo(0.20, 0.82)
		g.lineTo(0.80, 0.82)
		g.lineTo(0.80, 0.66)
		g.stroke(0.08)
	case glyphPlay:
		g.moveTo(0.30, 0.18)
		g.lineTo(0.30, 0.82)
		g.lineTo(0.82, 0.50)
		g.path.Close()
		g.fill()
	case glyphTextView:
		for i := 0; i < 4; i++ {
			y := 0.26 + float64(i)*0.16
			w := 0.72
			if i == 3 {
				w = 0.44
			}
			g.line(0.16, y, 0.16+w, y, 0.07)
		}
	case glyphGridView:
		for i := 0; i < 4; i++ {
			y := 0.26 + float64(i)*0.16
			g.line(0.12, y, 0.88, y, 0.05)
		}
		g.noteHead(0.36, 0.42, true, bg)
		g.noteHead(0.66, 0.74, true, bg)
	case glyphHelp:
		drawQuestionMark(g, 0.5, 0.5, 0.62)

	case glyphTrack:
		// A stack of staves: one line per part.
		for i := 0; i < 3; i++ {
			y := 0.30 + float64(i)*0.20
			g.line(0.16, y, 0.84, y, 0.07)
		}
	case glyphAddTrack:
		for i := 0; i < 3; i++ {
			y := 0.30 + float64(i)*0.20
			g.line(0.12, y, 0.54, y, 0.07)
		}
		drawPlus(g, 0.76, 0.50, 0.16)
	case glyphTuning:
		// Six strings of increasing gauge, which is what a tuning is a
		// choice about.
		for i := 0; i < 6; i++ {
			x := 0.15 + float64(i)*0.14
			g.line(x, 0.16, x, 0.84, 0.02+float64(i)*0.013)
		}
	case glyphTempo:
		// A metronome: the tapered body and its pendulum.
		g.moveTo(0.32, 0.84)
		g.lineTo(0.43, 0.20)
		g.lineTo(0.57, 0.20)
		g.lineTo(0.68, 0.84)
		g.path.Close()
		g.stroke(0.06)
		g.line(0.50, 0.78, 0.63, 0.32, 0.05)
	case glyphFolder:
		// A folder: the tab, then the body.
		g.moveTo(0.12, 0.76)
		g.lineTo(0.12, 0.26)
		g.lineTo(0.40, 0.26)
		g.lineTo(0.48, 0.38)
		g.lineTo(0.88, 0.38)
		g.lineTo(0.88, 0.76)
		g.path.Close()
		g.stroke(0.07)
	case glyphNewPiece:
		// A stave with a plus: a piece that does not exist yet.
		for i := 0; i < 4; i++ {
			y := 0.26 + float64(i)*0.16
			g.line(0.12, y, 0.56, y, 0.05)
		}
		drawPlus(g, 0.76, 0.50, 0.17)
	case glyphPencil:
		// A pencil: the body, and the nib it is sharpened to.
		g.line(0.28, 0.72, 0.74, 0.26, 0.11)
		g.moveTo(0.16, 0.84)
		g.lineTo(0.24, 0.62)
		g.lineTo(0.38, 0.76)
		g.path.Close()
		g.fill()
	case glyphSliders:
		// Three sliders at different settings — the modern settings mark,
		// and one that says "adjustable" where a cogwheel says "machinery".
		for i := 0; i < 3; i++ {
			y := 0.28 + float64(i)*0.22
			g.line(0.12, y, 0.88, y, 0.055)
			g.dot(0.30+float64(i)*0.22, y, 0.085)
		}
	case glyphTitle:
		g.line(0.16, 0.32, 0.84, 0.32, 0.085)
		g.line(0.16, 0.56, 0.62, 0.56, 0.055)
		g.line(0.16, 0.74, 0.48, 0.74, 0.055)
	}
}

// drawNote draws a note value: a head, optionally a stem, and n flags.
func drawNote(g *glyphCanvas, bg color.RGBA, flags int, stem, filled bool) {
	const (
		headX = 0.33
		headY = 0.70
		top   = 0.14
	)
	if !stem {
		// A whole note is a head and nothing else, so it sits in the
		// middle of the box — and is drawn LARGER, because a hollow head
		// at the stemmed size leaves a ring about a pixel thick, which at
		// twenty pixels reads as a smudge rather than as a note.
		g.wideNoteHead(0.5, 0.5, filled, bg)
		return
	}
	g.noteHead(headX, headY, filled, bg)
	// The stem rises from the right of the head, as it does on any note
	// below the middle line.
	stemX := headX + 0.148
	g.line(stemX, headY-0.06, stemX, top, 0.055)
	g.flags(stemX, top, flags)
}

// drawSlurredPair is the shape a hammer-on and a pull-off share: two
// noteheads under a slur. Only the letter over it differs, which is
// exactly how tab distinguishes them.
func drawSlurredPair(g *glyphCanvas, bg color.RGBA) {
	g.noteHead(0.24, 0.76, true, bg)
	g.noteHead(0.76, 0.76, true, bg)
	g.moveTo(0.24, 0.62)
	g.quadTo(0.50, 0.40, 0.76, 0.62)
	g.stroke(0.05)
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
	g.stroke(0.085)
	g.moveTo(0.64, 0.74)
	g.quadTo(0.38, 0.70, 0.46, 0.90)
	g.stroke(0.06)
}

// drawPlus draws a plus sign centred at (x, y) with the given half-width.
func drawPlus(g *glyphCanvas, x, y, r float64) {
	g.line(x-r, y, x+r, y, 0.075)
	g.line(x, y-r, x, y+r, 0.075)
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
	g.stroke(0.075)
	// The head, on the tail the arc starts from.
	g.moveTo(m(0.36), 0.30)
	g.lineTo(m(0.17), 0.47)
	g.lineTo(m(0.38), 0.60)
	g.stroke(0.075)
}

// drawDigit3 draws the 3 of a triplet bracket: two bowls, at a size the
// text renderer cannot reach without dropping a different typeface into
// the middle of a symbol.
func drawDigit3(g *glyphCanvas, x, y, h float64) {
	top, bot := y-h/2, y+h/2
	g.moveTo(x-h*0.32, top)
	g.quadTo(x+h*0.42, top-h*0.06, x, y)
	g.quadTo(x+h*0.48, y+h*0.08, x-h*0.32, bot)
	g.stroke(0.055)
}

// The three letters the symbols need are drawn as strokes rather than
// set as text.
//
// They have to obey the unit box like everything else here: the text
// renderer anchors at the top of a LINE BOX, which is taller than the ink
// and taller than the glyph box these are drawn in, so a letter placed
// that way lands wherever the face's ascent happens to put it and does
// not scale with the symbol around it. The H of a hammer-on was being
// drawn straight through the slur and into both noteheads because of it.
// Three strokes are cheaper than the metrics arithmetic that would fix
// the alternative, and they match the weight of the marks beside them.

// drawLetterH draws an H centred at x with its cap height h, the ink
// vertically centred on y.
func drawLetterH(g *glyphCanvas, x, y, h float64) {
	const wide = 0.30 // width as a fraction of the cap height, doubled
	half := h / 2
	left, right := x-h*wide, x+h*wide
	g.line(left, y-half, left, y+half, 0.055)
	g.line(right, y-half, right, y+half, 0.055)
	g.line(left, y, right, y, 0.055)
}

// drawLetterP draws a P the same way: a stem with a bowl on its top half.
func drawLetterP(g *glyphCanvas, x, y, h float64) {
	half := h / 2
	stem := x - h*0.30
	g.line(stem, y-half, stem, y+half, 0.055)
	g.moveTo(stem, y-half)
	g.quadTo(stem+h*0.80, y-half*0.72, stem, y)
	g.stroke(0.055)
}

// drawQuestionMark draws the help symbol: the hook and its dot.
func drawQuestionMark(g *glyphCanvas, x, y, h float64) {
	top := y - h/2
	g.moveTo(x-h*0.26, top+h*0.16)
	g.quadTo(x+h*0.36, top-h*0.14, x+h*0.20, top+h*0.34)
	g.quadTo(x+h*0.06, top+h*0.50, x, top+h*0.66)
	g.stroke(0.075)
	g.dot(x, y+h*0.42, 0.055)
}
