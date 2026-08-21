package ui

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/score"
)

const (
	screenW = 1280
	screenH = 720

	playheadX = 0.3
	stringGap = 26

	tabTop     = 274
	defaultPxQ = 96
)

const (
	engineMinTempoScale = 0.25
	engineMaxTempoScale = 2.0
)

const bpmEntryMaxDigits = 3

const bpmMsgFrames = 300

const defaultCountInBeats = 4

var (
	colBG       = color.RGBA{18, 18, 24, 255}
	colString   = color.RGBA{90, 90, 100, 255}
	colBarline  = color.RGBA{60, 60, 72, 255}
	colNote     = color.RGBA{235, 235, 235, 255}
	colNoteBG   = color.RGBA{18, 18, 24, 255}
	colSounding = color.RGBA{255, 200, 60, 255}
	colInferred = color.RGBA{120, 200, 255, 255}
	colPlayhead = color.RGBA{230, 70, 70, 255}

	colLoop     = color.RGBA{21, 56, 32, 90}
	colLoopEdge = color.RGBA{60, 160, 90, 255}
	colHUD      = color.RGBA{200, 200, 210, 255}
	colCountIn  = color.RGBA{255, 200, 60, 255}

	colHit      = color.RGBA{80, 210, 120, 255}
	colClose    = color.RGBA{240, 160, 50, 255}
	colMiss     = color.RGBA{235, 80, 80, 255}
	colWaitLo   = color.RGBA{180, 140, 40, 255}
	colWaitHi   = color.RGBA{255, 245, 180, 255}
	colTuneZone = color.RGBA{40, 90, 55, 255}

	colHelpDim = color.RGBA{4, 4, 6, 200}
	colWarnBG  = color.RGBA{58, 18, 18, 240}
)

type App struct {
	eng   *engine.Engine
	sc    *score.Score
	track int

	zoom float64

	metronome bool
	ramp      bool

	bpmEntry    bool
	bpmDigits   string
	bpmMsg      string
	bpmMsgUntil int64

	userMuted []bool
	solo      int

	countInBeats  int
	countInOn     bool
	engineCountIn int
	countInStale  bool
	countInApply  func(beats int) bool

	helpOpen bool

	quitAll func()

	settings func()

	reload func()

	anim animator

	outLatency   func() time.Duration
	latency      float64
	latencyArmed bool

	settingsTouched bool

	ptr      pointer
	drag     dragTarget
	wheelAcc float64

	loopDrawTick       int64
	loopDrawX          float64
	loopDrawing        bool
	loopDrawOnTimeline bool
	loopDrawPxPerTick  float64

	ph playhead

	liveUI
}

func New(eng *engine.Engine, sc *score.Score, track int) *App {
	eng.SetWaitTrack(track)
	return &App{eng: eng, sc: sc, track: track, zoom: 1, countInBeats: defaultCountInBeats}
}

func (a *App) Run() error {
	ebiten.SetWindowSize(screenW, screenH)
	title := a.sc.Title
	if title == "" {
		title = "musicTutor"
	} else {
		title = "musicTutor — " + title
	}
	ebiten.SetWindowTitle(title)
	err := ebiten.RunGame(a)
	if err == errQuit {
		return nil
	}
	return err
}

var errQuit = fmt.Errorf("quit")

func (a *App) pxPerTick() float64 {
	return a.zoom * defaultPxQ / float64(score.PPQ)
}

func (a *App) Track() int { return a.track }

func (a *App) displayed() *score.Track { return a.sc.Tracks[a.track] }

func (a *App) barAt(tick int64) int {
	bars := a.displayed().Bars
	for i, b := range bars {
		if tick < b.Start+b.Len() {
			return i
		}
	}
	return len(bars) - 1
}

func (a *App) shiftHeld() bool {
	return ebiten.IsKeyPressed(ebiten.KeyShiftLeft) || ebiten.IsKeyPressed(ebiten.KeyShiftRight)
}

