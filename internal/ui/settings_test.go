package ui

// The settings screen is exercised entirely through its state methods —
// no window is ever opened. Everything Draw shows comes from the text
// projections asserted here (deviceText, calibrationText, soundFontText,
// splitDeviceWarning), so covering them covers the screen.

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// ---- fakes ---------------------------------------------------------------

// settingsStubScreen is a do-nothing root for the Shell under test.
type settingsStubScreen struct{}

func (settingsStubScreen) Update() error              { return nil }
func (settingsStubScreen) Draw(*ebiten.Image)         {}
func (settingsStubScreen) Layout(int, int) (int, int) { return screenW, screenH }

// settingsFakePrefs is an in-memory Prefs that counts saves and can be
// made to fail them.
type settingsFakePrefs struct {
	recents   []string
	soundFont string
	countIn   int
	capID     string
	playID    string
	saveErr   error
	saves     int
}

func (p *settingsFakePrefs) Recents() []string         { return p.recents }
func (p *settingsFakePrefs) AddRecent(path string)     { p.recents = append([]string{path}, p.recents...) }
func (p *settingsFakePrefs) SoundFont() string         { return p.soundFont }
func (p *settingsFakePrefs) SetSoundFont(path string)  { p.soundFont = path }
func (p *settingsFakePrefs) CountIn() int              { return p.countIn }
func (p *settingsFakePrefs) SetCountIn(beats int)      { p.countIn = beats }
func (p *settingsFakePrefs) Devices() (string, string) { return p.capID, p.playID }
func (p *settingsFakePrefs) SetDevices(c, pl string)   { p.capID, p.playID = c, pl }
func (p *settingsFakePrefs) Save() error               { p.saves++; return p.saveErr }

// settingsPathPrefs adds the optional Path method, so the footer can find
// the config file without the integrator wiring it explicitly.
type settingsPathPrefs struct {
	settingsFakePrefs
	path string
}

func (p *settingsPathPrefs) Path() string { return p.path }

type settingsOffset struct {
	frames int
	ok     bool
}

// settingsFakeAudio is a scripted AudioServices. Calibrate reports the
// scripted progress values, signals reached, then blocks on release until
// the test lets it finish — which is what makes the idle/running/finished
// transitions observable without any timing assumptions.
type settingsFakeAudio struct {
	capture  []DeviceOption
	playback []DeviceOption
	devErr   error
	rate     int

	steps   []float64
	reached chan struct{}
	release chan struct{}
	frames  int
	conf    float64
	calErr  error

	mu       sync.Mutex
	offsets  map[string]settingsOffset
	calls    int
	lastCap  string
	lastPlay string
}

func (a *settingsFakeAudio) BackendName() string { return "fake" }

func (a *settingsFakeAudio) Devices() ([]DeviceOption, []DeviceOption, error) {
	return a.capture, a.playback, a.devErr
}

func (a *settingsFakeAudio) SampleRate() int { return a.rate }

func (a *settingsFakeAudio) CalibratedOffset(captureID, playbackID string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.offsets[captureID+"|"+playbackID]
	return o.frames, o.ok
}

func (a *settingsFakeAudio) Calibrate(captureID, playbackID string, progress func(float64)) (int, float64, error) {
	a.mu.Lock()
	a.calls++
	a.lastCap, a.lastPlay = captureID, playbackID
	a.mu.Unlock()

	for _, p := range a.steps {
		progress(p)
	}
	if a.reached != nil {
		a.reached <- struct{}{} // progress is now published
	}
	if a.release != nil {
		<-a.release
	}
	if a.calErr != nil {
		return 0, 0, a.calErr
	}
	a.mu.Lock()
	if a.offsets == nil {
		a.offsets = map[string]settingsOffset{}
	}
	a.offsets[captureID+"|"+playbackID] = settingsOffset{a.frames, true}
	a.mu.Unlock()
	return a.frames, a.conf, nil
}

func (a *settingsFakeAudio) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// settingsDevices is a realistic Windows enumeration: an onboard Realtek
// codec (the system default on both sides) and a Focusrite interface.
func settingsDevices() ([]DeviceOption, []DeviceOption) {
	capture := []DeviceOption{
		{ID: "cap-focus", Name: "Focusrite USB (Focusrite USB Audio)"},
		{ID: "cap-realtek", Name: "Microphone (Realtek(R) Audio)", Default: true},
	}
	playback := []DeviceOption{
		{ID: "play-realtek", Name: "Speakers (Realtek(R) Audio)", Default: true},
		{ID: "play-focus", Name: "Speakers (Focusrite USB Audio)"},
	}
	return capture, playback
}

