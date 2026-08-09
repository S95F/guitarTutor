package ui

// The in-app settings screen: everything that used to be a command-line
// flag. It is a vertical list of rows — capture device, playback device,
// latency calibration, SoundFont, count-in — with Up/Down moving between
// them and Left/Right or Enter adjusting the focused one. Escape returns
// errQuit, so the Shell pops back to whatever was underneath.
//
// All state and every decision live in plain methods (moveCursor, adjust,
// activate, setCountIn, startCalibration, syncSettings...) that tests
// drive directly; Draw is a projection of that state and makes no
// decisions of its own. Latency calibration blocks for seconds, so it runs
// on its own goroutine and publishes through a mutex-guarded block that
// the game loop only ever reads as a whole snapshot.

import (
	"fmt"
	"image/color"
	"strings"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// settingsDefaultRate is the sample rate used to render calibration
// offsets in milliseconds when the audio services do not report one. It
// matches the project-wide standard (internal/audio.DefaultSampleRate);
// the constant is duplicated rather than imported so the settings screen
// stays independent of the audio backend, which may be absent entirely.
const settingsDefaultRate = 48000

// settingsRow identifies one adjustable line of the screen. The rows
// actually present depend on what the services offer: with no audio
// backend, or with no devices to choose between, the device and
// calibration rows are absent and the section explains why instead.
type settingsRow int

const (
	srCapture settingsRow = iota
	srPlayback
	srCalibrate
	srSoundFont
	srCountIn
)

// calPhase is the state of the latency calibration run.
type calPhase int

const (
	calIdle    calPhase = iota // never run, or reset by a device change
	calRunning                 // a measurement is in flight
	calDone                    // the last measurement succeeded
	calFailed                  // the last measurement returned an error
)

// calSnap is an atomic view of the calibration state, taken under the
// lock and handed to the game loop as one value. Reading fields off the
// live struct would let Draw see a half-written result; everything that
// renders a calibration goes through a snapshot.
type calSnap struct {
	Phase      calPhase
	Progress   float64 // [0, 1], as reported by the callback
	Frames     int     // valid when Phase == calDone
	Confidence float64 // valid when Phase == calDone
	Err        error   // valid when Phase == calFailed
}

// settingsPather is the optional interface a Prefs implementation may
// satisfy to tell the footer where the config file lives. When Prefs does
// not implement it, the integrator can call SetConfigPath instead; with
// neither, the footer says the location is unknown.
type settingsPather interface{ Path() string }

// settingsRater is the optional interface an AudioServices implementation
// may satisfy to report its sample rate, used to convert calibration
// offsets from frames to milliseconds. Without it the screen assumes
// settingsDefaultRate, or whatever SetSampleRate was given.
type settingsRater interface{ SampleRate() int }

// Settings is the configuration screen: audio devices, latency
// calibration, SoundFont and count-in, each writing through
// Services().Prefs and saving immediately. A save error is displayed in
// the footer and never blocks the UI.
//
// The zero value is not usable; build one with NewSettings.
type Settings struct {
	sh *Shell

	rows []settingsRow
	cur  int

	// Device lists as enumerated once on construction, plus the indices
	// of the current selection within them. An index is -1 only when the
	// corresponding list is empty, in which case the device rows are
	// absent.
	capture  []DeviceOption
	playback []DeviceOption
	capIdx   int
	playIdx  int
	devErr   error

	countIn   int
	soundFont string

	// Cached stored offset for the selected pair, refreshed on a device
	// change and when a calibration finishes.
	offFrames int
	offOK     bool

	saveErr    error
	configPath string
	rate       int

	pick func(exts []string, chosen func(string))

	// mu guards everything written from outside the game loop: the
	// calibration run's published state and the file picker's mailbox.
	// The game loop reads them through calSnapshot and syncSettings.
	mu        sync.Mutex
	phase     calPhase
	progress  float64
	calFrames int
	calConf   float64
	calErr    error
	pendingSF *string
	// lastPhase is game-loop-owned: syncSettings compares it against the
	// snapshot to notice a run that has just finished.
	lastPhase calPhase
}

var _ Screen = (*Settings)(nil)

// NewSettings builds the settings screen for sh. It enumerates the audio
// devices once, up front: a settings screen that re-scans every frame
// would hammer the backend, and devices appearing mid-session is rare
// enough to be worth a trip out and back in. Services().Audio may be nil,
// in which case the device and calibration rows are omitted and the audio
// section explains that live input is unavailable.
func NewSettings(sh *Shell) *Settings {
	s := &Settings{sh: sh, capIdx: -1, playIdx: -1, rate: settingsDefaultRate}
	if p := s.prefs(); p != nil {
		s.countIn = clampCountIn(p.CountIn())
		s.soundFont = p.SoundFont()
		if pp, ok := p.(settingsPather); ok {
			s.configPath = pp.Path()
		}
	}
	if a := s.audio(); a != nil {
		if r, ok := a.(settingsRater); ok && r.SampleRate() > 0 {
			s.rate = r.SampleRate()
		}
		s.refreshDevices()
	}
	s.rebuild()
	s.refreshOffset()
	return s
}

// SetFilePicker installs the hook the SoundFont row uses to browse for a
// .sf2 file. The full file picker belongs to the browser screen, so the
// integrator wires it here: fn receives the extensions to filter on and a
// callback to invoke with the chosen path (never invoked if the user
// cancels). The callback is safe to call from any goroutine — it only
// posts to a mailbox that the game loop drains. When no picker is
// installed the SoundFont row shows the current value and offers only
// "clear".
func (s *Settings) SetFilePicker(fn func(exts []string, chosen func(string))) {
	s.pick = fn
	s.rebuild()
}

// SetConfigPath tells the footer where the config file lives, for Prefs
// implementations that do not expose a Path method of their own.
func (s *Settings) SetConfigPath(path string) { s.configPath = path }

// SetSampleRate overrides the rate used to show calibration offsets in
// milliseconds. Values below 1 are ignored.
func (s *Settings) SetSampleRate(hz int) {
	if hz > 0 {
		s.rate = hz
	}
}

func (s *Settings) prefs() Prefs {
	if s.sh == nil {
		return nil
	}
	return s.sh.Services().Prefs
}

func (s *Settings) audio() AudioServices {
	if s.sh == nil {
		return nil
	}
	return s.sh.Services().Audio
}

// refreshDevices enumerates the endpoints and resolves the stored
// preference to an index in each list. A stored ID that no longer exists
// (the interface was unplugged) falls back to the system default, then to
// the first entry — never to nothing, so the picker always shows a real
// device.
func (s *Settings) refreshDevices() {
	a := s.audio()
	if a == nil {
		return
	}
	in, out, err := a.Devices()
	s.capture, s.playback, s.devErr = in, out, err
	var wantCap, wantPlay string
	if p := s.prefs(); p != nil {
		wantCap, wantPlay = p.Devices()
	}
	s.capIdx = resolveDevice(s.capture, wantCap)
	s.playIdx = resolveDevice(s.playback, wantPlay)
}

// resolveDevice finds id in opts, falling back to the system default and
// then to the first entry. It returns -1 for an empty list.
func resolveDevice(opts []DeviceOption, id string) int {
	if len(opts) == 0 {
		return -1
	}
	if id != "" {
		for i, o := range opts {
			if o.ID == id {
				return i
			}
		}
	}
	for i, o := range opts {
		if o.Default {
			return i
		}
	}
	return 0
}

// rebuild recomputes which rows exist and clamps the cursor onto one.
func (s *Settings) rebuild() {
	s.rows = s.rows[:0]
	if s.hasDevices() {
		s.rows = append(s.rows, srCapture, srPlayback, srCalibrate)
	}
	s.rows = append(s.rows, srSoundFont, srCountIn)
	if s.cur >= len(s.rows) {
		s.cur = len(s.rows) - 1
	}
	if s.cur < 0 {
		s.cur = 0
	}
}

// hasDevices reports whether there is a real pair of pickers to show. A
// nil backend, an enumeration error, or an empty list on either side all
// mean no: the section renders an explanation instead of an empty picker.
func (s *Settings) hasDevices() bool {
	return s.audio() != nil && len(s.capture) > 0 && len(s.playback) > 0
}

// selectedIDs returns the currently selected capture and playback device
// IDs, or empty strings when there is nothing to select.
func (s *Settings) selectedIDs() (captureID, playbackID string) {
	if s.capIdx >= 0 && s.capIdx < len(s.capture) {
		captureID = s.capture[s.capIdx].ID
	}
	if s.playIdx >= 0 && s.playIdx < len(s.playback) {
		playbackID = s.playback[s.playIdx].ID
	}
	return captureID, playbackID
}

// selectedNames returns the display names of the selected pair.
func (s *Settings) selectedNames() (captureName, playbackName string) {
	if s.capIdx >= 0 && s.capIdx < len(s.capture) {
		captureName = s.capture[s.capIdx].Name
	}
	if s.playIdx >= 0 && s.playIdx < len(s.playback) {
		playbackName = s.playback[s.playIdx].Name
	}
	return captureName, playbackName
}

// refreshOffset re-reads the stored calibration for the selected pair.
func (s *Settings) refreshOffset() {
	s.offFrames, s.offOK = 0, false
	a := s.audio()
	if a == nil || !s.hasDevices() {
		return
	}
	capID, playID := s.selectedIDs()
	s.offFrames, s.offOK = a.CalibratedOffset(capID, playID)
}

// ---- navigation and adjustment -------------------------------------------

// moveCursor moves the focus by d rows, wrapping at both ends so Up from
// the first row lands on the last.
func (s *Settings) moveCursor(d int) {
	n := len(s.rows)
	if n == 0 {
		s.cur = 0
		return
	}
	s.cur = ((s.cur+d)%n + n) % n
}

// focused returns the row kind under the cursor.
func (s *Settings) focused() (settingsRow, bool) {
	if s.cur < 0 || s.cur >= len(s.rows) {
		return 0, false
	}
	return s.rows[s.cur], true
}

// adjust applies a Left (d = -1) or Right (d = +1) to the focused row.
func (s *Settings) adjust(d int) {
	r, ok := s.focused()
	if !ok {
		return
	}
	switch r {
	case srCapture:
		s.cycleCapture(d)
	case srPlayback:
		s.cyclePlayback(d)
	case srCalibrate:
		if d > 0 {
			s.startCalibration()
		}
	case srSoundFont:
		if d > 0 {
			s.chooseSoundFont()
		} else {
			s.clearSoundFont()
		}
	case srCountIn:
		s.setCountIn(s.countIn + d)
	}
}

// activate applies Enter to the focused row: the device pickers step
// forward, calibration starts, the SoundFont row browses (or clears when
// no picker is installed), and the count-in steps up, wrapping past 8
// back to 0 so Enter alone can reach every value.
func (s *Settings) activate() {
	r, ok := s.focused()
	if !ok {
		return
	}
	switch r {
	case srCountIn:
		if s.countIn >= maxCountIn {
			s.setCountIn(0)
		} else {
			s.setCountIn(s.countIn + 1)
		}
	case srSoundFont:
		if s.pick == nil {
			s.clearSoundFont()
		} else {
			s.chooseSoundFont()
		}
	default:
		s.adjust(+1)
	}
}

// cycleCapture selects the next (d = +1) or previous capture device,
// wrapping, and persists the new pair.
func (s *Settings) cycleCapture(d int) {
	if len(s.capture) == 0 {
		return
	}
	n := len(s.capture)
	s.capIdx = ((s.capIdx+d)%n + n) % n
	s.commitDevices()
}

// cyclePlayback selects the next or previous playback device, wrapping,
// and persists the new pair.
func (s *Settings) cyclePlayback(d int) {
	if len(s.playback) == 0 {
		return
	}
	n := len(s.playback)
	s.playIdx = ((s.playIdx+d)%n + n) % n
	s.commitDevices()
}

// commitDevices writes the selected pair through Prefs, saves, and
// re-reads the calibration — a different pair has a different offset, and
// any measurement shown for the old one is now stale.
func (s *Settings) commitDevices() {
	capID, playID := s.selectedIDs()
	if p := s.prefs(); p != nil {
		p.SetDevices(capID, playID)
		s.save()
	}
	s.resetCalibration()
	s.refreshOffset()
}

const maxCountIn = 8

// clampCountIn holds a count-in inside the supported 0..8 beats.
func clampCountIn(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxCountIn {
		return maxCountIn
	}
	return n
}

// setCountIn stores a count-in of n beats, clamped to 0..8, and saves.
func (s *Settings) setCountIn(n int) {
	n = clampCountIn(n)
	if n == s.countIn {
		return
	}
	s.countIn = n
	if p := s.prefs(); p != nil {
		p.SetCountIn(n)
		s.save()
	}
}

// clearSoundFont goes back to the built-in pluck synth.
func (s *Settings) clearSoundFont() {
	if s.soundFont == "" {
		return
	}
	s.applySoundFont("")
}

// chooseSoundFont asks the installed picker for a .sf2 file. It is a
// no-op with no picker installed.
func (s *Settings) chooseSoundFont() {
	if s.pick == nil {
		return
	}
	s.pick([]string{".sf2"}, func(path string) {
		// The picker may complete on another screen's frame or another
		// goroutine entirely, so the path goes to the mailbox and
		// syncSettings applies it on the game loop.
		s.mu.Lock()
		p := path
		s.pendingSF = &p
		s.mu.Unlock()
	})
}

// applySoundFont records path ("" selects the built-in pluck) and saves.
func (s *Settings) applySoundFont(path string) {
	s.soundFont = path
	if p := s.prefs(); p != nil {
		p.SetSoundFont(path)
		s.save()
	}
}

// save persists through Prefs, keeping any error for the footer. A failed
// save never blocks the UI: the change stays applied in memory and the
// user is told it did not reach disk.
func (s *Settings) save() {
	p := s.prefs()
	if p == nil {
		return
	}
	s.saveErr = p.Save()
}

// ---- calibration ---------------------------------------------------------

// startCalibration launches a measurement for the selected pair on its
// own goroutine and reports whether it started. It refuses while a run is
// already in flight — Calibrate drives the device for several seconds and
// two overlapping runs would measure each other.
func (s *Settings) startCalibration() bool {
	a := s.audio()
	if a == nil || !s.hasDevices() {
		return false
	}
	// The device pair is read here, on the game loop, so the goroutine
	// never touches the selection state.
	capID, playID := s.selectedIDs()

	s.mu.Lock()
	if s.phase == calRunning {
		s.mu.Unlock()
		return false
	}
	s.phase, s.progress = calRunning, 0
	s.calFrames, s.calConf, s.calErr = 0, 0, nil
	s.mu.Unlock()

	go func() {
		frames, conf, err := a.Calibrate(capID, playID, func(p float64) {
			if p < 0 {
				p = 0
			} else if p > 1 {
				p = 1
			}
			s.mu.Lock()
			// Only a run that is still current may move the bar.
			if s.phase == calRunning {
				s.progress = p
			}
			s.mu.Unlock()
		})
		s.mu.Lock()
		// The result lands in one critical section, so the game loop
		// never sees a frame count without its phase.
		if err != nil {
			s.phase, s.calErr = calFailed, err
		} else {
			s.phase, s.calFrames, s.calConf = calDone, frames, conf
			s.progress = 1
		}
		s.mu.Unlock()
	}()
	return true
}

// resetCalibration drops the displayed result of the last run. A run
// still in flight is left alone: it owns the device until it returns.
func (s *Settings) resetCalibration() {
	s.mu.Lock()
	if s.phase != calRunning {
		s.phase, s.progress = calIdle, 0
		s.calFrames, s.calConf, s.calErr = 0, 0, nil
		s.lastPhase = calIdle
	}
	s.mu.Unlock()
}

// calSnapshot copies the calibration state out under the lock. Every
// reader — Update, Draw, tests — goes through it, so no caller can
// observe a partially written result.
func (s *Settings) calSnapshot() calSnap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return calSnap{
		Phase:      s.phase,
		Progress:   s.progress,
		Frames:     s.calFrames,
		Confidence: s.calConf,
		Err:        s.calErr,
	}
}