func (a *App) SetQuitAll(fn func()) { a.quitAll = fn }

func (a *App) Update() error {
	a.frame++
	a.syncLive()

	a.stepPlayhead()
	a.ptr = readPointer()

	if a.bpmEntry || a.helpOpen {

		a.drag = dragNone
	}
	if a.bpmEntry {
		a.updateBPMEntry()
		return nil
	}
	if a.helpOpen {
		a.updateHelp()
		return nil
	}
	if a.warningVisible() && inpututil.IsKeyJustPressed(ebiten.KeyD) {
		a.dismissWarning()
		return nil
	}
	if err := a.handleKeys(); err != nil {
		return err
	}
	a.handleMouse(a.ptr, a.shiftHeld())
	return nil
}

func (a *App) handleKeys() error {
	eng := a.eng
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):

		return errQuit
	case inpututil.IsKeyJustPressed(ebiten.KeyQ):
		return a.quitApp()
	case helpKeyPressed():
		a.openHelp()
	case inpututil.IsKeyJustPressed(ebiten.KeySpace):
		a.togglePlay()
	case inpututil.IsKeyJustPressed(ebiten.KeyHome):
		a.seekTo(0)
	case inpututil.IsKeyJustPressed(ebiten.KeyEnd):
		a.seekLastBar()
	case inpututil.IsKeyJustPressed(ebiten.KeyLeft):
		a.seekPrevBar()
	case inpututil.IsKeyJustPressed(ebiten.KeyRight):
		a.seekNextBar()
	case inpututil.IsKeyJustPressed(ebiten.KeyUp):
		eng.SetTempoScale(eng.TempoScale() + 0.05)
	case inpututil.IsKeyJustPressed(ebiten.KeyDown):
		eng.SetTempoScale(eng.TempoScale() - 0.05)
	case inpututil.IsKeyJustPressed(ebiten.KeyA):
		a.loopSetA()
	case inpututil.IsKeyJustPressed(ebiten.KeyB):

		if a.shiftHeld() {
			a.openBPMEntry()
		} else {
			a.loopSetB()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyC):
		a.toggleCountIn()
	case inpututil.IsKeyJustPressed(ebiten.KeyL):
		if _, _, on := eng.Loop(); on {
			eng.ClearLoop()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyM):
		a.toggleMetronome()
	case inpututil.IsKeyJustPressed(ebiten.KeyR):
		a.toggleRamp()
	case inpututil.IsKeyJustPressed(ebiten.KeyS):
		a.openSettings()
	case inpututil.IsKeyJustPressed(ebiten.KeyF5):
		a.reloadPiece()
	case inpututil.IsKeyJustPressed(ebiten.KeyT):
		a.tunerView = !a.tunerView
	case inpututil.IsKeyJustPressed(ebiten.KeyW):
		if a.waitCtl {
			a.toggleWait()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyEqual), inpututil.IsKeyJustPressed(ebiten.KeyKPAdd):
		a.zoomIn()
	case inpututil.IsKeyJustPressed(ebiten.KeyMinus), inpututil.IsKeyJustPressed(ebiten.KeyKPSubtract):
		a.zoomOut()
	}
	for i := 0; i < len(a.sc.Tracks) && i < 9; i++ {
		if inpututil.IsKeyJustPressed(ebiten.Key1 + ebiten.Key(i)) {
			if a.shiftHeld() {
				a.toggleSolo(i)
			} else {
				a.toggleMute(i)
			}
		}
	}
	return nil
}

func (a *App) quitApp() error {
	if a.quitAll != nil {
		a.quitAll()
		return nil
	}
	return errQuit
}

func (a *App) SetSettingsOpener(fn func()) { a.settings = fn }

func (a *App) SetReloader(fn func()) { a.reload = fn }

func (a *App) MarkSettingsChanged() { a.settingsTouched = a.reload != nil }

func (a *App) openSettings() {
	if a.settings != nil {
		a.settings()
	}
}

