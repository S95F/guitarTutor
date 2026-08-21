package ui

// The settings screen is exercised entirely through its state methods —
// no window is ever opened. Everything Draw shows comes from the text
// projections asserted here (deviceText, calibrationText, soundFontText,
// splitDeviceWarning), so covering them covers the screen.

import (
	"context"
	"errors"
	"fmt"
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
	created   []string
	hideHint  bool
	soundFont string
	countIn   int
	capID     string
	playID    string
	saveErr   error
	saves     int
}

func (p *settingsFakePrefs) Recents() []string         { return p.recents }
func (p *settingsFakePrefs) AddRecent(path string)     { p.recents = append([]string{path}, p.recents...) }
func (p *settingsFakePrefs) Created() []string         { return p.created }
func (p *settingsFakePrefs) AddCreated(path string)    { p.created = append([]string{path}, p.created...) }
func (p *settingsFakePrefs) HintHidden() bool          { return p.hideHint }
func (p *settingsFakePrefs) SetHintHidden(h bool)      { p.hideHint = h }
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

// settingsBlockingAudio is an AudioServices whose calibration blocks until
// the test releases it or the run's context is cancelled — the two ways a
// real measurement ends. It offers the cancellable entry point, so it
// stands in for a backend that hands the device back when asked. Setting
// refuse makes Calibrate decline at once, which is how the device's own
// cross-instance guard turns a second screen's request down.
type settingsBlockingAudio struct {
	capture  []DeviceOption
	playback []DeviceOption

	started chan struct{} // one value per entered call
	release chan struct{} // closed to let a blocked run succeed

	frames int
	conf   float64

	mu        sync.Mutex
	refuse    error
	calls     int
	cancelled int
	returned  int
	offsets   map[string]settingsOffset
}

func newSettingsBlockingAudio() *settingsBlockingAudio {
	capture, playback := settingsDevices()
	return &settingsBlockingAudio{
		capture:  capture,
		playback: playback,
		started:  make(chan struct{}, 8),
		release:  make(chan struct{}),
		frames:   1440,
		conf:     0.9,
	}
}

func (a *settingsBlockingAudio) BackendName() string { return "blocking" }

func (a *settingsBlockingAudio) Devices() ([]DeviceOption, []DeviceOption, error) {
	return a.capture, a.playback, nil
}

func (a *settingsBlockingAudio) SampleRate() int { return 48000 }

func (a *settingsBlockingAudio) CalibratedOffset(captureID, playbackID string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.offsets[captureID+"|"+playbackID]
	return o.frames, o.ok
}

func (a *settingsBlockingAudio) Calibrate(captureID, playbackID string, progress func(float64)) (int, float64, error) {
	return a.CalibrateContext(context.Background(), captureID, playbackID, progress)
}

func (a *settingsBlockingAudio) CalibrateContext(ctx context.Context, captureID, playbackID string, progress func(float64)) (int, float64, error) {
	a.mu.Lock()
	a.calls++
	refuse := a.refuse
	a.mu.Unlock()
	if refuse != nil {
		return 0, 0, refuse
	}
	a.started <- struct{}{}
	progress(0.5)
	select {
	case <-ctx.Done():
		a.mu.Lock()
		a.cancelled++
		a.returned++
		a.mu.Unlock()
		return 0, 0, ctx.Err()
	case <-a.release:
	}
	a.mu.Lock()
	if a.offsets == nil {
		a.offsets = map[string]settingsOffset{}
	}
	a.offsets[captureID+"|"+playbackID] = settingsOffset{a.frames, true}
	a.returned++
	a.mu.Unlock()
	return a.frames, a.conf, nil
}

func (a *settingsBlockingAudio) stats() (calls, cancelled, returned int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.cancelled, a.returned
}

func (a *settingsBlockingAudio) setRefusal(err error) {
	a.mu.Lock()
	a.refuse = err
	a.mu.Unlock()
}

// settingsPlainAudio exposes only the AudioServices methods, hiding the
// cancellable entry point: a backend that cannot be interrupted. The inner
// services are held in a field rather than embedded precisely so
// CalibrateContext is not promoted.
type settingsPlainAudio struct{ inner *settingsBlockingAudio }

func (a *settingsPlainAudio) BackendName() string { return a.inner.BackendName() }

func (a *settingsPlainAudio) Devices() ([]DeviceOption, []DeviceOption, error) {
	return a.inner.Devices()
}

func (a *settingsPlainAudio) CalibratedOffset(captureID, playbackID string) (int, bool) {
	return a.inner.CalibratedOffset(captureID, playbackID)
}

