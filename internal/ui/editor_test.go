package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/guitarTutor/internal/edit"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
)

// newTestEditor builds an editor with no Shell behind it. Everything the
// tests drive is a plain method, so no game loop is involved.
func newTestEditor() *Editor { return NewEditor(nil) }

// press applies one key with no modifiers.
func press(t *testing.T, e *Editor, k ebiten.Key) error {
	t.Helper()
	return e.handleKey(k, mods{})
}

// pressMod applies one key with modifiers.
func pressMod(t *testing.T, e *Editor, k ebiten.Key, m mods) error {
	t.Helper()
	return e.handleKey(k, m)
}

func TestEditorTypesAFret(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)
	n, ok := e.doc.NoteAt(e.doc.Cursor().Str)
	if !ok || n.Fret != 5 {
		t.Fatalf("got %+v ok=%v, want fret 5", n, ok)
	}
}

func TestEditorTypesATwoDigitFret(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit1)
	press(t, e, ebiten.KeyDigit2)
	n, ok := e.doc.NoteAt(e.doc.Cursor().Str)
	if !ok || n.Fret != 12 {
		t.Fatalf("got fret %d ok=%v, want 12", n.Fret, ok)
	}
	// Past the hold window the next digit starts a fret of its own rather
	// than making a three-digit number.
	e.frame += edFretHoldFrames + 1
	e.expireFretDigits()
	press(t, e, ebiten.KeyDigit3)
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Fret != 3 {
		t.Errorf("after the hold window the fret is %d, want 3", n.Fret)
	}
}

func TestEditorSecondDigitOutOfRangeStartsOver(t *testing.T) {
	// 2 then 9 would be fret 29, which is legal; 3 then 9 would be 39,
	// which is past the format's limit, so the 9 has to become a fret of
	// its own instead of being swallowed.
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit3)
	press(t, e, ebiten.KeyDigit9)
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Fret != 9 {
		t.Errorf("got fret %d, want 9 (39 is out of range)", n.Fret)
	}
}

func TestEditorNonDigitEndsAPendingFret(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit1)
	press(t, e, ebiten.KeyRight) // move: not a digit
	press(t, e, ebiten.KeyDigit2)
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Fret != 2 {
		t.Errorf("got fret %d, want 2 — the move should have ended the pending 1", n.Fret)
	}
}

func TestEditorDurationKeys(t *testing.T) {
	e := newTestEditor()
	if got := e.doc.Duration(); got != score.Quarter {
		t.Fatalf("a new piece starts on %d ticks, want a quarter (%d)", got, score.Quarter)
	}
	press(t, e, ebiten.KeyBracketRight) // shorter
	if got := e.doc.Duration(); got != score.Eighth {
		t.Errorf("got %d ticks, want an eighth (%d)", got, score.Eighth)
	}
	press(t, e, ebiten.KeyPeriod) // dot it
	if got, want := e.doc.Duration(), score.Dotted(score.Eighth); got != want {
		t.Errorf("got %d ticks, want a dotted eighth (%d)", got, want)
	}
	press(t, e, ebiten.KeyT) // triplet replaces the dot
	if got, want := e.doc.Duration(), score.Triplet(score.Eighth); got != want {
		t.Errorf("got %d ticks, want an eighth triplet (%d)", got, want)
	}
	press(t, e, ebiten.KeyBracketLeft) // longer
	if got, want := e.doc.Duration(), score.Triplet(score.Quarter); got != want {
		t.Errorf("got %d ticks, want a quarter triplet (%d)", got, want)
	}
}

func TestEditorDurationStopsAtTheEnds(t *testing.T) {
	e := newTestEditor()
	for i := 0; i < 10; i++ {
		press(t, e, ebiten.KeyBracketRight)
	}
	if got := e.doc.Duration(); got != score.ThirtySec {
		t.Errorf("got %d ticks, want the shortest value (%d)", got, score.ThirtySec)
	}
	for i := 0; i < 10; i++ {
		press(t, e, ebiten.KeyBracketLeft)
	}
	if got := e.doc.Duration(); got != score.Whole {
		t.Errorf("got %d ticks, want the longest value (%d)", got, score.Whole)
	}
}

