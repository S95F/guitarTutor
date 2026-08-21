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
// decisions of its own.
//
// Latency calibration blocks for seconds, so it runs on its own goroutine.
// That goroutine never holds a reference to the screen: it writes into a
// calRun, a small mutex-guarded value it shares with the Settings that
// started it. Close (called by the Shell when the screen is popped) cancels
// the run and marks the calRun abandoned, so a measurement that outlives
// its screen publishes nowhere and, on a backend that honours the context,
// gives the audio device back before the next screen asks for it.

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"sync"
	"time"

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
	srSyncTrim
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
//
// CaptureID and PlaybackID are the pair the run was started for. A result
// describes only the pair it was measured on, so the projections compare
// them against the current selection and refuse to show a measurement the
// user has since navigated away from.
type calSnap struct {
	Phase      calPhase
	Progress   float64 // [0, 1], as reported by the callback
	Frames     int     // valid when Phase == calDone
	Confidence float64 // valid when Phase == calDone
	Err        error   // valid when Phase == calFailed
	CaptureID  string  // the pair this run was started for
	PlaybackID string
}

// calRun is the mutable state of one calibration run. It is the only thing
// the calibration goroutine writes to: the goroutine captures a *calRun and
// never a *Settings, so a run that outlives its screen cannot touch it.
//
// capID, playID, cancel and done are set before the goroutine starts and
// never written again, so they are read without the lock; everything below
// mu is written by the goroutine and read by the game loop.
type calRun struct {
	capID  string
	playID string
	cancel context.CancelFunc
	done   chan struct{} // closed when the goroutine returns

	mu        sync.Mutex
	abandoned bool // the screen was closed; publish nothing more
	phase     calPhase
	progress  float64
	frames    int
	conf      float64
	err       error
}

// setProgress moves the bar, clamped, for as long as the run is both
// current and still wanted.
func (r *calRun) setProgress(p float64) {
	if p < 0 {
		p = 0
	} else if p > 1 {
		p = 1
	}
	r.mu.Lock()
	if !r.abandoned && r.phase == calRunning {
		r.progress = p
	}
	r.mu.Unlock()
}

// finish publishes the outcome in one critical section, so the game loop
// never sees a frame count without its phase. An abandoned run publishes
// nothing: its screen is gone and the result belongs to nobody.
func (r *calRun) finish(frames int, conf float64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.abandoned {
		return
	}
	if err != nil {
		r.phase, r.err = calFailed, err
		return
	}
	r.phase, r.frames, r.conf, r.progress = calDone, frames, conf, 1
}

// abandon detaches the run from its screen and asks the backend to stop.
// It never blocks: the caller waits on done separately, so a progress
// callback contending for mu cannot deadlock the game loop.
func (r *calRun) abandon() {
	r.mu.Lock()
	r.abandoned = true
	r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

// abandonedNow reports whether the run has been detached from its screen.
func (r *calRun) abandonedNow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abandoned
}

// snapshot copies the published state out under the lock.
func (r *calRun) snapshot() calSnap {
	r.mu.Lock()
	defer r.mu.Unlock()
	return calSnap{
		Phase:      r.phase,
		Progress:   r.progress,
		Frames:     r.frames,
		Confidence: r.conf,
		Err:        r.err,
		CaptureID:  r.capID,
		PlaybackID: r.playID,
	}
}

// running reports whether the measurement is still in flight.
func (r *calRun) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase == calRunning
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

// settingsCanceller is the optional interface an AudioServices
// implementation may satisfy to accept a cancellation context for a
// calibration run. A backend that implements it gives the capture and
// playback devices back as soon as the screen is closed, instead of
// holding them for the rest of the measurement; one that does not is still
// detached from the screen by Close, it just keeps the device until its own
// timeout expires.
type settingsCanceller interface {
	CalibrateContext(ctx context.Context, captureID, playbackID string, progress func(float64)) (frames int, confidence float64, err error)
}

// calibrateWith runs a measurement, preferring the cancellable entry point
// when the backend offers one.
func calibrateWith(ctx context.Context, a AudioServices, captureID, playbackID string, progress func(float64)) (int, float64, error) {
	if c, ok := a.(settingsCanceller); ok {
		return c.CalibrateContext(ctx, captureID, playbackID, progress)
	}
	return a.Calibrate(captureID, playbackID, progress)
}

// Settings is the configuration screen: audio devices, latency
// calibration, SoundFont and count-in, each writing through
// Services().Prefs and saving immediately. A save error is displayed in
// the footer and never blocks the UI.
//
// The screen owns at most one calibration run at a time, and gives it up
// in Close, which the Shell calls when the screen is popped.
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
	// capMissing / playMissing record that the SAVED device was not among
	// the enumerated ones, so the index above is a fallback. The rows then
	// say so: showing the fallback as "<- selected" with no caveat let the
	// user conclude their unplugged interface was fine (audit C3). The
	// saved ID is deliberately left in the config, so plugging the device
	// back in restores it.
	capMissing  bool
	playMissing bool
	// capChosen / playChosen record that Prefs holds EXACTLY the device
	// shown. Without them the rows could not tell a device the user picked
	// from the system default they were handed on first run, and claimed
	// both were "<- selected" — see deviceText.
	capChosen  bool
	playChosen bool

	countIn   int
	syncTrim  int // milliseconds; see appconfig.Config.SyncTrimMS
	soundFont string

	// Cached stored offset for the selected pair, refreshed on a device
	// change and when a calibration finishes.
	offFrames int
	offOK     bool

	saveErr    error
	configPath string
	rate       int

	pick func(exts []string, chosen func(string))

	// onClose runs when the Shell pops this screen. It is how whatever
	// opened settings finds out that the user is done with them —
	// notably the practice view, which must then decide whether the
	// piece it is showing was built from settings that have since
	// changed. Settings itself cannot answer that: it does not know what
	// a piece was opened with.
	onClose func()

	// run is the calibration this screen started, or nil. The pointer is
	// game-loop-owned (startCalibration, resetCalibration and Close are the
	// only writers, and all three run on the loop); the value it points at
	// carries its own lock, because the calibration goroutine writes to it.
	run *calRun
	// lastPhase is game-loop-owned: syncSettings compares it against the
	// snapshot to notice a run that has just finished.
	lastPhase calPhase
	// notice explains a refused action — a second calibration, or a device
	// or count-in change during a run — so a locked key is visibly ignored
	// rather than silently dropped.
	notice string

	// helpOpen is the ?/F1 key-binding overlay; while it is up nothing
	// else on the screen reacts.
	helpOpen bool
	// sfBusy is true while a SoundFont dialog is up, so repeated
	// activation cannot stack dialogs. Cleared when the mailbox drains
	// an outcome — the picker contract is that chosen is ALWAYS called,
	// with "" on cancel.
	sfBusy bool
	// ptr is this frame's mouse, sampled once by Update so every decision
	// below takes it as a value and can be driven from a test.
	ptr pointer

	// mu guards the file picker's mailbox, which the picker's completion
	// callback may write from any goroutine.
	mu        sync.Mutex
	pendingSF *string
}