// syncSettings drains what other goroutines have posted: a SoundFont
// chosen through the picker, and a calibration that has finished (whose
// result the audio services have now stored, so the offset row is
// re-read). Update calls it every frame; tests call it directly.
func (s *Settings) syncSettings() {
	s.mu.Lock()
	sf := s.pendingSF
	s.pendingSF = nil
	phase := s.phase
	s.mu.Unlock()

	if sf != nil && *sf != s.soundFont {
		s.applySoundFont(*sf)
	}
	if phase != s.lastPhase {
		s.lastPhase = phase
		if phase == calDone {
			s.refreshOffset()
		}
	}
}

// ---- same-interface steering ---------------------------------------------

// settingsGenericTokens are the words that appear in every Windows
// endpoint name and so say nothing about which physical interface a
// device belongs to. "Speakers (Realtek(R) Audio)" and "Microphone
// (Realtek(R) Audio)" are the same card; the word that proves it is
// "realtek", not "audio".
var settingsGenericTokens = map[string]bool{
	"audio": true, "device": true, "sound": true, "card": true,
	"speaker": true, "speakers": true, "headphone": true, "headphones": true,
	"headset": true, "microphone": true, "mic": true, "line": true,
	"in": true, "out": true, "input": true, "output": true,
	"playback": true, "capture": true, "record": true, "recording": true,
	"digital": true, "analog": true, "analogue": true, "stereo": true,
	"mono": true, "mix": true, "wave": true, "usb": true, "hd": true,
	"high": true, "definition": true, "default": true, "system": true,
	"primary": true, "r": true,
}