// newSettingsFixture builds a Settings over a Shell with the given audio
// services (nil is a valid choice) and a fresh in-memory Prefs.
func newSettingsFixture(t *testing.T, audio AudioServices) (*Settings, *settingsFakePrefs) {
	t.Helper()
	p := &settingsFakePrefs{}
	var svc Services
	svc.Prefs = p
	if audio != nil {
		svc.Audio = audio
	}
	sh := NewShell(svc, settingsStubScreen{})
	return NewSettings(sh), p
}

// newSettingsAudio builds a fake backend over the realistic device lists.
func newSettingsAudio() *settingsFakeAudio {
	capture, playback := settingsDevices()
	return &settingsFakeAudio{capture: capture, playback: playback, rate: 48000}
}

// settingsWaitPhase polls the snapshot until the calibration reaches want.
func settingsWaitPhase(t *testing.T, s *Settings, want calPhase) calSnap {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sn := s.calSnapshot(); sn.Phase == want {
			return sn
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("calibration never reached phase %v (stuck at %v)", want, s.calSnapshot().Phase)
	return calSnap{}
}

// ---- navigation ----------------------------------------------------------

// TestSettingsRowNavigation: with a full set of services there are five
// rows, Up from the first wraps to the last, and Down from the last wraps
// to the first.
func TestSettingsRowNavigation(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	want := []settingsRow{srCapture, srPlayback, srCalibrate, srSoundFont, srCountIn}
	if len(s.rows) != len(want) {
		t.Fatalf("rows = %v, want %v", s.rows, want)
	}
	for i, r := range want {
		if s.rows[i] != r {
			t.Fatalf("row %d = %v, want %v", i, s.rows[i], r)
		}
	}
	if s.cur != 0 {
		t.Fatalf("initial cursor = %d, want 0", s.cur)
	}
	for i := 1; i < len(want); i++ {
		s.moveCursor(+1)
		if s.cur != i {
			t.Fatalf("after %d downs cursor = %d, want %d", i, s.cur, i)
		}
	}
	s.moveCursor(+1)
	if s.cur != 0 {
		t.Errorf("down from the last row = %d, want wrap to 0", s.cur)
	}
	s.moveCursor(-1)
	if s.cur != len(want)-1 {
		t.Errorf("up from the first row = %d, want wrap to %d", s.cur, len(want)-1)
	}
	// A jump larger than the list still lands in range.
	s.moveCursor(+37)
	if s.cur < 0 || s.cur >= len(want) {
		t.Errorf("cursor out of range after a big jump: %d", s.cur)
	}
}

// TestSettingsNilAudioDegrades: with no audio services the device and
// calibration rows are absent, the section explains itself, and every
// input is inert rather than fatal.
func TestSettingsNilAudioDegrades(t *testing.T) {
	s, _ := newSettingsFixture(t, nil)
	if s.hasDevices() {
		t.Fatal("hasDevices with no audio services")
	}
	for _, r := range s.rows {
		if r == srCapture || r == srPlayback || r == srCalibrate {
			t.Fatalf("device/calibration row %v present with no audio services", r)
		}
	}
	if len(s.rows) != 2 {
		t.Fatalf("rows = %v, want just soundfont and count-in", s.rows)
	}
	expl := s.audioUnavailableText()
	if len(expl) == 0 || !strings.Contains(strings.ToLower(strings.Join(expl, " ")), "unavailable") {
		t.Errorf("explanation = %q, want a reason live input is unavailable", expl)
	}
	if _, ok := s.splitDeviceWarning(); ok {
		t.Error("split-device warning with no devices")
	}
	if s.startCalibration() {
		t.Error("calibration started with no audio services")
	}
	// Drive every row through every input; nothing may panic.
	for i := range s.rows {
		s.cur = i
		s.adjust(-1)
		s.adjust(+1)
		s.activate()
	}
	s.syncSettings()
	if txt, _ := s.calibrationText(s.calSnapshot()); txt == "" {
		t.Error("empty calibration text with no audio services")
	}
}

// TestSettingsEmptyDeviceListsDegrade: a backend that enumerates nothing
// (or fails to) is treated like no backend — an explanation, never an
// empty picker.
func TestSettingsEmptyDeviceListsDegrade(t *testing.T) {
	for _, c := range []struct {
		name  string
		audio *settingsFakeAudio
		want  string
	}{
		{"enumeration error", &settingsFakeAudio{devErr: errors.New("WASAPI enumeration failed")}, "WASAPI"},
		{"no devices at all", &settingsFakeAudio{}, "no capture or playback"},
		{"no capture", &settingsFakeAudio{playback: []DeviceOption{{ID: "p", Name: "Speakers"}}}, "no capture devices"},
		{"no playback", &settingsFakeAudio{capture: []DeviceOption{{ID: "c", Name: "Microphone"}}}, "no playback devices"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newSettingsFixture(t, c.audio)
			if s.hasDevices() {
				t.Fatal("hasDevices with an unusable enumeration")
			}
			if len(s.rows) != 2 {
				t.Fatalf("rows = %v, want just soundfont and count-in", s.rows)
			}
			got := strings.Join(s.audioUnavailableText(), " ")
			if !strings.Contains(got, c.want) {
				t.Errorf("explanation = %q, want it to mention %q", got, c.want)
			}
			if s.startCalibration() {
				t.Error("calibration started with no usable device pair")
			}
		})
	}
}

