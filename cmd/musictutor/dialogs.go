package main

import (
	"errors"
	"os"
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
		return "", err.Error()
	}
	return p, ""
}

func pickSavePath(suggest string) string {
	opts := []zenity.Option{
		zenity.Title("Save the piece"),
		zenity.ConfirmOverwrite(),
		zenity.FileFilter{Name: "musicTutor tab (.gtab)", Patterns: []string{"*.gtab"}, CaseFold: true},
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