// deviceInterfaceName extracts the interface part of a Windows endpoint
// name: everything inside the first balanced parenthesised group, which
// is where the driver puts the adapter ("Microphone (Focusrite USB
// Audio)" -> "Focusrite USB Audio"). Nested parentheses are handled, so
// "Speakers (Realtek(R) Audio)" yields "Realtek(R) Audio". A name with no
// parentheses is returned unchanged.
func deviceInterfaceName(name string) string {
	i := strings.IndexByte(name, '(')
	if i < 0 {
		return name
	}
	depth := 0
	for j := i; j < len(name); j++ {
		switch name[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return name[i+1 : j]
			}
		}
	}
	return name[i+1:]
}

// deviceTokens reduces an endpoint name to a comparable normal form: the
// lowercase alphanumeric run-together of its interface part, plus the set
// of its distinctive (non-generic, non-numeric) tokens.
func deviceTokens(name string) (norm string, sig map[string]bool) {
	iface := strings.ToLower(deviceInterfaceName(name))
	sig = make(map[string]bool)
	var all strings.Builder
	var tok strings.Builder
	flush := func() {
		t := tok.String()
		tok.Reset()
		if t == "" {
			return
		}
		all.WriteString(t)
		if settingsGenericTokens[t] || strings.IndexFunc(t, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return // generic word, or a bare number like the "2-" prefix
		}
		sig[t] = true
	}
	for _, r := range iface {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			tok.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return all.String(), sig
}

// sameAudioInterface reports whether two endpoint names look like they
// belong to the same physical interface.
//
// The test is deliberately generous, because the cost of a false warning
// (a nag the user must learn to ignore) is higher than the cost of a
// missed one. Names whose interface parts normalise identically are the
// same device; otherwise they are the same if they share at least one
// distinctive token, so "Line In (Scarlett 2i2 USB)" and "Headphones
// (Scarlett 2i2 USB)" match while "Focusrite USB (Focusrite USB Audio)"
// and "Speakers (Realtek(R) Audio)" do not. When either name carries no
// distinctive token at all — "Microphone (USB Audio Device)" — there is
// nothing to match on, so the normalised comparison stands alone. An
// empty name is unknown rather than different, and never warns.
func sameAudioInterface(captureName, playbackName string) bool {
	if captureName == "" || playbackName == "" {
		return true
	}
	if captureName == playbackName {
		return true
	}
	cn, cs := deviceTokens(captureName)
	pn, ps := deviceTokens(playbackName)
	if cn == pn {
		return true
	}
	if len(cs) == 0 || len(ps) == 0 {
		return false
	}
	for t := range cs {
		if ps[t] {
			return true
		}
	}
	return false
}

// splitDeviceWarning returns the warning to show when the selected
// capture and playback devices are not the same physical interface, and
// whether there is one. Independent sample clocks drift apart over a
// practice session by more than a static calibration offset can absorb,
// which quietly degrades scoring.
func (s *Settings) splitDeviceWarning() (string, bool) {
	if !s.hasDevices() {
		return "", false
	}
	capName, playName := s.selectedNames()
	if sameAudioInterface(capName, playName) {
		return "", false
	}
	return "WARNING: capture and playback look like different interfaces. Their clocks drift" +
		" apart over a session, which one calibration cannot correct, and scoring suffers." +
		" Prefer the same interface for both.", true
}

// ---- text projections ----------------------------------------------------

// framesText renders an offset in frames and the milliseconds it works
// out to at the current sample rate.
func (s *Settings) framesText(frames int) string {
	rate := s.rate
	if rate <= 0 {
		rate = settingsDefaultRate
	}
	return fmt.Sprintf("%d frames (%.1f ms)", frames, float64(frames)*1000/float64(rate))
}

// deviceText renders one picker's value: position in the list, name, and
// the system-default and selected markers.
func deviceText(opts []DeviceOption, idx int) string {
	if idx < 0 || idx >= len(opts) {
		return "none available"
	}
	o := opts[idx]
	s := fmt.Sprintf("[%d/%d] %s", idx+1, len(opts), o.Name)
	if o.Default {
		s += "  (system default)"
	}
	return s + "  <- selected"
}

// audioUnavailableText explains why there is nothing to pick between.
func (s *Settings) audioUnavailableText() []string {
	if s.audio() == nil {
		return []string{
			"Live input is unavailable in this build or on this machine:",
			"no duplex audio backend was compiled in or could be opened.",
			"Playback and practice still work; scoring your playing does not.",
		}
	}
	if s.devErr != nil {
		return []string{
			"Could not list audio devices:",
			"  " + s.devErr.Error(),
		}
	}
	switch {
	case len(s.capture) == 0 && len(s.playback) == 0:
		return []string{"The audio backend reported no capture or playback devices."}
	case len(s.capture) == 0:
		return []string{"The audio backend reported no capture devices, so live input is unavailable."}
	default:
		return []string{"The audio backend reported no playback devices."}
	}
}

// soundFontText renders the SoundFont row's value.
func (s *Settings) soundFontText() string {
	if s.soundFont == "" {
		return "built-in pluck"
	}
	return s.soundFont
}

// calibrationText renders the calibration row's value and the color to
// draw it in, from a snapshot taken by the caller.
func (s *Settings) calibrationText(sn calSnap) (string, color.RGBA) {
	switch sn.Phase {
	case calRunning:
		return fmt.Sprintf("measuring... %3.0f%%", sn.Progress*100), colSounding
	case calDone:
		return fmt.Sprintf("measured %s, confidence %.0f%%", s.framesText(sn.Frames), sn.Confidence*100), colHit
	case calFailed:
		return "failed: " + sn.Err.Error(), colMiss
	}
	if s.offOK {
		return "stored " + s.framesText(s.offFrames), colHUD
	}
	return "not measured for this pair", colClose
}

// configText renders the footer's config-file location.
func (s *Settings) configText() string {
	if s.configPath == "" {
		return "config file: location unknown"
	}
	return "config file: " + s.configPath
}

// ---- Screen --------------------------------------------------------------

// Update handles navigation and adjustment. Escape returns errQuit, which
// tells the Shell this screen is finished.
func (s *Settings) Update() error {
	s.syncSettings()
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		return errQuit
	case inpututil.IsKeyJustPressed(ebiten.KeyUp):
		s.moveCursor(-1)
	case inpututil.IsKeyJustPressed(ebiten.KeyDown):
		s.moveCursor(+1)
	case inpututil.IsKeyJustPressed(ebiten.KeyLeft):
		s.adjust(-1)
	case inpututil.IsKeyJustPressed(ebiten.KeyRight):
		s.adjust(+1)
	case inpututil.IsKeyJustPressed(ebiten.KeyEnter), inpututil.IsKeyJustPressed(ebiten.KeyKPEnter):
		s.activate()
	}
	return nil
}

