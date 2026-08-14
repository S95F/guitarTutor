package ui

// Browser tests. Ebitengine cannot open a window here, so every test
// drives the Browser's plain state methods — setDir, move, activate,
// handleKey, click, handleDrop — against a real directory tree built in
// t.TempDir(). Nothing in this file renders.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

// --- doubles -------------------------------------------------------------

// browserStubScreen is a Screen that is never run: the fake opener hands
// it back so OpenPiece has something to push.
type browserStubScreen struct{}

func (browserStubScreen) Update() error      { return nil }
func (browserStubScreen) Draw(*ebiten.Image) {}

// browserFakeOpener returns a canned result for every Open.
type browserFakeOpener struct {
	warns  []string
	err    error
	opened []string
	closed int
}

func (o *browserFakeOpener) Open(path string) (Screen, []string, error) {
	o.opened = append(o.opened, path)
	if o.err != nil {
		return nil, o.warns, o.err
	}
	return browserStubScreen{}, o.warns, nil
}

func (o *browserFakeOpener) CloseCurrent() { o.closed++ }

// browserFakePrefs is an in-memory Prefs. It deliberately does not
// implement browserRecentRemover, so the session-only forget path is
// what the tests exercise by default; browserRemovingPrefs adds it.
type browserFakePrefs struct {
	recents []string
	saves   int
}

func (p *browserFakePrefs) Recents() []string { return p.recents }
func (p *browserFakePrefs) AddRecent(path string) {
	out := []string{path}
	for _, r := range p.recents {
		if r != path {
			out = append(out, r)
		}
	}
	p.recents = out
}
func (p *browserFakePrefs) SoundFont() string         { return "" }
func (p *browserFakePrefs) SetSoundFont(string)       {}
func (p *browserFakePrefs) CountIn() int              { return 0 }
func (p *browserFakePrefs) SetCountIn(int)            {}
func (p *browserFakePrefs) Devices() (string, string) { return "", "" }
func (p *browserFakePrefs) SetDevices(string, string) {}
func (p *browserFakePrefs) Save() error               { p.saves++; return nil }

// browserRemovingPrefs is a Prefs that can also forget a recent
// permanently — the optional extension the browser probes for.
type browserRemovingPrefs struct{ browserFakePrefs }

func (p *browserRemovingPrefs) RemoveRecent(path string) {
	out := p.recents[:0]
	for _, r := range p.recents {
		if r != path {
			out = append(out, r)
		}
	}
	p.recents = out
}

// browserDropFS mimics the shape of the fs.FS ebiten.DroppedFiles
// returns on desktop: the root lists the dropped items by base name, and
// opening one yields an *os.File that reports the real path from Name().
type browserDropFS struct{ paths []string }

func (d browserDropFS) Open(name string) (fs.File, error) {
	if name == "." {
		return nil, errors.New("root file not needed by these tests")
	}
	for _, p := range d.paths {
		if filepath.Base(p) == name {
			return os.Open(p)
		}
	}
	return nil, fs.ErrNotExist
}

func (d browserDropFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, fs.ErrNotExist
	}
	var out []fs.DirEntry
	for _, p := range d.paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		out = append(out, fs.FileInfoToDirEntry(fi))
	}
	return out, nil
}

// browserNameOnlyEntry is a listed dropped item that cannot be opened:
// the platform knows its name and nothing else.
type browserNameOnlyEntry struct{ name string }

func (e browserNameOnlyEntry) Name() string               { return e.name }
func (e browserNameOnlyEntry) IsDir() bool                { return false }
func (e browserNameOnlyEntry) Type() fs.FileMode          { return 0 }
func (e browserNameOnlyEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrPermission }

// browserPartialDropFS is a drop the platform could only partly resolve.
// It reproduces what the desktop implementation does when it fails to
// stat one of the dropped items: the listing comes back the right length
// but with nil fs.DirEntry values at and after the failure, alongside an
// error. bad names are listed but refuse to open.
type browserPartialDropFS struct {
	paths    []string // real files, recoverable
	bad      []string // listed, but Open fails
	nilHoles int      // nil entries the platform left behind
	err      error    // ReadDir's error alongside the partial listing
}

func (d browserPartialDropFS) Open(name string) (fs.File, error) {
	for _, p := range d.paths {
		if filepath.Base(p) == name {
			return os.Open(p)
		}
	}
	return nil, fs.ErrPermission
}

func (d browserPartialDropFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, fs.ErrNotExist
	}
	var out []fs.DirEntry
	for _, p := range d.paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		out = append(out, fs.FileInfoToDirEntry(fi))
	}
	for _, n := range d.bad {
		out = append(out, browserNameOnlyEntry{name: n})
	}
	for i := 0; i < d.nilHoles; i++ {
		out = append(out, nil)
	}
	return out, d.err
}

// --- fixtures ------------------------------------------------------------

