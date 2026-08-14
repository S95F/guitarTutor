package ui

// The start screen: the first thing a user sees when the binary is
// launched with no arguments. One list — recently opened pieces, or the
// getting-started checklist until there are any — beside an open card,
// plus drag-and-drop onto the window.
//
// Opening a file goes through the OPERATING SYSTEM's file dialog, not an
// in-app directory listing. The screen used to carry its own folder pane
// and a separate picker screen; both are gone, because a re-implemented
// file browser is always the worst file browser on the machine — no
// search, no pins, no network places, none of the muscle memory the real
// one has earned. The dialog blocks, so the integrator runs it on its
// own goroutine and posts the outcome to a mailbox this screen drains on
// the game loop (OfferDialogResult) — the same pattern the settings
// screen uses for its calibration wizard.
//
// Everything the screen decides lives in plain methods (move, activate,
// click, handleDrop, handleKey, launchOpenDialog) that take no Ebitengine
// state and can be driven directly from a test; Update only translates
// the keyboard and mouse into those calls, and Draw is a projection of
// the fields they set.
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
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Layout, in logical pixels (screenW x screenH). The recents list owns
// the left, the open card the right.
const (
	brwRecentX    = 24
	brwRecentW    = 600
	brwCardX      = 672
	brwCardW      = 584
	brwListTop    = 156
	brwRecentRowH = 40
	brwRecentRows = 10
	// brwStatusY leaves room for the worst-case status stack — an error
	// line plus four warnings plus the overflow line — to end above the
	// footer at uiFooterY (a browser test pins the arithmetic).
	brwStatusY   = 582
	brwNameScale = 1.35
	brwOpenBtnW  = 280.0
	brwOpenBtnH  = 48.0
)

// colMissing tints a recent whose file is no longer on disk. Every other
// colour this screen uses is the shared palette in theme.go.
var colMissing = color.RGBA{150, 90, 90, 255}

// browserPieceExts is the set of piece formats the application imports.
// It feeds both the drag-and-drop filter here and, through
// PieceExtensions, the file dialog's filter — one source of truth, so
// the dialog can never offer a file the drop path would reject.
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

