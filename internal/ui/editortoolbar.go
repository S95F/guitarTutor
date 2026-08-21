package ui

import (
	"fmt"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/musicTutor/internal/score"
)

type edButton struct {
	id    string
	glyph glyphID

	name string
	key  string

	label    string
	on       bool
	disabled bool

	why string
	act func()
}

type edGroup struct {
	caption string
	buttons []edButton
}

func (e *Editor) noteValueButtons() []edButton {
	base, dot, trip := baseOf(e.doc.Duration())
	values := []struct {
		ticks int64
		glyph glyphID
		name  string
	}{
		{score.Whole, glyphNoteWhole, "Whole note"},
		{score.Half, glyphNoteHalf, "Half note"},
		{score.Quarter, glyphNoteQuarter, "Quarter note"},
		{score.Eighth, glyphNoteEighth, "Eighth note"},
		{score.Sixteenth, glyphNoteSixteenth, "Sixteenth note"},
		{score.ThirtySec, glyphNoteThirtySecond, "Thirty-second note"},
	}
	out := make([]edButton, 0, len(values)+2)
	for _, v := range values {
		ticks := applyMod(v.ticks, dot, trip)
		out = append(out, edButton{
			id:    fmt.Sprintf("value%d", v.ticks),
			glyph: v.glyph,
			name:  v.name,

			key: "[  ]",
			on:  v.ticks == base,
			act: func() { e.setDuration(ticks) },
		})
	}
	return append(out,
		edButton{id: "dot", glyph: glyphDotted, name: "Dotted — half as long again", key: ".", on: dot,
			act: func() { e.modifyDuration(dotted) }},
		edButton{id: "triplet", glyph: glyphTriplet, name: "Triplet — three in the time of two", key: "T", on: trip,
			act: func() { e.modifyDuration(triplet) }},
	)
}

func (e *Editor) noteButtons() []edButton {
	tech := e.cursorTech()
	note, hasNote := e.doc.NoteAt(e.doc.Cursor().Str)

	const needNote = "type a fret on this string first"
	out := []edButton{{
		id: "tie", glyph: glyphTie, name: "Tie — hold, do not strike again", key: "`",
		on: hasNote && note.Tied, disabled: !hasNote, why: needNote,
		act: func() { e.report(e.doc.ToggleTie()) },
	}}
	for _, t := range []struct {
		id, name, key string
		glyph         glyphID
		bit           score.Technique
	}{
		{"hammer", "Hammer-on", "h", glyphHammer, score.TechHammer},
		{"pull", "Pull-off", "p", glyphPull, score.TechPull},
		{"slide", "Slide", "s", glyphSlide, score.TechSlide},
		{"bend", "Bend", "b", glyphBend, score.TechBend},
		{"vibrato", "Vibrato", "v", glyphVibrato, score.TechVibrato},
		{"dead", "Dead note — muted, no pitch", "x", glyphDead, score.TechDead},
	} {
		bit := t.bit
		out = append(out, edButton{
			id: t.id, glyph: t.glyph, name: t.name, key: t.key,
			on: tech&bit != 0, disabled: !hasNote, why: needNote,
			act: func() { e.report(e.doc.ToggleTech(bit)) },
		})
	}
	return out
}

func (e *Editor) beatButtons() []edButton {
	return []edButton{
		{id: "rest", glyph: glyphRest, name: "Rest — silence for this beat", key: "R",
			act: func() { e.report(e.doc.ClearBeat()) }},
		{id: "addbeat", glyph: glyphAddBeat, name: "Insert a beat after this one", key: "enter",
			act: func() { e.report(e.doc.InsertBeat()) }},
		{id: "delbeat", glyph: glyphDeleteBeat, name: "Remove this beat", key: "shift+del",
			act: func() { e.report(e.doc.DeleteBeat()) }},
		{id: "addbar", glyph: glyphAddBar, name: "Add a bar after this one", key: "N",
			act: e.addBarAfterCursor},
	}
}

func (e *Editor) historyButtons() []edButton {
	return []edButton{
		{id: "undo", glyph: glyphUndo, name: "Undo", key: "ctrl+Z",
			disabled: !e.doc.CanUndo(), why: "nothing to undo yet", act: e.undo},
		{id: "redo", glyph: glyphRedo, name: "Redo", key: "ctrl+Y",
			disabled: !e.doc.CanRedo(), why: "nothing to redo", act: e.redo},
	}
}

func (e *Editor) toolbarGroups() []edGroup {
	marks := e.noteButtons()
	if e.doc.Track().Wind != nil {
		marks = e.windNoteButtons()
	}
	groups := []edGroup{
		{caption: "NOTE LENGTH", buttons: e.noteValueButtons()},
		{caption: "ON THIS NOTE", buttons: marks},
		{caption: "THIS BAR", buttons: e.beatButtons()},
		{caption: "HISTORY", buttons: e.historyButtons()},
	}
	if e.text == nil {
		return groups
	}

	for gi := range groups {
		for bi := range groups[gi].buttons {
			groups[gi].buttons[bi].disabled = true
			groups[gi].buttons[bi].why = edTextViewWhy
		}
	}
	return groups
}