// browserTree writes an empty file at each relative path under root,
// creating parent directories as needed. A path ending in "/" makes a
// directory.
func browserTree(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", full, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

// browserFixture builds a Browser over a shell with the given doubles,
// pointed at dir.
func browserFixture(t *testing.T, pr Prefs, op Opener, dir string) *Browser {
	t.Helper()
	sh := NewShell(Services{Opener: op, Prefs: pr}, nil)
	b := NewBrowser(sh)
	b.setDir(dir)
	b.focus = browserPaneBrowse
	return b
}

// browserStatFixture builds a Browser whose recents probe is the given
// hook, installed before the first load so the probes NewBrowser would
// have made are counted too — which is why this repeats its two steps
// instead of calling it.
func browserStatFixture(t *testing.T, pr Prefs, op Opener, dir string,
	stat func(string) (fs.FileInfo, error)) *Browser {
	t.Helper()
	sh := NewShell(Services{Opener: op, Prefs: pr}, nil)
	b := &Browser{sh: sh, forgotten: map[string]bool{}, statFn: stat}
	b.reloadRecents()
	b.setDir(dir)
	return b
}

// browserPressed returns a handleKeys predicate for an exact set of keys
// held down on the same frame.
func browserPressed(keys ...ebiten.Key) func(ebiten.Key) bool {
	down := map[ebiten.Key]bool{}
	for _, k := range keys {
		down[k] = true
	}
	return func(k ebiten.Key) bool { return down[k] }
}

// browserNames lists a pane's entry names, for compact assertions.
func browserNames(es []browserEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.name
	}
	return out
}

func browserEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- tests ---------------------------------------------------------------

// TestBrowserListingFilters: the listing keeps sub-directories and the
// piece formats the app imports (case-insensitively), hides everything
// else, and sorts directories before files.
func TestBrowserListingFilters(t *testing.T) {
	dir := t.TempDir()
	browserTree(t, dir,
		"Zebra/", "albums/",
		"riff.gp", "SONG.MIDI", "tune.mid", "part.smf", "hand.gtab",
		"score.musicxml", "packed.mxl", "plain.xml",
		"notes.txt", "cover.png", "song.gp.bak", "noext",
		".hidden.gp", ".config/",
	)
	got, err := browserReadListing(dir, browserSupported)
	if err != nil {
		t.Fatalf("browserReadListing: %v", err)
	}
	want := []string{
		"albums", "Zebra", // directories first, case-insensitive order
		"hand.gtab", "packed.mxl", "part.smf", "plain.xml",
		"riff.gp", "score.musicxml", "SONG.MIDI", "tune.mid",
	}
	if names := browserNames(got); !browserEqual(names, want) {
		t.Errorf("listing = %v\nwant %v", names, want)
	}
	for _, e := range got {
		if !e.isDir && !browserSupported(e.name) {
			t.Errorf("unsupported file %q survived the filter", e.name)
		}
		if !filepath.IsAbs(e.path) {
			t.Errorf("entry %q has a non-absolute path %q", e.name, e.path)
		}
	}
}

// TestBrowserSupportedExtensions pins the accepted set and its
// case-insensitivity.
func TestBrowserSupportedExtensions(t *testing.T) {
	for _, n := range []string{"a.gtab", "a.mid", "a.midi", "a.smf", "a.gp",
		"a.musicxml", "a.mxl", "a.xml", "A.GP", "A.MusicXML"} {
		if !browserSupported(n) {
			t.Errorf("browserSupported(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"a.txt", "a.gpx", "a.gp5", "a", "a.mp3", "gp"} {
		if browserSupported(n) {
			t.Errorf("browserSupported(%q) = true, want false", n)
		}
	}
}

// TestBrowserClampTop covers the viewport arithmetic on its own: a list
// shorter than the viewport never scrolls, and the offset moves by the
// minimum needed to keep the selection visible.
func TestBrowserClampTop(t *testing.T) {
	for _, c := range []struct {
		name              string
		sel, top, rows, n int
		want              int
	}{
		{"list fits", 3, 0, 10, 5, 0},
		{"selection above the viewport", 2, 7, 5, 30, 2},
		{"selection below the viewport", 9, 0, 5, 30, 5},
		{"selection already visible", 3, 2, 5, 30, 2},
		{"clamped to the last page", 29, 28, 5, 30, 25},
		{"empty list", 0, 4, 5, 0, 0},
	} {
		if got := browserClampTop(c.sel, c.top, c.rows, c.n); got != c.want {
			t.Errorf("%s: browserClampTop(%d,%d,%d,%d) = %d, want %d",
				c.name, c.sel, c.top, c.rows, c.n, got, c.want)
		}
	}
}

// TestBrowserSelectionClampsAndScrolls: Up/Down stop at both ends of the
// list and drag the viewport with them.
func TestBrowserSelectionClampsAndScrolls(t *testing.T) {
	dir := t.TempDir()
	names := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		names = append(names, string(rune('a'+i/10))+string(rune('0'+i%10))+".gp")
	}
	browserTree(t, dir, names...)
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, dir)

	// 40 files plus the ".." row.
	if n := len(b.listing); n != 41 {
		t.Fatalf("listing has %d entries, want 41", n)
	}
	b.move(-1)
	if b.browseSel != 0 || b.browseTop != 0 {
		t.Errorf("up at the top moved to sel=%d top=%d, want 0/0", b.browseSel, b.browseTop)
	}
	for i := 0; i < 100; i++ {
		b.move(1)
	}
	if b.browseSel != 40 {
		t.Errorf("down past the end selected %d, want 40", b.browseSel)
	}
	if want := 41 - brwBrowseRows; b.browseTop != want {
		t.Errorf("viewport top = %d, want %d", b.browseTop, want)
	}
	if b.browseSel < b.browseTop || b.browseSel >= b.browseTop+brwBrowseRows {
		t.Errorf("selection %d outside viewport [%d,%d)", b.browseSel, b.browseTop, b.browseTop+brwBrowseRows)
	}

	// The wheel scrolls the viewport without moving the selection.
	sel := b.browseSel
	b.scrollBy(browserPaneBrowse, -1000)
	if b.browseTop != 0 {
		t.Errorf("scroll to the top left top=%d, want 0", b.browseTop)
	}
	if b.browseSel != sel {
		t.Errorf("scrolling moved the selection to %d, want %d", b.browseSel, sel)
	}
	b.scrollBy(browserPaneBrowse, 1000)
	if want := 41 - brwBrowseRows; b.browseTop != want {
		t.Errorf("scroll to the bottom left top=%d, want %d", b.browseTop, want)
	}
}

