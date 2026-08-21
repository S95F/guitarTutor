package ui

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

const settingsDefaultRate = 48000

type settingsRow int

const (
	srCapture settingsRow = iota
	srPlayback
	srCalibrate
	srSoundFont
	srCountIn
	srSyncTrim
)

type calPhase int

const (
	calIdle calPhase = iota
	calRunning
	calDone
	calFailed
)

type calSnap struct {
	Phase      calPhase
	Progress   float64
	Frames     int
	Confidence float64
	Err        error
	CaptureID  string
	PlaybackID string
}

type calRun struct {
	capID  string
	playID string
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	abandoned bool
	phase     calPhase
	progress  float64
	frames    int
	conf      float64
	err       error
}

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

func (r *calRun) abandon() {
	r.mu.Lock()
	r.abandoned = true
	r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *calRun) abandonedNow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abandoned
}

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

func (r *calRun) running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase == calRunning
}

type settingsPather interface{ Path() string }

type settingsRater interface{ SampleRate() int }

type settingsCanceller interface {
	CalibrateContext(ctx context.Context, captureID, playbackID string, progress func(float64)) (frames int, confidence float64, err error)
}

func calibrateWith(ctx context.Context, a AudioServices, captureID, playbackID string, progress func(float64)) (int, float64, error) {
	if c, ok := a.(settingsCanceller); ok {
		return c.CalibrateContext(ctx, captureID, playbackID, progress)
	}
	return a.Calibrate(captureID, playbackID, progress)
}

type Settings struct {
	sh *Shell

	rows []settingsRow
	cur  int

	capture  []DeviceOption
	playback []DeviceOption
	capIdx   int
	playIdx  int
	devErr   error

	capMissing  bool
	playMissing bool

	capChosen  bool
	playChosen bool

	countIn   int
	syncTrim  int
	soundFont string

	offFrames int
	offOK     bool

	saveErr    error
	configPath string
	rate       int

	pick func(exts []string, chosen func(string))

	onClose func()

	run *calRun

	lastPhase calPhase

	notice string

	helpOpen bool

	sfBusy bool

	ptr pointer

	mu        sync.Mutex
	pendingSF *string
}

var _ Screen = (*Settings)(nil)

var _ interface{ Close() } = (*Settings)(nil)

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

func (s *Settings) SetFilePicker(fn func(exts []string, chosen func(string))) {
	s.pick = fn
	s.rebuild()
}

func (s *Settings) SetConfigPath(path string) { s.configPath = path }

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

func deviceIsSaved(opts []DeviceOption, idx int, saved string) bool {
	return saved != "" && idx >= 0 && idx < len(opts) && opts[idx].ID == saved
}

type deviceState int

const (
	devChosen deviceState = iota
	devFallback
	devUnchosen
)

func devState(chosen, missing bool) deviceState {
	switch {
	case chosen:
		return devChosen
	case missing:
		return devFallback
	}
	return devUnchosen
}

func (s *Settings) deviceStateOf(r settingsRow) deviceState {
	switch r {
	case srCapture:
		return devState(s.capChosen, s.capMissing)
	case srPlayback:
		return devState(s.playChosen, s.playMissing)
	}
	return devChosen
}

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

func (s *Settings) hasDevices() bool {
	return s.audio() != nil && len(s.capture) > 0 && len(s.playback) > 0
}

func (s *Settings) selectedIDs() (captureID, playbackID string) {
	if s.capIdx >= 0 && s.capIdx < len(s.capture) {
		captureID = s.capture[s.capIdx].ID
	}
	if s.playIdx >= 0 && s.playIdx < len(s.playback) {
		playbackID = s.playback[s.playIdx].ID
	}
	return captureID, playbackID
}

func (s *Settings) selectedNames() (captureName, playbackName string) {
	if s.capIdx >= 0 && s.capIdx < len(s.capture) {
		captureName = s.capture[s.capIdx].Name
	}
	if s.playIdx >= 0 && s.playIdx < len(s.playback) {
		playbackName = s.playback[s.playIdx].Name
	}
	return captureName, playbackName
}

func (s *Settings) refreshOffset() {
	s.offFrames, s.offOK = 0, false
	a := s.audio()
	if a == nil || !s.hasDevices() {
		return
	}
	capID, playID := s.selectedIDs()
	s.offFrames, s.offOK = a.CalibratedOffset(capID, playID)
}

func (s *Settings) moveCursor(d int) {
	n := len(s.rows)
	if n == 0 {
		s.cur = 0
		return
	}
	s.cur = ((s.cur+d)%n + n) % n
}

func (s *Settings) focused() (settingsRow, bool) {
	if s.cur < 0 || s.cur >= len(s.rows) {
		return 0, false
	}
	return s.rows[s.cur], true
}

func (s *Settings) calibrating() bool { return s.run != nil && s.run.running() }