func TestEditorDurationThatWillNotFitStillArmsTheNextBeat(t *testing.T) {
	// Choosing a note value must never be refused outright: if it does not
	// fit the beat under the cursor it still has to become the value the
	// next note typed will take, or the palette would be unusable in a bar
	// that happens to be full.
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit0) // a quarter note
	press(t, e, ebiten.KeyRight)
	press(t, e, ebiten.KeyDigit0)
	press(t, e, ebiten.KeyRight)
	press(t, e, ebiten.KeyDigit0)
	press(t, e, ebiten.KeyRight)
	press(t, e, ebiten.KeyDigit0) // the 4/4 bar is now four quarters
	e.setDuration(score.Whole)
	if got := e.doc.Duration(); got != score.Whole {
		t.Errorf("the palette is on %d ticks, want the whole note that did not fit", got)
	}
	if msg, isErr := e.message(); !isErr || !strings.Contains(msg, "next beat") {
		t.Errorf("got %q (error=%v), want a message saying the value applies to the next beat", msg, isErr)
	}
}

func TestEditorTechniquesAndTie(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)
	for _, tc := range []struct {
		key ebiten.Key
		bit score.Technique
	}{
		{ebiten.KeyH, score.TechHammer},
		{ebiten.KeyP, score.TechPull},
		{ebiten.KeyS, score.TechSlide},
		{ebiten.KeyB, score.TechBend},
		{ebiten.KeyV, score.TechVibrato},
		{ebiten.KeyX, score.TechDead},
	} {
		press(t, e, tc.key)
		if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Tech&tc.bit == 0 {
			t.Errorf("%v did not set technique %d", tc.key, tc.bit)
		}
		press(t, e, tc.key)
		if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Tech&tc.bit != 0 {
			t.Errorf("%v did not clear technique %d", tc.key, tc.bit)
		}
	}
	press(t, e, ebiten.KeyRight)
	press(t, e, ebiten.KeyDigit5)
	press(t, e, ebiten.KeyBackquote)
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); !n.Tied {
		t.Error("the backquote did not tie the note")
	}
}

func TestEditorShiftedKeysDoNotAlsoFireTheirPlainMeaning(t *testing.T) {
	// shift+B opens the tempo entry and shift+P saves-and-practices; both
	// letters are also techniques, and a shifted press that ALSO bent the
	// note would be a silent corruption of the piece.
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)
	pressMod(t, e, ebiten.KeyB, mods{shift: true})
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Tech&score.TechBend != 0 {
		t.Error("shift+B bent the note as well as opening the tempo entry")
	}
	if e.entry == nil {
		t.Fatal("shift+B did not open the tempo entry")
	}
	e.entry = nil
	pressMod(t, e, ebiten.KeyT, mods{shift: true})
	if e.entry == nil || e.entry.kind != edEntryTitle {
		t.Error("shift+T did not open the title entry")
	}
}

func TestEditorBarOperations(t *testing.T) {
	e := newTestEditor()
	before := e.doc.BarCount()
	press(t, e, ebiten.KeyN)
	if got := e.doc.BarCount(); got != before+1 {
		t.Errorf("got %d bars, want %d", got, before+1)
	}
	pressMod(t, e, ebiten.KeyN, mods{shift: true})
	if got := e.doc.BarCount(); got != before {
		t.Errorf("after deleting, got %d bars, want %d", got, before)
	}
}

func TestEditorAddBarAtTheEndAppends(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyEnd)
	last := e.doc.Cursor().Bar
	press(t, e, ebiten.KeyN)
	if got := e.doc.Cursor().Bar; got != last+1 {
		t.Errorf("the cursor is in bar %d, want the new last bar %d", got, last+1)
	}
}

