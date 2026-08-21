package ui

// The start screen: the first thing a user sees when the binary is
// launched with no arguments.
//
// It lands on the pieces, in three panes, because there are three honest
// answers to "what do I want to play":
//
//   - RECENT is what was opened lately, from wherever it lives. A path,
//     and nothing more is known about it until it is opened.
//   - WRITTEN is what was made here lately — the same kind of shortcut,
//     for the other kind of piece.
//   - LIBRARY is the application's own folder, DESCRIBED. Every piece in
//     it has been read, so it is listed by what the music is (its title,
//     its meter, its tempo, how many bars) rather than by file name. It
//     is the one pane that is a place rather than a history: nothing
//     falls off the end of it, and a piece written six months ago is
//     still there.
//
// What used to stand in this space on a first run — a getting-started
// checklist occupying the whole left pane until a piece had been opened —
// is now a strip across the top that can be put away for good (see
// hint.go). Every step it names is also a row in settings, so a screen
// that keeps insisting on them is a screen that never stops being a
// tutorial.
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
// The selection is per pane. sel and top are the FOCUSED pane's, and the
// others' are parked in saved — rather than an array indexed by pane
// everywhere — so that every method about moving a selection stays a
// method about one list, and moving between panes is the only place that
// has to know there is more than one.
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
)

// Layout, in logical pixels (screenW x screenH). The panes divide the
// page width evenly; everything vertical is computed in layout(), because
// the hint strip changes height when it is put away and the panes take
// the room it gives back.
const (
	brwPaneGap   = 20.0
	brwPaneHeadH = 30.0 // a pane's title band, above its rows
	brwRowH      = 42.0
	brwActionH   = 44.0
	brwNameScale = 1.2
	// brwStatusLines is the band under the panes that the status stack
	// gets, and that the panes therefore stop above whether or not there
	// is anything in it. Reserving room for the worst case — an error plus
	// every warning an import can produce — would hold a third of the
	// screen empty for something that appears for a few seconds after an
	// import; reserving a handful of lines and letting a long stack say
	// how many more there are keeps both the panes and the messages
	// readable.
	brwStatusLines = 5
	// brwStatusWarnings is how many warnings are listed before the rest
	// are counted. The band has to hold the error line, the heading that
	// names the piece the warnings came from, these, and the "and N more"
	// line under them — five lines for two warnings, which is what a
	// browser test checks rather than trusts.
	brwStatusWarnings = 2
)

// A browserPane is one of the three lists of pieces.
type browserPane int

const (
	paneRecent browserPane = iota
	paneCreated
	paneLibrary
	paneCount
)

// paneTitles are the headings, and paneEmpty what a pane says instead of
// rows when it has none. An empty pane that says nothing is a pane the
// user assumes is broken.
var paneTitles = [paneCount]string{"RECENT", "WRITTEN HERE", "YOUR LIBRARY"}

var paneEmpty = [paneCount]string{
	"pieces you open will be listed here — or drop a file anywhere on this window",
	"pieces you write here will be listed here",
	"nothing in the folder yet — write a piece and it lands here",
}

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

