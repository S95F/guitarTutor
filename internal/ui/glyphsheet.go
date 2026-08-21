package ui

import (
	"github.com/hajimehoshi/ebiten/v2"
)

type GlyphSheet struct{}

func NewGlyphSheet() *GlyphSheet { return &GlyphSheet{} }

func (*GlyphSheet) Update() error { return nil }

var glyphSpecimens = []struct {
	id   glyphID
	name string
}{
	{glyphNoteWhole, "whole"},
	{glyphNoteHalf, "half"},
	{glyphNoteQuarter, "quarter"},
	{glyphNoteEighth, "eighth"},
	{glyphNoteSixteenth, "sixteenth"},
	{glyphNoteThirtySecond, "thirty-second"},
	{glyphDotted, "dotted"},
	{glyphTriplet, "triplet"},
	{glyphRest, "rest"},
	{glyphRestWhole, "whole rest"},
	{glyphRestHalf, "half rest"},

	{glyphTie, "tie"},
	{glyphHammer, "hammer-on"},
	{glyphPull, "pull-off"},
	{glyphSlide, "slide"},
	{glyphBend, "bend"},
	{glyphVibrato, "vibrato"},
	{glyphDead, "dead note"},

	{glyphAddBeat, "add beat"},
	{glyphAddBar, "add bar"},
	{glyphDeleteBeat, "remove beat"},

	{glyphUndo, "undo"},
	{glyphRedo, "redo"},
	{glyphSave, "save"},
	{glyphPlay, "play"},
	{glyphTextView, "text view"},
	{glyphGridView, "notation view"},
	{glyphHelp, "help"},

	{glyphRecord, "record"},
	{glyphTuner, "tuner"},

	{glyphTrack, "track"},
	{glyphAddTrack, "add track"},
	{glyphTuning, "tuning"},
	{glyphCapo, "capo"},
	{glyphWind, "wind instrument"},
	{glyphSlur, "slur"},
	{glyphTempo, "tempo"},
	{glyphTitle, "title"},

	{glyphFolder, "open a file"},
	{glyphNewPiece, "write a new piece"},
	{glyphPencil, "edit"},
	{glyphSliders, "settings"},
}

func (s *GlyphSheet) Draw(dst *ebiten.Image) {
	dst.Fill(colBG)
	drawHeader(dst, "GLYPHS", "every drawn symbol, at the sizes it is used", colDim)

	const (
		cols  = 9
		cellW = 136.0
		cellH = 108.0
	)
	x0, y0 := uiPadX, uiBodyTop+14.0
	for i, sp := range glyphSpecimens {
		col, row := i%cols, i/cols
		x := x0 + float64(col)*cellW
		y := y0 + float64(row)*cellH
		drawPanel(dst, rect{x, y, cellW - 10, cellH - 12}, colPanel, colPanelEdge)

		drawGlyph(dst, sp.id, rect{x + 10, y + 10, 28, 28}, colNote)
		drawGlyph(dst, sp.id, rect{x + 46, y + 14, 20, 20}, colNote)
		drawGlyph(dst, sp.id, rect{x + 72, y + 17, 14, 14}, colDisabled)

		drawPanel(dst, rect{x + 94, y + 8, 32, 32}, colOn, colOnEdge)
		drawGlyph(dst, sp.id, rect{x + 100, y + 14, 20, 20}, colNote)

		drawTextSmall(dst, truncateW(sp.name, cellW-24), x+10, y+52, colHUD)
	}
	drawFooter(dst, "glyph specimen sheet — tools/uishot -screen glyphs")
}
