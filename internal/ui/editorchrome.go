package ui

// The editor's chrome: the toolbar, the track strip, and the one-line
// entry prompt.
//
// Every chip carries the key that does the same thing. The editor has more
// bindings than any other screen here, and a toolbar that replaced the
// keyboard rather than teaching it would leave a user clicking forever at
// something they will do a thousand times an evening.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/guitarTutor/internal/score"
)

// An edChip is one toolbar control: how it looks, and what it does.
type edChip struct {
	state chipState
	act   func()
}

// toolbarChips is the first row: the note value being written, and the
// techniques and beat operations that apply to what is under the cursor.
func (e *Editor) toolbarChips() []edChip {
	_, dot, trip := baseOf(e.doc.Duration())
	tech := e.cursorTech()
	note, hasNote := e.doc.NoteAt(e.doc.Cursor().Str)

	chips := []edChip{
		{chipState{label: "−", key: "["}, func() { e.stepDuration(-1) }},
		{chipState{label: durationName(e.doc.Duration()), key: "value"}, nil},
		{chipState{label: "+", key: "]"}, func() { e.stepDuration(1) }},
		{chipState{label: "dot", key: ".", on: dot}, func() { e.modifyDuration(dotted) }},
		{chipState{label: "trip", key: "T", on: trip}, func() { e.modifyDuration(triplet) }},
	}
	for _, t := range []struct {
		label string
		key   string
		bit   score.Technique
	}{
		{"tie", "`", 0}, // 0 marks the tie, which is not a technique bit
		{"h", "h", score.TechHammer},
		{"p", "p", score.TechPull},
		{"s", "s", score.TechSlide},
		{"b", "b", score.TechBend},
		{"v", "v", score.TechVibrato},
		{"x", "x", score.TechDead},
	} {
		on := t.bit != 0 && tech&t.bit != 0
		if t.bit == 0 {
			on = hasNote && note.Tied
		}
		bit := t.bit
		act := func() { e.report(e.doc.ToggleTech(bit)) }
		if bit == 0 {
			act = func() { e.report(e.doc.ToggleTie()) }
		}
		chips = append(chips, edChip{chipState{label: t.label, key: t.key, on: on, disabled: !hasNote}, act})
	}
	chips = append(chips,
		edChip{chipState{label: "rest", key: "R"}, func() { e.report(e.doc.ClearBeat()) }},
		edChip{chipState{label: "+beat", key: "enter"}, func() { e.report(e.doc.InsertBeat()) }},
		edChip{chipState{label: "+bar", key: "N"}, e.addBarAfterCursor},
	)
	return greyIfText(e, chips)
}

// greyIfText disables the controls that edit the NOTATION while the raw
// text is showing. They would still work — they act on the document behind
// the text — but the text is applied over that document when the view
// closes, so the edit would be silently discarded a moment later. A
// control that quietly does nothing is worse than one that is visibly
// unavailable.
func greyIfText(e *Editor, chips []edChip) []edChip {
	if e.text == nil {
		return chips
	}
	for i := range chips {
		chips[i].state.disabled = true
	}
	return chips
}

// cursorTech is the technique bitmask on the note under the cursor.
func (e *Editor) cursorTech() score.Technique {
	if n, ok := e.doc.NoteAt(e.doc.Cursor().Str); ok {
		return n.Tech
	}
	return 0
}

// trackChips is the second row: the tracks, and the piece-wide actions.
func (e *Editor) trackChips() []edChip {
	var chips []edChip
	for i, tr := range e.doc.Score().Tracks {
		name := tr.Name
		if name == "" {
			name = fmt.Sprintf("track %d", i+1)
		}
		if tr.Role == score.RoleBacking {
			name += " (backing)"
		}
		idx := i
		chips = append(chips, edChip{
			chipState{label: truncateW(name, 160), key: "tab", on: i == e.doc.TrackIndex()},
			func() { e.doc.SelectTrack(idx) },
		})
	}
	chips = append(chips,
		edChip{chipState{label: "+ track", key: ""}, func() {
			e.report(e.doc.AddTrack(fmt.Sprintf("Track %d", len(e.doc.Score().Tracks)+1)))
		}},
		edChip{chipState{label: e.tuningName(), key: "shift+U"}, e.cycleTuning},
		edChip{chipState{label: fmt.Sprintf("%d/%d", e.doc.Bar().Num, e.doc.Bar().Den), key: "shift+M"},
			func() { e.openEntry(edEntryMeter) }},
		edChip{chipState{label: fmt.Sprintf("%.0f BPM", e.doc.TempoAtCursor()), key: "shift+B"},
			func() { e.openEntry(edEntryTempo) }},
	)
	return greyIfText(e, chips)
}

