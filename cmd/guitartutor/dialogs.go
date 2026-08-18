package main

// The OS file dialogs. The application used to carry its own directory
// listing and picker screen; both are gone — choosing a file is what the
// operating system's dialog is for, with its search, its pins, and the
// muscle memory the user already has. zenity drives the native dialog
// without cgo: comdlg32 on Windows, osascript on macOS, the zenity/qarma
// binary on Linux (where a machine without one degrades to the error
// path, which the start screen reports inline).
//
// Every dialog blocks for as long as the user browses, so callers run
// these on their own goroutine and deliver the outcome through the
// UI-side mailboxes (Browser.OfferDialogResult, the settings picker's
// chosen callback), which the game loop drains.

import (
	"errors"
	"os"
	"strings"

	"github.com/ncruces/zenity"

	"github.com/S95F/guitarTutor/internal/ui"
)

// pieceFileFilters builds the dialog filter from the importer's own
// extension list, so the dialog can never offer a file the import path
// would reject on sight.
func pieceFileFilters() zenity.FileFilter {
	exts := ui.PieceExtensions()
	patterns := make([]string, len(exts))
	for i, e := range exts {
		patterns[i] = "*" + e
	}
	return zenity.FileFilter{
		Name:     "Pieces (" + strings.Join(exts, ", ") + ")",
		Patterns: patterns,
		CaseFold: true,
	}
}

// pickPieceFile shows the open dialog for a piece, rooted at startDir.
// It blocks; run it on a goroutine. A cancel returns ("", "") — not an
// error, because the user changing their mind is not a condition to
// report.
func pickPieceFile(startDir string) (path, errMsg string) {
	opts := []zenity.Option{
		zenity.Title("Open a piece"),
		pieceFileFilters(),
	}
	if startDir != "" {
		// Filename with a trailing separator sets the starting directory
		// without proposing a file name. The separator must be the
		// PLATFORM's: a hardcoded backslash is an ordinary character on
		// macOS and Linux, and rooted the dialog in the parent directory
		// with a junk proposed name there (verification follow-up).
		opts = append(opts, zenity.Filename(startDir+string(os.PathSeparator)))
	}
	p, err := zenity.SelectFile(opts...)
	switch {
	case errors.Is(err, zenity.ErrCanceled):
		return "", ""
	case err != nil:
		return "", err.Error()
	}
	return p, ""
}

// pickSavePath shows the save dialog for a piece the editor is writing,
// proposing suggest as the name. Blocking, like the others; a cancel and
// a failure both come back empty, and the editor's busy guard re-arms on
// either. The overwrite prompt is the operating system's, which is the
// one the user already knows.
func pickSavePath(suggest string) string {
	opts := []zenity.Option{
		zenity.Title("Save the piece"),
		zenity.ConfirmOverwrite(),
		zenity.FileFilter{Name: "guitarTutor tab (.gtab)", Patterns: []string{"*.gtab"}, CaseFold: true},
	}
	if suggest != "" {
		opts = append(opts, zenity.Filename(suggest))
	}
	p, err := zenity.SelectFileSave(opts...)
	if err != nil {
		return ""
	}
	return p
}

// pickSoundFont shows the open dialog for a SoundFont. Blocking, like
// pickPieceFile; a cancel or a failure both come back empty — the
// settings row simply keeps its current value, which is the only sane
// outcome for either.
func pickSoundFont() string {
	p, err := zenity.SelectFile(
		zenity.Title("Choose a SoundFont"),
		zenity.FileFilter{Name: "SoundFonts (.sf2)", Patterns: []string{"*.sf2"}, CaseFold: true},
	)
	if err != nil {
		return ""
	}
	return p
}