// TestBrowserSelectionEmptyPane: an empty pane clamps to 0 and Enter on
// it does nothing rather than indexing past the end.
func TestBrowserSelectionEmptyPane(t *testing.T) {
	dir := t.TempDir()
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, dir)
	b.focus = browserPaneRecent
	b.move(1)
	b.move(-1)
	if b.recentSel != 0 || b.recentTop != 0 {
		t.Errorf("empty recents pane at sel=%d top=%d, want 0/0", b.recentSel, b.recentTop)
	}
	b.activate() // must not panic
	if b.errMsg != "" {
		t.Errorf("activating an empty pane set errMsg %q", b.errMsg)
	}
}

// TestBrowserDescendAndParent walks into a sub-directory and back out,
// and checks the folder just left comes back selected.
func TestBrowserDescendAndParent(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "albums/inner.gp", "top.gp")
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, root)

	if b.listing[0].name != ".." {
		t.Fatalf("first row = %q, want the parent row", b.listing[0].name)
	}
	b.setSelection(browserPaneBrowse, 1)
	if b.listing[1].name != "albums" {
		t.Fatalf("row 1 = %q, want albums", b.listing[1].name)
	}
	b.activate()
	if filepath.Base(b.dir) != "albums" {
		t.Fatalf("after descending, dir = %q", b.dir)
	}
	if names := browserNames(b.listing); !browserEqual(names, []string{"..", "inner.gp"}) {
		t.Errorf("sub-directory listing = %v", names)
	}

	b.goParent()
	if b.dir != filepath.Clean(root) {
		t.Fatalf("after going up, dir = %q, want %q", b.dir, root)
	}
	if b.listing[b.browseSel].name != "albums" {
		t.Errorf("selection after going up = %q, want albums", b.listing[b.browseSel].name)
	}
}

// TestBrowserParentAtRoot: at a filesystem root there is no parent row
// and Backspace is a no-op — not a crash and not a loop.
func TestBrowserParentAtRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir())
	if root == "" {
		root = "/"
	} else {
		root += string(filepath.Separator)
	}
	if p, ok := browserParentOf(root); ok {
		t.Fatalf("browserParentOf(%q) = %q, true; want no parent", root, p)
	}
	// A trailing separator must not fool the check either.
	if p, ok := browserParentOf(filepath.Clean(root) + string(filepath.Separator)); ok {
		t.Fatalf("browserParentOf with a trailing separator = %q, true; want no parent", p)
	}
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, root)
	if len(b.listing) > 0 && b.listing[0].name == ".." {
		t.Error("the filesystem root offers a parent row")
	}
	before := b.dir
	b.goParent() // no-op, must not panic
	if b.dir != before {
		t.Errorf("goParent at the root moved to %q", b.dir)
	}
	// One level down there is a parent again.
	if _, ok := browserParentOf(t.TempDir()); !ok {
		t.Error("a normal directory reports no parent")
	}
}

// TestBrowserUnreadableDirectory: a directory that cannot be listed
// shows the failure inline, leaves the pane usable, and offers the way
// back up.
func TestBrowserUnreadableDirectory(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "notafolder.gp")
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, root)

	for _, bad := range []string{
		filepath.Join(root, "notafolder.gp"), // a file, not a directory
		filepath.Join(root, "nope", "gone"),  // does not exist
	} {
		b.setDir(bad)
		if b.dirErr == "" {
			t.Errorf("setDir(%q) reported no error", bad)
		}
		if len(b.listing) != 1 || b.listing[0].name != ".." {
			t.Errorf("setDir(%q) listing = %v, want just the parent row", bad, browserNames(b.listing))
		}
		b.move(1) // must not panic on the short listing
		b.activate()
	}
	// Backing out clears the error.
	b.setDir(root)
	if b.dirErr != "" {
		t.Errorf("dirErr survived a good setDir: %q", b.dirErr)
	}
}

// TestBrowserMissingRecentFlaggedAndForgotten: a recent whose file has
// gone is marked, refuses to open with an explanation, and Delete drops
// it from the list.
func TestBrowserMissingRecentFlaggedAndForgotten(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "here.gp")
	here := filepath.Join(root, "here.gp")
	gone := filepath.Join(root, "gone.gp")

	pr := &browserFakePrefs{recents: []string{gone, here}}
	op := &browserFakeOpener{}
	b := browserFixture(t, pr, op, root)
	b.focus = browserPaneRecent

	if len(b.recents) != 2 {
		t.Fatalf("recents = %v", browserNames(b.recents))
	}
	if !b.recents[0].missing {
		t.Error("the deleted recent is not flagged missing")
	}
	if b.recents[1].missing {
		t.Error("an existing recent is flagged missing")
	}
	if b.recents[1].parent != filepath.Clean(root) {
		t.Errorf("recent parent = %q, want %q", b.recents[1].parent, root)
	}

	b.setSelection(browserPaneRecent, 0)
	b.activate()
	if b.errMsg == "" {
		t.Error("opening a missing recent reported nothing")
	}
	if len(op.opened) != 0 {
		t.Errorf("a missing recent was handed to the opener: %v", op.opened)
	}

	b.forgetRecent()
	if len(b.recents) != 1 || b.recents[0].path != here {
		t.Fatalf("after forgetting, recents = %v", browserNames(b.recents))
	}
	// The Prefs double cannot remove, so the entry is suppressed for the
	// session and stays suppressed across a reload.
	b.reloadRecents()
	if len(b.recents) != 1 {
		t.Errorf("a forgotten recent came back: %v", browserNames(b.recents))
	}
	if b.recentSel != 0 {
		t.Errorf("selection = %d after the list shrank, want 0", b.recentSel)
	}
}