// ---- devices -------------------------------------------------------------

// TestSameAudioInterface pins the same-interface heuristic against real
// Windows endpoint names.
func TestSameAudioInterface(t *testing.T) {
	for _, c := range []struct {
		name    string
		capture string
		play    string
		same    bool
	}{
		{"same Focusrite on both sides", "Focusrite USB (Focusrite USB Audio)", "Speakers (Focusrite USB Audio)", true},
		{"onboard Realtek on both sides", "Microphone (Realtek(R) Audio)", "Speakers (Realtek(R) Audio)", true},
		{"Focusrite in, onboard out", "Focusrite USB (Focusrite USB Audio)", "Speakers (Realtek(R) Audio)", false},
		{"onboard in, Focusrite out", "Microphone (Realtek(R) Audio)", "Speakers (Focusrite USB Audio)", false},
		{"Scarlett shares a model token", "Line In (Scarlett 2i2 USB)", "Headphones (Scarlett 2i2 USB)", true},
		{"two different interfaces", "Line In (Scarlett 2i2 USB)", "Speakers (Focusrite USB Audio)", false},
		{"identical names", "Digital Audio (S/PDIF) (High Definition Audio Device)", "Digital Audio (S/PDIF) (High Definition Audio Device)", true},
		{"generic dongle both sides", "Microphone (2- USB Audio Device)", "Speakers (2- USB Audio Device)", true},
		{"generic dongle against onboard", "Microphone (USB Audio Device)", "Speakers (Realtek(R) Audio)", false},
		{"no parentheses, same adapter", "UMC204HD 192k", "UMC204HD 192k", true},
		{"unknown capture name never nags", "", "Speakers (Realtek(R) Audio)", true},
		{"unknown playback name never nags", "Microphone (Realtek(R) Audio)", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := sameAudioInterface(c.capture, c.play); got != c.same {
				t.Errorf("sameAudioInterface(%q, %q) = %v, want %v", c.capture, c.play, got, c.same)
			}
		})
	}
}

