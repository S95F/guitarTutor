package ui

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

const (
	brwPaneGap   = 20.0
	brwPaneHeadH = 30.0
	brwRowH      = 42.0
	brwActionH   = 44.0
	brwNameScale = 1.2

	brwStatusLines = 5

	brwStatusWarnings = 2
)

type browserPane int

const (
	paneRecent browserPane = iota
	paneCreated
	paneLibrary
	paneCount
)

var paneTitles = [paneCount]string{"RECENT", "WRITTEN HERE", "YOUR LIBRARY"}

var paneEmpty = [paneCount]string{
	"pieces you open will be listed here — or drop a file anywhere on this window",
	"pieces you write here will be listed here",
	"nothing in the folder yet — write a piece and it lands here",
}

var colMissing = color.RGBA{150, 90, 90, 255}

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

func PieceExtensions() []string {
	out := make([]string, 0, len(browserPieceExts))
	for e := range browserPieceExts {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

func browserSupported(name string) bool {
	return browserPieceExts[strings.ToLower(filepath.Ext(name))]
}

type browserEntry struct {
	name string
	path string

	sub string

	missing bool
	problem bool
}

type browserRecentRemover interface {
	RemoveRecent(path string)
}

type dialogResult struct {
	path string
	err  string
	gen  int
}

type libraryResult struct {
	pieces []PieceInfo
	err    string
}

type Browser struct {
	sh *Shell

	panes [paneCount][]browserEntry
	focus browserPane
	sel   int
	top   int
	saved [paneCount]struct{ sel, top int }

	libErr      string
	libScanning bool

	forgotten map[string]bool

	recentStatus map[string]bool

	statFn func(string) (fs.FileInfo, error)

	errMsg string
	warns  []string

	warnsFrom   string
	warnsFailed bool

	hintOpen bool

	settings func()

	newPiece  func()
	editPiece func(path string)

	openDialog func(startDir string)

	dialogBusy bool

	helpOpen bool

	ptr       pointer
	anim      animator
	hoverIdx  int
	hoverPane browserPane
	wheelAcc  float64

	mu        sync.Mutex
	pending   *dialogResult
	libMail   *libraryResult
	dialogGen int

	launchGen int
}

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

func NewBrowserShell(svc Services) (*Shell, *Browser) {
	sh := NewShell(svc, browserPlaceholder{})
	b := NewBrowser(sh)
	sh.Replace(b)
	return sh, b
}

type browserPlaceholder struct{}

func (browserPlaceholder) Update() error          { return nil }
func (browserPlaceholder) Draw(dst *ebiten.Image) { dst.Fill(colBG) }

func (b *Browser) SetSettingsOpener(fn func()) { b.settings = fn }

func (b *Browser) SetNewPiece(fn func()) { b.newPiece = fn }

func (b *Browser) SetEditPiece(fn func(path string)) { b.editPiece = fn }

func (b *Browser) SetOpenDialog(fn func(startDir string)) { b.openDialog = fn }

func (b *Browser) OfferDialogResult(path, errMsg string) {
	b.mu.Lock()
	b.pending = &dialogResult{path: path, err: errMsg, gen: b.launchGen}
	b.mu.Unlock()
}

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

func (b *Browser) prefs() Prefs {
	if b.sh == nil {
		return nil
	}
	return b.sh.Services().Prefs
}

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

func (b *Browser) statRecent(path string) (fs.FileInfo, error) {
	if b.statFn != nil {
		return b.statFn(path)
	}
	return os.Stat(path)
}

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

func (b *Browser) entries() []browserEntry { return b.panes[b.focus] }

func (b *Browser) hasLibrary() bool {
	return b.sh != nil && b.sh.Services().Library != nil
}

func (b *Browser) paneOrder() []browserPane {
	if b.hasLibrary() {
		return []browserPane{paneRecent, paneCreated, paneLibrary}
	}
	return []browserPane{paneRecent, paneCreated}
}

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

func (b *Browser) listLen() int { return len(b.entries()) }

func browserClamp(i, n int) int {
	if n <= 0 || i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

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

func (b *Browser) setSel(i int) {
	b.sel = browserClamp(i, b.listLen())
	b.top = browserClampTop(b.sel, b.top, b.rowsPerPane(), b.listLen())
}

func (b *Browser) move(delta int) { b.setSel(b.sel + delta) }

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

func (b *Browser) selected() (browserEntry, bool) {
	es := b.entries()
	if b.sel < 0 || b.sel >= len(es) {
		return browserEntry{}, false
	}
	return es[b.sel], true
}

func (b *Browser) activate() {
	e, ok := b.selected()
	if !ok {
		return
	}
	switch {
	case e.missing:
		b.errMsg = "not found: " + e.path + "  (press Del to forget it)"
	case e.problem:

		if path, ok := b.editableSelection(); ok {
			b.ShowError(filepath.Base(e.path) + ": " + e.sub)
			b.editPiece(path)
			return
		}

		b.errMsg = filepath.Base(e.path) + ": " + e.sub + "  (editing is not available in this build)"
	default:
		b.openPath(e.path)
	}
}

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

	delete(b.forgotten, path)
	delete(b.recentStatus, path)
	b.reloadRecents()
	b.focusPane(paneRecent)
	b.setSel(0)

	b.mu.Lock()
	b.dialogGen++
	b.mu.Unlock()
}

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

			_ = pr.Save()
		}
	}
	b.reloadRecents()
}

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

