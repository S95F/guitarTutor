package ui

import (
	"os"

	"github.com/hajimehoshi/ebiten/v2"
	"path/filepath"
	"strings"
	"testing"
)

func newLibraryEditor(t *testing.T) (*Editor, string) {
	t.Helper()
	dir := t.TempDir()
	e := newTestEditor()
	e.SetLibraryDir(dir)
	return e, dir
}

func TestSaveWithNoPathOpensTheNameEntry(t *testing.T) {
	e, dir := newLibraryEditor(t)
	if e.save() {
		t.Fatal("save with no path reported success before any name was given")
	}
	if !e.saveEntryOpen() {
		t.Fatal("save with no path did not open the name entry")
	}
	if e.entry.buf != "untitled" {
		t.Errorf("the entry is seeded with %q, want the suggested name", e.entry.buf)
	}
	e.commitEntry()
	if e.entry != nil {
		t.Fatal("committing the seeded name did not close the entry")
	}
	want := filepath.Join(dir, "untitled.gtab")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the piece was not written to the library: %v", err)
	}
	if e.path != want || e.doc.Dirty() {
		t.Errorf("after saving: path %q dirty %v, want the library path and clean", e.path, e.doc.Dirty())
	}
}

func TestSaveEntryRefusesACollision(t *testing.T) {
	e, dir := newLibraryEditor(t)
	if err := os.WriteFile(filepath.Join(dir, "untitled.gtab"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.save()
	e.commitEntry()
	if !e.saveEntryOpen() {
		t.Fatal("a name collision closed the entry instead of asking for another name")
	}
	if msg, isErr := e.message(); !isErr || !strings.Contains(msg, "untitled") {
		t.Errorf("the collision message reads %q", msg)
	}
}

func TestLeavePromptSaveGoesThroughTheNameEntry(t *testing.T) {
	e, dir := newLibraryEditor(t)
	press(t, e, ebiten.KeyDigit5)
	e.leaving = true
	if err := e.applyLeaveChoice(edLeaveSave); err != nil {
		t.Fatalf("choosing save errored: %v", err)
	}
	if e.leaving || !e.saveEntryOpen() || !e.leaveAfterSave {
		t.Fatalf("leave-save state: leaving %v entry %v leaveAfterSave %v", e.leaving, e.saveEntryOpen(), e.leaveAfterSave)
	}
	e.commitEntry()
	if e.leaveAfterSave {
		t.Error("the save did not finish the leave it promised")
	}
	if _, err := os.Stat(filepath.Join(dir, "untitled.gtab")); err != nil {
		t.Errorf("the piece was not written on the way out: %v", err)
	}
}

func TestSaveEntryCancelClearsWhatWasPending(t *testing.T) {
	e, _ := newLibraryEditor(t)
	e.SetPractice(func(string) { t.Error("practice ran after the save was cancelled") })
	press(t, e, ebiten.KeyDigit5)
	e.saveAndPractice()
	if !e.saveEntryOpen() || !e.practicePending {
		t.Fatalf("saveAndPractice state: entry %v pending %v", e.saveEntryOpen(), e.practicePending)
	}
	e.closeEntry()
	if e.practicePending || e.leaveAfterSave || e.entry != nil {
		t.Error("cancelling the name entry left pending work armed")
	}
}

func TestPracticePendingRidesTheNameEntry(t *testing.T) {
	e, dir := newLibraryEditor(t)
	var practised string
	e.SetPractice(func(p string) { practised = p })
	press(t, e, ebiten.KeyDigit5)
	e.saveAndPractice()
	e.commitEntry()
	if want := filepath.Join(dir, "untitled.gtab"); practised != want {
		t.Errorf("practice opened %q, want the just-saved %q", practised, want)
	}
}

func TestSaveProblemLandsOnTheStatusLine(t *testing.T) {
	e := newTestEditor()
	e.dialogBusy = true
	e.OfferSaveProblem("file dialogs need the \"zenity\" program")
	e.drainDialog()
	if e.dialogBusy {
		t.Error("the problem did not release the dialog guard")
	}
	if msg, isErr := e.message(); !isErr || !strings.Contains(msg, "zenity") {
		t.Errorf("the problem message reads %q, err %v", msg, isErr)
	}
}

func TestAuditionFiresOnNoteEntry(t *testing.T) {
	var program, key int
	e := newTestEditor()
	e.SetAudition(func(p, k int) { program, key = p, k })
	press(t, e, ebiten.KeyDigit5)
	tr := e.doc.Track()
	n, _ := e.doc.NoteAt(e.doc.Cursor().Str)
	if key != tr.Pitch(n) || program != tr.Program {
		t.Errorf("guitar audition = (%d, %d), want (%d, %d)", program, key, tr.Program, tr.Pitch(n))
	}

	w := newTestWindEditor(t)
	w.SetAudition(func(p, k int) { program, key = p, k })
	press(t, w, ebiten.KeyD)
	wtr := w.doc.Track()
	wn, _ := w.doc.NoteAt(1)
	if key != wtr.Pitch(wn) || program != wtr.Program {
		t.Errorf("wind audition = (%d, %d), want (%d, %d)", program, key, wtr.Program, wtr.Pitch(wn))
	}
}