// TestSettingsDeviceSelection: cycling a picker wraps, writes the new
// pair through Prefs, saves, and raises the split-device warning exactly
// when the two names stop looking like one interface.
func TestSettingsDeviceSelection(t *testing.T) {
	audio := newSettingsAudio()
	s, p := newSettingsFixture(t, audio)

	// No stored preference: both pickers land on the system default,
	// which is the same interface, so there is nothing to warn about.
	if s.capIdx != 1 || s.playIdx != 0 {
		t.Fatalf("initial selection = capture %d, playback %d; want the system defaults (1, 0)", s.capIdx, s.playIdx)
	}
	if w, ok := s.splitDeviceWarning(); ok {
		t.Errorf("warning on a matched pair: %q", w)
	}
	if p.saves != 0 {
		t.Errorf("opening settings saved %d times, want 0", p.saves)
	}

	// Move capture to the Focusrite: now the pair is split.
	s.cur = 0
	s.adjust(+1) // wraps 1 -> 0
	if s.capIdx != 0 {
		t.Fatalf("capture index after right = %d, want wrap to 0", s.capIdx)
	}
	if p.capID != "cap-focus" || p.playID != "play-realtek" {
		t.Errorf("prefs pair = (%q, %q), want (cap-focus, play-realtek)", p.capID, p.playID)
	}
	if p.saves != 1 {
		t.Errorf("saves = %d, want 1 after a device change", p.saves)
	}
	w, ok := s.splitDeviceWarning()
	if !ok {
		t.Fatal("no warning for a Focusrite capture against onboard playback")
	}
	for _, want := range []string{"drift", "different interfaces"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not mention %q", w, want)
		}
	}

	// Move playback to the Focusrite too: matched again, warning gone.
	s.cur = 1
	s.adjust(+1) // 0 -> 1
	if s.playIdx != 1 {
		t.Fatalf("playback index = %d, want 1", s.playIdx)
	}
	if p.capID != "cap-focus" || p.playID != "play-focus" {
		t.Errorf("prefs pair = (%q, %q), want both Focusrite", p.capID, p.playID)
	}
	if w, ok := s.splitDeviceWarning(); ok {
		t.Errorf("warning on a matched Focusrite pair: %q", w)
	}

	// Left wraps backwards.
	s.cur = 0
	s.adjust(-1)
	if s.capIdx != 1 {
		t.Errorf("capture index after left = %d, want wrap to 1", s.capIdx)
	}
}

// TestSettingsDeviceTextMarksDefaultAndSelection: the picker's rendered
// value names the device, flags the system default, and marks the pair
// currently in use.
func TestSettingsDeviceTextMarksDefaultAndSelection(t *testing.T) {
	capture, playback := settingsDevices()
	got := deviceText(capture, 1)
	for _, want := range []string{"Microphone (Realtek(R) Audio)", "system default", "selected", "[2/2]"} {
		if !strings.Contains(got, want) {
			t.Errorf("deviceText = %q, want it to contain %q", got, want)
		}
	}
	if got := deviceText(playback, 1); strings.Contains(got, "system default") {
		t.Errorf("non-default device marked as the system default: %q", got)
	}
	if got := deviceText(nil, -1); got == "" {
		t.Error("deviceText on an empty list must still say something")
	}
}

// TestSettingsResolveDevice: a stored device that has been unplugged
// falls back to the system default rather than to nothing.
func TestSettingsResolveDevice(t *testing.T) {
	capture, _ := settingsDevices()
	for _, c := range []struct {
		name string
		id   string
		want int
	}{
		{"stored ID present", "cap-focus", 0},
		{"stored ID unplugged", "cap-gone", 1},
		{"no stored ID", "", 1},
	} {
		if got := resolveDevice(capture, c.id); got != c.want {
			t.Errorf("%s: resolveDevice = %d, want %d", c.name, got, c.want)
		}
	}
	if got := resolveDevice(nil, "x"); got != -1 {
		t.Errorf("resolveDevice on an empty list = %d, want -1", got)
	}
	// With no default flagged anywhere, the first entry wins.
	plain := []DeviceOption{{ID: "a"}, {ID: "b"}}
	if got := resolveDevice(plain, "zzz"); got != 0 {
		t.Errorf("resolveDevice with no default = %d, want 0", got)
	}
}

// TestSettingsStoredPairSelected: a stored pair is what the screen opens
// on, and its stored offset is the one shown.
func TestSettingsStoredPairSelected(t *testing.T) {
	audio := newSettingsAudio()
	audio.offsets = map[string]settingsOffset{"cap-focus|play-focus": {frames: 2400, ok: true}}
	p := &settingsFakePrefs{capID: "cap-focus", playID: "play-focus"}
	sh := NewShell(Services{Prefs: p, Audio: audio}, settingsStubScreen{})
	s := NewSettings(sh)

	if s.capIdx != 0 || s.playIdx != 1 {
		t.Fatalf("selection = (%d, %d), want the stored Focusrite pair (0, 1)", s.capIdx, s.playIdx)
	}
	if !s.offOK || s.offFrames != 2400 {
		t.Fatalf("stored offset = %d frames ok=%v, want 2400, true", s.offFrames, s.offOK)
	}
	txt, _ := s.calibrationText(s.calSnapshot())
	if !strings.Contains(txt, "2400 frames") || !strings.Contains(txt, "50.0 ms") {
		t.Errorf("calibration text = %q, want 2400 frames and 50.0 ms at 48 kHz", txt)
	}

	// Changing the pair drops the stale offset and says so.
	s.cur = 0
	s.adjust(+1)
	if s.offOK {
		t.Error("stale offset kept after changing the device pair")
	}
	txt, _ = s.calibrationText(s.calSnapshot())
	if !strings.Contains(txt, "not measured") {
		t.Errorf("calibration text for an unmeasured pair = %q", txt)
	}
}