// TestBrowserForgetPersistsWhenPrefsCanRemove: with a Prefs that
// implements removal, forgetting is written through and saved.
func TestBrowserForgetPersistsWhenPrefsCanRemove(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "a.gp", "b.gp")
	a := filepath.Join(root, "a.gp")
	bpath := filepath.Join(root, "b.gp")
	pr := &browserRemovingPrefs{browserFakePrefs{recents: []string{a, bpath}}}
	b := browserFixture(t, pr, &browserFakeOpener{}, root)
	b.focus = browserPaneRecent

	b.setSelection(browserPaneRecent, 0)
	b.forgetRecent()
	if len(pr.recents) != 1 || pr.recents[0] != bpath {
		t.Errorf("prefs recents = %v, want just %q", pr.recents, bpath)
	}
	if pr.saves == 0 {
		t.Error("removal was not saved")
	}
}

// TestBrowserOpenErrorIsCapturedNotPropagated is the survivability
// requirement: a malformed piece leaves an inline message and the screen
// keeps running.
func TestBrowserOpenErrorIsCapturedNotPropagated(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "broken.gp")
	op := &browserFakeOpener{err: errors.New("truncated archive"), warns: []string{"bar 3 has no notes"}}
	b := browserFixture(t, &browserFakePrefs{}, op, root)

	b.setSelection(browserPaneBrowse, 1)
	if b.listing[1].name != "broken.gp" {
		t.Fatalf("row 1 = %q", b.listing[1].name)
	}
	b.activate()

	if b.errMsg == "" || !strings.Contains(b.errMsg, "truncated archive") {
		t.Errorf("errMsg = %q, want the importer failure", b.errMsg)
	}
	if !strings.Contains(b.errMsg, "broken.gp") {
		t.Errorf("errMsg = %q, want the file name in it", b.errMsg)
	}
	if len(b.warns) != 1 {
		t.Errorf("warnings = %v, want the importer's one warning", b.warns)
	}
	// The screen is still alive and the failure did not become a quit.
	if err := b.handleKey(ebiten.KeyDown); err != nil {
		t.Errorf("handleKey after a failed open = %v, want nil", err)
	}
}

// TestBrowserOpenSuccess: a good piece reaches the opener, is recorded
// as recent, and any warnings are kept for display.
func TestBrowserOpenSuccess(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "good.gp")
	good := filepath.Join(root, "good.gp")
	pr := &browserFakePrefs{}
	op := &browserFakeOpener{warns: []string{"tempo track ignored"}}
	b := browserFixture(t, pr, op, root)

	b.setSelection(browserPaneBrowse, 1)
	b.activate()

	if len(op.opened) != 1 || op.opened[0] != good {
		t.Fatalf("opener saw %v, want [%q]", op.opened, good)
	}
	if b.errMsg != "" {
		t.Errorf("errMsg = %q after a good open", b.errMsg)
	}
	if len(b.warns) != 1 || b.warns[0] != "tempo track ignored" {
		t.Errorf("warnings = %v", b.warns)
	}
	if len(b.recents) != 1 || b.recents[0].path != good {
		t.Errorf("recents = %v, want the piece just opened", browserNames(b.recents))
	}
	if b.recents[0].missing {
		t.Error("a piece that just opened is flagged missing")
	}
}

// TestBrowserOpenWithoutOpener: a shell with no importer says so rather
// than dereferencing nil.
func TestBrowserOpenWithoutOpener(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "song.gp")
	sh := NewShell(Services{Prefs: &browserFakePrefs{}}, nil)
	b := NewBrowser(sh)
	b.setDir(root)
	b.focus = browserPaneBrowse
	b.setSelection(browserPaneBrowse, 1)
	b.activate()
	if b.errMsg == "" {
		t.Error("opening with no importer reported nothing")
	}
}

// TestBrowserEscapeQuits: Escape and Q finish the screen, which at the
// root of the stack exits the application. Nothing else returns an
// error.
func TestBrowserEscapeQuits(t *testing.T) {
	dir := t.TempDir()
	browserTree(t, dir, "a.gp")
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, dir)

	for _, k := range []ebiten.Key{ebiten.KeyEscape, ebiten.KeyQ} {
		if err := b.handleKey(k); err != errQuit {
			t.Errorf("handleKey(%v) = %v, want errQuit", k, err)
		}
	}
	for _, k := range []ebiten.Key{ebiten.KeyUp, ebiten.KeyDown, ebiten.KeyTab,
		ebiten.KeyBackspace, ebiten.KeyDelete, ebiten.KeyHome, ebiten.KeyEnd,
		ebiten.KeyPageUp, ebiten.KeyPageDown, ebiten.KeyS} {
		if err := b.handleKey(k); err != nil {
			t.Errorf("handleKey(%v) = %v, want nil", k, err)
		}
	}
}

// TestBrowserTabSwitchesPanes.
func TestBrowserTabSwitchesPanes(t *testing.T) {
	dir := t.TempDir()
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, dir)
	b.focus = browserPaneRecent
	if err := b.handleKey(ebiten.KeyTab); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if b.focus != browserPaneBrowse {
		t.Error("Tab did not move to the browse pane")
	}
	_ = b.handleKey(ebiten.KeyTab)
	if b.focus != browserPaneRecent {
		t.Error("Tab did not move back to the recents pane")
	}
}

