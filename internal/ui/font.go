package ui

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

const (
	fontBody  = 14.0
	fontSmall = 12.0

	uiTextH = 18.0
)

var (
	srcBody   = mustFaceSource(goregular.TTF)
	srcMedium = mustFaceSource(gomedium.TTF)
	srcMono   = mustFaceSource(gomono.TTF)

	faceCache = map[faceKey]*text.GoTextFace{}
)

type faceKey struct {
	src  *text.GoTextFaceSource
	size float64
}

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

func drawText(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcBody, fontBody), x, y, col)
}

func drawTextSmall(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcSmallSource(), fontSmall), x, y, col)
}

func srcSmallSource() *text.GoTextFaceSource { return srcBody }

func drawTextMono(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcMono, fontBody), x, y, col)
}

func drawTextScaled(dst *ebiten.Image, s string, x, y, scale float64, col color.RGBA) {
	drawFace(dst, s, faceOf(srcMedium, fontBody*scale), x, y, col)
}

func drawFace(dst *ebiten.Image, s string, face *text.GoTextFace, x, y float64, col color.RGBA) {
	op := &text.DrawOptions{}
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(col)
	text.Draw(dst, s, face, op)
}

func textW(s string) float64 { return text.Advance(s, faceOf(srcBody, fontBody)) }

func textWScaled(s string, scale float64) float64 {
	return text.Advance(s, faceOf(srcMedium, fontBody*scale))
}

func textWMono(s string) float64 { return text.Advance(s, faceOf(srcMono, fontBody)) }

func textWSmall(s string) float64 { return text.Advance(s, faceOf(srcSmallSource(), fontSmall)) }

func fitToWidth(s string, maxPx float64, face *text.GoTextFace, keepTail bool) string {
	if maxPx <= 0 {
		return ""
	}
	if text.Advance(s, face) <= maxPx {
		return s
	}
	const ell = "…"
	r := []rune(s)

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

	if text.Advance(ell, face) <= maxPx {
		return ell
	}
	return ""
}

func truncateW(s string, maxPx float64) string {
	return fitToWidth(s, maxPx, faceOf(srcBody, fontBody), false)
}

func truncateWScaled(s string, maxPx, scale float64) string {
	return fitToWidth(s, maxPx, faceOf(srcMedium, fontBody*scale), false)
}

func ellipsizeW(s string, maxPx float64) string {
	return fitToWidth(s, maxPx, faceOf(srcBody, fontBody), true)
}

func ellipsizeWSmall(s string, maxPx float64) string {
	return fitToWidth(s, maxPx, faceOf(srcSmallSource(), fontSmall), true)
}

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