func (a *App) reloadPiece() {
	if a.reload == nil {
		return
	}
	a.reload()
}

func (a *App) reloadPrompt() string {
	if !a.settingsTouched && !a.countInStale {
		return ""
	}
	return "settings changed: press F5 to re-open this piece with them"
}

func (a *App) SetTunerView(on bool) { a.tunerView = on }

func (a *App) SetHelpOpen(on bool) { a.helpOpen = on }

func (a *App) openHelp() { a.helpOpen = true }

func (a *App) closeHelp() { a.helpOpen = false }

func (a *App) updateHelp() {
	if helpDismissed(a.ptr) {
		a.closeHelp()
	}
}

func (a *App) loopSetA() {
	i := a.barAt(a.posTick())
	if i < 0 {
		return
	}
	b := a.displayed().Bars[i]
	_, end, on := a.eng.Loop()
	if !on || end <= b.Start {
		end = b.Start + b.Len()
	}
	a.eng.SetLoop(b.Start, end)
}

func (a *App) loopSetB() {
	i := a.barAt(a.posTick())
	if i < 0 {
		return
	}
	b := a.displayed().Bars[i]
	start, _, on := a.eng.Loop()
	if !on || start >= b.Start+b.Len() {
		start = b.Start
	}
	a.eng.SetLoop(start, b.Start+b.Len())
}

func (a *App) toggleMetronome() {
	a.metronome = !a.metronome
	a.eng.SetMetronome(a.metronome)
}

func (a *App) toggleRamp() {
	a.ramp = !a.ramp
	a.eng.SetRamp(engine.RampConfig{Enabled: a.ramp, Increment: 0.05, Target: 1.0})
	if !a.ramp {
		return
	}

	if _, _, on := a.eng.Loop(); !on {
		a.setBPMMessage("ramp raises the speed after each loop pass — set a loop first")
	} else if a.eng.TempoScale() >= 1.0 {
		a.setBPMMessage("already at full speed — slow down first and ramp brings you back up")
	}
}

func (a *App) SetInitialMetronome(on bool) { a.metronome = on }

func (a *App) openBPMEntry() {
	a.bpmEntry, a.bpmDigits = true, ""
	a.bpmMsg, a.bpmMsgUntil = "", 0
}

func (a *App) updateBPMEntry() {
	for d := 0; d <= 9; d++ {
		if inpututil.IsKeyJustPressed(ebiten.Key0+ebiten.Key(d)) ||
			inpututil.IsKeyJustPressed(ebiten.KeyKP0+ebiten.Key(d)) {
			a.bpmDigit(byte('0' + d))
		}
	}
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyBackspace):
		a.bpmBackspace()
	case inpututil.IsKeyJustPressed(ebiten.KeyEnter), inpututil.IsKeyJustPressed(ebiten.KeyKPEnter):
		a.commitBPMEntry()
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		a.cancelBPMEntry()
	case a.ptr.pressed && !a.ptr.over(bpmEntryRect()):

		a.cancelBPMEntry()
	}
}

func bpmEntryRect() rect {
	const w, h = 300, 84
	return rect{(screenW - w) / 2, tabTop + 20, w, h}
}

func (a *App) bpmDigit(d byte) {
	if !a.bpmEntry || d < '0' || d > '9' {
		return
	}
	if a.bpmDigits == "0" {
		a.bpmDigits = ""
	}
	if len(a.bpmDigits) >= bpmEntryMaxDigits {
		return
	}
	a.bpmDigits += string(d)
}

func (a *App) bpmBackspace() {
	if n := len(a.bpmDigits); n > 0 {
		a.bpmDigits = a.bpmDigits[:n-1]
	}
}

func (a *App) cancelBPMEntry() {
	a.bpmEntry, a.bpmDigits = false, ""
}

