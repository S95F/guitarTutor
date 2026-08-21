package ui

// The tablature editor: writing a piece by hand, rather than importing
// one. It is a full Screen, so the Shell hosts it exactly like the
// practice view and the settings screen.
//
// The editing itself lives in internal/edit, which owns the score, the
// cursor, the undo history and every rule about what an edit may do. This
// file is the projection of that: it turns keys and clicks into calls on
// the document and draws what comes back. That split is what lets the hard
// part — bars staying exactly full, meter changes following their bars —
// be tested without a window, and it is why nothing here reasons about
// ticks beyond deciding where to put a pixel.
//
// The same split the other screens use applies to input: Update samples
// the mouse and keyboard once and everything after that is a plain method
// a test can call directly (handleKey, click, save, toggleText).
//
// Two views of the same piece. The grid is the guitar-shaped one — move a
// cursor, type fret numbers onto strings. F2 swaps it for the raw .gtab
// text (editortext.go), which is the whole format rather than the part the
// grid has controls for, and switching back parses it. Neither view is
// the "real" one: both are the document, seen differently.

import (
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/edit"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// Layout, in logical pixels.
const (
	// The caption over each toolbar group, then the icon row, then the
	// piece row. The captions are what say whether a control acts on the
	// note under the cursor, on the bar, or on the whole piece — the
	// question a flat row of chips never answered.
	edCaptionY = uiBodyTop
	edToolbarY = edCaptionY + iconCapH + 2
	edTrackY   = edToolbarY + iconBtnSize + 12
	edGridTop  = edTrackY + iconBtnSize + 18
	// edLabelW is the margin at the left of every system that carries the
	// string names. A guitarist reads a staff by which STRING a line is,
	// and six anonymous lines make you count from the bottom every time.
	edLabelW = 26.0
	edGridX  = uiPadX + edLabelW
	edGridW  = screenW - 2*uiPadX - edLabelW

	edStringGap = 22.0
	// edSysPadTop leaves room above the top string for the bar number and
	// the meter or tempo marking a bar may carry.
	edSysPadTop = 30.0
	// edSysPadBottom leaves room under the bottom string for the rhythm
	// marks — the stems and flags that say how long each beat is, and the
	// 3 under a triplet. Without them the only clue to a note's length is
	// how far apart it is drawn from the next one, which is a comparison
	// and not a reading.
	edSysPadBottom = 40.0
	edSysGap       = 10.0

	// edPxPerQuarter is how wide a quarter note is drawn. Bars are sized
	// from their length so a 3/4 bar is visibly shorter than a 4/4 one.
	edPxPerQuarter = 56.0
	// edMinBeatW is the narrowest a beat may be drawn. A bar of 32nds would
	// otherwise stack its fret numbers on top of each other; such a bar is
	// widened past its proportional size instead.
	edMinBeatW = 26.0
	edBarInset = 7.0

	// edFretHoldFrames is how long a typed digit waits for a second one
	// before it is taken as final. Two-digit frets have to be typeable, and
	// a fret is entered by typing it, so "1" then "2" within this window is
	// fret 12 and after it is fret 1 then fret 2.
	// edMaxStrings bounds the per-string scratch the staff drawing keeps.
	// No instrument this application draws has more, and the array is on
	// the stack rather than allocated per bar per frame.
	edMaxStrings     = 16
	edFretHoldFrames = 40
	// edMsgFrames is how long the status line holds a message.
	edMsgFrames = 300
)

var (
	colEdCursor = color.RGBA{60, 110, 170, 255} // the cell the next edit lands in
	colEdCaret  = color.RGBA{120, 190, 255, 255}
	colEdRest   = color.RGBA{110, 110, 126, 255}
	// colTieArc is the slur that holds a note over. Brighter than the
	// staff lines it crosses, quieter than the fret numbers it joins.
	colTieArc = color.RGBA{150, 170, 200, 255}
)

// An Editor is one piece being written. Create it with NewEditor (a blank
// piece) or NewEditorFor (an existing one).
type Editor struct {
	sh  *Shell
	doc *edit.Doc

	// path is where Save writes. Empty means the piece has never been
	// saved and Save has to ask.
	path string

	ptr      pointer
	anim     animator
	tip      tips
	frame    int64
	scroll   float64
	helpOpen bool

	msg      string
	msgErr   bool
	msgUntil int64

	// Pending fret digits and the frame they expire on: see
	// edFretHoldFrames.
	fretDigits string
	fretUntil  int64

	// entry is the one-line prompt open over the grid (a tempo, a meter, a
	// title), or nil.
	entry *edEntry

	// text is the raw .gtab view when it is showing.
	text *gtabPane

	// leaving is the "you have unsaved changes" prompt.
	leaving bool

	// picker is the open instrument choice (a new piece, a new track), or
	// nil.
	picker *edPicker

	// practicePending remembers that a save was asked for BY shift+P, so
	// the dialog's answer can finish with the practice it promised — the
	// same shape as a save asked for on the way out finishing the leaving.
	practicePending bool

	// The save dialog runs off the game loop, like every other dialog in
	// this application, and posts its answer to a mailbox Update drains.
	saveDialog func(suggest string)
	dialogBusy bool
	mailMu     sync.Mutex
	mail       *string

	// onSaved lets the integrator record the file as recently opened.
	onSaved func(path string)
	// practice hands the saved piece to the Shell to be played.
	practice func(path string)
}

// NewEditor starts a blank piece.
func NewEditor(sh *Shell) *Editor {
	return &Editor{sh: sh, doc: edit.New(edit.NewOptions{})}
}

// NewEditorChoosing starts a blank piece with the instrument picker up, so
// the first thing a new piece asks is what it will be played on. The
// document underneath is the guitar default until the picker answers;
// cancelling keeps it.
func NewEditorChoosing(sh *Shell) *Editor {
	e := NewEditor(sh)
	e.openInstrumentPicker(pickNewPiece)
	return e
}

// NewEditorFor opens an existing piece for editing. path is where Save
// will write; pass "" for a piece that has no file yet (an imported one,
// say, which must be saved as .gtab before it can be written at all).
func NewEditorFor(sh *Shell, sc *score.Score, path string) (*Editor, error) {
	doc, err := edit.Open(sc)
	if err != nil {
		return nil, err
	}
	// Only a .gtab file can be written back; anything else has to be saved
	// under a new name, so the path is deliberately dropped rather than
	// kept and refused later.
	if strings.ToLower(filepath.Ext(path)) != ".gtab" {
		path = ""
	}
	return &Editor{sh: sh, doc: doc, path: path}, nil
}

// NewEditorForText opens the editor directly in its text view on raw
// .gtab source — the way in for a file that will NOT parse, which has no
// score to build a document from and whose only route to repair is the
// text it already is. The pane's own reparse shows the problem; fixing
// the text and leaving the view (or saving) takes the normal applyText
// path. The document behind the pane is a blank piece nobody sees: the
// grid only appears once the text parses into a real document.
func NewEditorForText(sh *Shell, src []byte, path string) *Editor {
	e := NewEditor(sh)
	if strings.ToLower(filepath.Ext(path)) == ".gtab" {
		e.path = path
	}
	e.text = newGtabPaneFromSource(src)
	e.text.fromFile = true
	return e
}

// SetSaveDialog installs the integrator's save dialog. It is called with a
// suggested file name, blocks while the user chooses, and must always
// answer through OfferSavePath — with "" when the user cancels, so the
// screen's busy guard re-arms either way.
func (e *Editor) SetSaveDialog(fn func(suggest string)) { e.saveDialog = fn }

// SetOnSaved installs a hook called with the path each successful save
// wrote, so the integrator can record it as recently opened.
func (e *Editor) SetOnSaved(fn func(path string)) { e.onSaved = fn }

// SetPractice installs the hook that opens a saved piece for practice.
// Without one the PRACTICE action is drawn disabled rather than lying.
func (e *Editor) SetPractice(fn func(path string)) { e.practice = fn }

// OfferSavePath posts the save dialog's answer. Safe to call from the
// dialog's own goroutine; Update drains it on the game loop.
func (e *Editor) OfferSavePath(path string) {
	e.mailMu.Lock()
	e.mail = &path
	e.mailMu.Unlock()
}

// Doc exposes the document, for tests and for an integrator that wants to
// read the piece being written.
func (e *Editor) Doc() *edit.Doc { return e.doc }

// SetHelpOpen opens or closes the key-binding overlay without a keypress,
// so the screenshot tool can photograph it.
func (e *Editor) SetHelpOpen(open bool) { e.helpOpen = open }

// ShowTextView switches between the notation and the raw .gtab text, and
// reports whether it could: going back to the notation parses the text,
// which can fail. It is toggleText with the destination named rather than
// implied — what the screenshot tool and a scripted integrator want.
func (e *Editor) ShowTextView(on bool) bool {
	if on == (e.text != nil) {
		return true
	}
	e.toggleText()
	return on == (e.text != nil)
}

// Path is the file the piece saves to, or "" if it has never been saved.
func (e *Editor) Path() string { return e.path }

// --- messages ---------------------------------------------------------

// say posts a transient status line. Errors are held in a different
// colour, because "that did not fit" and "saved" must not look alike.
func (e *Editor) say(msg string) { e.msg, e.msgErr, e.msgUntil = msg, false, e.frame+edMsgFrames }

// report posts an operation's refusal. A nil error is success and says
// nothing, which is what lets every call site read as `e.report(op())`.
func (e *Editor) report(err error) bool {
	if err == nil {
		return true
	}
	e.msg, e.msgErr, e.msgUntil = err.Error(), true, e.frame+edMsgFrames
	return false
}

// ShowError posts a refusal onto the editor's status line, in the same
// colour and for the same hold as the editor's own (see report). It
// exists for the actions the integrator performs on the editor's behalf —
// opening the saved piece for practice, say — which can fail with
// something the user needs to read: stderr is invisible to the windowed
// user, and a key that visibly does nothing reads as a dead key. The
// mirror of Browser.ShowError, one screen over.
func (e *Editor) ShowError(msg string) {
	e.msg, e.msgErr, e.msgUntil = msg, true, e.frame+edMsgFrames
}

// message is the status line still worth showing, and whether it is a
// refusal.
func (e *Editor) message() (string, bool) {
	if e.msg == "" || e.frame >= e.msgUntil {
		return "", false
	}
	return e.msg, e.msgErr
}

// --- the Screen contract ---------------------------------------------

// Update advances one frame. The order is the order of modality: a
// pending dialog answer, then the leave prompt, then the help overlay,
// then a one-line entry, then the view that is showing.
func (e *Editor) Update() error {
	e.frame++
	e.ptr = readPointer()
	e.drainDialog()

	if e.picker != nil {
		e.updatePicker()
		return nil
	}
	if e.leaving {
		return e.updateLeavePrompt()
	}
	if e.helpOpen {
		if helpDismissed(e.ptr) {
			e.helpOpen = false
		}
		return nil
	}
	if e.entry != nil {
		e.updateEntry()
		return nil
	}
	if e.text != nil {
		return e.updateText()
	}
	if err := e.updateGrid(); err != nil {
		return err
	}
	return nil
}

// Draw paints the frame.
func (e *Editor) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)
	e.anim.tick()
	drawHeader(screen, e.headerTitle(), e.statusLine(), colHUD)
	e.drawChrome(screen, e.layoutToolbar())
	if e.text != nil {
		e.drawTextPane(screen)
	} else {
		e.drawGrid(screen)
		e.drawFirstSteps(screen)
	}
	if msg, isErr := e.message(); msg != "" {
		col := colSounding
		if isErr {
			col = colMiss
		}
		drawText(screen, truncateW(msg, screenW-2*uiPadX), uiPadX, uiFooterY-20, col)
	}
	drawFooter(screen, e.hintLine())
	// The tooltip goes over the content and under the modals: while a
	// prompt is up the toolbar cannot be pressed, so a name floating over
	// the dimmed screen would be pointing at nothing.
	if e.modalUp() {
		e.tip.hide()
	} else {
		e.tip.draw(screen)
	}
	if e.entry != nil {
		e.drawEntry(screen)
	}
	if e.leaving {
		e.drawLeavePrompt(screen)
	}
	if e.helpOpen {
		title, rows := e.helpTable()
		drawHelpOverlay(screen, title, rows, editorHelpFootnote)
	}
	if e.picker != nil {
		e.drawPicker(screen)
	}
}

