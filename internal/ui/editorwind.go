package ui

import (
	"fmt"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/edit"
	"github.com/S95F/musicTutor/internal/score"
)

const edWindLines = 8

func (e *Editor) gridLines() int {
	if e.doc.Track().Wind != nil {
		return edWindLines
	}
	return len(e.doc.Track().Tuning)
}

type edWindLadder struct {
	lo, hi int
}

func edWindLadderFor(w *score.WindInstrument) edWindLadder {
	return edWindLadder{lo: w.Written(w.LowSounding) - 1, hi: w.Written(w.LowSounding+w.Span) + 1}
}

func (l edWindLadder) y(written int, strTop float64) float64 {
	span := float64(l.hi - l.lo)
	bottom := strTop + float64(edWindLines-1)*edStringGap
	y := bottom - float64(written-l.lo)/span*(bottom-strTop)
	if y < strTop {
		y = strTop
	}
	if y > bottom {
		y = bottom
	}
	return y
}

func drawWindSystemLines(screen *ebiten.Image, l edWindLadder, strTop, w float64) {
	for c := (l.lo + 11) / 12 * 12; c <= l.hi; c += 12 {
		y := float32(l.y(c, strTop))
		vector.StrokeLine(screen, edGridX, y, float32(edGridX+w), y, 1, colString, false)
		name := edPitchName(c)
		drawTextSmall(screen, name, edGridX-10-textWSmall(name), float64(y)-7, colDim)
	}
}

func (e *Editor) windEntryWritten() int {
	w := e.doc.Track().Wind
	return w.Written(e.doc.WindReference())
}

var edNoteClasses = map[ebiten.Key]int{
	ebiten.KeyC: 0, ebiten.KeyD: 2, ebiten.KeyE: 4, ebiten.KeyF: 5,
	ebiten.KeyG: 7, ebiten.KeyA: 9, ebiten.KeyB: 11,
}