var _ Screen = (*Settings)(nil)

// The Shell's teardown hook probes a popped screen for this interface, and
// Settings answers it: see Close.
var _ interface{ Close() } = (*Settings)(nil)

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
		if tp, ok := p.(settingsSyncTrimmer); ok {
			s.syncTrim = clampSyncTrim(tp.SyncTrim())
		}
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
	s.focusFirstUnconfigured()
	return s
}

// focusFirstUnconfigured puts the cursor on the first setting that still
// needs attention: a capture device that has never been chosen, then a
// device pair that has never been measured. It is what makes the start
// screen's getting-started checklist land somewhere useful — the step
// says "choose your audio interface", and the screen it opens is already
// sitting on that row. With everything configured the cursor stays at the
// top, which is where someone who came to change something expects it.
func (s *Settings) focusFirstUnconfigured() {
	if !s.hasDevices() {
		return
	}
	capID, playID := s.selectedIDs()
	var wantCap string
	if p := s.prefs(); p != nil {
		wantCap, _ = p.Devices()
	}
	if wantCap == "" {
		if i := s.rowIndex(srCapture); i >= 0 {
			s.cur = i
		}
		return
	}
	if _, ok := s.audio().CalibratedOffset(capID, playID); !ok {
		if i := s.rowIndex(srCalibrate); i >= 0 {
			s.cur = i
		}
	}
}

// SetFilePicker installs the hook the SoundFont row uses to browse for a
// .sf2 file. The integrator wires it to the OS file dialog, which blocks
// while the user browses, so fn must return immediately (start a
// goroutine) and call chosen with the picked path — with "" on a cancel
// or failure, ALWAYS: the callback is also how the row learns the dialog
// is gone and re-arms itself. It is safe to call from any goroutine: it
// only posts to a mailbox that the game loop drains. When no picker is
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
	s.refreshMissing()
}

// refreshMissing recomputes whether the shown selection is a fallback for
// a saved device that is not connected. It runs on every device commit as
// well as at construction: the flags describe the CURRENT saved IDs, and
// an early version computed them once, so the "not connected" note kept
// showing after the user explicitly selected a connected device — at
// which point both of the note's claims were false (verification
// follow-up to C3).
func (s *Settings) refreshMissing() {
	var wantCap, wantPlay string
	if p := s.prefs(); p != nil {
		wantCap, wantPlay = p.Devices()
	}
	s.capChosen = deviceIsSaved(s.capture, s.capIdx, wantCap)
	s.playChosen = deviceIsSaved(s.playback, s.playIdx, wantPlay)
	s.capMissing = wantCap != "" && !s.capChosen && s.capIdx >= 0 && s.capIdx < len(s.capture)
	s.playMissing = wantPlay != "" && !s.playChosen && s.playIdx >= 0 && s.playIdx < len(s.playback)
}

// deviceIsSaved reports whether the entry the picker is showing is the one
// Prefs holds — as opposed to a fallback for a saved device that is not
// connected, or the system default nobody has chosen yet.
func deviceIsSaved(opts []DeviceOption, idx int, saved string) bool {
	return saved != "" && idx >= 0 && idx < len(opts) && opts[idx].ID == saved
}

// deviceState is what a picker may honestly say about the entry it shows.
type deviceState int

const (
	devChosen   deviceState = iota // Prefs holds this exact device
	devFallback                    // Prefs holds another device, not connected
	devUnchosen                    // Prefs holds nothing at all
)

// devState reads the state off the pair of flags refreshMissing keeps.
func devState(chosen, missing bool) deviceState {
	switch {
	case chosen:
		return devChosen
	case missing:
		return devFallback
	}
	return devUnchosen
}

// deviceStateOf answers for one of the two picker rows; any other row is
// not a device and never unchosen.
func (s *Settings) deviceStateOf(r settingsRow) deviceState {
	switch r {
	case srCapture:
		return devState(s.capChosen, s.capMissing)
	case srPlayback:
		return devState(s.playChosen, s.playMissing)
	}
	return devChosen
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
	if s.hasSyncTrim() {
		s.rows = append(s.rows, srSyncTrim)
	}
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

// calibrating reports whether a measurement this screen started is still
// in flight.
func (s *Settings) calibrating() bool { return s.run != nil && s.run.running() }

// calLockedNotice is what the screen says when it ignores a key because a
// measurement is running.
const calLockedNotice = "a calibration is running: the device pair and count-in are locked until it finishes" +
	" (changing them mid-run would measure one pair and store it against another)"

// calBusyNotice is what the screen says when a second calibration is asked
// for while one is still in flight.
const calBusyNotice = "a calibration is already running: two overlapping runs would measure each other"

// lockedDuringRun reports whether row r must ignore input right now, and
// posts the explanation when it does. The device pair and the count-in are
// the rows a running measurement depends on: the pair is what is being
// measured, and the count-in writes and saves preferences the run's own
// store-and-save will race with.
func (s *Settings) lockedDuringRun(r settingsRow) bool {
	if !s.calibrating() {
		return false
	}
	switch r {
	case srCapture, srPlayback, srCountIn, srSyncTrim:
		s.notice = calLockedNotice
		return true
	}
	return false
}

// adjust applies a Left (d = -1) or Right (d = +1) to the focused row.
func (s *Settings) adjust(d int) {
	r, ok := s.focused()
	if !ok {
		return
	}
	if s.lockedDuringRun(r) {
		return
	}
	s.notice = ""
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
	case srSyncTrim:
		s.setSyncTrim(s.syncTrim + d*syncTrimStepMS)
	}
}

