package ui

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

const (
	edCaptionY = uiBodyTop
	edToolbarY = edCaptionY + iconCapH + 2
	edTrackY   = edToolbarY + iconBtnSize + 12
	edGridTop  = edTrackY + iconBtnSize + 18

	edLabelW = 26.0
	edGridX  = uiPadX + edLabelW
	edGridW  = screenW - 2*uiPadX - edLabelW

	edStringGap = 22.0

	edSysPadTop = 30.0

	edSysPadBottom = 40.0
	edSysGap       = 10.0

	edPxPerQuarter = 56.0

	edMinBeatW = 26.0
	edBarInset = 7.0

	edMaxStrings     = 16
	edFretHoldFrames = 40

	edMsgFrames = 300
)

var (
	colEdCursor = color.RGBA{60, 110, 170, 255}
	colEdCaret  = color.RGBA{120, 190, 255, 255}
	colEdRest   = color.RGBA{110, 110, 126, 255}

	colTieArc = color.RGBA{150, 170, 200, 255}
)

type Editor struct {
	sh  *Shell
	doc *edit.Doc

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

	fretDigits string
	fretUntil  int64

	entry *edEntry

	text *gtabPane

	leaving bool

	picker *edPicker

	practicePending bool

	saveDialog func(suggest string)
	libraryDir string

	leaveAfterSave bool
	dialogBusy     bool
	mailMu         sync.Mutex
	mail           *saveAnswer

	onSaved func(path string)

	practice   func(path string)
	auditionFn func(program, key int)
}

func NewEditor(sh *Shell) *Editor {
	return &Editor{sh: sh, doc: edit.New(edit.NewOptions{})}
}

func NewEditorChoosing(sh *Shell) *Editor {
	e := NewEditor(sh)
	e.openInstrumentPicker(pickNewPiece)
	return e
}

func NewEditorFor(sh *Shell, sc *score.Score, path string) (*Editor, error) {
	doc, err := edit.Open(sc)
	if err != nil {
		return nil, err
	}

	if strings.ToLower(filepath.Ext(path)) != ".gtab" {
		path = ""
	}
	return &Editor{sh: sh, doc: doc, path: path}, nil
}

func NewEditorForText(sh *Shell, src []byte, path string) *Editor {
	e := NewEditor(sh)
	if strings.ToLower(filepath.Ext(path)) == ".gtab" {
		e.path = path
	}
	e.text = newGtabPaneFromSource(src)
	e.text.fromFile = true
	return e
}

func (e *Editor) SetSaveDialog(fn func(suggest string)) { e.saveDialog = fn }

func (e *Editor) SetLibraryDir(dir string) { e.libraryDir = dir }

func (e *Editor) SetOnSaved(fn func(path string)) { e.onSaved = fn }

func (e *Editor) SetPractice(fn func(path string)) { e.practice = fn }

func (e *Editor) SetAudition(fn func(program, key int)) { e.auditionFn = fn }

func (e *Editor) auditionNote() {
	if e.auditionFn == nil {
		return
	}
	tr := e.doc.Track()
	if n, ok := e.doc.NoteAt(e.doc.Cursor().Str); ok && n.Tech&score.TechDead == 0 {
		e.auditionFn(tr.Program, tr.Pitch(n))
	}
}

type saveAnswer struct{ path, problem string }

func (e *Editor) OfferSavePath(path string) {
	e.mailMu.Lock()
	e.mail = &saveAnswer{path: path}
	e.mailMu.Unlock()
}

func (e *Editor) OfferSaveProblem(msg string) {
	e.mailMu.Lock()
	e.mail = &saveAnswer{problem: msg}
	e.mailMu.Unlock()
}

func (e *Editor) Doc() *edit.Doc { return e.doc }

func (e *Editor) SetHelpOpen(open bool) { e.helpOpen = open }

func (e *Editor) ShowTextView(on bool) bool {
	if on == (e.text != nil) {
		return true
	}
	e.toggleText()
	return on == (e.text != nil)
}

