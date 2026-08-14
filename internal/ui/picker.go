package ui

// A modal file picker: one directory listing, filtered to the extensions
// the caller asked for, that hands back a chosen path and finishes.
//
// It exists because the settings screen's SoundFont row had nowhere to
// go. Settings could clear a SoundFont back to the built-in pluck, but
// choosing one meant quitting the application and passing -sf2 on the
// command line — an in-app setting that could only be turned off is worse
// than no setting at all.
//
// The listing, sorting, scrolling and error handling are the start
// screen's, shared rather than reimplemented (browserReadListing,
// browserClampTop and friends). What is different is the shape of the
// interaction: one pane, a filter chosen by the caller, and a result that
// goes to a callback instead of opening a piece.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Layout of the single pane.
const (
	pkListTop = 150.0
	pkRowH    = 24.0
	pkRows    = 18
)

// A FilePicker is the modal chooser. It finishes — returning errQuit, so
// the Shell pops it — as soon as a file is chosen or the user backs out,
// and calls chosen only in the first case.
type FilePicker struct {
	sh    *Shell
	title string
	// accept filters the listing; exts is kept for the footer, which says
	// what the picker is looking for rather than leaving the empty
	// folders unexplained.
	accept func(string) bool
	exts   []string
	chosen func(string)

	dir     string
	listing []browserEntry
	sel     int
	top     int
	dirErr  string

	ptr      pointer
	wheelAcc float64
}

var _ Screen = (*FilePicker)(nil)

// NewFilePicker builds a chooser over exts (".sf2" and the like), opening
// on the user's home directory. chosen runs on the game loop with the
// selected path, and not at all if the user backs out.
func NewFilePicker(sh *Shell, title string, exts []string, chosen func(string)) *FilePicker {
	p := &FilePicker{
		sh:     sh,
		title:  title,
		accept: extMatcher(exts),
		exts:   exts,
		chosen: chosen,
	}
	p.setDir(pickerStartDir())
	return p
}

// pickerStartDir is where the chooser opens: the user's home, then the
// working directory.
func pickerStartDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// setDir points the pane at dir. As on the start screen, a directory that
// cannot be read still becomes current — with the failure recorded — so
// Backspace can always get back out of it.
func (p *FilePicker) setDir(dir string) {
	dir = filepath.Clean(dir)
	p.dir, p.dirErr = dir, ""
	kids, err := browserReadListing(dir, p.accept)
	if err != nil {
		p.dirErr = browserErrText(err)
		kids = nil
	}
	p.listing = nil
	if parent, ok := browserParentOf(dir); ok {
		p.listing = append(p.listing, browserEntry{name: "..", path: parent, isDir: true})
	}
	p.listing = append(p.listing, kids...)
	p.sel, p.top = 0, 0
}

// move steps the selection, scrolling the viewport to follow.
func (p *FilePicker) move(d int) { p.setSel(p.sel + d) }

func (p *FilePicker) setSel(i int) {
	p.sel = browserClamp(i, len(p.listing))
	p.top = browserClampTop(p.sel, p.top, pkRows, len(p.listing))
}

// scrollBy moves the viewport without moving the selection.
func (p *FilePicker) scrollBy(d int) {
	max := len(p.listing) - pkRows
	if max < 0 {
		max = 0
	}
	p.top = browserClamp(p.top+d, max+1)
}

// goParent moves up one directory, keeping the folder just left selected.
func (p *FilePicker) goParent() {
	parent, ok := browserParentOf(p.dir)
	if !ok {
		return
	}
	leaving := filepath.Base(p.dir)
	p.setDir(parent)
	for i, e := range p.listing {
		if e.name == leaving {
			p.setSel(i)
			return
		}
	}
}

// activate descends into the selected directory, or chooses the selected
// file and finishes. It reports whether the picker is done.
func (p *FilePicker) activate() (done bool) {
	if p.sel < 0 || p.sel >= len(p.listing) {
		return false
	}
	e := p.listing[p.sel]
	if e.isDir {
		if e.name == ".." {
			p.goParent()
		} else {
			p.setDir(e.path)
		}
		return false
	}
	if p.chosen != nil {
		p.chosen(e.path)
	}
	return true
}

// hitTest maps a cursor position to a row index. While a directory error
// is showing the rows are not drawn, so nothing under the cursor may hit:
// an invisible row that still answered clicks would navigate blind.
func (p *FilePicker) hitTest(x, y float64) (int, bool) {
	if p.dirErr != "" {
		return 0, false
	}
	if x < uiPadX || x > screenW-uiPadX || y < pkListTop {
		return 0, false
	}
	row := int((y - pkListTop) / pkRowH)
	if row < 0 || row >= pkRows {
		return 0, false
	}
	i := p.top + row
	if i >= len(p.listing) {
		return 0, false
	}
	return i, true
}

