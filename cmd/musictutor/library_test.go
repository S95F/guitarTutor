package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// writePiece puts a .gtab file in the managed folder.
func writePiece(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLibraryScanDescribesPieces(t *testing.T) {
	root := t.TempDir()
	t.Setenv(appconfig.EnvConfigDir, root)
	dir, err := appconfig.EnsurePiecesDir()
	if err != nil {
		t.Fatal(err)
	}
	writePiece(t, dir, "riff.gtab", `\title My Riff
\tempo 96
\time 3/4
0.6.4 3.6.4 5.5.4 |
`)
	// Not a piece: the scanner has to ignore it rather than try to parse it.
	writePiece(t, dir, "notes.txt", "shopping list")

	pieces, err := pieceLibrary{}.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(pieces) != 1 {
		t.Fatalf("scanned %d pieces, want 1 (the .txt is not one)", len(pieces))
	}
	p := pieces[0]
	if p.Title != "My Riff" {
		t.Errorf("title = %q, want the piece's own", p.Title)
	}
	if p.Name != "riff" {
		t.Errorf("name = %q, want the file name without its extension", p.Name)
	}
	if p.Problem != "" {
		t.Errorf("a valid piece reported a problem: %q", p.Problem)
	}
	for _, want := range []string{"3/4", "96 BPM", "1 bar"} {
		if !strings.Contains(p.Summary, want) {
			t.Errorf("summary %q does not mention %q", p.Summary, want)
		}
	}
	if p.Modified.IsZero() {
		t.Error("the piece has no modification time, so the list cannot be sorted")
	}
}

func TestLibraryScanListsWhatWillNotParse(t *testing.T) {
	// A piece that will not parse is exactly the one worth finding, so it
	// is listed with the reason rather than dropped.
	root := t.TempDir()
	t.Setenv(appconfig.EnvConfigDir, root)
	dir, err := appconfig.EnsurePiecesDir()
	if err != nil {
		t.Fatal(err)
	}
	writePiece(t, dir, "broken.gtab", `\title Broken
\time 4/4
0.6.4 |
`)

	pieces, err := pieceLibrary{}.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(pieces) != 1 {
		t.Fatalf("scanned %d pieces, want the broken one listed", len(pieces))
	}
	if pieces[0].Problem == "" {
		t.Fatal("the broken piece reported no problem")
	}
	if strings.Contains(pieces[0].Problem, dir) {
		t.Errorf("the problem repeats the path (%q); the row above already carries it", pieces[0].Problem)
	}
	if !strings.HasPrefix(pieces[0].Problem, "line ") {
		t.Errorf("the problem reads %q, want it to start with the line", pieces[0].Problem)
	}
}

func TestLibraryScanOnAMissingFolderIsEmptyNotAnError(t *testing.T) {
	// A fresh install has no folder yet, and an empty list says that
	// better than an error about a missing directory.
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())
	pieces, err := pieceLibrary{}.Scan()
	if err != nil {
		t.Fatalf("Scan on a folder that is not there: %v", err)
	}
	if len(pieces) != 0 {
		t.Errorf("scanned %d pieces from a folder that does not exist", len(pieces))
	}
}

func TestLibraryScanIsNewestFirst(t *testing.T) {
	root := t.TempDir()
	t.Setenv(appconfig.EnvConfigDir, root)
	dir, err := appconfig.EnsurePiecesDir()
	if err != nil {
		t.Fatal(err)
	}
	const body = "\\tempo 120\n\\time 4/4\n0.6.1 |\n"
	old := writePiece(t, dir, "old.gtab", body)
	recent := writePiece(t, dir, "new.gtab", body)
	// Two files written in the same tick have the same timestamp, so the
	// order is set rather than assumed.
	past := mustStat(t, recent).ModTime().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	pieces, err := pieceLibrary{}.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 2 {
		t.Fatalf("scanned %d pieces, want 2", len(pieces))
	}
	if pieces[0].Name != "new" {
		t.Errorf("the list starts with %q, want the most recently written", pieces[0].Name)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func TestDescribePiece(t *testing.T) {
	parse := func(src string) *score.Score {
		t.Helper()
		sc, err := textfmt.Parse([]byte(src), "p")
		if err != nil {
			t.Fatalf("parsing the fixture: %v", err)
		}
		return sc
	}
	for _, tt := range []struct {
		name string
		src  string
		want []string
		not  []string
	}{
		{
			name: "plain",
			src:  "\\tempo 120\n\\time 4/4\n0.6.1 |\n0.6.1 |\n",
			want: []string{"4/4", "120 BPM", "2 bars"},
			// A single track is not worth saying, and standard tuning is
			// what almost every piece is in — noise, both of them.
			not: []string{"track", "standard"},
		},
		{
			name: "two tracks",
			src:  "\\tempo 90\n\\time 4/4\n0.6.1 |\n\\track Rhythm\n0.6.1 |\n",
			want: []string{"2 tracks"},
		},
		{
			name: "capo",
			src:  "\\tempo 90\n\\time 4/4\n\\capo 3\n0.6.1 |\n",
			want: []string{"capo 3"},
		},
		{
			// A tuning the app knows gets its name, not a shrug.
			name: "named tuning",
			src:  "\\tempo 90\n\\time 4/4\n\\tuning D2 A2 D3 G3 B3 E4\n0.6.1 |\n",
			want: []string{"drop D"},
			not:  []string{"altered tuning"},
		},
		{
			name: "unnamed tuning",
			src:  "\\tempo 90\n\\time 4/4\n\\tuning C2 A2 D3 G3 B3 E4\n0.6.1 |\n",
			want: []string{"altered tuning"},
		},
		{
			// A capo must not hide the tuning: both are why the piece
			// sounds the way it does.
			name: "capo over a named tuning",
			src:  "\\tempo 90\n\\time 4/4\n\\tuning D2 A2 D3 G3 B3 E4\n\\capo 2\n0.6.1 |\n",
			want: []string{"drop D · capo 2"},
		},
		{
			// A wind piece is named by its instrument — the first thing
			// its player wants to know — and never by tuning vocabulary.
			name: "soprano sax",
			src:  "\\tempo 96\n\\time 4/4\n\\instrument soprano sax\nD5.1 |\n",
			want: []string{"soprano sax"},
			not:  []string{"tuning", "capo"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := describePiece(parse(tt.src))
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("describePiece = %q, want it to mention %q", got, w)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(got, n) {
					t.Errorf("describePiece = %q, which should not mention %q", got, n)
				}
			}
		})
	}
}

func TestSuggestSavePathLandsInTheLibrary(t *testing.T) {
	root := t.TempDir()
	t.Setenv(appconfig.EnvConfigDir, root)
	o := &shellOpener{prefs: &shellPrefs{}}

	got := o.suggestSavePath("My Riff.gtab")
	dir, err := appconfig.PiecesDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "My Riff.gtab"); got != want {
		t.Errorf("suggestSavePath = %q, want %q", got, want)
	}
	// A piece that already has a home keeps it.
	elsewhere := filepath.Join(root, "elsewhere", "song.gtab")
	if got := o.suggestSavePath(elsewhere); got != elsewhere {
		t.Errorf("suggestSavePath moved an absolute path: %q", got)
	}
	if got := o.suggestSavePath(""); got != "" {
		t.Errorf("suggestSavePath invented a name: %q", got)
	}
}