func (e *Editor) Path() string { return e.path }

func (e *Editor) say(msg string) { e.msg, e.msgErr, e.msgUntil = msg, false, e.frame+edMsgFrames }

func (e *Editor) report(err error) bool {
	if err == nil {
		return true
	}
	e.msg, e.msgErr, e.msgUntil = err.Error(), true, e.frame+edMsgFrames
	return false
}

func (e *Editor) ShowError(msg string) {
	e.msg, e.msgErr, e.msgUntil = msg, true, e.frame+edMsgFrames
}

func (e *Editor) message() (string, bool) {
	if e.msg == "" || e.frame >= e.msgUntil {
		return "", false
	}
	return e.msg, e.msgErr
}

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
	return e.updateGrid()
}

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

func (e *Editor) helpTable() (title string, rows []helpBinding) {
	if e.text != nil {
		return "TEXT VIEW KEYS", e.textBindings()
	}
	return "EDITOR KEYS", e.editorBindings()
}

func (e *Editor) headerTitle() string {
	name := e.doc.Score().Title
	if name == "" {
		name = "Untitled piece"
	}
	if e.doc.Dirty() {
		name += " •"
	}

	const gap = 32.0
	budget := screenW - 2*uiPadX - textW(e.statusLine()) - gap
	return truncateWScaled(name, budget, uiTitleScl)
}

func (e *Editor) statusLine() string {
	c := e.doc.Cursor()
	bar := e.doc.Bar()
	where := fmt.Sprintf("bar %d/%d   %s", c.Bar+1, e.doc.BarCount(), e.cursorPitch())

	what := fmt.Sprintf("%d/%d   %.0f BPM   note: %s", bar.Num, bar.Den, e.doc.TempoAtCursor(), durationName(e.doc.Duration()))
	file := "unsaved"
	if e.path != "" {
		file = filepath.Base(e.path)
	}
	return where + "     " + what + "     " + file
}

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

func edStringName(tr *score.Track, str int) string {
	if str < 1 || str > len(tr.Tuning) {
		return "?"
	}
	return edPitchClass(tr.Tuning[str-1])
}

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

func (e *Editor) modalUp() bool {
	return e.entry != nil || e.leaving || e.helpOpen || e.picker != nil
}

func (e *Editor) updateGrid() error {
	if s, rem := wheelSteps(e.ptr.wheel); s != 0 {
		e.scroll -= float64(s) * edStringGap * 2
		e.ptr.wheel = rem
	}
	if err := e.handleKeys(); err != nil {
		return err
	}

	if e.entry != nil || e.text != nil || e.leaving || e.helpOpen || e.picker != nil {
		return nil
	}
	e.handleMouse()
	return nil
}

type edBarBox struct {
	index int
	x, w  float64
}

type edSystem struct {
	bars []edBarBox
}

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

func edBeatX(box edBarBox, bar *score.Bar, bt *score.Beat) float64 {
	span := box.w - 2*edBarInset
	if bar.Len() <= 0 {
		return box.x + edBarInset
	}
	return box.x + edBarInset + float64(bt.Start-bar.Start)/float64(bar.Len())*span
}

func (e *Editor) systemHeight() float64 {
	n := e.gridLines()
	return edSysPadTop + float64(n-1)*edStringGap + edSysPadBottom + edSysGap
}

func gridBottom() float64 { return uiFooterY - 28 }

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