// PieceExtensions returns the piece formats the application imports,
// sorted, with their leading dots. The integrator builds the OS file
// dialog's filter from this.
func PieceExtensions() []string {
	out := make([]string, 0, len(browserPieceExts))
	for e := range browserPieceExts {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// browserSupported reports whether a file name looks like a piece the
// application can import. The comparison is case-insensitive, so SONG.GP
// lists the same as song.gp.
func browserSupported(name string) bool {
	return browserPieceExts[strings.ToLower(filepath.Ext(name))]
}

// A browserEntry is one selectable row of the recents list.
type browserEntry struct {
	name    string // base name, shown prominently
	path    string // full path, what gets opened
	parent  string // containing directory, shown dimmed
	missing bool   // a recent whose file is no longer on disk
}

// browserRecentRemover is an optional extension of Prefs: an
// implementation that can drop one entry from the recents list. Prefs
// itself has no remove, so when the configured implementation does not
// provide this, forgetting a recent (the Delete key) hides it for the
// rest of the session only.
type browserRecentRemover interface {
	RemoveRecent(path string)
}

// dialogResult is what the file-dialog goroutine posts back: a chosen
// path, an error worth showing, or neither (the user cancelled). gen is
// the browser's dialog generation at the moment the result was posted;
// a result whose generation predates the last piece open is stale.
type dialogResult struct {
	path string
	err  string
	gen  int
}

// A Browser is the start screen. It lists recently opened pieces (or the
// first-run checklist), launches the OS file dialog to open new ones,
// opens the selection through the Shell, and reports import errors and
// warnings inline rather than propagating them — a malformed file must
// never end the session.
type Browser struct {
	sh *Shell

	recents []browserEntry
	sel     int
	top     int
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

	errMsg string
	warns  []string

	settings func()
	// openDialog launches the OS file dialog rooted at the given
	// directory. The integrator wires it (SetOpenDialog); it must not
	// block — it starts a goroutine that eventually posts to
	// OfferDialogResult. nil means no dialog is available in this build,
	// and the open card says so instead of silently doing nothing.
	openDialog func(startDir string)
	// dialogBusy is true from launch until the result (or cancel) comes
	// back, so a double-click cannot stack two Explorer windows. Game
	// loop owned; the mailbox drain clears it.
	dialogBusy bool

	// helpOpen is the ?/F1 key-binding overlay. While it is up nothing
	// else on the screen reacts, so a key pressed to dismiss it cannot
	// also open a piece behind it.
	helpOpen bool

	// Mouse state, refreshed every frame.
	ptr      pointer
	hoverIdx int // recents row under the cursor, -1 for none
	wheelAcc float64

	// mu guards the dialog mailbox, written by the dialog goroutine and
	// drained by Update on the game loop. dialogGen (also under mu) is
	// bumped every time a piece opens: a dialog left floating while the
	// user opened something else by other means must not auto-open its
	// half-hour-old choice the moment the start screen is next shown
	// (verification follow-up).
	mu        sync.Mutex
	pending   *dialogResult
	dialogGen int
	// launchGen is the generation the outstanding dialog was LAUNCHED
	// under; its result is stamped with this, not with the generation at
	// the moment the user finally answers — that is the whole point.
	launchGen int
}

// NewBrowser builds the start screen for sh, loading the recents list
// from the shell's preferences.
func NewBrowser(sh *Shell) *Browser {
	b := &Browser{sh: sh, forgotten: map[string]bool{}, hoverIdx: -1}
	b.reloadRecents()
	return b
}

// NewBrowserShell builds the application host with the start screen as
// its root, and hands back both so the caller can wire the settings and
// dialog openers.
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

// SetOpenDialog installs the OS file dialog launcher behind the open
// card, the O key, and a dropped folder. fn receives the directory the
// dialog should start in and must return immediately, posting its
// outcome to OfferDialogResult from whatever goroutine runs the dialog.
func (b *Browser) SetOpenDialog(fn func(startDir string)) { b.openDialog = fn }

// OfferDialogResult posts the file dialog's outcome. Safe from any
// goroutine. An empty path with an empty error is a cancel and does
// nothing beyond re-arming the dialog.
func (b *Browser) OfferDialogResult(path, errMsg string) {
	b.mu.Lock()
	b.pending = &dialogResult{path: path, err: errMsg, gen: b.launchGen}
	b.mu.Unlock()
}

// drainDialog applies a posted dialog outcome on the game loop.
func (b *Browser) drainDialog() {
	b.mu.Lock()
	res := b.pending
	b.pending = nil
	b.mu.Unlock()
	if res == nil {
		return
	}
	b.dialogBusy = false
	b.mu.Lock()
	stale := res.gen != b.dialogGen
	b.mu.Unlock()
	if stale {
		// The user opened something else while this dialog floated; its
		// choice is an answer to a question nobody is asking any more.
		if res.path != "" {
			b.errMsg = "ignored " + filepath.Base(res.path) + ", chosen while another piece was opening — press O to open it now"
		}
		return
	}
	switch {
	case res.err != "":
		b.errMsg = "could not open the file dialog: " + res.err
	case res.path != "":
		b.openPath(res.path)
	}
}

// launchOpenDialog starts the OS file dialog rooted where the user's
// pieces live. A dialog already in flight is left alone: two Explorer
// windows fighting over one mailbox helps nobody.
func (b *Browser) launchOpenDialog(startDir string) {
	if b.openDialog == nil {
		b.errMsg = "no file dialog is available in this build"
		return
	}
	if b.dialogBusy {
		return
	}
	if startDir == "" {
		startDir = b.startDir()
	}
	b.dialogBusy = true
	b.errMsg = ""
	b.mu.Lock()
	b.launchGen = b.dialogGen
	b.mu.Unlock()
	b.openDialog(startDir)
}

// prefs returns the preferences facade, or nil when the shell has none.
func (b *Browser) prefs() Prefs {
	if b.sh == nil {
		return nil
	}
	return b.sh.Services().Prefs
}

// startDir picks the directory the file dialog opens on: the folder
// holding the most recent piece if it still exists, otherwise the user's
// home, otherwise the working directory.
// It deliberately does NOT stat anything: a recent that is not marked
// missing had its file probed when it joined the list, and re-checking
// its folder here would block the game loop for the SMB timeout every
// time O is pressed while a network share naps — the exact stall the
// recentStatus cache exists to avoid (verification follow-up). If the
// folder has since vanished, the OS dialog falls back to its own
// default, which is the graceful outcome anyway.
func (b *Browser) startDir() string {
	for _, e := range b.recents {
		if e.missing {
			continue
		}
		return filepath.Dir(e.path)
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

// reloadRecents rebuilds the recents list from Prefs, dropping entries
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
	b.setSel(b.sel)
}

// listLen is how many rows the list holds: recents, or the checklist
// standing in for them on a first run.
func (b *Browser) listLen() int {
	if b.onboarding() {
		return len(b.stepList())
	}
	return len(b.recents)
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

// setSel selects a row, clamping it and scrolling the viewport so it
// stays visible.
func (b *Browser) setSel(i int) {
	b.sel = browserClamp(i, b.listLen())
	b.top = browserClampTop(b.sel, b.top, brwRecentRows, b.listLen())
}

// move steps the selection by delta, clamping at both ends.
func (b *Browser) move(delta int) { b.setSel(b.sel + delta) }

// scrollBy scrolls the viewport by delta rows without moving the
// selection — the mouse wheel's behaviour.
func (b *Browser) scrollBy(delta int) {
	max := b.listLen() - brwRecentRows
	if max < 0 {
		max = 0
	}
	top := b.top + delta
	if top > max {
		top = max
	}
	if top < 0 {
		top = 0
	}
	b.top = top
}

// activate opens whatever is selected: a recent piece, or a checklist
// step's action on a first run. A recent that has gone missing says so
// instead.
func (b *Browser) activate() {
	if b.onboarding() {
		b.activateStep()
		return
	}
	if b.sel >= len(b.recents) {
		return
	}
	e := b.recents[b.sel]
	if e.missing {
		b.errMsg = "not found: " + e.path + "  (press Del to forget it)"
		return
	}
	b.openPath(e.path)
}

// openPath opens a piece. Any import failure is recorded for inline
// display and never propagated: a malformed file must not end the
// session.
func (b *Browser) openPath(path string) {
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
	b.setSel(0)
	// Anything a floating dialog says after this moment answers an
	// earlier question; see dialogGen.
	b.mu.Lock()
	b.dialogGen++
	b.mu.Unlock()
}

// forgetRecent (the Delete key) removes the selected recent. It is
// permanent when the preferences implementation supports removal, and
// otherwise lasts for the session.
func (b *Browser) forgetRecent() {
	if b.onboarding() || b.sel >= len(b.recents) {
		return
	}
	p := b.recents[b.sel].path
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
// piece is opened, and a dropped FOLDER opens the OS file dialog rooted
// there — the closest honest reading of "here is where my tabs live"
// now that the screen carries no directory listing of its own. Items the
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
		if browserSupported(p) && !browserIsDir(p) {
			// openPath resets the status line and fills it in on failure.
			b.openPath(p)
			b.noteSkipped(skipped)
			return
		}
	}
	for _, p := range paths {
		if browserIsDir(p) {
			b.errMsg = ""
			b.launchOpenDialog(p)
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
	ebiten.KeyEnter, ebiten.KeyNumpadEnter,
	ebiten.KeyO, ebiten.KeyDelete, ebiten.KeyS,
	ebiten.KeyF1, ebiten.KeySlash,
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
		b.move(-brwRecentRows)
	case ebiten.KeyPageDown:
		b.move(brwRecentRows)
	case ebiten.KeyHome:
		b.setSel(0)
	case ebiten.KeyEnd:
		b.setSel(b.listLen() - 1)
	case ebiten.KeyEnter, ebiten.KeyNumpadEnter:
		b.activate()
	case ebiten.KeyO:
		b.launchOpenDialog("")
	case ebiten.KeyDelete:
		b.forgetRecent()
	case ebiten.KeyS:
		if b.settings != nil {
			b.settings()
		}
	case ebiten.KeyF1, ebiten.KeySlash:
		b.helpOpen = true
	}
	return nil
}

// browserBindings resolves this screen's control table. It is the single
// source of both the footer hint and the ?/F1 overlay.
func (b *Browser) browserBindings() []helpBinding {
	leave := helpBinding{Group: "session", Keys: "esc", Hint: "esc quit", Desc: "Quit guitarTutor"}
	if b.sh != nil && b.sh.Depth() > 1 {
		leave = helpBinding{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Go back to where you came from"}
	}
	return []helpBinding{
		{Group: "opening", Keys: "O", Hint: "O open a file", Off: b.openDialog == nil,
			Desc: "Choose a piece with the system's own file dialog"},
		{Group: "opening", Keys: "enter", Hint: "enter open", Desc: "Open the selected recent piece"},
		{Group: "opening", Keys: "drag and drop", Desc: "Drop a piece on the window to open it; drop a folder to browse it in the file dialog"},
		{Group: "opening", Keys: "del", Hint: "del forget recent", Desc: "Forget the selected recent piece"},

		{Group: "choosing", Keys: "up / down", Hint: "up/dn select", Desc: "Move the selection"},
		{Group: "choosing", Keys: "page up / down", Desc: "Move the selection a screenful at a time"},
		{Group: "choosing", Keys: "click", Desc: "Select an entry; click it again to open it"},

		{Group: "session", Keys: "S", Hint: "s settings", Off: b.settings == nil,
			Desc: "Audio devices, latency calibration, SoundFont and count-in"},
		{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
		leave,
		{Group: "session", Keys: "Q", Desc: "Quit guitarTutor"},
	}
}

// hintLine is the one-line key summary in the footer, built from the
// same table as the overlay.
func (b *Browser) hintLine() string { return hintLineOf(b.browserBindings()) }

// openButtonRect is the open card's button, shared by layout and hit
// testing so they cannot drift.
func openButtonRect() rect {
	return rect{brwCardX + (brwCardW-brwOpenBtnW)/2, brwListTop + 64, brwOpenBtnW, brwOpenBtnH}
}

// hitTest maps a cursor position to a recents-list row index.
func (b *Browser) hitTest(x, y int) (int, bool) {
	if y < brwListTop || x < brwRecentX || x >= brwRecentX+brwRecentW {
		return 0, false
	}
	row := (y - brwListTop) / brwRecentRowH
	if row < 0 || row >= brwRecentRows {
		return 0, false
	}
	i := b.top + row
	if i >= b.listLen() {
		return 0, false
	}
	return i, true
}

// click applies a left click on a list row: the first click selects, a
// click on the row that is already selected opens it — which is also
// what the second click of a double click lands on.
func (b *Browser) click(i int) {
	if b.sel == i {
		b.activate()
		return
	}
	b.setSel(i)
}

// handleMouse reads the pointer once per frame: hover highlighting,
// wheel scrolling, list clicks, and the open button.
func (b *Browser) handleMouse(pt pointer) {
	i, ok := b.hitTest(int(pt.x), int(pt.y))
	b.hoverIdx = -1
	if ok {
		b.hoverIdx = i
	}

	if pt.wheel != 0 {
		b.wheelAcc += pt.wheel
		var steps int
		steps, b.wheelAcc = wheelSteps(b.wheelAcc)
		if steps != 0 {
			b.scrollBy(-steps * 3)
		}
	}
	if !pt.pressed {
		return
	}
	if pt.over(openButtonRect()) {
		b.launchOpenDialog("")
		return
	}
	if ok {
		b.click(i)
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
	b.ptr = readPointer()
	b.drainDialog()
	// The overlay is modal: while it is up, a key or click dismisses it
	// and reaches nothing underneath.
	if b.helpOpen {
		if helpDismissed(b.ptr) {
			b.helpOpen = false
		}
		return nil
	}
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
	if b.queuedEdits() != queued || b.helpOpen {
		return nil
	}
	b.handleMouse(b.ptr)
	return nil
}

// Draw paints the start screen. It reads only fields the methods above
// set, so nothing here decides anything.
func (b *Browser) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)
	drawHeader(screen, "guitarTutor", "a practice companion for guitarists", colDim)

	b.drawRecents(screen)
	b.drawOpenCard(screen)
	b.drawStatus(screen)
	drawFooter(screen, b.hintLine())

	if b.helpOpen {
		drawHelpOverlay(screen, "START SCREEN KEYS", b.browserBindings(), "")
	}
}

// drawPaneFrame paints a pane's background and title.
func drawPaneFrame(screen *ebiten.Image, x, w float64, rows int, title string) {
	h := float64(rows * brwRecentRowH)
	fillRounded(screen, rect{x - 8, brwListTop - 8, w + 16, h + 16}, colPanel)
	drawText(screen, title, x, 124, colDim)
	vector.StrokeLine(screen, float32(x), 146, float32(x+w), 146, 1, colPanelEdge, false)
}

// rowBG returns the background colour for a list row, or false when it
// needs none.
func (b *Browser) rowBG(i int) (color.RGBA, bool) {
	if b.sel == i && b.listLen() > 0 {
		return colFocus, true
	}
	if b.hoverIdx == i {
		return colHover, true
	}
	return color.RGBA{}, false
}

func (b *Browser) drawRecents(screen *ebiten.Image) {
	if b.onboarding() {
		drawPaneFrame(screen, brwRecentX, brwRecentW, len(b.stepList())+1, "GETTING STARTED")
		b.drawSteps(screen)
		return
	}
	drawPaneFrame(screen, brwRecentX, brwRecentW, brwRecentRows, "RECENT PIECES")
	for row := 0; row < brwRecentRows; row++ {
		i := b.top + row
		if i >= len(b.recents) {
			break
		}
		e := b.recents[i]
		y := float32(brwListTop + row*brwRecentRowH)
		if bg, ok := b.rowBG(i); ok {
			fillRounded(screen, rect{brwRecentX - 4, float64(y) - 2, brwRecentW + 8, brwRecentRowH - 2}, bg)
		}
		nameCol := colNote
		sub := e.parent
		if e.missing {
			nameCol = colMissing
			sub = "missing — press Del to forget"
		}
		drawTextScaled(screen, truncateWScaled(e.name, brwRecentW-16, brwNameScale), brwRecentX+4, float64(y), brwNameScale, nameCol)
		drawTextSmall(screen, ellipsizeWSmall(sub, brwRecentW-16), brwRecentX+4, float64(y)+22, colDim)
	}
	if len(b.recents) > brwRecentRows {
		drawTextSmall(screen, fmt.Sprintf("%d–%d of %d", b.top+1,
			min(b.top+brwRecentRows, len(b.recents)), len(b.recents)),
			brwRecentX+4, brwListTop+brwRecentRows*brwRecentRowH+12, colBarline)
	}
}

// drawOpenCard paints the right-hand card: the button that opens the
// system file dialog, and the drop hint.
func (b *Browser) drawOpenCard(screen *ebiten.Image) {
	drawPaneFrame(screen, brwCardX, brwCardW, brwRecentRows, "OPEN")

	btn := openButtonRect()
	label := "Open a piece…"
	fill, edge, tcol := colFocus, colInferred, colNote
	switch {
	case b.openDialog == nil:
		fill, edge, tcol = colBG, colBarline, colBarline
	case b.dialogBusy:
		label = "waiting for the file dialog…"
		fill, edge, tcol = colPanel, colPanelEdge, colDim
	case b.ptr.over(btn):
		edge = colNote
	}
	drawPanel(screen, btn, fill, edge)
	drawTextScaled(screen, label, centreXScaled(label, btn.x, btn.w, 1.15), btn.y+11, 1.15, tcol)
	hint := "browses with the system's file dialog  (O)"
	drawTextSmall(screen, hint, brwCardX+(brwCardW-textWSmall(hint))/2, btn.y+btn.h+14, colDim)

	midY := btn.y + btn.h + 64
	or := "— or —"
	drawTextSmall(screen, or, brwCardX+(brwCardW-textWSmall(or))/2, midY, colBarline)
	drop := "drop a file anywhere on this window"
	drawText(screen, drop, brwCardX+(brwCardW-textW(drop))/2, midY+26, colHUD)
	formats := "Guitar Pro (.gp) · MusicXML (.musicxml, .mxl) · MIDI (.mid) · text tab (.gtab)"
	drawTextSmall(screen, formats, brwCardX+(brwCardW-textWSmall(formats))/2, midY+52, colDim)
}

// drawStatus paints the last open's error and importer warnings under
// the panes.
func (b *Browser) drawStatus(screen *ebiten.Image) {
	const maxWarn = 4
	const width = screenW - 2*brwRecentX
	y := float64(brwStatusY)
	if b.errMsg != "" {
		drawText(screen, ellipsizeW(b.errMsg, width), brwRecentX, y, colMiss)
		y += 20
	}
	for i, w := range b.warns {
		if i == maxWarn {
			drawText(screen, fmt.Sprintf("… and %d more warnings", len(b.warns)-maxWarn), brwRecentX, y, colClose)
			break
		}
		drawText(screen, truncateW("warning: "+w, width), brwRecentX, y, colClose)
		y += 18
	}
}
