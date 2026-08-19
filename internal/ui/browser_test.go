package ui

// Browser tests. Ebitengine cannot open a window here, so every test
// drives the Browser's plain state methods — move, activate, handleKey,
// click, handleDrop, launchOpenDialog — with doubles standing in for the
// preferences, the importer, and the OS file dialog. Nothing in this
// file renders.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	recents  []string
	created  []string
	hideHint bool
	saves    int
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
func (p *browserFakePrefs) Created() []string { return p.created }
func (p *browserFakePrefs) AddCreated(path string) {
	out := []string{path}
	for _, r := range p.created {
		if r != path {
			out = append(out, r)
		}
	}
	p.created = out
}
func (p *browserFakePrefs) HintHidden() bool          { return p.hideHint }
func (p *browserFakePrefs) SetHintHidden(h bool)      { p.hideHint = h }
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

// browserFixture builds a Browser over a shell with the given doubles.
// The recents list — the screen's one list — comes from pr.
func browserFixture(t *testing.T, pr Prefs, op Opener) *Browser {
	t.Helper()
	sh := NewShell(Services{Opener: op, Prefs: pr}, nil)
	return NewBrowser(sh)
}

// browserStatFixture builds a Browser whose recents probe is the given
// hook, installed before the first load so the probes NewBrowser would
// have made are counted too — which is why this repeats its steps
// instead of calling it.
func browserStatFixture(t *testing.T, pr Prefs, op Opener,
	stat func(string) (fs.FileInfo, error)) *Browser {
	t.Helper()
	sh := NewShell(Services{Opener: op, Prefs: pr}, nil)
	b := &Browser{sh: sh, forgotten: map[string]bool{}, hoverIdx: -1, statFn: stat}
	b.reloadRecents()
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

// browserNames lists the entries' names, for compact assertions.
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

// browserRecentsFixture builds a Browser whose recents are n real files
// under a fresh temp root, returning the paths in list order.
func browserRecentsFixture(t *testing.T, op Opener, n int) (*Browser, []string) {
	t.Helper()
	root := t.TempDir()
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := string(rune('a'+i/10)) + string(rune('0'+i%10)) + ".gp"
		browserTree(t, root, name)
		paths = append(paths, filepath.Join(root, name))
	}
	return browserFixture(t, &browserFakePrefs{recents: paths}, op), paths
}

// --- tests ---------------------------------------------------------------

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
	b, _ := browserRecentsFixture(t, &browserFakeOpener{}, 40)

	if n := b.listLen(); n != 40 {
		t.Fatalf("list has %d entries, want 40", n)
	}
	b.move(-1)
	if b.sel != 0 || b.top != 0 {
		t.Errorf("up at the top moved to sel=%d top=%d, want 0/0", b.sel, b.top)
	}
	for i := 0; i < 100; i++ {
		b.move(1)
	}
	if b.sel != 39 {
		t.Errorf("down past the end selected %d, want 39", b.sel)
	}
	if want := 40 - b.rowsPerPane(); b.top != want {
		t.Errorf("viewport top = %d, want %d", b.top, want)
	}
	if b.sel < b.top || b.sel >= b.top+b.rowsPerPane() {
		t.Errorf("selection %d outside viewport [%d,%d)", b.sel, b.top, b.top+b.rowsPerPane())
	}

	// The wheel scrolls the viewport without moving the selection.
	sel := b.sel
	b.scrollBy(-1000)
	if b.top != 0 {
		t.Errorf("scroll to the top left top=%d, want 0", b.top)
	}
	if b.sel != sel {
		t.Errorf("scrolling moved the selection to %d, want %d", b.sel, sel)
	}
	b.scrollBy(1000)
	if want := 40 - b.rowsPerPane(); b.top != want {
		t.Errorf("scroll to the bottom left top=%d, want %d", b.top, want)
	}
}