func (a *App) commitBPMEntry() {
	digits := a.bpmDigits
	a.bpmEntry, a.bpmDigits = false, ""
	if digits == "" {
		return
	}
	target, err := strconv.Atoi(digits)
	if err != nil || target <= 0 {
		a.setBPMMessage("enter a target BPM between 1 and 999")
		return
	}
	scale, actual, clamped := a.scaleForBPM(float64(target))
	a.eng.SetTempoScale(scale)
	if clamped {
		a.setBPMMessage(fmt.Sprintf("%d BPM is outside the x%.2f-x%.2f practice range: set to %.0f BPM (x%.2f)",
			target, engineMinTempoScale, engineMaxTempoScale, actual, scale))
		return
	}
	a.setBPMMessage(fmt.Sprintf("tempo set to %.0f BPM (x%.2f)", actual, scale))
}

func (a *App) baseBPM() float64 {
	us := a.sc.Tempos.At(a.posTick())
	if us <= 0 {
		us = 500000
	}
	return 60e6 / float64(us)
}

func (a *App) scaleForBPM(target float64) (scale, actual float64, clamped bool) {
	base := a.baseBPM()
	scale = target / base
	if scale < engineMinTempoScale {
		scale, clamped = engineMinTempoScale, true
	}
	if scale > engineMaxTempoScale {
		scale, clamped = engineMaxTempoScale, true
	}
	return scale, base * scale, clamped
}

func (a *App) setBPMMessage(msg string) {
	a.bpmMsg, a.bpmMsgUntil = msg, a.frame+bpmMsgFrames
}

func (a *App) bpmMessage() string {
	if a.bpmMsg == "" || a.frame >= a.bpmMsgUntil {
		return ""
	}
	return a.bpmMsg
}

func (a *App) ensureMuteState() {
	if len(a.userMuted) == len(a.sc.Tracks) {
		return
	}
	a.userMuted = make([]bool, len(a.sc.Tracks))
	for i := range a.userMuted {
		a.userMuted[i] = a.eng.TrackMuted(i)
	}
}

func (a *App) mutedAudibly(i int) bool {
	a.ensureMuteState()
	return a.userMuted[i] || (a.solo > 0 && a.solo != i+1)
}

func (a *App) applyMutes() {
	a.ensureMuteState()
	for i := range a.userMuted {
		a.eng.SetTrackMuted(i, a.mutedAudibly(i))
	}
}

func (a *App) toggleMute(i int) {
	a.ensureMuteState()
	if i < 0 || i >= len(a.userMuted) {
		return
	}
	a.userMuted[i] = !a.userMuted[i]
	a.applyMutes()
}

func (a *App) toggleSolo(i int) {
	a.ensureMuteState()
	if i < 0 || i >= len(a.userMuted) {
		return
	}
	if a.solo == i+1 {
		a.solo = 0
	} else {
		a.solo = i + 1
	}
	a.applyMutes()
}

func (a *App) SetCountIn(beats int) {
	if beats > 0 {
		a.countInBeats, a.countInOn = beats, true
		a.engineCountIn = beats
	} else {
		a.countInBeats, a.countInOn = defaultCountInBeats, false
		a.engineCountIn = 0
	}
	a.countInStale = false
}

func (a *App) SyncCountIn(beats int) {
	if beats > 0 {
		a.countInBeats, a.countInOn = beats, true
	} else {
		a.countInBeats, a.countInOn = defaultCountInBeats, false
	}
	a.countInStale = a.CountInBeats() != a.engineCountIn
}

func (a *App) CountInBeats() int {
	if !a.countInOn {
		return 0
	}
	if a.countInBeats <= 0 {
		return defaultCountInBeats
	}
	return a.countInBeats
}

func (a *App) SetCountInApplier(fn func(beats int) bool) { a.countInApply = fn }

func (a *App) toggleCountIn() {
	a.countInOn = !a.countInOn
	applied := a.countInApply != nil && a.countInApply(a.CountInBeats())
	if applied {
		a.engineCountIn = a.CountInBeats()
		a.countInStale = false
		return
	}
	a.countInStale = a.CountInBeats() != a.engineCountIn
	if a.countInStale && a.reload == nil {
		a.setBPMMessage("the count-in change applies when the piece is next opened")
	}
}