// actionChips is the right-hand group of the second row: what to do with
// the piece as a whole. They are laid out from the right edge so that
// adding a track never pushes SAVE off the screen.
func (e *Editor) actionChips() []edChip {
	label := "text"
	if e.text != nil {
		label = "grid"
	}
	return []edChip{
		{chipState{label: "save", key: "ctrl+S", on: e.doc.Dirty()}, func() { e.save() }},
		{chipState{label: label, key: "F2", on: e.text != nil}, e.toggleText},
		{chipState{label: "practice", key: "shift+P", disabled: e.practice == nil}, e.saveAndPractice},
		{chipState{label: "?", key: "F1"}, func() { e.helpOpen = true }},
	}
}

// layoutChips places a row of chips left to right from x, and returns the
// rect of each.
func layoutChips(chips []edChip, x, y float64) []rect {
	out := make([]rect, len(chips))
	for i, c := range chips {
		w := chipW(c.state)
		out[i] = rect{x, y, w, chipH}
		x += w + chipGap
	}
	return out
}

// layoutChipsRight places a row of chips ending at the right margin.
func layoutChipsRight(chips []edChip, y float64) []rect {
	total := 0.0
	for _, c := range chips {
		total += chipW(c.state) + chipGap
	}
	return layoutChips(chips, screenW-uiPadX-total+chipGap, y)
}

func (e *Editor) drawToolbar(screen *ebiten.Image) {
	chips := e.toolbarChips()
	dt := uiFrameSeconds()
	for i, r := range layoutChips(chips, uiPadX, edToolbarY) {
		av := e.anim.step("tool:"+chips[i].state.label, e.ptr.over(r), e.ptr.down, dt)
		drawChip(screen, r, chips[i].state, av)
	}
}

func (e *Editor) drawTrackStrip(screen *ebiten.Image) {
	actions := e.actionChips()
	actionRects := layoutChipsRight(actions, edTrackY)
	limit := screenW - uiPadX
	if len(actionRects) > 0 {
		limit = actionRects[0].x - chipGap
	}
	tracks := e.trackChips()
	dt := uiFrameSeconds()
	for i, r := range layoutChips(tracks, uiPadX, edTrackY) {
		if r.x+r.w > limit {
			break // no room; the keyboard still reaches them
		}
		av := e.anim.step("track:"+tracks[i].state.label, e.ptr.over(r), e.ptr.down, dt)
		drawChip(screen, r, tracks[i].state, av)
	}
	for i, r := range actionRects {
		av := e.anim.step("act:"+actions[i].state.label, e.ptr.over(r), e.ptr.down, dt)
		drawChip(screen, r, actions[i].state, av)
	}
}

// hotspots are every clickable control on the editor's chrome, in the
// order the drawing puts them.
func (e *Editor) hotspots() []hotspot {
	var out []hotspot
	add := func(chips []edChip, rects []rect, limit float64) {
		for i, r := range rects {
			if r.x+r.w > limit || chips[i].act == nil || chips[i].state.disabled {
				continue
			}
			act := chips[i].act
			out = append(out, hotspot{r: r, on: act})
		}
	}
	tool := e.toolbarChips()
	add(tool, layoutChips(tool, uiPadX, edToolbarY), screenW)

	actions := e.actionChips()
	actionRects := layoutChipsRight(actions, edTrackY)
	limit := screenW - uiPadX
	if len(actionRects) > 0 {
		limit = actionRects[0].x - chipGap
	}
	tracks := e.trackChips()
	add(tracks, layoutChips(tracks, uiPadX, edTrackY), limit)
	add(actions, actionRects, screenW)
	return out
}

// --- the one-line entry ----------------------------------------------

// edEntryKind is what a prompt is asking for.
type edEntryKind int

const (
	edEntryTempo edEntryKind = iota
	edEntryMeter
	edEntryTitle
)

// An edEntry is the prompt open over the notation.
type edEntry struct {
	kind   edEntryKind
	prompt string
	hint   string
	buf    string
	max    int
	// allow reports whether a typed rune belongs in this entry, so a
	// numeric field cannot be filled with letters.
	allow func(r rune) bool
	apply func(*Editor, string) error
}

// openEntry starts a prompt, seeded with what is currently in force so
// that pressing enter straight away changes nothing.
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
			apply: func(e *Editor, s string) error { return e.doc.SetTitle(strings.TrimSpace(s)) },
		}
	}
}

// parseMeterText reads "n/d".
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

// updateEntry runs the prompt. It owns the keyboard while it is open, so
// typing a digit into a tempo cannot also write a fret.
func (e *Editor) updateEntry() {
	en := e.entry
	for _, r := range ebiten.AppendInputChars(nil) {
		if len([]rune(en.buf)) >= en.max || !en.allow(r) {
			continue
		}
		en.buf += string(r)
	}
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyBackspace):
		if r := []rune(en.buf); len(r) > 0 {
			en.buf = string(r[:len(r)-1])
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		e.entry = nil
	case inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter):
		e.commitEntry()
	}
	// A click outside the box cancels, matching the practice view's tempo
	// entry.
	if e.ptr.pressed && !e.ptr.over(edEntryRect()) {
		e.entry = nil
	}
}

// commitEntry applies the prompt, keeping it open when what was typed does
// not work — losing a half-typed value to a typo is its own small cruelty.
func (e *Editor) commitEntry() {
	en := e.entry
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
