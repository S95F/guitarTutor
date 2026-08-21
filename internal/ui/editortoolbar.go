package ui

// The editor's toolbar: what you press to write music.
//
// It used to be a flat row of fifteen small text chips reading
//
//	−  1/4  +  dot  trip  tie  h  p  s  b  v  x  rest  +beat  +bar
//
// and the letters in the middle of that are not abbreviations, they are
// the .gtab file format's technique characters. A guitarist who has never
// opened the format documentation has no way to know that `p` is a
// pull-off rather than "play", and no way to find out except by pressing
// it. That is the whole of what "you have to know the format to use the
// editor" means, and it was true.
//
// So the toolbar is symbols now. Every one of them is a mark a guitarist
// has already met — a filled notehead with a flag is an eighth note, an
// arc between two heads is a tie, a line between two heads is a slide, an
// arrow curving up is a bend, a cross is a muted string — and the ones
// that are letters in real notation (the H of a hammer-on, the P of a
// pull-off) are letters here for the same reason they are letters there.
// Resting on any of them names it and gives its key (tooltip.go).
//
// Three other things changed with it, and each is a decision rather than
// a tidy-up:
//
//   - The note value is PICKED, not stepped. Six buttons, one lit. The old
//     −/+ pair meant finding a value took up to five presses and reading a
//     fraction to know where you were, and "+" made the note shorter,
//     which is exactly backwards from what a plus should do.
//   - The controls are GROUPED under captions, because what a control acts
//     on — the note under the cursor, the beat, the piece — is the first
//     thing you need to know about it and was previously not shown at all.
//   - Undo and redo are on the toolbar. They were keyboard-only, which is
//     a strange thing to hide from somebody who is learning by pressing
//     buttons to see what they do.

import (
	"fmt"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/musicTutor/internal/score"
)

// An edButton is one control in the toolbar: a symbol, what it is called,
// and what it does.
type edButton struct {
	// id is stable across frames and independent of the label, so the
	// animation and the tooltip follow the control rather than its text.
	id    string
	glyph glyphID
	// name is what the tooltip calls it — the guitarist's word, never the
	// format's letter.
	name string
	key  string
	// label puts text beside the symbol, for the few controls that carry a
	// value (the tempo, the meter, a track's name).
	label    string
	on       bool
	disabled bool
	// why explains a control that is greyed. A disabled icon with no name
	// is the worst thing on a toolbar — it is precisely the control the
	// user does not understand — so the tooltip still answers, and says
	// what would make it available.
	why string
	act func()
}

// An edGroup is a cluster of related controls under one caption.
type edGroup struct {
	caption string
	buttons []edButton
}

// noteValueButtons is the note-length picker: the six values, each drawn
// as the note it writes, with the current one lit.
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
			// The bracket keys still step through them, so the tooltip
			// teaches the pair rather than a key each.
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

// noteButtons are the marks that go ON the note under the cursor. They
// are disabled when there is no note there, which is most of how a
// first-timer learns that a fret number comes first.
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

// beatButtons change what is in the bar rather than what is on a note.
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

// historyButtons are undo and redo, which were keyboard-only.
func (e *Editor) historyButtons() []edButton {
	return []edButton{
		{id: "undo", glyph: glyphUndo, name: "Undo", key: "ctrl+Z",
			disabled: !e.doc.CanUndo(), why: "nothing to undo yet", act: e.undo},
		{id: "redo", glyph: glyphRedo, name: "Redo", key: "ctrl+Y",
			disabled: !e.doc.CanRedo(), why: "nothing to redo", act: e.redo},
	}
}

// toolbarGroups is the whole first row, in the order the work happens:
// choose a length, type a note, mark it, then shape the bar.
func (e *Editor) toolbarGroups() []edGroup {
	groups := []edGroup{
		{caption: "NOTE LENGTH", buttons: e.noteValueButtons()},
		{caption: "ON THIS NOTE", buttons: e.noteButtons()},
		{caption: "THIS BAR", buttons: e.beatButtons()},
		{caption: "HISTORY", buttons: e.historyButtons()},
	}
	if e.text == nil {
		return groups
	}
	// The raw text is showing: these all edit the document underneath it,
	// and the text is applied over that document when the view closes, so
	// the edit would be discarded a moment later. The why has to change
	// with the reason — "type a fret first" is the grid's answer, and here
	// it would send the user hunting for a fret cell that is not on screen.
	for gi := range groups {
		for bi := range groups[gi].buttons {
			groups[gi].buttons[bi].disabled = true
			groups[gi].buttons[bi].why = edTextViewWhy
		}
	}
	return groups
}

// edTextViewWhy is what a greyed notation control answers while the raw
// text is showing: the way to use it is to leave the text view.
const edTextViewWhy = "go back to the notation first (F2)"

// --- the piece row -----------------------------------------------------