type practiceBinding struct {
	Group string

	Keys string

	Hint string

	Desc string

	Enabled func(a *App) bool

	Reword func(a *App) (hint, desc string)

	Caveat func(a *App) string
}

func (b practiceBinding) enabled(a *App) bool { return b.Enabled == nil || b.Enabled(a) }

func liveInputCaveat(have func(a *App) bool) func(a *App) string {
	return func(a *App) string {
		if have(a) {
			return ""
		}
		return "needs live input — " + a.liveRemedy()
	}
}

var practiceBindings = []practiceBinding{
	{Group: "transport", Keys: "space", Hint: "space play/pause", Desc: "Start or pause playback"},
	{Group: "transport", Keys: "left / right", Hint: "arrows seek", Desc: "Jump to the previous / next bar; inside a loop, right wraps to the loop start"},
	{Group: "transport", Keys: "home / end", Desc: "Jump to the first / last bar"},
	{Group: "transport", Keys: "click / drag", Desc: "On the notation or the timeline: move the playhead"},

	{Group: "tempo", Keys: "up / down", Hint: "up/dn tempo", Desc: "Practice speed up / down by 5%"},
	{Group: "tempo", Keys: "shift+B", Hint: "shift+B bpm", Desc: "Type an exact target BPM"},
	{Group: "tempo", Keys: "R", Desc: "Ramp the speed up 5% after each loop pass"},
	{Group: "tempo", Keys: "C", Desc: "Count-in on / off for the next play"},

	{Group: "loop", Keys: "A / B", Hint: "A/B loop", Desc: "Set the loop start / end at the current bar"},
	{Group: "loop", Keys: "L", Desc: "Clear the loop"},
	{Group: "loop", Keys: "shift+drag", Desc: "Drag out a new loop on the notation or the timeline, snapped to beats"},
	{Group: "loop", Keys: "drag an edge", Desc: "Move a loop end: snaps to the beat, shift for tick-exact"},

	{Group: "tracks", Keys: "1..9", Hint: "1-9 mute", Desc: "Mute / unmute a track (or click its chip)"},
	{Group: "tracks", Keys: "shift+1..9", Desc: "Solo a track; press again to release (or right-click it)"},

	{Group: "practice", Keys: "M", Hint: "M click", Desc: "Metronome click on / off"},

	{Group: "practice", Keys: "T", Hint: "T tuner", Desc: "Tuner overlay",
		Caveat: liveInputCaveat(func(a *App) bool { return a.live })},
	{Group: "practice", Keys: "W", Hint: "W wait", Desc: "Wait at each note until you play it",
		Enabled: func(a *App) bool { return a.waitCtl },
		Caveat:  liveInputCaveat(func(a *App) bool { return a.waitCtl })},

	{Group: "view", Keys: "+ / -", Desc: "Zoom the notation in / out (or the wheel over it)"},

	{Group: "session", Keys: "S", Hint: "S settings", Desc: "Settings, without leaving the piece",
		Enabled: func(a *App) bool { return a.settings != nil },
		Caveat: func(a *App) string {
			if a.settings == nil {
				return "needs the full app — start musicTutor without a file"
			}
			return ""
		}},
	{Group: "session", Keys: "F5", Desc: "Re-open this piece, picking up changed settings",
		Enabled: func(a *App) bool { return a.reload != nil }},
	{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
	{Group: "session", Keys: "D", Desc: "Dismiss the warning banner"},
	{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Leave this piece and go back",
		Reword: func(a *App) (string, string) {
			if a.quitAll == nil {

				return "esc quit", "Leave the piece — with nothing behind it, this quits"
			}
			return "esc back", "Leave this piece and go back"
		}},
	{Group: "session", Keys: "Q", Hint: "Q quit", Desc: "Quit musicTutor"},
}

func (a *App) helpRows() []helpBinding {
	out := make([]helpBinding, len(practiceBindings))
	for i, b := range practiceBindings {
		hint, desc := b.Hint, b.Desc
		if b.Reword != nil {
			hint, desc = b.Reword(a)
		}
		explained := false
		if b.Caveat != nil {
			if c := b.Caveat(a); c != "" {
				desc += " (" + c + ")"
				explained = true
			}
		}
		off := !b.enabled(a)
		out[i] = helpBinding{Group: b.Group, Keys: b.Keys, Hint: hint, Desc: desc,
			Off: off, Explained: off && explained}
	}
	return out
}

func (a *App) hintLine() string { return hintLineOf(a.helpRows()) }

func (a *App) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)
	a.anim.tick()
	l := a.layout()
	if a.tunerView {
		a.drawTuner(screen)
	} else {
		a.drawTab(screen)
	}
	a.drawHUD(screen, l)
	if a.warningVisible() {
		a.drawWarning(screen)
	}
	if a.bpmEntry {
		a.drawBPMEntry(screen)
	}
	if a.helpOpen {
		a.drawHelp(screen)
	}
}

