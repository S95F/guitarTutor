package ui

// The typefaces. Three real faces replace the 7x13 bitmap the UI grew up
// with: Go Regular for body text, Go Medium for headings and anything
// scaled up, and Go Mono where digits and fret numbers need to align.
// They ship as Go source inside golang.org/x/image — already a
// dependency — so the single-binary, zero-assets rule holds: nothing is
// downloaded, nothing sits beside the .exe.
//
// The change that ripples furthest is not the look but the measurement:
// a proportional face has no "glyph width", so every piece of layout
// arithmetic that multiplied a character count now asks the shaper for
// the real advance (see theme.go). Faces are cached per (source, size) —
// text/v2 builds a glyph atlas per size, so the draw path must hand it
// the same Face value every frame, not a fresh struct.

import (
	"bytes"
	"log"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"

	"image/color"
)

// The type scale. Body carries almost everything; small is for captions
// and key hints under chips; headings are body sizes multiplied by the
// scale the caller passes to drawTextScaled.
const (
	fontBody  = 14.0
	fontSmall = 12.0
	// uiTextH is the vertical room one body line occupies for layout
	// purposes (the face's own line height is a shade under this).
	uiTextH = 18.0
)

var (
	srcBody   = mustFaceSource(goregular.TTF)
	srcMedium = mustFaceSource(gomedium.TTF)
	srcMono   = mustFaceSource(gomono.TTF)

	// faceCache hands back one Face value per (source, size) so the glyph
	// atlas behind it is reused. Only the game loop (and single-threaded
	// tests) touch it, so it needs no lock.
	faceCache = map[faceKey]*text.GoTextFace{}
)

type faceKey struct {
	src  *text.GoTextFaceSource
	size float64
}

// mustFaceSource parses an embedded font. The bytes are compiled into
// the binary, so a parse failure is a build defect, not a runtime
// condition anyone can recover from.
func mustFaceSource(ttf []byte) *text.GoTextFaceSource {
	s, err := text.NewGoTextFaceSource(bytes.NewReader(ttf))
	if err != nil {
		log.Fatalf("ui: embedded font failed to parse: %v", err)
	}
	return s
}

func faceOf(src *text.GoTextFaceSource, size float64) *text.GoTextFace {
	k := faceKey{src, size}
	if f, ok := faceCache[k]; ok {
		return f
	}
	f := &text.GoTextFace{Source: src, Size: size}
	faceCache[k] = f
	return f
}

// drawText draws body text with its top-left at (x, y) — the same anchor
// the whole layout was built around.
func drawText(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcBody, fontBody), x, y, col)
}

// drawTextSmall draws caption-size body text.
func drawTextSmall(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcSmallSource(), fontSmall), x, y, col)
}

// srcSmallSource keeps small text in the regular weight; a hook in case
// captions ever want their own face.
func srcSmallSource() *text.GoTextFaceSource { return srcBody }

// drawTextMono draws mono text — fret numbers, clock times, anything
// whose digits must line up.
func drawTextMono(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcMono, fontBody), x, y, col)
}

// drawTextScaled draws heading text at scale times the body size, in the
// medium weight: everything the old bitmap UI scaled up was a heading of
// some kind, and a true face at the target size replaces the blocky
// nearest-neighbour magnification.
func drawTextScaled(dst *ebiten.Image, s string, x, y, scale float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcMedium, fontBody*scale), x, y, col)
}

func drawFace(dst *ebiten.Image, s string, face *text.GoTextFace, x, y float64, col color.RGBA) {
	op := &text.DrawOptions{}
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(col)
	text.Draw(dst, s, face, op)
}

// ---- measurement -----------------------------------------------------------

// textW is the pixel width of body text — a real shaped advance, not a
// character count. Everything that centres, right-aligns, truncates or
// wraps goes through these, so what is measured is always what is drawn.
func textW(s string) float64 { return text.Advance(s, faceOf(srcBody, fontBody)) }

// textWScaled is the pixel width of heading text at scale — the same
// face drawTextScaled draws with, which is what keeps centred headings
// actually centred.
func textWScaled(s string, scale float64) float64 {
	return text.Advance(s, faceOf(srcMedium, fontBody*scale))
}

// textWMono is the pixel width of mono text.
func textWMono(s string) float64 { return text.Advance(s, faceOf(srcMono, fontBody)) }

// textWSmall is the pixel width of caption-size text.
func textWSmall(s string) float64 { return text.Advance(s, faceOf(srcSmallSource(), fontSmall)) }

// fitToWidth shortens s until face renders it within maxPx, keeping the
// front and appending an ellipsis. Rune-safe by construction.
func fitToWidth(s string, maxPx float64, face *text.GoTextFace, keepTail bool) string {
	if maxPx <= 0 {
		return ""
	}
	if text.Advance(s, face) <= maxPx {
		return s
	}
	const ell = "…"
	r := []rune(s)
	// Linear from the full string down: names on this UI are short, and
	// the loop runs only when something actually overflows.
	for n := len(r) - 1; n > 0; n-- {
		var candidate string
		if keepTail {
			candidate = ell + string(r[len(r)-n:])
		} else {
			candidate = string(r[:n]) + ell
		}
		if text.Advance(candidate, face) <= maxPx {
			return candidate
		}
	}
	// Not even one rune plus the marker fits. The marker alone is only
	// honest if it fits; past that, nothing legible does.
	if text.Advance(ell, face) <= maxPx {
		return ell
	}
	return ""
}

// truncateW shortens body text to maxPx keeping the front — the right
// choice for names.
func truncateW(s string, maxPx float64) string {
	return fitToWidth(s, maxPx, faceOf(srcBody, fontBody), false)
}

// truncateWScaled is truncateW for heading text drawn at scale.
func truncateWScaled(s string, maxPx, scale float64) string {
	return fitToWidth(s, maxPx, faceOf(srcMedium, fontBody*scale), false)
}

// ellipsizeW shortens body text to maxPx keeping the tail — the right
// choice for paths, where the last folders identify it.
func ellipsizeW(s string, maxPx float64) string {
	return fitToWidth(s, maxPx, faceOf(srcBody, fontBody), true)
}

// ellipsizeWSmall is ellipsizeW measured with the caption face — for
// text drawTextSmall draws, which would otherwise be over-truncated by
// the wider body measurement.
func ellipsizeWSmall(s string, maxPx float64) string {
	return fitToWidth(s, maxPx, faceOf(srcSmallSource(), fontSmall), true)
}

// wrapTextW breaks s into lines of at most maxPx pixels, on spaces,
// measured with the body face. A single word wider than the line stands
// alone rather than being cut.
func wrapTextW(s string, maxPx float64) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	face := faceOf(srcBody, fontBody)
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if text.Advance(line+" "+w, face) > maxPx {
			out = append(out, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(out, line)
}
