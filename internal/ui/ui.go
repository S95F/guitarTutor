// Package ui is the Ebitengine practice view: a scrolling tablature with a
// fixed playhead, driven entirely by the engine's clock. The UI never keeps
// its own notion of time — every frame it asks the engine where it is
// (PosTick) and draws accordingly, so display and audio cannot drift.
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

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/score"
)

const (
	screenW = 1280
	screenH = 720

	playheadX = 0.3 // fraction of width where "now" sits
	stringGap = 26
	// tabTop leaves the top of the window to the transport row and the
	// track strip (see transport.go), so the controls and the notation
	// stop competing for the same band of pixels. It is chosen to centre a
	// standard six-string staff between the controls above and the
	// timeline below; the layout test checks that four to eight strings
	// all still fit without collisions.
	tabTop     = 274
	defaultPxQ = 96 // pixels per quarter note at zoom 1
)

// The engine clamps the practice tempo scale to this range but does not
// export the bounds, so the practice view mirrors them. They are needed
// here only to tell the user that a typed BPM was out of reach and what
// they got instead; the engine remains the authority that actually clamps.
const (
	engineMinTempoScale = 0.25
	engineMaxTempoScale = 2.0
)

// bpmEntryMaxDigits caps typed tempo input at three digits. 999 BPM is
// already far past the engine's ceiling for any real piece, and a cap
// keeps the entry box a fixed size.
const bpmEntryMaxDigits = 3

// bpmMsgFrames is how long the tempo-entry result line stays on screen,
// in Update ticks (Ebitengine runs at 60 per second).
const bpmMsgFrames = 300

// defaultCountInBeats is the count-in the C key turns on for a piece that
// was opened without one.
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
	// colLoop is the loop region's wash. color.RGBA is alpha-premultiplied
	// — that is what image/color means by the type — so the components
	// have to be scaled by the alpha themselves. Written the obvious way,
	// {60, 160, 90, 60}, the green is four times its own alpha, which
	// Ebitengine clamps into a near-solid block that buries the tab it is
	// supposed to tint. These are (60, 160, 90) premultiplied by 90/255.
	colLoop     = color.RGBA{21, 56, 32, 90}
	colLoopEdge = color.RGBA{60, 160, 90, 255}
	colHUD      = color.RGBA{200, 200, 210, 255}
	colCountIn  = color.RGBA{255, 200, 60, 255}

	// Phase 2 live feedback (live.go).
	colHit      = color.RGBA{80, 210, 120, 255}  // verdict: hit
	colClose    = color.RGBA{240, 160, 50, 255}  // verdict: close
	colMiss     = color.RGBA{235, 80, 80, 255}   // verdict: miss
	colWaitLo   = color.RGBA{180, 140, 40, 255}  // wait pulse, dim phase
	colWaitHi   = color.RGBA{255, 245, 180, 255} // wait pulse, bright phase
	colTuneZone = color.RGBA{40, 90, 55, 255}    // tuner in-tune band

	// Phase 5 overlays.
	// colHelpDim is the help overlay backdrop: near-opaque, and
	// alpha-premultiplied like every other translucent colour here.
	colHelpDim = color.RGBA{4, 4, 6, 200}    // help overlay backdrop, behind the card
	colWarnBG  = color.RGBA{58, 18, 18, 240} // live-warning banner fill
)