// helpTable is what the overlay behind ? shows: the keys of the view the
// user is actually looking at. The grid's table tells a text-view user
// that digits type frets and h is a hammer-on, none of which is true
// there — in the text view those keys just type characters.
func (e *Editor) helpTable() (title string, rows []helpBinding) {
	if e.text != nil {
		return "TEXT VIEW KEYS", e.textBindings()
	}
	return "EDITOR KEYS", e.editorBindings()
}

// headerTitle names the piece, with the unsaved marker every editor has.
func (e *Editor) headerTitle() string {
	name := e.doc.Score().Title
	if name == "" {
		name = "Untitled piece"
	}
	if e.doc.Dirty() {
		name += " •"
	}
	// The budget is what the header actually has left: the width less both
	// margins, the status line sharing the row, and a gap. A fixed half
	// the window let a long title run into a long status — the same fault
	// the practice view's own title had (audit A-series).
	const gap = 32.0
	budget := screenW - 2*uiPadX - textW(e.statusLine()) - gap
	return truncateWScaled(name, budget, uiTitleScl)
}

// statusLine is the right-hand header text: where the cursor is and what
// is in force there.
func (e *Editor) statusLine() string {
	c := e.doc.Cursor()
	bar := e.doc.Bar()
	where := fmt.Sprintf("bar %d/%d   %s", c.Bar+1, e.doc.BarCount(), e.cursorPitch())
	// The note value is labelled: next to a time signature, a bare "1/4"
	// reads as a second one.
	what := fmt.Sprintf("%d/%d   %.0f BPM   note: %s", bar.Num, bar.Den, e.doc.TempoAtCursor(), durationName(e.doc.Duration()))
	file := "unsaved"
	if e.path != "" {
		file = filepath.Base(e.path)
	}
	return where + "     " + what + "     " + file
}

// cursorPitch names the string the cursor is on and, when there is a note
// on it, the pitch that note actually SOUNDS. Fret numbers are positions,
// not pitches — the same 5 is an A on one string and a D on the next, and
// under a capo it is neither — so a writer checking what they just typed
// against what they can hear needs the app to say which note it is.
func (e *Editor) cursorPitch() string {
	tr := e.doc.Track()
	if tr.Wind != nil {
		return e.windCursorPitch()
	}
	str := e.doc.Cursor().Str
	label := fmt.Sprintf("%s string", edStringName(tr, str))
	n, ok := e.doc.NoteAt(str)
	if !ok {
		return label
	}
	return label + " · sounding " + edPitchName(tr.Pitch(n))
}

// edStringName is a string's open note, without an octave: what is
// written down the left of a tab staff.
//
// The TUNING's note, not the capoed one. A capo is a movable nut, and a
// guitarist still calls the fifth string the A string with one on — tab
// has always been written that way, and labelling a capo-2 drop-D piece
// "F# C# A E B E" is a set of names nobody uses for those strings. Where
// the capo does have to be accounted for is the pitch a note SOUNDS, and
// that is what cursorPitch reports.
func edStringName(tr *score.Track, str int) string {
	if str < 1 || str > len(tr.Tuning) {
		return "?"
	}
	return edPitchClass(tr.Tuning[str-1])
}

// edPitchClass and edPitchName spell a MIDI key. Sharps only: the editor
// has no key signature to decide otherwise, and a guess that contradicts
// the piece is worse than a consistent spelling.
var edPitchClasses = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

func edPitchClass(key int) string {
	if key < 0 || key > 127 {
		return "?"
	}
	return edPitchClasses[key%12]
}

func edPitchName(key int) string {
	if key < 0 || key > 127 {
		return "?"
	}
	return edPitchClasses[key%12] + strconv.Itoa(key/12-1)
}

// modalUp reports whether something is covering the editor: a one-line
// prompt, the unsaved-changes question, or the key list. While one is,
// the chrome underneath is inert.
func (e *Editor) modalUp() bool {
	return e.entry != nil || e.leaving || e.helpOpen || e.picker != nil
}

// --- the grid ---------------------------------------------------------

func (e *Editor) updateGrid() error {
	if s, rem := wheelSteps(e.ptr.wheel); s != 0 {
		e.scroll -= float64(s) * edStringGap * 2
		e.ptr.wheel = rem
	}
	if err := e.handleKeys(); err != nil {
		return err
	}
	// A key may have opened an entry, the text view or the leave prompt;
	// the click that follows belongs to whatever is showing next frame.
	if e.entry != nil || e.text != nil || e.leaving || e.helpOpen || e.picker != nil {
		return nil
	}
	e.handleMouse()
	return nil
}

// An edBarBox is one bar's place in a laid-out system.
type edBarBox struct {
	index int
	x, w  float64
}

// An edSystem is one row of bars, as notation calls it.
type edSystem struct {
	bars []edBarBox
}