func (e *Editor) clampScroll(systems []edSystem) {
	h := e.systemHeight()
	view := gridBottom() - edGridTop
	total := float64(len(systems)) * h
	max := total - view
	if max < 0 {
		max = 0
	}

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

		w := 0.0
		for _, b := range sys.bars {
			w = b.x + b.w
		}
		if tr.Wind != nil {

			drawWindSystemLines(screen, edWindLadderFor(tr.Wind), strTop, w)
		} else {
			for s := 0; s < nStr; s++ {
				y := float32(strTop + float64(s)*edStringGap)
				vector.StrokeLine(screen, edGridX, y, float32(edGridX+w), y, 1, colString, false)

				name := edStringName(tr, s+1)
				drawTextSmall(screen, name, edGridX-10-textWSmall(name), float64(y)-7, colDim)
			}
		}
		rhythmY := strTop + float64(nStr-1)*edStringGap + 12
		for _, box := range sys.bars {
			e.drawBar(screen, tr, box, strTop, nStr, cur)
			e.drawRhythm(screen, tr, box, rhythmY)
		}

		x := float32(edGridX + w)
		vector.StrokeLine(screen, x, float32(strTop-6), x, float32(strTop+float64(nStr-1)*edStringGap+6), 1, colBarline, false)
	}
}