// App is the ebiten.Game for one practice session. It also satisfies
// Screen, so the Shell can host it unchanged: Update returns errQuit when
// the user leaves the piece and the Shell pops back to whatever opened it.
type App struct {
	eng   *engine.Engine
	sc    *score.Score
	track int // index of the displayed (tab) track

	zoom float64
	// The metronome and ramp have no engine-side getters (set-only
	// config), so the UI mirrors their state for the HUD and toggles.
	metronome bool
	ramp      bool

	// Fixed-BPM entry (shift+B). While bpmEntry is open Update routes
	// every key to the entry, so digits cannot also fire the track
	// mute/solo bindings. bpmMsg reports the result — in particular that
	// a target was clamped — until bpmMsgUntil.
	bpmEntry    bool
	bpmDigits   string
	bpmMsg      string
	bpmMsgUntil int64

	// Track mute and solo. userMuted is the user's own per-track mute
	// choice and is the only thing the mute keys write; solo is layered
	// over it (see mutedAudibly), so releasing solo restores exactly the
	// mutes the user had asked for. solo is 1-based with 0 meaning "no
	// solo", which keeps the zero value inert.
	userMuted []bool
	solo      int

	// Count-in state for the next Play. countInBeats is the configured
	// length, countInOn the C toggle, and engineCountIn what the running
	// engine was actually constructed with (0 = none). countInStale is
	// true while the user's choice differs from engineCountIn and no
	// applier could push it through — a comparison, not a latch, so a
	// toggle-and-back withdraws the reload offer it raised.
	countInBeats  int
	countInOn     bool
	engineCountIn int
	countInStale  bool
	countInApply  func(beats int) bool

	// helpOpen is the ?/F1 key-binding overlay; while it is up Update
	// routes every key to it so nothing acts on the piece behind it.
	helpOpen bool

	// quitAll, when the integrator has wired it, is what Q does: leave
	// the whole application rather than just this piece. nil keeps the
	// standalone behaviour of quitting the process.
	quitAll func()

	// settings opens the settings screen over this piece (the S key and
	// the SETTINGS chip). nil in a standalone practice window, where
	// there is no shell to host another screen: the chip is then drawn
	// disabled rather than lying about a key that does nothing.
	settings func()
	// reload re-opens the current piece. Some settings — the audio
	// device, the SoundFont, the count-in — are read when a piece is
	// opened and cannot reach a running engine, so changing one leaves
	// the view showing something the configuration no longer describes.
	// Rather than pretend otherwise, the view offers this.
	reload func()
	// anim eases the transport buttons and the chips; see anim.go.
	anim animator

	// outLatency reports how much rendered audio has not been heard yet,
	// and latency is that figure smoothed over display frames. Together
	// they are what puts the playhead on the note that is SOUNDING rather
	// than the one being rendered (see playhead.go). latencyArmed marks
	// the first reading, which is taken whole instead of being eased into
	// from zero.
	outLatency   func() time.Duration
	latency      float64
	latencyArmed bool

	// settingsTouched is set when the settings screen this view opened
	// changed something that only applies at open time, and drives the
	// reload prompt.
	settingsTouched bool

	// Mouse state, refreshed once per Update. drag is the gesture that
	// currently owns the pointer; wheelAcc carries fractional trackpad
	// scroll between frames.
	ptr      pointer
	drag     dragTarget
	wheelAcc float64

	// The shift+drag loop gesture (dragLoopNew). loopDrawTick and
	// loopDrawX anchor the press — the tick under it and the pixel it
	// landed on — and loopDrawing latches once the pointer has travelled
	// far enough that the gesture means "draw a loop" rather than the
	// seek the same press performs without shift. loopDrawOnTimeline
	// remembers which surface anchored the gesture, because the two map
	// pixels to ticks at very different scales and the drag must stay in
	// the one it started in.
	loopDrawTick       int64
	loopDrawX          float64
	loopDrawing        bool
	loopDrawOnTimeline bool

	// ph is the drawn playback position (playhead.go): the engine's, but
	// carried smoothly between its once-per-block publishes.
	ph playhead

	// liveUI carries the Phase 2 live-feedback state and feed mailbox
	// (live.go). Its zero value is fully inert: no feeds, Phase 1
	// behavior.
	liveUI
}

// New builds the practice view. track is the index into sc.Tracks to
// display as tablature (play/mute always applies to all tracks).
//
// The displayed track is also the track wait mode halts on. Left to its
// default the engine waits at every RoleUser track's notes, which freezes
// playback for good on a piece with two user parts — the wait engages on
// a note this view never draws and the scorer never expects, so nothing
// can release it. Setting it here means the halt, the tablature and the
// expectation always describe the same music (bug review W2/W3).
func New(eng *engine.Engine, sc *score.Score, track int) *App {
	eng.SetWaitTrack(track)
	return &App{eng: eng, sc: sc, track: track, zoom: 1, countInBeats: defaultCountInBeats}
}