// activate applies Enter to the focused row: the device pickers step
// forward, calibration starts, the SoundFont row browses (or clears when
// no picker is installed), and the count-in steps up, wrapping past 8
// back to 0 so Enter alone can reach every value.
//
// The one exception is a picker whose device has never been chosen. There
// Enter COMMITS what is shown instead of stepping past it, because that is
// what the row promises ("press enter to use it") and because stepping
// would store a device the first-run user never looked at.
func (s *Settings) activate() {
	r, ok := s.focused()
	if !ok {
		return
	}
	if s.lockedDuringRun(r) {
		return
	}
	switch r {
	case srCapture, srPlayback:
		s.notice = ""
		if s.deviceStateOf(r) == devUnchosen {
			s.commitDevices()
			return
		}
		s.adjust(+1)
	case srCountIn:
		s.notice = ""
		if s.countIn >= MaxCountIn {
			s.setCountIn(0)
		} else {
			s.setCountIn(s.countIn + 1)
		}
	case srSoundFont:
		s.notice = ""
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
	// The selection just became the saved device, so any "saved device is
	// not connected" note about the OLD saved ID has expired.
	s.refreshMissing()
}

// MaxCountIn bounds the count-in the settings row offers, in beats: two
// bars of 4/4.
//
// It is stated here rather than taken from internal/appconfig because this
// package deliberately does not know what the config file looks like — it
// works against the Prefs facade and nothing else. The storage layer
// enforces the same bound on what it loads, and a test in cmd/musictutor,
// which is the one place that sees both, asserts the two numbers agree.
const MaxCountIn = 8

// clampCountIn holds a count-in inside the supported 0..8 beats.
func clampCountIn(n int) int {
	if n < 0 {
		return 0
	}
	if n > MaxCountIn {
		return MaxCountIn
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

// syncTrimStepMS is how far one press of Left or Right moves the trim.
// Five milliseconds is under what anyone can hear as a timing error on its
// own and coarse enough to cross the useful range in a dozen presses.
const syncTrimStepMS = 5

// MaxSyncTrimMS bounds the manual audio/visual trim either way. A quarter
// of a second is already far more than any driver holds; past that the
// trim stops correcting an error and starts being one.
//
// It is stated here rather than taken from internal/appconfig because this
// package deliberately does not know what the config file looks like — it
// works against the Prefs facade and nothing else. The storage layer
// enforces the same bound on the way in and out, and a test in
// cmd/musictutor, which is the one place that sees both, asserts the two
// numbers agree.
const MaxSyncTrimMS = 250

// clampSyncTrim holds the trim inside the range the config will store.
func clampSyncTrim(ms int) int {
	if ms < -MaxSyncTrimMS {
		return -MaxSyncTrimMS
	}
	if ms > MaxSyncTrimMS {
		return MaxSyncTrimMS
	}
	return ms
}

// A settingsSyncTrimmer is the optional Prefs extension that stores the
// audio/visual sync trim. Prefs itself has no such pair, so a build whose
// preferences cannot hold one simply does not show the row — rather than
// showing a control whose value evaporates when the application closes.
type settingsSyncTrimmer interface {
	SyncTrim() int
	SetSyncTrim(ms int)
}

// hasSyncTrim reports whether the trim can be stored, and therefore shown.
func (s *Settings) hasSyncTrim() bool {
	p := s.prefs()
	if p == nil {
		return false
	}
	_, ok := p.(settingsSyncTrimmer)
	return ok
}

// setSyncTrim moves the playhead against the sound and persists it.
func (s *Settings) setSyncTrim(ms int) {
	ms = clampSyncTrim(ms)
	if ms == s.syncTrim {
		return
	}
	s.syncTrim = ms
	if tp, ok := s.prefs().(settingsSyncTrimmer); ok {
		tp.SetSyncTrim(ms)
		s.save()
	}
}

// syncTrimText spells the trim and which way it moves things, because the
// sign alone tells nobody anything: what the user knows is that the tab is
// ahead of the sound or behind it, and the row has to be readable in those
// words.
func (s *Settings) syncTrimText() string {
	switch {
	case s.syncTrim == 0:
		return "0 ms  (measured buffering only)"
	case s.syncTrim > 0:
		return fmt.Sprintf("+%d ms  (tab drawn later)", s.syncTrim)
	default:
		return fmt.Sprintf("%d ms  (tab drawn earlier)", s.syncTrim)
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
	if s.pick == nil || s.sfBusy {
		// One dialog at a time: without the guard, every Enter press or
		// browse click stacked another unparented OS dialog, and the
		// last one confirmed silently overwrote the others' choices —
		// the exact failure the start screen's dialogBusy exists to
		// prevent (verification follow-up).
		return
	}
	s.sfBusy = true
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
// own goroutine and reports whether it started. It refuses, with a visible
// notice, while a run this screen started is already in flight — Calibrate
// drives the device for several seconds and two overlapping runs would
// measure each other.
//
// That guard only covers this instance. The screen is rebuilt on every
// visit, so the device's owner — the AudioServices implementation — holds
// the guard that spans instances; its refusal comes back as an error from
// Calibrate and is displayed like any other failure.
func (s *Settings) startCalibration() bool {
	a := s.audio()
	if a == nil || !s.hasDevices() {
		return false
	}
	if s.calibrating() {
		s.notice = calBusyNotice
		return false
	}
	// On first run the pickers show the system defaults with nothing in
	// Prefs, and the one prominent button on the screen is "calibrate
	// now". Measuring that pair without committing it stored the offset
	// under concrete device IDs while the saved pair stayed empty — and
	// the application only opens the live capture path when the STORED
	// capture ID is non-empty, so a mouse user who clicked the obvious
	// button and watched it succeed got playback-only practice with no
	// scoring and nothing to explain why. Calibrating a pair is the
	// strongest possible way of choosing it, so choose it. A fallback for
	// a saved device that is merely unplugged is deliberately NOT
	// committed: that saved ID must survive until its interface returns,
	// so a fallback pair keeps the old store-offset-only behaviour.
	capSt, playSt := s.deviceStateOf(srCapture), s.deviceStateOf(srPlayback)
	if (capSt == devUnchosen || playSt == devUnchosen) && capSt != devFallback && playSt != devFallback {
		s.commitDevices()
	}
	// The device pair is read here, on the game loop, so the goroutine
	// never touches the selection state — and the result stays bound to
	// the pair it was measured for.
	capID, playID := s.selectedIDs()

	ctx, cancel := context.WithCancel(context.Background())
	run := &calRun{
		capID:  capID,
		playID: playID,
		cancel: cancel,
		done:   make(chan struct{}),
		phase:  calRunning,
	}
	s.run = run
	s.lastPhase = calRunning
	s.notice = ""

	go func() {
		// The goroutine closes over run, ctx and a — never over s — so
		// nothing it does can reach a screen that has been popped.
		defer close(run.done)
		defer cancel()
		frames, conf, err := calibrateWith(ctx, a, capID, playID, run.setProgress)
		run.finish(frames, conf, err)
	}()
	return true
}

// settingsCloseGrace bounds how long Close waits for a cancelled
// calibration to return. A backend that honours the context returns at
// once; one that does not must not hold the game loop hostage, so the wait
// gives up and the (already detached) run finishes on its own.
const settingsCloseGrace = 300 * time.Millisecond

// Close releases what the screen owns. The Shell calls it when the screen
// is popped: an Escape during a measurement would otherwise leave the run
// holding the capture and playback devices for the rest of its timeout,
// and a piece opened seconds later would collide with a live duplex
// stream.
//
// It cancels the run, detaches it so nothing it publishes can be seen
// again, and waits — briefly — for the goroutine to return, so the device
// is free before the next screen asks for it. Calling it twice, or with no
// run in flight, does nothing.
func (s *Settings) Close() {
	if s.onClose != nil {
		fn := s.onClose
		// Cleared before the call so a handler that somehow leads back
		// here cannot run twice; Close is documented as at-most-once.
		s.onClose = nil
		fn()
	}
	run := s.run
	s.run = nil
	s.lastPhase = calIdle
	if run == nil {
		return
	}
	run.abandon()
	select {
	case <-run.done:
	case <-time.After(settingsCloseGrace):
	}
}

// SetOnClose installs a callback the Shell's teardown runs when this
// screen is popped. It runs on the game loop, so it must not block.
func (s *Settings) SetOnClose(fn func()) { s.onClose = fn }

// resetCalibration drops the displayed result of the last run. A run
// still in flight is left alone: it owns the device until it returns.
func (s *Settings) resetCalibration() {
	if s.run == nil || s.run.running() {
		return
	}
	s.run = nil
	s.lastPhase = calIdle
}

// calSnapshot copies the calibration state out under the run's lock. Every
// reader — Update, Draw, tests — goes through it, so no caller can
// observe a partially written result.
func (s *Settings) calSnapshot() calSnap {
	if s.run == nil {
		return calSnap{Phase: calIdle}
	}
	return s.run.snapshot()
}

// snapIsCurrent reports whether sn describes the pair the user is looking
// at. A measurement taken on one pair says nothing about another, so a
// snapshot whose pair has been superseded is not shown as a result for the
// pair now selected.
func (s *Settings) snapIsCurrent(sn calSnap) bool {
	if sn.Phase == calIdle {
		return true
	}
	capID, playID := s.selectedIDs()
	return sn.CaptureID == capID && sn.PlaybackID == playID
}

// syncSettings drains what other goroutines have posted: a SoundFont
// chosen through the picker, and a calibration that has finished (whose
// result the audio services have now stored, so the offset row is
// re-read). Update calls it every frame; tests call it directly.
func (s *Settings) syncSettings() {
	s.mu.Lock()
	sf := s.pendingSF
	s.pendingSF = nil
	s.mu.Unlock()

	if sf != nil {
		// Every dialog outcome re-arms the browse; an empty path is a
		// cancel and changes nothing.
		s.sfBusy = false
		if *sf != "" && *sf != s.soundFont {
			s.applySoundFont(*sf)
		}
	}
	phase := s.calSnapshot().Phase
	if phase != s.lastPhase {
		s.lastPhase = phase
		if phase != calRunning {
			// Whatever the run was blocking is allowed again, so the
			// notice explaining the block has expired with it.
			s.notice = ""
		}
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

// SameAudioInterface reports whether two endpoint names look like they
// belong to the same physical interface. Exported because the application
// layer asks the same question when it opens a piece live: two heuristics
// answering it differently had the settings screen and the practice view
// giving the user contradictory verdicts on the same device pair
// (audit C2). An empty name means unknown, and unknown never warns.
func SameAudioInterface(captureName, playbackName string) bool {
	return sameAudioInterface(captureName, playbackName)
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

// framesText renders an offset in milliseconds first and the frames it
// was measured in after. Milliseconds lead because they are the unit a
// musician can sanity-check — everyone who has stood away from their amp
// has a feel for what 130 ms of delay is, while a frame count means
// nothing without doing the sample-rate arithmetic in your head.
func (s *Settings) framesText(frames int) string {
	rate := s.rate
	if rate <= 0 {
		rate = settingsDefaultRate
	}
	return fmt.Sprintf("%.1f ms (%d frames)", float64(frames)*1000/float64(rate), frames)
}

// deviceText renders one picker's value: position in the list, name, and
// the system-default and selected markers.
//
// The selected marker used to be unconditional, so on first run the row
// read "[1/3] Microphone (...) (system default) <- selected" for a device
// Prefs had never been told about. The application only opens the live
// capture path when the STORED capture ID is non-empty (cmd/musictutor:
// captureID == "" falls back to playback-only), so the user who came from
// the checklist step "choose your audio interface", read that line and
// pressed Escape got no scoring at all and nothing to explain it. An entry
// nobody has chosen therefore says so, and says which key chooses it.
func deviceText(opts []DeviceOption, idx int, st deviceState) string {
	if idx < 0 || idx >= len(opts) {
		return "none available"
	}
	o := opts[idx]
	s := fmt.Sprintf("[%d/%d] %s", idx+1, len(opts), o.Name)
	if o.Default {
		s += "  (system default)"
	}
	if st == devUnchosen {
		return s + "  <- not chosen yet: press enter to use it"
	}
	// A fallback IS what a piece will use, so it is honestly the selection;
	// the note beside the row is what says it is standing in for something
	// (audit C3).
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

// soundFontNote is the line under the SoundFont row, with the colour to
// draw it in. It covers the two states in which the row cannot do what it
// looks like it can: no file dialog in this build, and a dialog already
// open. The second matters most — the dialog is unowned, so on Windows it
// can sit BEHIND the game window, and the row is then the only thing that
// can tell the user where their missing dialog went instead of leaving
// them clicking a button that swallows everything.
func (s *Settings) soundFontNote() (string, color.RGBA) {
	switch {
	case s.sfBusy:
		return "a file dialog is open: finish or cancel it, it may be sitting behind this window", colClose
	case s.pick == nil:
		return "no file dialog is available in this build; -sf2 on the command line still works", colBarline
	}
	return "", colBarline
}

// soundFontText renders the SoundFont row's value.
func (s *Settings) soundFontText() string {
	if s.soundFont == "" {
		return "built-in pluck"
	}
	return s.soundFont
}

// settingsWeakConfidence is the confidence below which a stored
// calibration is flagged as worth redoing. The measurement itself accepts
// anything over its own floor of 0.5, so a run that barely scraped past —
// clicks heard faintly, or at inconsistent delays — is stored and then
// forever reads exactly like a clean loopback measurement. 0.7 sits far
// enough above the floor to catch those without nagging about the
// ordinary spread of honest mic-in-front-of-speaker runs.
const settingsWeakConfidence = 0.7

// settingsConfidencer is the optional interface an AudioServices
// implementation may satisfy to report the confidence stored alongside a
// pair's calibration offset. Without it the screen simply cannot tell a
// shaky measurement from a solid one, and says nothing.
type settingsConfidencer interface {
	StoredConfidence(captureID, playbackID string) (float64, bool)
}

// storedConfidence asks the backend for the selected pair's stored
// confidence, when the backend can answer at all.
func (s *Settings) storedConfidence() (float64, bool) {
	c, ok := s.audio().(settingsConfidencer)
	if !ok {
		return 0, false
	}
	capID, playID := s.selectedIDs()
	return c.StoredConfidence(capID, playID)
}

// storedCalibrationText renders what is known about the selected pair
// without any live run: the offset the backend has on file, or an admission
// that there is none. An offset whose stored confidence is weak says so —
// the number is on file and practice works, but the user who measured it
// through a barely-audible loopback deserves to know it is suspect.
func (s *Settings) storedCalibrationText() (string, color.RGBA) {
	if !s.offOK {
		return "not measured for this pair", colClose
	}
	txt := "stored " + s.framesText(s.offFrames)
	if conf, ok := s.storedConfidence(); ok && conf < settingsWeakConfidence {
		return txt + ", weak measurement: worth re-measuring", colClose
	}
	return txt, colHUD
}

// calibrationText renders the calibration row's value and the color to
// draw it in, from a snapshot taken by the caller. A snapshot belonging to
// a pair the user has left behind is never shown as this pair's result:
// the stored value stands instead, and a run still measuring the old pair
// says so rather than letting its progress read as this pair's.
func (s *Settings) calibrationText(sn calSnap) (string, color.RGBA) {
	if !s.snapIsCurrent(sn) {
		txt, col := s.storedCalibrationText()
		if sn.Phase == calRunning {
			return txt + "  (measuring the previous pair)", col
		}
		return txt, col
	}
	switch sn.Phase {
	case calRunning:
		return fmt.Sprintf("measuring... %3.0f%%", sn.Progress*100), colSounding
	case calDone:
		return fmt.Sprintf("measured %s, confidence %.0f%%", s.framesText(sn.Frames), sn.Confidence*100), colHit
	case calFailed:
		// The error itself carries the advice — what to connect, what to
		// check — and runs to roughly twice the row's width, so rendering
		// it here truncated exactly the part that says what to do. The
		// row states the outcome; the full advice is wrapped into the
		// notice band directly below (see items).
		return "measurement failed", colMiss
	}
	return s.storedCalibrationText()
}

// calFailureAdvice is the measurement error as the notice band shows it.
// The message is written by the latency package for exactly this moment —
// it names the missing loopback, the input level, the click spacing — but
// its "latency: " package prefix is developer vocabulary and is trimmed
// at this boundary.
func calFailureAdvice(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimPrefix(err.Error(), "latency: ")
}

// saveErrLine renders the footer's save failure, and whether there is one.
//
// A real one is a wrapped os.Rename error carrying two full paths and the
// operating system's message — several times the width of the window, and
// it used to be drawn unbounded straight off the right edge. There is no
// room to wrap it: the config-file line sits directly above and the key
// hints directly below, so it is cut to one. What is cut is the FRONT,
// because the reason ("There is not enough space on the disk.") is the
// last thing in a wrapped path error and the only part that says what to
// do about it; the label is kept so the line still announces itself.
func (s *Settings) saveErrLine() (string, bool) {
	if s.saveErr == nil {
		return "", false
	}
	const label = "SAVE FAILED: "
	return label + ellipsizeW(s.saveErr.Error(), settingsFooterW-textW(label)), true
}

// configText renders the footer's config-file location.
func (s *Settings) configText() string {
	if s.configPath == "" {
		return "config file: location unknown"
	}
	return "config file: " + s.configPath
}

// ---- layout ---------------------------------------------------------------

// The screen is laid out as a display list before anything is drawn.
//
// Laying it out first — rather than drawing straight down the page with a
// running cursor, as this screen used to — is what lets the mouse find a
// row exactly where it was drawn. The blocks between the rows are not
// fixed height: a split-device warning wraps to as many lines as it
// needs, and a running calibration replaces a hint line with a progress
// bar. A hit test that recomputed those offsets separately would agree
// with the drawing only until one of them changed. Draw renders the list
// and the hit test reads the same list, so neither can go out of true.

// Layout metrics. They sit on the shared page margins in theme.go, so
// this screen lines up with the start screen and the practice view.
const (
	settingsLeft   = uiPadX
	settingsValueX = 300.0
	settingsLineH  = uiLineH
	settingsRowH   = uiRowH
	// settingsWrap is the pixel width a note may occupy beside the value
	// column before it wraps.
	settingsWrap = screenW - settingsValueX - uiPadX
	// settingsBtnH was 20, which is four pixels short of a body line: the
	// label's descenders were drawn through the bottom border (the g of
	// "measuring...") and every label sat about 2px low. 22 clears both
	// borders and still leaves 4px between the buttons of adjacent rows at
	// the 26px row pitch.
	settingsBtnH   = 22.0
	settingsBtnPad = 8.0
	settingsBtnGap = 6.0
	// settingsValueGap is the clearance a row's value keeps from the
	// leftmost button beside it, or from the right page margin.
	settingsValueGap = 12.0
	// settingsFooterW is the width of the footer's own lines, margin to
	// margin.
	settingsFooterW = screenW - 2*settingsLeft
	// settingsNoticeLines and settingsSfNoteLines are the heights, in body
	// lines, of the two FIXED bands below the calibration and SoundFont
	// rows. See reserveNote: what those bands say changes with what the
	// user just did, and none of it may move a row. Three lines because
	// the notice band also carries a failed measurement's advice, and the
	// longest of those — the click-spacing aliasing explanation — needs
	// them all.
	settingsNoticeLines = 3
	settingsSfNoteLines = 1
	// settingsCalHintLines is the height, in body lines, of the FIXED
	// band under the calibration row. The setup hint needs two lines to
	// head off the doomed guitar-into-the-input setup, and the running
	// progress bar pads itself to the same height, because pressing
	// "calibrate now" must not move the rows below it (see reserveNote).
	settingsCalHintLines = 2
)

// calSetupHint is what sits under the calibration row when nothing is
// running. Its job is to prevent the natural guitar setup — DI into the
// input, monitors on the output, press the button — which fails after
// several opaque seconds, because the click train comes out of the
// speakers and a pickup only hears strings. A mic'd player (a sax, a
// voice) is already set up right, and the hint's mic clause is their
// setup described. Saying what must physically happen is cheaper than
// explaining the failure afterwards.
const calSetupHint = "plays a few seconds of clicks that must travel out and back in:" +
	" point a mic at your speakers, or cable an output back into an input;" +
	" a guitar's pickup won't hear them, a mic'd horn already does"

type settingsItemKind int

const (
	siSection settingsItemKind = iota
	siNote
	siRow
	siProgress
)

// A settingsButton is one clickable affordance on a row. The keyboard
// reaches the same actions through adjust and activate; these exist so a
// mouse user does not have to guess that left and right cycle a device.
type settingsButton struct {
	label string
	r     rect
	act   func(*Settings)
	// disabled draws the button as unavailable. A control whose click is
	// swallowed must look swallowed: the SoundFont browse kept its idle
	// look while an OS dialog was already up, and since that dialog is
	// unparented and can sit BEHIND the game window, the user's response
	// was to click it again.
	disabled bool
}

// A settingsItem is one entry of the display list.
type settingsItem struct {
	kind settingsItemKind
	y    float64
	text string
	col  color.RGBA
	// row is the index into s.rows for siRow, and -1 otherwise.
	row     int
	label   string
	buttons []settingsButton
	prog    float64
	// valueW is how much room the value has before it reaches this row's
	// own buttons; see addRow.
	valueW float64
}

// valueText is the row's value clipped to the room it actually has. A
// calibration failure carries the backend's error, which for a failed
// store is a path plus an OS message — around 1150px of text, drawn
// straight under the row's opaque buttons and off the right edge of the
// page before it was bounded.
func (it settingsItem) valueText() string { return truncateW(it.text, it.valueW) }

// band is the full-width rectangle a row occupies, which is what a click
// anywhere along the line lands in.
func (it settingsItem) band() rect {
	return rect{settingsLeft - 8, it.y - 5, screenW - 2*settingsLeft + 16, settingsRowH - 6}
}

// itemsBuilder accumulates the display list and owns the layout cursor.
type itemsBuilder struct {
	out []settingsItem
	y   float64
	row int // index into s.rows, advanced as each focusable row is added
}

func (b *itemsBuilder) section(title string) {
	b.out = append(b.out, settingsItem{kind: siSection, y: b.y, text: title, row: -1})
	b.y += uiSectionH
}

func (b *itemsBuilder) note(text string, col color.RGBA) {
	for _, line := range wrapTextW(text, settingsWrap) {
		b.out = append(b.out, settingsItem{kind: siNote, y: b.y, text: line, col: col, row: -1})
		b.y += settingsLineH
	}
	b.y += 4
}

// reserveNote emits at most lines lines of text into a band of FIXED
// height, whether or not there is anything to say.
//
// A band whose height follows its contents moves every row below it. That
// is how a REFUSED click could hit something: clicking "+" on the count-in
// during a calibration posts the lock notice, which as an ordinary note
// pushed the rows below it down ~40px, so the identical second click —
// same pixel, user has not moved the mouse — landed on the SoundFont row's
// "clear" and discarded their SoundFont without a word. Nothing a band
// says may change where a row is drawn.
func (b *itemsBuilder) reserveNote(lines int, text string, col color.RGBA) {
	top := b.y
	if text != "" {
		wrapped := wrapTextW(text, settingsWrap)
		if len(wrapped) > lines {
			// More than the band holds: fold the rest into the last line
			// and cut it there, rather than let it run out of the band.
			wrapped[lines-1] = truncateW(strings.Join(wrapped[lines-1:], " "), settingsWrap)
			wrapped = wrapped[:lines]
		}
		for i, l := range wrapped {
			b.out = append(b.out, settingsItem{
				kind: siNote, y: top + float64(i)*settingsLineH, text: l, col: col, row: -1,
			})
		}
	}
	b.y = top + float64(lines)*settingsLineH + 4
}

// progress advances exactly as far as the one-line note it replaces while
// a measurement runs, so pressing "calibrate now" does not shift the rows
// below the bar either.
func (b *itemsBuilder) progress(f float64) {
	b.out = append(b.out, settingsItem{kind: siProgress, y: b.y, prog: f, row: -1})
	b.y += settingsLineH + 4
}

// addRow adds a focusable line with its buttons laid out right to left,
// so the rightmost button sits on the page margin whatever the labels are.
func (b *itemsBuilder) addRow(label, value string, col color.RGBA, btns ...settingsButton) {
	x := screenW - uiPadX
	for i := len(btns) - 1; i >= 0; i-- {
		w := textW(btns[i].label) + 2*settingsBtnPad
		x -= w
		btns[i].r = rect{x, b.y - 4, w, settingsBtnH}
		x -= settingsBtnGap
	}
	// The value gets what the buttons leave. The budget is computed here,
	// where the buttons were actually placed, so Draw cannot work it out
	// differently — the same reason the hit test reads this list.
	right := screenW - uiPadX
	if len(btns) > 0 {
		right = btns[0].r.x
	}
	b.out = append(b.out, settingsItem{
		kind: siRow, y: b.y, text: value, col: col,
		row: b.row, label: label, buttons: btns,
		valueW: right - settingsValueGap - settingsValueX,
	})
	b.row++
	b.y += settingsRowH
}

// items builds the whole screen. The order and the conditions match what
// rebuild put in s.rows, which is what keeps the cursor index and the
// drawn rows in step.
func (s *Settings) items() []settingsItem {
	b := &itemsBuilder{y: uiBodyTop + 8}

	backend := "audio backend: none"
	if a := s.audio(); a != nil {
		backend = "audio backend: " + a.BackendName()
	}
	b.note(backend, colBarline)

	b.section("AUDIO DEVICES")
	if s.hasDevices() {
		b.addRow("capture", deviceText(s.capture, s.capIdx, s.deviceStateOf(srCapture)), colHUD,
			settingsButton{label: "<", act: func(s *Settings) { s.adjustRow(srCapture, -1) }},
			settingsButton{label: ">", act: func(s *Settings) { s.adjustRow(srCapture, +1) }})
		b.addRow("playback", deviceText(s.playback, s.playIdx, s.deviceStateOf(srPlayback)), colHUD,
			settingsButton{label: "<", act: func(s *Settings) { s.adjustRow(srPlayback, -1) }},
			settingsButton{label: ">", act: func(s *Settings) { s.adjustRow(srPlayback, +1) }})
		if s.capMissing {
			b.note("the saved capture device is not connected: the fallback shown here is what a piece will actually use", colClose)
		}
		if s.playMissing {
			b.note("the saved playback device is not connected: the fallback shown here is what a piece will actually use", colClose)
		}
		if w, ok := s.splitDeviceWarning(); ok {
			b.note(w, colClose)
		}
	} else {
		for _, l := range s.audioUnavailableText() {
			b.note(l, colClose)
		}
	}

	b.section("LATENCY CALIBRATION")
	sn := s.calSnapshot()
	if s.hasDevices() {
		txt, col := s.calibrationText(sn)
		running := sn.Phase == calRunning && s.snapIsCurrent(sn)
		label := "calibrate now"
		if running {
			label = "measuring..."
		}
		b.addRow("round-trip offset", txt, col,
			settingsButton{label: label, act: func(s *Settings) {
				s.focusRow(srCalibrate)
				s.startCalibration()
			}})
		if running {
			b.progress(sn.Progress)
			// Pad the bar out to the hint band's height, so swapping one
			// for the other moves nothing below them.
			b.y += float64(settingsCalHintLines-1) * settingsLineH
		} else {
			b.reserveNote(settingsCalHintLines, calSetupHint, colBarline)
		}
	} else {
		b.note("unavailable without a capture and playback device", colBarline)
	}

	// An input a running measurement is holding off was ignored on
	// purpose; say so where the user is looking rather than swallowing
	// the key. The band is there whether or not there is a notice, so
	// saying it cannot move what the user is about to click. With nothing
	// refused to explain, the band carries a failed measurement's advice
	// instead: the row above only states the outcome, and this is the
	// room the advice wraps into.
	noticeTxt, noticeCol := s.notice, colClose
	if noticeTxt == "" && sn.Phase == calFailed && s.snapIsCurrent(sn) {
		noticeTxt, noticeCol = calFailureAdvice(sn.Err), colMiss
	}
	b.reserveNote(settingsNoticeLines, noticeTxt, noticeCol)

	b.section("INSTRUMENT")
	sfButtons := []settingsButton{{label: "clear", act: func(s *Settings) {
		s.focusRow(srSoundFont)
		s.clearSoundFont()
	}}}
	if s.pick != nil {
		browse := settingsButton{label: "browse", act: func(s *Settings) {
			s.focusRow(srSoundFont)
			s.chooseSoundFont()
		}}
		if s.sfBusy {
			// The click is already a no-op (chooseSoundFont refuses while
			// a dialog is up); this is the half that was missing — the
			// button now looks like what it does.
			browse.label, browse.disabled = "waiting…", true
		}
		sfButtons = append([]settingsButton{browse}, sfButtons...)
	}
	b.addRow("soundfont", ellipsizeW(s.soundFontText(), 430), colHUD, sfButtons...)
	sfNote, sfCol := s.soundFontNote()
	b.reserveNote(settingsSfNoteLines, sfNote, sfCol)

	b.section("PRACTICE")
	b.addRow("count-in beats", fmt.Sprintf("%d  (0-%d)", s.countIn, MaxCountIn), colHUD,
		settingsButton{label: "-", act: func(s *Settings) { s.adjustRow(srCountIn, -1) }},
		settingsButton{label: "+", act: func(s *Settings) { s.adjustRow(srCountIn, +1) }})
	if s.hasSyncTrim() {
		b.addRow("audio / visual sync", s.syncTrimText(), colHUD,
			settingsButton{label: "-", act: func(s *Settings) { s.adjustRow(srSyncTrim, -1) }},
			settingsButton{label: "+", act: func(s *Settings) { s.adjustRow(srSyncTrim, +1) }})
		b.reserveNote(1, "the app already subtracts the buffering it can measure; this trims what it cannot", colDim)
	}

	return b.out
}

// focusRow puts the keyboard cursor on a row. EVERY row button calls it
// before acting: the device and count-in buttons went through adjustRow
// and did, but calibrate, browse and clear acted directly, so clicking one
// of them left the highlight — and the arrow keys — on whatever row the
// cursor happened to be on, and the next Right adjusted a setting the user
// was not looking at.
func (s *Settings) focusRow(kind settingsRow) {
	if i := s.rowIndex(kind); i >= 0 {
		s.cur = i
	}
}

// adjustRow focuses a row and applies a step to it, which is what a click
// on one of its buttons means: the mouse both moves the cursor and acts,
// where the keyboard needs two presses.
func (s *Settings) adjustRow(kind settingsRow, d int) {
	s.focusRow(kind)
	s.adjust(d)
}

// rowIndex finds a row kind's position in s.rows, or -1 when the row is
// not present in this configuration.
func (s *Settings) rowIndex(kind settingsRow) int {
	for i, r := range s.rows {
		if r == kind {
			return i
		}
	}
	return -1
}

// ---- input ----------------------------------------------------------------

// settingsBindings resolves this screen's control table, the source of
// both the footer hint and the ?/F1 overlay.
func (s *Settings) settingsBindings() []helpBinding {
	leave := helpBinding{Group: "session", Keys: "esc", Hint: "esc back", Desc: "Go back to where you came from"}
	if s.sh != nil && s.sh.Depth() <= 1 {
		leave = helpBinding{Group: "session", Keys: "esc", Hint: "esc quit", Desc: "Quit musicTutor"}
	}
	return []helpBinding{
		{Group: "moving", Keys: "up / down", Hint: "up/dn select", Desc: "Move between settings"},
		{Group: "moving", Keys: "click", Desc: "Select a setting, or press one of its buttons"},

		{Group: "changing", Keys: "left / right", Hint: "left/right adjust", Desc: "Step the selected setting"},
		{Group: "changing", Keys: "enter", Hint: "enter action", Desc: "Act on the selected setting: cycle it, browse, or calibrate"},

		{Group: "session", Keys: "? or F1", Hint: "? help", Desc: "This key-binding list"},
		leave,
	}
}

// hintLine is the footer summary, from the same table as the overlay.
func (s *Settings) hintLine() string { return hintLineOf(s.settingsBindings()) }

// handleMouse applies one frame of pointer input: a click on a row's
// button acts on it, and a click anywhere else along the row selects it.
// Taking the whole band rather than only the label is deliberate — a
// setting is a line, and the line is the target.
func (s *Settings) handleMouse(p pointer) {
	if !p.pressed {
		return
	}
	for _, it := range s.items() {
		if it.kind != siRow {
			continue
		}
		for _, btn := range it.buttons {
			if p.over(btn.r) {
				btn.act(s)
				return
			}
		}
		if p.over(it.band()) {
			s.cur = it.row
			return
		}
	}
}

// ---- Screen ---------------------------------------------------------------

// Update handles navigation and adjustment. Escape returns errQuit, which
// tells the Shell this screen is finished.
func (s *Settings) Update() error {
	s.ptr = readPointer()
	s.syncSettings()
	if s.helpOpen {
		if helpDismissed(s.ptr) {
			s.helpOpen = false
		}
		return nil
	}
	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape):
		return errQuit
	case helpKeyPressed():
		s.helpOpen = true
		return nil
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
	s.handleMouse(s.ptr)
	return nil
}

// Draw paints the screen from the display list. It is a pure projection
// of the state the methods above maintain: it makes no decisions and
// mutates nothing.
func (s *Settings) Draw(dst *ebiten.Image) {
	dst.Fill(colBG)
	drawHeader(dst, "SETTINGS", "changes are saved as you make them", colDim)

	for _, it := range s.items() {
		switch it.kind {
		case siSection:
			y := it.y
			drawSection(dst, &y, it.text)
		case siNote:
			drawText(dst, it.text, settingsValueX, it.y, it.col)
		case siProgress:
			const barW, barH = 320, 8
			x0 := float32(settingsValueX)
			vector.StrokeRect(dst, x0, float32(it.y), barW, barH, 1, colString, false)
			vector.DrawFilledRect(dst, x0+1, float32(it.y)+1, (barW-2)*float32(it.prog), barH-2, colSounding, false)
		case siRow:
			s.drawItemRow(dst, it)
		}
	}

	fy := screenH - 68.0
	drawText(dst, s.configText(), settingsLeft, fy, colBarline)
	if line, ok := s.saveErrLine(); ok {
		drawText(dst, line, settingsLeft, fy+settingsLineH, colMiss)
	}
	drawFooter(dst, s.hintLine())

	if s.helpOpen {
		drawHelpOverlay(dst, "SETTINGS KEYS", s.settingsBindings(), "")
	}
}

// drawItemRow paints one focusable row: the highlight band when it holds
// the cursor, the label, the value, and the row's buttons.
func (s *Settings) drawItemRow(dst *ebiten.Image, it settingsItem) {
	col := it.col
	if it.row == s.cur {
		band := it.band()
		vector.DrawFilledRect(dst, float32(band.x), float32(band.y), float32(band.w), float32(band.h), colFocus, false)
		drawText(dst, ">", settingsLeft-8, it.y, colSounding)
		col = colNote
	}
	drawText(dst, it.label, settingsLeft+8, it.y, colHUD)
	drawText(dst, it.valueText(), settingsValueX, it.y, col)
	for _, btn := range it.buttons {
		fill, edge, tc := colPanel, colPanelEdge, colHUD
		switch {
		case btn.disabled:
			// The same greying drawChip uses, so "you cannot press this"
			// reads the same wherever it appears.
			fill, edge, tc = colBG, colBarline, colBarline
		case s.ptr.over(btn.r):
			fill, edge, tc = colHover, colDim, colNote
		}
		drawPanel(dst, btn.r, fill, edge)
		drawText(dst, btn.label, centreX(btn.label, btn.r.x, btn.r.w), settingsBtnTextY(btn.r), tc)
	}
}

// settingsBtnTextY centres one body line vertically inside a button. The
// label used to be drawn at a flat r.y+4, which is right for exactly one
// button height and was wrong for the one in use: the ink of a descender
// (the g of "measuring...") came down through the bottom border and every
// label sat about 2px low. Deriving it from the rect keeps the two in step
// whatever the height becomes.
func settingsBtnTextY(r rect) float64 { return r.y + (r.h-uiTextH)/2 }