// ---- calibration ---------------------------------------------------------

// TestSettingsCalibrationStateMachine drives idle -> running -> success
// with a Calibrate that blocks until the test releases it. While it is
// running the snapshot must show only progress (never a half-written
// result), and a second start must be refused.
func TestSettingsCalibrationStateMachine(t *testing.T) {
	audio := newSettingsAudio()
	audio.steps = []float64{0, 0.5, 1.5} // the last one is clamped
	audio.reached = make(chan struct{})
	audio.release = make(chan struct{})
	audio.frames, audio.conf = 1440, 0.87
	s, _ := newSettingsFixture(t, audio)

	if sn := s.calSnapshot(); sn.Phase != calIdle {
		t.Fatalf("initial phase = %v, want idle", sn.Phase)
	}
	if !s.startCalibration() {
		t.Fatal("first startCalibration refused")
	}
	<-audio.reached // progress has been published; Calibrate is blocked

	sn := s.calSnapshot()
	if sn.Phase != calRunning {
		t.Fatalf("phase during the run = %v, want running", sn.Phase)
	}
	if sn.Progress != 1 {
		t.Errorf("progress = %v, want the clamped 1", sn.Progress)
	}
	if sn.Frames != 0 || sn.Confidence != 0 || sn.Err != nil {
		t.Errorf("partially written result visible while running: %+v", sn)
	}
	if txt, _ := s.calibrationText(sn); !strings.Contains(txt, "measuring") {
		t.Errorf("running text = %q, want it to say it is measuring", txt)
	}

	// A second start while one is in flight is refused, and does not
	// reach the backend.
	if s.startCalibration() {
		t.Error("a second calibration started while one was running")
	}
	if s.startCalibration() {
		t.Error("a third calibration started while one was running")
	}
	if n := audio.callCount(); n != 1 {
		t.Errorf("Calibrate called %d times, want 1", n)
	}
	// The UI stays responsive: navigation still works mid-run.
	s.moveCursor(+1)
	s.syncSettings()
	if s.offOK {
		t.Error("offset published before the run finished")
	}

	close(audio.release)
	sn = settingsWaitPhase(t, s, calDone)
	if sn.Frames != 1440 || sn.Confidence != 0.87 || sn.Err != nil {
		t.Fatalf("result = %+v, want 1440 frames, 0.87 confidence, no error", sn)
	}
	txt, _ := s.calibrationText(sn)
	for _, want := range []string{"1440 frames", "30.0 ms", "87%"} {
		if !strings.Contains(txt, want) {
			t.Errorf("result text = %q, want it to contain %q", txt, want)
		}
	}
	// Once the loop syncs, the stored offset the backend recorded shows.
	s.syncSettings()
	if !s.offOK || s.offFrames != 1440 {
		t.Errorf("stored offset after calibration = %d ok=%v, want 1440, true", s.offFrames, s.offOK)
	}
	// And a new run may now start.
	audio.reached, audio.release = nil, nil
	if !s.startCalibration() {
		t.Error("a fresh calibration refused after the previous one finished")
	}
	settingsWaitPhase(t, s, calDone)

	if audio.lastCap != "cap-realtek" || audio.lastPlay != "play-realtek" {
		t.Errorf("calibrated pair = (%q, %q), want the selected pair", audio.lastCap, audio.lastPlay)
	}
}