// Run opens the window and blocks until the user quits. It is the
// standalone entry point; under the Shell the App is just a Screen and
// Shell.Run owns the window.
func (a *App) Run() error {
	ebiten.SetWindowSize(screenW, screenH)
	title := a.sc.Title
	if title == "" {
		title = "guitarTutor"
	} else {
		title = "guitarTutor — " + title
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

// Track is the index of the part being practiced: the one drawn as
// tablature, waited on, and — through the caller wiring the scorer — the
// one scored. It exists so the live wiring reads the track from the view
// rather than being handed it again, which is how those three came to
// disagree in the first place (bug review W3).
func (a *App) Track() int { return a.track }

func (a *App) displayed() *score.Track { return a.sc.Tracks[a.track] }

// barAt returns the index in the displayed track's bars containing tick.
func (a *App) barAt(tick int64) int {
	bars := a.displayed().Bars
	for i, b := range bars {
		if tick < b.Start+b.Len() {
			return i
		}
	}
	return len(bars) - 1
}

// shiftHeld reports whether either shift key is down. Shift is the
// modifier that turns B into the BPM entry and the digits into solos.
func (a *App) shiftHeld() bool {
	return ebiten.IsKeyPressed(ebiten.KeyShiftLeft) || ebiten.IsKeyPressed(ebiten.KeyShiftRight)
}

// SetQuitAll installs the action for the Q key: leaving the application
// rather than only this piece. The integrator wires it to whatever ends
// the Shell's run loop. With no action installed Q behaves as it always
// has and finishes the screen, which for a standalone practice window is
// the same thing.
//
// Escape is unaffected and always finishes only this screen, so under the
// Shell it means "back to the song list".
func (a *App) SetQuitAll(fn func()) { a.quitAll = fn }

// Update handles input. All controls go straight to the engine.
//
// Returning errQuit means "this screen is finished": the Shell pops the
// practice view, releases its audio and shows whatever opened it. Escape
// is deliberately that key, so leaving a piece never leaves the app.
func (a *App) Update() error {
	a.frame++
	a.syncLive()
	// Before any input: the playhead advances one display frame here and
	// only here, so a seek made further down this Update is picked up on
	// the next one, as the jump it is.
	a.stepPlayhead()
	a.ptr = readPointer()

	// Modal layers take the whole keyboard. The BPM entry must swallow
	// digits so they cannot double as track mute/solo keys, and the help
	// overlay must not let a stray key act on the piece behind it. Both
	// also close on a click outside themselves, because a modal you can
	// only dismiss from the keyboard is a dead end for anyone who opened
	// it with the mouse.
	if a.bpmEntry || a.helpOpen {
		// A modal owns every input, including a drag that was in flight
		// when it opened: the gesture dies here rather than resuming on
		// whatever the cursor points at when the modal closes.
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

// handleKeys applies this frame's keyboard. It returns errQuit when the
// user leaves the piece.
func (a *App) handleKeys() error {
	eng := a.eng
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		// Finish this screen only: back to the song list.
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
		// Shift turns the loop-end key into the exact-tempo entry.
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

// quitApp is the Q key: leave the whole application when the integrator
// has wired that up (SetQuitAll), otherwise finish this screen, which for
// a standalone practice window ends the process.
func (a *App) quitApp() error {
	if a.quitAll != nil {
		a.quitAll()
		return nil
	}
	return errQuit
}

// SetSettingsOpener installs the action behind the S key and the SETTINGS
// chip: showing the settings screen over this piece. Without it — a
// standalone practice window has no shell to host another screen — the
// chip is drawn disabled and S does nothing, rather than the view
// advertising a key that silently fails.
func (a *App) SetSettingsOpener(fn func()) { a.settings = fn }

// SetReloader installs the action behind F5: re-opening the piece from
// disk. The integrator supplies it because re-opening means rebuilding an
// engine and an audio stream, which the view knows nothing about.
func (a *App) SetReloader(fn func()) { a.reload = fn }

// MarkSettingsChanged tells the view that a setting which is only read
// when a piece is opened has changed underneath it — the capture device,
// the SoundFont, the count-in. The view then offers a reload instead of
// going on displaying a session the configuration no longer describes.
// Whoever hosts the settings screen calls this on the way back.
func (a *App) MarkSettingsChanged() { a.settingsTouched = a.reload != nil }

// openSettings shows the settings screen over the piece, when the
// integrator has wired one.
func (a *App) openSettings() {
	if a.settings != nil {
		a.settings()
	}
}

// reloadPiece (F5) re-opens the piece, picking up settings the running
// engine could not. The prompt is deliberately not cleared here: a reload
// that succeeds replaces this whole screen, taking the prompt with it,
// and after one that fails the offer is still true — clearing it made a
// failed reload silently withdraw the only hint that the session no
// longer matches the configuration (audit A2).
func (a *App) reloadPiece() {
	if a.reload == nil {
		return
	}
	a.reload()
}

// reloadPrompt is the line offering a reload, or "" when there is
// nothing to offer. Two independent conditions raise it: the settings
// screen reported an open-time change on the way back (settingsTouched),
// or the C key holds a count-in the running engine was not built with
// (countInStale). The second withdraws itself when the toggle round-trips
// back to the engine's value.
func (a *App) reloadPrompt() string {
	if !a.settingsTouched && !a.countInStale {
		return ""
	}
	return "settings changed: press F5 to re-open this piece with them"
}

// SetTunerView opens or closes the tuner overlay, and SetHelpOpen the
// key-binding overlay. The T and ?/F1 keys do the same; these exist so a
// caller can put the view into a known state without synthesizing key
// presses — which is what the screenshot tool needs in order to render
// the overlays for review, and what an integrator would need to drive
// the tuner from a menu of their own.
func (a *App) SetTunerView(on bool) { a.tunerView = on }

// SetHelpOpen raises or lowers the key-binding overlay. See SetTunerView.
func (a *App) SetHelpOpen(on bool) { a.helpOpen = on }

// openHelp raises the key-binding overlay.
func (a *App) openHelp() { a.helpOpen = true }

// closeHelp lowers the key-binding overlay.
func (a *App) closeHelp() { a.helpOpen = false }

// updateHelp runs the key-binding overlay: escape, F1 or ? closes it, a
// click anywhere closes it, and nothing else reaches the piece. Escape
// closing the overlay rather than the piece is why the overlay is handled
// before the main bindings.
func (a *App) updateHelp() {
	if helpDismissed(a.ptr) {
		a.closeHelp()
	}
}

// loopSetA (the A key) sets the loop start to the current bar, keeping the
// end if a loop is already set past it. No-op when the displayed track has
// no bars (barAt returns -1).
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

// loopSetB (the B key) sets the loop end to the current bar's end, keeping
// the start if a loop is already set before it. No-op when the displayed
// track has no bars.
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
	// The toggle stands either way — arming the ramp before setting the
	// loop is a fine order to do things in — but a chip that lights up
	// when nothing can happen is a promise, so the transient line says
	// what is still missing. Rising toward 1.0 is all the engine's ramp
	// does, which is why full speed has nothing to ramp to.
	if _, _, on := a.eng.Loop(); !on {
		a.setBPMMessage("ramp raises the speed after each loop pass — set a loop first")
	} else if a.eng.TempoScale() >= 1.0 {
		a.setBPMMessage("already at full speed — slow down first and ramp brings you back up")
	}
}

// SetInitialMetronome records the metronome state chosen at startup so the
// M key toggles from the right place.
func (a *App) SetInitialMetronome(on bool) { a.metronome = on }

// --- Fixed-BPM entry (shift+B) ---

// openBPMEntry starts typing an exact target tempo. It clears any previous
// result message so the box is unambiguous.
func (a *App) openBPMEntry() {
	a.bpmEntry, a.bpmDigits = true, ""
	a.bpmMsg, a.bpmMsgUntil = "", 0
}

// updateBPMEntry runs the numeric entry: digits type, backspace deletes,
// enter applies, escape cancels. Nothing else is read, which is the point
// — the digits must not reach the track mute/solo bindings.
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
		// Clicking away from a box is how every other application
		// dismisses one; a modal that ignores it traps a mouse user.
		a.cancelBPMEntry()
	}
}

// bpmEntryRect is the tempo-entry box, centred over the tab.
func bpmEntryRect() rect {
	const w, h = 300, 84
	return rect{(screenW - w) / 2, tabTop + 20, w, h}
}

// bpmDigit appends one typed digit, up to bpmEntryMaxDigits. A leading
// zero is dropped so "0" then "9" reads as 9 rather than 09.
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

// bpmBackspace deletes the last typed digit.
func (a *App) bpmBackspace() {
	if n := len(a.bpmDigits); n > 0 {
		a.bpmDigits = a.bpmDigits[:n-1]
	}
}

// cancelBPMEntry closes the entry without touching the tempo.
func (a *App) cancelBPMEntry() {
	a.bpmEntry, a.bpmDigits = false, ""
}

// commitBPMEntry applies the typed target. The engine practises in tempo
// scale, not BPM, so the target is converted against the score's own tempo
// at the current position and clamped to the engine's range; when the
// target was out of reach the user is told what they got instead.
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

// baseBPM is the score's own tempo at the playhead, before practice
// scaling. A missing or invalid tempo map falls back to the SMF default of
// 120 BPM, which is what score.TempoMap.At already assumes.
func (a *App) baseBPM() float64 {
	us := a.sc.Tempos.At(a.posTick())
	if us <= 0 {
		us = 500000
	}
	return 60e6 / float64(us)
}

// scaleForBPM converts a target tempo into the practice scale that
// produces it at the current position, clamped to the engine's range. It
// reports the scale to apply, the BPM that scale actually yields, and
// whether the target had to be clamped to get there.
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

// setBPMMessage posts the entry's result for bpmMsgFrames frames.
func (a *App) setBPMMessage(msg string) {
	a.bpmMsg, a.bpmMsgUntil = msg, a.frame+bpmMsgFrames
}

// bpmMessage is the tempo-entry result still worth showing, or "".
func (a *App) bpmMessage() string {
	if a.bpmMsg == "" || a.frame >= a.bpmMsgUntil {
		return ""
	}
	return a.bpmMsg
}

// --- Track mute and solo ---

// ensureMuteState lazily seeds the remembered per-track mute choices from
// the engine, so a piece opened with tracks already muted (the cmd layer
// mutes the player's own track for play-along) starts from that baseline
// instead of silently unmuting it.
func (a *App) ensureMuteState() {
	if len(a.userMuted) == len(a.sc.Tracks) {
		return
	}
	a.userMuted = make([]bool, len(a.sc.Tracks))
	for i := range a.userMuted {
		a.userMuted[i] = a.eng.TrackMuted(i)
	}
}

// mutedAudibly reports whether track i is silent right now: either the
// user muted it, or another track is soloed. Mute and solo are separate
// flags layered at this one point, which is what lets solo be released
// without disturbing the user's own mutes.
func (a *App) mutedAudibly(i int) bool {
	a.ensureMuteState()
	return a.userMuted[i] || (a.solo > 0 && a.solo != i+1)
}

// applyMutes pushes the mute/solo layering into the engine.
func (a *App) applyMutes() {
	a.ensureMuteState()
	for i := range a.userMuted {
		a.eng.SetTrackMuted(i, a.mutedAudibly(i))
	}
}

// toggleMute (keys 1-9) flips the user's own mute for track i. Under an
// active solo the flag is still recorded — it just may not be what you
// hear until the solo is released.
func (a *App) toggleMute(i int) {
	a.ensureMuteState()
	if i < 0 || i >= len(a.userMuted) {
		return
	}
	a.userMuted[i] = !a.userMuted[i]
	a.applyMutes()
}

// toggleSolo (shift+1..9) solos track i, muting every other track;
// soloing the same track again releases it and restores exactly the mutes
// the user had chosen. Soloing a different track moves the solo.
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

// --- Count-in ---

// SetCountIn records the count-in the engine was constructed with, so the
// C key toggles from the right place and the HUD reports the truth. Zero
// or less means the piece opened without one: C then turns on a four-beat
// count-in.
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

// SyncCountIn updates the count-in the user has chosen — after the
// settings screen changed it underneath a running piece — WITHOUT
// touching engineCountIn, which still records what the engine was built
// with. Keeping those apart is the point: the view now offers the right
// value on the next toggle, and the pending comparison still knows the
// running engine does not have it, so the F5 prompt stays up.
//
// Without this the view kept its open-time mirror, and the next press of
// C wrote that stale value straight back over what the user had just
// chosen in settings.
func (a *App) SyncCountIn(beats int) {
	if beats > 0 {
		a.countInBeats, a.countInOn = beats, true
	} else {
		a.countInBeats, a.countInOn = defaultCountInBeats, false
	}
	a.countInStale = a.CountInBeats() != a.engineCountIn
}

// CountInBeats reports the count-in the user wants for the next Play: the
// configured number of beats, or 0 once C has switched it off. The
// integrator can read this when re-opening the piece, or persist it.
func (a *App) CountInBeats() int {
	if !a.countInOn {
		return 0
	}
	if a.countInBeats <= 0 {
		return defaultCountInBeats
	}
	return a.countInBeats
}

// SetCountInApplier installs the hook the C key uses to push a changed
// count-in into the running engine. It is called with the beats the user
// now wants (0 = off) and must report whether the change will take effect
// on the next Play.
//
// engine.Engine has no such setter today: Options.CountInBeats is
// construction-only. With no applier installed — or one that reports
// false — the C key still records the choice and the HUD says the change
// applies when the piece is re-opened, rather than pretending it worked.
func (a *App) SetCountInApplier(fn func(beats int) bool) { a.countInApply = fn }

// toggleCountIn (the C key and the COUNT-IN chip) flips the count-in for
// the next Play. The engine takes its count-in at construction, so when
// no applier could push the change into the running engine the chip alone
// would be lying — it lights up for a change that will not happen. The
// old HUD line carried an "(on re-open)" caveat; its replacement is
// active: with a reloader wired a PENDING change raises the F5 prompt,
// and without one the transient message line says when the choice takes
// effect (audit A6).
//
// Pending is a comparison against what the engine was built with, not a
// latch: toggling to a new value and back is no change at all, and an
// early version that latched on every unapplied press left a permanent
// false "settings changed" prompt after a C-C round trip (verification
// follow-up to A6). settingsTouched is untouched here — it belongs to
// the settings screen's own round-trip-guarded comparison.
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

// --- Key bindings: one table, two renderings ---

// A practiceBinding is one row of the practice view's control table: the
// key, the compact wording for the HUD hint line, and the sentence the
// help overlay shows. Every key the practice view responds to appears here
// exactly once and both renderings are generated from it, so the hint line
// and the overlay cannot drift apart.
type practiceBinding struct {
	// Group is the help overlay's section heading. Consecutive rows
	// sharing a group are drawn under one heading, in table order.
	Group string
	// Keys is the key label for the overlay, e.g. "shift+1..9".
	Keys string
	// Hint is the complete fragment for the one-line HUD hint, e.g.
	// "space play/pause". Empty leaves the binding out of the hint line;
	// the overlay still lists it.
	Hint string
	// Desc is the overlay's description.
	Desc string
	// Enabled reports whether the binding currently does anything. nil
	// means always. W needs a live detector, so it is gated: without one
	// it is dropped from the hint line and greyed in the overlay.
	Enabled func(a *App) bool
	// Reword replaces Hint and Desc when what a key DOES depends on how
	// the view is hosted. Escape is the case: under the shell it returns
	// to the start screen, but `guitartutor play song.gp` has nothing
	// behind it, so the same key ends the process — and saying "go back"
	// there, one line above "Q quit", promises a screen that does not
	// exist.
	//
	// A binding that carries both a Reword and an Enabled gate takes on a
	// duty: while it is off, the reworded Desc must say what would turn
	// it on. The overlay then shows that sentence in place of its generic
	// "(not available now)", because a greyed row that names its remedy
	// is an explanation and one that only announces its own absence is a
	// dead end.
	Reword func(a *App) (hint, desc string)
}

// enabled resolves the optional gate.
func (b practiceBinding) enabled(a *App) bool { return b.Enabled == nil || b.Enabled(a) }

// practiceBindings is the single source of truth for the practice view's
// keyboard. Order matters: it is the order of both the hint line and the
// help overlay.
var practiceBindings = []practiceBinding{
	{Group: "transport", Keys: "space", Hint: "space play/pause", Desc: "Start or pause playback"},
	{Group: "transport", Keys: "left / right", Hint: "arrows seek", Desc: "Jump to the previous / next bar; inside a loop, right wraps to the loop start"},
	{Group: "transport", Keys: "home / end", Desc: "Jump to the first / last bar"},
	{Group: "transport", Keys: "click / drag", Desc: "On the tab or the timeline: move the playhead"},

	{Group: "tempo", Keys: "up / down", Hint: "up/dn tempo", Desc: "Practice speed up / down by 5%"},
	{Group: "tempo", Keys: "shift+B", Hint: "shift+B bpm", Desc: "Type an exact target BPM"},
	{Group: "tempo", Keys: "R", Desc: "Ramp the speed up 5% after each loop pass"},
	{Group: "tempo", Keys: "C", Desc: "Count-in on / off for the next play"},

	// A and B share a row: the overlay has no scrolling and the card is
	// full, so a new gesture buys its line from a pair that reads as one
	// idea anyway.
	{Group: "loop", Keys: "A / B", Hint: "A/B loop", Desc: "Set the loop start / end at the current bar"},
	{Group: "loop", Keys: "L", Desc: "Clear the loop"},
	{Group: "loop", Keys: "shift+drag", Desc: "Drag out a new loop on the tab or the timeline, snapped to beats"},
	{Group: "loop", Keys: "drag an edge", Desc: "Move a loop end: snaps to the beat, shift for tick-exact"},

	{Group: "tracks", Keys: "1..9", Hint: "1-9 mute", Desc: "Mute / unmute a track (or click its chip)"},
	{Group: "tracks", Keys: "shift+1..9", Desc: "Solo a track; press again to release (or right-click it)"},

	{Group: "practice", Keys: "M", Hint: "M click", Desc: "Metronome click on / off"},
	// T stays active without live input — the overlay it opens explains
	// itself — but its row carries the same caveat the overlay shows, so
	// nobody has to open a silent tuner to learn why it is silent.
	{Group: "practice", Keys: "T", Hint: "T tuner", Desc: "Tuner overlay",
		Reword: func(a *App) (string, string) {
			if !a.live {
				return "T tuner", "Tuner overlay (needs live input — " + a.liveRemedy() + ")"
			}
			return "T tuner", "Tuner overlay"
		}},
	{Group: "practice", Keys: "W", Hint: "W wait", Desc: "Wait at each note until you play it",
		Enabled: func(a *App) bool { return a.waitCtl },
		Reword: func(a *App) (string, string) {
			if !a.waitCtl {
				return "W wait", "Wait at each note until you play it (needs live input — " + a.liveRemedy() + ")"
			}
			return "W wait", "Wait at each note until you play it"
		}},

	{Group: "view", Keys: "+ / -", Desc: "Zoom the tab in / out (or the wheel over it)"},

	{Group: "session", Keys: "S", Hint: "S settings", Desc: "Settings, without leaving the piece",
		Enabled: func(a *App) bool { return a.settings != nil },
		Reword: func(a *App) (string, string) {
			if a.settings == nil {
				return "S settings", "Settings, without leaving the piece (needs the full app — start guitarTutor without a file)"
			}
			return "S settings", "Settings, without leaving the piece"
		}},
	{Group: "session", Keys: "F5", Desc: "Re-open this piece, picking up changed settings",
		Enabled: func(a *App) bool { return a.reload != nil }},
	{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
	{Group: "session", Keys: "D", Desc: "Dismiss the warning banner"},
	{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Leave this piece and go back",
		Reword: func(a *App) (string, string) {
			if a.quitAll == nil {
				// Nothing hosts this view, so finishing it ends the run.
				return "esc quit", "Leave the piece — with nothing behind it, this quits"
			}
			return "esc back", "Leave this piece and go back"
		}},
	{Group: "session", Keys: "Q", Hint: "Q quit", Desc: "Quit guitarTutor"},
}

// helpRows resolves the binding table against this App: which bindings
// do anything right now is a question only the view can answer.
func (a *App) helpRows() []helpBinding {
	out := make([]helpBinding, len(practiceBindings))
	for i, b := range practiceBindings {
		hint, desc := b.Hint, b.Desc
		if b.Reword != nil {
			hint, desc = b.Reword(a)
		}
		off := !b.enabled(a)
		// An off row whose Reword names its remedy has already explained
		// itself (the contract on the Reword field), so the overlay skips
		// its generic unavailability stamp.
		out[i] = helpBinding{Group: b.Group, Keys: b.Keys, Hint: hint, Desc: desc,
			Off: off, Explained: off && b.Reword != nil}
	}
	return out
}

// hintLine is the one-line footer summary, built from the same table as
// the overlay so the two cannot drift apart.
func (a *App) hintLine() string { return hintLineOf(a.helpRows()) }

// Draw renders the frame from engine state. The tuner overlay (T)
// replaces the tab; the HUD and transport keys stay active either way.
// The live warning, the tempo entry and the help overlay stack on top in
// that order, help being fully modal.
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
	// The drawn position is fractional: a tab that scrolled in whole ticks
	// would still step, just more finely.
	pos := a.posF()
	posTick := int64(math.Round(pos))
	ppt := a.pxPerTick()
	phX := float32(screenW * playheadX)
	tr := a.displayed()
	nStr := len(tr.Tuning)

	tickToX := func(t int64) float32 {
		return phX + float32((float64(t)-pos)*ppt)
	}
	// Visible tick range with a little slack.
	minTick := posTick - int64(float64(phX)/ppt) - score.PPQ
	maxTick := posTick + int64(float64(screenW-float64(phX))/ppt) + score.PPQ

	// String lines.
	for si := 0; si < nStr; si++ {
		y := float32(tabTop + si*stringGap)
		vector.StrokeLine(screen, 0, y, screenW, y, 1, colString, false)
	}

	// Loop region.
	if la, lb, on := a.eng.Loop(); on {
		x0, x1 := tickToX(la), tickToX(lb)
		vector.DrawFilledRect(screen, x0, tabTop-20, x1-x0, float32(nStr-1)*stringGap+40, colLoop, false)
		vector.StrokeLine(screen, x0, tabTop-20, x0, float32(tabTop+(nStr-1)*stringGap+20), 2, colLoopEdge, false)
		vector.StrokeLine(screen, x1, tabTop-20, x1, float32(tabTop+(nStr-1)*stringGap+20), 2, colLoopEdge, false)
	}

	// Bars, beats, notes.
	waiting := a.waitingKeys()
	for bi, bar := range tr.Bars {
		barEnd := bar.Start + bar.Len()
		if barEnd < minTick || bar.Start > maxTick {
			continue
		}
		x := tickToX(bar.Start)
		vector.StrokeLine(screen, x, tabTop-14, x, float32(tabTop+(nStr-1)*stringGap+14), 1, colBarline, false)
		drawTextSmall(screen, fmt.Sprintf("%d", bi+1), float64(x)+4, tabTop-32, colHint)

		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				nx := tickToX(beat.Start)
				ny := float32(tabTop + (n.String-1)*stringGap)
				label := fmt.Sprintf("%d", n.Fret)
				if n.Tied {
					label = "~" + label
				}
				if n.Tech&score.TechDead != 0 {
					label = "x"
				}
				col, sounding := a.noteCue(beat.Start, beat.Dur, n.String, n.Inferred, pos, posTick, waiting)
				// Blank out the string line behind the number. Fret
				// numbers are mono so a 12 reads as wide as two 1s and
				// chord columns stay aligned; the box is the measured
				// advance, not a per-character guess.
				w := float32(textWMono(label))
				vector.DrawFilledRect(screen, nx-2, ny-9, w+4, 18, colNoteBG, false)
				drawTextMono(screen, label, float64(nx), float64(ny)-9, col)
				if sounding {
					vector.DrawFilledRect(screen, nx-2, ny+9, w+4, 2, colSounding, false)
				}
			}
		}
	}
	// Final barline.
	if n := len(tr.Bars); n > 0 {
		endTick := tr.Bars[n-1].Start + tr.Bars[n-1].Len()
		x := tickToX(endTick)
		vector.StrokeLine(screen, x, tabTop-14, x, float32(tabTop+(nStr-1)*stringGap+14), 2, colBarline, false)
	}

	// Playhead.
	vector.StrokeLine(screen, phX, tabTop-24, phX, float32(tabTop+(nStr-1)*stringGap+24), 2, colPlayhead, false)

}