// A browserEntry is one selectable row of a pane.
type browserEntry struct {
	name string // shown prominently: a file name, or a piece's own title
	path string // full path, what gets opened
	// sub is the quiet second line: the containing directory for a path,
	// or the description of the music for a library piece.
	sub string
	// missing marks a listed path whose file is no longer on disk, and
	// problem a library piece the application could not read.
	missing bool
	problem bool
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

// A libraryResult is what a background scan posts back.
type libraryResult struct {
	pieces []PieceInfo
	err    string
}

// A Browser is the start screen. It lists recently opened pieces (or the
// first-run checklist), launches the OS file dialog to open new ones,
// opens the selection through the Shell, and reports import errors and
// warnings inline rather than propagating them — a malformed file must
// never end the session.
type Browser struct {
	sh *Shell

	// panes holds each list's rows; the focused one's cursor is sel/top
	// and the others' are parked in saved (see focusPane).
	panes [paneCount][]browserEntry
	focus browserPane
	sel   int
	top   int
	saved [paneCount]struct{ sel, top int }

	// libErr is why the last library scan failed, and libScanning is true
	// while one is in flight. A scan reads and parses every piece in the
	// folder, so it runs off the game loop and posts to libMail.
	libErr      string
	libScanning bool
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
	// warnsFrom names the piece the warnings came from. The status band
	// can be read minutes after the open — the user practises, comes
	// back — and a bare "warning:" with no piece over it is orphaned
	// text about nothing in particular. warnsFailed records whether that
	// open failed, because the heading must not say a piece "opened" that
	// did not — and it must be recorded, not inferred from errMsg, which
	// other actions (the O key, a drop) clear while the warnings stay.
	// Both live and die with warns.
	warnsFrom   string
	warnsFailed bool

	// hintOpen mirrors the stored "has the getting-started strip been put
	// away" flag, so the screen can be driven without preferences.
	hintOpen bool

	settings func()
	// newPiece opens the editor on a blank piece, and editPiece opens it
	// on an existing one. The UI package cannot build an editor's file
	// dialogs, so the application wires both; until it does, the two
	// buttons are drawn disabled rather than silently doing nothing.
	newPiece  func()
	editPiece func(path string)
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
	ptr       pointer
	anim      animator
	hoverIdx  int         // row under the cursor, -1 for none
	hoverPane browserPane // the pane it is in; meaningless when hoverIdx is -1
	wheelAcc  float64

	// mu guards the dialog mailbox, written by the dialog goroutine and
	// drained by Update on the game loop. dialogGen (also under mu) is
	// bumped every time a piece opens: a dialog left floating while the
	// user opened something else by other means must not auto-open its
	// half-hour-old choice the moment the start screen is next shown
	// (verification follow-up).
	mu        sync.Mutex
	pending   *dialogResult
	libMail   *libraryResult
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
	b.hintOpen = true
	if pr := b.prefs(); pr != nil {
		b.hintOpen = !pr.HintHidden()
	}
	b.reloadRecents()
	b.rescanLibrary()
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

// SetNewPiece installs the action behind the "Write a new piece" button
// and the N key: opening the tablature editor on a blank piece.
func (b *Browser) SetNewPiece(fn func()) { b.newPiece = fn }

// SetEditPiece installs the action behind the "Edit selected" button and
// the E key: opening the editor on a piece already on the recents list.
func (b *Browser) SetEditPiece(fn func(path string)) { b.editPiece = fn }

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
	for _, p := range b.paneOrder() {
		for _, e := range b.panes[p] {
			if e.missing {
				continue
			}
			return filepath.Dir(e.path)
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
	known := b.recentStatus
	status := make(map[string]bool, len(known))
	build := func(paths []string) []browserEntry {
		var out []browserEntry
		for _, p := range paths {
			if b.forgotten[p] {
				continue
			}
			missing, seen := status[p]
			if !seen {
				if missing, seen = known[p]; !seen {
					fi, err := b.statRecent(p)
					missing = err != nil || fi == nil || fi.IsDir()
				}
			}
			status[p] = missing
			out = append(out, browserEntry{
				name:    filepath.Base(p),
				path:    p,
				sub:     filepath.Dir(p),
				missing: missing,
			})
		}
		return out
	}
	var recents, created []string
	if pr := b.prefs(); pr != nil {
		recents, created = pr.Recents(), pr.Created()
	}
	b.panes[paneRecent] = build(recents)
	b.panes[paneCreated] = build(created)
	b.recentStatus = status
	b.setSel(b.sel)
}

// --- panes -------------------------------------------------------------

// entries are the focused pane's rows.
func (b *Browser) entries() []browserEntry { return b.panes[b.focus] }

// hasLibrary reports whether this build manages a library at all, and
// therefore whether the third pane exists.
func (b *Browser) hasLibrary() bool {
	return b.sh != nil && b.sh.Services().Library != nil
}

// paneOrder is the panes actually on screen, left to right.
func (b *Browser) paneOrder() []browserPane {
	if b.hasLibrary() {
		return []browserPane{paneRecent, paneCreated, paneLibrary}
	}
	return []browserPane{paneRecent, paneCreated}
}

// focusPane moves the keyboard to another pane, parking the cursor of the
// one being left and restoring the one being entered — so coming back to
// a list finds it where it was rather than at the top.
func (b *Browser) focusPane(p browserPane) {
	if p < 0 || p >= paneCount || p == b.focus {
		return
	}
	if !b.hasLibrary() && p == paneLibrary {
		return
	}
	b.saved[b.focus].sel, b.saved[b.focus].top = b.sel, b.top
	b.focus = p
	b.sel, b.top = b.saved[p].sel, b.saved[p].top
	b.setSel(b.sel)
}

// stepPane moves the focus one pane along, stopping at the ends rather
// than wrapping: a list that jumps from the right-hand pane back to the
// left is a list nobody can walk off the end of on purpose.
func (b *Browser) stepPane(delta int) {
	order := b.paneOrder()
	at := 0
	for i, p := range order {
		if p == b.focus {
			at = i
		}
	}
	at += delta
	if at < 0 || at >= len(order) {
		return
	}
	b.focusPane(order[at])
}

// --- the library -------------------------------------------------------

// rescanLibrary reads the managed folder on its own goroutine and posts
// what it finds. Scanning parses every piece in the folder, which is
// cheap per file and unbounded in the number of them, so it never runs on
// the game loop.
func (b *Browser) rescanLibrary() {
	if !b.hasLibrary() || b.libScanning {
		return
	}
	lib := b.sh.Services().Library
	b.libScanning = true
	go func() {
		pieces, err := lib.Scan()
		res := &libraryResult{pieces: pieces}
		if err != nil {
			res.err = err.Error()
		}
		b.mu.Lock()
		b.libMail = res
		b.mu.Unlock()
	}()
}

// drainLibrary applies a finished scan on the game loop.
func (b *Browser) drainLibrary() {
	b.mu.Lock()
	res := b.libMail
	b.libMail = nil
	b.mu.Unlock()
	if res == nil {
		return
	}
	b.libScanning = false
	b.libErr = res.err
	b.panes[paneLibrary] = libraryEntries(res.pieces)
	if b.focus == paneLibrary {
		b.setSel(b.sel)
	} else {
		b.saved[paneLibrary].sel, b.saved[paneLibrary].top =
			browserClamp(b.saved[paneLibrary].sel, len(b.panes[paneLibrary])), 0
	}
}

// libraryEntries turns described pieces into rows. The name shown is the
// piece's own title where it has one — the library is the pane that knows
// what the music IS, and falling back to the file name there would throw
// that away.
func libraryEntries(pieces []PieceInfo) []browserEntry {
	out := make([]browserEntry, 0, len(pieces))
	for _, p := range pieces {
		name := strings.TrimSpace(p.Title)
		if name == "" {
			name = p.Name
		}
		sub, problem := p.Summary, false
		if p.Problem != "" {
			sub, problem = p.Problem, true
			if name == "" {
				name = p.Name
			}
		}
		out = append(out, browserEntry{name: name, path: p.Path, sub: sub, problem: problem})
	}
	return out
}

// libraryNote is the line under the library pane: where the folder is, or
// what went wrong reading it.
func (b *Browser) libraryNote() (string, bool) {
	switch {
	case !b.hasLibrary():
		return "", false
	case b.libErr != "":
		return b.libErr, true
	case b.libScanning:
		return "reading the folder…", false
	}
	return b.sh.Services().Library.Dir(), false
}

// listLen is how many rows the focused pane holds.
func (b *Browser) listLen() int { return len(b.entries()) }

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
	b.top = browserClampTop(b.sel, b.top, b.rowsPerPane(), b.listLen())
}

// move steps the selection by delta, clamping at both ends.
func (b *Browser) move(delta int) { b.setSel(b.sel + delta) }

// scrollBy scrolls the viewport by delta rows without moving the
// selection — the mouse wheel's behaviour.
func (b *Browser) scrollBy(delta int) {
	max := b.listLen() - b.rowsPerPane()
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

// selected is the entry under the cursor, and whether there is one.
func (b *Browser) selected() (browserEntry, bool) {
	es := b.entries()
	if b.sel < 0 || b.sel >= len(es) {
		return browserEntry{}, false
	}
	return es[b.sel], true
}

// activate opens whatever is selected. An entry that has gone missing,
// or one the application could not read, says so instead of failing
// silently.
func (b *Browser) activate() {
	e, ok := b.selected()
	if !ok {
		return
	}
	switch {
	case e.missing:
		b.errMsg = "not found: " + e.path + "  (press Del to forget it)"
	case e.problem:
		// A piece that will not parse is listed ON PURPOSE, and the editor
		// is the one place it can be repaired — so Enter goes where the E
		// key goes instead of dead-ending in a message about the E key.
		// The status keeps the parse error, without the dead-end phrasing,
		// so the reason for landing in the editor is still on screen when
		// the user comes back from it.
		if path, ok := b.editableSelection(); ok {
			b.ShowError(filepath.Base(e.path) + ": " + e.sub)
			b.editPiece(path)
			return
		}
		// Reached only when editPiece is unwired: with it wired,
		// editableSelection sent the entry to the editor above. So the
		// line must not promise the E key — E is exactly as dead as
		// Enter here.
		b.errMsg = filepath.Base(e.path) + ": " + e.sub + "  (editing is not available in this build)"
	default:
		b.openPath(e.path)
	}
}

// openPath opens a piece. Any import failure is recorded for inline
// display and never propagated: a malformed file must not end the
// session.
func (b *Browser) openPath(path string) {
	b.clearStatus()
	if b.sh == nil || b.sh.Services().Opener == nil {
		b.errMsg = "no importer is available in this build"
		return
	}
	warns, err := b.sh.OpenPiece(path)
	b.warns = warns
	if len(warns) > 0 {
		b.warnsFrom, b.warnsFailed = filepath.Base(path), err != nil
	}
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
	b.focusPane(paneRecent)
	b.setSel(0)
	// Anything a floating dialog says after this moment answers an
	// earlier question; see dialogGen.
	b.mu.Lock()
	b.dialogGen++
	b.mu.Unlock()
}

// forgetRecent (the Delete key) drops the selected shortcut. It is
// permanent when the preferences implementation supports removal, and
// otherwise lasts for the session.
//
// It applies to the two path lists only. The library is a FOLDER, and
// forgetting one of its entries would either do nothing visible or delete
// somebody's work — neither of which is what a Delete key on a list
// should be allowed to mean.
func (b *Browser) forgetRecent() {
	if b.focus == paneLibrary {
		b.errMsg = "the library lists what is in the folder; delete the file to remove it"
		return
	}
	e, ok := b.selected()
	if !ok {
		return
	}
	if b.forgotten == nil {
		b.forgotten = map[string]bool{}
	}
	b.forgotten[e.path] = true
	if pr := b.prefs(); pr != nil {
		if rm, ok := pr.(browserRecentRemover); ok {
			rm.RemoveRecent(e.path)
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

// browserIgnoredNote describes the readable pieces of a drop beyond the
// one that opened, in the singular or the plural.
func browserIgnoredNote(n int) string {
	if n == 1 {
		return "1 more dropped piece was ignored — drop pieces one at a time to open each"
	}
	return fmt.Sprintf("%d more dropped pieces were ignored — drop pieces one at a time to open each", n)
}

// noteIgnored reports what happened to the rest of a multi-piece drop.
// Only one piece can be practised at a time, so only the first opens —
// but discarding the others in silence would leave the user counting
// windows that never appeared. Naming the piece that DID open matters
// too: without it, the note reads as if the whole drop was refused.
func (b *Browser) noteIgnored(opened string, n int) {
	if n <= 0 {
		return
	}
	if b.errMsg == "" {
		b.errMsg = "opened " + opened + "; " + browserIgnoredNote(n)
		return
	}
	b.errMsg += "; " + browserIgnoredNote(n)
}

// handleDrop acts on files dropped onto the window: the first supported
// piece is opened, and a dropped FOLDER opens the OS file dialog rooted
// there — the closest honest reading of "here is where my tabs live"
// now that the screen carries no directory listing of its own. Items the
// platform could not hand over are skipped, and the outcome is always
// reported — including the readable pieces beyond the first, which are
// not opened, and a drop of nothing usable, which must say so rather
// than fail silently.
func (b *Browser) handleDrop(fsys fs.FS) {
	paths, skipped := browserDroppedPaths(fsys)
	if len(paths) == 0 {
		b.errMsg = "nothing dropped on the window could be opened"
		b.noteSkipped(skipped)
		return
	}
	for i, p := range paths {
		if browserSupported(p) && !browserIsDir(p) {
			rest := 0
			for _, q := range paths[i+1:] {
				if browserSupported(q) && !browserIsDir(q) {
					rest++
				}
			}
			// openPath resets the status line and fills it in on failure.
			b.openPath(p)
			b.noteIgnored(filepath.Base(p), rest)
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
	ebiten.KeyLeft, ebiten.KeyRight, ebiten.KeyTab,
	ebiten.KeyO, ebiten.KeyDelete, ebiten.KeyS, ebiten.KeyN, ebiten.KeyE,
	ebiten.KeyH, ebiten.KeyF5,
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

// browserShiftHeld reports whether a shift key is down, which is the one
// piece of live keyboard state handleKey needs and cannot be given as a
// key of its own (Tab and shift+Tab are one binding read two ways).
func browserShiftHeld() bool {
	return ebiten.IsKeyPressed(ebiten.KeyShiftLeft) || ebiten.IsKeyPressed(ebiten.KeyShiftRight)
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
		b.move(-b.rowsPerPane())
	case ebiten.KeyPageDown:
		b.move(b.rowsPerPane())
	case ebiten.KeyLeft:
		b.stepPane(-1)
	case ebiten.KeyRight:
		b.stepPane(1)
	case ebiten.KeyTab:
		if browserShiftHeld() {
			b.stepPane(-1)
		} else {
			b.stepPane(1)
		}
	case ebiten.KeyH:
		b.toggleHint()
	case ebiten.KeyF5:
		b.reloadRecents()
		b.rescanLibrary()
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
	case ebiten.KeyN:
		b.startNewPiece()
	case ebiten.KeyE:
		b.editSelected()
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
	// Delete means something different in the library, which is a folder
	// rather than a history: there is no shortcut there to forget. A
	// footer that advertises it anyway would be teaching a key that
	// cannot work on the pane the user is looking at.
	return []helpBinding{
		{Group: "opening", Keys: "O", Hint: "O open a file", Off: b.openDialog == nil,
			Desc: "Choose a piece with the system's own file dialog"},
		{Group: "opening", Keys: "enter", Hint: "enter open", Desc: "Open the selected piece"},
		{Group: "opening", Keys: "drag and drop", Desc: "Drop a piece on the window to open it; drop a folder to browse it in the file dialog"},
		{Group: "opening", Keys: "del", Hint: "del forget", Off: b.focus == paneLibrary,
			Desc: "Forget the selected shortcut (the file is left alone)"},

		{Group: "choosing", Keys: "tab / left / right", Hint: "tab list", Desc: "Move between recent, written here, and your library"},
		{Group: "choosing", Keys: "F5", Desc: "Re-read the library folder"},

		{Group: "writing", Keys: "N", Hint: "N new piece", Off: b.newPiece == nil,
			Desc: "Write a piece by hand in the editor"},
		{Group: "writing", Keys: "E", Hint: "E edit", Off: !browserCanEdit(b),
			Desc: "Edit the selected piece; an imported one has to be saved as .gtab"},

		{Group: "choosing", Keys: "up / down", Hint: "up/dn select", Desc: "Move the selection"},
		{Group: "choosing", Keys: "page up / down", Desc: "Move the selection a screenful at a time"},
		{Group: "choosing", Keys: "home / end", Desc: "Jump to the first or last entry"},
		{Group: "choosing", Keys: "click", Desc: "Select an entry; click it again to open it"},

		{Group: "session", Keys: "S", Hint: "S settings", Off: b.settings == nil,
			Desc: "Audio devices, latency calibration, SoundFont and count-in"},
		{Group: "session", Keys: "H", Desc: "Show or hide the getting-started strip"},
		{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
		leave,
		{Group: "session", Keys: "Q", Desc: "Quit guitarTutor"},
	}
}

// browserCanEdit reports whether the E binding does anything right now.
func browserCanEdit(b *Browser) bool {
	_, ok := b.editableSelection()
	return ok
}

// hintLine is the one-line key summary in the footer, built from the
// same table as the overlay.
func (b *Browser) hintLine() string { return hintLineOf(b.browserBindings()) }

// ShowError posts a message to the start screen's status area. It exists
// for the actions the integrator performs on the screen's behalf — opening
// a piece for editing, say — which can fail with something the user needs
// to read. It deliberately does NOT go through the dialog mailbox: that
// path also clears the "a file dialog is open" guard, and borrowing it for
// an unrelated failure would re-arm a dialog that is still on screen.
// ShowError replaces the whole status band with one error line. It is
// the ONE place the band's fields are written together, so a new field
// cannot be forgotten at some of the sites that reset it.
func (b *Browser) ShowError(msg string) {
	b.errMsg, b.warns, b.warnsFrom, b.warnsFailed = msg, nil, "", false
}

// clearStatus empties the status band.
func (b *Browser) clearStatus() { b.ShowError("") }

// startNewPiece opens the editor on a blank piece.
func (b *Browser) startNewPiece() {
	if b.newPiece == nil {
		return
	}
	b.clearStatus()
	b.newPiece()
}

// editableSelection is the recent the E key and the Edit button would
// open, and whether there is one. The onboarding checklist has no pieces
// on it, and a recent whose file has gone is not editable either.
func (b *Browser) editableSelection() (string, bool) {
	if b.editPiece == nil {
		return "", false
	}
	e, ok := b.selected()
	if !ok || e.missing {
		return "", false
	}
	// A library piece that would not parse is offered ON PURPOSE: the
	// editor's text view is where it can be looked at and repaired, and
	// refusing to open the one file that needs attention would be an odd
	// place to draw the line.
	return e.path, true
}

// editSelected opens the editor on the selected recent.
func (b *Browser) editSelected() {
	path, ok := b.editableSelection()
	if !ok {
		return
	}
	b.clearStatus()
	b.editPiece(path)
}

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
	// The one-action-per-frame baseline is taken BEFORE the dialog
	// mailbox is drained, because draining it can open a piece — and a
	// piece opened that way has to stop the rest of the frame exactly
	// like one opened by Enter. Sampling afterwards folded the dialog's
	// own push into the baseline, so a key edge in the same frame could
	// stack a second stack edit, and an Escape there had the Shell build
	// a practice screen it discarded without releasing its audio.
	queued := b.queuedEdits()
	b.drainDialog()
	// A finished library scan is applied here too. It cannot open
	// anything, so it needs none of the one-action-per-frame care the
	// dialog above does — it only ever replaces the contents of a list.
	b.drainLibrary()
	if b.queuedEdits() != queued {
		return nil
	}
	// The overlay is modal: while it is up, a key or click dismisses it
	// and reaches nothing underneath.
	if b.helpOpen {
		if helpDismissed(b.ptr) {
			b.helpOpen = false
		}
		return nil
	}
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

// RefreshLibrary re-reads the managed pieces folder. The application
// calls it after the editor has saved, so a piece written this session
// appears in the library the moment the user comes back to the start
// screen rather than at the next launch.
func (b *Browser) RefreshLibrary() { b.rescanLibrary() }
