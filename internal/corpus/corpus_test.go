package corpus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirLayout(t *testing.T) {
	got := Dir("../..", Chords)
	want := filepath.Join("../..", filepath.FromSlash("testdata/real"), "chords")
	if got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
	// Nested categories are spelled with forward slashes; Dir must yield
	// the platform's own separators (filepath.Join, never string concat),
	// so wind/tones resolves on Windows too.
	got = Dir("../..", WindTones)
	want = filepath.Join("../..", "testdata", "real", "wind", "tones")
	if got != want {
		t.Errorf("Dir(WindTones) = %q, want %q", got, want)
	}
}

// TestFilesNestedCategory: a category with a path separator lists exactly
// like a flat one — the wind corpus is reached through the same two calls
// every guitar test uses.
func TestFilesNestedCategory(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root, WindTones)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"c5.wav", "g#4-scoop.flac", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Files(root, WindTones, ".wav", ".flac")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(got), got)
	}
}

func TestFilesFiltersByExtension(t *testing.T) {
	root := t.TempDir()
	dir := Dir(root, Notes)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"e2.wav", "a2.FLAC", "notes.txt", "readme.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Files(root, Notes, ".wav", ".flac")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(got), got)
	}
	// Extension matching is case-insensitive, and directories are skipped.
	var names []string
	for _, p := range got {
		names = append(names, filepath.Base(p))
	}
	if names[0] != "a2.FLAC" && names[1] != "a2.FLAC" {
		t.Errorf("case-insensitive match missed a2.FLAC: %v", names)
	}

	all, err := Files(root, Notes)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("no-extension filter returned %d files, want all 4: %v", len(all), all)
	}
}

func TestFilesMissingCorpus(t *testing.T) {
	if _, err := Files(t.TempDir(), Chords); err == nil {
		t.Error("Files on a missing corpus returned nil error; callers rely on it to decide to skip")
	}
}

// TestRequireSkipsWithoutCorpus documents the contract that matters most:
// a clone without the corpus must report skips, never failures. It runs
// Require in a subtest and asserts that subtest skipped.
func TestRequireSkipsWithoutCorpus(t *testing.T) {
	root := t.TempDir()
	var skipped bool
	t.Run("inner", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		Require(t, root, Chords, ".wav")
	})
	if !skipped {
		t.Error("Require did not skip on a missing corpus")
	}
}