func TestEditorUndoRedoKeys(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit7)
	pressMod(t, e, ebiten.KeyZ, mods{ctrl: true})
	if _, ok := e.doc.NoteAt(e.doc.Cursor().Str); ok {
		t.Error("ctrl+Z did not take the note back")
	}
	pressMod(t, e, ebiten.KeyY, mods{ctrl: true})
	if n, ok := e.doc.NoteAt(e.doc.Cursor().Str); !ok || n.Fret != 7 {
		t.Error("ctrl+Y did not put the note back")
	}
	// ctrl+shift+Z is the other redo, so it has to have something to redo.
	pressMod(t, e, ebiten.KeyZ, mods{ctrl: true})
	pressMod(t, e, ebiten.KeyZ, mods{ctrl: true, shift: true})
	if n, ok := e.doc.NoteAt(e.doc.Cursor().Str); !ok || n.Fret != 7 {
		t.Error("ctrl+shift+Z did not redo")
	}
}

func TestEditorLeavingCleanQuitsAtOnce(t *testing.T) {
	e := newTestEditor()
	if err := press(t, e, ebiten.KeyEscape); err != errQuit {
		t.Errorf("Escape on an unmodified piece = %v, want errQuit", err)
	}
}

func TestEditorLeavingDirtyAsksFirst(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit3)
	if err := press(t, e, ebiten.KeyEscape); err != nil {
		t.Fatalf("Escape on a changed piece = %v, want nil (the prompt goes up)", err)
	}
	if !e.leaving {
		t.Fatal("the unsaved-changes prompt did not open")
	}
	if err := e.applyLeaveChoice(edLeaveCancel); err != nil {
		t.Errorf("cancel = %v, want nil", err)
	}
	if e.leaving {
		t.Error("cancel left the prompt up")
	}
	e.leaving = true
	if err := e.applyLeaveChoice(edLeaveDiscard); err != errQuit {
		t.Errorf("discard = %v, want errQuit", err)
	}
}

func TestEditorSaveWritesAndCleansTheDirtyFlag(t *testing.T) {
	e := newTestEditor()
	path := filepath.Join(t.TempDir(), "riff.gtab")
	var saved []string
	e.SetOnSaved(func(p string) { saved = append(saved, p) })
	press(t, e, ebiten.KeyDigit3)
	if !e.doc.Dirty() {
		t.Fatal("typing a note left the piece clean")
	}
	e.path = path
	if !e.save() {
		msg, _ := e.message()
		t.Fatalf("save failed: %s", msg)
	}
	if e.doc.Dirty() {
		t.Error("the piece is still dirty after saving")
	}
	if len(saved) != 1 || saved[0] != path {
		t.Errorf("onSaved got %v, want [%s]", saved, path)
	}
	back, err := textfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("the saved file does not parse: %v", err)
	}
	if got := back.Tracks[0].Bars[0].Beats[0].Notes; len(got) != 1 || got[0].Fret != 3 {
		t.Errorf("the saved file holds %+v, want one note at fret 3", got)
	}
}

func TestEditorSaveWithoutAPathAsksTheDialog(t *testing.T) {
	e := newTestEditor()
	var asked []string
	e.SetSaveDialog(func(suggest string) { asked = append(asked, suggest) })
	e.doc.SetTitle("My Riff")
	e.save()
	if len(asked) != 1 {
		t.Fatalf("the dialog was asked %d times, want 1", len(asked))
	}
	if !strings.HasSuffix(asked[0], ".gtab") || !strings.Contains(asked[0], "My Riff") {
		t.Errorf("suggested %q, want a .gtab name built from the title", asked[0])
	}
	// A second save while the dialog is up must not stack another.
	e.save()
	if len(asked) != 1 {
		t.Errorf("a second save opened another dialog (%d total)", len(asked))
	}
	// The answer arrives on the mailbox and is applied on the game loop.
	path := filepath.Join(t.TempDir(), "answer")
	e.OfferSavePath(path)
	e.drainDialog()
	if e.path != path+".gtab" {
		t.Errorf("saved to %q, want %q — a missing extension should be added", e.path, path+".gtab")
	}
	if _, err := os.Stat(e.path); err != nil {
		t.Errorf("the file was not written: %v", err)
	}
}