func browserSkipNote(n int) string {
	if n == 1 {
		return "1 dropped item could not be read and was skipped"
	}
	return fmt.Sprintf("%d dropped items could not be read and were skipped", n)
}

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

func browserIgnoredNote(n int) string {
	if n == 1 {
		return "1 more dropped piece was ignored — drop pieces one at a time to open each"
	}
	return fmt.Sprintf("%d more dropped pieces were ignored — drop pieces one at a time to open each", n)
}

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
	b.errMsg = "dropped file is not a piece musicTutor can open: " + filepath.Base(paths[0])
	b.noteSkipped(skipped)
}

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

var browserRepeatKeys = map[ebiten.Key]bool{
	ebiten.KeyUp: true, ebiten.KeyDown: true,
	ebiten.KeyPageUp: true, ebiten.KeyPageDown: true,
}

func browserShiftHeld() bool {
	return ebiten.IsKeyPressed(ebiten.KeyShiftLeft) || ebiten.IsKeyPressed(ebiten.KeyShiftRight)
}

func browserKeyFires(repeat bool, d int) bool {
	if d == 1 {
		return true
	}
	if !repeat {
		return false
	}
	return d > 30 && (d-30)%4 == 0
}

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

func (b *Browser) browserBindings() []helpBinding {
	leave := helpBinding{Group: "session", Keys: "esc", Hint: "esc quit", Desc: "Quit musicTutor"}
	if b.sh != nil && b.sh.Depth() > 1 {
		leave = helpBinding{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Go back to where you came from"}
	}

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
		{Group: "session", Keys: "Q", Desc: "Quit musicTutor"},
	}
}

func browserCanEdit(b *Browser) bool {
	_, ok := b.editableSelection()
	return ok
}

func (b *Browser) hintLine() string { return hintLineOf(b.browserBindings()) }

func (b *Browser) ShowError(msg string) {
	b.errMsg, b.warns, b.warnsFrom, b.warnsFailed = msg, nil, "", false
}

func (b *Browser) clearStatus() { b.ShowError("") }

func (b *Browser) startNewPiece() {
	if b.newPiece == nil {
		return
	}
	b.clearStatus()
	b.newPiece()
}

func (b *Browser) editableSelection() (string, bool) {
	if b.editPiece == nil {
		return "", false
	}
	e, ok := b.selected()
	if !ok || e.missing {
		return "", false
	}

	return e.path, true
}

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

func browserKeyFiresNow(k ebiten.Key) bool {
	return browserKeyFires(browserRepeatKeys[k], inpututil.KeyPressDuration(k))
}

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

func (b *Browser) Update() error {
	b.ptr = readPointer()

	queued := b.queuedEdits()
	b.drainDialog()

	b.drainLibrary()
	if b.queuedEdits() != queued {
		return nil
	}

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

func (b *Browser) RefreshLibrary() { b.rescanLibrary() }