func (a *settingsPlainAudio) Calibrate(captureID, playbackID string, progress func(float64)) (int, float64, error) {
	return a.inner.Calibrate(captureID, playbackID, progress)
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
	got := deviceText(capture, 1, devChosen)
	for _, want := range []string{"Microphone (Realtek(R) Audio)", "system default", "selected", "[2/2]"} {
		if !strings.Contains(got, want) {
			t.Errorf("deviceText = %q, want it to contain %q", got, want)
		}
	}
	if got := deviceText(playback, 1, devChosen); strings.Contains(got, "system default") {
		t.Errorf("non-default device marked as the system default: %q", got)
	}
	if got := deviceText(nil, -1, devUnchosen); got == "" {
		t.Error("deviceText on an empty list must still say something")
	}
	// A fallback for an unplugged device IS what a piece will use, so it
	// keeps the selected marker; the note beside the row carries the
	// caveat (audit C3).
	if got := deviceText(capture, 1, devFallback); !strings.Contains(got, "selected") {
		t.Errorf("fallback text = %q, want it to still say what is in use", got)
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
	if !strings.Contains(txt, "measurement failed") {
		t.Errorf("failure text = %q, want it to state the outcome", txt)
	}
	if strings.Contains(txt, "no click detected") {
		t.Errorf("failure text = %q: the advice belongs in the notice band, where it can wrap", txt)
	}
	if col != colMiss {
		t.Errorf("failure color = %v, want colMiss", col)
	}
	s.syncSettings()
	if s.offOK {
		t.Error("offset published after a failed calibration")
	}
	// Once the loop syncs — clearing the refused-second-run notice — the
	// band under the section carries the backend's advice.
	if !settingsSays(s, "no click detected") {
		t.Errorf("the backend's advice never reached the screen: %q", settingsNoteLines(s))
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

// TestSettingsCloseCancelsCalibration is the fix for a calibration that
// outlived the screen that started it: Escape popped the screen while the
// run kept the capture and playback devices for the rest of its timeout,
// so a piece opened seconds later collided with a live duplex stream.
// Close must cancel the run, wait for the goroutine, and leave nothing
// that can write back into the popped screen.
func TestSettingsCloseCancelsCalibration(t *testing.T) {
	audio := newSettingsBlockingAudio()
	s, _ := newSettingsFixture(t, audio)

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	<-audio.started
	if sn := s.calSnapshot(); sn.Phase != calRunning {
		t.Fatalf("phase = %v, want running", sn.Phase)
	}
	run := s.run
	if run == nil {
		t.Fatal("the screen kept no handle on the run it started")
	}

	start := time.Now()
	s.Close()
	elapsed := time.Since(start)

	// Close waited for the goroutine rather than timing out: a backend
	// that honours the context has already given the devices back.
	select {
	case <-run.done:
	default:
		t.Fatalf("Close returned after %v with the calibration goroutine still running", elapsed)
	}
	if elapsed > 2*settingsCloseGrace {
		t.Errorf("Close blocked for %v, want a bound near %v", elapsed, settingsCloseGrace)
	}
	if _, cancelled, _ := audio.stats(); cancelled != 1 {
		t.Errorf("backend cancellations = %d, want 1: Close must ask for the device back", cancelled)
	}
	if !run.abandonedNow() {
		t.Error("the run was not marked abandoned, so it may still publish into a popped screen")
	}
	if sn := s.calSnapshot(); sn.Phase != calIdle {
		t.Errorf("phase after Close = %v, want idle", sn.Phase)
	}
	// The goroutine finished after the screen let go, and wrote nothing
	// anyone can see.
	if got := run.snapshot(); got.Phase != calRunning || got.Frames != 0 {
		t.Errorf("an abandoned run published %+v, want nothing", got)
	}
	s.syncSettings()
	if s.offOK {
		t.Error("an abandoned run's offset reached the screen")
	}
	s.Close() // idempotent, and safe with no run in flight
	if _, cancelled, _ := audio.stats(); cancelled != 1 {
		t.Errorf("a second Close touched the backend again (cancellations = %d)", cancelled)
	}
}

// TestSettingsCloseIsBoundedWithoutCancellation: a backend that ignores
// cancellation must not hold the game loop. Close gives up waiting, but
// the run is detached all the same, so its late result lands nowhere.
func TestSettingsCloseIsBoundedWithoutCancellation(t *testing.T) {
	inner := newSettingsBlockingAudio()
	s, _ := newSettingsFixture(t, &settingsPlainAudio{inner: inner})

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	<-inner.started
	run := s.run
	if run == nil {
		t.Fatal("the screen kept no handle on the run it started")
	}

	start := time.Now()
	s.Close()
	if elapsed := time.Since(start); elapsed > 5*settingsCloseGrace {
		t.Errorf("Close blocked the game loop for %v on a backend that ignores cancellation", elapsed)
	}
	if !run.abandonedNow() {
		t.Fatal("the run was not detached")
	}
	if sn := s.calSnapshot(); sn.Phase != calIdle {
		t.Errorf("phase after Close = %v, want idle", sn.Phase)
	}

	// The run finishes long after the screen is gone.
	close(inner.release)
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the calibration goroutine never returned")
	}
	if got := run.snapshot(); got.Phase != calRunning || got.Frames != 0 {
		t.Errorf("a run that outlived its screen published %+v, want nothing", got)
	}
	s.syncSettings()
	if s.offOK || s.calSnapshot().Phase != calIdle {
		t.Errorf("a run that outlived its screen wrote into it: offOK=%v phase=%v", s.offOK, s.calSnapshot().Phase)
	}
}

// TestSettingsStaleResultIsNotShownForANewPair: a measurement describes
// the pair it was taken on. Changing the pair mid-run used to publish the
// old pair's result against the new one, so the screen claimed a device
// combination was calibrated when it never had been.
func TestSettingsStaleResultIsNotShownForANewPair(t *testing.T) {
	audio := newSettingsAudio()
	audio.reached = make(chan struct{})
	audio.release = make(chan struct{})
	audio.frames, audio.conf = 1440, 0.9
	s, _ := newSettingsFixture(t, audio)

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	<-audio.reached
	measuredCap, measuredPlay := s.selectedIDs()

	// The pair changes while the measurement is in flight. The input layer
	// refuses this mid-run; the state method is driven directly, because
	// the result must stay bound to its pair however the pair changes.
	s.cycleCapture(+1)
	if capID, _ := s.selectedIDs(); capID == measuredCap {
		t.Fatalf("capture device did not change (still %q)", capID)
	}

	// Even the running run's progress must not read as this pair's.
	txt, _ := s.calibrationText(s.calSnapshot())
	if !strings.Contains(txt, "previous pair") {
		t.Errorf("text while measuring a superseded pair = %q, want it to say which pair is being measured", txt)
	}

	close(audio.release)
	settingsWaitPhase(t, s, calDone)
	s.syncSettings()

	txt, _ = s.calibrationText(s.calSnapshot())
	if strings.Contains(txt, "1440") || strings.Contains(txt, "confidence") {
		t.Errorf("a result measured on %q is shown for the pair now selected: %q", measuredCap, txt)
	}
	if !strings.Contains(txt, "not measured") {
		t.Errorf("text for the unmeasured pair = %q, want it to admit there is no measurement", txt)
	}

	// Back on the pair it was measured for, the stored value is shown.
	s.cycleCapture(-1)
	if capID, playID := s.selectedIDs(); capID != measuredCap || playID != measuredPlay {
		t.Fatalf("selection = (%q, %q), want the measured pair", capID, playID)
	}
	txt, _ = s.calibrationText(s.calSnapshot())
	if !strings.Contains(txt, "1440 frames") {
		t.Errorf("text back on the measured pair = %q, want the stored 1440 frames", txt)
	}
}

// TestSettingsRefusedCalibrationSaysSo: a refusal is a message, not a
// silent no-op — both the one this screen makes and the one the device's
// owner makes when another screen is already measuring.
func TestSettingsRefusedCalibrationSaysSo(t *testing.T) {
	audio := newSettingsBlockingAudio()
	s, _ := newSettingsFixture(t, audio)

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	<-audio.started
	if s.startCalibration() {
		t.Error("a second calibration started while one was in flight")
	}
	if !strings.Contains(s.notice, "already running") {
		t.Errorf("notice after a refused second run = %q, want it to say one is already running", s.notice)
	}
	if calls, _, _ := audio.stats(); calls != 1 {
		t.Errorf("Calibrate called %d times, want 1", calls)
	}

	// A second visit builds a new screen, so the guard that matters lives
	// with the device. Its refusal comes back as an error and is shown.
	busy := errors.New("a calibration is already running on this device")
	audio.setRefusal(busy)
	s2 := NewSettings(s.sh)
	if !s2.startCalibration() {
		t.Fatal("the second screen never asked the backend")
	}
	sn := settingsWaitPhase(t, s2, calFailed)
	txt, col := s2.calibrationText(sn)
	if !strings.Contains(txt, "measurement failed") {
		t.Errorf("refusal text = %q, want the row to state the outcome", txt)
	}
	if col != colMiss {
		t.Errorf("refusal color = %v, want colMiss", col)
	}
	if !settingsSays(s2, busy.Error()) {
		t.Errorf("the device owner's message never reached the screen: %q", settingsNoteLines(s2))
	}

	s2.Close()
	s.Close()
}

// TestSettingsInputLockedWhileCalibrating: the device rows and the
// count-in are the settings a running measurement depends on, so they
// ignore input until it finishes — visibly, and only until it finishes.
func TestSettingsInputLockedWhileCalibrating(t *testing.T) {
	audio := newSettingsBlockingAudio()
	s, p := newSettingsFixture(t, audio)
	capBefore, playBefore, countBefore := s.capIdx, s.playIdx, s.countIn

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	<-audio.started
	savesBefore := p.saves

	s.cur = 0 // capture
	s.adjust(+1)
	if s.capIdx != capBefore {
		t.Errorf("capture index changed by left/right mid-run: %d, want %d", s.capIdx, capBefore)
	}
	s.activate()
	if s.capIdx != capBefore {
		t.Errorf("capture index changed by enter mid-run: %d, want %d", s.capIdx, capBefore)
	}
	s.cur = 1 // playback
	s.adjust(-1)
	if s.playIdx != playBefore {
		t.Errorf("playback index changed mid-run: %d, want %d", s.playIdx, playBefore)
	}
	s.cur = len(s.rows) - 1 // count-in
	s.adjust(+1)
	s.activate()
	if s.countIn != countBefore || p.countIn != countBefore {
		t.Errorf("count-in changed mid-run: %d (prefs %d), want %d", s.countIn, p.countIn, countBefore)
	}
	if p.saves != savesBefore {
		t.Errorf("a locked row still wrote to prefs (saves = %d, was %d)", p.saves, savesBefore)
	}
	if !strings.Contains(s.notice, "locked") {
		t.Errorf("ignored input left no message: %q", s.notice)
	}
	// Navigation is never locked: the user can still read the screen.
	s.moveCursor(+1)
	s.moveCursor(-1)

	close(audio.release)
	settingsWaitPhase(t, s, calDone)
	s.syncSettings()
	if s.notice != "" {
		t.Errorf("the lock notice outlived the run: %q", s.notice)
	}

	s.cur = 0
	s.adjust(+1)
	if s.capIdx == capBefore {
		t.Error("the capture row is still locked after the run finished")
	}
	if p.saves == savesBefore {
		t.Error("the change accepted after the run was not saved")
	}
}

// TestSettingsCalibrateNowCommitsAnUnchosenPair: on first run the pickers
// show the system defaults with nothing stored, and the one prominent
// button on the screen is "calibrate now". It used to measure that pair
// and store the offset under the concrete device IDs while Prefs stayed
// empty — and the application only opens the live capture path when the
// STORED capture ID is non-empty, so the mouse user who clicked the
// obvious button and watched it go green got playback-only practice with
// no scoring and nothing to explain why. Calibrating a pair is choosing
// it.
func TestSettingsCalibrateNowCommitsAnUnchosenPair(t *testing.T) {
	audio := newSettingsAudio()
	audio.frames, audio.conf = 6240, 0.95
	s, p := newSettingsFixture(t, audio)
	if capID, _ := p.Devices(); capID != "" {
		t.Fatalf("the fixture already holds a capture device (%q)", capID)
	}

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	// The pair is committed on the way in, not when the run finishes: a
	// failed measurement still leaves the user's choice on file.
	if p.capID != "cap-realtek" || p.playID != "play-realtek" {
		t.Errorf("prefs pair = (%q, %q), want the shown defaults committed", p.capID, p.playID)
	}
	if p.saves != 1 {
		t.Errorf("saves = %d, want the committed choice written once", p.saves)
	}
	if st := s.deviceStateOf(srCapture); st != devChosen {
		t.Errorf("capture state after calibrate = %v, want devChosen", st)
	}

	settingsWaitPhase(t, s, calDone)
	s.syncSettings()
	if !s.offOK || s.offFrames != 6240 {
		t.Errorf("stored offset = %d ok=%v, want the measured 6240", s.offFrames, s.offOK)
	}
	if audio.lastCap != "cap-realtek" || audio.lastPlay != "play-realtek" {
		t.Errorf("measured pair = (%q, %q), want the committed one", audio.lastCap, audio.lastPlay)
	}
}

// TestSettingsCalibrateNowKeepsAnUnpluggedSavedDevice: the fallback shown
// for a saved-but-unplugged device is NOT committed by calibrating. The
// saved ID must survive until its interface is plugged back in, so a
// fallback pair keeps the old store-offset-only behaviour.
func TestSettingsCalibrateNowKeepsAnUnpluggedSavedDevice(t *testing.T) {
	for _, c := range []struct {
		name          string
		capID, playID string
	}{
		{"saved capture unplugged", "cap-gone", "play-realtek"},
		{"nothing chosen but saved playback unplugged", "", "play-gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			audio := newSettingsAudio()
			p := &settingsFakePrefs{capID: c.capID, playID: c.playID}
			sh := NewShell(Services{Prefs: p, Audio: audio}, settingsStubScreen{})
			s := NewSettings(sh)

			if !s.startCalibration() {
				t.Fatal("startCalibration refused")
			}
			if p.capID != c.capID || p.playID != c.playID {
				t.Errorf("prefs pair = (%q, %q), want the saved (%q, %q) left for the device's return",
					p.capID, p.playID, c.capID, c.playID)
			}
			if p.saves != 0 {
				t.Errorf("saves = %d, want no write for a fallback pair", p.saves)
			}
			settingsWaitPhase(t, s, calDone)
		})
	}
}

// TestSettingsCalibrationFailureAdviceIsWrappedIntoTheNoticeBand: the
// advice in a measurement failure — what to connect, what to check — runs
// to roughly twice the row's width, so rendering it as the row value cut
// off exactly the part that says what to do. It goes to the fixed notice
// band under the section instead, with the latency package's own prefix
// trimmed, and showing it must not move a single control.
func TestSettingsCalibrationFailureAdviceIsWrappedIntoTheNoticeBand(t *testing.T) {
	const advice = "no click arrivals found in the captured signal: check that the loopback" +
		" (cable, or speaker to mic) is connected, that the right capture device is selected," +
		" and that the input isn't muted"
	audio := newSettingsAudio()
	audio.calErr = errors.New("latency: " + advice)
	s, _ := newSettingsFixture(t, audio)
	tops := settingsRowTops(s)

	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	settingsWaitPhase(t, s, calFailed)

	if !settingsSays(s, advice) {
		t.Errorf("the notice band does not carry the whole advice: %q", settingsNoteLines(s))
	}
	if settingsSays(s, "latency:") {
		t.Errorf("the package prefix leaked to the screen: %q", settingsNoteLines(s))
	}
	if got := settingsRowTops(s); got != tops {
		t.Errorf("showing the advice moved the rows:\n got %s\nwant %s", got, tops)
	}
	for _, l := range settingsNoteLines(s) {
		if textW(l) > settingsWrap {
			t.Errorf("note line measures %.0fpx, past the %.0fpx band width: %q", textW(l), settingsWrap, l)
		}
	}
	// The longest advice the latency package writes needs every line of
	// the band; anything the band cannot hold is cut mid-sentence.
	longest := "the round-trip delay looks like it meets or exceeds the click spacing (24000 frames, 500 ms):" +
		" every click after the first arrived at a consistent delay but the first never did, so each match" +
		" is probably the previous click aliased one spacing late — increase the click spacing beyond the" +
		" largest plausible delay, then run calibration again"
	if got := len(wrapTextW(longest, settingsWrap)); got > settingsNoticeLines {
		t.Errorf("the longest advice wraps to %d lines; the band holds %d", got, settingsNoticeLines)
	}
}

// TestSettingsCalibrationHintSpeaksToAGuitarist: the old hint ("make the
// output audible to the input") was engineer phrasing that let the
// natural setup — guitar plugged in, monitors on — run and fail, because
// a pickup cannot hear the click train. The hint now says what must
// physically happen, in a guitarist's words, and fits the fixed band it
// lives in.
func TestSettingsCalibrationHintSpeaksToAGuitarist(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	for _, want := range []string{"point a mic at your speakers", "cable an output back into an input", "pickup"} {
		if !settingsSays(s, want) {
			t.Errorf("the calibration hint never mentions %q: %q", want, settingsNoteLines(s))
		}
	}
	if settingsSays(s, "make the output audible") {
		t.Error("the engineer phrasing is back")
	}
	if lines := wrapTextW(calSetupHint, settingsWrap); len(lines) > settingsCalHintLines {
		t.Errorf("the hint wraps to %d lines; its band holds %d", len(lines), settingsCalHintLines)
	}
}

// TestSettingsFramesTextLeadsWithMilliseconds: the offset leads with the
// unit a musician can sanity-check — everyone knows what 130 ms of delay
// feels like — and the frame count follows for anyone comparing against
// the config file.
func TestSettingsFramesTextLeadsWithMilliseconds(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	if got, want := s.framesText(6240), "130.0 ms (6240 frames)"; got != want {
		t.Errorf("framesText = %q, want %q", got, want)
	}
}

// settingsConfidentAudio adds the optional StoredConfidence method: a
// backend that can say how trustworthy a stored offset is.
type settingsConfidentAudio struct {
	*settingsFakeAudio
	conf     float64
	confOK   bool
	confCap  string
	confPlay string
}

func (a *settingsConfidentAudio) StoredConfidence(captureID, playbackID string) (float64, bool) {
	a.confCap, a.confPlay = captureID, playbackID
	return a.conf, a.confOK
}

// TestSettingsWeakStoredCalibrationIsFlagged: the config stores the
// calibration confidence precisely so the UI can suggest recalibration,
// but a run that scraped past the measurement floor forever read as a
// clean "stored ...", indistinguishable from a solid loopback
// measurement. Below the threshold the row now says the measurement is
// weak and worth redoing.
func TestSettingsWeakStoredCalibrationIsFlagged(t *testing.T) {
	for _, c := range []struct {
		name   string
		conf   float64
		confOK bool
		weak   bool
	}{
		{"scraped past the floor", 0.55, true, true},
		{"solid loopback", 0.95, true, false},
		{"exactly at the threshold", settingsWeakConfidence, true, false},
		{"no confidence on file", 0, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			inner := newSettingsAudio()
			inner.offsets = map[string]settingsOffset{"cap-realtek|play-realtek": {frames: 6240, ok: true}}
			audio := &settingsConfidentAudio{settingsFakeAudio: inner, conf: c.conf, confOK: c.confOK}
			s, _ := newSettingsFixture(t, audio)

			txt, col := s.calibrationText(s.calSnapshot())
			if !strings.Contains(txt, "stored 130.0 ms (6240 frames)") {
				t.Fatalf("stored text = %q, want the offset on file", txt)
			}
			if c.weak {
				if !strings.Contains(txt, "weak measurement: worth re-measuring") {
					t.Errorf("stored text = %q, want the weak-measurement flag", txt)
				}
				if col != colClose {
					t.Errorf("weak stored color = %v, want colClose", col)
				}
			} else {
				if strings.Contains(txt, "weak") {
					t.Errorf("stored text = %q, want no flag at confidence %.2f", txt, c.conf)
				}
				if col != colHUD {
					t.Errorf("stored color = %v, want colHUD", col)
				}
			}
			if c.confOK && (audio.confCap != "cap-realtek" || audio.confPlay != "play-realtek") {
				t.Errorf("confidence asked for (%q, %q), want the selected pair", audio.confCap, audio.confPlay)
			}
		})
	}
}