// layoutSystems packs the track's bars into rows. Bar widths come from
// their length in ticks, so the notation reads proportionally, but a bar
// carrying more beats than that width can show is widened to fit them.
func (e *Editor) layoutSystems() []edSystem {
	tr := e.doc.Track()
	var systems []edSystem
	var cur edSystem
	x := 0.0
	for i, bar := range tr.Bars {
		w := edBarWidth(bar)
		if len(cur.bars) > 0 && x+w > edGridW {
			systems = append(systems, cur)
			cur = edSystem{}
			x = 0
		}
		cur.bars = append(cur.bars, edBarBox{index: i, x: x, w: w})
		x += w
	}
	if len(cur.bars) > 0 {
		systems = append(systems, cur)
	}
	return systems
}

// edBarWidth is how wide a bar is drawn.
func edBarWidth(bar *score.Bar) float64 {
	w := float64(bar.Len()) / float64(score.PPQ) * edPxPerQuarter
	if m := float64(len(bar.Beats))*edMinBeatW + 2*edBarInset; w < m {
		w = m
	}
	if w > edGridW {
		w = edGridW
	}
	return w
}

// edBeatX is where a beat sits inside its bar's box.
func edBeatX(box edBarBox, bar *score.Bar, bt *score.Beat) float64 {
	span := box.w - 2*edBarInset
	if bar.Len() <= 0 {
		return box.x + edBarInset
	}
	return box.x + edBarInset + float64(bt.Start-bar.Start)/float64(bar.Len())*span
}

// systemHeight is one row of bars, top to bottom.
func (e *Editor) systemHeight() float64 {
	n := e.gridLines()
	return edSysPadTop + float64(n-1)*edStringGap + edSysPadBottom + edSysGap
}

// gridBottom is where the notation area ends: above the status line and
// the footer hint.
func gridBottom() float64 { return uiFooterY - 28 }

// systemOfBar finds which laid-out system a bar index landed in.
func systemOfBar(systems []edSystem, bar int) int {
	for si, s := range systems {
		for _, b := range s.bars {
			if b.index == bar {
				return si
			}
		}
	}
	return 0
}

// clampScroll keeps the notation from being scrolled past either end, and
// keeps the cursor's own system on screen. The second half is what makes
// the arrow keys usable at all — walking off the bottom of the window with
// no way back is a worse bug than not scrolling.
func (e *Editor) clampScroll(systems []edSystem) {
	h := e.systemHeight()
	view := gridBottom() - edGridTop
	total := float64(len(systems)) * h
	max := total - view
	if max < 0 {
		max = 0
	}
	// The cursor's system first, so it wins over the previous scroll.
	si := float64(systemOfBar(systems, e.doc.Cursor().Bar))
	if top := si * h; e.scroll > top {
		e.scroll = top
	}
	if bot := (si+1)*h - view; e.scroll < bot {
		e.scroll = bot
	}
	if e.scroll > max {
		e.scroll = max
	}
	if e.scroll < 0 {
		e.scroll = 0
	}
}

func (e *Editor) drawGrid(dst *ebiten.Image) {
	// Everything below is drawn into the notation band only. Scrolling a
	// long piece puts a system partly above the top of the band, and the
	// per-system skip below only drops the ones that are ENTIRELY outside
	// — so without this the staff lines and fret numbers of a partly
	// visible system were painted straight over the toolbar.
	screen := dst.SubImage(image.Rect(0, int(edGridTop)-2, screenW, int(gridBottom()))).(*ebiten.Image)

	systems := e.layoutSystems()
	e.clampScroll(systems)
	tr := e.doc.Track()
	nStr := e.gridLines()
	cur := e.doc.Cursor()
	h := e.systemHeight()

	for si, sys := range systems {
		top := edGridTop + float64(si)*h - e.scroll
		if top+h < edGridTop || top > gridBottom() {
			continue
		}
		strTop := top + edSysPadTop
		// String lines run the width of the bars actually on this system,
		// not the whole page: a half-full last system that drew full-width
		// staves would look like bars that are not there.
		w := 0.0
		for _, b := range sys.bars {
			w = b.x + b.w
		}
		if tr.Wind != nil {
			// A wind system's lines are the pitch ladder's octave Cs, not
			// strings; see editorwind.go.
			drawWindSystemLines(screen, edWindLadderFor(tr.Wind), strTop, w)
		} else {
			for s := 0; s < nStr; s++ {
				y := float32(strTop + float64(s)*edStringGap)
				vector.StrokeLine(screen, edGridX, y, float32(edGridX+w), y, 1, colString, false)
				// The string's own note, in the margin. It is the tuning's, so
				// a drop-D piece says D at the bottom without anyone asking.
				name := edStringName(tr, s+1)
				drawTextSmall(screen, name, edGridX-10-textWSmall(name), float64(y)-7, colDim)
			}
		}
		rhythmY := strTop + float64(nStr-1)*edStringGap + 12
		for _, box := range sys.bars {
			e.drawBar(screen, tr, box, strTop, nStr, cur)
			e.drawRhythm(screen, tr, box, rhythmY)
		}
		// The closing barline of the system.
		x := float32(edGridX + w)
		vector.StrokeLine(screen, x, float32(strTop-6), x, float32(strTop+float64(nStr-1)*edStringGap+6), 1, colBarline, false)
	}
}

func (e *Editor) drawBar(screen *ebiten.Image, tr *score.Track, box edBarBox, strTop float64, nStr int, cur edit.Cursor) {
	bar := tr.Bars[box.index]
	// Where each string's last note was drawn and which beat it was in, so
	// a tie can be an ARC back to it. A tie holds the note in the PREVIOUS
	// beat (see score.Note.Tied), so a note two beats back is not what it
	// is tied to — arcing to that one draws a sustain across a rest that
	// playback does not produce. A string with nothing immediately before
	// it reads as -1 and gets the short leading arc a tie over a barline
	// is engraved with.
	var lastX [edMaxStrings]float64
	var lastBeat [edMaxStrings]int
	for i := range lastX {
		lastX[i] = -1
		lastBeat[i] = -2
	}
	x := float32(edGridX + box.x)
	vector.StrokeLine(screen, x, float32(strTop-6), x, float32(strTop+float64(nStr-1)*edStringGap+6), 1, colBarline, false)
	drawTextSmall(screen, strconv.Itoa(box.index+1), float64(x)+4, strTop-26, colHint)

	// A bar that starts a new meter or tempo says so above the staff, the
	// way notation does — and the way the file will, since both are only
	// writable at a barline.
	// The markings a bar can carry, in the order a score writes them: the
	// meter first, then the tempo with the note it counts in front of its
	// number. They are drawn as two pieces rather than one string because
	// the note glyph belongs to the TEMPO — put in front of the whole
	// marking it reads as counting the time signature.
	meter, tempo := e.barMarking(box.index, bar)
	mx := float64(x) + 4
	if meter != "" {
		drawTextSmall(screen, meter, mx, strTop-13, colInferred)
		mx += textWSmall(meter) + 10
	}
	if tempo != "" {
		drawGlyph(screen, glyphNoteQuarter, rect{mx, strTop - 15, 12, 13}, colInferred)
		drawTextSmall(screen, tempo, mx+13, strTop-13, colInferred)
	}

	// The technique letters sit to the RIGHT of a fret number, in the space
	// the next beat's own background is about to be painted over. Drawing
	// them in a second pass, after every beat has laid its background
	// down, is what stops "12h" losing its h whenever the beats are close
	// together.
	type pendingMark struct {
		s    string
		x, y float64
	}
	var marks []pendingMark

	// The wind band's vertical axis; nil on a fretted track.
	var ladder *edWindLadder
	if tr.Wind != nil {
		l := edWindLadderFor(tr.Wind)
		ladder = &l
	}
	// noteY is where a note draws: its string's line, or its written
	// pitch on the ladder.
	noteY := func(n score.Note) float64 {
		if ladder != nil {
			return ladder.y(tr.Wind.Written(tr.Pitch(n)), strTop)
		}
		return strTop + float64(n.String-1)*edStringGap
	}
	// cellY is where the lit cursor cell sits when the beat is EMPTY: the
	// cursor's string, or the pitch a typed letter would land nearest —
	// the cell is the promise of where the next note goes, and on a
	// ladder that place is a pitch, not a lane.
	cellY := func() float64 {
		if ladder != nil {
			return ladder.y(e.windEntryWritten(), strTop)
		}
		return strTop + float64(cur.Str-1)*edStringGap
	}

	for bi, bt := range bar.Beats {
		bx := edGridX + edBeatX(box, bar, bt)
		onCursor := box.index == cur.Bar && bi == cur.Beat
		if onCursor {
			// The cursor: a caret down the whole beat, and a lit cell where
			// the next note typed will land.
			vector.StrokeLine(screen, float32(bx-4), float32(strTop-8),
				float32(bx-4), float32(strTop+float64(nStr-1)*edStringGap+8), 1, colEdCaret, false)
			y := cellY()
			if len(bt.Notes) > 0 {
				y = noteY(bt.Notes[0])
				if ladder == nil {
					y = strTop + float64(cur.Str-1)*edStringGap
				}
			}
			drawCursorCellAt(screen, bx, y)
		}
		// A note draws its own background so the string line does not run
		// through the digits — and on the cursor's own cell that background
		// has to BE the cursor, or the highlight is painted out by the note
		// it is highlighting and only the empty half of it shows.
		bg := func(str int) color.RGBA {
			if onCursor && str == cur.Str {
				return colEdCursor
			}
			return colNoteBG
		}

		if len(bt.Notes) == 0 {
			// A rest is drawn on the middle string, where it does not read
			// as belonging to any one of them — as the rest SYMBOL, not as
			// the letter r, which is what the file writes and what nobody
			// reading a stave has ever seen.
			//
			// The backdrop that breaks the staff lines behind it is drawn
			// in the CURSOR's colour when this is the cursor's beat: it
			// overlaps the lit cell, and painting the page colour there
			// punched a hole through the one mark that says where the next
			// note will land.
			ry := strTop + float64(nStr-1)/2*edStringGap
			vector.DrawFilledRect(screen, float32(bx-3), float32(ry-10), 18, 20, colBG, false)
			drawGlyph(screen, edRestGlyph(bt.Dur), rect{bx - 2, ry - 9, 16, 18}, colEdRest)
			// The backdrop that breaks the staff lines behind the rest can
			// cross the lit cursor cell, which sits on whichever string the
			// cursor is on. Painting the page colour there punched a hole in
			// it — and painting the CURSOR colour instead drew a second lit
			// block, which reads as two cursors. The cell is simply laid
			// down again: there is no note under it to cover.
			if onCursor {
				drawCursorCellAt(screen, bx, cellY())
			}
			continue
		}
		for _, n := range bt.Notes {
			ny := noteY(n)
			label := strconv.Itoa(n.Fret)
			if ladder != nil {
				// A wind note's glyph is the written name its player reads,
				// exactly as the practice view draws it.
				label = edPitchName(tr.Wind.Written(tr.Pitch(n)))
			} else if n.Tech&score.TechDead != 0 {
				label = "x"
			}
			// A tie is an arc back to the note it holds, which is how it is
			// engraved. It used to be a tilde in front of the fret number —
			// the file format's character, and one that already means
			// vibrato to a guitarist.
			if n.Tied && n.String < edMaxStrings {
				from := -1.0
				if lastBeat[n.String] == bi-1 {
					from = lastX[n.String]
				}
				edDrawTie(screen, from, bx, ny)
			}
			col := colNote
			if n.Inferred {
				col = colInferred
			}
			w := float32(textWMono(label))
			vector.DrawFilledRect(screen, float32(bx-2), float32(ny-9), w+4, 18, bg(n.String), false)
			drawTextMono(screen, label, bx, ny-9, col)
			suffix := techSuffix(n.Tech)
			if ladder != nil {
				suffix = windTechSuffix(n.Tech)
			}
			if suffix != "" {
				marks = append(marks, pendingMark{suffix, bx + float64(w) + 3, ny - 9})
			}
			if n.String < edMaxStrings {
				lastX[n.String], lastBeat[n.String] = bx, bi
			}
		}
	}
	for _, m := range marks {
		drawTextSmall(screen, m.s, m.x, m.y, colSounding)
	}
}