// TestBrowserSelectionStaysInBounds: the selection clamps at both ends
// of an empty pane, and Enter never indexes past the end of a list —
// even if the selection somehow outlived a shrink.
func TestBrowserSelectionStaysInBounds(t *testing.T) {
	// An EMPTY pane is the case that has no row to land on at all, and
	// moving in it must not walk the cursor off either end.
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	if b.listLen() != 0 {
		t.Fatalf("the fixture has %d rows, want an empty pane", b.listLen())
	}
	b.move(-1)
	if b.sel != 0 || b.top != 0 {
		t.Errorf("up in an empty pane left sel=%d top=%d, want 0/0", b.sel, b.top)
	}
	for i := 0; i < 50; i++ {
		b.move(1)
	}
	if b.sel != 0 || b.top != 0 {
		t.Errorf("down in an empty pane left sel=%d top=%d, want 0/0", b.sel, b.top)
	}
	b.activate() // must not index past the slice

	// A selection past the recents — standing in for any frame where the
	// list shrank before setSel re-clamped — activates to nothing rather
	// than indexing past the slice.
	root := t.TempDir()
	browserTree(t, root, "here.gp")
	op := &browserFakeOpener{}
	b = browserFixture(t, &browserFakePrefs{recents: []string{filepath.Join(root, "here.gp")}}, op)
	b.sel = 5
	b.activate() // must not panic
	if len(op.opened) != 0 {
		t.Errorf("an out-of-range selection opened %v", op.opened)
	}
	if b.errMsg != "" {
		t.Errorf("an out-of-range selection set errMsg %q", b.errMsg)
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
	b := browserFixture(t, pr, op)

	if len(b.panes[paneRecent]) != 2 {
		t.Fatalf("recents = %v", browserNames(b.panes[paneRecent]))
	}
	if !b.panes[paneRecent][0].missing {
		t.Error("the deleted recent is not flagged missing")
	}
	if b.panes[paneRecent][1].missing {
		t.Error("an existing recent is flagged missing")
	}
	if b.panes[paneRecent][1].sub != filepath.Clean(root) {
		t.Errorf("recent parent = %q, want %q", b.panes[paneRecent][1].sub, root)
	}

	b.setSel(0)
	b.activate()
	if b.errMsg == "" {
		t.Error("opening a missing recent reported nothing")
	}
	if len(op.opened) != 0 {
		t.Errorf("a missing recent was handed to the opener: %v", op.opened)
	}

	b.forgetRecent()
	if len(b.panes[paneRecent]) != 1 || b.panes[paneRecent][0].path != here {
		t.Fatalf("after forgetting, recents = %v", browserNames(b.panes[paneRecent]))
	}
	// The Prefs double cannot remove, so the entry is suppressed for the
	// session and stays suppressed across a reload.
	b.reloadRecents()
	if len(b.panes[paneRecent]) != 1 {
		t.Errorf("a forgotten recent came back: %v", browserNames(b.panes[paneRecent]))
	}
	if b.sel != 0 {
		t.Errorf("selection = %d after the list shrank, want 0", b.sel)
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
	b := browserFixture(t, pr, &browserFakeOpener{})

	b.setSel(0)
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
	broken := filepath.Join(root, "broken.gp")
	op := &browserFakeOpener{err: errors.New("truncated archive"), warns: []string{"bar 3 has no notes"}}
	b := browserFixture(t, &browserFakePrefs{recents: []string{broken}}, op)

	b.setSel(0)
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

// TestBrowserOpenSuccess: a good piece chosen in the file dialog reaches
// the opener, is recorded as recent, and any warnings are kept for
// display.
func TestBrowserOpenSuccess(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "good.gp")
	good := filepath.Join(root, "good.gp")
	pr := &browserFakePrefs{}
	op := &browserFakeOpener{warns: []string{"tempo track ignored"}}
	b := browserFixture(t, pr, op)
	b.SetOpenDialog(func(string) {})

	b.launchOpenDialog("")
	b.OfferDialogResult(good, "")
	b.drainDialog()

	if len(op.opened) != 1 || op.opened[0] != good {
		t.Fatalf("opener saw %v, want [%q]", op.opened, good)
	}
	if b.errMsg != "" {
		t.Errorf("errMsg = %q after a good open", b.errMsg)
	}
	if len(b.warns) != 1 || b.warns[0] != "tempo track ignored" {
		t.Errorf("warnings = %v", b.warns)
	}
	if len(b.panes[paneRecent]) != 1 || b.panes[paneRecent][0].path != good {
		t.Errorf("recents = %v, want the piece just opened", browserNames(b.panes[paneRecent]))
	}
	if b.panes[paneRecent][0].missing {
		t.Error("a piece that just opened is flagged missing")
	}
	if b.dialogBusy {
		t.Error("the dialog is still marked busy after its result arrived")
	}
}

// TestBrowserWarningsRememberTheirPiece: importer warnings can sit in
// the status band for minutes while the user practises and comes back,
// so they carry the name of the piece they came from — and the name goes
// away with them, never outliving the warnings it explains.
func TestBrowserWarningsRememberTheirPiece(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "good.gp", "clean.gp")
	good := filepath.Join(root, "good.gp")
	clean := filepath.Join(root, "clean.gp")
	op := &browserFakeOpener{warns: []string{"tempo track ignored"}}
	b := browserFixture(t, &browserFakePrefs{}, op)

	b.openPath(good)
	if b.warnsFrom != "good.gp" {
		t.Errorf("warnsFrom = %q, want the piece the warnings came from", b.warnsFrom)
	}

	// A later open with nothing to warn about clears the heading with the
	// warnings, and ShowError replaces the whole status.
	op.warns = nil
	b.openPath(clean)
	if b.warnsFrom != "" || len(b.warns) != 0 {
		t.Errorf("after a clean open, warnsFrom = %q with %d warnings; want both gone",
			b.warnsFrom, len(b.warns))
	}
	op.warns = []string{"tempo track ignored"}
	b.openPath(good)
	b.ShowError("boom")
	if b.warnsFrom != "" {
		t.Errorf("warnsFrom = %q after ShowError, want it cleared with the warnings", b.warnsFrom)
	}
}

func TestBrowserWarningsHeadingIsHonestAboutFailure(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "good.gp")
	good := filepath.Join(root, "good.gp")
	op := &browserFakeOpener{warns: []string{"tempo track ignored"}}
	b := browserFixture(t, &browserFakePrefs{}, op)

	b.openPath(good)
	if got := b.warnsHeading(); got != "good.gp opened with warnings:" {
		t.Errorf("after a clean open the heading is %q", got)
	}

	// An importer can warn AND fail; the heading must not claim the
	// piece opened.
	op.err = fmt.Errorf("truncated file")
	b.openPath(good)
	if got := b.warnsHeading(); got != "warnings from good.gp:" {
		t.Errorf("after a failed open the heading is %q, want it not to claim the piece opened", got)
	}

	// The verdict is recorded at open time, not inferred from the error
	// line: launching the open dialog clears errMsg but keeps the
	// warnings, and the heading must keep telling the truth.
	b.SetOpenDialog(func(string) {})
	b.launchOpenDialog("")
	if got := b.warnsHeading(); got != "warnings from good.gp:" {
		t.Errorf("after the O key cleared the error line the heading became %q", got)
	}
}

// TestBrowserOpenWithoutOpener: a shell with no importer says so rather
// than dereferencing nil.
func TestBrowserOpenWithoutOpener(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "song.gp")
	song := filepath.Join(root, "song.gp")
	sh := NewShell(Services{Prefs: &browserFakePrefs{recents: []string{song}}}, nil)
	b := NewBrowser(sh)
	b.setSel(0)
	b.activate()
	if b.errMsg == "" {
		t.Error("opening with no importer reported nothing")
	}
}

// TestBrowserEscapeQuits: Escape and Q finish the screen, which at the
// root of the stack exits the application. Nothing else returns an
// error.
func TestBrowserEscapeQuits(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})

	for _, k := range []ebiten.Key{ebiten.KeyEscape, ebiten.KeyQ} {
		if err := b.handleKey(k); err != errQuit {
			t.Errorf("handleKey(%v) = %v, want errQuit", k, err)
		}
	}
	for _, k := range []ebiten.Key{ebiten.KeyUp, ebiten.KeyDown, ebiten.KeyDelete,
		ebiten.KeyHome, ebiten.KeyEnd, ebiten.KeyPageUp, ebiten.KeyPageDown,
		ebiten.KeyS, ebiten.KeyO} {
		if err := b.handleKey(k); err != nil {
			t.Errorf("handleKey(%v) = %v, want nil", k, err)
		}
	}
}