const (
	settingsLeft   = 32.0  // left margin for labels
	settingsValueX = 300.0 // column where a row's value starts
	settingsLineH  = 18.0  // one line of body text
	settingsRowH   = 26.0  // one focusable row
)

// Draw paints the screen. It is a pure projection of the state the
// methods above maintain: it makes no decisions and mutates nothing
// except a local layout cursor.
func (s *Settings) Draw(dst *ebiten.Image) {
	dst.Fill(colBG)
	drawTextScaled(dst, "SETTINGS", settingsLeft, 18, 2, colNote)

	back := "esc back"
	if s.sh != nil && s.sh.Depth() <= 1 {
		back = "esc quit"
	}
	help := "up/dn select   left/right adjust   enter action   " + back
	drawText(dst, help, settingsLeft, screenH-24, colBarline)

	y := 64.0
	row := 0 // index into s.rows, advanced as each focusable row is drawn

	// --- audio devices ---
	backend := "audio backend: none"
	if a := s.audio(); a != nil {
		backend = "audio backend: " + a.BackendName()
	}
	drawText(dst, backend, settingsLeft, y, colBarline)
	y += settingsLineH + 6
	s.sectionHead(dst, &y, "AUDIO DEVICES")

	if s.hasDevices() {
		s.drawRow(dst, &y, row, "capture", deviceText(s.capture, s.capIdx), colHUD)
		row++
		s.drawRow(dst, &y, row, "playback", deviceText(s.playback, s.playIdx), colHUD)
		row++
		if w, ok := s.splitDeviceWarning(); ok {
			for _, l := range wrapText(w, 132) {
				drawText(dst, l, settingsValueX, y, colClose)
				y += settingsLineH
			}
			y += 4
		}
	} else {
		for _, l := range s.audioUnavailableText() {
			drawText(dst, l, settingsValueX, y, colClose)
			y += settingsLineH
		}
		y += 8
	}

	// --- latency calibration ---
	s.sectionHead(dst, &y, "LATENCY CALIBRATION")
	if s.hasDevices() {
		sn := s.calSnapshot()
		txt, col := s.calibrationText(sn)
		s.drawRow(dst, &y, row, "round-trip offset", txt, col)
		row++
		if sn.Phase == calRunning {
			const barW, barH = 320, 8
			x0 := float32(settingsValueX)
			vector.StrokeRect(dst, x0, float32(y), barW, barH, 1, colString, false)
			vector.DrawFilledRect(dst, x0+1, float32(y)+1, (barW-2)*float32(sn.Progress), barH-2, colSounding, false)
			y += float64(barH) + 10
		} else {
			drawText(dst, "enter: calibrate now (takes a few seconds)", settingsValueX, y, colBarline)
			y += settingsLineH + 2
		}
	} else {
		drawText(dst, "unavailable without a capture and playback device", settingsValueX, y, colBarline)
		y += settingsLineH + 6
	}

	// --- soundfont ---
	s.sectionHead(dst, &y, "INSTRUMENT")
	s.drawRow(dst, &y, row, "soundfont", s.soundFontText(), colHUD)
	row++
	hint := "left: clear to built-in"
	if s.pick != nil {
		hint = "right/enter: browse for .sf2    left: clear to built-in"
	}
	drawText(dst, hint, settingsValueX, y, colBarline)
	y += settingsLineH + 6

	// --- count-in ---
	s.sectionHead(dst, &y, "PRACTICE")
	s.drawRow(dst, &y, row, "count-in beats", fmt.Sprintf("%d  (0-%d)", s.countIn, maxCountIn), colHUD)

	// --- footer ---
	fy := screenH - 68.0
	drawText(dst, s.configText(), settingsLeft, fy, colBarline)
	if s.saveErr != nil {
		drawText(dst, "SAVE FAILED: "+s.saveErr.Error(), settingsLeft, fy+settingsLineH, colMiss)
	} else {
		drawText(dst, "changes are saved as you make them", settingsLeft, fy+settingsLineH, colBarline)
	}
}

// sectionHead draws a section title and advances the layout cursor.
func (s *Settings) sectionHead(dst *ebiten.Image, y *float64, title string) {
	drawText(dst, title, settingsLeft, *y, colInferred)
	vector.StrokeLine(dst, settingsLeft, float32(*y)+16, screenW-settingsLeft, float32(*y)+16, 1, colBarline, false)
	*y += settingsLineH + 8
}

// drawRow draws one focusable row and advances the layout cursor. The
// focused row gets a highlight band and a caret.
func (s *Settings) drawRow(dst *ebiten.Image, y *float64, idx int, label, value string, col color.RGBA) {
	if idx == s.cur {
		vector.DrawFilledRect(dst, settingsLeft-8, float32(*y)-5, screenW-2*settingsLeft+16, settingsRowH-6, colLoop, false)
		drawText(dst, ">", settingsLeft-8, *y, colSounding)
		col = colNote
	}
	drawText(dst, label, settingsLeft+8, *y, colHUD)
	drawText(dst, value, settingsValueX, *y, col)
	*y += settingsRowH
}

// wrapText breaks s into lines of at most width characters, on spaces.
// basicfont is fixed-width, so a character count is a pixel count.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			out = append(out, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(out, line)
}