func (e *Editor) typeNoteLetter(class int) {
	ref := e.windEntryWritten()
	base := (ref/12)*12 + class
	best := base
	for _, cand := range []int{base - 12, base, base + 12} {
		if abs(cand-ref) < abs(best-ref) || (abs(cand-ref) == abs(best-ref) && cand > best) {
			best = cand
		}
	}
	if e.report(e.doc.SetWindPitch(best)) {
		e.auditionNote()
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (e *Editor) windCursorPitch() string {
	w := e.doc.Track().Wind
	n, ok := e.doc.NoteAt(1)
	if !ok {
		return w.Name
	}
	sounding := e.doc.Track().Pitch(n)
	return fmt.Sprintf("written %s · sounding %s", edPitchName(w.Written(sounding)), edPitchName(sounding))
}

func windTechSuffix(t score.Technique) string {
	var b strings.Builder
	for _, m := range []struct {
		bit  score.Technique
		char byte
	}{
		{score.TechSlur, 'l'}, {score.TechSlide, 's'},
		{score.TechBend, 'b'}, {score.TechVibrato, 'v'},
	} {
		if t&m.bit != 0 {
			b.WriteByte(m.char)
		}
	}
	return b.String()
}

func (e *Editor) windNoteButtons() []edButton {
	tech := e.cursorTech()
	note, hasNote := e.doc.NoteAt(1)

	const needNote = "type a note first (A-G)"
	out := []edButton{{
		id: "tie", glyph: glyphTie, name: "Tie — hold, do not tongue again", key: "`",
		on: hasNote && note.Tied, disabled: !hasNote, why: needNote,
		act: func() { e.report(e.doc.ToggleTie()) },
	}}
	for _, t := range []struct {
		id, name, key string
		glyph         glyphID
		bit           score.Technique
	}{
		{"slur", "Slur — no fresh tongue", "l", glyphSlur, score.TechSlur},
		{"slide", "Slide — scoop into the note", "s", glyphSlide, score.TechSlide},
		{"bend", "Bend", "", glyphBend, score.TechBend},
		{"vibrato", "Vibrato", "v", glyphVibrato, score.TechVibrato},
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

func (e *Editor) windBindings() []helpBinding {
	return []helpBinding{
		{Group: "notes", Keys: "A-G", Hint: "A-G note", Desc: "Type a note name — it lands at the octave nearest the note before it"},
		{Group: "notes", Keys: "↑ / ↓", Hint: "↑↓ pitch", Desc: "Move the note by a semitone; with shift, by an octave"},
		{Group: "notes", Keys: "del / R", Hint: "R rest", Desc: "Clear the note, or make the whole beat a rest"},
		{Group: "notes", Keys: "`", Hint: "` tie", Desc: "Tie the note: hold it, do not tongue it again"},
		{Group: "notes", Keys: "l / s / v", Hint: "l slur", Desc: "Slur (no fresh tongue), slide, vibrato — B types the note, so the bend mark is its toolbar button"},

		{Group: "rhythm", Keys: "[ / ]", Hint: "[ ] note length", Desc: "Longer / shorter note, for this beat and the ones you type next — or click one in the toolbar"},
		{Group: "rhythm", Keys: ". / T", Desc: "Dot the note value, or make it a triplet"},
		{Group: "rhythm", Keys: "space", Hint: "space next", Desc: "Move on to the next beat, adding a bar at the end of the piece — the rhythm for writing one"},
		{Group: "rhythm", Keys: "enter", Hint: "enter insert beat", Desc: "Put another beat in after this one"},
		{Group: "rhythm", Keys: "shift+del", Desc: "Remove the beat"},

		{Group: "bars", Keys: "N", Hint: "N add bar", Desc: "Add a bar after this one (at the end of the piece, that is a new last bar)"},
		{Group: "bars", Keys: "shift+N", Desc: "Delete this bar, from every track"},
		{Group: "bars", Keys: "shift+M / shift+B", Desc: "Set the time signature or the tempo from this bar on"},

		{Group: "moving", Keys: "← / →", Hint: "←→ move", Desc: "Left and right by beat"},
		{Group: "moving", Keys: "page up/down · home/end", Desc: "A bar at a time, and the two ends of the piece"},
		{Group: "moving", Keys: "click / wheel", Desc: "Put the cursor on a beat; the wheel scrolls the notation"},

		{Group: "piece", Keys: "shift+T", Desc: "Name the piece"},
		{Group: "piece", Keys: "tab", Hint: "tab track", Desc: "Move to the next track (shift+tab for the previous one)"},
		{Group: "piece", Keys: "shift+A", Desc: "Add another track"},
		{Group: "piece", Keys: "F2", Hint: "F2 file text", Desc: "Show the piece as the text file it saves to — where the instrument voice or a comment can be set"},

		{Group: "session", Keys: "ctrl+Z / ctrl+Y", Hint: "ctrl+Z undo", Desc: "Undo and redo"},
		{Group: "session", Keys: "ctrl+S", Hint: "ctrl+S save", Desc: "Save (ctrl+shift+S saves under a new name)"},
		{Group: "session", Keys: "shift+P", Hint: "shift+P practice", Off: e.practice == nil,
			Desc: edPracticeWhat},
		{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
		{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Leave the editor"},
	}
}

func windFirstStepsContent() ([]firstStep, string) {
	lines := []firstStep{
		{"A-G", "type a note — it lands nearest the one before it"},
		{"↑ ↓", "move it by a semitone (shift: an octave); [ and ] choose the note value"},
		{"space", "move on to the next beat — and past the last bar, it makes another"},
	}
	tail := "ctrl+S saves it into your library · shift+P opens it for practice · ? lists every key"
	return lines, tail
}

type pickPurpose int

const (
	pickNewPiece pickPurpose = iota
	pickAddTrack
)

type edPicker struct {
	purpose pickPurpose
	sel     int
}

func pickerChoices() []string {
	out := []string{"guitar"}
	return append(out, score.WindNames()...)
}

var edPickerChoices = pickerChoices()

func pickerWind(i int) *score.WindInstrument {
	if i <= 0 {
		return nil
	}
	names := score.WindNames()
	if i-1 >= len(names) {
		return nil
	}
	return score.WindByName(names[i-1])
}

func (e *Editor) openInstrumentPicker(p pickPurpose) {
	e.picker = &edPicker{purpose: p}
}

func (e *Editor) updatePicker() {
	n := len(edPickerChoices)
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyUp):
		e.picker.sel = (e.picker.sel + n - 1) % n
	case inpututil.IsKeyJustPressed(ebiten.KeyDown):
		e.picker.sel = (e.picker.sel + 1) % n
	case inpututil.IsKeyJustPressed(ebiten.KeyEnter), inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter),
		inpututil.IsKeyJustPressed(ebiten.KeySpace):
		e.applyPick(e.picker.sel)
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		e.cancelPick()
	default:
		chosen := -1
		e.ptr.hit(e.pickerSpots(&chosen))
		if chosen >= 0 {
			e.applyPick(chosen)
		}
	}
}

func (e *Editor) applyPick(i int) {
	p := e.picker
	e.picker = nil
	w := pickerWind(i)
	switch p.purpose {
	case pickNewPiece:

		e.doc = edit.New(edit.NewOptions{Wind: w})
	case pickAddTrack:
		e.report(e.doc.AddTrack(fmt.Sprintf("Track %d", len(e.doc.Score().Tracks)+1), w))
	}
}

func (e *Editor) cancelPick() { e.picker = nil }

func edPickerRect() rect {
	h := 78.0 + float64(len(edPickerChoices))*edPickRowH + 16
	return rect{screenW/2 - 220, screenH/2 - h/2, 440, h}
}

const edPickRowH = 30.0

func (e *Editor) pickerSpots(out *int) []hotspot {
	r := edPickerRect()
	var spots []hotspot
	for i := range edPickerChoices {
		idx := i
		spots = append(spots, hotspot{
			r:  rect{r.x + 16, r.y + 66 + float64(i)*edPickRowH, r.w - 32, edPickRowH},
			on: func() { *out = idx },
		})
	}
	return spots
}

func (e *Editor) drawPicker(screen *ebiten.Image) {
	vector.DrawFilledRect(screen, 0, 0, screenW, screenH, colHelpDim, false)
	r := edPickerRect()
	drawPanel(screen, r, colPanel, colSounding)
	title := "What will this piece be played on?"
	if e.picker.purpose == pickAddTrack {
		title = "What will the new track be played on?"
	}
	drawTextScaled(screen, title, centreXScaled(title, r.x, r.w, 1.3), r.y+18, 1.3, colNote)

	for i, name := range edPickerChoices {
		row := rect{r.x + 16, r.y + 66 + float64(i)*edPickRowH, r.w - 32, edPickRowH}
		fill, edge, ink := colPanel, colPanelEdge, colHUD
		if i == e.picker.sel {
			fill, edge, ink = colFocus, colInferred, colNote
		}
		if e.ptr.over(row) {
			edge = colNote
		}
		drawPanel(screen, rect{row.x, row.y + 2, row.w, row.h - 4}, fill, edge)
		g := glyphTuning
		if i > 0 {
			g = glyphWind
		}
		drawGlyph(screen, g, rect{row.x + 8, row.y + (row.h-glyphBox)/2, glyphBox, glyphBox}, ink)
		drawText(screen, name, row.x+8+glyphBox+8, row.y+(row.h-uiTextH)/2+1, ink)
	}
	hint := "↑ ↓ choose · enter takes it · esc keeps the guitar"
	if e.picker.purpose == pickAddTrack {
		hint = "↑ ↓ choose · enter takes it · esc adds nothing"
	}
	drawTextSmall(screen, hint, r.x+(r.w-textWSmall(hint))/2, r.y+r.h-22, colDim)
}