// TestBrowserSettingsHook: S is inert and unadvertised until the
// application installs an opener for the settings screen.
func TestBrowserSettingsHook(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})

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

// TestBrowserBindingsDocumentWorkingKeys: H toggles the getting-started
// strip and Home/End jump the selection, so the control table must list
// them — a strip whose button says "hide (H)" while H is documented
// nowhere is a trap. The settings hint carries the key's own capital,
// matching the practice view and the S on the button itself.
func TestBrowserBindingsDocumentWorkingKeys(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	rows := map[string]helpBinding{}
	for _, r := range b.browserBindings() {
		rows[r.Keys] = r
	}
	for _, want := range []string{"H", "home / end", "page up / down"} {
		if _, ok := rows[want]; !ok {
			t.Errorf("browserBindings has no %q row", want)
		}
	}
	if r := rows["H"]; r.Off {
		t.Error("the H binding is marked off; the strip can always be toggled")
	}

	b.SetSettingsOpener(func() {})
	if !strings.Contains(b.hintLine(), "S settings") {
		t.Errorf("footer hint = %q, want the settings key shown as the capital S it is", b.hintLine())
	}
}

// TestBrowserHitTestAndClick: hovering maps to a recents row, a first
// click selects, and a click on the already-selected row opens.
func TestBrowserHitTestAndClick(t *testing.T) {
	op := &browserFakeOpener{}
	b, paths := browserRecentsFixture(t, op, 3)

	l := b.layout()
	pane := l.panes[0]
	rowY := func(row int) float64 { return pane.y + brwPaneHeadH + float64(row)*brwRowH + 4 }

	// Above the panes is nothing.
	if _, _, _, onPane := b.paneHit(l, pane.x+10, 20); onPane {
		t.Error("the header hit-tested as a pane")
	}
	// A pane's heading is on the pane but not on a row: clicking it moves
	// the focus without changing what is selected.
	if _, _, onRow, onPane := b.paneHit(l, pane.x+10, pane.y+4); onRow || !onPane {
		t.Errorf("the pane heading hit-tested onRow=%v onPane=%v; want false, true", onRow, onPane)
	}
	// Past the last entry is on the pane but not on a row.
	if _, _, onRow, _ := b.paneHit(l, pane.x+10, rowY(b.listLen())); onRow {
		t.Error("empty space below the rows hit-tested as a row")
	}

	p, i, ok, _ := b.paneHit(l, pane.x+10, rowY(2))
	if !ok || i != 2 || p != paneRecent {
		t.Fatalf("paneHit = pane %d row %d, %v; want the recent pane's row 2", p, i, ok)
	}

	// First click selects, it does not open.
	b.click(i)
	if b.sel != 2 {
		t.Fatalf("after the first click: sel=%d, want 2", b.sel)
	}
	if len(op.opened) != 0 {
		t.Fatalf("the first click opened %v", op.opened)
	}
	// Clicking the selected row opens it — which is also what the second
	// click of a double click lands on.
	b.click(i)
	if len(op.opened) != 1 || op.opened[0] != paths[2] {
		t.Errorf("clicking the selected row opened %v, want [%q]", op.opened, paths[2])
	}
}

// TestBrowserClickAcrossPanesSelectsBeforeOpening: a press on an
// unfocused pane moves the focus there and picks the row under the
// cursor — and must not ALSO open it. focusPane restores that pane's
// parked selection (0 for a pane never visited), so a first click on its
// first row used to count as the second click of a double-click and open
// the piece outright, against the documented "click it again to open it".
func TestBrowserClickAcrossPanesSelectsBeforeOpening(t *testing.T) {
	dir := t.TempDir()
	browserTree(t, dir, "a.gp", "x.gtab")
	pr := &browserFakePrefs{
		recents: []string{filepath.Join(dir, "a.gp")},
		created: []string{filepath.Join(dir, "x.gtab")},
	}
	op := &browserFakeOpener{}
	b := browserFixture(t, pr, op)
	if b.focus != paneRecent {
		t.Fatalf("the fixture starts on pane %d, want the recent one", b.focus)
	}

	l := b.layout()
	over := l.panes[1]
	press := pointer{x: over.x + 10, y: over.y + brwPaneHeadH + 4, pressed: true, down: true}
	b.handleMouse(press)
	if b.focus != paneCreated || b.sel != 0 {
		t.Fatalf("the press landed on pane %d row %d, want the written pane's row 0", b.focus, b.sel)
	}
	if len(op.opened) != 0 {
		t.Fatalf("one click on an unfocused pane opened %v; the first click may only select", op.opened)
	}
	// The pane now has the focus, so the same press again is the promised
	// second click and opens the row.
	b.handleMouse(press)
	if len(op.opened) != 1 || op.opened[0] != pr.created[0] {
		t.Errorf("clicking the row again opened %v, want [%q]", op.opened, pr.created[0])
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
// launches the OS file dialog rooted there, dropping something
// unsupported explains itself.
func TestBrowserHandleDrop(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "albums/inner.gp", "song.gp", "notes.txt")
	song := filepath.Join(root, "song.gp")
	albums := filepath.Join(root, "albums")
	notes := filepath.Join(root, "notes.txt")

	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op)

	b.handleDrop(browserDropFS{paths: []string{song}})
	if len(op.opened) != 1 || op.opened[0] != song {
		t.Fatalf("dropping a piece: opener saw %v", op.opened)
	}

	// A dropped folder is "here is where my tabs live": the dialog opens
	// rooted there, and is marked in flight.
	var dialogDirs []string
	b.SetOpenDialog(func(startDir string) { dialogDirs = append(dialogDirs, startDir) })
	b.handleDrop(browserDropFS{paths: []string{albums}})
	if len(dialogDirs) != 1 || dialogDirs[0] != albums {
		t.Errorf("dropping a folder launched the dialog with %v, want [%q]", dialogDirs, albums)
	}
	if !b.dialogBusy {
		t.Error("dropping a folder did not mark the dialog busy")
	}

	b.errMsg = ""
	b.handleDrop(browserDropFS{paths: []string{notes}})
	if b.errMsg == "" {
		t.Error("dropping an unsupported file reported nothing")
	}
	if len(op.opened) != 1 {
		t.Errorf("an unsupported drop reached the opener: %v", op.opened)
	}

	// A drop of nothing recoverable reports itself and launches nothing.
	b.handleDrop(browserDropFS{paths: []string{filepath.Join(root, "vanished.gp")}})
	if b.errMsg == "" {
		t.Error("an unrecoverable drop reported nothing")
	}
	if len(op.opened) != 1 || len(dialogDirs) != 1 {
		t.Errorf("an unrecoverable drop acted: opened %v, dialogs %v", op.opened, dialogDirs)
	}
}

