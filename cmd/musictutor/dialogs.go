package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/ncruces/zenity"

	"github.com/S95F/musicTutor/internal/ui"
)

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

func pickPieceFile(startDir string) (path, errMsg string) {
	opts := []zenity.Option{
		zenity.Title("Open a piece"),
		pieceFileFilters(),
	}
	if startDir != "" {

		opts = append(opts, zenity.Filename(startDir+string(os.PathSeparator)))
	}
	p, err := zenity.SelectFile(opts...)
	switch {
	case errors.Is(err, zenity.ErrCanceled):
		return "", ""
	case err != nil:
		return "", dialogProblem(err, "or drop the file anywhere on this window")
	}
	return p, ""
}

func pickSavePath(suggest string) (path, errMsg string) {
	opts := []zenity.Option{
		zenity.Title("Save the piece"),
		zenity.ConfirmOverwrite(),
		zenity.FileFilter{Name: "musicTutor tab (.gtab)", Patterns: []string{"*.gtab"}, CaseFold: true},
	}
	if suggest != "" {
		opts = append(opts, zenity.Filename(suggest))
	}
	p, err := zenity.SelectFileSave(opts...)
	switch {
	case errors.Is(err, zenity.ErrCanceled):
		return "", ""
	case err != nil:
		return "", dialogProblem(err, "or plain ctrl+S saves into your library without a dialog")
	}
	return p, ""
}

func dialogProblem(err error, instead string) string {
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
		return "file dialogs need the \"zenity\" program — install it (on Ubuntu: sudo apt install zenity), " + instead
	}
	return "the file dialog failed (" + err.Error() + ") — " + instead
}

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