// noteCue resolves the two independent cues a fret glyph carries: the
// colour of the number itself — inferred base, a verdict tinting over it
// once the playhead has reached the note (latest wins, loops re-judge each
// pass), an active wait point pulsing over all — and whether the
// "sounding now" underline is drawn beneath it.
//
// The sounding cue used to be one more colour in the same channel, which
// failed twice over: colSounding and colClose are indistinguishable at
// 13px, and the verdict branch won the fight, so from the second loop
// pass on the note under the playhead wore last pass's green or red
// instead of the you-are-here highlight. On its own channel the position
// cue and the verdict coexist on every pass.
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

// drawHUD paints everything around the notation: the header, the
// transport row, the track strip, the timeline and the footer.
func (a *App) drawHUD(screen *ebiten.Image, l practiceLayout) {
	drawHeader(screen, a.pieceTitle(), a.statusLine(), a.statusColour())
	a.drawTransport(screen, l, a.ptr)
	a.drawTrackStrip(screen, l, a.ptr)
	a.drawTimeline(screen, l, a.ptr)

	// Both transport-state chips are drawn here rather than inside
	// drawTab, so the tuner does not hide them: wait mode halting the
	// piece is exactly the moment a user checking their tuning needs to
	// be told why everything stopped, and COUNT-IN was already surviving
	// the tuner while WAITING was not.
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

// pieceTitle is the header's left-hand text: the piece's own title, or a
// neutral fallback for a score that carries none.
func (a *App) pieceTitle() string {
	if a.sc.Title == "" {
		return "guitarTutor"
	}
	// The budget is what the header actually has left: the full width
	// less both margins, the status line sharing the row, and a gap. A
	// fixed 520 clipped ordinary transcription titles while 400-odd
	// pixels of the same line sat empty.
	const gap = 32.0
	budget := screenW - 2*uiPadX - textW(a.statusLine()) - gap
	return truncateWScaled(a.sc.Title, budget, uiTitleScl)
}

// statusLine is the header's right-hand text: what the transport is
// doing, at what tempo, and on which pass.
func (a *App) statusLine() string {
	state := "paused"
	if a.eng.Playing() {
		state = "playing"
	}
	if in, _ := a.eng.CountingIn(); in {
		state = "count-in"
	}
	s := fmt.Sprintf("%s     %.0f BPM  (x%.2f)", state, a.eng.EffectiveBPM(), a.eng.TempoScale())
	// The pass counter counts laps of a loop, so it only appears while a
	// loop is armed; shown unconditionally it read "pass 0" for the whole
	// of every straight-through session — a lap count for a race nobody
	// was running.
	if _, _, on := a.eng.Loop(); on {
		s += fmt.Sprintf("     pass %d", a.eng.PassCount())
	}
	if a.live {
		s += "     live"
	}
	return s
}

// statusColour tints the header's status: amber while the transport is
// moving, grey at rest, so the state reads without being parsed.
func (a *App) statusColour() color.RGBA {
	if a.eng.Playing() {
		return colSounding
	}
	return colHUD
}

// drawBPMEntry paints the exact-tempo entry box: what has been typed so
// far, and the ways out of it.
func (a *App) drawBPMEntry(screen *ebiten.Image) {
	r := bpmEntryRect()
	vector.DrawFilledRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), colBG, false)
	vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), 2, colSounding, false)
	drawText(screen, "target BPM", r.x+16, r.y+12, colHUD)
	drawTextScaled(screen, a.bpmDigits+"_", r.x+16, r.y+30, 2, colNote)
	drawText(screen, "enter apply    esc or click away to cancel", r.x+16, r.y+64, colHint)
}

// drawHelp paints the full key-binding list over everything else, from
// the same table the footer hint uses.
func (a *App) drawHelp(screen *ebiten.Image) {
	drawHelpOverlay(screen, "PRACTICE KEYS", a.helpRows(), practiceHelpFootnote)
}

// Layout implements ebiten.Game with a fixed logical size.
func (a *App) Layout(int, int) (int, int) { return screenW, screenH }
