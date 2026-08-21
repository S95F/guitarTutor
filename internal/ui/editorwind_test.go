package ui

// The wind editor: note letters onto the pitch ladder, the arrow nudges,
// the instrument picker, and the toolbar that swaps a guitar's marks for a
// horn's. These are the editing moves; the ladder geometry itself is pinned
// by wind_test.go against the practice view's identical ladder.

import (
	"strings"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/musicTutor/internal/edit"
	"github.com/S95F/musicTutor/internal/score"
)

// newTestWindEditor is a soprano sax piece open on the grid.
func newTestWindEditor(t *testing.T) *Editor {
	t.Helper()
	w := score.WindByName("soprano sax")
	if w == nil {
		t.Fatal("no soprano sax in the registry")
	}
	e := NewEditor(nil)
	e.doc = edit.New(edit.NewOptions{Wind: w})
	return e
}

// windWritten is the written pitch of the note under the cursor.
func windWritten(t *testing.T, e *Editor) int {
	t.Helper()
	n, ok := e.doc.NoteAt(1)
	if !ok {
		t.Fatal("no note under the cursor")
	}
	tr := e.doc.Track()
	return tr.Wind.Written(tr.Pitch(n))
}

// TestWindEditorTypesNoteLetters: a letter lands a natural at the octave
// nearest the reference — the middle of the horn on an empty piece, the
// note itself afterwards — and a dead tie between octaves goes up.
func TestWindEditorTypesNoteLetters(t *testing.T) {
	e := newTestWindEditor(t)

	// An empty soprano sax piece starts from the middle of its range:
	// sounding 72, written 74. D is that written D5 exactly.
	press(t, e, ebiten.KeyD)
	if got := windWritten(t, e); got != 74 {
		t.Fatalf("typing D onto an empty piece gave written %d, want 74 (D5)", got)
	}

	// From D5, A is closer below (A4, 5 semitones) than above (7).
	press(t, e, ebiten.KeyA)
	if got := windWritten(t, e); got != 69 {
		t.Errorf("typing A after D5 gave written %d, want 69 (A4)", got)
	}

	// From B4, F is six semitones either way; the tie goes up.
	press(t, e, ebiten.KeyB)
	if got := windWritten(t, e); got != 71 {
		t.Fatalf("typing B after A4 gave written %d, want 71 (B4)", got)
	}
	press(t, e, ebiten.KeyF)
	if got := windWritten(t, e); got != 77 {
		t.Errorf("typing F after B4 gave written %d, want 77 (F5): the dead tie goes up", got)
	}
}

// TestWindEditorArrowsNudgePitch: ↑/↓ move the note by a semitone, an
// octave with shift, and refuse to leave the nameable range.
func TestWindEditorArrowsNudgePitch(t *testing.T) {
	e := newTestWindEditor(t)
	press(t, e, ebiten.KeyD) // written 74
	press(t, e, ebiten.KeyUp)
	if got := windWritten(t, e); got != 75 {
		t.Errorf("↑ gave written %d, want 75", got)
	}
	pressMod(t, e, ebiten.KeyDown, mods{shift: true})
	if got := windWritten(t, e); got != 63 {
		t.Errorf("shift+↓ gave written %d, want 63", got)
	}

	// The soprano sax's lowest written note is Bb3 (58): the steps down to
	// it land, the one past it is refused and the note stays put.
	for i := 0; i < 5; i++ {
		press(t, e, ebiten.KeyDown)
	}
	if got := windWritten(t, e); got != 58 {
		t.Fatalf("stepping down to the horn's bottom left written %d, want 58 (Bb3)", got)
	}
	press(t, e, ebiten.KeyDown)
	if got := windWritten(t, e); got != 58 {
		t.Errorf("nudging past the bottom left written %d, want 58 (the refusal keeps the note)", got)
	}
	if msg, isErr := e.message(); msg == "" || !isErr {
		t.Error("nudging below the horn's range said nothing")
	}
}

