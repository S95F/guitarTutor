package appconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAddRecentOrdering(t *testing.T) {
	dir := t.TempDir()
	p := func(name string) string { return filepath.Join(dir, name) }

	var c Config
	c.AddRecent(p("a.gp"))
	c.AddRecent(p("b.gp"))
	c.AddRecent(p("c.gp"))
	want := []string{p("c.gp"), p("b.gp"), p("a.gp")}
	if !reflect.DeepEqual(c.Recents, want) {
		t.Fatalf("after three adds Recents =\n %v\nwant %v", c.Recents, want)
	}

	// Re-opening a listed piece moves it to the front rather than
	// duplicating it: the list stays a most-recent-first history.
	c.AddRecent(p("a.gp"))
	want = []string{p("a.gp"), p("c.gp"), p("b.gp")}
	if !reflect.DeepEqual(c.Recents, want) {
		t.Errorf("after re-adding a.gp Recents =\n %v\nwant %v", c.Recents, want)
	}
}

func TestAddRecentIgnoresBlankPaths(t *testing.T) {
	var c Config
	c.AddRecent("")
	c.AddRecent("   ")
	if len(c.Recents) != 0 {
		t.Errorf("blank paths recorded: %v", c.Recents)
	}
}

func TestAddRecentCollapsesSpellingsOfOnePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// Resolved working directory: on systems where the temp dir is behind
	// a symlink, Getwd is what filepath.Abs will agree with.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	var c Config
	c.AddRecent("song.gp")                                 // relative
	c.AddRecent(filepath.Join(wd, "sub", "..", "song.gp")) // absolute, uncleaned
	if runtime.GOOS == "windows" {
		c.AddRecent(strings.ReplaceAll(filepath.Join(wd, "song.gp"), `\`, "/")) // forward slashes
	}
	if len(c.Recents) != 1 {
		t.Fatalf("one file reached several ways gave %d entries: %v", len(c.Recents), c.Recents)
	}
	if want := filepath.Join(wd, "song.gp"); c.Recents[0] != want {
		t.Errorf("Recents[0] = %q, want the normalized absolute path %q", c.Recents[0], want)
	}
}

func TestAddRecentCaseSensitivityFollowsPlatform(t *testing.T) {
	dir := t.TempDir()
	lower := filepath.Join(dir, "song.gp")
	upper := filepath.Join(dir, "SONG.GP")

	var c Config
	c.AddRecent(lower)
	c.AddRecent(upper)

	if runtime.GOOS == "windows" {
		// One file on a case-insensitive file system: one entry, spelled
		// the way it was most recently opened.
		if !reflect.DeepEqual(c.Recents, []string{upper}) {
			t.Errorf("Recents = %v, want [%q] (Windows folds case)", c.Recents, upper)
		}
		return
	}
	if !reflect.DeepEqual(c.Recents, []string{upper, lower}) {
		t.Errorf("Recents = %v, want [%q %q] (case-sensitive elsewhere)", c.Recents, upper, lower)
	}
}

func TestAddRecentEnforcesCap(t *testing.T) {
	dir := t.TempDir()
	var c Config
	const extra = 5
	for i := 0; i < MaxRecents+extra; i++ {
		c.AddRecent(filepath.Join(dir, fmt.Sprintf("song%02d.gp", i)))
	}
	if len(c.Recents) != MaxRecents {
		t.Fatalf("len(Recents) = %d, want the cap %d", len(c.Recents), MaxRecents)
	}
	newest := filepath.Join(dir, fmt.Sprintf("song%02d.gp", MaxRecents+extra-1))
	if c.Recents[0] != newest {
		t.Errorf("Recents[0] = %q, want the newest %q", c.Recents[0], newest)
	}
	// The oldest additions fell off the back, not the front.
	oldest := filepath.Join(dir, "song00.gp")
	for _, r := range c.Recents {
		if r == oldest {
			t.Errorf("oldest entry %q survived the cap: %v", oldest, c.Recents)
		}
	}
	wantLast := filepath.Join(dir, fmt.Sprintf("song%02d.gp", extra))
	if got := c.Recents[len(c.Recents)-1]; got != wantLast {
		t.Errorf("last entry = %q, want %q", got, wantLast)
	}
}

func TestAddRecentDoesNotMutateSharedSlice(t *testing.T) {
	dir := t.TempDir()
	shared := []string{filepath.Join(dir, "a.gp"), filepath.Join(dir, "b.gp")}
	snapshot := append([]string(nil), shared...)

	c := Config{Recents: shared}
	c.AddRecent(filepath.Join(dir, "c.gp"))
	if !reflect.DeepEqual(shared, snapshot) {
		t.Errorf("AddRecent wrote through the caller's slice: %v, want %v", shared, snapshot)
	}
}

func TestForgetRecent(t *testing.T) {
	dir := t.TempDir()
	p := func(name string) string { return filepath.Join(dir, name) }

	var c Config
	c.AddRecent(p("a.gp"))
	c.AddRecent(p("b.gp"))
	c.AddRecent(p("c.gp"))

	// Forgetting a path that was never listed changes nothing.
	before := append([]string(nil), c.Recents...)
	c.ForgetRecent(p("nope.gp"))
	if !reflect.DeepEqual(c.Recents, before) {
		t.Errorf("forgetting an unlisted path changed Recents: %v, want %v", c.Recents, before)
	}

	// Forgetting matches the same way adding de-duplicates: uncleaned and
	// (on Windows) differently-cased spellings still find the entry.
	c.ForgetRecent(filepath.Join(dir, "sub", "..", "b.gp"))
	want := []string{p("c.gp"), p("a.gp")}
	if !reflect.DeepEqual(c.Recents, want) {
		t.Fatalf("after forgetting b.gp Recents = %v, want %v", c.Recents, want)
	}
	if runtime.GOOS == "windows" {
		c.ForgetRecent(strings.ToUpper(p("c.gp")))
		if want := []string{p("a.gp")}; !reflect.DeepEqual(c.Recents, want) {
			t.Fatalf("case-folded forget left Recents = %v, want %v", c.Recents, want)
		}
	} else {
		c.ForgetRecent(p("c.gp"))
	}

	// Draining the list leaves nil, matching the zero Config.
	c.ForgetRecent(p("a.gp"))
	if c.Recents != nil {
		t.Errorf("emptied Recents = %v, want nil", c.Recents)
	}
	// And on an already-empty list it is still a no-op.
	c.ForgetRecent(p("a.gp"))
	c.ForgetRecent("")
	if c.Recents != nil {
		t.Errorf("Recents = %v after no-op forgets, want nil", c.Recents)
	}
}

func TestSanitizeRecents(t *testing.T) {
	dir := t.TempDir()
	p := func(name string) string { return filepath.Join(dir, name) }

	long := make([]string, 0, MaxRecents+3)
	for i := 0; i < MaxRecents+3; i++ {
		long = append(long, filepath.Join(dir, fmt.Sprintf("s%02d.gp", i)))
	}

	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays nil", nil, nil},
		{"blanks dropped", []string{"", "  ", p("a.gp")}, []string{p("a.gp")}},
		{"all blank becomes nil", []string{"", " "}, nil},
		{"duplicates collapse, first wins", []string{p("a.gp"), p("b.gp"), p("a.gp")}, []string{p("a.gp"), p("b.gp")}},
		{"over-long list is capped", long, long[:MaxRecents]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeRecents(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sanitizeRecents(%v) =\n %v\nwant %v", tt.in, got, tt.want)
			}
		})
	}
}