const calLockedNotice = "a calibration is running: the device pair and count-in are locked until it finishes" +
	" (changing them mid-run would measure one pair and store it against another)"

const calBusyNotice = "a calibration is already running: two overlapping runs would measure each other"

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

func (s *Settings) cycleCapture(d int) {
	if len(s.capture) == 0 {
		return
	}
	n := len(s.capture)
	s.capIdx = ((s.capIdx+d)%n + n) % n
	s.commitDevices()
}

func (s *Settings) cyclePlayback(d int) {
	if len(s.playback) == 0 {
		return
	}
	n := len(s.playback)
	s.playIdx = ((s.playIdx+d)%n + n) % n
	s.commitDevices()
}

func (s *Settings) commitDevices() {
	capID, playID := s.selectedIDs()
	if p := s.prefs(); p != nil {
		p.SetDevices(capID, playID)
		s.save()
	}
	s.resetCalibration()
	s.refreshOffset()

	s.refreshMissing()
}

const MaxCountIn = 8

func clampCountIn(n int) int {
	if n < 0 {
		return 0
	}
	if n > MaxCountIn {
		return MaxCountIn
	}
	return n
}

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

const syncTrimStepMS = 5

const MaxSyncTrimMS = 250

func clampSyncTrim(ms int) int {
	if ms < -MaxSyncTrimMS {
		return -MaxSyncTrimMS
	}
	if ms > MaxSyncTrimMS {
		return MaxSyncTrimMS
	}
	return ms
}

type settingsSyncTrimmer interface {
	SyncTrim() int
	SetSyncTrim(ms int)
}

func (s *Settings) hasSyncTrim() bool {
	p := s.prefs()
	if p == nil {
		return false
	}
	_, ok := p.(settingsSyncTrimmer)
	return ok
}

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

func (s *Settings) clearSoundFont() {
	if s.soundFont == "" {
		return
	}
	s.applySoundFont("")
}

func (s *Settings) chooseSoundFont() {
	if s.pick == nil || s.sfBusy {

		return
	}
	s.sfBusy = true
	s.pick([]string{".sf2"}, func(path string) {

		s.mu.Lock()
		p := path
		s.pendingSF = &p
		s.mu.Unlock()
	})
}

func (s *Settings) applySoundFont(path string) {
	s.soundFont = path
	if p := s.prefs(); p != nil {
		p.SetSoundFont(path)
		s.save()
	}
}

func (s *Settings) save() {
	p := s.prefs()
	if p == nil {
		return
	}
	s.saveErr = p.Save()
}

func (s *Settings) startCalibration() bool {
	a := s.audio()
	if a == nil || !s.hasDevices() {
		return false
	}
	if s.calibrating() {
		s.notice = calBusyNotice
		return false
	}

	capSt, playSt := s.deviceStateOf(srCapture), s.deviceStateOf(srPlayback)
	if (capSt == devUnchosen || playSt == devUnchosen) && capSt != devFallback && playSt != devFallback {
		s.commitDevices()
	}

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

		defer close(run.done)
		defer cancel()
		frames, conf, err := calibrateWith(ctx, a, capID, playID, run.setProgress)
		run.finish(frames, conf, err)
	}()
	return true
}

const settingsCloseGrace = 300 * time.Millisecond

func (s *Settings) Close() {
	if s.onClose != nil {
		fn := s.onClose

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

func (s *Settings) SetOnClose(fn func()) { s.onClose = fn }

func (s *Settings) resetCalibration() {
	if s.run == nil || s.run.running() {
		return
	}
	s.run = nil
	s.lastPhase = calIdle
}

func (s *Settings) calSnapshot() calSnap {
	if s.run == nil {
		return calSnap{Phase: calIdle}
	}
	return s.run.snapshot()
}

func (s *Settings) snapIsCurrent(sn calSnap) bool {
	if sn.Phase == calIdle {
		return true
	}
	capID, playID := s.selectedIDs()
	return sn.CaptureID == capID && sn.PlaybackID == playID
}

func (s *Settings) syncSettings() {
	s.mu.Lock()
	sf := s.pendingSF
	s.pendingSF = nil
	s.mu.Unlock()

	if sf != nil {

		s.sfBusy = false
		if *sf != "" && *sf != s.soundFont {
			s.applySoundFont(*sf)
		}
	}
	phase := s.calSnapshot().Phase
	if phase != s.lastPhase {
		s.lastPhase = phase
		if phase != calRunning {

			s.notice = ""
		}
		if phase == calDone {
			s.refreshOffset()
		}
	}
}

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
			return
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

func SameAudioInterface(captureName, playbackName string) bool {
	return sameAudioInterface(captureName, playbackName)
}

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

func (s *Settings) framesText(frames int) string {
	rate := s.rate
	if rate <= 0 {
		rate = settingsDefaultRate
	}
	return fmt.Sprintf("%.1f ms (%d frames)", float64(frames)*1000/float64(rate), frames)
}

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

	return s + "  <- selected"
}

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

func (s *Settings) soundFontNote() (string, color.RGBA) {
	switch {
	case s.sfBusy:
		return "a file dialog is open: finish or cancel it, it may be sitting behind this window", colClose
	case s.pick == nil:
		return "no file dialog is available in this build; -sf2 on the command line still works", colBarline
	}
	return "", colBarline
}

func (s *Settings) soundFontText() string {
	if s.soundFont == "" {
		return "built-in pluck"
	}
	return s.soundFont
}

const settingsWeakConfidence = 0.7

type settingsConfidencer interface {
	StoredConfidence(captureID, playbackID string) (float64, bool)
}

func (s *Settings) storedConfidence() (float64, bool) {
	c, ok := s.audio().(settingsConfidencer)
	if !ok {
		return 0, false
	}
	capID, playID := s.selectedIDs()
	return c.StoredConfidence(capID, playID)
}

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

		return "measurement failed", colMiss
	}
	return s.storedCalibrationText()
}