// TestSettingsCalibrationFailure: an error from the backend is shown, not
// swallowed, and leaves the screen able to try again.
func TestSettingsCalibrationFailure(t *testing.T) {
	audio := newSettingsAudio()
	audio.reached = make(chan struct{})
	audio.release = make(chan struct{})
	audio.calErr = errors.New("no click detected: check the input level")
	s, _ := newSettingsFixture(t, audio)

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	<-audio.reached
	if s.startCalibration() {
		t.Error("a second calibration started while one was running")
	}
	close(audio.release)

	sn := settingsWaitPhase(t, s, calFailed)
	if sn.Err == nil || sn.Frames != 0 {
		t.Fatalf("failed snapshot = %+v, want an error and no frames", sn)
	}
	txt, col := s.calibrationText(sn)
	if !strings.Contains(txt, "no click detected") {
		t.Errorf("failure text = %q, want the backend's message", txt)
	}
	if col != colMiss {
		t.Errorf("failure color = %v, want colMiss", col)
	}
	s.syncSettings()
	if s.offOK {
		t.Error("offset published after a failed calibration")
	}
	audio.reached, audio.release, audio.calErr = nil, nil, nil
	if !s.startCalibration() {
		t.Error("retry refused after a failure")
	}
	settingsWaitPhase(t, s, calDone)
}

// TestSettingsCalibrationConcurrentReads hammers the snapshot from the
// game loop's side while a run publishes progress from its own goroutine.
// The race detector is the assertion.
func TestSettingsCalibrationConcurrentReads(t *testing.T) {
	audio := newSettingsAudio()
	for i := 0; i < 200; i++ {
		audio.steps = append(audio.steps, float64(i)/200)
	}
	audio.frames, audio.conf = 960, 0.5
	s, _ := newSettingsFixture(t, audio)

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		sn := s.calSnapshot()
		s.syncSettings()
		_, _ = s.calibrationText(sn)
		if sn.Phase == calDone {
			break
		}
		if sn.Phase == calRunning && (sn.Frames != 0 || sn.Err != nil) {
			t.Fatalf("torn read while running: %+v", sn)
		}
		if time.Now().After(deadline) {
			t.Fatal("calibration never finished")
		}
	}
}

// ---- soundfont -----------------------------------------------------------

// TestSettingsSoundFontWithoutPicker: with no picker wired, the row shows
// the current value and Enter clears back to the built-in.
func TestSettingsSoundFontWithoutPicker(t *testing.T) {
	p := &settingsFakePrefs{soundFont: `C:\sf2\ChateauGrand.sf2`}
	sh := NewShell(Services{Prefs: p}, settingsStubScreen{})
	s := NewSettings(sh)

	if got := s.soundFontText(); got != `C:\sf2\ChateauGrand.sf2` {
		t.Fatalf("soundFontText = %q, want the configured path", got)
	}
	s.cur = 0
	if r, _ := s.focused(); r != srSoundFont {
		t.Fatalf("first row = %v, want the soundfont row", r)
	}
	s.activate() // no picker: Enter clears
	if p.soundFont != "" || s.soundFontText() != "built-in pluck" {
		t.Errorf("after clear: prefs %q, text %q", p.soundFont, s.soundFontText())
	}
	if p.saves != 1 {
		t.Errorf("saves = %d, want 1", p.saves)
	}
	s.activate() // already built-in: nothing to do, nothing to save
	if p.saves != 1 {
		t.Errorf("clearing an already-clear soundfont saved again (saves = %d)", p.saves)
	}
}

// TestSettingsSoundFontPicker: the hook is called with the .sf2 filter,
// and a path chosen later — from another goroutine — is applied on the
// game loop's next sync, never inside the callback.
func TestSettingsSoundFontPicker(t *testing.T) {
	s, p := newSettingsFixture(t, nil)
	var gotExts []string
	var chosen func(string)
	s.SetFilePicker(func(exts []string, fn func(string)) {
		gotExts, chosen = exts, fn
	})

	s.cur = 0
	s.activate()
	if len(gotExts) != 1 || gotExts[0] != ".sf2" {
		t.Fatalf("picker extensions = %v, want [.sf2]", gotExts)
	}
	if chosen == nil {
		t.Fatal("picker did not receive a completion callback")
	}
	if p.soundFont != "" {
		t.Error("prefs written before the user chose anything")
	}

	done := make(chan struct{})
	go func() { defer close(done); chosen(`D:\Sounds\Fluid.sf2`) }()
	<-done
	if p.soundFont != "" {
		t.Error("the callback wrote prefs directly instead of posting to the mailbox")
	}
	s.syncSettings()
	if p.soundFont != `D:\Sounds\Fluid.sf2` || s.soundFontText() != `D:\Sounds\Fluid.sf2` {
		t.Errorf("after sync: prefs %q, text %q", p.soundFont, s.soundFontText())
	}
	if p.saves != 1 {
		t.Errorf("saves = %d, want 1", p.saves)
	}
	// A cancelled pick posts nothing, so a later sync changes nothing.
	s.syncSettings()
	if p.saves != 1 {
		t.Errorf("an empty sync saved again (saves = %d)", p.saves)
	}
	// Left still clears back to the built-in.
	s.adjust(-1)
	if p.soundFont != "" {
		t.Errorf("left did not clear the soundfont: %q", p.soundFont)
	}
}