// TestSettingsBackendWithoutConfidenceNeverFlags: the interface is
// optional, and a backend that cannot answer must read exactly as before.
func TestSettingsBackendWithoutConfidenceNeverFlags(t *testing.T) {
	audio := newSettingsAudio()
	audio.offsets = map[string]settingsOffset{"cap-realtek|play-realtek": {frames: 6240, ok: true}}
	s, _ := newSettingsFixture(t, audio)
	txt, col := s.calibrationText(s.calSnapshot())
	if strings.Contains(txt, "weak") {
		t.Errorf("a backend with no confidence to report was flagged: %q", txt)
	}
	if col != colHUD {
		t.Errorf("stored color = %v, want colHUD", col)
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
	if s.countIn != MaxCountIn || p.countIn != MaxCountIn {
		t.Errorf("count-in after 20 rights = %d (prefs %d), want %d", s.countIn, p.countIn, MaxCountIn)
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
	for i := 1; i <= MaxCountIn; i++ {
		s.activate()
		if s.countIn != i {
			t.Fatalf("count-in after %d enters = %d, want %d", i, s.countIn, i)
		}
	}
	s.activate()
	if s.countIn != 0 {
		t.Errorf("enter at %d beats = %d, want a wrap to 0", MaxCountIn, s.countIn)
	}
	// An out-of-range stored value is clamped when the screen opens.
	p.countIn = 99
	sh := NewShell(Services{Prefs: p}, settingsStubScreen{})
	if got := NewSettings(sh).countIn; got != MaxCountIn {
		t.Errorf("stored 99 opened as %d, want %d", got, MaxCountIn)
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
	s.SetConfigPath(`C:\Users\p\AppData\Roaming\musicTutor\config.json`)
	if got := s.configText(); !strings.Contains(got, `AppData\Roaming\musicTutor\config.json`) {
		t.Errorf("configText after SetConfigPath = %q", got)
	}

	p := &settingsPathPrefs{path: `C:\cfg\musicTutor.json`}
	sh := NewShell(Services{Prefs: p}, settingsStubScreen{})
	if got := NewSettings(sh).configText(); !strings.Contains(got, `C:\cfg\musicTutor.json`) {
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
	const wrapPx = 420.0
	lines := wrapTextW(w, wrapPx)
	if len(lines) < 2 {
		t.Fatalf("warning wrapped to %d lines at %v px", len(lines), wrapPx)
	}
	for _, l := range lines {
		// Measured with the face that draws it; a single over-wide word
		// is the only permitted overflow, and this warning has none.
		if textW(l) > wrapPx {
			t.Errorf("line measures %.1fpx, past the %v px width: %q", textW(l), wrapPx, l)
		}
	}
	if got := strings.Join(lines, " "); got != w {
		t.Errorf("wrapped text = %q, want the original %q", got, w)
	}
	if wrapTextW("   ", 100) != nil {
		t.Error("wrapTextW on blank input should produce no lines")
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

// ---- layout: nothing paints outside the room it was given ----------------
//
// The display list is the whole of the screen's geometry (see items), so
// these read it the way Draw and the hit test do rather than opening a
// window.

// settingsNoteLines returns every note line the screen is showing, which
// is how a test reads what it is telling the user.
func settingsNoteLines(s *Settings) []string {
	var out []string
	for _, it := range s.items() {
		if it.kind == siNote {
			out = append(out, it.text)
		}
	}
	return out
}

// settingsSays reports whether any note line mentions want.
func settingsSays(s *Settings, want string) bool {
	return strings.Contains(strings.Join(settingsNoteLines(s), " "), want)
}

// settingsRowTops renders where every row sits, as one comparable string:
// what a test asserts about is usually that something did NOT move.
func settingsRowTops(s *Settings) string {
	var b strings.Builder
	for _, it := range s.items() {
		if it.kind == siRow {
			fmt.Fprintf(&b, "%d@%.0f ", it.row, it.y)
		}
	}
	return b.String()
}

// settingsPressAt builds a click in the middle of a rect.
func settingsPressAt(r rect) pointer {
	return pointer{x: r.x + r.w/2, y: r.y + r.h/2, down: true, pressed: true}
}

// TestSettingsRowValueIsBoundedByItsButtons: a row's value used to be
// drawn unbounded from the value column, so a value wider than the page —
// here a device whose driver reports a marketing novel of a name — painted
// straight under the row's own opaque buttons and off the right edge.
func TestSettingsRowValueIsBoundedByItsButtons(t *testing.T) {
	audio := newSettingsAudio()
	audio.capture[1].Name = "Microphone (Aggressively Professional Reference Studio Audio Interface" +
		" With The Preposterously Long Marketing Name, 24-bit 192 kHz Edition)"
	s, _ := newSettingsFixture(t, audio)

	it := settingsRowItem(t, s, srCapture)
	if len(it.buttons) == 0 {
		t.Fatal("the capture row has no buttons to be bounded by")
	}
	if raw := textW(it.text); raw <= it.valueW {
		t.Fatalf("the fixture name measures %.0fpx and already fits %.0fpx: it cannot show the overflow", raw, it.valueW)
	}
	if got := textW(it.valueText()); got > it.valueW {
		t.Errorf("the row paints %.0fpx of value into %.0fpx of room", got, it.valueW)
	}
	// Every row, not just this one: the budget stops clear of the row's own
	// leftmost button, or of the page margin when it has none.
	for _, row := range s.items() {
		if row.kind != siRow {
			continue
		}
		if row.valueW <= 0 {
			t.Errorf("row %d was given no room at all for its value", row.row)
			continue
		}
		limit := screenW - uiPadX
		if len(row.buttons) > 0 {
			limit = row.buttons[0].r.x
		}
		if right := settingsValueX + row.valueW; right > limit {
			t.Errorf("row %d may paint out to %.0f, past %.0f", row.row, right, limit)
		}
		if w := textW(row.valueText()); w > row.valueW {
			t.Errorf("row %d paints %.0fpx into %.0fpx of room", row.row, w, row.valueW)
		}
	}
}

// TestSettingsSaveErrorFitsTheFooter: the failure line is a wrapped
// os.Rename error with two full paths, which used to be drawn unbounded
// off the right edge of the window. It is cut to the page width, keeping
// the label and the OS reason at the end — the only part that says what
// went wrong.
func TestSettingsSaveErrorFitsTheFooter(t *testing.T) {
	s, p := newSettingsFixture(t, nil)
	p.saveErr = errors.New(`appconfig: rename C:\Users\p\AppData\Roaming\musicTutor\config.json.tmp ` +
		`C:\Users\p\AppData\Roaming\musicTutor\config.json: The process cannot access the file ` +
		`because it is being used by another process.`)

	if _, ok := s.saveErrLine(); ok {
		t.Fatal("the footer announced a failure before anything was saved")
	}
	s.cur = len(s.rows) - 1
	s.adjust(+1) // any change saves, and this save fails

	line, ok := s.saveErrLine()
	if !ok {
		t.Fatal("a failed save left the footer with nothing to say")
	}
	if raw := textW("SAVE FAILED: " + p.saveErr.Error()); raw <= settingsFooterW {
		t.Fatalf("the fixture error measures %.0fpx and already fits %.0fpx", raw, settingsFooterW)
	}
	if w := textW(line); w > settingsFooterW {
		t.Errorf("the footer line measures %.0fpx, past the %.0fpx page width: %q", w, settingsFooterW, line)
	}
	if !strings.HasPrefix(line, "SAVE FAILED:") {
		t.Errorf("footer line = %q, want it to announce itself", line)
	}
	if !strings.HasSuffix(line, "used by another process.") {
		t.Errorf("footer line = %q, want the OS reason at the end to survive the cut", line)
	}
	// A successful save takes the line away again.
	p.saveErr = nil
	s.adjust(+1)
	if got, ok := s.saveErrLine(); ok {
		t.Errorf("footer still shows %q after a successful save", got)
	}
}

// TestSettingsRefusedClickLeavesTheControlsWhereTheyWere: posting a notice
// used to insert an ordinary note between the rows, pushing everything
// below it down ~40px. So during a calibration, clicking "+" on the
// count-in (refused, notice appears) and clicking again WITHOUT MOVING put
// the second click on a different control entirely — in the worst case the
// SoundFont row's "clear", which discarded the user's SoundFont silently.
func TestSettingsRefusedClickLeavesTheControlsWhereTheyWere(t *testing.T) {
	audio := newSettingsBlockingAudio()
	p := &settingsFakePrefs{soundFont: `C:\sf2\ChateauGrand.sf2`}
	sh := NewShell(Services{Prefs: p, Audio: audio}, settingsStubScreen{})
	s := NewSettings(sh)
	defer s.Close()

	idle := settingsRowTops(s)
	if !s.startCalibration() {
		t.Fatal("startCalibration refused")
	}
	<-audio.started
	if running := settingsRowTops(s); running != idle {
		t.Errorf("starting a measurement moved the rows:\n got %s\nwant %s", running, idle)
	}

	plus := settingsRowItem(t, s, srCountIn).buttons[1]
	click := settingsPressAt(plus.r)

	s.handleMouse(click) // refused: the count-in is locked mid-run
	if s.notice == "" {
		t.Fatal("the refused click was swallowed silently")
	}
	if !settingsSays(s, "locked") {
		t.Error("the notice never reached the screen")
	}
	if got := settingsRowTops(s); got != idle {
		t.Errorf("posting a notice moved the rows:\n got %s\nwant %s", got, idle)
	}
	if got := settingsRowItem(t, s, srCountIn).buttons[1]; got.r != plus.r {
		t.Errorf("the button under the cursor moved to %+v from %+v", got.r, plus.r)
	}

	// The user clicks the same pixel again without moving the mouse. It
	// must still mean the count-in, and nothing else.
	s.handleMouse(click)
	if s.soundFont == "" {
		t.Error(`the second click reached the SoundFont row's "clear" and discarded the SoundFont`)
	}
	if s.countIn != 0 {
		t.Errorf("count-in = %d: a locked row accepted the click after all", s.countIn)
	}

	// A notice far longer than its band still moves nothing.
	s.notice = strings.Repeat("a refusal nobody would ever write this long ", 20)
	if got := settingsRowTops(s); got != idle {
		t.Errorf("an over-long notice moved the rows:\n got %s\nwant %s", got, idle)
	}
	close(audio.release)
}

// TestSettingsButtonLabelsSitInsideTheirButtons: the label used to be
// drawn at a flat r.y+4 in a 20px button, so a body line's ink (y+2.43 to
// y+16.10 for a Go Regular 14 ascender/descender) came down through the
// bottom border — the g of "measuring..." was cut by it — and every label
// sat about 2px low.
func TestSettingsButtonLabelsSitInsideTheirButtons(t *testing.T) {
	// Where a body line's ink actually falls below the y drawText is given.
	const inkTop, inkBottom = 2.43, 16.10

	s, _ := newSettingsFixture(t, newSettingsAudio())
	s.SetFilePicker(func([]string, func(string)) {})

	var prev *settingsButton
	for _, it := range s.items() {
		if it.kind != siRow {
			continue
		}
		for i := range it.buttons {
			btn := it.buttons[i]
			if btn.r.h < uiTextH+4 {
				t.Errorf("%q: a %.0fpx button cannot hold an %.0fpx line with air above and below",
					btn.label, btn.r.h, uiTextH)
			}
			ty := settingsBtnTextY(btn.r)
			if top := ty + inkTop; top < btn.r.y+1 {
				t.Errorf("%q: ink starts %.2fpx into the button, through the top border",
					btn.label, top-btn.r.y)
			}
			if bottom := ty + inkBottom; bottom > btn.r.y+btn.r.h-1 {
				t.Errorf("%q: ink reaches %.2fpx of a %.0fpx button, through the bottom border",
					btn.label, bottom-btn.r.y, btn.r.h)
			}
			// Taller buttons must still fit the 26px row pitch.
			if prev != nil && prev.r.y+prev.r.h > btn.r.y {
				t.Errorf("%q (bottom %.0f) touches %q (top %.0f) on the next row",
					prev.label, prev.r.y+prev.r.h, btn.label, btn.r.y)
			}
		}
		if n := len(it.buttons); n > 0 {
			last := it.buttons[n-1]
			prev = &last
		}
	}
}

// TestSettingsBrowseSaysADialogIsOpen: sfBusy already refused a second
// dialog, but the button was drawn exactly as when idle and swallowed
// every click without a word — and the dialog is unowned, so on Windows it
// can be sitting behind the game window, which is precisely when the user
// clicks again.
func TestSettingsBrowseSaysADialogIsOpen(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	asked := 0
	var done func(string)
	s.SetFilePicker(func(_ []string, chosen func(string)) { asked++; done = chosen })

	idle := settingsRowItem(t, s, srSoundFont).buttons[0]
	if idle.label != "browse" || idle.disabled {
		t.Fatalf("the idle browse button = %+v, want an enabled \"browse\"", idle)
	}
	tops := settingsRowTops(s)

	s.handleMouse(settingsPressAt(idle.r))
	if asked != 1 {
		t.Fatalf("the browse button asked for a file %d times, want 1", asked)
	}

	busy := settingsRowItem(t, s, srSoundFont).buttons[0]
	if !busy.disabled {
		t.Error("the browse button is still drawn as if it could be pressed")
	}
	if busy.label == "browse" {
		t.Error("the browse button still says \"browse\" while its dialog is up")
	}
	if !settingsSays(s, "dialog is open") {
		t.Errorf("nothing on the screen says where the dialog went: %q", settingsNoteLines(s))
	}
	if got := settingsRowTops(s); got != tops {
		t.Errorf("saying the dialog is open moved the rows:\n got %s\nwant %s", got, tops)
	}
	// Clicking the waiting button stays a no-op.
	s.handleMouse(settingsPressAt(busy.r))
	if asked != 1 {
		t.Errorf("a click on the waiting button opened another dialog (%d)", asked)
	}

	done("") // the user cancelled: the row re-arms
	s.syncSettings()
	if got := settingsRowItem(t, s, srSoundFont).buttons[0]; got.disabled || got.label != "browse" {
		t.Errorf("after the dialog closed the button = %+v, want an enabled \"browse\"", got)
	}
	if settingsSays(s, "dialog is open") {
		t.Error("the screen still claims a dialog is open after it closed")
	}
}

// TestSettingsRowButtonsTakeTheCursor: the device and count-in buttons went
// through adjustRow and focused their row, but calibrate, browse and clear
// acted directly — so a click on one of them left the blue highlight and
// the arrow keys on a different row, and the next Right adjusted something
// the user was not looking at.
func TestSettingsRowButtonsTakeTheCursor(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	s.SetFilePicker(func([]string, func(string)) {})
	defer s.Close()

	for _, c := range []struct {
		name string
		kind settingsRow
		btn  int
	}{
		{"capture >", srCapture, 1},
		{"count-in +", srCountIn, 1},
		{"soundfont browse", srSoundFont, 0},
		{"soundfont clear", srSoundFont, 1},
		{"calibrate now", srCalibrate, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := s.rowIndex(c.kind)
			// Park the cursor somewhere else, the way a user who has just
			// adjusted another setting leaves it.
			s.cur = (want + 1) % len(s.rows)
			it := settingsRowItem(t, s, c.kind)
			if c.btn >= len(it.buttons) {
				t.Fatalf("row %v has %d buttons", c.kind, len(it.buttons))
			}
			s.handleMouse(settingsPressAt(it.buttons[c.btn].r))
			if s.cur != want {
				t.Errorf("after pressing %s the cursor is on row %d (%v), want row %d (%v)",
					c.name, s.cur, s.rows[s.cur], want, c.kind)
			}
		})
	}
	settingsWaitPhase(t, s, calDone)
}

// TestSettingsDeviceRowAdmitsNothingIsChosenYet: deviceText appended
// "<- selected" to whatever resolveDevice landed on, including the
// system-default fallback used when Prefs has never been written. A
// first-run user opened Settings from the checklist step "choose your
// audio interface", read that the device was selected, pressed Escape —
// and got no live scoring, because the application only opens the capture
// path when the STORED capture ID is non-empty.
func TestSettingsDeviceRowAdmitsNothingIsChosenYet(t *testing.T) {
	s, p := newSettingsFixture(t, newSettingsAudio())
	if capID, _ := p.Devices(); capID != "" {
		t.Fatalf("the fixture already holds a capture device (%q)", capID)
	}

	it := settingsRowItem(t, s, srCapture)
	if strings.Contains(it.text, "selected") {
		t.Errorf("a device nobody has chosen claims to be selected: %q", it.text)
	}
	for _, want := range []string{"system default", "not chosen yet", "enter"} {
		if !strings.Contains(it.text, want) {
			t.Errorf("first-run capture row = %q, want it to mention %q", it.text, want)
		}
	}

	// Enter commits the device SHOWN rather than stepping past it, so the
	// row's own promise holds — and only then does it claim the selection.
	s.cur = s.rowIndex(srCapture)
	shown := s.capture[s.capIdx].ID
	s.activate()
	if capID, _ := p.Devices(); capID != shown {
		t.Errorf("enter stored %q, want the device the row was showing (%q)", capID, shown)
	}
	if p.saves != 1 {
		t.Errorf("saves = %d, want the choice written once", p.saves)
	}
	it = settingsRowItem(t, s, srCapture)
	if !strings.Contains(it.text, "<- selected") {
		t.Errorf("after choosing it the capture row = %q, want the selected marker", it.text)
	}
	if strings.Contains(it.text, "not chosen") {
		t.Errorf("after choosing it the capture row still disowns it: %q", it.text)
	}
	// The same commit wrote the playback ID, so that row is honest too.
	if got := settingsRowItem(t, s, srPlayback).text; !strings.Contains(got, "<- selected") {
		t.Errorf("playback row = %q, want the pair's commit reflected", got)
	}
	// A second Enter cycles as before, now that there is a choice on file.
	before := s.capIdx
	s.activate()
	if s.capIdx == before {
		t.Error("enter on an already-chosen picker no longer steps to the next device")
	}
}

// --- the audio / visual sync trim ---------------------------------------

// settingsTrimPrefs is a Prefs that can store the sync trim, so the row
// appears. The plain fake deliberately cannot, which is what the "row is
// absent" test below relies on.
type settingsTrimPrefs struct {
	settingsFakePrefs
	trim int
}

func (p *settingsTrimPrefs) SyncTrim() int      { return p.trim }
func (p *settingsTrimPrefs) SetSyncTrim(ms int) { p.trim = ms }

// settingsWithPrefs builds a Settings over a given Prefs, which is what
// these tests vary — newSettingsFixture always makes its own.
func settingsWithPrefs(t *testing.T, p Prefs) *Settings {
	t.Helper()
	return NewSettings(NewShell(Services{Prefs: p}, settingsStubScreen{}))
}

func TestSettingsSyncTrimRowAdjustsAndPersists(t *testing.T) {
	pr := &settingsTrimPrefs{}
	s := settingsWithPrefs(t, pr)
	i := s.rowIndex(srSyncTrim)
	if i < 0 {
		t.Fatal("the sync trim row is missing from a Prefs that can store it")
	}
	s.cur = i
	s.adjust(+1)
	if pr.trim != syncTrimStepMS {
		t.Errorf("one press right stored %d ms, want %d", pr.trim, syncTrimStepMS)
	}
	s.adjust(-1)
	s.adjust(-1)
	if pr.trim != -syncTrimStepMS {
		t.Errorf("two presses left from there stored %d ms, want %d", pr.trim, -syncTrimStepMS)
	}
}

func TestSettingsSyncTrimIsBounded(t *testing.T) {
	pr := &settingsTrimPrefs{}
	s := settingsWithPrefs(t, pr)
	s.cur = s.rowIndex(srSyncTrim)
	for i := 0; i < 200; i++ {
		s.adjust(+1)
	}
	if pr.trim != MaxSyncTrimMS {
		t.Errorf("the trim ran to %d ms, want the %d cap", pr.trim, MaxSyncTrimMS)
	}
	for i := 0; i < 400; i++ {
		s.adjust(-1)
	}
	if pr.trim != -MaxSyncTrimMS {
		t.Errorf("the trim ran to %d ms, want the %d floor", pr.trim, -MaxSyncTrimMS)
	}
}

func TestSettingsSyncTrimSeedsFromPrefs(t *testing.T) {
	pr := &settingsTrimPrefs{trim: -40}
	s := settingsWithPrefs(t, pr)
	if s.syncTrim != -40 {
		t.Errorf("the row opened at %d ms, want the stored -40", s.syncTrim)
	}
	if got := s.syncTrimText(); !strings.Contains(got, "earlier") {
		t.Errorf("a negative trim reads %q; it should say which way it moves the tab", got)
	}
	pr2 := &settingsTrimPrefs{trim: 40}
	if got := settingsWithPrefs(t, pr2).syncTrimText(); !strings.Contains(got, "later") {
		t.Errorf("a positive trim reads %q; it should say which way it moves the tab", got)
	}
}

// TestSettingsSyncTrimRowAbsentWithoutStorage: a control whose value
// evaporates on exit is worse than no control.
func TestSettingsSyncTrimRowAbsentWithoutStorage(t *testing.T) {
	s := settingsWithPrefs(t, &settingsFakePrefs{})
	if s.rowIndex(srSyncTrim) >= 0 {
		t.Error("the sync trim row is offered by a Prefs that cannot store it")
	}
}