// TestBrowserSettingsHook: S is inert and unadvertised until the
// application installs an opener for the settings screen.
func TestBrowserSettingsHook(t *testing.T) {
	dir := t.TempDir()
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, dir)

	if strings.Contains(b.hintLine(), "settings") {
		t.Errorf("hint advertises settings with no hook: %q", b.hintLine())
	}
	if err := b.handleKey(ebiten.KeyS); err != nil {
		t.Errorf("S with no hook = %v, want nil", err)
	}

	calls := 0
	b.SetSettingsOpener(func() { calls++ })
	if !strings.Contains(b.hintLine(), "settings") {
		t.Errorf("hint hides settings with a hook installed: %q", b.hintLine())
	}
	if err := b.handleKey(ebiten.KeyS); err != nil {
		t.Fatalf("S with a hook = %v", err)
	}
	if calls != 1 {
		t.Errorf("settings hook called %d times, want 1", calls)
	}
}

// TestBrowserHitTestAndClick: hovering maps to a row, a first click
// selects, and a click on the already-selected row opens.
func TestBrowserHitTestAndClick(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "albums/", "one.gp", "two.gp")
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, root)
	b.focus = browserPaneRecent

	// Above the lists is nothing.
	if _, _, ok := b.hitTest(brwBrowseX+10, 20); ok {
		t.Error("the header hit-tested as a row")
	}
	// Between the panes is nothing.
	if _, _, ok := b.hitTest((brwRecentX+brwRecentW+brwBrowseX)/2, brwListTop+4); ok {
		t.Error("the gap between panes hit-tested as a row")
	}
	// Past the last entry is nothing.
	if _, _, ok := b.hitTest(brwBrowseX+10, brwListTop+len(b.listing)*brwBrowseRowH+4); ok {
		t.Error("empty space below the listing hit-tested as a row")
	}

	p, i, ok := b.hitTest(brwBrowseX+10, brwListTop+2*brwBrowseRowH+4)
	if !ok || p != browserPaneBrowse || i != 2 {
		t.Fatalf("hitTest = %v, %d, %v; want the browse pane, row 2", p, i, ok)
	}

	// First click focuses and selects, it does not open.
	b.click(p, i)
	if b.focus != browserPaneBrowse || b.browseSel != 2 {
		t.Fatalf("after the first click: focus=%v sel=%d", b.focus, b.browseSel)
	}
	if b.dir != filepath.Clean(root) {
		t.Fatal("the first click opened something")
	}
	// Clicking the selected row opens it — here, descends into it.
	b.setSelection(browserPaneBrowse, 1)
	if b.listing[1].name != "albums" {
		t.Fatalf("row 1 = %q, want albums", b.listing[1].name)
	}
	b.click(browserPaneBrowse, 1)
	if filepath.Base(b.dir) != "albums" {
		t.Errorf("clicking the selected folder left dir = %q", b.dir)
	}
}

// TestBrowserWheelSteps: fractional trackpad deltas accumulate instead
// of being thrown away.
func TestBrowserWheelSteps(t *testing.T) {
	acc := 0.0
	total := 0
	for i := 0; i < 4; i++ {
		acc += 0.3
		var steps int
		steps, acc = wheelSteps(acc)
		total += steps
	}
	if total != 1 {
		t.Errorf("four 0.3 notches produced %d steps, want 1", total)
	}
	if steps, rem := wheelSteps(-2.5); steps != -2 || rem > -0.49 || rem < -0.51 {
		t.Errorf("wheelSteps(-2.5) = %d, %v; want -2 and about -0.5", steps, rem)
	}
}

// TestBrowserKeyRepeat: every key acts on the press, and only the
// navigation keys repeat while held.
func TestBrowserKeyRepeat(t *testing.T) {
	if !browserKeyFires(false, 1) || !browserKeyFires(true, 1) {
		t.Error("a key press did not fire on frame 1")
	}
	for _, d := range []int{2, 10, 30, 200} {
		if browserKeyFires(false, d) {
			t.Errorf("a non-repeating key fired at hold %d", d)
		}
	}
	if browserKeyFires(true, 20) {
		t.Error("a repeating key fired before the hold delay")
	}
	fires := 0
	for d := 2; d <= 60; d++ {
		if browserKeyFires(true, d) {
			fires++
		}
	}
	if fires == 0 {
		t.Error("a held navigation key never repeated")
	}
	if !browserRepeatKeys[ebiten.KeyDown] || browserRepeatKeys[ebiten.KeyEnter] {
		t.Error("the repeat set is wrong: Enter must not repeat")
	}
}

// TestBrowserDroppedPathsRecoversRealPaths: the dropped-files fs.FS only
// names its entries, and the browser recovers the full path behind each
// one.
func TestBrowserDroppedPathsRecoversRealPaths(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "albums/", "song.gp")
	song := filepath.Join(root, "song.gp")
	albums := filepath.Join(root, "albums")

	got, skipped := browserDroppedPaths(browserDropFS{paths: []string{song, albums}})
	if !browserEqual(got, []string{song, albums}) {
		t.Fatalf("browserDroppedPaths = %v, want [%q %q]", got, song, albums)
	}
	if skipped != 0 {
		t.Errorf("a fully recoverable drop reported %d skipped items", skipped)
	}
	if got, _ := browserDroppedPaths(nil); got != nil {
		t.Error("a nil filesystem produced paths")
	}
	if got, _ := browserDroppedPaths(browserDropFS{}); len(got) != 0 {
		t.Errorf("an empty drop produced %v", got)
	}
}