// TestWindEditorRefusesGuitarKeys: fret digits and string techniques get a
// message naming what a wind part wants instead, and place nothing.
func TestWindEditorRefusesGuitarKeys(t *testing.T) {
	for _, k := range []ebiten.Key{ebiten.KeyDigit5, ebiten.KeyH, ebiten.KeyP, ebiten.KeyX} {
		e := newTestWindEditor(t)
		press(t, e, k)
		if _, ok := e.doc.NoteAt(1); ok {
			t.Errorf("key %v put a note on a wind track", k)
		}
		if msg, _ := e.message(); msg == "" {
			t.Errorf("key %v was refused silently", k)
		}
	}
}

// TestWindEditorSlurAndMarks: L toggles the slur the wind file writes as
// its own letter, and the letters that are also note names still type
// notes — B is the note B, not a bend.
func TestWindEditorSlurAndMarks(t *testing.T) {
	e := newTestWindEditor(t)
	press(t, e, ebiten.KeyD)
	press(t, e, ebiten.KeyL)
	n, _ := e.doc.NoteAt(1)
	if n.Tech&score.TechSlur == 0 {
		t.Error("L did not slur the note")
	}
	before := windWritten(t, e)
	press(t, e, ebiten.KeyB)
	if got := windWritten(t, e); got == before {
		t.Error("B did not type the note B on a wind track")
	}
	n, _ = e.doc.NoteAt(1)
	if n.Tech&score.TechBend != 0 {
		t.Error("B bent the note; on a wind track it is a note name")
	}
}

// TestWindToolbarSwapsTheMarks: the note-mark group is the wind set (no
// hammer, pull or dead note), and the piece row shows the instrument where
// a guitar shows its tuning and capo.
func TestWindToolbarSwapsTheMarks(t *testing.T) {
	e := newTestWindEditor(t)
	ids := map[string]bool{}
	for _, b := range editorButtons(e) {
		ids[b.id] = true
	}
	for _, want := range []string{"tie", "slur", "slide", "bend", "vibrato", "instrument"} {
		if !ids[want] {
			t.Errorf("the wind toolbar has no %q control", want)
		}
	}
	for _, gone := range []string{"hammer", "pull", "dead", "tuning", "capo"} {
		if ids[gone] {
			t.Errorf("the wind toolbar still offers %q", gone)
		}
	}
	chip := editorButton(t, e, "instrument")
	if !strings.Contains(chip.label, "soprano sax") {
		t.Errorf("the instrument chip reads %q, want the instrument's name", chip.label)
	}
	if !chip.disabled || chip.why == "" {
		t.Error("the instrument chip should be fixed (disabled), with the reason in its tooltip")
	}
}

// TestWindEditorStatusNamesBothPitches: the header's cursor description
// gives the written name the player reads and the sounding name the tuner
// hears — the same both-worlds honesty a capoed guitar gets.
func TestWindEditorStatusNamesBothPitches(t *testing.T) {
	e := newTestWindEditor(t)
	press(t, e, ebiten.KeyD)
	got := e.cursorPitch()
	for _, want := range []string{"written D5", "sounding C5"} {
		if !strings.Contains(got, want) {
			t.Errorf("cursor pitch reads %q, want it to contain %q", got, want)
		}
	}
}

// TestWindHelpDoesNotTeachBAsBend: on a wind track the letter B types the
// note B (TestWindEditorSlurAndMarks pins the key itself), so the help
// table must not list b among the technique keys — a row teaching "b
// bend" would rewrite the note's pitch for anyone who followed it. The
// bend mark's one control is its toolbar button, which carries no key.
func TestWindHelpDoesNotTeachBAsBend(t *testing.T) {
	e := newTestWindEditor(t)
	for _, row := range e.editorBindings() {
		if row.Group != "notes" || strings.HasPrefix(row.Keys, "A-G") {
			continue
		}
		for _, k := range strings.Split(row.Keys, "/") {
			if strings.TrimSpace(k) == "b" {
				t.Errorf("the wind help row %q (%q) advertises b, which types the note B", row.Keys, row.Desc)
			}
		}
	}
	if b := editorButton(t, e, "bend"); b.key != "" {
		t.Errorf("the wind bend button advertises key %q; B types the note B", b.key)
	}
}

