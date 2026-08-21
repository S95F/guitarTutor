package corpus

import (
	"os"
	"path/filepath"
	"testing"
)

const Root = "testdata/real"

type Category string

const (
	Notes Category = "notes"

	Chords Category = "chords"

	Techniques Category = "techniques"

	Scores Category = "scores"

	WindTones Category = "wind/tones"

	WindArticulation Category = "wind/articulation"

	WindRooms Category = "wind/rooms"
)

func Dir(repoRoot string, c Category) string {
	return filepath.Join(repoRoot, filepath.FromSlash(Root), filepath.FromSlash(string(c)))
}

func Files(repoRoot string, c Category, exts ...string) ([]string, error) {
	dir := Dir(repoRoot, c)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if len(exts) == 0 {
			out = append(out, filepath.Join(dir, e.Name()))
			continue
		}
		ext := filepath.Ext(e.Name())
		for _, want := range exts {
			if equalFold(ext, want) {
				out = append(out, filepath.Join(dir, e.Name()))
				break
			}
		}
	}
	return out, nil
}

func Require(t *testing.T, repoRoot string, c Category, exts ...string) []string {
	t.Helper()
	files, err := Files(repoRoot, c, exts...)
	if err != nil || len(files) == 0 {
		t.Skipf("no %s corpus at %s — see docs/TESTDATA.md (this is a skip, not a failure)",
			c, Dir(repoRoot, c))
	}
	return files
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
