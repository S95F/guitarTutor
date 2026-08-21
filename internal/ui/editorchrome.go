package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func (e *Editor) cursorTech() score.Technique {
	if n, ok := e.doc.NoteAt(e.doc.Cursor().Str); ok {
		return n.Tech
	}
	return 0
}

type edEntryKind int

const (
	edEntryTempo edEntryKind = iota
	edEntryMeter
	edEntryTitle
	edEntryCapo
)

type edEntry struct {
	kind   edEntryKind
	prompt string
	hint   string
	buf    string
	max    int

	seeded bool

	allow func(r rune) bool
	apply func(*Editor, string) error
}

func (e *Editor) openEntry(kind edEntryKind) {
	switch kind {
	case edEntryTempo:
		e.entry = &edEntry{
			kind: kind, prompt: "tempo from this bar on", hint: "beats per minute, 1 to 1000",
			buf: strconv.Itoa(int(e.doc.TempoAtCursor() + 0.5)), max: 4,
			allow: func(r rune) bool { return r >= '0' && r <= '9' },
			apply: func(e *Editor, s string) error {
				bpm, err := strconv.ParseFloat(s, 64)
				if err != nil {
					return fmt.Errorf("%q is not a number of beats per minute", s)
				}
				return e.doc.SetTempo(bpm)
			},
		}
	case edEntryMeter:
		bar := e.doc.Bar()
		e.entry = &edEntry{
			kind: kind, prompt: "time signature from this bar on", hint: "as n/d, e.g. 3/4, 6/8, 7/8",
			buf: fmt.Sprintf("%d/%d", bar.Num, bar.Den), max: 6,
			allow: func(r rune) bool { return (r >= '0' && r <= '9') || r == '/' },
			apply: func(e *Editor, s string) error {
				num, den, ok := parseMeterText(s)
				if !ok {
					return fmt.Errorf("%q is not a time signature; write it as n/d", s)
				}
				return e.doc.SetMeter(num, den)
			},
		}
	case edEntryTitle:
		e.entry = &edEntry{
			kind: kind, prompt: "piece title", hint: "shown in the header and saved with the piece",
			buf: e.doc.Score().Title, max: 80,
			allow: func(r rune) bool { return r >= 32 && r != 127 },
			apply: func(e *Editor, s string) error {

				if s = strings.TrimSpace(s); s == e.doc.Score().Title {
					return nil
				}
				return e.doc.SetTitle(s)
			},
		}
	case edEntryCapo:
		e.entry = &edEntry{
			kind: kind, prompt: "capo for this track",
			hint: fmt.Sprintf("the fret it clamps, 0 to %d — 0 takes it off", textfmt.MaxFret),
			buf:  strconv.Itoa(e.doc.Track().Capo), max: 2,
			allow: func(r rune) bool { return r >= '0' && r <= '9' },
			apply: func(e *Editor, s string) error {
				fret, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("%q is not a fret number", s)
				}
				return e.doc.SetCapo(fret)
			},
		}
	}

	e.entry.seeded = kind != edEntryTitle
}

func (en *edEntry) feed(runes []rune) {
	for _, r := range runes {
		if !en.allow(r) {
			continue
		}
		if en.seeded {
			en.buf, en.seeded = "", false
		}
		if len([]rune(en.buf)) < en.max {
			en.buf += string(r)
		}
	}
}

func parseMeterText(s string) (num, den int, ok bool) {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return 0, 0, false
	}
	n, err1 := strconv.Atoi(strings.TrimSpace(s[:i]))
	d, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return n, d, true
}

func (e *Editor) updateEntry() {
	en := e.entry
	en.feed(ebiten.AppendInputChars(nil))
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyBackspace):
		if r := []rune(en.buf); len(r) > 0 {
			en.buf, en.seeded = string(r[:len(r)-1]), false
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		e.entry = nil
	case inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter):
		e.commitEntry()
	}

	if e.ptr.pressed && !e.ptr.over(edEntryRect()) {
		e.entry = nil
	}
}

func (e *Editor) commitEntry() {
	en := e.entry
	if en.seeded {

		e.entry = nil
		return
	}
	if strings.TrimSpace(en.buf) == "" && en.kind != edEntryTitle {
		e.entry = nil
		return
	}
	if err := en.apply(e, en.buf); err != nil {
		e.report(err)
		return
	}
	e.entry = nil
}

func edEntryRect() rect { return rect{screenW/2 - 230, screenH/2 - 60, 460, 120} }

func (e *Editor) drawEntry(screen *ebiten.Image) {
	vector.DrawFilledRect(screen, 0, 0, screenW, screenH, colHelpDim, false)
	r := edEntryRect()
	drawPanel(screen, r, colPanel, colSounding)
	drawText(screen, e.entry.prompt, r.x+16, r.y+12, colHUD)
	drawTextScaled(screen, e.entry.buf+"_", r.x+16, r.y+34, 1.8, colNote)
	drawTextSmall(screen, e.entry.hint, r.x+16, r.y+76, colDim)
	drawTextSmall(screen, "enter apply    esc cancel", r.x+16, r.y+94, colHint)
}
