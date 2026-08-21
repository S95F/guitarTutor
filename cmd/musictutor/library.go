package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/ui"
)

type pieceLibrary struct{}

func (pieceLibrary) Dir() string {
	dir, err := appconfig.PiecesDir()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return dir
}

func (pieceLibrary) Scan() ([]ui.PieceInfo, error) {
	dir, err := appconfig.PiecesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	out := make([]ui.PieceInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".gtab") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info := ui.PieceInfo{
			Path: path,
			Name: strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
		}
		if fi, err := e.Info(); err == nil {
			info.Modified = fi.ModTime()
		}
		sc, err := textfmt.ParseFile(path)
		if err != nil {

			info.Problem = textfmt.ProblemLine(err)
		} else {
			info.Title = sc.Title
			info.Summary = describePiece(sc)
		}
		out = append(out, info)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

func describePiece(sc *score.Score) string {
	bars, tracks := 0, len(sc.Tracks)
	for _, tr := range sc.Tracks {
		if n := len(tr.Bars); n > bars {
			bars = n
		}
	}
	m := sc.Meters.At(0)
	parts := []string{
		fmt.Sprintf("%d/%d", m.Num, m.Den),
		fmt.Sprintf("%.0f BPM", 60e6/float64(sc.Tempos.At(0))),
		plural(bars, "bar"),
	}
	if tracks > 1 {
		parts = append(parts, plural(tracks, "track"))
	}
	if len(sc.Tracks) > 0 {
		if name := instrumentName(sc.Tracks[0]); name != "" {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, " · ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func instrumentName(tr *score.Track) string {
	if tr.Wind != nil {
		return tr.Wind.Name
	}
	var parts []string
	if !tr.Tuning.Equal(score.StandardTuning) {
		parts = append(parts, score.TuningName(tr.Tuning))
	}
	if tr.Capo > 0 {
		parts = append(parts, fmt.Sprintf("capo %d", tr.Capo))
	}
	return strings.Join(parts, " · ")
}