func TestEditorCancelledSaveDialogReArms(t *testing.T) {
	e := newTestEditor()
	var asked int
	e.SetSaveDialog(func(string) { asked++ })
	e.save()
	e.OfferSavePath("") // cancel
	e.drainDialog()
	if e.dialogBusy {
		t.Error("a cancelled dialog left the busy guard set")
	}
	e.save()
	if asked != 2 {
		t.Errorf("the dialog was asked %d times, want 2 — the cancel should have re-armed it", asked)
	}
}

func TestEditorSaveWithNoDialogSaysSo(t *testing.T) {
	e := newTestEditor()
	e.save()
	msg, isErr := e.message()
	if !isErr || !strings.Contains(msg, "save dialog") {
		t.Errorf("got %q (error=%v), want an explanation that there is no save dialog", msg, isErr)
	}
}

func TestEditorTextViewRoundTrips(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit7)
	before, err := textfmt.Format(e.doc.Score())
	if err != nil {
		t.Fatal(err)
	}
	e.toggleText()
	if e.text == nil {
		t.Fatal("F2 did not open the text view")
	}
	if got := e.text.text(); got != string(before) {
		t.Errorf("the text view shows\n%s\nwant\n%s", got, before)
	}
	if !e.text.ok {
		t.Errorf("the text view says the piece does not parse: %s", e.text.status)
	}
	e.toggleText()
	if e.text != nil {
		t.Fatal("F2 did not close the text view")
	}
	after, err := textfmt.Format(e.doc.Score())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("a round trip through the text view changed the piece\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

func TestEditorTextViewAppliesEdits(t *testing.T) {
	e := newTestEditor()
	e.toggleText()
	// Retitle the piece by editing the text, the way the view exists to
	// allow.
	for i, line := range e.text.lines {
		if strings.HasPrefix(string(line), "\\tempo") {
			e.text.lines[i] = []rune("\\tempo 96")
		}
	}
	e.text.reparse()
	if !e.text.ok {
		t.Fatalf("the edited text does not parse: %s", e.text.status)
	}
	e.toggleText()
	if e.text != nil {
		t.Fatalf("the view stayed open: %v", e.msg)
	}
	if got := e.doc.TempoAtCursor(); got < 95.9 || got > 96.1 {
		t.Errorf("the tempo came back as %g, want 96", got)
	}
	if !e.doc.Dirty() {
		t.Error("applying a text edit left the piece clean")
	}
}

func TestEditorTextViewRefusesToApplyBrokenText(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit3)
	e.toggleText()
	e.text.lines = append(e.text.lines, []rune("this is not a piece"))
	e.text.reparse()
	if e.text.ok {
		t.Fatal("nonsense parsed")
	}
	e.toggleText()
	if e.text == nil {
		t.Fatal("the view closed on text that does not parse, throwing the typing away")
	}
	msg, isErr := e.message()
	if !isErr || msg == "" {
		t.Errorf("got %q (error=%v), want the parser's complaint", msg, isErr)
	}
	// The note typed before switching is still in the document.
	if _, ok := e.doc.NoteAt(e.doc.Cursor().Str); !ok {
		t.Error("the document lost its note while the text was broken")
	}
}

