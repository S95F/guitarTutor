// Package ui is the Ebitengine practice view: a scrolling tablature with a
// fixed playhead, driven entirely by the engine's clock. The UI never keeps
// its own notion of time — every frame it asks the engine where it is
// (PosTick) and draws accordingly, so display and audio cannot drift.
package ui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font/basicfont"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/score"
)

const (
	screenW = 1280
	screenH = 720

	playheadX  = 0.3 // fraction of width where "now" sits
	stringGap  = 26
	tabTop     = 160
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
	colLoop     = color.RGBA{60, 160, 90, 60}
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
	colHelpDim = color.RGBA{10, 10, 14, 240} // help overlay backdrop
	colWarnBG  = color.RGBA{58, 18, 18, 240} // live-warning banner fill
)

var face = text.NewGoXFace(basicfont.Face7x13)

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
	// length, countInOn the C toggle, and countInStale records that the
	// user changed it but no applier could push it into the running
	// engine — the HUD then says so rather than lying.
	countInBeats int
	countInOn    bool
	countInStale bool
	countInApply func(beats int) bool

	// helpOpen is the ?/F1 key-binding overlay; while it is up Update
	// routes every key to it so nothing acts on the piece behind it.
	helpOpen bool

	// quitAll, when the integrator has wired it, is what Q does: leave
	// the whole application rather than just this piece. nil keeps the
	// standalone behaviour of quitting the process.
	quitAll func()

	// liveUI carries the Phase 2 live-feedback state and feed mailbox
	// (live.go). Its zero value is fully inert: no feeds, Phase 1
	// behavior.
	liveUI
}

// New builds the practice view. track is the index into sc.Tracks to
// display as tablature (play/mute always applies to all tracks).
func New(eng *engine.Engine, sc *score.Score, track int) *App {
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

	// Modal layers take the whole keyboard. The BPM entry must swallow
	// digits so they cannot double as track mute/solo keys, and the help
	// overlay must not let a stray key act on the piece behind it.
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

	eng, bars := a.eng, a.displayed().Bars

	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		// Finish this screen only: back to the song list.
		return errQuit
	case inpututil.IsKeyJustPressed(ebiten.KeyQ):
		return a.quitApp()
	case inpututil.IsKeyJustPressed(ebiten.KeyF1),
		inpututil.IsKeyJustPressed(ebiten.KeySlash) && a.shiftHeld():
		a.openHelp()
	case inpututil.IsKeyJustPressed(ebiten.KeySpace):
		if eng.Playing() {
			eng.Pause()
		} else {
			eng.Play()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyHome):
		eng.SeekTick(0)
	case inpututil.IsKeyJustPressed(ebiten.KeyLeft):
		if i := a.barAt(eng.PosTick()); i >= 0 {
			// To the start of this bar; if already near it, the bar before.
			b := bars[i]
			if eng.PosTick()-b.Start < b.Len()/8 && i > 0 {
				eng.SeekTick(bars[i-1].Start)
			} else {
				eng.SeekTick(b.Start)
			}
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyRight):
		if i := a.barAt(eng.PosTick()); i+1 < len(bars) {
			eng.SeekTick(bars[i+1].Start)
		}
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
	case inpututil.IsKeyJustPressed(ebiten.KeyT):
		a.tunerView = !a.tunerView
	case inpututil.IsKeyJustPressed(ebiten.KeyW):
		if a.waitCtl {
			a.toggleWait()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyEqual), inpututil.IsKeyJustPressed(ebiten.KeyKPAdd):
		if a.zoom < 4 {
			a.zoom *= 1.25
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyMinus), inpututil.IsKeyJustPressed(ebiten.KeyKPSubtract):
		if a.zoom > 0.3 {
			a.zoom /= 1.25
		}
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

// openHelp raises the key-binding overlay.
func (a *App) openHelp() { a.helpOpen = true }

// closeHelp lowers the key-binding overlay.
func (a *App) closeHelp() { a.helpOpen = false }

// updateHelp runs the key-binding overlay: escape, F1 or ? closes it and
// nothing else reaches the piece. Escape closing the overlay rather than
// the piece is why the overlay is handled before the main bindings.
func (a *App) updateHelp() {
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape),
		inpututil.IsKeyJustPressed(ebiten.KeyF1),
		inpututil.IsKeyJustPressed(ebiten.KeySlash):
		a.closeHelp()
	}
}