// TestBrowserMultiPieceDropReportsTheRest: only the first supported
// piece of a drop opens, and what happened to the others must be said —
// they used to be discarded in silence, leaving the user counting
// windows that never appeared.
func TestBrowserMultiPieceDropReportsTheRest(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "one.gp", "two.gp", "three.gtab", "notes.txt")
	one := filepath.Join(root, "one.gp")
	two := filepath.Join(root, "two.gp")
	three := filepath.Join(root, "three.gtab")
	notes := filepath.Join(root, "notes.txt")
	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op)

	b.handleDrop(browserDropFS{paths: []string{one, two, three, notes}})
	if len(op.opened) != 1 || op.opened[0] != one {
		t.Fatalf("a three-piece drop opened %v, want the first piece only", op.opened)
	}
	if !strings.Contains(b.errMsg, "one.gp") {
		t.Errorf("errMsg = %q, want the opened piece named in it", b.errMsg)
	}
	if !strings.Contains(b.errMsg, "2 more dropped pieces were ignored") {
		t.Errorf("errMsg = %q, want the two ignored pieces counted", b.errMsg)
	}

	// One leftover piece is reported in the singular.
	op.opened = nil
	b.handleDrop(browserDropFS{paths: []string{one, two}})
	if !strings.Contains(b.errMsg, "1 more dropped piece was ignored") {
		t.Errorf("errMsg = %q, want the one ignored piece in it", b.errMsg)
	}

	// A single-piece drop has nothing to add and stays quiet.
	op.opened = nil
	b.handleDrop(browserDropFS{paths: []string{one}})
	if b.errMsg != "" {
		t.Errorf("a clean single-piece drop set errMsg %q", b.errMsg)
	}
}

// TestBrowserEmptyRecentPaneTeachesDragAndDrop: outside the ?-overlay,
// the empty recent pane is the one place a new user looks first, so it
// is where drag-and-drop gets written down.
func TestBrowserEmptyRecentPaneTeachesDragAndDrop(t *testing.T) {
	if !strings.Contains(paneEmpty[paneRecent], "drop a file") {
		t.Errorf("the empty recent pane says %q, want drag-and-drop taught in it", paneEmpty[paneRecent])
	}
}

// TestBrowserStartsOnTheLastPiecesFolder: with a usable recent, the file
// dialog starts where that piece lives rather than at home — a recent
// whose file has gone missing is skipped over.
func TestBrowserStartsOnTheLastPiecesFolder(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "songs/last.gp")
	last := filepath.Join(root, "songs", "last.gp")
	pr := &browserFakePrefs{recents: []string{filepath.Join(root, "ghost.gp"), last}}
	b := browserFixture(t, pr, &browserFakeOpener{})
	if got, want := b.startDir(), filepath.Dir(last); got != want {
		t.Errorf("startDir() = %q, want %q", got, want)
	}
}