// TestWindHelpTableFitsItsCard: the wind table is measured through the
// same layout the overlay draws with, like every other table.
func TestWindHelpTableFitsItsCard(t *testing.T) {
	e := newTestWindEditor(t)
	rows := e.editorBindings()
	if len(rows) == 0 || rows[0].Keys != "A-G" {
		t.Fatal("a wind editor's help table does not lead with the note letters")
	}
	f := helpLayout(rows, editorHelpFootnote)
	card := helpCard()
	if bottom := card.y + card.h; f.bottom > bottom-6 {
		t.Errorf("the wind editor's %d bindings end at %.0f; the card ends at %.0f",
			len(rows), f.bottom, bottom)
	}
}

// TestInstrumentPickerOnNewPiece: a new piece opens asking what it will be
// played on; answering rebuilds the blank document for that instrument,
// declining keeps the guitar.
func TestInstrumentPickerOnNewPiece(t *testing.T) {
	e := NewEditorChoosing(nil)
	if e.picker == nil || e.picker.purpose != pickNewPiece {
		t.Fatal("NewEditorChoosing did not open the instrument picker")
	}
	if !e.modalUp() {
		t.Error("the open picker does not register as a modal")
	}
	sax := -1
	for i, name := range pickerChoices() {
		if name == "soprano sax" {
			sax = i
		}
	}
	if sax < 0 {
		t.Fatal("the picker does not offer the soprano sax")
	}
	e.applyPick(sax)
	if e.picker != nil {
		t.Error("choosing did not close the picker")
	}
	if w := e.doc.Wind(); w == nil || w.Name != "soprano sax" {
		t.Errorf("the new piece's instrument = %v, want the soprano sax", w)
	}

	// Escape keeps the guitar the blank piece already was.
	e2 := NewEditorChoosing(nil)
	e2.cancelPick()
	if e2.doc.Wind() != nil || e2.doc.Track().Wind != nil {
		t.Error("cancelling the pick did not keep the guitar")
	}
}

// TestInstrumentPickerOnAddTrack: shift+A asks the same question for the
// new track, so a band piece can mix a guitar and a horn.
func TestInstrumentPickerOnAddTrack(t *testing.T) {
	e := newTestEditor()
	pressMod(t, e, ebiten.KeyA, mods{shift: true})
	if e.picker == nil || e.picker.purpose != pickAddTrack {
		t.Fatal("shift+A did not ask which instrument")
	}
	sax := -1
	for i, name := range pickerChoices() {
		if name == "soprano sax" {
			sax = i
		}
	}
	e.applyPick(sax)
	sc := e.doc.Score()
	if len(sc.Tracks) != 2 || sc.Tracks[1].Wind == nil {
		t.Fatalf("answering soprano sax left %d tracks (wind on the new one: %v)",
			len(sc.Tracks), len(sc.Tracks) > 1 && sc.Tracks[1].Wind != nil)
	}
	// Cancelling adds nothing.
	e.openInstrumentPicker(pickAddTrack)
	e.cancelPick()
	if got := len(e.doc.Score().Tracks); got != 2 {
		t.Errorf("a cancelled add-track pick left %d tracks, want 2", got)
	}
}

// TestWindGridUsesTheFixedBand: a wind system is sized to its fixed band
// height however few notes it holds, so the layout cannot jump as the
// piece is written.
func TestWindGridUsesTheFixedBand(t *testing.T) {
	e := newTestWindEditor(t)
	if got := e.gridLines(); got != edWindLines {
		t.Errorf("gridLines = %d, want the fixed band of %d", got, edWindLines)
	}
	l := edWindLadderFor(e.doc.Track().Wind)
	if l.hi-l.lo < 12 {
		t.Errorf("ladder compass %d..%d spans less than an octave", l.lo, l.hi)
	}
	// Higher written pitch sits higher on the band.
	const top = 100.0
	if l.y(l.lo+12, top) >= l.y(l.lo, top) {
		t.Error("a higher pitch does not sit higher on the ladder")
	}
	// The clamp keeps altissimo inside the band instead of above the bar
	// numbers.
	if y := l.y(l.hi+24, top); y < top {
		t.Errorf("a pitch above the compass drew at %.1f, above the band's top line %.1f", y, top)
	}
}