// drawRhythm paints the note values under a bar: a stem per beat, with a
// flag for every halving past a quarter, a dot for a dotted value and a 3
// over a triplet. It is the notation half of a tab staff — fret numbers
// say WHERE and these say WHEN — and without it the only clue to a beat's
// length is its spacing, which tells you nothing about a beat on its own.
//
// Deliberately not beamed. Beaming groups by meter and by phrasing, and a
// wrong beam reads as a wrong rhythm; separate flags are always right,
// and they are what the editor needs to make a length checkable at a
// glance.
func (e *Editor) drawRhythm(screen *ebiten.Image, tr *score.Track, box edBarBox, y float64) {
	bar := tr.Bars[box.index]
	for _, bt := range bar.Beats {
		x := float32(edGridX + edBeatX(box, bar, bt) + 3)
		base, dot, trip := baseOf(bt.Dur)
		col := colDim
		if len(bt.Notes) == 0 {
			col = colBarline // a rest's rhythm is quieter than a note's
		}

		if base == score.Whole {
			// A whole note has no stem anywhere in notation; a short bar
			// under the staff says "this one is as long as they get".
			vector.StrokeLine(screen, x-3, float32(y+8), x+3, float32(y+8), 1, col, false)
		} else {
			h := float32(11)
			if base == score.Half {
				h = 7 // a shorter stem, the way a half note is lighter
			}
			vector.StrokeLine(screen, x, float32(y), x, float32(y)+h, 1, col, false)
			for f := 0; f < edFlagsFor(base); f++ {
				fy := float32(y) + h - float32(f)*3
				vector.StrokeLine(screen, x, fy, x+5, fy-3, 1, col, false)
			}
		}
		if dot {
			vector.DrawFilledRect(screen, x+3, float32(y+3), 2, 2, col, false)
		}
		if trip {
			// Under the stem, not over it: over it is where the bottom
			// string is, and a 3 drawn there sat on the staff among the
			// fret numbers and read as one of them.
			drawTextSmall(screen, "3", float64(x)-2, y+12, col)
		}
	}
}

// edFlagsFor is how many flags a plain note value carries: one for an
// eighth, two for a sixteenth, three for a thirty-second.
func edFlagsFor(base int64) int {
	switch base {
	case score.Eighth:
		return 1
	case score.Sixteenth:
		return 2
	case score.ThirtySec:
		return 3
	}
	return 0
}

// isEmpty reports whether the piece has no notes anywhere yet. It stops
// at the first one it finds, so it costs nothing on a piece that is not
// empty — which is every piece it returns false for.
func (e *Editor) isEmpty() bool {
	for _, tr := range e.doc.Score().Tracks {
		for _, bar := range tr.Bars {
			for _, bt := range bar.Beats {
				if len(bt.Notes) > 0 {
					return false
				}
			}
		}
	}
	return true
}

// drawFirstSteps paints the three sentences that get somebody from a
// blank staff to a bar of music, and disappears the moment there is a
// note on the staff.
//
// The help overlay behind ? is a reference: complete, alphabetised by
// nothing, and useless to a person who does not yet know that this screen
// is a thing you type fret numbers into. What a blank staff needs is the
// first three keys, in the order they are pressed, where the notation is
// about to appear.
func (e *Editor) drawFirstSteps(screen *ebiten.Image) {
	if !e.isEmpty() {
		return
	}
	// Under the last system that is on screen, not under the first: an
	// empty piece can be long enough to fill two systems, and a fixed
	// offset painted the guidance straight through the second staff.
	systems := e.layoutSystems()
	y := edGridTop + float64(len(systems))*e.systemHeight() - e.scroll + 14
	if min := edGridTop + e.systemHeight(); y < min {
		y = min
	}
	if y+120 > gridBottom() {
		return // no room for it without running into the status line
	}
	lines, tail := firstStepsContent()
	if e.doc.Track().Wind != nil {
		lines, tail = windFirstStepsContent()
	}
	drawText(screen, "Writing a piece", edGridX, y, colInferred)
	for i, l := range lines {
		ly := y + 26 + float64(i)*22
		drawTextMono(screen, l.key, edGridX, ly, colSounding)
		drawText(screen, l.what, edGridX+64, ly, colHUD)
	}
	drawTextSmall(screen, tail, edGridX, y+26+float64(len(lines))*22+8, colHint)
}

// A firstStep is one line of the blank-staff guidance.
type firstStep struct{ key, what string }

// firstStepsContent is what the guidance says. It is a function rather
// than literals inside drawFirstSteps so the tests that keep its fret
// range and its shift+P promise honest read the strings the screen draws.
// The fret bound comes from the format itself; a hand-written "0-24" here
// once disagreed with the "0-30" the help overlay claimed.
func firstStepsContent() ([]firstStep, string) {
	lines := []firstStep{
		{fmt.Sprintf("0-%d", textfmt.MaxFret), "type a fret number onto the highlighted string"},
		{"↑ ↓", "choose the string; [ and ] choose the note value"},
		{"space", "move on to the next beat — and past the last bar, it makes another"},
	}
	tail := "ctrl+S saves it into your library · shift+P opens it for practice · ? lists every key"
	return lines, tail
}