// handleMouse applies one frame of pointer input, reporting whether the
// picker is finished. A click selects; a click on the already-selected
// row activates it, which is also where a double click's second press
// lands — the same rule the start screen uses.
func (p *FilePicker) handleMouse(pt pointer) (done bool) {
	if pt.wheel != 0 {
		var steps int
		p.wheelAcc += pt.wheel
		steps, p.wheelAcc = wheelSteps(p.wheelAcc)
		p.scrollBy(-steps * 3)
	}
	if !pt.pressed {
		return false
	}
	i, ok := p.hitTest(pt.x, pt.y)
	if !ok {
		return false
	}
	if p.sel == i {
		return p.activate()
	}
	p.setSel(i)
	return false
}

// pickerKeys are every key the chooser reacts to.
var pickerKeys = []ebiten.Key{
	ebiten.KeyUp, ebiten.KeyDown, ebiten.KeyPageUp, ebiten.KeyPageDown,
	ebiten.KeyHome, ebiten.KeyEnd, ebiten.KeyEnter, ebiten.KeyNumpadEnter,
	ebiten.KeyBackspace, ebiten.KeyEscape,
}

// handleKey applies one key press, reporting whether the picker is done.
func (p *FilePicker) handleKey(k ebiten.Key) (done bool) {
	switch k {
	case ebiten.KeyEscape:
		return true
	case ebiten.KeyUp:
		p.move(-1)
	case ebiten.KeyDown:
		p.move(1)
	case ebiten.KeyPageUp:
		p.move(-pkRows)
	case ebiten.KeyPageDown:
		p.move(pkRows)
	case ebiten.KeyHome:
		p.setSel(0)
	case ebiten.KeyEnd:
		p.setSel(len(p.listing) - 1)
	case ebiten.KeyEnter, ebiten.KeyNumpadEnter:
		return p.activate()
	case ebiten.KeyBackspace:
		p.goParent()
	}
	return false
}

// Update translates this frame's input. Returning errQuit finishes the
// screen, whether the user chose something or backed out.
func (p *FilePicker) Update() error {
	p.ptr = readPointer()
	for _, k := range pickerKeys {
		if !browserKeyFires(browserRepeatKeys[k], inpututil.KeyPressDuration(k)) {
			continue
		}
		if p.handleKey(k) {
			return errQuit
		}
	}
	if p.handleMouse(p.ptr) {
		return errQuit
	}
	return nil
}

// Draw paints the chooser.
func (p *FilePicker) Draw(dst *ebiten.Image) {
	dst.Fill(colBG)
	drawHeader(dst, p.title, strings.Join(p.exts, "  "), colDim)
	drawText(dst, ellipsize(p.dir, fitChars(screenW-2*uiPadX)), uiPadX, uiBodyTop+8, colDim)

	const listW = screenW - 2*uiPadX
	vector.DrawFilledRect(dst, uiPadX-8, pkListTop-8, listW+16, pkRows*pkRowH+16, colPanel, false)

	if p.dirErr != "" {
		// The error replaces the listing outright. Falling through and
		// drawing the ".." row painted the selection highlight over this
		// very message, leaving an unexplained empty pane (audit A5).
		drawText(dst, "cannot read this folder: "+p.dirErr, uiPadX, pkListTop, colMiss)
		drawText(dst, "press Backspace to go back up", uiPadX, pkListTop+20, colBarline)
		drawFooter(dst, "backspace up a folder   esc cancel")
		return
	}
	for row := 0; row < pkRows; row++ {
		i := p.top + row
		if i >= len(p.listing) {
			break
		}
		e := p.listing[i]
		y := pkListTop + float64(row)*pkRowH
		hover := false
		if hi, ok := p.hitTest(p.ptr.x, p.ptr.y); ok && hi == i {
			hover = true
		}
		switch {
		case i == p.sel:
			vector.DrawFilledRect(dst, uiPadX-4, float32(y)-3, float32(listW)+8, pkRowH, colFocus, false)
		case hover:
			vector.DrawFilledRect(dst, uiPadX-4, float32(y)-3, float32(listW)+8, pkRowH, colHover, false)
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
		drawText(dst, truncate(label, fitChars(listW-16)), uiPadX+4, y, col)
	}
	if p.dirErr == "" && len(p.listing) == 0 {
		drawText(dst, "Nothing here matching "+strings.Join(p.exts, ", "), uiPadX+4, pkListTop, colDim)
	}
	drawFooter(dst, "up/dn select   enter open or choose   backspace up a folder   esc cancel")
}