func TestGtabPaneEditing(t *testing.T) {
	e := newTestEditor()
	e.toggleText()
	p := e.text
	p.cy, p.cx = 0, 0
	p.insertRune('/')
	p.insertRune('/')
	if got := string(p.lines[0][:2]); got != "//" {
		t.Errorf("got %q, want the two slashes just typed", got)
	}
	if p.cx != 2 {
		t.Errorf("the caret is at column %d, want 2", p.cx)
	}
	p.applyKey(ebiten.KeyBackspace)
	if got := string(p.lines[0][:1]); got != "/" {
		t.Errorf("got %q after a backspace, want one slash", got)
	}
	// Splitting and rejoining a line has to be exactly reversible.
	before := p.text()
	p.splitLine()
	p.backspace()
	if got := p.text(); got != before {
		t.Errorf("split then join changed the text\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

func TestGtabPaneCaretStaysInBounds(t *testing.T) {
	e := newTestEditor()
	e.toggleText()
	p := e.text
	p.cy, p.cx = 999, 999
	p.clampCaret()
	if p.cy != len(p.lines)-1 {
		t.Errorf("the caret is on line %d, want the last (%d)", p.cy, len(p.lines)-1)
	}
	if p.cx > len(p.lines[p.cy]) {
		t.Errorf("the caret is at column %d, past the line's %d", p.cx, len(p.lines[p.cy]))
	}
	p.cy, p.cx = -5, -5
	p.clampCaret()
	if p.cy != 0 || p.cx != 0 {
		t.Errorf("got line %d column %d, want the start", p.cy, p.cx)
	}
}

func TestEditorClickPutsTheCursorWhereYouClicked(t *testing.T) {
	e := newTestEditor()
	systems := e.layoutSystems()
	if len(systems) == 0 {
		t.Fatal("the piece laid out to no systems")
	}
	// The second bar of the first system, on string 3.
	sys := systems[0]
	if len(sys.bars) < 2 {
		t.Skip("the default piece does not put two bars on one system")
	}
	box := sys.bars[1]
	strTop := edGridTop + edSysPadTop
	x := edGridX + box.x + 10
	y := strTop + 2*edStringGap
	if !e.clickGrid(x, y) {
		t.Fatal("the click missed the staff")
	}
	c := e.doc.Cursor()
	if c.Bar != box.index {
		t.Errorf("the cursor is in bar %d, want %d", c.Bar, box.index)
	}
	if c.Str != 3 {
		t.Errorf("the cursor is on string %d, want 3", c.Str)
	}
}

func TestEditorClickOutsideTheStaffIsIgnored(t *testing.T) {
	e := newTestEditor()
	before := e.doc.Cursor()
	if e.clickGrid(100, 10) {
		t.Error("a click on the header was taken as a click on the staff")
	}
	if e.doc.Cursor() != before {
		t.Error("a click outside the staff moved the cursor")
	}
}

// TestEditorLayoutDoesNotOverflow checks that bars are packed into systems
// that fit the window. A bar drawn past the right margin is one the user
// cannot click.
func TestEditorLayoutDoesNotOverflow(t *testing.T) {
	e := newTestEditor()
	for i := 0; i < 30; i++ {
		if err := e.doc.AppendBar(); err != nil {
			t.Fatal(err)
		}
	}
	systems := e.layoutSystems()
	seen := 0
	for si, sys := range systems {
		if len(sys.bars) == 0 {
			t.Errorf("system %d has no bars", si)
		}
		for _, b := range sys.bars {
			if b.index != seen {
				t.Fatalf("bar %d appears at position %d; the systems are out of order", b.index, seen)
			}
			seen++
			if b.x+b.w > edGridW+0.01 {
				t.Errorf("bar %d ends at %.1f, past the %.1f the page has", b.index+1, b.x+b.w, edGridW)
			}
		}
	}
	if seen != e.doc.BarCount() {
		t.Errorf("the layout placed %d bars, the piece has %d", seen, e.doc.BarCount())
	}
}

// TestEditorScrollFollowsTheCursor checks that walking to the end of a
// long piece brings the notation with it.
func TestEditorScrollFollowsTheCursor(t *testing.T) {
	e := newTestEditor()
	for i := 0; i < 40; i++ {
		if err := e.doc.AppendBar(); err != nil {
			t.Fatal(err)
		}
	}
	e.doc.GoToEnd()
	systems := e.layoutSystems()
	e.clampScroll(systems)
	h := e.systemHeight()
	si := systemOfBar(systems, e.doc.Cursor().Bar)
	top := edGridTop + float64(si)*h - e.scroll
	if top < edGridTop-0.01 || top+h > gridBottom()+0.01 {
		t.Errorf("the cursor's system is drawn at %.1f..%.1f, outside the visible %.1f..%.1f",
			top, top+h, edGridTop, gridBottom())
	}
}

func TestEditorForAnImportedPieceHasNoPath(t *testing.T) {
	src, err := os.ReadFile("../../testdata/fixture_rich.gtab")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := textfmt.Parse(src, "rich")
	if err != nil {
		t.Fatal(err)
	}
	// A .gp or .mid piece cannot be written back to its own file, so the
	// editor must not remember one.
	e, err := NewEditorFor(nil, sc, "C:\\music\\song.gp")
	if err != nil {
		t.Fatal(err)
	}
	if e.Path() != "" {
		t.Errorf("the editor kept %q as its save target; only .gtab can be written back", e.Path())
	}
	e2, err := NewEditorFor(nil, sc, "C:\\music\\song.gtab")
	if err != nil {
		t.Fatal(err)
	}
	if e2.Path() == "" {
		t.Error("the editor dropped a .gtab path it could have saved to")
	}
}

func TestEditorTuningCycle(t *testing.T) {
	e := newTestEditor()
	if got := e.tuningName(); got != "standard E" {
		t.Errorf("a new piece is in %q, want standard E", got)
	}
	e.cycleTuning()
	if got := e.tuningName(); got != "drop D" {
		t.Errorf("after one cycle the tuning is %q, want drop D", got)
	}
	// All the way round returns to where it started.
	for i := 1; i < len(edTunings); i++ {
		e.cycleTuning()
	}
	if got := e.tuningName(); got != "standard E" {
		t.Errorf("after a full cycle the tuning is %q, want standard E", got)
	}
}

func TestEditorMeterEntry(t *testing.T) {
	e := newTestEditor()
	e.openEntry(edEntryMeter)
	if e.entry == nil {
		t.Fatal("the meter entry did not open")
	}
	if e.entry.buf != "4/4" {
		t.Errorf("the entry is seeded with %q, want the meter in force (4/4)", e.entry.buf)
	}
	e.entry.buf = "7/8"
	e.commitEntry()
	if e.entry != nil {
		t.Fatalf("the entry stayed open: %v", e.msg)
	}
	if bar := e.doc.Bar(); bar.Num != 7 || bar.Den != 8 {
		t.Errorf("the bar is %d/%d, want 7/8", bar.Num, bar.Den)
	}
}

func TestEditorEntryKeepsBadInput(t *testing.T) {
	e := newTestEditor()
	e.openEntry(edEntryTempo)
	e.entry.buf = "0"
	e.commitEntry()
	if e.entry == nil {
		t.Fatal("a refused tempo closed the entry and threw away what was typed")
	}
	if msg, isErr := e.message(); !isErr || msg == "" {
		t.Errorf("got %q (error=%v), want an explanation", msg, isErr)
	}
	e.entry.buf = "180"
	e.commitEntry()
	if e.entry != nil {
		t.Fatal("a good tempo did not close the entry")
	}
	if got := e.doc.TempoAtCursor(); got < 179.9 || got > 180.1 {
		t.Errorf("the tempo is %g, want 180", got)
	}
}

func TestEditorToolbarChipsMatchTheKeys(t *testing.T) {
	// The chips exist to teach the keyboard, so every one of them has to
	// do exactly what its key does.
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)

	find := func(label string) edChip {
		t.Helper()
		for _, c := range e.toolbarChips() {
			if c.state.label == label {
				return c
			}
		}
		t.Fatalf("no toolbar chip labelled %q", label)
		return edChip{}
	}
	find("h").act()
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Tech&score.TechHammer == 0 {
		t.Error("the h chip did not set the hammer-on")
	}
	if !find("h").state.on {
		t.Error("the h chip does not light up for a note that has a hammer-on")
	}
	find("+bar").act()
	if got := e.doc.BarCount(); got != 5 {
		t.Errorf("the +bar chip left %d bars, want 5", got)
	}
}

func TestEditorTechniqueChipsAreOffWithoutANote(t *testing.T) {
	e := newTestEditor() // the cursor starts on a rest
	for _, c := range e.toolbarChips() {
		switch c.state.label {
		case "h", "p", "s", "b", "v", "x", "tie":
			if !c.state.disabled {
				t.Errorf("the %q chip is live with no note under the cursor", c.state.label)
			}
		}
	}
}

func TestEditorHotspotsAreInsideTheWindow(t *testing.T) {
	e := newTestEditor()
	if err := e.doc.AddTrack("Rhythm"); err != nil {
		t.Fatal(err)
	}
	if err := e.doc.AddTrack("Bass"); err != nil {
		t.Fatal(err)
	}
	for _, h := range e.hotspots() {
		if h.r.x < 0 || h.r.y < 0 || h.r.x+h.r.w > screenW || h.r.y+h.r.h > screenH {
			t.Errorf("a control sits at %+v, outside the %dx%d window", h.r, screenW, screenH)
		}
	}
}

func TestEditorOpenAndSaveAnImportedPiece(t *testing.T) {
	// The whole round trip a user takes when they want to fix a bar of an
	// imported piece: open it, change something, save it as .gtab, and get
	// a file that plays back as what the editor showed.
	src, err := os.ReadFile("../../testdata/fixture_rich.gtab")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := textfmt.Parse(src, "rich")
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEditorFor(nil, sc, "")
	if err != nil {
		t.Fatal(err)
	}
	e.doc.GoTo(edit.Cursor{Bar: 0, Beat: 0, Str: 6})
	if err := e.doc.SetFret(5); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "edited.gtab")
	e.path = path
	if !e.save() {
		msg, _ := e.message()
		t.Fatalf("save failed: %s", msg)
	}
	back, err := textfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("the saved piece does not parse: %v", err)
	}
	if got := back.Tracks[0].Bars[0].Beats[0].Notes[0].Fret; got != 5 {
		t.Errorf("the saved piece has fret %d in the first beat, want 5", got)
	}
	if len(back.Tracks) != 2 {
		t.Errorf("the saved piece has %d tracks, want the 2 it was opened with", len(back.Tracks))
	}
}