// barMarking is what is written above a bar that begins a new meter or a
// new tempo: the time signature, and the tempo without its note, which
// the caller draws.
func (e *Editor) barMarking(index int, bar *score.Bar) (meter, tempo string) {
	sc := e.doc.Score()
	if index == 0 {
		meter = fmt.Sprintf("%d/%d", bar.Num, bar.Den)
	} else if prev := e.doc.Track().Bars[index-1]; prev.Num != bar.Num || prev.Den != bar.Den {
		meter = fmt.Sprintf("%d/%d", bar.Num, bar.Den)
	}
	for _, t := range sc.Tempos {
		if t.Tick == bar.Start && (index > 0 || len(sc.Tempos) > 0) {
			if index == 0 || t.Tick != 0 {
				tempo = fmt.Sprintf("= %.0f", 60e6/float64(t.USPerQuarter))
			}
		}
	}
	return meter, tempo
}

// drawCursorCellAt lights the cell the next note typed will land in: the
// beat at bx, at the line (a string's, or a ladder pitch's) at cy.
func drawCursorCellAt(dst *ebiten.Image, bx, cy float64) {
	vector.DrawFilledRect(dst, float32(bx-5), float32(cy-10), 28, 20, colEdCursor, false)
}

// edRestGlyph is the rest that stands for a length. A whole and a half
// rest are their own shapes and are what a bar of silence is written
// with; everything shorter shares the quarter rest's outline, since the
// rhythm marks under the staff already say which it is and a flagged rest
// at sixteen pixels is a smudge.
func edRestGlyph(dur int64) glyphID {
	switch base, _, _ := baseOf(dur); base {
	case score.Whole:
		return glyphRestWhole
	case score.Half:
		return glyphRestHalf
	}
	return glyphRest
}

// edDrawTie draws the arc that holds a note over from the previous one.
// from is where that note was drawn, or negative when it was in an
// earlier bar — in which case the arc comes in from the left, the way a
// tie across a barline is engraved.
func edDrawTie(dst *ebiten.Image, from, to, y float64) {
	start := from + 8
	if from < 0 || start > to-10 {
		start = to - 16
	}
	var p vector.Path
	p.MoveTo(float32(start), float32(y-11))
	p.QuadTo(float32((start+to)/2), float32(y-20), float32(to+3), float32(y-11))
	op := &vector.DrawPathOptions{AntiAlias: true}
	op.ColorScale.ScaleWithColor(colTieArc)
	vector.StrokePath(dst, &p, &vector.StrokeOptions{Width: 1.4, LineCap: vector.LineCapRound}, op)
}

// techSuffix spells a note's techniques the way the file does, minus the
// dead-note mark, which is drawn as the fret number itself.
func techSuffix(t score.Technique) string {
	var b strings.Builder
	for _, m := range []struct {
		bit  score.Technique
		char byte
	}{
		{score.TechHammer, 'h'}, {score.TechPull, 'p'}, {score.TechSlide, 's'},
		{score.TechBend, 'b'}, {score.TechVibrato, 'v'},
	} {
		if t&m.bit != 0 {
			b.WriteByte(m.char)
		}
	}
	return b.String()
}

// --- mouse ------------------------------------------------------------

func (e *Editor) handleMouse() {
	if e.ptr.hit(e.hotspots()) {
		return
	}
	if e.ptr.pressed {
		e.clickGrid(e.ptr.x, e.ptr.y)
	}
}

// clickGrid puts the cursor where the notation was clicked. It reports
// whether the click landed on the staff at all.
func (e *Editor) clickGrid(px, py float64) bool {
	if py < edGridTop || py > gridBottom() || px < edGridX || px > edGridX+edGridW {
		return false
	}
	systems := e.layoutSystems()
	// The wheel moves e.scroll during Update and only Draw clamps it, so a
	// flick and a click in the same frame would otherwise map the click
	// through a scroll the user has never seen drawn — past the ends, the
	// system index fell out of range and the click went dead.
	e.clampScroll(systems)
	h := e.systemHeight()
	si := int((py - edGridTop + e.scroll) / h)
	if si < 0 || si >= len(systems) {
		return false
	}
	sys := systems[si]
	strTop := edGridTop + float64(si)*h + edSysPadTop - e.scroll
	str := 1
	if e.doc.Track().Wind == nil {
		nStr := len(e.doc.Track().Tuning)
		str = int((py-strTop+edStringGap/2)/edStringGap) + 1
		if str < 1 {
			str = 1
		}
		if str > nStr {
			str = nStr
		}
	}

	// The bar whose box contains the click, or the last one on the system
	// when the click is past its end.
	x := px - edGridX
	box := sys.bars[len(sys.bars)-1]
	for _, b := range sys.bars {
		if x < b.x+b.w {
			box = b
			break
		}
	}
	bar := e.doc.Track().Bars[box.index]
	// The beat whose span contains the click: each beat owns from its own
	// x up to the next one's.
	beat := 0
	for bi, bt := range bar.Beats {
		if x >= edBeatX(box, bar, bt)-4 {
			beat = bi
		}
	}
	e.doc.GoTo(edit.Cursor{Bar: box.index, Beat: beat, Str: str})
	return true
}

// --- keyboard ---------------------------------------------------------

// editorKeys is every key the grid reacts to. Building the list once and
// scanning it is what keeps Update out of the business of asking
// Ebitengine about forty keys by hand.
var editorKeys = []ebiten.Key{
	ebiten.KeyLeft, ebiten.KeyRight, ebiten.KeyUp, ebiten.KeyDown,
	ebiten.KeyHome, ebiten.KeyEnd, ebiten.KeyPageUp, ebiten.KeyPageDown,
	ebiten.KeyDigit0, ebiten.KeyDigit1, ebiten.KeyDigit2, ebiten.KeyDigit3, ebiten.KeyDigit4,
	ebiten.KeyDigit5, ebiten.KeyDigit6, ebiten.KeyDigit7, ebiten.KeyDigit8, ebiten.KeyDigit9,
	ebiten.KeyNumpad0, ebiten.KeyNumpad1, ebiten.KeyNumpad2, ebiten.KeyNumpad3, ebiten.KeyNumpad4,
	ebiten.KeyNumpad5, ebiten.KeyNumpad6, ebiten.KeyNumpad7, ebiten.KeyNumpad8, ebiten.KeyNumpad9,
	ebiten.KeyDelete, ebiten.KeyBackspace, ebiten.KeyEnter, ebiten.KeyNumpadEnter,
	ebiten.KeySpace,
	ebiten.KeyR, ebiten.KeyN, ebiten.KeyH, ebiten.KeyP, ebiten.KeyS, ebiten.KeyB, ebiten.KeyA,
	ebiten.KeyV, ebiten.KeyX, ebiten.KeyT, ebiten.KeyU, ebiten.KeyM, ebiten.KeyZ, ebiten.KeyY,
	// The wind lane's note letters and its slur; inert on a fretted track.
	ebiten.KeyC, ebiten.KeyD, ebiten.KeyE, ebiten.KeyF, ebiten.KeyG, ebiten.KeyL,
	ebiten.KeyBracketLeft, ebiten.KeyBracketRight, ebiten.KeyPeriod, ebiten.KeyBackquote,
	ebiten.KeyTab, ebiten.KeyF1, ebiten.KeyF2, ebiten.KeySlash, ebiten.KeyEscape,
}

// editorRepeatKeys auto-repeat while held. Movement and deletion do;
// nothing that creates or destroys a bar does.
var editorRepeatKeys = map[ebiten.Key]bool{
	ebiten.KeyLeft: true, ebiten.KeyRight: true, ebiten.KeyUp: true, ebiten.KeyDown: true,
	ebiten.KeyPageUp: true, ebiten.KeyPageDown: true,
	ebiten.KeyDelete: true, ebiten.KeyBackspace: true,
	ebiten.KeySpace: true,
}

// editorKeyFires decides whether a key held for d frames acts this frame.
func editorKeyFires(repeat bool, d int) bool {
	if d == 1 {
		return true
	}
	if !repeat {
		return false
	}
	return d > 30 && (d-30)%4 == 0
}

// mods is the modifier state one key press was made with. It is passed
// rather than read so that handleKey stays a plain function of its
// arguments and a test can press ctrl+Z without a game loop.
type mods struct{ shift, ctrl bool }

// readMods samples the modifier keys.
func readMods() mods {
	return mods{
		shift: ebiten.IsKeyPressed(ebiten.KeyShiftLeft) || ebiten.IsKeyPressed(ebiten.KeyShiftRight),
		ctrl: ebiten.IsKeyPressed(ebiten.KeyControlLeft) || ebiten.IsKeyPressed(ebiten.KeyControlRight) ||
			ebiten.IsKeyPressed(ebiten.KeyMetaLeft) || ebiten.IsKeyPressed(ebiten.KeyMetaRight),
	}
}