// TestBrowserFirstRunShowsTheHint: a first run has no pieces in any
// pane, so the getting-started strip is what carries the screen — and
// the file dialog still has somewhere sensible to open.
func TestBrowserFirstRunShowsTheHint(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	if !b.hintOpen {
		t.Error("the getting-started strip is not showing on a first run")
	}
	if len(b.stepList()) == 0 {
		t.Error("the strip has no steps")
	}
	if b.hasAnyPiece() {
		t.Error("a first run reports pieces it does not have")
	}
	if b.listLen() != 0 {
		t.Errorf("the recent pane has %d rows on a first run, want 0", b.listLen())
	}
	if b.startDir() == "" {
		t.Error("the file dialog has no starting directory")
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

// TestBrowserNilPrefs: a shell without preferences still puts up a
// working start screen.
func TestBrowserNilPrefs(t *testing.T) {
	sh := NewShell(Services{Opener: &browserFakeOpener{}}, nil)
	b := NewBrowser(sh)
	if len(b.panes[paneRecent]) != 0 {
		t.Errorf("recents = %v with no Prefs", browserNames(b.panes[paneRecent]))
	}
	b.forgetRecent() // must not panic
	b.reloadRecents()
	if b.hintLine() == "" {
		t.Error("empty hint line")
	}
	if b.startDir() == "" {
		t.Error("the file dialog has no starting directory")
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
	b := browserFixture(t, &browserFakePrefs{recents: []string{song}}, op)

	b.setSel(0)
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
	b.setSel(0)
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
	b := browserStatFixture(t, pr, &browserFakeOpener{}, stat)

	if probes[unreachable] != 1 {
		t.Fatalf("the first load probed the unreachable recent %d times, want 1", probes[unreachable])
	}
	if !b.panes[paneRecent][0].missing {
		t.Error("the unreachable recent is not flagged missing")
	}
	if b.panes[paneRecent][1].missing {
		t.Error("a present recent is flagged missing")
	}

	// Frames, opens and Deletes all rebuild the list; none may re-probe.
	for i := 0; i < 10; i++ {
		b.reloadRecents()
	}
	if probes[unreachable] != 1 {
		t.Errorf("the unreachable recent was probed %d times across the session, want 1", probes[unreachable])
	}
	if !b.panes[paneRecent][0].missing || b.panes[paneRecent][1].missing {
		t.Errorf("carried-forward status is wrong: missing = %v, %v; want true, false",
			b.panes[paneRecent][0].missing, b.panes[paneRecent][1].missing)
	}

	b.setSel(1)
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
// again — through the OS dialog now — puts it back in the
// configuration's recents, and the list must agree with what was saved
// instead of hiding it until the next restart.
func TestBrowserReopeningAForgottenRecentShowsItAgain(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "song.gp")
	song := filepath.Join(root, "song.gp")
	pr := &browserFakePrefs{recents: []string{song}}
	op := &browserFakeOpener{}
	b := browserFixture(t, pr, op)

	b.setSel(0)
	b.forgetRecent()
	if len(b.panes[paneRecent]) != 0 {
		t.Fatalf("after Delete, recents = %v, want none", browserNames(b.panes[paneRecent]))
	}

	// The user finds the same file again in the dialog.
	b.SetOpenDialog(func(string) {})
	b.launchOpenDialog("")
	b.OfferDialogResult(song, "")
	b.drainDialog()

	if len(op.opened) != 1 {
		t.Fatalf("opener saw %v, want the re-opened piece", op.opened)
	}
	if b.forgotten[song] {
		t.Error("the forget flag survived a successful open of that exact file")
	}
	if len(b.panes[paneRecent]) != 1 || b.panes[paneRecent][0].path != song {
		t.Fatalf("recents = %v after re-opening a forgotten piece, want it listed again",
			browserNames(b.panes[paneRecent]))
	}
	if b.panes[paneRecent][0].missing {
		t.Error("a piece that just opened is flagged missing")
	}
	// And it stays visible across the reloads later frames trigger.
	b.reloadRecents()
	if len(b.panes[paneRecent]) != 1 {
		t.Errorf("the re-opened recent vanished again: %v", browserNames(b.panes[paneRecent]))
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
	b := browserFixture(t, &browserFakePrefs{}, op)
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

	// The same for a listed item that refuses to open, and for a folder —
	// which now launches the dialog rooted there.
	op.opened = nil
	b.errMsg = ""
	var dialogDirs []string
	b.SetOpenDialog(func(startDir string) { dialogDirs = append(dialogDirs, startDir) })
	b.handleDrop(browserPartialDropFS{paths: []string{albums}, bad: []string{"locked.gp"}})
	if len(dialogDirs) != 1 || dialogDirs[0] != albums {
		t.Errorf("dropping a folder alongside a bad item launched the dialog with %v, want [%q]", dialogDirs, albums)
	}
	if !strings.Contains(b.errMsg, "1 dropped item") {
		t.Errorf("errMsg = %q, want the one skipped item in it", b.errMsg)
	}
}

// TestBrowserDropAlwaysReportsOutcome: handleDrop promises to say so
// when a drop yields nothing usable, and used to return in silence.
func TestBrowserDropAlwaysReportsOutcome(t *testing.T) {
	root := t.TempDir()
	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op)
	launches := 0
	b.SetOpenDialog(func(string) { launches++ })

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
		b.handleDrop(c.fsys)
		if b.errMsg == "" {
			t.Errorf("%s reported nothing", c.name)
		}
	}
	if len(op.opened) != 0 {
		t.Errorf("an unusable drop reached the opener: %v", op.opened)
	}
	if launches != 0 {
		t.Errorf("an unusable drop launched the dialog %d times", launches)
	}
}

// TestBrowserDialogRoundTrip covers the mailbox between the screen and
// the dialog goroutine: launch marks the dialog in flight and hands over
// a starting directory; a chosen path is opened, a cancel does nothing,
// and a failure is reported inline — each re-arming the dialog.
func TestBrowserDialogRoundTrip(t *testing.T) {
	root := t.TempDir()
	browserTree(t, root, "picked.gtab")
	picked := filepath.Join(root, "picked.gtab")
	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op)
	var dirs []string
	b.SetOpenDialog(func(startDir string) { dirs = append(dirs, startDir) })

	// A chosen file comes back and is opened.
	b.launchOpenDialog("")
	if !b.dialogBusy {
		t.Fatal("launching did not mark the dialog busy")
	}
	if len(dirs) != 1 || dirs[0] == "" {
		t.Fatalf("the dialog was launched with dirs %v, want one non-empty starting directory", dirs)
	}
	b.OfferDialogResult(picked, "")
	b.drainDialog()
	if len(op.opened) != 1 || op.opened[0] != picked {
		t.Fatalf("opener saw %v, want [%q]", op.opened, picked)
	}
	if b.dialogBusy {
		t.Error("the dialog stayed busy after its result was drained")
	}

	// A cancel opens nothing and reports nothing: not choosing a file is
	// not an error.
	b.launchOpenDialog("")
	b.OfferDialogResult("", "")
	b.drainDialog()
	if len(op.opened) != 1 {
		t.Errorf("a cancelled dialog opened %v", op.opened[1:])
	}
	if b.dialogBusy {
		t.Error("the dialog stayed busy after a cancel")
	}
	if b.errMsg != "" {
		t.Errorf("a cancel set errMsg %q", b.errMsg)
	}

	// A dialog failure is shown inline rather than swallowed.
	b.launchOpenDialog("")
	b.OfferDialogResult("", "boom")
	b.drainDialog()
	if !strings.Contains(b.errMsg, "boom") {
		t.Errorf("errMsg = %q, want the dialog failure in it", b.errMsg)
	}
	if b.dialogBusy {
		t.Error("the dialog stayed busy after a failure")
	}
}

// TestBrowserDialogBusyGuard: while a dialog is in flight another launch
// is ignored — two Explorer windows fighting over one mailbox helps
// nobody — and once the result arrives the dialog can launch again.
func TestBrowserDialogBusyGuard(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	calls := 0
	b.SetOpenDialog(func(string) { calls++ })

	b.launchOpenDialog("")
	b.launchOpenDialog("")
	if calls != 1 {
		t.Fatalf("a launch while busy ran the dialog %d times, want 1", calls)
	}
	b.OfferDialogResult("", "")
	b.drainDialog()
	b.launchOpenDialog("")
	if calls != 2 {
		t.Errorf("after a result the dialog launched %d times, want 2", calls)
	}
}

// TestBrowserDialogUnavailable: a build with no dialog wired says so
// instead of silently doing nothing.
func TestBrowserDialogUnavailable(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	b.launchOpenDialog("")
	if !strings.Contains(b.errMsg, "no file dialog") {
		t.Errorf("errMsg = %q, want the no-dialog explanation", b.errMsg)
	}
}

// TestBrowserOKeyLaunchesDialog: O is the keyboard's way to the open
// card's button.
func TestBrowserOKeyLaunchesDialog(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	calls := 0
	b.SetOpenDialog(func(string) { calls++ })
	if err := b.handleKey(ebiten.KeyO); err != nil {
		t.Fatalf("handleKey(O) = %v", err)
	}
	if calls != 1 {
		t.Errorf("O launched the dialog %d times, want 1", calls)
	}
}

// TestBrowserStatusStackClearsFooter pins the arithmetic behind the
// reserved status band (verification follow-up): the worst-case stack —
// an error line, the heading naming the piece the warnings came from,
// the listed warnings, and the overflow line — must end above the footer
// hint, or the two draw on top of each other and both become unreadable.
// The panes stop above the band whether or not there is anything in it,
// so they cannot reach it either.
func TestBrowserStatusStackClearsFooter(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	for _, hint := range []bool{true, false} {
		b.hintOpen = hint
		l := b.layout()
		worstBottom := l.statusY + 20 + 18 + brwStatusWarnings*18 + uiTextH
		if worstBottom > uiFooterY {
			t.Errorf("hint=%v: the status stack can reach y=%v, past the footer at y=%v",
				hint, worstBottom, float64(uiFooterY))
		}
		for i, p := range l.panes {
			if bottom := p.y + p.h; bottom > l.statusY {
				t.Errorf("hint=%v: pane %d ends at y=%v, inside the status band at y=%v",
					hint, i, bottom, l.statusY)
			}
		}
		if l.rows < 4 {
			t.Errorf("hint=%v: only %d rows fit in a pane", hint, l.rows)
		}
	}
}

// TestBrowserStaleDialogResultDiscarded (verification follow-up): a
// dialog left floating while the user opened a different piece by other
// means must not auto-open its old choice the moment the start screen is
// next shown — that pick answered a question nobody is asking any more.
func TestBrowserStaleDialogResultDiscarded(t *testing.T) {
	op := &browserFakeOpener{}
	b := browserFixture(t, &browserFakePrefs{}, op)
	b.SetOpenDialog(func(string) {})

	// A dialog goes up; before it answers, the user opens a piece some
	// other way (a drop, a recent).
	b.launchOpenDialog("")
	dropped := filepath.Join(t.TempDir(), "dropped.gtab")
	if err := os.WriteFile(dropped, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.openPath(dropped)
	if len(op.opened) != 1 {
		t.Fatalf("opened %d pieces, want the dropped one only", len(op.opened))
	}

	// The floating dialog finally answers. Its choice is stale.
	b.OfferDialogResult(`C:\old\choice.gp`, "")
	b.drainDialog()
	if len(op.opened) != 1 {
		t.Errorf("a stale dialog result auto-opened a piece: %v", op.opened)
	}
	if b.dialogBusy {
		t.Error("draining the stale result should still re-arm the dialog")
	}
	if !strings.Contains(b.errMsg, "choice.gp") {
		t.Errorf("the discard should say what it ignored, got %q", b.errMsg)
	}

	// A fresh round trip afterwards works normally.
	b.launchOpenDialog("")
	fresh := filepath.Join(t.TempDir(), "fresh.gtab")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.OfferDialogResult(fresh, "")
	b.drainDialog()
	if len(op.opened) != 2 {
		t.Errorf("a fresh dialog result did not open: %v", op.opened)
	}
}

// --- writing a piece from the start screen -------------------------------

func TestBrowserNewPieceKeyAndButton(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	calls := 0
	b.SetNewPiece(func() { calls++ })
	if err := b.handleKey(ebiten.KeyN); err != nil {
		t.Fatalf("N = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("N opened the editor %d times, want 1", calls)
	}
	r := b.actionRect(t, "new")
	b.handleMouse(pointer{x: r.x + 4, y: r.y + 4, pressed: true, down: true})
	if calls != 2 {
		t.Errorf("the button opened the editor %d times in total, want 2", calls)
	}
}

func TestBrowserNewPieceIsInertWithoutAnEditor(t *testing.T) {
	// A build that wired no editor must not crash on the key; it just does
	// nothing, and the footer does not advertise it.
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	if err := b.handleKey(ebiten.KeyN); err != nil {
		t.Fatalf("N = %v, want nil", err)
	}
	if strings.Contains(b.hintLine(), "new piece") {
		t.Errorf("the footer advertises N with no editor wired: %q", b.hintLine())
	}
}

func TestBrowserEditSelectedPiece(t *testing.T) {
	dir := t.TempDir()
	browserTree(t, dir, "one.gtab", "two.gp")
	pr := &browserFakePrefs{recents: []string{
		filepath.Join(dir, "one.gtab"),
		filepath.Join(dir, "two.gp"),
	}}
	b := browserFixture(t, pr, &browserFakeOpener{})
	var edited []string
	b.SetEditPiece(func(p string) { edited = append(edited, p) })

	b.setSel(0)
	if err := b.handleKey(ebiten.KeyE); err != nil {
		t.Fatalf("E = %v, want nil", err)
	}
	if len(edited) != 1 || edited[0] != pr.recents[0] {
		t.Fatalf("E edited %v, want [%s]", edited, pr.recents[0])
	}
	// An imported format is offered too: the editor opens it and makes the
	// user save it as .gtab, which is a better answer than refusing here.
	b.setSel(1)
	b.handleKey(ebiten.KeyE)
	if len(edited) != 2 || edited[1] != pr.recents[1] {
		t.Errorf("E edited %v, want the .gp second", edited)
	}
}

func TestBrowserEditIsOffWithoutASelection(t *testing.T) {
	// The onboarding checklist has no pieces on it, so there is nothing to
	// edit and the binding says so rather than doing nothing quietly.
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	b.SetEditPiece(func(string) { t.Error("E edited something on the checklist") })
	if _, ok := b.editableSelection(); ok {
		t.Error("the checklist offered a piece to edit")
	}
	b.handleKey(ebiten.KeyE)
}

func TestBrowserEditIsOffForAMissingFile(t *testing.T) {
	pr := &browserFakePrefs{recents: []string{filepath.Join(t.TempDir(), "gone.gtab")}}
	b := browserFixture(t, pr, &browserFakeOpener{})
	b.SetEditPiece(func(string) { t.Error("E opened a file that is not there") })
	if _, ok := b.editableSelection(); ok {
		t.Error("a missing recent was offered for editing")
	}
	b.handleKey(ebiten.KeyE)
}

// actionRect finds one of the action row's buttons by id, so a test can
// click it without repeating the layout arithmetic.
func (b *Browser) actionRect(t *testing.T, id string) rect {
	t.Helper()
	l := b.layout()
	for i, a := range b.actionList() {
		if a.id == id && i < len(l.actions) {
			return l.actions[i]
		}
	}
	t.Fatalf("no action button %q", id)
	return rect{}
}

// --- the library pane ----------------------------------------------------

// stubLibrary is a Library over a fixed list, so the pane can be driven
// without a folder. A nil list is a build that manages an empty library,
// which is a different thing from managing none at all.
type stubLibrary struct {
	dir    string
	pieces []PieceInfo
	err    error
}

func (l stubLibrary) Dir() string { return l.dir }
func (l stubLibrary) Scan() ([]PieceInfo, error) {
	return l.pieces, l.err
}

// waitForLibrary drives the scan through its mailbox: rescanLibrary posts
// from a goroutine and drainLibrary applies it on the game loop, so a test
// has to let the one finish before it asks the other.
func waitForLibrary(t *testing.T, b *Browser) {
	t.Helper()
	for i := 0; i < 200; i++ {
		b.drainLibrary()
		if !b.libScanning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the library scan never finished")
}

func TestBrowserLibraryPaneDescribesPieces(t *testing.T) {
	lib := stubLibrary{dir: `C:\pieces`, pieces: []PieceInfo{
		{Path: `C:\pieces\a.gtab`, Name: "a", Title: "Warmup in A", Summary: "4/4 · 92 BPM · 8 bars"},
		{Path: `C:\pieces\b.gtab`, Name: "b", Summary: "3/4 · 60 BPM · 4 bars"},
	}}
	b := browserLibFixture(t, lib)
	waitForLibrary(t, b)

	rows := b.panes[paneLibrary]
	if len(rows) != 2 {
		t.Fatalf("the library pane has %d rows, want 2", len(rows))
	}
	// A piece with a title is listed BY that title: the library is the
	// pane that knows what the music is.
	if rows[0].name != "Warmup in A" {
		t.Errorf("row 0 is named %q, want the piece's title", rows[0].name)
	}
	if rows[0].sub != "4/4 · 92 BPM · 8 bars" {
		t.Errorf("row 0's description is %q", rows[0].sub)
	}
	// One with no title falls back to the file name rather than a blank.
	if rows[1].name != "b" {
		t.Errorf("an untitled piece is named %q, want its file name", rows[1].name)
	}
	if note, isErr := b.libraryNote(); note != `C:\pieces` || isErr {
		t.Errorf("the pane's note is %q (error=%v), want the folder", note, isErr)
	}
}

func TestBrowserLibraryPaneAbsentWithoutTheService(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	if b.hasLibrary() {
		t.Error("a build with no Library reports one")
	}
	if got := len(b.paneOrder()); got != 2 {
		t.Errorf("got %d panes, want 2 without a library", got)
	}
	// And the focus cannot be moved onto a pane that is not there.
	b.focusPane(paneLibrary)
	if b.focus == paneLibrary {
		t.Error("the focus moved to a library pane that does not exist")
	}
	for i := 0; i < 5; i++ {
		b.stepPane(1)
	}
	if b.focus != paneCreated {
		t.Errorf("stepping right landed on pane %d, want the last one (%d)", b.focus, paneCreated)
	}
}

func TestBrowserWrittenPaneListsCreatedPieces(t *testing.T) {
	dir := t.TempDir()
	browserTree(t, dir, "mine.gtab", "opened.gp")
	pr := &browserFakePrefs{
		recents: []string{filepath.Join(dir, "opened.gp")},
		created: []string{filepath.Join(dir, "mine.gtab")},
	}
	b := browserFixture(t, pr, &browserFakeOpener{})
	if got := len(b.panes[paneRecent]); got != 1 {
		t.Errorf("the recent pane has %d rows, want 1", got)
	}
	if got := len(b.panes[paneCreated]); got != 1 {
		t.Fatalf("the written pane has %d rows, want 1", got)
	}
	if got := b.panes[paneCreated][0].name; got != "mine.gtab" {
		t.Errorf("the written pane lists %q", got)
	}
}

func TestBrowserPanesKeepTheirOwnSelection(t *testing.T) {
	dir := t.TempDir()
	browserTree(t, dir, "a.gp", "b.gp", "c.gp", "x.gtab", "y.gtab")
	pr := &browserFakePrefs{
		recents: []string{filepath.Join(dir, "a.gp"), filepath.Join(dir, "b.gp"), filepath.Join(dir, "c.gp")},
		created: []string{filepath.Join(dir, "x.gtab"), filepath.Join(dir, "y.gtab")},
	}
	b := browserFixture(t, pr, &browserFakeOpener{})
	b.setSel(2)
	b.stepPane(1)
	if b.focus != paneCreated {
		t.Fatalf("focus is pane %d, want the written one", b.focus)
	}
	if b.sel != 0 {
		t.Errorf("the written pane opened on row %d, want 0", b.sel)
	}
	b.setSel(1)
	b.stepPane(-1)
	if b.focus != paneRecent || b.sel != 2 {
		t.Errorf("back on pane %d row %d, want the recent pane's row 2", b.focus, b.sel)
	}
	b.stepPane(1)
	if b.sel != 1 {
		t.Errorf("the written pane came back on row %d, want the 1 it was left on", b.sel)
	}
}

func TestBrowserActivateALibraryPieceOpensIt(t *testing.T) {
	op := &browserFakeOpener{}
	lib := stubLibrary{pieces: []PieceInfo{{Path: `C:\pieces\a.gtab`, Title: "A", Summary: "4/4"}}}
	b := browserLibFixtureWith(t, lib, op)
	waitForLibrary(t, b)
	b.focusPane(paneLibrary)
	b.setSel(0)
	b.activate()
	if len(op.opened) != 1 || op.opened[0] != `C:\pieces\a.gtab` {
		t.Errorf("opened %v, want the library piece", op.opened)
	}
}

func TestBrowserLibraryPieceThatWillNotParse(t *testing.T) {
	op := &browserFakeOpener{}
	lib := stubLibrary{pieces: []PieceInfo{
		{Path: `C:\pieces\broken.gtab`, Name: "broken", Problem: "line 7:14: bar underfull"},
	}}
	b := browserLibFixtureWith(t, lib, op)
	waitForLibrary(t, b)
	b.focusPane(paneLibrary)
	b.setSel(0)

	// Opening it reports the problem rather than handing a broken file to
	// the importer.
	b.activate()
	if len(op.opened) != 0 {
		t.Errorf("a piece that will not parse was opened: %v", op.opened)
	}
	if !strings.Contains(b.errMsg, "bar underfull") {
		t.Errorf("the status says %q, want the parse problem", b.errMsg)
	}
	// But editing it IS offered: the text view is where it gets fixed.
	var edited []string
	b.SetEditPiece(func(p string) { edited = append(edited, p) })
	if _, ok := b.editableSelection(); !ok {
		t.Fatal("a piece that will not parse cannot be opened in the editor")
	}
	b.editSelected()
	if len(edited) != 1 {
		t.Errorf("editing the broken piece ran %d times, want 1", len(edited))
	}
}

// TestBrowserEnterOnABrokenLibraryPieceOpensTheEditor: with an editor
// wired, Enter (and the second click of a double-click) on a piece that
// will not parse goes where the E key goes instead of dead-ending in a
// message about the E key — and the parse error stays in the status so
// the reason for landing in the editor is on screen on the way back.
func TestBrowserEnterOnABrokenLibraryPieceOpensTheEditor(t *testing.T) {
	op := &browserFakeOpener{}
	lib := stubLibrary{pieces: []PieceInfo{
		{Path: `C:\pieces\broken.gtab`, Name: "broken", Problem: "line 7:14: bar underfull"},
	}}
	b := browserLibFixtureWith(t, lib, op)
	waitForLibrary(t, b)
	var edited []string
	b.SetEditPiece(func(p string) { edited = append(edited, p) })
	b.focusPane(paneLibrary)
	b.setSel(0)

	b.activate()
	if len(edited) != 1 || edited[0] != `C:\pieces\broken.gtab` {
		t.Fatalf("Enter edited %v, want the broken piece", edited)
	}
	if len(op.opened) != 0 {
		t.Errorf("a piece that will not parse reached the importer: %v", op.opened)
	}
	if !strings.Contains(b.errMsg, "bar underfull") || !strings.Contains(b.errMsg, "broken.gtab") {
		t.Errorf("the status says %q, want the file and its parse problem", b.errMsg)
	}
	if strings.Contains(b.errMsg, "press E") {
		t.Errorf("the status says %q; the E advice belongs to the build with no editor wired", b.errMsg)
	}
}

func TestBrowserDeleteIsRefusedOnTheLibrary(t *testing.T) {
	lib := stubLibrary{pieces: []PieceInfo{{Path: `C:\pieces\a.gtab`, Title: "A"}}}
	b := browserLibFixture(t, lib)
	waitForLibrary(t, b)
	b.focusPane(paneLibrary)
	b.setSel(0)
	b.forgetRecent()
	if len(b.panes[paneLibrary]) != 1 {
		t.Error("Delete removed a library row")
	}
	if !strings.Contains(b.errMsg, "delete the file") {
		t.Errorf("the status says %q, want an explanation", b.errMsg)
	}
}

func TestBrowserWheelScrollsThePaneUnderTheCursor(t *testing.T) {
	dir := t.TempDir()
	var recents, created []string
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("r%02d.gp", i)
		browserTree(t, dir, name)
		recents = append(recents, filepath.Join(dir, name))
		name = fmt.Sprintf("c%02d.gtab", i)
		browserTree(t, dir, name)
		created = append(created, filepath.Join(dir, name))
	}
	b := browserFixture(t, &browserFakePrefs{recents: recents, created: created}, &browserFakeOpener{})
	l := b.layout()
	// The cursor over the WRITTEN pane, while the keyboard is on RECENT.
	over := l.panes[1]
	for i := 0; i < 4; i++ {
		b.handleMouse(pointer{x: over.x + 10, y: over.y + brwPaneHeadH + 4, wheel: -1})
	}
	if b.focus != paneCreated {
		t.Fatalf("the wheel left the focus on pane %d, want the one under the cursor", b.focus)
	}
	if b.top == 0 {
		t.Error("the pane under the cursor did not scroll")
	}
	if b.saved[paneRecent].top != 0 {
		t.Errorf("the recent pane scrolled to %d without the cursor being over it", b.saved[paneRecent].top)
	}
}

// browserLibFixture builds a browser with a library service and no
// recents, which is what most library tests want.
func browserLibFixture(t *testing.T, lib Library) *Browser {
	t.Helper()
	return browserLibFixtureWith(t, lib, &browserFakeOpener{})
}

func browserLibFixtureWith(t *testing.T, lib Library, op Opener) *Browser {
	t.Helper()
	sh := NewShell(Services{Opener: op, Prefs: &browserFakePrefs{}, Library: lib}, nil)
	return NewBrowser(sh)
}