func TestEditorNotationChipsAreDeadInTextMode(t *testing.T) {
	// A note chip clicked while the text is showing would edit the document
	// UNDER the text, and applying the text on the way out would discard
	// that edit without a word. The chips are greyed instead.
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)
	e.toggleText()
	if e.text == nil {
		t.Fatal("the text view did not open")
	}
	for _, c := range e.toolbarChips() {
		if !c.state.disabled {
			t.Errorf("the %q toolbar chip is live while the text is showing", c.state.label)
		}
	}
	for _, c := range e.trackChips() {
		if !c.state.disabled {
			t.Errorf("the %q track chip is live while the text is showing", c.state.label)
		}
	}
	// The piece-wide actions stay live: save, back to the grid, practice.
	for _, c := range e.actionChips() {
		if c.state.label != "practice" && c.state.disabled {
			t.Errorf("the %q action chip is greyed while the text is showing", c.state.label)
		}
	}
	// And no hotspot reaches a greyed chip.
	for _, h := range e.hotspots() {
		if h.r.y < edGridTop && h.r.x < screenW/2 {
			t.Errorf("a left-hand chip at %+v is still clickable in text mode", h.r)
		}
	}
}

func TestEditorHelpTableFitsItsCard(t *testing.T) {
	// The overlay has no scrolling, so a table that outgrows the card runs
	// its last rows off the bottom of the window — which is where the
	// "how do I get out of here" line lives.
	e := newTestEditor()
	rows := e.editorBindings()
	groups := 0
	last := ""
	for _, r := range rows {
		if r.Group != last {
			groups++
			last = r.Group
		}
	}
	h := helpTopY + float64(groups)*(helpHeadH+helpGroupH) + float64(len(rows))*helpRowH
	// Plus the footnote and the dismiss line under it.
	h += 2 * uiTextH
	if card := helpCard(); h > card.y+card.h {
		t.Errorf("the editor's %d bindings in %d groups need %.0f pixels; the card ends at %.0f",
			len(rows), groups, h, card.y+card.h)
	}
}