// handleKeys dispatches every key that fires this frame.
func (e *Editor) handleKeys() error {
	m := readMods()
	for _, k := range editorKeys {
		d := inpututil.KeyPressDuration(k)
		if d == 0 || !editorKeyFires(editorRepeatKeys[k], d) {
			continue
		}
		if err := e.handleKey(k, m); err != nil {
			return err
		}
		if e.entry != nil || e.text != nil || e.leaving || e.helpOpen || e.picker != nil {
			return nil
		}
	}
	e.expireFretDigits()
	return nil
}

// handleKey applies one key press. It returns errQuit when the user
// leaves the editor.
func (e *Editor) handleKey(k ebiten.Key, m mods) error {
	wind := e.doc.Track().Wind != nil
	if digit, ok := digitOf(k); ok {
		if wind {
			e.report(fmt.Errorf("a wind part takes note names — A to G puts one down, ↑ and ↓ move it"))
			return nil
		}
		e.typeFret(digit)
		return nil
	}
	// Anything that is not another digit ends a pending fret.
	e.commitFretDigits()

	if m.ctrl {
		switch k {
		case ebiten.KeyZ:
			if m.shift {
				e.redo()
			} else {
				e.undo()
			}
			return nil
		case ebiten.KeyY:
			e.redo()
			return nil
		case ebiten.KeyS:
			if m.shift {
				e.saveAs()
			} else {
				e.save()
			}
			return nil
		}
		return nil
	}

	switch k {
	case ebiten.KeyEscape:
		return e.leave()
	case ebiten.KeyLeft:
		e.doc.MoveBeat(-1)
	case ebiten.KeyRight:
		e.doc.MoveBeat(1)
	case ebiten.KeyUp:
		if wind {
			// Up raises the pitch — a semitone, an octave with shift — on
			// the one lane a wind part has; there is no string to choose.
			e.report(e.doc.NudgePitch(nudgeStep(m)))
		} else {
			e.doc.MoveString(-1)
		}
	case ebiten.KeyDown:
		if wind {
			e.report(e.doc.NudgePitch(-nudgeStep(m)))
		} else {
			e.doc.MoveString(1)
		}
	case ebiten.KeyPageUp:
		e.doc.MoveBar(-1)
	case ebiten.KeyPageDown:
		e.doc.MoveBar(1)
	case ebiten.KeyHome:
		e.doc.GoToStart()
	case ebiten.KeyEnd:
		e.doc.GoToEnd()

	case ebiten.KeyDelete, ebiten.KeyBackspace:
		if m.shift {
			e.report(e.doc.DeleteBeat())
		} else {
			e.report(e.doc.ClearNote())
		}
	case ebiten.KeyR:
		e.report(e.doc.ClearBeat())
	case ebiten.KeyEnter, ebiten.KeyNumpadEnter:
		e.report(e.doc.InsertBeat())
	case ebiten.KeySpace:
		e.advance()

	case ebiten.KeyBracketLeft:
		e.stepDuration(-1)
	case ebiten.KeyBracketRight:
		e.stepDuration(1)
	case ebiten.KeyPeriod:
		e.modifyDuration(dotted)
	case ebiten.KeyT:
		if m.shift {
			e.openEntry(edEntryTitle)
		} else {
			e.modifyDuration(triplet)
		}
	case ebiten.KeyBackquote:
		e.report(e.doc.ToggleTie())

	case ebiten.KeyH:
		if wind {
			e.report(fmt.Errorf("no hammer-ons on a %s — l marks a slur", e.doc.Track().Wind.Name))
		} else {
			e.report(e.doc.ToggleTech(score.TechHammer))
		}
	case ebiten.KeyP:
		if m.shift {
			e.saveAndPractice()
		} else if wind {
			e.report(fmt.Errorf("no pull-offs on a %s — l marks a slur", e.doc.Track().Wind.Name))
		} else {
			e.report(e.doc.ToggleTech(score.TechPull))
		}
	case ebiten.KeyS:
		e.report(e.doc.ToggleTech(score.TechSlide))
	case ebiten.KeyB:
		if m.shift {
			e.openEntry(edEntryTempo)
		} else if wind {
			// B is the note, not the bend: a wind part's letters are its
			// pitches, and the bend mark keeps its toolbar button.
			e.typeNoteLetter(edNoteClasses[k])
		} else {
			e.report(e.doc.ToggleTech(score.TechBend))
		}
	case ebiten.KeyV:
		e.report(e.doc.ToggleTech(score.TechVibrato))
	case ebiten.KeyX:
		if wind {
			e.report(fmt.Errorf("no dead notes on a %s — R makes the beat a rest", e.doc.Track().Wind.Name))
		} else {
			e.report(e.doc.ToggleTech(score.TechDead))
		}
	case ebiten.KeyL:
		if wind {
			e.report(e.doc.ToggleTech(score.TechSlur))
		}
	case ebiten.KeyC, ebiten.KeyD, ebiten.KeyE, ebiten.KeyG:
		if wind {
			e.typeNoteLetter(edNoteClasses[k])
		}
	case ebiten.KeyF:
		if wind && !m.shift {
			e.typeNoteLetter(edNoteClasses[k])
		}

	case ebiten.KeyN:
		if m.shift {
			e.report(e.doc.DeleteBar())
		} else {
			e.addBarAfterCursor()
		}
	case ebiten.KeyM:
		if m.shift {
			e.openEntry(edEntryMeter)
		}
	case ebiten.KeyU:
		if m.shift {
			if wind {
				e.report(fmt.Errorf("a %s has no tuning to cycle", e.doc.Track().Wind.Name))
			} else {
				e.cycleTuning()
			}
		}
	case ebiten.KeyA:
		if m.shift {
			// Adding a track had no key at all, and its control is the one
			// the piece row runs out of room for first.
			e.openInstrumentPicker(pickAddTrack)
		} else if wind {
			e.typeNoteLetter(edNoteClasses[k])
		}
	case ebiten.KeyTab:
		e.stepTrack(m.shift)
	case ebiten.KeyF2:
		e.toggleText()
	case ebiten.KeyF1, ebiten.KeySlash:
		e.helpOpen = true
	}
	return nil
}

// nudgeStep is how far the arrow keys move a wind note: a semitone, an
// octave with shift.
func nudgeStep(m mods) int {
	if m.shift {
		return 12
	}
	return 1
}

// digitOf maps a number key — top row or numpad — to its value.
func digitOf(k ebiten.Key) (int, bool) {
	switch {
	case k >= ebiten.KeyDigit0 && k <= ebiten.KeyDigit9:
		return int(k - ebiten.KeyDigit0), true
	case k >= ebiten.KeyNumpad0 && k <= ebiten.KeyNumpad9:
		return int(k - ebiten.KeyNumpad0), true
	}
	return 0, false
}

// typeFret takes one digit of a fret number. Frets go past nine, so a
// digit is held briefly in case a second one follows; the note is written
// on every keystroke either way, so what is on screen is always what has
// been typed.
func (e *Editor) typeFret(digit int) {
	if e.frame >= e.fretUntil {
		e.fretDigits = ""
	}
	next := e.fretDigits + strconv.Itoa(digit)
	fret, err := strconv.Atoi(next)
	if err != nil || fret > textfmt.MaxFret {
		// The second digit would take it out of range, so it starts a fret
		// of its own instead of being dropped.
		next = strconv.Itoa(digit)
		fret = digit
	}
	if !e.report(e.doc.SetFret(fret)) {
		e.fretDigits, e.fretUntil = "", 0
		return
	}
	e.fretDigits, e.fretUntil = next, e.frame+edFretHoldFrames
}

// commitFretDigits ends a pending multi-digit fret, so the next digit
// typed starts a new one.
func (e *Editor) commitFretDigits() { e.fretDigits, e.fretUntil = "", 0 }

// expireFretDigits drops a pending fret once its window has passed.
func (e *Editor) expireFretDigits() {
	if e.frame >= e.fretUntil {
		e.fretDigits = ""
	}
}

// advance moves onto the next beat, adding a bar when there is no next
// beat to move onto.
//
// It is the difference between filling in a form and writing music. The
// arrow keys navigate what is already there, which is what you want when
// correcting; writing a piece from nothing is a rhythm of type-a-fret,
// move-on, type-a-fret, and running into the end of the last bar every
// four notes to press a different key for "make me somewhere to put the
// next one" breaks it. Space never stops.
func (e *Editor) advance() {
	c := e.doc.Cursor()
	atEnd := c.Bar == e.doc.BarCount()-1 && c.Beat == len(e.doc.Bar().Beats)-1
	if !atEnd {
		e.doc.MoveBeat(1)
		return
	}
	// AppendBar puts the cursor in the bar it makes.
	e.report(e.doc.AppendBar())
}

// --- duration ---------------------------------------------------------

// durationMod names the two things a plain note value can be turned into.
type durationMod int

const (
	dotted durationMod = iota
	triplet
)