// TestBrowserHandleDrop: dropping a piece opens it, dropping a folder
// browses to it, dropping something unsupported explains itself.
func TestBrowserHandleDrop(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "albums/inner.gp", "song.gp", "notes.txt")
	song := filepath.Join(root, "song.gp")
	albums := filepath.Join(root, "albums")
	notes := filepath.Join(root, "notes.txt")

	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op, root)

	b.handleDrop(browserDropFS{paths: []string{song}})
	if len(op.opened) != 1 || op.opened[0] != song {
		t.Fatalf("dropping a piece: opener saw %v", op.opened)
	}

	b.handleDrop(browserDropFS{paths: []string{albums}})
	if filepath.Base(b.dir) != "albums" {
		t.Errorf("dropping a folder left dir = %q", b.dir)
	}
	if b.focus != browserPaneBrowse {
		t.Error("dropping a folder did not focus the browse pane")
	}

	b.errMsg = ""
	b.handleDrop(browserDropFS{paths: []string{notes}})
	if b.errMsg == "" {
		t.Error("dropping an unsupported file reported nothing")
	}
	if len(op.opened) != 1 {
		t.Errorf("an unsupported drop reached the opener: %v", op.opened)
	}

	// A drop of nothing recoverable changes nothing.
	before := b.dir
	b.handleDrop(browserDropFS{paths: []string{filepath.Join(root, "vanished.gp")}})
	if b.dir != before {
		t.Errorf("an unrecoverable drop moved to %q", b.dir)
	}
}

// TestBrowserStartsOnTheLastPiecesFolder: with a usable recent, the
// browse pane opens where that piece lives rather than at home.
func TestBrowserStartsOnTheLastPiecesFolder(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "songs/last.gp")
	last := filepath.Join(root, "songs", "last.gp")
	pr := &browserFakePrefs{recents: []string{filepath.Join(root, "ghost.gp"), last}}
	sh := NewShell(Services{Opener: &browserFakeOpener{}, Prefs: pr}, nil)
	b := NewBrowser(sh)
	if b.dir != filepath.Dir(last) {
		t.Errorf("start directory = %q, want %q", b.dir, filepath.Dir(last))
	}
	if b.focus != browserPaneRecent {
		t.Error("with recents present, focus should start on the recents pane")
	}
}

// TestBrowserStartsOnTheChecklistWithNoRecents: a first run has no
// pieces to list, so the left pane holds the getting-started checklist
// and the focus stays on it. Landing in a folder listing instead would
// drop a brand-new user into the one part of the screen that assumes
// they already know what they are looking for.
func TestBrowserStartsOnTheChecklistWithNoRecents(t *testing.T) {
	sh := NewShell(Services{Opener: &browserFakeOpener{}, Prefs: &browserFakePrefs{}}, nil)
	b := NewBrowser(sh)
	if !b.onboarding() {
		t.Fatal("with no recents the screen should be onboarding")
	}
	if b.focus != browserPaneRecent {
		t.Error("focus should start on the checklist")
	}
	if b.paneLen(browserPaneRecent) == 0 {
		t.Error("the checklist pane reports nothing to select")
	}
	if b.dir == "" {
		t.Error("no start directory was chosen")
	}
}

// TestNewBrowserShell: the convenience constructor leaves a shell one
// frame deep whose root becomes the browser as soon as the first Update
// applies the deferred replace — before anything is drawn.
func TestNewBrowserShell(t *testing.T) {
	svc := Services{Opener: &browserFakeOpener{}, Prefs: &browserFakePrefs{}}
	sh, b := NewBrowserShell(svc)
	if sh == nil || b == nil {
		t.Fatal("NewBrowserShell returned nil")
	}
	if sh.Depth() != 1 {
		t.Errorf("shell depth = %d, want 1", sh.Depth())
	}
	if b.sh != sh {
		t.Error("the browser is not attached to the shell it was built with")
	}
	// The placeholder is inert, so the first frame's Update is safe and
	// swaps it for the browser.
	if err := sh.Update(); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if sh.stack[0] != Screen(b) {
		t.Errorf("root after the first frame = %T, want the browser", sh.stack[0])
	}
}

// TestBrowserNilPrefs: a shell without preferences still browses.
func TestBrowserNilPrefs(t *testing.T) {
	dir := t.TempDir()
	browserTree(t, dir, "a.gp")
	sh := NewShell(Services{Opener: &browserFakeOpener{}}, nil)
	b := NewBrowser(sh)
	b.setDir(dir)
	if len(b.recents) != 0 {
		t.Errorf("recents = %v with no Prefs", browserNames(b.recents))
	}
	b.focus = browserPaneRecent
	b.forgetRecent() // must not panic
	b.reloadRecents()
	if b.hintLine() == "" {
		t.Error("empty hint line")
	}
}

