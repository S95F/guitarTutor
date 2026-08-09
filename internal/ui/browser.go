package ui

// The start screen: the first thing a user sees when the binary is
// launched with no arguments. Two sections side by side — recent pieces
// on the left, a filtered directory listing on the right — plus
// drag-and-drop onto the window.
//
// Everything the screen decides lives in plain methods on Browser
// (move, activate, setDir, click, handleDrop, handleKey) that take no
// Ebitengine state and can be driven directly from a test; Update only
// translates the keyboard and mouse into those calls, and Draw is a
// projection of the fields they set. That split is what makes the screen
// testable at all: Ebitengine cannot open a window in a unit test.
//
// Drag-and-drop note. ebiten.DroppedFiles returns an fs.FS whose root
// lists the dropped items by base name only — the real path is not part
// of the fs.FS contract. It is recoverable in this version because the
// desktop implementation backs each root entry with os.Open, and the
// *os.File it returns reports the real path from Name(); see
// browserDroppedPaths, which falls back to skipping the items it cannot
// recover — rather than guessing, and rather than throwing away the
// whole drop — if a future implementation stops doing that.

import (
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Layout of the two panes, in logical pixels (screenW x screenH).
const (
	brwRecentX    = 24
	brwRecentW    = 400
	brwBrowseX    = 456
	brwBrowseW    = 800
	brwListTop    = 156
	brwRecentRowH = 36
	brwRecentRows = 11
	brwBrowseRowH = 22
	brwBrowseRows = 18
	brwStatusY    = 578
	brwCharW      = 7 // basicfont is a 7x13 fixed-width face
	brwNameScale  = 1.7
	// brwRecentNameChars is how many scaled-up characters fit across the
	// recents pane: (brwRecentW-16) / (brwCharW * brwNameScale), rounded
	// down. Spelt out because Go will not truncate a constant to int.
	brwRecentNameChars = 32
)

var (
	colBrwPanel   = color.RGBA{24, 24, 32, 255}
	colBrwSel     = color.RGBA{38, 66, 104, 255}
	colBrwSelDim  = color.RGBA{34, 38, 48, 255}
	colBrwHover   = color.RGBA{28, 30, 40, 255}
	colBrwDim     = color.RGBA{132, 132, 148, 255}
	colBrwMissing = color.RGBA{150, 90, 90, 255}
)

// browserPieceExts is the set of piece formats the application imports.
// Anything else is hidden from the listing: an unfiltered view of a real
// Documents folder is unusable.
var browserPieceExts = map[string]bool{
	".gtab":     true,
	".mid":      true,
	".midi":     true,
	".smf":      true,
	".gp":       true,
	".musicxml": true,
	".mxl":      true,
	".xml":      true,
}

// browserSupported reports whether a file name looks like a piece the
// application can import. The comparison is case-insensitive, so SONG.GP
// lists the same as song.gp.
func browserSupported(name string) bool {
	return browserPieceExts[strings.ToLower(filepath.Ext(name))]
}

// A browserEntry is one selectable row: a recent piece, a sub-directory,
// or a piece file in the current directory.
type browserEntry struct {
	name    string // base name, shown prominently
	path    string // full path, what gets opened
	parent  string // containing directory, shown dimmed (recents)
	isDir   bool
	missing bool // a recent whose file is no longer on disk
}

// browserPane names one of the two sections; Tab moves between them.
type browserPane int

// The two sections of the start screen.
const (
	browserPaneRecent browserPane = iota
	browserPaneBrowse
)

// browserRecentRemover is an optional extension of Prefs: an
// implementation that can drop one entry from the recents list. Prefs
// itself has no remove, so when the configured implementation does not
// provide this, forgetting a recent (the Delete key) hides it for the
// rest of the session only.
type browserRecentRemover interface {
	RemoveRecent(path string)
}

// A Browser is the start screen and piece browser. It lists recently
// opened pieces alongside a directory listing filtered to the formats
// the application imports, opens the selection through the Shell, and
// reports import errors and warnings inline rather than propagating them
// — a malformed file must never end the session.
type Browser struct {
	sh    *Shell
	focus browserPane

	recents   []browserEntry
	recentSel int
	recentTop int
	// forgotten holds recents dismissed with Delete, so they stay gone
	// for this session even when Prefs cannot remove them permanently.
	// Opening a piece again clears its entry: a pane that keeps hiding a
	// file the user just opened disagrees with the configuration it is
	// supposed to be showing, and stays wrong until the next restart.
	forgotten map[string]bool
	// recentStatus remembers, per recent path, whether the file was
	// missing the one time it was probed. reloadRecents runs on the game
	// loop — from every open and every Delete — and stat on an
	// unreachable network share blocks for seconds, so a path that is
	// already on the list is never probed a second time.
	recentStatus map[string]bool
	// statFn probes one recent path; nil means os.Stat. Tests replace it
	// to count the probes and to stand in for a path that blocks.
	statFn func(string) (fs.FileInfo, error)

	dir       string
	listing   []browserEntry
	browseSel int
	browseTop int
	dirErr    string

	errMsg string
	warns  []string

	settings func()

	// Mouse state, refreshed every frame by handleMouse.
	hoverOK   bool
	hoverPane browserPane
	hoverIdx  int
	wheelAcc  float64
}

// NewBrowser builds the start screen for sh. It loads the recents list
// from the shell's preferences and opens the directory listing on the
// most recent piece's folder, falling back to the user's home directory.
func NewBrowser(sh *Shell) *Browser {
	b := &Browser{sh: sh, forgotten: map[string]bool{}}
	b.reloadRecents()
	b.setDir(b.startDir())
	if len(b.recents) == 0 {
		b.focus = browserPaneBrowse
	}
	return b
}

// NewBrowserShell builds the application host with the start screen as
// its root, and hands back both so the caller can wire the settings
// opener.
//
// It exists because of a chicken and egg: NewBrowser needs the Shell it
// will talk to, and NewShell needs a root screen. The Shell therefore
// starts on an inert placeholder which the browser replaces before
// anything is drawn — Shell.Update applies deferred stack edits at the
// end of the frame, and Ebitengine draws after updating, so the
// placeholder never reaches the screen.
//
// A build that was given a piece on the command line does not need this:
// it passes its practice screen to NewShell as the root directly.
func NewBrowserShell(svc Services) (*Shell, *Browser) {
	sh := NewShell(svc, browserPlaceholder{})
	b := NewBrowser(sh)
	sh.Replace(b)
	return sh, b
}

// browserPlaceholder is the do-nothing root NewBrowserShell starts with.
type browserPlaceholder struct{}

func (browserPlaceholder) Update() error          { return nil }
func (browserPlaceholder) Draw(dst *ebiten.Image) { dst.Fill(colBG) }

// SetSettingsOpener installs the action bound to the S key. The UI
// package cannot construct the settings screen itself, so the
// application wires it here; until it does, S is inert and the hint for
// it is not drawn.
func (b *Browser) SetSettingsOpener(fn func()) { b.settings = fn }

// prefs returns the preferences facade, or nil when the shell has none.
func (b *Browser) prefs() Prefs {
	if b.sh == nil {
		return nil
	}
	return b.sh.Services().Prefs
}

// startDir picks the directory the browser opens on: the folder holding
// the most recent piece if it still exists, otherwise the user's home,
// otherwise the working directory.
func (b *Browser) startDir() string {
	for _, e := range b.recents {
		if e.missing {
			continue
		}
		if d := filepath.Dir(e.path); browserIsDir(d) {
			return d
		}
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func browserIsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// statRecent probes one recent path. Production stats it; tests replace
// the hook.
func (b *Browser) statRecent(path string) (fs.FileInfo, error) {
	if b.statFn != nil {
		return b.statFn(path)
	}
	return os.Stat(path)
}

// reloadRecents rebuilds the recents pane from Prefs, dropping entries
// forgotten this session and flagging the ones whose file has gone away.
//
// It runs on the game loop, and it is called again after every open and
// every Delete, so it must not touch the filesystem for paths it has
// already seen: one recent on a disconnected network share would
// otherwise stall the window for seconds each time. A path's status is
// therefore probed exactly once — when it first appears on the list —
// and carried forward from then on. The only paths that appear later in
// a session are pieces the opener has just read successfully, so the
// probe that does happen on the loop is of a file known to be reachable.
// The cache is rebuilt from the current list on each pass, so a path
// that leaves the recents stops being remembered.
func (b *Browser) reloadRecents() {
	b.recents = nil
	known := b.recentStatus
	status := make(map[string]bool, len(known))
	if pr := b.prefs(); pr != nil {
		for _, p := range pr.Recents() {
			if b.forgotten[p] {
				continue
			}
			missing, seen := known[p]
			if !seen {
				fi, err := b.statRecent(p)
				missing = err != nil || fi == nil || fi.IsDir()
			}
			status[p] = missing
			b.recents = append(b.recents, browserEntry{
				name:    filepath.Base(p),
				path:    p,
				parent:  filepath.Dir(p),
				missing: missing,
			})
		}
	}
	b.recentStatus = status
	b.recentSel = browserClamp(b.recentSel, len(b.recents))
	b.recentTop = browserClampTop(b.recentSel, b.recentTop, brwRecentRows, len(b.recents))
}

// browserReadListing reads dir and returns the rows worth showing:
// sub-directories and files whose extension the application imports,
// directories first and then names, both case-insensitively. Dot-files
// are hidden. The error is the directory read failure, if any; the
// caller shows it inline.
func browserReadListing(dir string) ([]browserEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]browserEntry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		isDir := de.IsDir()
		// A symlink reports neither dir nor regular from ReadDir; stat
		// it so linked folders are still browsable.
		if !isDir && de.Type()&os.ModeSymlink != 0 {
			isDir = browserIsDir(filepath.Join(dir, name))
		}
		if !isDir && !browserSupported(name) {
			continue
		}
		out = append(out, browserEntry{name: name, path: filepath.Join(dir, name), isDir: isDir})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir
		}
		li, lj := strings.ToLower(out[i].name), strings.ToLower(out[j].name)
		if li != lj {
			return li < lj
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// browserParentOf returns the parent of dir and whether there is one.
// At a filesystem root (/ on Unix, C:\ on Windows) there is not, and the
// second result is false.
func browserParentOf(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	p := filepath.Dir(dir)
	if p == dir {
		return dir, false
	}
	return p, true
}

// setDir points the browse pane at dir. A directory that cannot be read
// still becomes the current directory — with the failure recorded in
// dirErr and an empty listing — so the user can always back out of it
// with Backspace instead of being stuck.
func (b *Browser) setDir(dir string) {
	dir = filepath.Clean(dir)
	b.dir = dir
	b.dirErr = ""
	kids, err := browserReadListing(dir)
	if err != nil {
		b.dirErr = browserErrText(err)
		kids = nil
	}
	b.listing = nil
	if p, ok := browserParentOf(dir); ok {
		b.listing = append(b.listing, browserEntry{name: "..", path: p, isDir: true})
	}
	b.listing = append(b.listing, kids...)
	b.browseSel, b.browseTop = 0, 0
}

// browserErrText renders a filesystem error for inline display without
// repeating the path the pane is already showing.
func browserErrText(err error) string {
	if pe, ok := err.(*fs.PathError); ok && pe.Err != nil {
		return pe.Err.Error()
	}
	return err.Error()
}

// selectName moves the browse selection to the entry with this base
// name, if it is present. Used when going up so the folder just left is
// highlighted.
func (b *Browser) selectName(name string) {
	for i, e := range b.listing {
		if e.name == name {
			b.browseSel = i
			b.browseTop = browserClampTop(i, b.browseTop, brwBrowseRows, len(b.listing))
			return
		}
	}
}

// goParent moves the browse pane up one directory, keeping the folder
// just left selected. At a filesystem root it does nothing.
func (b *Browser) goParent() {
	p, ok := browserParentOf(b.dir)
	if !ok {
		return
	}
	leaving := filepath.Base(b.dir)
	b.setDir(p)
	b.focus = browserPaneBrowse
	b.selectName(leaving)
}

// browserClamp constrains an index to [0, n), returning 0 for an empty
// list.
func browserClamp(i, n int) int {
	if n <= 0 || i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// browserClampTop returns the scroll offset that keeps sel visible in a
// viewport of rows rows over a list of n entries, moving as little as
// possible.
func browserClampTop(sel, top, rows, n int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	if sel < top {
		top = sel
	}
	if sel >= top+rows {
		top = sel - rows + 1
	}
	if top > n-rows {
		top = n - rows
	}
	if top < 0 {
		top = 0
	}
	return top
}

// paneRows reports the viewport height of a pane in rows.
func (b *Browser) paneRows(p browserPane) int {
	if p == browserPaneRecent {
		return brwRecentRows
	}
	return brwBrowseRows
}

// paneLen reports how many entries a pane holds.
func (b *Browser) paneLen(p browserPane) int {
	if p == browserPaneRecent {
		return len(b.recents)
	}
	return len(b.listing)
}

// selection reports the selected index in a pane.
func (b *Browser) selection(p browserPane) int {
	if p == browserPaneRecent {
		return b.recentSel
	}
	return b.browseSel
}

// setSelection selects an index in a pane, clamping it and scrolling the
// viewport so it stays visible.
func (b *Browser) setSelection(p browserPane, i int) {
	i = browserClamp(i, b.paneLen(p))
	if p == browserPaneRecent {
		b.recentSel = i
		b.recentTop = browserClampTop(i, b.recentTop, brwRecentRows, len(b.recents))
		return
	}
	b.browseSel = i
	b.browseTop = browserClampTop(i, b.browseTop, brwBrowseRows, len(b.listing))
}

// move steps the focused pane's selection by delta, clamping at both
// ends and scrolling the viewport to follow.
func (b *Browser) move(delta int) {
	b.setSelection(b.focus, b.selection(b.focus)+delta)
}

// scrollBy scrolls a pane's viewport by delta rows without moving the
// selection — the mouse wheel's behaviour.
func (b *Browser) scrollBy(p browserPane, delta int) {
	rows, n := b.paneRows(p), b.paneLen(p)
	max := n - rows
	if max < 0 {
		max = 0
	}
	top := b.paneTop(p) + delta
	if top > max {
		top = max
	}
	if top < 0 {
		top = 0
	}
	if p == browserPaneRecent {
		b.recentTop = top
	} else {
		b.browseTop = top
	}
}

// paneTop reports a pane's scroll offset.
func (b *Browser) paneTop(p browserPane) int {
	if p == browserPaneRecent {
		return b.recentTop
	}
	return b.browseTop
}

// toggleFocus moves between the recent and browse sections.
func (b *Browser) toggleFocus() {
	if b.focus == browserPaneRecent {
		b.focus = browserPaneBrowse
	} else {
		b.focus = browserPaneRecent
	}
}

// activate opens whatever is selected in the focused pane: a directory
// is descended into, a piece is opened, a recent that has gone missing
// says so instead.
func (b *Browser) activate() {
	switch b.focus {
	case browserPaneRecent:
		if b.recentSel >= len(b.recents) {
			return
		}
		e := b.recents[b.recentSel]
		if e.missing {
			b.errMsg = "not found: " + e.path + "  (press Del to forget it)"
			return
		}
		b.openPath(e.path)
	default:
		if b.browseSel >= len(b.listing) {
			return
		}
		e := b.listing[b.browseSel]
		if e.isDir {
			if e.name == ".." {
				b.goParent()
				return
			}
			b.setDir(e.path)
			b.focus = browserPaneBrowse
			return
		}
		b.openPath(e.path)
	}
}

// openPath opens a piece, or browses to it when it is a directory. Any
// import failure is recorded for inline display and never propagated: a
// malformed file must not end the session.
func (b *Browser) openPath(path string) {
	if browserIsDir(path) {
		b.setDir(path)
		b.focus = browserPaneBrowse
		return
	}
	b.errMsg, b.warns = "", nil
	if b.sh == nil || b.sh.Services().Opener == nil {
		b.errMsg = "no importer is available in this build"
		return
	}
	warns, err := b.sh.OpenPiece(path)
	b.warns = warns
	if err != nil {
		b.errMsg = fmt.Sprintf("cannot open %s: %v", filepath.Base(path), err)
		return
	}
	// The shell just recorded it; show it at the top of the list for
	// when the user comes back from practising. Opening a piece also
	// un-forgets it: it is back in the configuration's recents, and a
	// pane that went on hiding it would contradict what was saved until
	// the next restart.
	delete(b.forgotten, path)
	delete(b.recentStatus, path)
	b.reloadRecents()
	b.setSelection(browserPaneRecent, 0)
}

// forgetRecent (the Delete key) removes the selected recent. It is
// permanent when the preferences implementation supports removal, and
// otherwise lasts for the session.
func (b *Browser) forgetRecent() {
	if b.focus != browserPaneRecent || b.recentSel >= len(b.recents) {
		return
	}
	p := b.recents[b.recentSel].path
	if b.forgotten == nil {
		b.forgotten = map[string]bool{}
	}
	b.forgotten[p] = true
	if pr := b.prefs(); pr != nil {
		if rm, ok := pr.(browserRecentRemover); ok {
			rm.RemoveRecent(p)
			// A failed write must not block the UI; settings surfaces it.
			_ = pr.Save()
		}
	}
	b.reloadRecents()
}

// browserDroppedPaths recovers the real paths behind a dropped-files
// fs.FS, and reports how many of the dropped items it had to give up on.
// The root of that filesystem lists the dropped items by base name; the
// real path comes back from the *os.File the desktop implementation
// opens them with. Anything whose path cannot be recovered and confirmed
// on disk is skipped rather than guessed at — one unusable item must not
// cost the user the rest of the drop.
//
// The listing is taken as far as it goes even when reading it failed:
// the desktop implementation stats each dropped item and, when one of
// them cannot be stat'd, hands back a slice that is the right length but
// holds a nil fs.DirEntry at and after the failure, alongside the error.
// Calling Name on one of those nils panics, so the nil check below is
// load-bearing, not defensive.
func browserDroppedPaths(fsys fs.FS) (paths []string, skipped int) {
	if fsys == nil {
		return nil, 0
	}
	ents, err := fs.ReadDir(fsys, ".")
	if err != nil && len(ents) == 0 {
		return nil, 0
	}
	for _, de := range ents {
		if de == nil {
			skipped++
			continue
		}
		f, err := fsys.Open(de.Name())
		if err != nil {
			skipped++
			continue
		}
		real := ""
		if named, ok := f.(interface{ Name() string }); ok {
			real = named.Name()
		}
		_ = f.Close()
		if real == "" || filepath.Base(real) != de.Name() {
			skipped++
			continue
		}
		if _, err := os.Stat(real); err != nil {
			skipped++
			continue
		}
		paths = append(paths, real)
	}
	return paths, skipped
}

// browserSkipNote describes the dropped items that could not be read, in
// the singular or the plural.
func browserSkipNote(n int) string {
	if n == 1 {
		return "1 dropped item could not be read and was skipped"
	}
	return fmt.Sprintf("%d dropped items could not be read and were skipped", n)
}

// noteSkipped appends the skipped-items note to whatever the drop has
// already reported, so the outcome is never silent.
func (b *Browser) noteSkipped(skipped int) {
	if skipped <= 0 {
		return
	}
	if b.errMsg == "" {
		b.errMsg = browserSkipNote(skipped)
		return
	}
	b.errMsg += "; " + browserSkipNote(skipped)
}

// handleDrop acts on files dropped onto the window: the first supported
// piece is opened, or the first directory is browsed to. Items the
// platform could not hand over are skipped, and the outcome is always
// reported — including a drop of nothing usable, which must say so
// rather than fail silently.
func (b *Browser) handleDrop(fsys fs.FS) {
	paths, skipped := browserDroppedPaths(fsys)
	if len(paths) == 0 {
		b.errMsg = "nothing dropped on the window could be opened"
		b.noteSkipped(skipped)
		return
	}
	for _, p := range paths {
		if browserIsDir(p) {
			b.setDir(p)
			b.focus = browserPaneBrowse
			b.errMsg = ""
			b.noteSkipped(skipped)
			return
		}
	}
	for _, p := range paths {
		if browserSupported(p) {
			// openPath resets the status line and fills it in on failure.
			b.openPath(p)
			b.noteSkipped(skipped)
			return
		}
	}
	b.errMsg = "dropped file is not a piece guitarTutor can open: " + filepath.Base(paths[0])
	b.noteSkipped(skipped)
}

// browserKeys are every key the start screen reacts to.
var browserKeys = []ebiten.Key{
	ebiten.KeyUp, ebiten.KeyDown, ebiten.KeyPageUp, ebiten.KeyPageDown,
	ebiten.KeyHome, ebiten.KeyEnd,
	ebiten.KeyEnter, ebiten.KeyNumpadEnter, ebiten.KeyBackspace,
	ebiten.KeyTab, ebiten.KeyDelete, ebiten.KeyS,
	ebiten.KeyEscape, ebiten.KeyQ,
}

// browserRepeatKeys auto-repeat while held; everything else fires once
// per press, because a held Enter opening a piece repeatedly would be a
// disaster.
var browserRepeatKeys = map[ebiten.Key]bool{
	ebiten.KeyUp: true, ebiten.KeyDown: true,
	ebiten.KeyPageUp: true, ebiten.KeyPageDown: true,
}

// browserKeyFires decides whether a key held for d frames acts this
// frame: on the press, then after a hold delay at a steady rate.
func browserKeyFires(repeat bool, d int) bool {
	if d == 1 {
		return true
	}
	if !repeat {
		return false
	}
	return d > 30 && (d-30)%4 == 0
}

// handleKey applies one key press. It returns errQuit when the user
// leaves the screen — which at the root of the stack exits the
// application — and nil otherwise.
func (b *Browser) handleKey(k ebiten.Key) error {
	switch k {
	case ebiten.KeyEscape, ebiten.KeyQ:
		return errQuit
	case ebiten.KeyUp:
		b.move(-1)
	case ebiten.KeyDown:
		b.move(1)
	case ebiten.KeyPageUp:
		b.move(-b.paneRows(b.focus))
	case ebiten.KeyPageDown:
		b.move(b.paneRows(b.focus))
	case ebiten.KeyHome:
		b.setSelection(b.focus, 0)
	case ebiten.KeyEnd:
		b.setSelection(b.focus, b.paneLen(b.focus)-1)
	case ebiten.KeyEnter, ebiten.KeyNumpadEnter:
		b.activate()
	case ebiten.KeyBackspace:
		b.goParent()
	case ebiten.KeyTab:
		b.toggleFocus()
	case ebiten.KeyDelete:
		b.forgetRecent()
	case ebiten.KeyS:
		if b.settings != nil {
			b.settings()
		}
	}
	return nil
}

// browserWheelSteps splits an accumulated wheel delta into whole
// notches and the remainder to carry into the next frame, so a trackpad
// producing fractional deltas still scrolls smoothly.
func browserWheelSteps(acc float64) (steps int, rem float64) {
	steps = int(acc)
	return steps, acc - float64(steps)
}

// hitTest maps a cursor position to the pane and entry index under it.
func (b *Browser) hitTest(x, y int) (browserPane, int, bool) {
	if y < brwListTop {
		return 0, 0, false
	}
	switch {
	case x >= brwRecentX && x < brwRecentX+brwRecentW:
		row := (y - brwListTop) / brwRecentRowH
		if row < 0 || row >= brwRecentRows {
			return 0, 0, false
		}
		i := b.recentTop + row
		if i >= len(b.recents) {
			return 0, 0, false
		}
		return browserPaneRecent, i, true
	case x >= brwBrowseX && x < brwBrowseX+brwBrowseW:
		row := (y - brwListTop) / brwBrowseRowH
		if row < 0 || row >= brwBrowseRows {
			return 0, 0, false
		}
		i := b.browseTop + row
		if i >= len(b.listing) {
			return 0, 0, false
		}
		return browserPaneBrowse, i, true
	}
	return 0, 0, false
}

// click applies a left click on an entry: the first click focuses the
// pane and selects the row, a click on the row that is already selected
// opens it — which is also what the second click of a double click
// lands on.
func (b *Browser) click(p browserPane, i int) {
	if b.focus == p && b.selection(p) == i {
		b.activate()
		return
	}
	b.focus = p
	b.setSelection(p, i)
}

// handleMouse reads the pointer once per frame: hover highlighting,
// wheel scrolling of the pane under the cursor, and clicks.
func (b *Browser) handleMouse() {
	x, y := ebiten.CursorPosition()
	p, i, ok := b.hitTest(x, y)
	b.hoverOK, b.hoverPane, b.hoverIdx = ok, p, i

	if _, dy := ebiten.Wheel(); dy != 0 {
		b.wheelAcc += dy
		var steps int
		steps, b.wheelAcc = browserWheelSteps(b.wheelAcc)
		if steps != 0 {
			target := b.focus
			if ok {
				target = p
			}
			b.scrollBy(target, -steps*3)
		}
	}
	if ok && inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		b.click(p, i)
	}
}

// queuedEdits reports how many stack edits the shell is holding for the
// end of this frame. It goes up the moment one of this screen's actions
// pushes another screen, which is how the input loop below knows to stop.
func (b *Browser) queuedEdits() int {
	if b.sh == nil {
		return 0
	}
	return len(b.sh.pending)
}

// browserKeyFiresNow reports whether a key acts this frame, reading
// Ebitengine's press durations. Split out so handleKeys can be driven
// from a test with a canned set of presses.
func browserKeyFiresNow(k ebiten.Key) bool {
	return browserKeyFires(browserRepeatKeys[k], inpututil.KeyPressDuration(k))
}

// handleKeys applies every key that fires this frame, asking fires about
// each one in turn, and stops at the first key that changes the screen
// stack.
//
// Stopping matters: opening a piece only queues the push, which the
// Shell applies at the end of the frame. A later key in the same frame
// that returned errQuit would have the Shell pop this screen and apply
// the push in the same pass — building a practice screen it immediately
// discards, without the CloseCurrent that releases its audio stream, and
// swallowing the quit into the bargain. One action per frame is enough.
func (b *Browser) handleKeys(fires func(ebiten.Key) bool) error {
	queued := b.queuedEdits()
	for _, k := range browserKeys {
		if !fires(k) {
			continue
		}
		if err := b.handleKey(k); err != nil {
			return err
		}
		if b.queuedEdits() != queued {
			return nil
		}
	}
	return nil
}

// Update translates this frame's input into state changes. It returns
// errQuit when the user leaves the screen; import failures are held for
// display instead of being returned, so a bad file cannot end the app.
func (b *Browser) Update() error {
	queued := b.queuedEdits()
	if fsys := ebiten.DroppedFiles(); fsys != nil {
		b.handleDrop(fsys)
	}
	if b.queuedEdits() != queued {
		return nil
	}
	if err := b.handleKeys(browserKeyFiresNow); err != nil {
		return err
	}
	if b.queuedEdits() != queued {
		return nil
	}
	b.handleMouse()
	return nil
}

// hintLine is the one-line key summary under the title. The settings
// hint appears only once an opener has been installed, and the last key
// is honest about whether leaving quits or goes back.
func (b *Browser) hintLine() string {
	parts := []string{
		"up/dn select", "enter open", "backspace up a folder",
		"tab switch pane", "del forget recent",
	}
	if b.settings != nil {
		parts = append(parts, "s settings")
	}
	leave := "esc quit"
	if b.sh != nil && b.sh.Depth() > 1 {
		leave = "esc back"
	}
	return strings.Join(append(parts, leave), "   ")
}

// browserTruncate shortens a string to max characters, keeping the
// front — the right choice for names.
func browserTruncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// browserEllipsize shortens a string to max characters, keeping the
// tail — the right choice for paths, where the last folders identify it.
func browserEllipsize(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[len(s)-max:]
	}
	return "..." + s[len(s)-(max-3):]
}

// Draw paints the start screen. It reads only fields the methods above
// set, so nothing here decides anything.
func (b *Browser) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)

	drawTextScaled(screen, "guitarTutor", brwRecentX, 22, 2.2, colNote)
	drawText(screen, b.hintLine(), brwRecentX, 74, colBrwDim)
	drawText(screen, "drop a Guitar Pro, MusicXML or MIDI file on this window to open it", brwRecentX, 94, colBarline)

	b.drawRecents(screen)
	b.drawBrowse(screen)
	b.drawStatus(screen)
}