// plainDurations are the six note values, longest first, that [ and ]
// step through. Dots and triplets are modifiers on top (see
// modifyDuration), which is exactly how the format spells them.
var plainDurations = []int64{
	score.Whole, score.Half, score.Quarter, score.Eighth, score.Sixteenth, score.ThirtySec,
}

// baseOf splits a duration into the plain value it is built on and what
// was done to it.
func baseOf(ticks int64) (base int64, dot, trip bool) {
	for _, b := range plainDurations {
		switch ticks {
		case b:
			return b, false, false
		case score.Dotted(b):
			return b, true, false
		case score.Triplet(b):
			return b, false, true
		}
	}
	return score.Quarter, false, false
}

// applyMod rebuilds a duration from its parts.
func applyMod(base int64, dot, trip bool) int64 {
	switch {
	case dot:
		return score.Dotted(base)
	case trip:
		return score.Triplet(base)
	}
	return base
}

// stepDuration moves the current duration one note value shorter (+1) or
// longer (-1), keeping any dot or triplet on it.
func (e *Editor) stepDuration(delta int) {
	base, dot, trip := baseOf(e.doc.Duration())
	i := 0
	for j, b := range plainDurations {
		if b == base {
			i = j
		}
	}
	i += delta
	if i < 0 || i >= len(plainDurations) {
		return
	}
	e.setDuration(applyMod(plainDurations[i], dot, trip))
}

// modifyDuration toggles the dot or the triplet on the current duration.
// They are mutually exclusive: the format has no dotted triplet, and
// offering one that cannot be written is worse than not offering it.
func (e *Editor) modifyDuration(m durationMod) {
	base, dot, trip := baseOf(e.doc.Duration())
	switch m {
	case dotted:
		dot, trip = !dot, false
	case triplet:
		trip, dot = !trip, false
	}
	e.setDuration(applyMod(base, dot, trip))
}

// setDuration applies a chosen length to the beat under the cursor. A
// length that will not fit falls back to setting it for new beats only, so
// choosing "eighth note" before typing one always works even when the beat
// currently under the cursor cannot become an eighth.
func (e *Editor) setDuration(ticks int64) {
	if err := e.doc.SetDuration(ticks); err != nil {
		if e.report(e.doc.SetNewBeatDuration(ticks)) {
			e.msg = err.Error() + " — it will be used for the next beat instead"
			e.msgErr = true
			e.msgUntil = e.frame + edMsgFrames
		}
	}
}

// durationName spells a length the way a musician says it out loud. The
// fraction is what the FILE calls it; "quarter" is what a guitarist calls
// it, and the status line is read by the second of those.
func durationName(ticks int64) string {
	base, dot, trip := baseOf(ticks)
	name := map[int64]string{
		score.Whole: "whole", score.Half: "half", score.Quarter: "quarter",
		score.Eighth: "eighth", score.Sixteenth: "sixteenth", score.ThirtySec: "thirty-second",
	}[base]
	switch {
	case dot:
		return "dotted " + name
	case trip:
		return name + " triplet"
	}
	return name
}

// --- structural actions ----------------------------------------------

// addBarAfterCursor puts a bar in after the one the cursor is in, which at
// the end of the piece is the same thing as adding one.
func (e *Editor) addBarAfterCursor() {
	c := e.doc.Cursor()
	if c.Bar == e.doc.BarCount()-1 {
		e.report(e.doc.AppendBar())
		return
	}
	e.doc.MoveBar(1)
	if !e.report(e.doc.InsertBar()) {
		e.doc.MoveBar(-1)
	}
}

// stepTrack moves to the next or previous track.
func (e *Editor) stepTrack(back bool) {
	n := len(e.doc.Score().Tracks)
	if n < 2 {
		e.say("this piece has one track; the + chip adds another")
		return
	}
	i := e.doc.TrackIndex() + 1
	if back {
		i = e.doc.TrackIndex() - 1
	}
	e.doc.SelectTrack((i%n + n) % n)
}

// cycleTuning re-tunes the current track to the next preset in
// score.NamedTunings — the tunings a guitarist actually retunes to.
// Anything else is reachable by editing the \tuning line in the text
// view, which is the whole format rather than the part with a control.
func (e *Editor) cycleTuning() {
	cur := e.doc.Track().Tuning
	next := 0
	for i, t := range score.NamedTunings {
		if cur.Equal(t.Tuning) {
			next = (i + 1) % len(score.NamedTunings)
		}
	}
	if e.report(e.doc.SetTuning(score.NamedTunings[next].Tuning)) {
		e.say("tuning: " + score.NamedTunings[next].Name)
	}
}

// tuningName names the current tuning, or describes it when it is not one
// of the presets.
func (e *Editor) tuningName() string {
	return score.TuningName(e.doc.Track().Tuning)
}

func (e *Editor) undo() {
	if !e.doc.Undo() {
		e.say("nothing to undo")
	}
}

func (e *Editor) redo() {
	if !e.doc.Redo() {
		e.say("nothing to redo")
	}
}

// --- saving -----------------------------------------------------------

// save writes the piece to its file, asking for one the first time.
func (e *Editor) save() bool {
	if e.path == "" {
		e.saveAs()
		return false
	}
	return e.writeTo(e.path)
}

// saveAs asks for a file name and saves there.
func (e *Editor) saveAs() {
	if e.saveDialog == nil {
		e.report(fmt.Errorf("this build has no save dialog wired, so the piece cannot be written"))
		return
	}
	if e.dialogBusy {
		return
	}
	e.dialogBusy = true
	e.saveDialog(e.suggestedName())
}

// suggestedName is the file name the save dialog opens with: the piece's
// own title, made safe for a file system.
func (e *Editor) suggestedName() string {
	if e.path != "" {
		return e.path
	}
	name := strings.TrimSpace(e.doc.Score().Title)
	if name == "" {
		name = "untitled"
	}
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String() + ".gtab"
}

// writeTo saves and reports. It is the one place that clears the dirty
// flag, so nothing can claim a piece is saved without a file behind it.
func (e *Editor) writeTo(path string) bool {
	if err := textfmt.WriteFile(path, e.doc.Score()); err != nil {
		// The writer prefixes its errors with the package name, which is
		// for a log and not for somebody who has just pressed Save.
		e.report(fmt.Errorf("%s", strings.TrimPrefix(err.Error(), "gtab: ")))
		return false
	}
	e.path = path
	e.doc.MarkSaved()
	e.say("saved to " + filepath.Base(path))
	if e.onSaved != nil {
		e.onSaved(path)
	}
	return true
}

// drainDialog applies a save-dialog answer that arrived since the last
// frame. A cancel arrives as "" and only re-arms the guard.
func (e *Editor) drainDialog() {
	e.mailMu.Lock()
	got := e.mail
	e.mail = nil
	e.mailMu.Unlock()
	if got == nil {
		return
	}
	e.dialogBusy = false
	path := *got
	if path == "" {
		// A cancelled dialog also cancels whatever the save was FOR.
		e.practicePending = false
		return
	}
	if strings.ToLower(filepath.Ext(path)) != ".gtab" {
		path += ".gtab"
	}
	// The dialog floats while the editor stays interactive, so the text
	// view can have been opened — and edited — since the save was asked
	// for. Writing e.doc then would file a document the screen no longer
	// shows; the answer must go through the same apply-first discipline
	// as every other file action (see applyTextThen). Text that will not
	// parse keeps its view open with the complaint and the save waits for
	// another day, along with whatever it was for.
	if e.text != nil {
		if !e.applyText() {
			e.practicePending = false
			e.leaving = false
			// applyText reported the parse position, but the user just
			// answered a SAVE dialog and the standing pane status shows
			// the same complaint — the news is that nothing was written.
			e.report(fmt.Errorf("not saved — the text does not parse; the complaint is under the pane"))
			return
		}
		e.text = nil
	}
	saved := e.writeTo(path)
	// A save asked for on the way out finishes the leaving — and wins
	// over a practice promised earlier: both can be armed against one
	// dialog (shift+P asks, then Escape arms the leave), and the user's
	// last word was "leave", so popping the editor AND pushing practice
	// would navigate twice from one answer.
	if saved && e.leaving {
		e.leaving = false
		e.practicePending = false
		e.finishLeaving()
	}
	// And a save asked for by shift+P finishes with the practice it
	// promised — the user named the file to hear the piece, not to file it.
	if e.practicePending {
		e.practicePending = false
		if saved && e.practice != nil {
			e.practice(e.path)
		}
	}
}

// saveAndPractice saves the piece and opens it for practice, which is the
// only way to hear what has been written: the editor deliberately owns no
// audio of its own.
func (e *Editor) saveAndPractice() {
	if e.practice == nil {
		e.report(fmt.Errorf("this build cannot open a piece for practice from here"))
		return
	}
	if e.doc.Dirty() || e.path == "" {
		if !e.save() {
			// When the save is off asking for a file name, remember why it
			// was asked for; drainDialog finishes the job when the answer
			// arrives. Any other failure has already been reported and
			// promises nothing.
			e.practicePending = e.dialogBusy
			return
		}
	}
	e.practice(e.path)
}