func (a *App) drawTab(screen *ebiten.Image) {

	pos := a.posF()
	posTick := int64(math.Round(pos))
	ppt := a.pxPerTick()
	phX := float32(screenW * playheadX)
	tr := a.displayed()
	wind := tr.Wind != nil
	nLines := a.laneLines()
	bandTop, bandBottom := float64(tabTop), a.tabBottom()

	var ladder windLadder
	if wind {
		ladder = windLadderFor(tr)
	}

	tickToX := func(t int64) float32 {
		return phX + float32((float64(t)-pos)*ppt)
	}

	minTick := posTick - int64(float64(phX)/ppt) - score.PPQ
	maxTick := posTick + int64(float64(screenW-float64(phX))/ppt) + score.PPQ

	if wind {
		a.drawWindGrid(screen, ladder, bandTop, bandBottom)
	} else {
		for si := 0; si < nLines; si++ {
			y := float32(tabTop + si*stringGap)
			vector.StrokeLine(screen, 0, y, screenW, y, 1, colString, false)
		}
	}

	if la, lb, on := a.eng.Loop(); on {
		x0, x1 := tickToX(la), tickToX(lb)
		vector.DrawFilledRect(screen, x0, tabTop-20, x1-x0, float32(bandBottom-bandTop)+40, colLoop, false)
		vector.StrokeLine(screen, x0, tabTop-20, x0, float32(bandBottom+20), 2, colLoopEdge, false)
		vector.StrokeLine(screen, x1, tabTop-20, x1, float32(bandBottom+20), 2, colLoopEdge, false)
	}

	waiting := a.waitingKeys()
	for bi, bar := range tr.Bars {
		barEnd := bar.Start + bar.Len()
		if barEnd < minTick || bar.Start > maxTick {
			continue
		}
		x := tickToX(bar.Start)
		vector.StrokeLine(screen, x, tabTop-14, x, float32(bandBottom+14), 1, colBarline, false)
		drawTextSmall(screen, fmt.Sprintf("%d", bi+1), float64(x)+4, tabTop-32, colHint)

		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				nx := tickToX(beat.Start)
				var ny float32
				var label string
				if wind {
					ny = float32(ladder.y(tr.Wind.Written(tr.Pitch(n)), bandTop, bandBottom))
					label = windNoteLabel(tr, n)
				} else {
					ny = float32(tabTop + (n.String-1)*stringGap)
					label = fmt.Sprintf("%d", n.Fret)
					if n.Tied {
						label = "~" + label
					}
					if n.Tech&score.TechDead != 0 {
						label = "x"
					}
				}
				col, sounding := a.noteCue(beat.Start, beat.Dur, n.String, n.Inferred, pos, posTick, waiting)

				w := float32(textWMono(label))
				vector.DrawFilledRect(screen, nx-2, ny-9, w+4, 18, colNoteBG, false)
				drawTextMono(screen, label, float64(nx), float64(ny)-9, col)
				if sounding {
					vector.DrawFilledRect(screen, nx-2, ny+9, w+4, 2, colSounding, false)
				}
			}
		}
	}

	if n := len(tr.Bars); n > 0 {
		endTick := tr.Bars[n-1].Start + tr.Bars[n-1].Len()
		x := tickToX(endTick)
		vector.StrokeLine(screen, x, tabTop-14, x, float32(bandBottom+14), 2, colBarline, false)
	}

	vector.StrokeLine(screen, phX, tabTop-24, phX, float32(bandBottom+24), 2, colPlayhead, false)

}