// drawPaneFrame paints a pane's background, title and focus underline.
func (b *Browser) drawPaneFrame(screen *ebiten.Image, p browserPane, x, w float32, title string) {
	rows, rowH := b.paneRows(p), brwRecentRowH
	if p == browserPaneBrowse {
		rowH = brwBrowseRowH
	}
	h := float32(rows * rowH)
	vector.DrawFilledRect(screen, x-8, brwListTop-8, w+16, h+16, colBrwPanel, false)
	col := colBrwDim
	if b.focus == p {
		col = colSounding
	}
	drawText(screen, title, float64(x), 124, col)
	vector.StrokeLine(screen, x, 142, x+w, 142, 1, col, false)
}

// rowBG returns the background colour for a row, or false when it needs
// none.
func (b *Browser) rowBG(p browserPane, i int) (color.RGBA, bool) {
	if b.selection(p) == i && b.paneLen(p) > 0 {
		if b.focus == p {
			return colBrwSel, true
		}
		return colBrwSelDim, true
	}
	if b.hoverOK && b.hoverPane == p && b.hoverIdx == i {
		return colBrwHover, true
	}
	return color.RGBA{}, false
}

func (b *Browser) drawRecents(screen *ebiten.Image) {
	b.drawPaneFrame(screen, browserPaneRecent, brwRecentX, brwRecentW, "RECENT PIECES")
	if len(b.recents) == 0 {
		drawText(screen, "No recent pieces yet.", brwRecentX+4, brwListTop+8, colBrwDim)
		drawText(screen, "Browse on the right, or drop a file", brwRecentX+4, brwListTop+32, colBarline)
		drawText(screen, "onto this window.", brwRecentX+4, brwListTop+50, colBarline)
		return
	}
	pathChars := (brwRecentW - 16) / brwCharW
	for row := 0; row < brwRecentRows; row++ {
		i := b.recentTop + row
		if i >= len(b.recents) {
			break
		}
		e := b.recents[i]
		y := float32(brwListTop + row*brwRecentRowH)
		if bg, ok := b.rowBG(browserPaneRecent, i); ok {
			vector.DrawFilledRect(screen, brwRecentX-4, y-2, brwRecentW+8, brwRecentRowH-2, bg, false)
		}
		nameCol := colNote
		sub := e.parent
		if e.missing {
			nameCol = colBrwMissing
			sub = "missing - press Del to forget"
		}
		drawTextScaled(screen, browserTruncate(e.name, brwRecentNameChars), brwRecentX+4, float64(y), brwNameScale, nameCol)
		drawText(screen, browserEllipsize(sub, pathChars), brwRecentX+4, float64(y)+21, colBrwDim)
	}
	if len(b.recents) > brwRecentRows {
		drawText(screen, fmt.Sprintf("%d-%d of %d", b.recentTop+1,
			browserMin(b.recentTop+brwRecentRows, len(b.recents)), len(b.recents)),
			brwRecentX+4, brwListTop+brwRecentRows*brwRecentRowH+12, colBarline)
	}
}