const edTextViewWhy = "go back to the notation first (F2)"

func (e *Editor) pieceButtons() []edButton {
	var out []edButton
	for i, tr := range e.doc.Score().Tracks {
		name := tr.Name
		if name == "" {
			name = fmt.Sprintf("Track %d", i+1)
		}
		tip := "Edit this track"
		if tr.Role == score.RoleBacking {
			tip = "Edit this track (accompaniment, not the part you practise)"

			name += " (backing)"
		}
		idx := i
		out = append(out, edButton{
			id: fmt.Sprintf("track%d", i), glyph: glyphTrack, name: tip, key: "tab",
			label: truncateW(name, 150), on: i == e.doc.TrackIndex(),
			act: func() { e.doc.SelectTrack(idx) },
		})
	}
	out = append(out,
		edButton{id: "addtrack", glyph: glyphAddTrack, name: "Add another track", key: "shift+A",
			act: func() { e.openInstrumentPicker(pickAddTrack) }})
	if w := e.doc.Track().Wind; w != nil {

		out = append(out,
			edButton{id: "instrument", glyph: glyphWind, name: "This track's instrument", label: w.Name,
				disabled: true, why: "chosen when the track is made — add a track for another instrument"})
	} else {
		out = append(out,
			edButton{id: "tuning", glyph: glyphTuning, name: "Tuning for this track", key: "shift+U",
				label: e.tuningName(), act: e.cycleTuning},

			edButton{id: "capo", glyph: glyphCapo, name: "Capo for this track",
				label: e.capoLabel(), act: func() { e.openEntry(edEntryCapo) }})
	}
	out = append(out,
		edButton{id: "meter", glyph: glyphNone, name: "Time signature from this bar on", key: "shift+M",
			label: fmt.Sprintf("%d/%d", e.doc.Bar().Num, e.doc.Bar().Den),
			act:   func() { e.openEntry(edEntryMeter) }},
		edButton{id: "tempo", glyph: glyphTempo, name: "Tempo from this bar on", key: "shift+B",
			label: fmt.Sprintf("%.0f", e.doc.TempoAtCursor()),
			act:   func() { e.openEntry(edEntryTempo) }},
		edButton{id: "title", glyph: glyphTitle, name: "Name the piece", key: "shift+T",
			label: edPieceTitle(e), act: func() { e.openEntry(edEntryTitle) }},
	)
	if e.text != nil {
		for i := range out {
			out[i].disabled = true
			out[i].why = edTextViewWhy
		}
	}
	return out
}

func (e *Editor) capoLabel() string {
	if c := e.doc.Track().Capo; c > 0 {
		return fmt.Sprintf("capo %d", c)
	}
	return "capo"
}

func (e *Editor) fileButtons() []edButton {
	view := edButton{id: "view", glyph: glyphTextView, name: "Show the piece as the text file it saves to", key: "F2",
		act: e.toggleText}
	save := func() { e.save() }
	practiceKey, practice := "shift+P", e.saveAndPractice
	if e.text != nil {
		view = edButton{id: "view", glyph: glyphGridView, name: "Back to the notation", key: "F2",
			on: true, act: e.toggleText}

		save = func() { e.applyTextThen(func() { e.save() }) }
		practiceKey, practice = "", func() { e.applyTextThen(e.saveAndPractice) }
	}
	return []edButton{
		{id: "save", glyph: glyphSave, name: e.saveTip(), key: "ctrl+S", on: e.doc.Dirty(), act: save},
		view,
		{id: "practice", glyph: glyphPlay, name: edPracticeWhat, key: practiceKey,
			disabled: e.practice == nil, why: "this build cannot play a piece from here",
			act: practice},
		{id: "help", glyph: glyphHelp, name: "Every key this screen answers to", key: "F1",
			act: func() { e.helpOpen = true }},
	}
}

func edPieceTitle(e *Editor) string {
	name := strings.TrimSpace(e.doc.Score().Title)
	if name == "" {
		name = "Untitled"
	}
	return truncateW(name, 130)
}

func (e *Editor) saveTip() string {
	if e.doc.Dirty() {
		return "Save — there are unsaved changes"
	}
	return "Save"
}

type edToolbarLayout struct {
	groups  []edGroup
	rects   [][]rect
	caps    []rect
	piece   []edButton
	pieceAt []rect
	files   []edButton
	filesAt []rect
}

