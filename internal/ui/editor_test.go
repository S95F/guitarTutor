package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/musicTutor/internal/edit"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func newTestEditor() *Editor { return NewEditor(nil) }

func press(t *testing.T, e *Editor, k ebiten.Key) error {
	t.Helper()
	return e.handleKey(k, mods{})
}

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

	e.frame += edFretHoldFrames + 1
	e.expireFretDigits()
	press(t, e, ebiten.KeyDigit3)
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Fret != 3 {
		t.Errorf("after the hold window the fret is %d, want 3", n.Fret)
	}
}

func TestEditorSecondDigitOutOfRangeStartsOver(t *testing.T) {

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
	press(t, e, ebiten.KeyRight)
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
	press(t, e, ebiten.KeyBracketRight)
	if got := e.doc.Duration(); got != score.Eighth {
		t.Errorf("got %d ticks, want an eighth (%d)", got, score.Eighth)
	}
	press(t, e, ebiten.KeyPeriod)
	if got, want := e.doc.Duration(), score.Dotted(score.Eighth); got != want {
		t.Errorf("got %d ticks, want a dotted eighth (%d)", got, want)
	}
	press(t, e, ebiten.KeyT)
	if got, want := e.doc.Duration(), score.Triplet(score.Eighth); got != want {
		t.Errorf("got %d ticks, want an eighth triplet (%d)", got, want)
	}
	press(t, e, ebiten.KeyBracketLeft)
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

	e := newTestEditor()
	press(t, e, ebiten.KeyDigit0)
	press(t, e, ebiten.KeyRight)
	press(t, e, ebiten.KeyDigit0)
	press(t, e, ebiten.KeyRight)
	press(t, e, ebiten.KeyDigit0)
	press(t, e, ebiten.KeyRight)
	press(t, e, ebiten.KeyDigit0)
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

	e.save()
	if len(asked) != 1 {
		t.Errorf("a second save opened another dialog (%d total)", len(asked))
	}

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
	e.OfferSavePath("")
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

func TestEditorShowErrorLandsOnTheStatusLine(t *testing.T) {
	e := newTestEditor()
	e.ShowError("cannot practise piece.gtab: no audio")
	msg, isErr := e.message()
	if msg != "cannot practise piece.gtab: no audio" || !isErr {
		t.Errorf("message() = %q (error=%v), want the shown error", msg, isErr)
	}

	e.frame += edMsgFrames
	if msg, _ := e.message(); msg != "" {
		t.Errorf("the message still reads %q after its hold expired", msg)
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

	setTextLine(t, e, "\\tempo", "\\tempo 96")
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

func TestEditorClickMapsThroughTheClampedScroll(t *testing.T) {
	e := newTestEditor()

	e.scroll = 1e9
	strTop := edGridTop + edSysPadTop
	if !e.clickGrid(edGridX+10, strTop+2*edStringGap) {
		t.Fatal("a click on the first system went dead: it mapped through the unclamped scroll")
	}
	if c := e.doc.Cursor(); c.Bar != 0 || c.Str != 3 {
		t.Errorf("the click landed on bar %d string %d, want bar 0 string 3", c.Bar, c.Str)
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

	for i := 1; i < len(score.NamedTunings); i++ {
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
	e.entry.feed([]rune("7/8"))
	e.commitEntry()
	if e.entry != nil {
		t.Fatalf("the entry stayed open: %v", e.msg)
	}
	if bar := e.doc.Bar(); bar.Num != 7 || bar.Den != 8 {
		t.Errorf("the bar is %d/%d, want 7/8", bar.Num, bar.Den)
	}
}

func TestEditorEntryEnterOnTheSeedChangesNothing(t *testing.T) {
	e := newTestEditor()
	if err := e.doc.SetTempo(120.3); err != nil {
		t.Fatal(err)
	}
	e.doc.MarkSaved()

	e.openEntry(edEntryTempo)
	e.commitEntry()
	if e.entry != nil {
		t.Fatal("enter on the seeded tempo did not close the entry")
	}
	if got := e.doc.TempoAtCursor(); got < 120.29 || got > 120.31 {
		t.Errorf("enter on the seeded tempo changed it to %g, want 120.3 untouched", got)
	}

	e.openEntry(edEntryTitle)
	e.commitEntry()
	if e.entry != nil {
		t.Fatal("enter on the seeded title did not close the entry")
	}
	if e.doc.Dirty() {
		t.Error("enter on seeded entries marked the piece dirty; nothing was edited")
	}
}

func TestEditorEntryKeepsBadInput(t *testing.T) {
	e := newTestEditor()
	e.openEntry(edEntryTempo)
	e.entry.feed([]rune("0"))
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

func editorButtons(e *Editor) []edButton {
	var out []edButton
	for _, g := range e.toolbarGroups() {
		out = append(out, g.buttons...)
	}
	out = append(out, e.pieceButtons()...)
	return append(out, e.fileButtons()...)
}

func editorButton(t *testing.T, e *Editor, id string) edButton {
	t.Helper()
	for _, b := range editorButtons(e) {
		if b.id == id {
			return b
		}
	}
	t.Fatalf("no toolbar control with id %q", id)
	return edButton{}
}

func TestEditorToolbarButtonsMatchTheKeys(t *testing.T) {

	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)

	editorButton(t, e, "hammer").act()
	if n, _ := e.doc.NoteAt(e.doc.Cursor().Str); n.Tech&score.TechHammer == 0 {
		t.Error("the hammer-on control did not set the hammer-on")
	}
	if !editorButton(t, e, "hammer").on {
		t.Error("the hammer-on control does not light up for a note that has one")
	}
	editorButton(t, e, "addbar").act()
	if got := e.doc.BarCount(); got != 5 {
		t.Errorf("the add-bar control left %d bars, want 5", got)
	}
}

func TestEditorToolbarCarriesNoFormatLetters(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)
	for _, b := range editorButtons(e) {
		if b.name == "" {
			t.Errorf("control %q has no name for its tooltip", b.id)
		}
		if len([]rune(b.name)) <= 2 {
			t.Errorf("control %q is named %q, which is a symbol rather than a word", b.id, b.name)
		}
		if n := len([]rune(b.label)); n == 1 {
			t.Errorf("control %q is labelled with the single character %q", b.id, b.label)
		}
		if strings.Contains(b.label, "1/") {
			t.Errorf("control %q still labels a note value as the fraction %q", b.id, b.label)
		}
	}
}

func TestEditorNoteValueIsPickedDirectly(t *testing.T) {
	e := newTestEditor()
	values := []struct {
		id    string
		ticks int64
	}{
		{"value3840", score.Whole}, {"value1920", score.Half}, {"value960", score.Quarter},
		{"value480", score.Eighth}, {"value240", score.Sixteenth}, {"value120", score.ThirtySec},
	}
	for _, v := range values {
		editorButton(t, e, v.id).act()
		if got := e.doc.Duration(); got != v.ticks {
			t.Errorf("%s selected %d ticks, want %d", v.id, got, v.ticks)
		}
		lit := 0
		for _, b := range editorButtons(e) {
			if strings.HasPrefix(b.id, "value") && b.on {
				lit++
				if b.id != v.id {
					t.Errorf("%s is lit while %s is selected", b.id, v.id)
				}
			}
		}
		if lit != 1 {
			t.Errorf("%d note values are lit, want exactly 1", lit)
		}
	}
}

func TestEditorDotAndTripletKeepTheChosenValue(t *testing.T) {
	e := newTestEditor()
	editorButton(t, e, "value480").act()
	editorButton(t, e, "dot").act()
	if got, want := e.doc.Duration(), score.Dotted(score.Eighth); got != want {
		t.Errorf("got %d ticks, want a dotted eighth (%d)", got, want)
	}
	if !editorButton(t, e, "value480").on {
		t.Error("dotting an eighth stopped the eighth being the selected value")
	}
	if !editorButton(t, e, "dot").on {
		t.Error("the dot control is not lit after dotting")
	}
	editorButton(t, e, "triplet").act()
	if got, want := e.doc.Duration(), score.Triplet(score.Eighth); got != want {
		t.Errorf("got %d ticks, want an eighth triplet (%d)", got, want)
	}
	if editorButton(t, e, "dot").on {
		t.Error("the dot is still lit after choosing a triplet; they are exclusive")
	}
}

func TestEditorNoteControlsAreOffWithoutANote(t *testing.T) {
	e := newTestEditor()
	for _, id := range []string{"tie", "hammer", "pull", "slide", "bend", "vibrato", "dead"} {
		if !editorButton(t, e, id).disabled {
			t.Errorf("the %q control is live with no note under the cursor", id)
		}
	}

	if editorButton(t, e, "value480").disabled {
		t.Error("the note-value picker is disabled with no note under the cursor")
	}
}

func TestEditorUndoRedoAreOnTheToolbar(t *testing.T) {
	e := newTestEditor()
	if !editorButton(t, e, "undo").disabled || !editorButton(t, e, "redo").disabled {
		t.Error("undo and redo are live on a piece with no history")
	}
	press(t, e, ebiten.KeyDigit7)
	if editorButton(t, e, "undo").disabled {
		t.Fatal("undo is disabled after an edit")
	}
	editorButton(t, e, "undo").act()
	if _, ok := e.doc.NoteAt(e.doc.Cursor().Str); ok {
		t.Error("the undo control did not take the note back")
	}
	if editorButton(t, e, "redo").disabled {
		t.Fatal("redo is disabled after an undo")
	}
	editorButton(t, e, "redo").act()
	if _, ok := e.doc.NoteAt(e.doc.Cursor().Str); !ok {
		t.Error("the redo control did not put the note back")
	}
}

func TestEditorHotspotsAreInsideTheWindow(t *testing.T) {
	e := newTestEditor()
	if err := e.doc.AddTrack("Rhythm", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.doc.AddTrack("Bass", nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range e.hotspots() {
		if h.r.x < 0 || h.r.y < 0 || h.r.x+h.r.w > screenW || h.r.y+h.r.h > screenH {
			t.Errorf("a control sits at %+v, outside the %dx%d window", h.r, screenW, screenH)
		}
	}
}

func TestEditorOpenAndSaveAnImportedPiece(t *testing.T) {

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

	e := newTestEditor()
	press(t, e, ebiten.KeyDigit5)
	e.toggleText()
	if e.text == nil {
		t.Fatal("the text view did not open")
	}
	for _, g := range e.toolbarGroups() {
		for _, b := range g.buttons {
			if !b.disabled {
				t.Errorf("the %q control is live while the text is showing", b.id)
			}
		}
	}
	for _, b := range e.pieceButtons() {
		if !b.disabled {
			t.Errorf("the %q piece control is live while the text is showing", b.id)
		}
	}

	for _, b := range e.fileButtons() {
		if b.id != "practice" && b.disabled {
			t.Errorf("the %q file control is greyed while the text is showing", b.id)
		}
	}

	for _, h := range e.hotspots() {
		if h.r.y < edGridTop && h.r.x < screenW/2 {
			t.Errorf("a left-hand control at %+v is still clickable in text mode", h.r)
		}
	}
}

func TestEditorHelpTableFitsItsCard(t *testing.T) {

	e := newTestEditor()
	rows := e.editorBindings()
	f := helpLayout(rows, editorHelpFootnote)
	card := helpCard()
	if bottom := card.y + card.h; f.bottom > bottom-6 {
		t.Errorf("the editor's %d bindings end at %.0f; the card ends at %.0f",
			len(rows), f.bottom, bottom)
	}
}

func TestEveryHelpTableFitsItsCard(t *testing.T) {
	e := newTestEditor()
	app := newApp(t, 4)
	brw := NewBrowser(NewShell(Services{Prefs: &settingsFakePrefs{}}, nil))
	set, _ := newSettingsFixture(t, newSettingsAudio())
	for _, tc := range []struct {
		name string
		rows []helpBinding
		foot string
	}{
		{"editor", e.editorBindings(), editorHelpFootnote},
		{"editor text view", e.textBindings(), editorHelpFootnote},
		{"practice", app.helpRows(), practiceHelpFootnote},
		{"start screen", brw.browserBindings(), browserHelpFootnote},
		{"settings", set.settingsBindings(), ""},
	} {
		f := helpLayout(tc.rows, tc.foot)
		if bottom := helpCard().y + helpCard().h; f.bottom > bottom-6 {
			t.Errorf("the %s table ends at %.0f; the card ends at %.0f", tc.name, f.bottom, bottom)
		}
		if tc.foot != "" && f.footY >= f.dismissY {
			t.Errorf("the %s footnote is at %.0f, on or under the dismiss line at %.0f",
				tc.name, f.footY, f.dismissY)
		}
	}
}

func TestEditorSpaceAdvancesAndGrowsThePiece(t *testing.T) {
	e := newTestEditor()
	press(t, e, ebiten.KeyDigit0)
	before := e.doc.Cursor()
	press(t, e, ebiten.KeySpace)
	if got := e.doc.Cursor(); got.Beat == before.Beat && got.Bar == before.Bar {
		t.Fatalf("space left the cursor at %+v", got)
	}

	e.doc.GoToEnd()
	bars := e.doc.BarCount()
	press(t, e, ebiten.KeySpace)
	if got := e.doc.BarCount(); got != bars+1 {
		t.Errorf("space at the end left %d bars, want %d", got, bars+1)
	}
	if got := e.doc.Cursor().Bar; got != bars {
		t.Errorf("the cursor is in bar %d, want the new one (%d)", got, bars)
	}

	e.doc.GoToEnd()
	bars = e.doc.BarCount()
	press(t, e, ebiten.KeyRight)
	if got := e.doc.BarCount(); got != bars {
		t.Errorf("the right arrow added a bar (%d, was %d)", got, bars)
	}
}

func TestEditorStringLabelsIgnoreTheCapo(t *testing.T) {

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

	e := newTestEditor()
	e.doc.GoTo(edit.Cursor{Bar: 0, Beat: 0, Str: 5})
	if got := e.cursorPitch(); got != "A string" {
		t.Errorf("with no note the cursor reads %q, want the string named", got)
	}
	if err := e.doc.SetFret(0); err != nil {
		t.Fatal(err)
	}
	if got := e.cursorPitch(); !strings.HasSuffix(got, "sounding A2") {
		t.Errorf("open A reads %q, want it to sound A2", got)
	}
	if err := e.doc.SetCapo(2); err != nil {
		t.Fatal(err)
	}
	if got := e.cursorPitch(); !strings.HasSuffix(got, "sounding B2") {
		t.Errorf("open A at capo 2 reads %q, want it to sound B2", got)
	}
}

func TestEditorRhythmFlags(t *testing.T) {

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

	press(t, e, ebiten.KeyDelete)
	if !e.isEmpty() {
		t.Error("clearing the only note did not leave the piece empty")
	}
}

func setTextLine(t *testing.T, e *Editor, prefix, replacement string) {
	t.Helper()
	if e.text == nil {
		t.Fatal("the text view is not open")
	}
	for i, line := range e.text.lines {
		if strings.HasPrefix(string(line), prefix) {
			e.text.lines[i] = []rune(replacement)
			e.text.reparse()
			return
		}
	}
	t.Fatalf("no line starts with %q", prefix)
}

func TestEditorTextViewSaveButtonSavesTheTextOnScreen(t *testing.T) {

	e := newTestEditor()
	e.path = filepath.Join(t.TempDir(), "piece.gtab")
	press(t, e, ebiten.KeyDigit5)
	e.toggleText()
	setTextLine(t, e, "\\tempo", "\\tempo 96")
	editorButton(t, e, "save").act()
	if e.text != nil {
		t.Fatalf("saving did not return to the notation: %v", e.msg)
	}
	src, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	if !strings.Contains(string(src), "\\tempo 96") {
		t.Errorf("the saved file is missing the text edit:\n%s", src)
	}
	if got := e.doc.TempoAtCursor(); got < 95.9 || got > 96.1 {
		t.Errorf("the document's tempo is %g after the save, want the text's 96", got)
	}
}

func TestEditorTextViewPracticeButtonPractisesTheTextOnScreen(t *testing.T) {

	e := newTestEditor()
	e.path = filepath.Join(t.TempDir(), "piece.gtab")
	var practised []string
	e.SetPractice(func(p string) { practised = append(practised, p) })
	e.toggleText()
	setTextLine(t, e, "\\tempo", "\\tempo 96")
	editorButton(t, e, "practice").act()
	if e.text != nil {
		t.Fatalf("practising did not return to the notation: %v", e.msg)
	}
	if len(practised) != 1 || practised[0] != e.path {
		t.Fatalf("practice opened %v, want [%s]", practised, e.path)
	}
	src, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatalf("the piece was not saved before practising: %v", err)
	}
	if !strings.Contains(string(src), "\\tempo 96") {
		t.Error("the file opened for practice is missing the text edit")
	}
}

func TestEditorTextViewSaveButtonKeepsBrokenTextOpen(t *testing.T) {
	e := newTestEditor()
	e.path = filepath.Join(t.TempDir(), "piece.gtab")
	e.toggleText()
	e.text.lines = append(e.text.lines, []rune("this is not a piece"))
	e.text.reparse()
	editorButton(t, e, "save").act()
	if e.text == nil {
		t.Fatal("broken text closed the view, throwing the typing away")
	}
	if _, err := os.Stat(e.path); err == nil {
		t.Error("a file was written from text that does not parse")
	}
	if msg, isErr := e.message(); !isErr || msg == "" {
		t.Errorf("got %q (error=%v), want the parser's complaint", msg, isErr)
	}
}

func TestEditorOpensBrokenTextForRepair(t *testing.T) {

	src := "\\tempo nope\n0.6.1 |\n"
	path := filepath.Join(t.TempDir(), "broken.gtab")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEditorForText(nil, []byte(src), path)
	if e.text == nil {
		t.Fatal("the editor did not open in the text view")
	}
	if e.text.ok {
		t.Fatal("the pane believes the broken fixture parses; the test proves nothing")
	}
	if e.path != path {
		t.Errorf("the editor's save target is %q, want the broken file %q", e.path, path)
	}
	if got := e.text.text(); got != src {
		t.Errorf("the pane holds %q, want the file's own text %q", got, src)
	}

	if err := e.escapeText(); err != errQuit {
		t.Errorf("escape from the untouched broken text returned %v, want errQuit", err)
	}
	if e.text == nil {
		t.Error("walking away must not pretend the text was applied")
	}

	e.text.insertRune('x')
	e.text.reparse()
	if err := e.escapeText(); err != nil {
		t.Errorf("the first escape from edited broken text returned %v, want a refusal (nil)", err)
	}
	if msg, isErr := e.message(); !isErr || !strings.Contains(msg, "esc again") {
		t.Errorf("the refusal says %q (error=%v), want it to name the second-escape way out", msg, isErr)
	}
	if err := e.escapeText(); err != errQuit {
		t.Errorf("the second escape returned %v, want errQuit", err)
	}

	e.text = newGtabPaneFromSource([]byte("\\tempo 100\n0.6.1 |\n"))
	e.applyTextThen(func() { e.save() })
	if e.text != nil {
		t.Fatal("repaired text did not return to the notation")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the repaired file back: %v", err)
	}
	if !strings.Contains(string(b), "\\tempo 100") {
		t.Errorf("the repaired file holds %q, want the fixed tempo in it", b)
	}
}

func TestEditorGreyedControlsInTextModeSayWhy(t *testing.T) {

	e := newTestEditor()
	e.toggleText()
	for _, g := range e.toolbarGroups() {
		for _, b := range g.buttons {
			if !b.disabled || b.why != edTextViewWhy {
				t.Errorf("control %q is disabled=%v with why=%q, want %q",
					b.id, b.disabled, b.why, edTextViewWhy)
			}
		}
	}
	for _, b := range e.pieceButtons() {
		if !b.disabled || b.why != edTextViewWhy {
			t.Errorf("piece control %q is disabled=%v with why=%q, want %q",
				b.id, b.disabled, b.why, edTextViewWhy)
		}
	}
}

func TestEditorShiftPCarriesThroughTheFirstSaveDialog(t *testing.T) {

	e := newTestEditor()
	var practised []string
	e.SetPractice(func(p string) { practised = append(practised, p) })
	e.SetSaveDialog(func(string) {})
	press(t, e, ebiten.KeyDigit5)
	e.saveAndPractice()
	if len(practised) != 0 {
		t.Fatal("practice started before the piece had a file")
	}
	if !e.practicePending {
		t.Fatal("the practice intent was not remembered across the dialog")
	}
	e.OfferSavePath(filepath.Join(t.TempDir(), "riff"))
	e.drainDialog()
	if len(practised) != 1 || practised[0] != e.path {
		t.Fatalf("after naming the file, practice opened %v, want [%s]", practised, e.path)
	}
	if e.practicePending {
		t.Error("the practice intent was not consumed")
	}
}

func TestEditorDialogAnswerAppliesTheOnScreenText(t *testing.T) {

	e := newTestEditor()
	e.SetSaveDialog(func(string) {})
	press(t, e, ebiten.KeyDigit5)
	e.save()
	if !e.dialogBusy {
		t.Fatal("the save did not go through the dialog; the test proves nothing")
	}
	e.toggleText()
	e.text = newGtabPaneFromSource([]byte("\\tempo 77\n0.6.1 |\n"))
	e.OfferSavePath(filepath.Join(t.TempDir(), "riff"))
	e.drainDialog()
	b, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatalf("reading the saved file: %v", err)
	}
	if !strings.Contains(string(b), "\\tempo 77") {
		t.Errorf("the file holds %q, want the on-screen text's tempo in it", b)
	}

	e2 := newTestEditor()
	var practised []string
	e2.SetPractice(func(p string) { practised = append(practised, p) })
	e2.SetSaveDialog(func(string) {})
	press(t, e2, ebiten.KeyDigit5)
	e2.saveAndPractice()
	e2.toggleText()
	e2.text = newGtabPaneFromSource([]byte("this is not a piece"))
	target := filepath.Join(t.TempDir(), "broken")
	e2.OfferSavePath(target)
	e2.drainDialog()
	if _, err := os.Stat(target + ".gtab"); err == nil {
		t.Error("a file was written from text that does not parse")
	}
	if len(practised) != 0 || e2.practicePending {
		t.Errorf("practice fired (%v) or stayed pending (%v) off an aborted save", practised, e2.practicePending)
	}
	if e2.text == nil {
		t.Error("the aborted save closed the text view, throwing the typing away")
	}
	if msg, isErr := e2.message(); !isErr || !strings.Contains(msg, "not saved") {
		t.Errorf("got %q (error=%v), want a 'not saved' report", msg, isErr)
	}
}

func TestEditorOneDialogAnswerCannotLeaveAndPractise(t *testing.T) {

	e := newTestEditor()
	var practised []string
	e.SetPractice(func(p string) { practised = append(practised, p) })
	e.SetSaveDialog(func(string) {})
	press(t, e, ebiten.KeyDigit5)
	e.saveAndPractice()
	e.leaving = true
	e.OfferSavePath(filepath.Join(t.TempDir(), "riff"))
	e.drainDialog()
	if len(practised) != 0 {
		t.Errorf("one answer both left the editor and opened practice: %v", practised)
	}
	if e.leaving || e.practicePending {
		t.Errorf("intents survived the drain: leaving=%v practicePending=%v", e.leaving, e.practicePending)
	}
}

func TestEditorEscapeIsInertWhileTheDialogFloats(t *testing.T) {

	e := newTestEditor()
	e.SetSaveDialog(func(string) {})
	e.save()
	if !e.dialogBusy {
		t.Fatal("the save did not go through the dialog")
	}
	if err := e.leave(); err != nil {
		t.Errorf("leave under a floating dialog returned %v, want nil (inert)", err)
	}
}

func TestEditorCancelledSaveDialogDropsThePracticeIntent(t *testing.T) {
	e := newTestEditor()
	var practised []string
	e.SetPractice(func(p string) { practised = append(practised, p) })
	e.SetSaveDialog(func(string) {})
	press(t, e, ebiten.KeyDigit5)
	e.saveAndPractice()
	e.OfferSavePath("")
	e.drainDialog()
	if e.practicePending {
		t.Error("cancelling the dialog left the practice intent pending")
	}
	if len(practised) != 0 {
		t.Fatalf("a cancelled save still opened practice: %v", practised)
	}

	e.save()
	e.OfferSavePath(filepath.Join(t.TempDir(), "riff"))
	e.drainDialog()
	if len(practised) != 0 {
		t.Errorf("a plain save after the cancel opened practice: %v", practised)
	}
}

func TestEditorCapoChipShowsAndSetsTheCapo(t *testing.T) {

	e := newTestEditor()
	if got := editorButton(t, e, "capo").label; got != "capo" {
		t.Errorf("with no capo the chip reads %q, want just the word", got)
	}
	editorButton(t, e, "capo").act()
	if e.entry == nil || e.entry.kind != edEntryCapo {
		t.Fatal("the capo chip did not open the capo entry")
	}
	if e.entry.buf != "0" {
		t.Errorf("the entry is seeded with %q, want the capo in force (0)", e.entry.buf)
	}

	e.entry.feed([]rune("12"))
	if e.entry.buf != "12" {
		t.Fatalf("typing 1 2 over the seed leaves %q, want 12", e.entry.buf)
	}

	e.entry = nil
	if e.report(e.doc.SetTitle("My Riff")) {
		e.openEntry(edEntryTitle)
		e.entry.feed([]rune(" 2"))
		if e.entry.buf != "My Riff 2" {
			t.Errorf("typing into the title left %q, want the seed kept and appended to", e.entry.buf)
		}
	}
	e.openEntry(edEntryCapo)
	e.entry.feed([]rune("2"))
	e.commitEntry()
	if e.entry != nil {
		t.Fatalf("the entry stayed open: %v", e.msg)
	}
	if got := e.doc.Track().Capo; got != 2 {
		t.Errorf("the capo is at fret %d, want 2", got)
	}
	if got := editorButton(t, e, "capo").label; got != "capo 2" {
		t.Errorf("with a capo on the chip reads %q, want it to name the fret", got)
	}
}

func TestEditorCapoEntryKeepsBadInput(t *testing.T) {
	e := newTestEditor()
	e.openEntry(edEntryCapo)
	e.entry.feed([]rune("99"))
	e.commitEntry()
	if e.entry == nil {
		t.Fatal("a refused capo closed the entry and threw away what was typed")
	}
	if msg, isErr := e.message(); !isErr || msg == "" {
		t.Errorf("got %q (error=%v), want the refusal", msg, isErr)
	}
}

func TestGtabLegendCoversEveryDirective(t *testing.T) {

	for _, directive := range []string{
		`\title`, `\tempo`, `\time`, `\tuning`, `\capo`, `\track`, `\instrument`, `\backing`, `\program`,
	} {
		found := false
		for _, row := range gtLegend {
			if strings.HasPrefix(row.example, directive) {
				found = true
			}
		}
		if !found {
			t.Errorf("the legend has no row for %s", directive)
		}
	}
}

func TestGtabLegendFitsItsColumn(t *testing.T) {

	r := gtLegendRect()
	lines := gtLegendLayout(r)
	if len(lines) != len(gtLegend) {
		t.Fatalf("only %d of the legend's %d rows fit the column", len(lines), len(gtLegend))
	}
	footY := r.y + r.h - 18
	for _, l := range lines {
		if l.y+12 > footY {
			t.Errorf("the %q row at %.0f runs into the footnote line at %.0f",
				l.example+l.means, l.y, footY)
		}
	}
	for _, row := range gtLegend {
		if row.example == "" {
			continue
		}
		if w := textWSmall(row.example); 12+w+6 > gtLegendCol {
			t.Errorf("the example %q is %.0fpx wide and collides with its meaning", row.example, w)
		}
		if w := textWSmall(row.means); w > r.w-gtLegendCol-22 {
			t.Errorf("the meaning %q is %.0fpx wide and would be cut short", row.means, w)
		}
	}
}

func TestEditorHelpDescribesTheViewOnScreen(t *testing.T) {

	e := newTestEditor()
	if title, _ := e.helpTable(); title != "EDITOR KEYS" {
		t.Errorf("the grid's help is titled %q, want EDITOR KEYS", title)
	}
	e.toggleText()
	title, rows := e.helpTable()
	if title != "TEXT VIEW KEYS" {
		t.Errorf("the text view's help is titled %q, want TEXT VIEW KEYS", title)
	}
	for _, b := range rows {
		if strings.Contains(b.Desc, "hammer") || strings.HasPrefix(b.Keys, "0-") {
			t.Errorf("the text view's help teaches a grid key: %+v", b)
		}
	}
	if len(rows) == 0 || rows[0].Hint != "type to edit" {
		t.Error("the text view's help is not the text view's own table")
	}
}

func TestEditorFretRangeComesFromTheFormat(t *testing.T) {

	want := fmt.Sprintf("0-%d", textfmt.MaxFret)
	e := newTestEditor()
	found := false
	for _, b := range e.editorBindings() {
		if strings.HasPrefix(b.Keys, "0-") {
			found = true
			if b.Keys != want {
				t.Errorf("the help overlay says %q, want %q", b.Keys, want)
			}
		}
	}
	if !found {
		t.Error("the help overlay has no fret-range row")
	}
	lines, _ := firstStepsContent()
	if lines[0].key != want {
		t.Errorf("the first-steps guidance says %q, want %q", lines[0].key, want)
	}
}

func TestEditorShiftPIsDescribedAsPracticeEverywhere(t *testing.T) {

	e := newTestEditor()
	if got := editorButton(t, e, "practice").name; got != edPracticeWhat {
		t.Errorf("the toolbar tooltip says %q, want %q", got, edPracticeWhat)
	}
	for _, b := range e.editorBindings() {
		if b.Keys == "shift+P" && b.Desc != edPracticeWhat {
			t.Errorf("the help overlay says %q, want %q", b.Desc, edPracticeWhat)
		}
	}
	_, tail := firstStepsContent()
	if !strings.Contains(tail, "practice") || strings.Contains(tail, "plays it back") {
		t.Errorf("the first-steps tail still promises playback: %q", tail)
	}
}
