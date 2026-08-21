package main

// The library: the pieces the application keeps in its own folder, and
// what it can say about each of them.
//
// This is the one list on the start screen that is a PLACE rather than a
// history, and the only one that describes the music rather than naming a
// file. It can, because every piece in it is in the format the app writes
// — so reading one costs a few microseconds and yields the title, the
// meter, the tempo and the size of the piece. That is what the start
// screen shows instead of "riff-2.gtab".
//
// Scanning touches the disk and parses every file in the folder, so the
// UI runs it off the game loop (see Browser.rescanLibrary). Nothing here
// caches: the folder is small, the parse is fast, and a cache that goes
// stale when a file is edited outside the app would be a worse answer
// than reading it again.

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

// pieceLibrary implements ui.Library over the application's own pieces
// directory. It holds no state: the folder is the state.
type pieceLibrary struct{}

// Dir is the folder the pieces live in. A configuration directory that
// cannot be located is reported as text rather than hidden — the pane
// draws this line, and "unavailable" with a reason is more use than a
// blank.
func (pieceLibrary) Dir() string {
	dir, err := appconfig.PiecesDir()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return dir
}

// Scan lists the pieces in the folder, newest first, describing each.
//
// A folder that does not exist yet is not an error: it is what a fresh
// install looks like, and the empty list the pane shows says so better
// than a message about a missing directory would.
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
			// Listed anyway, with the reason: a piece that will not parse
			// is exactly the one worth finding, and the editor's text view
			// is where it gets fixed.
			info.Problem = textfmt.ProblemLine(err)
		} else {
			info.Title = sc.Title
			info.Summary = describePiece(sc)
		}
		out = append(out, info)
	}
	// Newest first: the folder is a place, but the thing you want out of
	// it is almost always what you were last working on.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// describePiece is the one line the library shows under a piece's title:
// what the music is, in the terms its player would ask.
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

// plural renders a count with its noun.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// instrumentName says what the piece is played on, when that is worth a
// word. A wind track is named outright — "soprano sax" is the first thing
// its player wants to know. A fretted track is assumed to be a guitar, so
// only what departs from the default is said: the tuning when it is not
// standard, the capo when there is one. Saying "standard E" about every
// piece would be noise; saying "drop D" about the one piece that is in it
// is the whole point of a description.
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