func (a *App) noteCue(start, dur int64, str int, inferred bool, pos float64, posTick int64, waiting map[noteKey]bool) (col color.RGBA, sounding bool) {
	col = colNote
	if inferred {
		col = colInferred
	}
	if v, ok := a.verdictAt(start, str, posTick); ok {
		col = verdictColor(v)
	}
	if waiting[noteKey{start, str}] {
		col = a.pulseCol()
	}
	return col, float64(start) <= pos && pos < float64(start+dur)
}

func (a *App) drawHUD(screen *ebiten.Image, l practiceLayout) {
	drawHeader(screen, a.pieceTitle(), a.statusLine(), a.statusColour())
	a.drawTransport(screen, l, a.ptr)
	a.drawTrackStrip(screen, l, a.ptr)
	a.drawTimeline(screen, l, a.ptr)

	if in, left := a.eng.CountingIn(); in {
		a.drawStateChip(screen, fmt.Sprintf("COUNT-IN %d", left), colCountIn, a.stateChipY())
	} else if a.eng.Waiting() {
		a.drawStateChip(screen, "WAITING", a.pulseCol(), a.stateChipY())
	}
	if a.live {
		a.drawLiveHUD(screen)
		a.drawLegend(screen)
	}
	if msg := a.bpmMessage(); msg != "" {
		drawText(screen, msg, centreX(msg, 0, screenW), a.msgY(), colSounding)
	}
	if msg := a.reloadPrompt(); msg != "" {
		drawText(screen, msg, centreX(msg, 0, screenW), a.msgY()+20, colClose)
	}
	drawFooter(screen, a.hintLine())
}

func (a *App) pieceTitle() string {
	if a.sc.Title == "" {
		return "musicTutor"
	}

	const gap = 32.0
	budget := screenW - 2*uiPadX - textW(a.statusLine()) - gap
	return truncateWScaled(a.sc.Title, budget, uiTitleScl)
}

func (a *App) statusLine() string {
	state := "paused"
	if a.eng.Playing() {
		state = "playing"
	}
	if in, _ := a.eng.CountingIn(); in {
		state = "count-in"
	}
	s := fmt.Sprintf("%s     %.0f BPM  (x%.2f)", state, a.eng.EffectiveBPM(), a.eng.TempoScale())

	if _, _, on := a.eng.Loop(); on {
		s += fmt.Sprintf("     pass %d", a.eng.PassCount())
	}
	if a.live {
		s += "     live"
	}
	return s
}

func (a *App) statusColour() color.RGBA {
	if a.eng.Playing() {
		return colSounding
	}
	return colHUD
}

func (a *App) drawBPMEntry(screen *ebiten.Image) {
	r := bpmEntryRect()
	vector.DrawFilledRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), colBG, false)
	vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 2, colSounding, false)
	drawText(screen, "target BPM", r.x+16, r.y+12, colHUD)
	drawTextScaled(screen, a.bpmDigits+"_", r.x+16, r.y+30, 2, colNote)
	drawText(screen, "enter apply    esc or click away to cancel", r.x+16, r.y+64, colHint)
}

func (a *App) drawHelp(screen *ebiten.Image) {
	drawHelpOverlay(screen, "PRACTICE KEYS", a.helpRows(), practiceHelpFootnote)
}

func (a *App) Layout(int, int) (int, int) { return screenW, screenH }
