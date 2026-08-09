// Package corpus locates the real-instrument test corpus: recorded guitar
// audio, and score files exported by Guitar Pro and MuseScore themselves.
//
// Everything guitarTutor's audio and importer code is validated against
// today is SYNTHESIZED — Karplus-Strong plucks for pitch, chroma and
// chord tests, and self-authored fixtures for the .gp and MusicXML
// importers. That has already flattered the project once: Phase 4 shipped
// a chord-verification claim measured on E-shaped voicings that produced
// false misses on a correctly played open G (see ROADMAP Phase 4). Real
// recordings are the standing answer to that class of mistake.
//
// The corpus lives under testdata/real/ and is NOT committed: the audio
// datasets are large and carry their own licences, and real-world tabs
// are copyrighted arrangements. Each developer supplies their own, and
// every test that needs it skips cleanly when it is missing, so a fresh
// clone still passes. See docs/TESTDATA.md for what to put where.
package corpus

import (
	"os"
	"path/filepath"
	"testing"
)

// Root is the corpus directory relative to the repository root.
const Root = "testdata/real"

// Category names the corpus subdirectories. Tests ask for one rather
// than hard-coding paths, so the layout can move without a sweep.
type Category string

const (
	// Notes holds single-note recordings: one file per note, ideally
	// spanning the fretboard, for pitch accuracy and octave-error tests.
	Notes Category = "notes"
	// Chords holds strummed chord recordings, named by shape (open-g,
	// am, c, e5...), for chord verification. The voicings that are
	// marginal in synthesis are exactly the ones worth recording.
	Chords Category = "chords"
	// Techniques holds palm mutes, bends, slides, hammer-ons and
	// pull-offs — the playing that the onset gate and the tracker's
	// hysteresis handle worst.
	Techniques Category = "techniques"
	// Scores holds .gp files exported by Guitar Pro and .musicxml/.mxl
	// exported by MuseScore, to validate the clean-room importers
	// against what the real applications actually emit.
	Scores Category = "scores"
)

// Dir returns the absolute path of a corpus category, given the path from
// the calling package back to the repository root (usually "../.." for an
// internal package). It does not check existence.
func Dir(repoRoot string, c Category) string {
	return filepath.Join(repoRoot, filepath.FromSlash(Root), string(c))
}

// Files lists the corpus files in a category with any of the given
// extensions (lower-case, with the dot; nil means every file). The result
// is sorted by filepath.Glob's ordering per extension, so it is stable.
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

// Require returns the corpus files for a category, or skips the test when
// the corpus is absent. This is the function tests should use: a clone
// without the corpus reports skips, never failures.
//
//	files := corpus.Require(t, "../..", corpus.Chords, ".wav", ".flac")
func Require(t *testing.T, repoRoot string, c Category, exts ...string) []string {
	t.Helper()
	files, err := Files(repoRoot, c, exts...)
	if err != nil || len(files) == 0 {
		t.Skipf("no %s corpus at %s — see docs/TESTDATA.md (this is a skip, not a failure)",
			c, Dir(repoRoot, c))
	}
	return files
}

// equalFold compares two ASCII extensions case-insensitively.
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