// --- writing a piece from nothing ----------------------------------------

func TestEditorSpaceAdvancesAndGrowsThePiece(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit0)
	before := e.doc.Cursor()
	press(t, e, ebiten.KeySpace)
	if got := e.doc.Cursor(); got.Beat == before.Beat && got.Bar == before.Bar {
		t.Fatalf("space left the cursor at %+v", got)
	}
	// Space at the very end of the piece makes somewhere to keep typing,
	// which is the whole point of it being a separate key from the arrows.
	e.doc.GoToEnd()
	bars := e.doc.BarCount()
	press(t, e, ebiten.KeySpace)
	if got := e.doc.BarCount(); got != bars+1 {
		t.Errorf("space at the end left %d bars, want %d", got, bars+1)
	}
	if got := e.doc.Cursor().Bar; got != bars {
		t.Errorf("the cursor is in bar %d, want the new one (%d)", got, bars)
	}
	// And the arrows are unchanged: they navigate, they do not create.
	e.doc.GoToEnd()
	bars = e.doc.BarCount()
	press(t, e, ebiten.KeyRight)
	if got := e.doc.BarCount(); got != bars {
		t.Errorf("the right arrow added a bar (%d, was %d)", got, bars)
	}
}

func TestEditorStringLabelsIgnoreTheCapo(t *testing.T) {
	// A capo is a movable nut; a guitarist still calls the fifth string
	// the A string with one on, and tab is written that way.
	e := newTestEditor()
	if got := edStringName(e.doc.Track(), 5); got != "A" {
		t.Errorf("string 5 is labelled %q, want A", got)
	}
	if err := e.doc.SetCapo(2); err != nil {
		t.Fatal(err)
	}
	if got := edStringName(e.doc.Track(), 5); got != "A" {
		t.Errorf("with a capo on, string 5 is labelled %q, want A still", got)
	}
	if got := edStringName(e.doc.Track(), 99); got != "?" {
		t.Errorf("a string that does not exist is labelled %q", got)
	}
}