// TestBrowserTextShortening pins the two truncation helpers: names keep
// their front, paths keep their tail, and neither ever renders wider
// than the space it was given — measured with the same face that draws.
func TestBrowserTextShortening(t *testing.T) {
	const long = "a fairly long piece name that cannot fit"
	if got := truncateW(long, 120); textW(got) > 120 {
		t.Errorf("truncateW result %q measures %.1fpx, past the 120px budget", got, textW(got))
	}
	if got := truncateW(long, 120); !strings.HasPrefix(got, "a fairly") || !strings.HasSuffix(got, "…") {
		t.Errorf("truncateW = %q, want the front kept and an ellipsis appended", got)
	}
	if got := truncateW("abc", 120); got != "abc" {
		t.Errorf("truncateW on a short string = %q, want it untouched", got)
	}

	const path = `C:\Users\me\Music\tabs\song.gp`
	if got := ellipsizeW(path, 110); textW(got) > 110 {
		t.Errorf("ellipsizeW result %q measures %.1fpx, past the 110px budget", got, textW(got))
	}
	if got := ellipsizeW(path, 110); !strings.HasSuffix(got, "song.gp") || !strings.HasPrefix(got, "…") {
		t.Errorf("ellipsizeW = %q, want the tail kept behind an ellipsis", got)
	}
	if got := ellipsizeW(path, 1000); got != path {
		t.Errorf("ellipsizeW with room to spare = %q, want it untouched", got)
	}
	if got := ellipsizeW("abc", 0); got != "" {
		t.Errorf("ellipsizeW with no room = %q, want empty", got)
	}
}

// TestBrowserOneActionPerFrame is the B1 regression. Enter only queues
// the practice screen's push, which the Shell applies at the end of the
// frame; an Escape processed behind it on the same frame made the Shell
// pop this screen and apply that push in one pass — building a practice
// screen it then discarded without CloseCurrent, leaking the audio
// stream it had started, and swallowing the quit. Input must stop at the
// first action that changes the screen stack.
func TestBrowserOneActionPerFrame(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "song.gp")
	song := filepath.Join(root, "song.gp")
	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op, root)

	b.setSelection(browserPaneBrowse, 1)
	if b.listing[1].name != "song.gp" {
		t.Fatalf("row 1 = %q, want song.gp", b.listing[1].name)
	}

	if err := b.handleKeys(browserPressed(ebiten.KeyEnter, ebiten.KeyEscape)); err != nil {
		t.Fatalf("open and quit on one frame = %v, want nil: the quit must not ride along with a queued push", err)
	}
	if len(op.opened) != 1 || op.opened[0] != song {
		t.Fatalf("opener saw %v, want one open of %q", op.opened, song)
	}
	if n := len(b.sh.pending); n != 1 {
		t.Fatalf("shell holds %d queued stack edits, want just the push", n)
	}
	// End of frame: the screen that was built is the one that is shown.
	for _, fn := range b.sh.pending {
		fn()
	}
	b.sh.pending = b.sh.pending[:0]
	if b.sh.Depth() != 2 {
		t.Errorf("shell depth = %d, want the practice screen pushed and kept", b.sh.Depth())
	}
	if op.closed != 0 {
		t.Errorf("CloseCurrent ran %d times for a screen that was never popped", op.closed)
	}

	// A second piece cannot be opened on the same frame either.
	op.opened = nil
	b.setSelection(browserPaneBrowse, 1)
	if err := b.handleKeys(browserPressed(ebiten.KeyEnter, ebiten.KeyNumpadEnter)); err != nil {
		t.Fatalf("two open keys on one frame = %v", err)
	}
	if len(op.opened) != 1 {
		t.Errorf("two open keys on one frame opened %v, want one piece", op.opened)
	}

	// Escape on its own still leaves the screen, and an idle frame is quiet.
	if err := b.handleKeys(browserPressed(ebiten.KeyEscape)); err != errQuit {
		t.Errorf("Escape alone = %v, want errQuit", err)
	}
	if err := b.handleKeys(browserPressed()); err != nil {
		t.Errorf("an idle frame = %v, want nil", err)
	}
}

// TestBrowserRecentsProbeEachPathOnce is the B2 regression: reloadRecents
// runs on the game loop after every open and every Delete, and a recent
// on an unreachable share takes seconds to stat. Each path is probed
// once, when it first appears, and its status is carried forward.
func TestBrowserRecentsProbeEachPathOnce(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "here.gp")
	here := filepath.Join(root, "here.gp")
	// Stands in for a path on a disconnected network share: every probe
	// of it would block the frame for seconds.
	unreachable := filepath.Join(root, "share", "far.gp")

	probes := map[string]int{}
	stat := func(p string) (fs.FileInfo, error) {
		probes[p]++
		if p == unreachable {
			return nil, fs.ErrNotExist
		}
		return os.Stat(p)
	}
	pr := &browserFakePrefs{recents: []string{unreachable, here}}
	b := browserStatFixture(t, pr, &browserFakeOpener{}, root, stat)

	if probes[unreachable] != 1 {
		t.Fatalf("the first load probed the unreachable recent %d times, want 1", probes[unreachable])
	}
	if !b.recents[0].missing {
		t.Error("the unreachable recent is not flagged missing")
	}
	if b.recents[1].missing {
		t.Error("a present recent is flagged missing")
	}

	// Frames, opens and Deletes all rebuild the pane; none may re-probe.
	for i := 0; i < 10; i++ {
		b.reloadRecents()
	}
	if probes[unreachable] != 1 {
		t.Errorf("the unreachable recent was probed %d times across the session, want 1", probes[unreachable])
	}
	if !b.recents[0].missing || b.recents[1].missing {
		t.Errorf("carried-forward status is wrong: missing = %v, %v; want true, false",
			b.recents[0].missing, b.recents[1].missing)
	}

	b.focus = browserPaneRecent
	b.setSelection(browserPaneRecent, 1)
	b.forgetRecent()
	if probes[unreachable] != 1 {
		t.Errorf("Delete re-probed the unreachable recent (%d probes)", probes[unreachable])
	}

	// A genuinely new recent is probed, once.
	browserTree(t, root, "fresh.gp")
	fresh := filepath.Join(root, "fresh.gp")
	pr.AddRecent(fresh)
	b.reloadRecents()
	b.reloadRecents()
	if probes[fresh] != 1 {
		t.Errorf("a new recent was probed %d times, want 1", probes[fresh])
	}
	if probes[unreachable] != 1 {
		t.Errorf("adding a recent re-probed the unreachable one (%d probes)", probes[unreachable])
	}
}