// TestSettingsUpdateDrainsMailbox: Update is wired to syncSettings, so a
// path chosen while another screen was on top lands on the next frame
// without any key being pressed. (Update is the only part of the screen
// that touches ebiten input; with no keys down it is a plain no-op.)
func TestSettingsUpdateDrainsMailbox(t *testing.T) {
	s, p := newSettingsFixture(t, nil)
	var chosen func(string)
	s.SetFilePicker(func(_ []string, fn func(string)) { chosen = fn })
	s.cur = 0
	s.activate()
	chosen(`E:\sf2\Airfont.sf2`)

	if err := s.Update(); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if p.soundFont != `E:\sf2\Airfont.sf2` {
		t.Errorf("soundfont after one Update = %q, want the chosen path", p.soundFont)
	}
}

// ---- count-in ------------------------------------------------------------

// TestSettingsCountInClamps: the count-in stays inside 0..8 however hard
// it is pushed, and Enter wraps past the top so every value is reachable.
func TestSettingsCountInClamps(t *testing.T) {
	s, p := newSettingsFixture(t, nil)
	s.cur = len(s.rows) - 1
	if r, _ := s.focused(); r != srCountIn {
		t.Fatalf("last row = %v, want the count-in row", r)
	}
	for i := 0; i < 20; i++ {
		s.adjust(+1)
	}
	if s.countIn != maxCountIn || p.countIn != maxCountIn {
		t.Errorf("count-in after 20 rights = %d (prefs %d), want %d", s.countIn, p.countIn, maxCountIn)
	}
	for i := 0; i < 20; i++ {
		s.adjust(-1)
	}
	if s.countIn != 0 || p.countIn != 0 {
		t.Errorf("count-in after 20 lefts = %d (prefs %d), want 0", s.countIn, p.countIn)
	}
	// No save for a no-op adjustment at the clamp.
	saves := p.saves
	s.adjust(-1)
	if p.saves != saves {
		t.Errorf("adjusting at the clamp saved again (saves = %d, was %d)", p.saves, saves)
	}
	// Enter steps up and wraps at the top.
	for i := 1; i <= maxCountIn; i++ {
		s.activate()
		if s.countIn != i {
			t.Fatalf("count-in after %d enters = %d, want %d", i, s.countIn, i)
		}
	}
	s.activate()
	if s.countIn != 0 {
		t.Errorf("enter at %d beats = %d, want a wrap to 0", maxCountIn, s.countIn)
	}
	// An out-of-range stored value is clamped when the screen opens.
	p.countIn = 99
	sh := NewShell(Services{Prefs: p}, settingsStubScreen{})
	if got := NewSettings(sh).countIn; got != maxCountIn {
		t.Errorf("stored 99 opened as %d, want %d", got, maxCountIn)
	}
}

// ---- persistence ---------------------------------------------------------

// TestSettingsSaveErrorSurfaces: a Prefs whose Save fails shows the error
// and keeps working — the change is applied in memory and the next
// successful save clears the message.
func TestSettingsSaveErrorSurfaces(t *testing.T) {
	s, p := newSettingsFixture(t, nil)
	p.saveErr = errors.New("open config.json: access is denied")

	s.cur = len(s.rows) - 1
	s.adjust(+1)
	if s.saveErr == nil {
		t.Fatal("save error swallowed")
	}
	if !strings.Contains(s.saveErr.Error(), "access is denied") {
		t.Errorf("save error = %v, want the write failure", s.saveErr)
	}
	if s.countIn != 1 {
		t.Errorf("count-in = %d, want the change applied in memory despite the failed save", s.countIn)
	}
	// Still responsive, and a later success clears the message.
	p.saveErr = nil
	s.adjust(+1)
	if s.saveErr != nil {
		t.Errorf("save error %v survived a successful save", s.saveErr)
	}
}