func (b *Browser) drawBrowse(screen *ebiten.Image) {
	title := "BROWSE   " + browserEllipsize(b.dir, (brwBrowseW-80)/brwCharW)
	b.drawPaneFrame(screen, browserPaneBrowse, brwBrowseX, brwBrowseW, title)

	if b.dirErr != "" {
		drawText(screen, "cannot read this folder: "+b.dirErr, brwBrowseX+4, brwListTop+8, colMiss)
		drawText(screen, "press Backspace to go back up", brwBrowseX+4, brwListTop+28, colBarline)
		return
	}
	nameChars := (brwBrowseW - 16) / brwCharW
	for row := 0; row < brwBrowseRows; row++ {
		i := b.browseTop + row
		if i >= len(b.listing) {
			break
		}
		e := b.listing[i]
		y := float32(brwListTop + row*brwBrowseRowH)
		if bg, ok := b.rowBG(browserPaneBrowse, i); ok {
			vector.DrawFilledRect(screen, brwBrowseX-4, y-3, brwBrowseW+8, brwBrowseRowH, bg, false)
		}
		label, col := e.name, colNote
		if e.isDir {
			col = colInferred
			if e.name == ".." {
				label = ".. (up one folder)"
			} else {
				label += "/"
			}
		}
		drawText(screen, browserTruncate(label, nameChars), brwBrowseX+4, float64(y), col)
	}
	if len(b.listing) == 0 {
		drawText(screen, "No pieces or folders here.", brwBrowseX+4, brwListTop+8, colBrwDim)
	}
	if len(b.listing) > brwBrowseRows {
		drawText(screen, fmt.Sprintf("%d-%d of %d", b.browseTop+1,
			browserMin(b.browseTop+brwBrowseRows, len(b.listing)), len(b.listing)),
			brwBrowseX+4, brwListTop+brwBrowseRows*brwBrowseRowH+12, colBarline)
	}
}

// drawStatus paints the last open's error and importer warnings under
// the panes.
func (b *Browser) drawStatus(screen *ebiten.Image) {
	const maxWarn = 4
	width := (screenW - 2*brwRecentX) / brwCharW
	y := float64(brwStatusY)
	if b.errMsg != "" {
		drawText(screen, browserEllipsize(b.errMsg, width), brwRecentX, y, colMiss)
		y += 20
	}
	for i, w := range b.warns {
		if i == maxWarn {
			drawText(screen, fmt.Sprintf("... and %d more warnings", len(b.warns)-maxWarn), brwRecentX, y, colClose)
			break
		}
		drawText(screen, browserTruncate("warning: "+w, width), brwRecentX, y, colClose)
		y += 18
	}
}

func browserMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