func (e *Editor) drawBar(screen *ebiten.Image, tr *score.Track, box edBarBox, strTop float64, nStr int, cur edit.Cursor) {
	bar := tr.Bars[box.index]

	var lastX [edMaxStrings]float64
	var lastBeat [edMaxStrings]int
	for i := range lastX {
		lastX[i] = -1
		lastBeat[i] = -2
	}
	x := float32(edGridX + box.x)
	vector.StrokeLine(screen, x, float32(strTop-6), x, float32(strTop+float64(nStr-1)*edStringGap+6), 1, colBarline, false)
	drawTextSmall(screen, strconv.Itoa(box.index+1), float64(x)+4, strTop-26, colHint)

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

	type pendingMark struct {
		s    string
		x, y float64
	}
	var marks []pendingMark

	var ladder *edWindLadder
	if tr.Wind != nil {
		l := edWindLadderFor(tr.Wind)
		ladder = &l
	}

	noteY := func(n score.Note) float64 {
		if ladder != nil {
			return ladder.y(tr.Wind.Written(tr.Pitch(n)), strTop)
		}
		return strTop + float64(n.String-1)*edStringGap
	}

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

			vector.StrokeLine(screen, float32(bx-4), float32(strTop-8),
				float32(bx-4), float32(strTop+float64(nStr-1)*edStringGap+8), 1, colEdCaret, false)
			y := cellY()
			if len(bt.Notes) > 0 {
				if ladder != nil {
					y = noteY(bt.Notes[0])
				} else {
					y = strTop + float64(cur.Str-1)*edStringGap
				}
			}
			drawCursorCellAt(screen, bx, y)
		}

		bg := func(str int) color.RGBA {
			if onCursor && str == cur.Str {
				return colEdCursor
			}
			return colNoteBG
		}

		if len(bt.Notes) == 0 {

			ry := strTop + float64(nStr-1)/2*edStringGap
			vector.DrawFilledRect(screen, float32(bx-3), float32(ry-10), 18, 20, colBG, false)
			drawGlyph(screen, edRestGlyph(bt.Dur), rect{bx - 2, ry - 9, 16, 18}, colEdRest)

			if onCursor {
				drawCursorCellAt(screen, bx, cellY())
			}
			continue
		}
		for _, n := range bt.Notes {
			ny := noteY(n)
			label := strconv.Itoa(n.Fret)
			if ladder != nil {

				label = edPitchName(tr.Wind.Written(tr.Pitch(n)))
			} else if n.Tech&score.TechDead != 0 {
				label = "x"
			}

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

func (e *Editor) drawRhythm(screen *ebiten.Image, tr *score.Track, box edBarBox, y float64) {
	bar := tr.Bars[box.index]
	for _, bt := range bar.Beats {
		x := float32(edGridX + edBeatX(box, bar, bt) + 3)
		base, dot, trip := baseOf(bt.Dur)
		col := colDim
		if len(bt.Notes) == 0 {
			col = colBarline
		}

		if base == score.Whole {

			vector.StrokeLine(screen, x-3, float32(y+8), x+3, float32(y+8), 1, col, false)
		} else {
			h := float32(11)
			if base == score.Half {
				h = 7
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

			drawTextSmall(screen, "3", float64(x)-2, y+12, col)
		}
	}
}

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

func (e *Editor) drawFirstSteps(screen *ebiten.Image) {
	if !e.isEmpty() {
		return
	}

	systems := e.layoutSystems()
	y := edGridTop + float64(len(systems))*e.systemHeight() - e.scroll + 14
	if min := edGridTop + e.systemHeight(); y < min {
		y = min
	}
	if y+120 > gridBottom() {
		return
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

type firstStep struct{ key, what string }

func firstStepsContent() ([]firstStep, string) {
	lines := []firstStep{
		{fmt.Sprintf("0-%d", textfmt.MaxFret), "type a fret number onto the highlighted string"},
		{"↑ ↓", "choose the string; [ and ] choose the note value"},
		{"space", "move on to the next beat — and past the last bar, it makes another"},
	}
	tail := "ctrl+S saves it into your library · shift+P opens it for practice · ? lists every key"
	return lines, tail
}

func (e *Editor) barMarking(index int, bar *score.Bar) (meter, tempo string) {
	sc := e.doc.Score()
	if index == 0 {
		meter = fmt.Sprintf("%d/%d", bar.Num, bar.Den)
	} else if prev := e.doc.Track().Bars[index-1]; prev.Num != bar.Num || prev.Den != bar.Den {
		meter = fmt.Sprintf("%d/%d", bar.Num, bar.Den)
	}
	for _, t := range sc.Tempos {
		if t.Tick == bar.Start && (index == 0 || t.Tick != 0) {
			tempo = fmt.Sprintf("= %.0f", 60e6/float64(t.USPerQuarter))
		}
	}
	return meter, tempo
}

func drawCursorCellAt(dst *ebiten.Image, bx, cy float64) {
	vector.DrawFilledRect(dst, float32(bx-5), float32(cy-10), 28, 20, colEdCursor, false)
}

func edRestGlyph(dur int64) glyphID {
	switch base, _, _ := baseOf(dur); base {
	case score.Whole:
		return glyphRestWhole
	case score.Half:
		return glyphRestHalf
	}
	return glyphRest
}

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

func (e *Editor) handleMouse() {
	if e.ptr.hit(e.hotspots()) {
		return
	}
	if e.ptr.pressed {
		e.clickGrid(e.ptr.x, e.ptr.y)
	}
}

func (e *Editor) clickGrid(px, py float64) bool {
	if py < edGridTop || py > gridBottom() || px < edGridX || px > edGridX+edGridW {
		return false
	}
	systems := e.layoutSystems()

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

	x := px - edGridX
	box := sys.bars[len(sys.bars)-1]
	for _, b := range sys.bars {
		if x < b.x+b.w {
			box = b
			break
		}
	}
	bar := e.doc.Track().Bars[box.index]

	beat := 0
	for bi, bt := range bar.Beats {
		if x >= edBeatX(box, bar, bt)-4 {
			beat = bi
		}
	}
	e.doc.GoTo(edit.Cursor{Bar: box.index, Beat: beat, Str: str})
	return true
}

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

	ebiten.KeyC, ebiten.KeyD, ebiten.KeyE, ebiten.KeyF, ebiten.KeyG, ebiten.KeyL,
	ebiten.KeyBracketLeft, ebiten.KeyBracketRight, ebiten.KeyPeriod, ebiten.KeyBackquote,
	ebiten.KeyTab, ebiten.KeyF1, ebiten.KeyF2, ebiten.KeySlash, ebiten.KeyEscape,
}

var editorRepeatKeys = map[ebiten.Key]bool{
	ebiten.KeyLeft: true, ebiten.KeyRight: true, ebiten.KeyUp: true, ebiten.KeyDown: true,
	ebiten.KeyPageUp: true, ebiten.KeyPageDown: true,
	ebiten.KeyDelete: true, ebiten.KeyBackspace: true,
	ebiten.KeySpace: true,
}

func editorKeyFires(repeat bool, d int) bool {
	if d == 1 {
		return true
	}
	if !repeat {
		return false
	}
	return d > 30 && (d-30)%4 == 0
}

type mods struct{ shift, ctrl bool }

func readMods() mods {
	return mods{
		shift: ebiten.IsKeyPressed(ebiten.KeyShiftLeft) || ebiten.IsKeyPressed(ebiten.KeyShiftRight),
		ctrl: ebiten.IsKeyPressed(ebiten.KeyControlLeft) || ebiten.IsKeyPressed(ebiten.KeyControlRight) ||
			ebiten.IsKeyPressed(ebiten.KeyMetaLeft) || ebiten.IsKeyPressed(ebiten.KeyMetaRight),
	}
}

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

			if e.report(e.doc.NudgePitch(nudgeStep(m))) {
				e.auditionNote()
			}
		} else {
			e.doc.MoveString(-1)
		}
	case ebiten.KeyDown:
		if wind {
			if e.report(e.doc.NudgePitch(-nudgeStep(m))) {
				e.auditionNote()
			}
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
			e.report(fmt.Errorf("no hammer-ons on %s — l marks a slur", score.An(e.doc.Track().Wind.Name)))
		} else {
			e.report(e.doc.ToggleTech(score.TechHammer))
		}
	case ebiten.KeyP:
		if m.shift {
			e.saveAndPractice()
		} else if wind {
			e.report(fmt.Errorf("no pull-offs on %s — l marks a slur", score.An(e.doc.Track().Wind.Name)))
		} else {
			e.report(e.doc.ToggleTech(score.TechPull))
		}
	case ebiten.KeyS:
		e.report(e.doc.ToggleTech(score.TechSlide))
	case ebiten.KeyB:
		if m.shift {
			e.openEntry(edEntryTempo)
		} else if wind {

			e.typeNoteLetter(edNoteClasses[k])
		} else {
			e.report(e.doc.ToggleTech(score.TechBend))
		}
	case ebiten.KeyV:
		e.report(e.doc.ToggleTech(score.TechVibrato))
	case ebiten.KeyX:
		if wind {
			e.report(fmt.Errorf("no dead notes on %s — R makes the beat a rest", score.An(e.doc.Track().Wind.Name)))
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
				e.report(fmt.Errorf("%s has no tuning to cycle", score.An(e.doc.Track().Wind.Name)))
			} else {
				e.cycleTuning()
			}
		}
	case ebiten.KeyA:
		if m.shift {

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

func nudgeStep(m mods) int {
	if m.shift {
		return 12
	}
	return 1
}

func digitOf(k ebiten.Key) (int, bool) {
	switch {
	case k >= ebiten.KeyDigit0 && k <= ebiten.KeyDigit9:
		return int(k - ebiten.KeyDigit0), true
	case k >= ebiten.KeyNumpad0 && k <= ebiten.KeyNumpad9:
		return int(k - ebiten.KeyNumpad0), true
	}
	return 0, false
}

func (e *Editor) typeFret(digit int) {
	if e.frame >= e.fretUntil {
		e.fretDigits = ""
	}
	next := e.fretDigits + strconv.Itoa(digit)
	fret, err := strconv.Atoi(next)
	if err != nil || fret > textfmt.MaxFret {

		next = strconv.Itoa(digit)
		fret = digit
	}
	if !e.report(e.doc.SetFret(fret)) {
		e.fretDigits, e.fretUntil = "", 0
		return
	}
	e.fretDigits, e.fretUntil = next, e.frame+edFretHoldFrames
	e.auditionNote()
}

func (e *Editor) commitFretDigits() { e.fretDigits, e.fretUntil = "", 0 }

func (e *Editor) expireFretDigits() {
	if e.frame >= e.fretUntil {
		e.fretDigits = ""
	}
}

func (e *Editor) advance() {
	c := e.doc.Cursor()
	atEnd := c.Bar == e.doc.BarCount()-1 && c.Beat == len(e.doc.Bar().Beats)-1
	if !atEnd {
		e.doc.MoveBeat(1)
		return
	}

	e.report(e.doc.AppendBar())
}

type durationMod int

const (
	dotted durationMod = iota
	triplet
)

var plainDurations = []int64{
	score.Whole, score.Half, score.Quarter, score.Eighth, score.Sixteenth, score.ThirtySec,
}

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

func applyMod(base int64, dot, trip bool) int64 {
	switch {
	case dot:
		return score.Dotted(base)
	case trip:
		return score.Triplet(base)
	}
	return base
}

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

func (e *Editor) setDuration(ticks int64) {
	if err := e.doc.SetDuration(ticks); err != nil {
		if e.report(e.doc.SetNewBeatDuration(ticks)) {
			e.msg = err.Error() + " — it will be used for the next beat instead"
			e.msgErr = true
			e.msgUntil = e.frame + edMsgFrames
		}
	}
}

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

func (e *Editor) save() bool {
	if e.path == "" {
		if e.libraryDir != "" {
			e.openSaveEntry()
		} else {
			e.saveAs()
		}
		return false
	}
	return e.writeTo(e.path)
}

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

func (e *Editor) saveEntryOpen() bool { return e.entry != nil && e.entry.kind == edEntrySaveName }

func (e *Editor) afterSave() {
	if e.leaveAfterSave {
		e.leaveAfterSave = false
		e.practicePending = false
		e.finishLeaving()
		return
	}
	if e.practicePending {
		e.practicePending = false
		if e.practice != nil {
			e.practice(e.path)
		}
	}
}

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

func (e *Editor) writeTo(path string) bool {
	if err := textfmt.WriteFile(path, e.doc.Score()); err != nil {

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

func (e *Editor) drainDialog() {
	e.mailMu.Lock()
	got := e.mail
	e.mail = nil
	e.mailMu.Unlock()
	if got == nil {
		return
	}
	e.dialogBusy = false
	if got.problem != "" {
		e.practicePending = false
		e.leaving = false
		e.ShowError(got.problem)
		return
	}
	path := got.path
	if path == "" {

		e.practicePending = false
		return
	}
	if strings.ToLower(filepath.Ext(path)) != ".gtab" {
		path += ".gtab"
	}

	if e.text != nil {
		if !e.applyText() {
			e.practicePending = false
			e.leaving = false

			e.report(fmt.Errorf("not saved — the text does not parse; the complaint is under the pane"))
			return
		}
		e.text = nil
	}
	saved := e.writeTo(path)

	if saved && e.leaving {
		e.leaving = false
		e.practicePending = false
		e.finishLeaving()
	}

	if e.practicePending {
		e.practicePending = false
		if saved && e.practice != nil {
			e.practice(e.path)
		}
	}
}

func (e *Editor) saveAndPractice() {
	if e.practice == nil {
		e.report(fmt.Errorf("this build cannot open a piece for practice from here"))
		return
	}
	if e.doc.Dirty() || e.path == "" {
		if !e.save() {

			e.practicePending = e.dialogBusy || e.saveEntryOpen()
			return
		}
	}
	e.practice(e.path)
}

func (e *Editor) leave() error {

	if e.dialogBusy {
		return nil
	}
	if e.doc.Dirty() {
		e.leaving = true
		return nil
	}
	return errQuit
}

func (e *Editor) finishLeaving() {
	if e.sh != nil {
		e.sh.Pop()
	}
}

type edLeaveChoice int

const (
	edLeaveNone edLeaveChoice = iota
	edLeaveSave
	edLeaveDiscard
	edLeaveCancel
)

func (e *Editor) updateLeavePrompt() error {
	if e.dialogBusy {
		return nil
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

func (e *Editor) applyLeaveChoice(c edLeaveChoice) error {
	switch c {
	case edLeaveSave:
		if e.save() {
			e.leaving = false
			return errQuit
		}

		return nil
	case edLeaveDiscard:
		e.leaving = false
		return errQuit
	case edLeaveCancel:
		e.leaving = false
	}
	return nil
}

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