func TestEditorCursorPitchReportsWhatSounds(t *testing.T) {
	// The fret number is a position; what a writer checks against their
	// ear is the pitch, and the capo moves that even though it does not
	// move the string's name.
	e := newTestEditor()
	e.doc.GoTo(edit.Cursor{Bar: 0, Beat: 0, Str: 5})
	if got := e.cursorPitch(); !strings.Contains(got, "str 5 (A)") {
		t.Errorf("with no note the cursor reads %q", got)
	}
	if err := e.doc.SetFret(0); err != nil {
		t.Fatal(err)
	}
	if got := e.cursorPitch(); !strings.HasSuffix(got, "= A2") {
		t.Errorf("open A reads %q, want it to sound A2", got)
	}
	if err := e.doc.SetCapo(2); err != nil {
		t.Fatal(err)
	}
	if got := e.cursorPitch(); !strings.HasSuffix(got, "= B2") {
		t.Errorf("open A at capo 2 reads %q, want it to sound B2", got)
	}
}

func TestEditorRhythmFlags(t *testing.T) {
	// The flags are the readable half of a note value: one for an eighth,
	// two for a sixteenth, and none for anything a quarter or longer.
	for _, tt := range []struct {
		base int64
		want int
	}{
		{score.Whole, 0}, {score.Half, 0}, {score.Quarter, 0},
		{score.Eighth, 1}, {score.Sixteenth, 2}, {score.ThirtySec, 3},
	} {
		if got := edFlagsFor(tt.base); got != tt.want {
			t.Errorf("edFlagsFor(%d) = %d, want %d", tt.base, got, tt.want)
		}
	}
}

func TestEditorFirstStepsGoAwayOnTheFirstNote(t *testing.T) {
	e := newTestEditor()
	if !e.isEmpty() {
		t.Fatal("a new piece does not report itself empty")
	}
	press(t, e, ebiten.KeyDigit5)
	if e.isEmpty() {
		t.Error("a piece with a note in it still reports itself empty")
	}
	// A rest is not a note: clearing the only note brings the guidance
	// back, because the staff is blank again.
	press(t, e, ebiten.KeyDelete)
	if !e.isEmpty() {
		t.Error("clearing the only note did not leave the piece empty")
	}
}