// pieceButtons is the second row's left half: which track is being
// edited, and the three settings that belong to the piece rather than to
// a note. These carry labels as well as symbols, because their VALUE is
// the information — a metronome icon says "tempo" and "112" says what it
// is.
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
			// The role stays on the ROW as well as in the tooltip: which
			// part is the one you play is a fact about the piece, and a
			// fact you have to hover to learn is a fact most people never
			// learn.
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
			act: func() {
				e.report(e.doc.AddTrack(fmt.Sprintf("Track %d", len(e.doc.Score().Tracks)+1)))
			}},
		edButton{id: "tuning", glyph: glyphTuning, name: "Tuning for this track", key: "shift+U",
			label: e.tuningName(), act: e.cycleTuning},
		// The capo sits beside the tuning it shifts. Without this chip a
		// piece with a capo rendered exactly like one without, and putting
		// one on meant knowing the text format's directive.
		edButton{id: "capo", glyph: glyphCapo, name: "Capo for this track",
			label: e.capoLabel(), act: func() { e.openEntry(edEntryCapo) }},
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

// capoLabel is the capo chip's text: the fret when one is on, and just the
// word when none is — the chip still has to say what it is for, and a
// symbol-only chip here would be the rebus the piece row exists to avoid.
func (e *Editor) capoLabel() string {
	if c := e.doc.Track().Capo; c > 0 {
		return fmt.Sprintf("capo %d", c)
	}
	return "capo"
}

// fileButtons is the second row's right half: what to do with the piece
// as a whole. They stay live in the text view, because saving and going
// back are exactly what that view needs.
func (e *Editor) fileButtons() []edButton {
	view := edButton{id: "view", glyph: glyphTextView, name: "Show the piece as the text file it saves to", key: "F2",
		act: e.toggleText}
	save := func() { e.save() }
	practiceKey, practice := "shift+P", e.saveAndPractice
	if e.text != nil {
		view = edButton{id: "view", glyph: glyphGridView, name: "Back to the notation", key: "F2",
			on: true, act: e.toggleText}
		// The text on screen is the piece now: saving or practising the
		// document UNDERNEATH it would act on a version the user is no
		// longer looking at, and say "saved" about edits that never reached
		// the file. Both go the way ctrl+S already does — apply the text,
		// return to the notation, then act. shift+P itself types a P here,
		// so the practice control shows no key while the text is up.
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

// edPieceTitle is the piece's name for the toolbar, which makes the row
// read as a summary of the piece as well as a set of things to press.
func edPieceTitle(e *Editor) string {
	name := strings.TrimSpace(e.doc.Score().Title)
	if name == "" {
		name = "Untitled"
	}
	return truncateW(name, 130)
}

// saveTip says whether there is anything to save, so the lit state of the
// save button has words behind it.
func (e *Editor) saveTip() string {
	if e.doc.Dirty() {
		return "Save — there are unsaved changes"
	}
	return "Save"
}

// --- layout ------------------------------------------------------------

// An edToolbarLayout is one frame's toolbar geometry: where every control
// and every caption goes. Drawing and hit testing both read it.
// The rects and the controls they belong to are built together and kept
// together. Drawing used to lay the toolbar out and then ask for the
// controls a second time; the two calls agreed, because nothing changes
// between them inside one frame — but "agree because nothing happens to
// change them" is a property somebody has to keep true, and every one of
// these slices is indexed in parallel with another. Building them once
// removes the question along with the work.
type edToolbarLayout struct {
	groups  []edGroup
	rects   [][]rect
	caps    []rect
	piece   []edButton
	pieceAt []rect
	files   []edButton
	filesAt []rect
}

// layoutToolbar places the icon row, the piece row and the file row.
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

	// The file row is laid out from the right so that adding a track can
	// never push Save off the screen.
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
	// The settings and the add-track control are placed FIRST, from the
	// right, and the track buttons fill what is left. Laid out left to
	// right the tracks came first and pushed the rest off the end — and
	// "add a track" has no keyboard binding, so a piece with enough tracks
	// in it could not gain another one at all.
	tracks, fixed := e.pieceButtons(), []edButton(nil)
	for len(tracks) > 0 && tracks[len(tracks)-1].id != "" {
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
			break // out of room; the tab key still reaches this track
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

// edPieceWidth is how wide a piece-row control needs to be for its symbol
// and its value.
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

// --- drawing -----------------------------------------------------------

// drawChrome paints both toolbar rows from one layout. tips are offered
// only when nothing modal is up: a name floating over a dimmed screen
// belongs to a control that cannot be pressed.
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

// tipTextFor is what a control's tooltip says: its name, and — when it is
// greyed — what would make it available.
func tipTextFor(b edButton) string {
	if b.disabled && b.why != "" {
		return b.name + " — " + b.why
	}
	return b.name
}

// drawValueButton paints a control that carries a value: its symbol on
// the left and the value beside it, so the row reads as a summary of the
// piece as well as a set of things to press.
func drawValueButton(dst *ebiten.Image, r rect, b edButton, av animValues) {
	if b.label == "" {
		drawIconGlyphButton(dst, r, b.glyph, b.on, b.disabled, av)
		return
	}
	fill, edge, ink := colPanel, colPanelEdge, colHUD
	switch {
	case b.disabled:
		// colDisabled, not colBarline, for the same reason as the icon
		// buttons: the piece row spends the whole text view disabled, and
		// a rule colour is not readable ink.
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

// --- hit testing -------------------------------------------------------

// hotspots is every clickable control on the editor's chrome.
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