// loopSetA (the A key) sets the loop start to the current bar, keeping the
// end if a loop is already set past it. No-op when the displayed track has
// no bars (barAt returns -1).
func (a *App) loopSetA() {
	i := a.barAt(a.eng.PosTick())
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
	i := a.barAt(a.eng.PosTick())
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
	}
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
	us := a.sc.Tempos.At(a.eng.PosTick())
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

// trackMarks is the two-character mute/solo badge the HUD shows for track
// i: 'M' when the user muted it, 'm' when it is muted only because another
// track is soloed, and 'S' on the soloed track itself.
func (a *App) trackMarks(i int) string {
	a.ensureMuteState()
	if i < 0 || i >= len(a.userMuted) {
		return "  "
	}
	m := byte(' ')
	switch {
	case a.userMuted[i]:
		m = 'M'
	case a.solo > 0 && a.solo != i+1:
		m = 'm'
	}
	s := byte(' ')
	if a.solo == i+1 {
		s = 'S'
	}
	return string([]byte{m, s})
}

// --- Count-in ---

// SetCountIn records the count-in the engine was constructed with, so the
// C key toggles from the right place and the HUD reports the truth. Zero
// or less means the piece opened without one: C then turns on a four-beat
// count-in.
func (a *App) SetCountIn(beats int) {
	if beats > 0 {
		a.countInBeats, a.countInOn = beats, true
	} else {
		a.countInBeats, a.countInOn = defaultCountInBeats, false
	}
	a.countInStale = false
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

// toggleCountIn (the C key) flips the count-in for the next Play.
func (a *App) toggleCountIn() {
	a.countInOn = !a.countInOn
	applied := a.countInApply != nil && a.countInApply(a.CountInBeats())
	a.countInStale = !applied
}

// countInLabel is the HUD's count-in state, including the honest caveat
// when the change could not reach the running engine.
func (a *App) countInLabel() string {
	s := "count-in off"
	if b := a.CountInBeats(); b > 0 {
		s = fmt.Sprintf("count-in %d", b)
	}
	if a.countInStale {
		s += " (on re-open)"
	}
	return s
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
}

// enabled resolves the optional gate.
func (b practiceBinding) enabled(a *App) bool { return b.Enabled == nil || b.Enabled(a) }

// practiceBindings is the single source of truth for the practice view's
// keyboard. Order matters: it is the order of both the hint line and the
// help overlay.
var practiceBindings = []practiceBinding{
	{Group: "transport", Keys: "space", Hint: "space play/pause", Desc: "Start or pause playback"},
	{Group: "transport", Keys: "left / right", Hint: "arrows seek", Desc: "Jump to the previous / next bar"},
	{Group: "transport", Keys: "home", Desc: "Jump back to the first bar"},

	{Group: "tempo", Keys: "up / down", Hint: "up/dn tempo", Desc: "Practice speed up / down by 5%"},
	{Group: "tempo", Keys: "shift+B", Hint: "shift+B bpm", Desc: "Type an exact target BPM"},
	{Group: "tempo", Keys: "R", Desc: "Ramp the speed up 5% after each loop pass"},
	{Group: "tempo", Keys: "C", Desc: "Count-in on / off for the next play"},

	{Group: "loop", Keys: "A", Hint: "A/B loop", Desc: "Set the loop start at the current bar"},
	{Group: "loop", Keys: "B", Desc: "Set the loop end at the current bar"},
	{Group: "loop", Keys: "L", Desc: "Clear the loop"},

	{Group: "tracks", Keys: "1..9", Hint: "1-9 mute", Desc: "Mute / unmute a track"},
	{Group: "tracks", Keys: "shift+1..9", Desc: "Solo a track; press again to release"},

	{Group: "practice", Keys: "M", Hint: "M click", Desc: "Metronome click on / off"},
	{Group: "practice", Keys: "T", Hint: "T tuner", Desc: "Tuner overlay"},
	{Group: "practice", Keys: "W", Hint: "W wait", Desc: "Wait at each note until you play it",
		Enabled: func(a *App) bool { return a.waitCtl }},

	{Group: "view", Keys: "+ / -", Desc: "Zoom the tab in / out"},

	{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
	{Group: "session", Keys: "D", Desc: "Dismiss the warning banner"},
	{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Leave this piece and go back"},
	{Group: "session", Keys: "Q", Hint: "Q quit", Desc: "Quit guitarTutor"},
}

// A practiceHelpGroup is one section of the help overlay.
type practiceHelpGroup struct {
	Name string
	Rows []practiceBinding
}

// helpGroups slices the binding table into the overlay's sections, in
// table order.
func (a *App) helpGroups() []practiceHelpGroup {
	var gs []practiceHelpGroup
	for _, b := range practiceBindings {
		if n := len(gs); n > 0 && gs[n-1].Name == b.Group {
			gs[n-1].Rows = append(gs[n-1].Rows, b)
			continue
		}
		gs = append(gs, practiceHelpGroup{Name: b.Group, Rows: []practiceBinding{b}})
	}
	return gs
}

// hintLine is the one-line HUD summary, built from the same table as the
// overlay and dropping bindings that are not currently available.
func (a *App) hintLine() string {
	parts := make([]string, 0, len(practiceBindings))
	for _, b := range practiceBindings {
		if b.Hint == "" || !b.enabled(a) {
			continue
		}
		parts = append(parts, b.Hint)
	}
	return strings.Join(parts, "  ")
}

// Draw renders the frame from engine state. The tuner overlay (T)
// replaces the tab; the HUD and transport keys stay active either way.
// The live warning, the tempo entry and the help overlay stack on top in
// that order, help being fully modal.
func (a *App) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)
	if a.tunerView {
		a.drawTuner(screen)
	} else {
		a.drawTab(screen)
	}
	a.drawHUD(screen)
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
	pos := a.eng.PosTick()
	ppt := a.pxPerTick()
	phX := float32(screenW * playheadX)
	tr := a.displayed()
	nStr := len(tr.Tuning)

	tickToX := func(t int64) float32 {
		return phX + float32(float64(t-pos)*ppt)
	}
	// Visible tick range with a little slack.
	minTick := pos - int64(float64(phX)/ppt) - score.PPQ
	maxTick := pos + int64(float64(screenW-float64(phX))/ppt) + score.PPQ

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
		drawText(screen, fmt.Sprintf("%d", bi+1), float64(x)+3, tabTop-32, colBarline)

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
				// Sounding/inferred are the base; a verdict tints
				// over them once the playhead has reached the
				// note (latest wins — loops re-judge each pass);
				// an active wait point pulses over all.
				col := colNote
				if n.Inferred {
					col = colInferred
				}
				if beat.Start <= pos && pos < beat.Start+beat.Dur {
					col = colSounding
				}
				if v, ok := a.verdictAt(beat.Start, n.String, pos); ok {
					col = verdictColor(v)
				}
				if waiting[noteKey{beat.Start, n.String}] {
					col = a.pulseCol()
				}
				// Blank out the string line behind the number.
				w := float32(8 * len(label))
				vector.DrawFilledRect(screen, nx-2, ny-7, w+3, 14, colNoteBG, false)
				drawText(screen, label, float64(nx), float64(ny)-7, col)
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

	if a.eng.Waiting() {
		drawText(screen, "WAITING", screenW*playheadX-24, tabTop-48, a.pulseCol())
	}
}

func (a *App) drawHUD(screen *ebiten.Image) {
	state := "paused"
	if a.eng.Playing() {
		state = "playing"
	}
	if in, left := a.eng.CountingIn(); in {
		drawText(screen, fmt.Sprintf("COUNT-IN %d", left), screenW*playheadX-30, tabTop-64, colCountIn)
		state = "count-in"
	}
	line1 := fmt.Sprintf("%s | %.0f BPM (x%.2f) | pass %d | %s",
		state, a.eng.EffectiveBPM(), a.eng.TempoScale(), a.eng.PassCount(), a.countInLabel())
	if _, _, on := a.eng.Loop(); on {
		line1 += " | LOOP"
	}
	if a.metronome {
		line1 += " | click"
	}
	if a.ramp {
		line1 += " | ramp"
	}
	if a.wait {
		line1 += " | wait"
	}
	if a.live {
		line1 += " | live"
	}
	drawText(screen, line1, 16, 16, colHUD)

	if a.live {
		a.drawLiveHUD(screen)
		a.drawLegend(screen)
	}

	for i, t := range a.sc.Tracks {
		name := t.Name
		if name == "" {
			name = fmt.Sprintf("track %d", i+1)
		}
		cur := " "
		if i == a.track {
			cur = ">"
		}
		col := colHUD
		if a.mutedAudibly(i) {
			col = colString
		}
		drawText(screen, fmt.Sprintf("%s%d %s [%s]", cur, i+1, name, a.trackMarks(i)), 16, float64(40+16*i), col)
	}

	if msg := a.bpmMessage(); msg != "" {
		drawText(screen, msg, (screenW-float64(7*len(msg)))/2, screenH-70, colSounding)
	}

	hint := a.hintLine()
	drawText(screen, hint, 16, screenH-24, colBarline)
}

// drawBPMEntry paints the exact-tempo entry box: what has been typed so
// far, and the two keys that end it.
func (a *App) drawBPMEntry(screen *ebiten.Image) {
	const w, h = 300, 80
	x := float32((screenW - w) / 2)
	y := float32(tabTop - 130)
	vector.DrawFilledRect(screen, x, y, w, h, colBG, false)
	vector.StrokeRect(screen, x, y, w, h, 2, colSounding, false)
	drawText(screen, "target BPM", float64(x)+16, float64(y)+12, colHUD)
	drawTextScaled(screen, a.bpmDigits+"_", float64(x)+16, float64(y)+30, 2, colNote)
	drawText(screen, "enter apply   esc cancel", float64(x)+16, float64(y)+60, colBarline)
}

// drawHelp paints the full key-binding list over everything else. It is
// generated from practiceBindings, the same table the HUD hint line uses;
// bindings that are unavailable right now are greyed rather than hidden,
// so the key is never a mystery.
func (a *App) drawHelp(screen *ebiten.Image) {
	vector.DrawFilledRect(screen, 0, 0, screenW, screenH, colHelpDim, false)
	drawTextScaled(screen, "KEY BINDINGS", 220, 24, 2, colNote)
	y := 68.0
	for _, g := range a.helpGroups() {
		drawText(screen, strings.ToUpper(g.Name), 220, y, colSounding)
		y += 18
		for _, b := range g.Rows {
			col, desc := colNote, b.Desc
			if !b.enabled(a) {
				col, desc = colBarline, b.Desc+"  (not available now)"
			}
			drawText(screen, b.Keys, 240, y, col)
			drawText(screen, desc, 420, y, col)
			y += 16
		}
		y += 8
	}
	drawText(screen, "track marks:  M muted    m muted by another track's solo    S soloed", 220, y+4, colHUD)
	drawText(screen, "esc, F1 or ? closes this", 220, float64(screenH)-40, colHUD)
}

func drawText(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	op := &text.DrawOptions{}
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(col)
	text.Draw(dst, s, face, op)
}

// Layout implements ebiten.Game with a fixed logical size.
func (a *App) Layout(int, int) (int, int) { return screenW, screenH }