// TestBrowserReopeningAForgottenRecentShowsItAgain is the B3 regression:
// Delete hides a recent for the session, but opening that exact file
// again puts it back in the configuration's recents, and the pane must
// agree with what was saved instead of hiding it until the next restart.
func TestBrowserReopeningAForgottenRecentShowsItAgain(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "song.gp")
	song := filepath.Join(root, "song.gp")
	pr := &browserFakePrefs{recents: []string{song}}
	op := &browserFakeOpener{}
	b := browserFixture(t, pr, op, root)

	b.focus = browserPaneRecent
	b.setSelection(browserPaneRecent, 0)
	b.forgetRecent()
	if len(b.recents) != 0 {
		t.Fatalf("after Delete, recents = %v, want none", browserNames(b.recents))
	}

	b.focus = browserPaneBrowse
	b.setSelection(browserPaneBrowse, 1)
	if b.listing[1].name != "song.gp" {
		t.Fatalf("row 1 = %q, want song.gp", b.listing[1].name)
	}
	b.activate()

	if len(op.opened) != 1 {
		t.Fatalf("opener saw %v, want the re-opened piece", op.opened)
	}
	if b.forgotten[song] {
		t.Error("the forget flag survived a successful open of that exact file")
	}
	if len(b.recents) != 1 || b.recents[0].path != song {
		t.Fatalf("recents = %v after re-opening a forgotten piece, want it listed again",
			browserNames(b.recents))
	}
	if b.recents[0].missing {
		t.Error("a piece that just opened is flagged missing")
	}
	// And it stays visible across the reloads later frames trigger.
	b.reloadRecents()
	if len(b.recents) != 1 {
		t.Errorf("the re-opened recent vanished again: %v", browserNames(b.recents))
	}
}

// TestBrowserDropSkipsUnreadableEntries is the B4 regression: the
// platform hands back a partial listing with nil holes and an error when
// it cannot stat one of the dropped items. Reading the nils panics, and
// bailing out on the error threw away the items that were fine.
func TestBrowserDropSkipsUnreadableEntries(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "song.gp", "albums/")
	song := filepath.Join(root, "song.gp")
	albums := filepath.Join(root, "albums")

	// The helper survives a listing that is nothing but holes.
	paths, skipped := browserDroppedPaths(browserPartialDropFS{
		nilHoles: 3, err: errors.New("cannot stat dropped item 1"),
	})
	if len(paths) != 0 || skipped != 3 {
		t.Errorf("an all-holes drop = %v, %d skipped; want none and 3", paths, skipped)
	}

	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op, root)
	b.handleDrop(browserPartialDropFS{
		paths:    []string{song},
		nilHoles: 2,
		err:      errors.New("cannot stat dropped item 2"),
	})
	if len(op.opened) != 1 || op.opened[0] != song {
		t.Fatalf("a drop with unreadable items opened %v, want %q", op.opened, song)
	}
	if b.errMsg == "" {
		t.Fatal("a drop that lost two items reported nothing")
	}
	if !strings.Contains(b.errMsg, "2 dropped items") {
		t.Errorf("errMsg = %q, want the two skipped items in it", b.errMsg)
	}

	// The same for a listed item that refuses to open, and for a folder.
	op.opened = nil
	b.errMsg = ""
	b.handleDrop(browserPartialDropFS{paths: []string{albums}, bad: []string{"locked.gp"}})
	if filepath.Base(b.dir) != "albums" {
		t.Errorf("dropping a folder alongside a bad item left dir = %q", b.dir)
	}
	if !strings.Contains(b.errMsg, "1 dropped item") {
		t.Errorf("errMsg = %q, want the one skipped item in it", b.errMsg)
	}
}

// TestBrowserDropAlwaysReportsOutcome: handleDrop promises to say so
// when a drop yields nothing usable, and used to return in silence.
func TestBrowserDropAlwaysReportsOutcome(t *testing.T) {
	root := t.TempDir()
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, root)

	for _, c := range []struct {
		name string
		fsys fs.FS
	}{
		{"an empty drop", browserDropFS{}},
		{"a drop of nothing recoverable", browserDropFS{paths: []string{filepath.Join(root, "vanished.gp")}}},
		{"a drop of items that will not open", browserPartialDropFS{bad: []string{"locked.gp"}}},
		{"a drop the platform could not read at all", browserPartialDropFS{err: errors.New("read failed")}},
	} {
		b.errMsg = ""
		before := b.dir
		b.handleDrop(c.fsys)
		if b.errMsg == "" {
			t.Errorf("%s reported nothing", c.name)
		}
		if b.dir != before {
			t.Errorf("%s moved the browse pane to %q", c.name, b.dir)
		}
	}
}

// TestBrowserTextIsASCII: basicfont has no glyphs beyond ASCII, so any
// non-ASCII character in the chrome would render as tofu.
func TestBrowserTextIsASCII(t *testing.T) {
	dir := t.TempDir()
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{}, dir)
	b.SetSettingsOpener(func() {})
	for _, s := range []string{b.hintLine(), "RECENT PIECES", "BROWSE", ".. (up one folder)"} {
		for _, r := range s {
			if r > 127 {
				t.Errorf("non-ASCII rune %q in %q", r, s)
			}
		}
	}
}