func calFailureAdvice(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimPrefix(err.Error(), "latency: ")
}

func (s *Settings) saveErrLine() (string, bool) {
	if s.saveErr == nil {
		return "", false
	}
	const label = "SAVE FAILED: "
	return label + ellipsizeW(s.saveErr.Error(), settingsFooterW-textW(label)), true
}

func (s *Settings) configText() string {
	if s.configPath == "" {
		return "config file: location unknown"
	}
	return "config file: " + s.configPath
}

const (
	settingsLeft   = uiPadX
	settingsValueX = 300.0
	settingsLineH  = uiLineH
	settingsRowH   = uiRowH

	settingsWrap = screenW - settingsValueX - uiPadX

	settingsBtnH   = 22.0
	settingsBtnPad = 8.0
	settingsBtnGap = 6.0

	settingsValueGap = 12.0

	settingsFooterW = screenW - 2*settingsLeft

	settingsNoticeLines = 3
	settingsSfNoteLines = 1

	settingsCalHintLines = 2
)

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

type settingsButton struct {
	label string
	r     rect
	act   func(*Settings)

	disabled bool
}

type settingsItem struct {
	kind settingsItemKind
	y    float64
	text string
	col  color.RGBA

	row     int
	label   string
	buttons []settingsButton
	prog    float64

	valueW float64
}

func (it settingsItem) valueText() string { return truncateW(it.text, it.valueW) }

func (it settingsItem) band() rect {
	return rect{settingsLeft - 8, it.y - 5, screenW - 2*settingsLeft + 16, settingsRowH - 6}
}

type itemsBuilder struct {
	out []settingsItem
	y   float64
	row int
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

func (b *itemsBuilder) reserveNote(lines int, text string, col color.RGBA) {
	top := b.y
	if text != "" {
		wrapped := wrapTextW(text, settingsWrap)
		if len(wrapped) > lines {

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

func (b *itemsBuilder) progress(f float64) {
	b.out = append(b.out, settingsItem{kind: siProgress, y: b.y, prog: f, row: -1})
	b.y += settingsLineH + 4
}

func (b *itemsBuilder) addRow(label, value string, col color.RGBA, btns ...settingsButton) {
	x := screenW - uiPadX
	for i := len(btns) - 1; i >= 0; i-- {
		w := textW(btns[i].label) + 2*settingsBtnPad
		x -= w
		btns[i].r = rect{x, b.y - 4, w, settingsBtnH}
		x -= settingsBtnGap
	}

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

			b.y += float64(settingsCalHintLines-1) * settingsLineH
		} else {
			b.reserveNote(settingsCalHintLines, calSetupHint, colBarline)
		}
	} else {
		b.note("unavailable without a capture and playback device", colBarline)
	}

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

func (s *Settings) focusRow(kind settingsRow) {
	if i := s.rowIndex(kind); i >= 0 {
		s.cur = i
	}
}

func (s *Settings) adjustRow(kind settingsRow, d int) {
	s.focusRow(kind)
	s.adjust(d)
}

func (s *Settings) rowIndex(kind settingsRow) int {
	for i, r := range s.rows {
		if r == kind {
			return i
		}
	}
	return -1
}

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

func (s *Settings) hintLine() string { return hintLineOf(s.settingsBindings()) }

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

			fill, edge, tc = colBG, colBarline, colBarline
		case s.ptr.over(btn.r):
			fill, edge, tc = colHover, colDim, colNote
		}
		drawPanel(dst, btn.r, fill, edge)
		drawText(dst, btn.label, centreX(btn.label, btn.r.x, btn.r.w), settingsBtnTextY(btn.r), tc)
	}
}

func settingsBtnTextY(r rect) float64 { return r.y + (r.h-uiTextH)/2 }