// TestSettingsConfigPath: the footer finds the config location from a
// Prefs that exposes Path, and SetConfigPath covers the ones that do not.
func TestSettingsConfigPath(t *testing.T) {
	s, _ := newSettingsFixture(t, nil)
	if got := s.configText(); !strings.Contains(got, "unknown") {
		t.Errorf("configText with no path = %q, want it to admit the location is unknown", got)
	}
	s.SetConfigPath(`C:\Users\p\AppData\Roaming\guitarTutor\config.json`)
	if got := s.configText(); !strings.Contains(got, `AppData\Roaming\guitarTutor\config.json`) {
		t.Errorf("configText after SetConfigPath = %q", got)
	}

	p := &settingsPathPrefs{path: `C:\cfg\guitarTutor.json`}
	sh := NewShell(Services{Prefs: p}, settingsStubScreen{})
	if got := NewSettings(sh).configText(); !strings.Contains(got, `C:\cfg\guitarTutor.json`) {
		t.Errorf("configText from a Prefs with Path = %q", got)
	}
}

// TestSettingsSampleRate: the offset is rendered in the backend's own
// sample rate when it reports one, and at the project default otherwise.
func TestSettingsSampleRate(t *testing.T) {
	audio := newSettingsAudio()
	audio.rate = 44100
	s, _ := newSettingsFixture(t, audio)
	if got := s.framesText(4410); !strings.Contains(got, "100.0 ms") {
		t.Errorf("framesText at 44.1 kHz = %q, want 100.0 ms", got)
	}
	audio.rate = 0 // backend does not know: fall back to 48000
	s2, _ := newSettingsFixture(t, audio)
	if got := s2.framesText(4800); !strings.Contains(got, "100.0 ms") {
		t.Errorf("framesText at the default rate = %q, want 100.0 ms", got)
	}
	s2.SetSampleRate(0) // ignored
	s2.SetSampleRate(96000)
	if got := s2.framesText(9600); !strings.Contains(got, "100.0 ms") {
		t.Errorf("framesText after SetSampleRate = %q, want 100.0 ms", got)
	}
}

// TestSettingsNilPrefs: a Services with no Prefs is degenerate but must
// not crash the screen.
func TestSettingsNilPrefs(t *testing.T) {
	sh := NewShell(Services{}, settingsStubScreen{})
	s := NewSettings(sh)
	for i := range s.rows {
		s.cur = i
		s.adjust(+1)
		s.adjust(-1)
		s.activate()
	}
	s.syncSettings()
	if s.saveErr != nil {
		t.Errorf("save error with no Prefs: %v", s.saveErr)
	}
}

// ---- text helpers --------------------------------------------------------

// TestSettingsWrapText: the warning wraps on spaces without dropping or
// duplicating words, and never exceeds the width.
func TestSettingsWrapText(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	s.cur = 0
	s.adjust(+1) // split the pair so there is a warning to wrap
	w, ok := s.splitDeviceWarning()
	if !ok {
		t.Fatal("expected a split-device warning")
	}
	lines := wrapText(w, 60)
	if len(lines) < 2 {
		t.Fatalf("warning wrapped to %d lines at width 60", len(lines))
	}
	for _, l := range lines {
		if len(l) > 60 {
			t.Errorf("line exceeds the width: %q", l)
		}
	}
	if got := strings.Join(lines, " "); got != w {
		t.Errorf("wrapped text = %q, want the original %q", got, w)
	}
	if wrapText("   ", 10) != nil {
		t.Error("wrapText on blank input should produce no lines")
	}
}

// TestSettingsDeviceInterfaceName pins the parenthesis parsing the
// same-interface heuristic depends on, including the nested "(R)".
func TestSettingsDeviceInterfaceName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Speakers (Realtek(R) Audio)", "Realtek(R) Audio"},
		{"Microphone (Focusrite USB Audio)", "Focusrite USB Audio"},
		{"UMC204HD 192k", "UMC204HD 192k"},
		{"Broken (unclosed", "unclosed"},
		{"", ""},
	} {
		if got := deviceInterfaceName(c.in); got != c.want {
			t.Errorf("deviceInterfaceName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