func (e *Editor) layoutToolbar() edToolbarLayout {
	var l edToolbarLayout
	l.groups = e.toolbarGroups()
	x := uiPadX
	for _, g := range l.groups {
		l.caps = append(l.caps, rect{x, edCaptionY, 0, iconCapH})
		var rects []rect
		for range g.buttons {
			rects = append(rects, rect{x, edToolbarY, iconBtnSize, iconBtnSize})
			x += iconBtnSize + iconBtnGap
		}
		x += iconGrpGap - iconBtnGap
		l.rects = append(l.rects, rects)
	}

	l.files = e.fileButtons()
	total := float64(len(l.files)) * (iconBtnSize + iconBtnGap)
	fx := screenW - uiPadX - total + iconBtnGap
	for range l.files {
		l.filesAt = append(l.filesAt, rect{fx, edTrackY, iconBtnSize, iconBtnSize})
		fx += iconBtnSize + iconBtnGap
	}

	limit := screenW - uiPadX
	if len(l.filesAt) > 0 {
		limit = l.filesAt[0].x - iconGrpGap
	}

	tracks, fixed := e.pieceButtons(), []edButton(nil)
	for len(tracks) > 0 {
		last := tracks[len(tracks)-1]
		if strings.HasPrefix(last.id, "track") {
			break
		}
		fixed = append([]edButton{last}, fixed...)
		tracks = tracks[:len(tracks)-1]
	}
	fixedW := 0.0
	for _, b := range fixed {
		fixedW += edPieceWidth(b) + iconBtnGap
	}

	px := uiPadX
	for _, b := range tracks {
		w := edPieceWidth(b)
		if px+w+fixedW > limit {
			break
		}
		l.piece = append(l.piece, b)
		l.pieceAt = append(l.pieceAt, rect{px, edTrackY, w, iconBtnSize})
		px += w + iconBtnGap
	}
	for _, b := range fixed {
		w := edPieceWidth(b)
		l.piece = append(l.piece, b)
		l.pieceAt = append(l.pieceAt, rect{px, edTrackY, w, iconBtnSize})
		px += w + iconBtnGap
	}
	return l
}

func edPieceWidth(b edButton) float64 {
	if b.label == "" {
		return iconBtnSize
	}
	w := textW(b.label) + 14
	if b.glyph != glyphNone {
		w += glyphBox + 4
	}
	return w + 12
}

func (e *Editor) drawChrome(screen *ebiten.Image, l edToolbarLayout) {
	dt := uiFrameSeconds()
	live := !e.modalUp()

	paint := func(prefix string, b edButton, r rect, icon bool) {
		over := live && e.ptr.over(r)
		av := e.anim.step(prefix+b.id, over && !b.disabled, e.ptr.down, dt)
		if icon {
			drawIconGlyphButton(screen, r, b.glyph, b.on, b.disabled, av)
		} else {
			drawValueButton(screen, r, b, av)
		}
		e.tip.offer(prefix+b.id, tipTextFor(b), b.key, r, over, dt)
	}
	for gi, g := range l.groups {
		drawGroupCaption(screen, g.caption, l.caps[gi].x, l.caps[gi].y)
		for bi, b := range g.buttons {
			paint("tb:", b, l.rects[gi][bi], true)
		}
	}
	for i, b := range l.piece {
		paint("pc:", b, l.pieceAt[i], false)
	}
	for i, b := range l.files {
		paint("fl:", b, l.filesAt[i], true)
	}
}

func tipTextFor(b edButton) string {
	if b.disabled && b.why != "" {
		return b.name + " — " + b.why
	}
	return b.name
}

func drawValueButton(dst *ebiten.Image, r rect, b edButton, av animValues) {
	if b.label == "" {
		drawIconGlyphButton(dst, r, b.glyph, b.on, b.disabled, av)
		return
	}
	fill, edge, ink := colPanel, colPanelEdge, colHUD
	switch {
	case b.disabled:

		fill, edge, ink = colBG, colBarline, colDisabled
	case b.on:
		fill, edge, ink = colOn, colOnEdge, colNote
		fill = lerpCol(fill, colOnHover, av.hover)
	default:
		fill = lerpCol(fill, colHover, av.hover)
		edge = lerpCol(edge, colDim, av.hover)
		ink = lerpCol(ink, colNote, av.hover)
	}
	if !b.disabled {
		r = av.animate(r)
	}
	drawPanel(dst, r, fill, edge)
	x := r.x + 9
	if b.glyph != glyphNone {
		drawGlyph(dst, b.glyph, rect{x, r.y + (r.h-glyphBox)/2, glyphBox, glyphBox}, ink)
		x += glyphBox + 4
	}
	drawText(dst, b.label, x, r.y+(r.h-uiTextH)/2+1, ink)
	if !b.disabled {
		drawFlash(dst, r, av)
	}
}

func (e *Editor) hotspots() []hotspot {
	var out []hotspot
	l := e.layoutToolbar()
	add := func(b edButton, r rect) {
		if b.disabled || b.act == nil {
			return
		}
		act := b.act
		out = append(out, hotspot{r: r, on: act})
	}
	for gi, g := range l.groups {
		for bi, b := range g.buttons {
			add(b, l.rects[gi][bi])
		}
	}
	for i, b := range l.piece {
		add(b, l.pieceAt[i])
	}
	for i, b := range l.files {
		add(b, l.filesAt[i])
	}
	return out
}