// --- leaving ----------------------------------------------------------

// leave ends the editing session, asking first when there is unsaved work.
func (e *Editor) leave() error {
	// While the OS save dialog floats, it has the user's attention;
	// leaving underneath it would pop the editor its answer is addressed
	// to, and the answer would arrive to a mailbox nobody drains.
	if e.dialogBusy {
		return nil
	}
	if e.doc.Dirty() {
		e.leaving = true
		return nil
	}
	return errQuit
}

// finishLeaving pops the editor once the prompt is answered. It goes
// through the Shell rather than returning errQuit because by the time the
// prompt is answered Update has already returned for this frame.
func (e *Editor) finishLeaving() {
	if e.sh != nil {
		e.sh.Pop()
	}
}

// An edLeaveChoice is how the unsaved-changes prompt was answered.
type edLeaveChoice int

const (
	edLeaveNone edLeaveChoice = iota
	edLeaveSave
	edLeaveDiscard
	edLeaveCancel
)

// updateLeavePrompt runs the modal "you have unsaved changes" question.
//
// The buttons record a choice rather than acting, and one place acts on
// it. Written the other way — each button doing its own thing and the
// click result then being inspected — the discard button popped the screen
// itself AND left the caller looking at a prompt that was no longer up, so
// it popped a second time and took the start screen with it.
func (e *Editor) updateLeavePrompt() error {
	if e.dialogBusy {
		return nil // the save dialog is answering for us
	}
	choice := edLeaveNone
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyS):
		choice = edLeaveSave
	case inpututil.IsKeyJustPressed(ebiten.KeyD):
		choice = edLeaveDiscard
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape), inpututil.IsKeyJustPressed(ebiten.KeyC):
		choice = edLeaveCancel
	default:
		clicked := edLeaveNone
		e.ptr.hit(e.leavePromptSpots(&clicked))
		choice = clicked
	}
	return e.applyLeaveChoice(choice)
}

// applyLeaveChoice is the single place the prompt's answer takes effect.
func (e *Editor) applyLeaveChoice(c edLeaveChoice) error {
	switch c {
	case edLeaveSave:
		if e.save() {
			e.leaving = false
			return errQuit
		}
		// A save that had to ask for a name finishes in drainDialog.
		return nil
	case edLeaveDiscard:
		e.leaving = false
		return errQuit
	case edLeaveCancel:
		e.leaving = false
	}
	return nil
}

// leavePromptSpots are the prompt's three buttons; each records into out.
func (e *Editor) leavePromptSpots(out *edLeaveChoice) []hotspot {
	r := edPromptRect()
	bw, bh := 150.0, 34.0
	y := r.y + r.h - bh - 16
	return []hotspot{
		{r: rect{r.x + 16, y, bw, bh}, on: func() { *out = edLeaveSave }},
		{r: rect{r.x + 16 + bw + 12, y, bw, bh}, on: func() { *out = edLeaveDiscard }},
		{r: rect{r.x + r.w - bw - 16, y, bw, bh}, on: func() { *out = edLeaveCancel }},
	}
}

func edPromptRect() rect { return rect{screenW/2 - 280, screenH/2 - 90, 560, 180} }

func (e *Editor) drawLeavePrompt(screen *ebiten.Image) {
	vector.DrawFilledRect(screen, 0, 0, screenW, screenH, colHelpDim, false)
	r := edPromptRect()
	drawPanel(screen, r, colPanel, colSounding)
	title := "This piece has unsaved changes"
	drawTextScaled(screen, title, centreXScaled(title, r.x, r.w, 1.3), r.y+22, 1.3, colNote)
	sub := "Save it before leaving?"
	drawText(screen, sub, centreX(sub, r.x, r.w), r.y+62, colHUD)

	bw, bh := 150.0, 34.0
	y := r.y + r.h - bh - 16
	for i, b := range []struct {
		label string
		key   string
		r     rect
	}{
		{"Save", "S", rect{r.x + 16, y, bw, bh}},
		{"Discard", "D", rect{r.x + 16 + bw + 12, y, bw, bh}},
		{"Cancel", "esc", rect{r.x + r.w - bw - 16, y, bw, bh}},
	} {
		fill, edge := colPanel, colPanelEdge
		if i == 0 {
			fill, edge = colFocus, colInferred
		}
		if e.ptr.over(b.r) {
			edge = colNote
		}
		drawPanel(screen, b.r, fill, edge)
		drawText(screen, b.label, centreX(b.label, b.r.x, b.r.w), b.r.y+4, colNote)
		drawTextSmall(screen, b.key, b.r.x+(b.r.w-textWSmall(b.key))/2, b.r.y+20, colDim)
	}
}

// --- the control table ------------------------------------------------

// edPracticeWhat is the one sentence shift+P is described with, everywhere
// it appears. The toolbar tooltip used to promise "play it back", which
// reads as an in-editor preview the editor deliberately does not have;
// what the key actually does is save and open the piece for practice, and
// every description of it has to make the same promise.
const edPracticeWhat = "Save the piece and open it for practice"

func (e *Editor) editorBindings() []helpBinding {
	if e.doc.Track().Wind != nil {
		return e.windBindings()
	}
	return []helpBinding{
		{Group: "notes", Keys: fmt.Sprintf("0-%d", textfmt.MaxFret), Hint: "0-9 fret", Desc: "Type a fret onto the cursor's string; two digits within a moment make one number"},
		{Group: "notes", Keys: "del / R", Hint: "R rest", Desc: "Clear the note on this string, or make the whole beat a rest"},
		{Group: "notes", Keys: "`", Hint: "` tie", Desc: "Tie the note: hold it, do not strike it again"},
		{Group: "notes", Keys: "h / p / s", Hint: "h hammer-on", Desc: "Hammer-on, pull-off, slide"},
		{Group: "notes", Keys: "b / v / x", Desc: "Bend, vibrato, dead note (muted, no pitch)"},

		{Group: "rhythm", Keys: "[ / ]", Hint: "[ ] note length", Desc: "Longer / shorter note, for this beat and the ones you type next — or click one in the toolbar"},
		{Group: "rhythm", Keys: ". / T", Desc: "Dot the note value, or make it a triplet"},
		{Group: "rhythm", Keys: "space", Hint: "space next", Desc: "Move on to the next beat, adding a bar at the end of the piece — the rhythm for writing one"},
		{Group: "rhythm", Keys: "enter", Hint: "enter insert beat", Desc: "Put another beat in after this one"},
		{Group: "rhythm", Keys: "shift+del", Desc: "Remove the beat"},

		{Group: "bars", Keys: "N", Hint: "N add bar", Desc: "Add a bar after this one (at the end of the piece, that is a new last bar)"},
		{Group: "bars", Keys: "shift+N", Desc: "Delete this bar, from every track"},
		{Group: "bars", Keys: "shift+M / shift+B", Desc: "Set the time signature or the tempo from this bar on"},

		{Group: "moving", Keys: "arrows", Hint: "arrows move", Desc: "Left and right by beat, up and down by string"},
		{Group: "moving", Keys: "page up/down · home/end", Desc: "A bar at a time, and the two ends of the piece"},
		{Group: "moving", Keys: "click / wheel", Desc: "Put the cursor on a beat and a string; the wheel scrolls the notation"},

		{Group: "piece", Keys: "shift+T", Desc: "Name the piece"},
		{Group: "piece", Keys: "shift+U", Desc: "Cycle the tuning: standard, drop D, half and full step down, DADGAD, open G"},
		{Group: "piece", Keys: "tab", Hint: "tab track", Desc: "Move to the next track (shift+tab for the previous one)"},
		{Group: "piece", Keys: "shift+A", Desc: "Add another track"},
		{Group: "piece", Keys: "F2", Hint: "F2 file text", Desc: "Show the piece as the text file it saves to — where an unusual tuning, the instrument voice or a comment can be set"},

		{Group: "session", Keys: "ctrl+Z / ctrl+Y", Hint: "ctrl+Z undo", Desc: "Undo and redo"},
		{Group: "session", Keys: "ctrl+S", Hint: "ctrl+S save", Desc: "Save (ctrl+shift+S saves under a new name)"},
		{Group: "session", Keys: "shift+P", Hint: "shift+P practice", Off: e.practice == nil,
			Desc: edPracticeWhat},
		{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
		{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Leave the editor"},
	}
}

func (e *Editor) hintLine() string {
	if e.text != nil {
		return hintLineOf(e.textBindings())
	}
	return hintLineOf(e.editorBindings())
}
