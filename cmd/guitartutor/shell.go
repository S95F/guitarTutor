package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/oto/v3"

	"github.com/S95F/guitarTutor/internal/appconfig"
	"github.com/S95F/guitarTutor/internal/audio"
	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/live"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/ui"
)

// runShell opens the windowed application with no piece loaded: the start
// screen lists recent pieces and browses for new ones, and settings are
// reachable without quitting. This is what a double-clicked binary does.
func runShell() error {
	cfg, err := appconfig.Load()
	if err != nil {
		// A corrupt config must not stop the app from starting; the
		// settings screen surfaces the problem.
		fmt.Fprintln(os.Stderr, "warning: config unreadable, starting with defaults:", err)
	}
	prefs := &shellPrefs{cfg: cfg}
	opener := &shellOpener{prefs: prefs}
	defer opener.CloseCurrent()

	svc := ui.Services{Opener: opener, Prefs: prefs}
	// A missing backend is normal (no cgo, or no audio system): the
	// settings screen explains it rather than showing an empty picker.
	if b := audio.Available(); b != nil {
		svc.Audio = &shellAudio{backend: b, prefs: prefs}
	}

	sh, browser := ui.NewBrowserShell(svc)
	opener.shell = sh
	browser.SetSettingsOpener(func() { sh.Show(ui.NewSettings(sh)) })
	return sh.Run()
}

// --- Prefs -------------------------------------------------------------

// shellPrefs adapts the persisted config to the UI's Prefs facade. Every
// setter writes through to the in-memory config; Save persists.
//
// It is safe for concurrent use, and has to be: the calibration wizard
// runs off the game loop (AudioServices.Calibrate blocks for seconds) and
// stores its result into the same config the game loop is reading and
// saving. Without the mutex below that is a concurrent map write against
// a concurrent map iteration inside encoding/json — a fatal, unrecoverable
// runtime throw that kills the process mid-practice.
//
// Config.Save's value receiver is NOT protection: copying a Config copies
// the map headers, so the marshaller still walks the very maps another
// goroutine is writing. Save therefore takes a deep copy under the lock
// and marshals that, outside the lock (the write touches the disk and has
// no business blocking the game loop).
//
// Two Saves must also not reach the file at the same time: appconfig.Save
// is atomic per call (write a temp file, rename over the target), but two
// concurrent renames onto one path fail outright on Windows ("Access is
// denied"), which would surface to the user as a bogus "could not save".
// saveMu serializes the write. It is taken BEFORE the snapshot, so the
// last snapshot taken is the last one written and a queued Save can never
// overwrite newer state with older.
type shellPrefs struct {
	saveMu sync.Mutex
	mu     sync.Mutex
	cfg    appconfig.Config
}

func (p *shellPrefs) Recents() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.cfg.Recents))
	copy(out, p.cfg.Recents)
	return out
}

func (p *shellPrefs) AddRecent(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.AddRecent(path)
}

// RemoveRecent is the optional extension the browser probes for, so
// forgetting a recent survives a restart instead of lasting one session.
func (p *shellPrefs) RemoveRecent(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.ForgetRecent(path)
}

func (p *shellPrefs) SoundFont() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.SoundFontPath
}

func (p *shellPrefs) SetSoundFont(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.SoundFontPath = path
}

func (p *shellPrefs) CountIn() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.CountInBeats
}

func (p *shellPrefs) SetCountIn(beats int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.CountInBeats = beats
}

func (p *shellPrefs) Devices() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.CaptureDeviceID, p.cfg.PlaybackDeviceID
}

func (p *shellPrefs) SetDevices(cap, play string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.CaptureDeviceID, p.cfg.PlaybackDeviceID = cap, play
}

func (p *shellPrefs) Save() error {
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	return p.snapshot().Save()
}

// snapshot returns a deep copy of the config: the maps and the recents
// slice are cloned, so nothing the caller (or encoding/json inside Save)
// touches afterwards aliases state another goroutine may be writing.
func (p *shellPrefs) snapshot() appconfig.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.cfg
	c.LatencyOffsets = maps.Clone(p.cfg.LatencyOffsets)
	c.LatencyConfidence = maps.Clone(p.cfg.LatencyConfidence)
	c.Recents = slices.Clone(p.cfg.Recents)
	return c
}

// offsetFor reads a stored calibration under the lock. calibratedOffset
// takes a Config by value, which shares the map headers — so the read has
// to happen while the lock is held, not on a copy handed out first.
func (p *shellPrefs) offsetFor(captureID, playbackID string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return calibratedOffset(p.cfg, captureID, playbackID)
}

// StoreOffset records a calibration result and persists it, all under the
// same lock discipline as everything else here. It exists so the
// calibration goroutine never reaches into p.cfg directly: that reach was
// the write half of the fatal map race.
func (p *shellPrefs) StoreOffset(captureID, playbackID string, offsetFrames int, confidence float64) error {
	p.mu.Lock()
	p.cfg.SetOffset(captureID, playbackID, offsetFrames, confidence)
	p.mu.Unlock()
	return p.Save()
}

// Path is the optional extension the settings screen uses for its footer.
// An unresolvable config location is shown as such rather than hidden.
func (p *shellPrefs) Path() string {
	path, err := appconfig.Path()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return path
}

// --- Audio -------------------------------------------------------------

// errCalibrationBusy reports a calibration refused because one is already
// running. The guard lives here, on the object that owns the device, and
// not on the settings screen: the shell rebuilds that screen on every
// visit, so a per-screen flag guards nothing across visits.
var errCalibrationBusy = errors.New("a calibration is already running on this device — wait for it to finish")

// The settings screen discovers cancellation by type-asserting for this
// method set, so dropping it would not break the build — it would silently
// leave a calibration holding the audio device for its full timeout after
// the user has left the screen. That is the exact class of silent
// degradation this project keeps finding in review, so assert the shape at
// compile time instead of trusting a runtime probe.
var _ interface {
	ui.AudioServices
	CalibrateContext(ctx context.Context, captureID, playbackID string, progress func(float64)) (int, float64, error)
} = (*shellAudio)(nil)

// shellAudio adapts the duplex backend to the UI's AudioServices. One
// instance is created per run and owns the capture/playback pair, so it is
// also the right place for the "one calibration at a time" guard.
type shellAudio struct {
	backend audio.Backend
	prefs   *shellPrefs

	// calibrating is true while a pass holds the device. Two settings
	// screens (or a screen and a stale goroutine from the previous one)
	// must not drive one device pair at the same time.
	calibrating atomic.Bool
}

func (a *shellAudio) BackendName() string { return a.backend.Name() }

// SampleRate is the optional extension the settings screen uses to show
// calibration offsets in milliseconds.
func (a *shellAudio) SampleRate() int { return sampleRate }

func (a *shellAudio) Devices() (capture, playback []ui.DeviceOption, err error) {
	cap, play, err := a.backend.Devices()
	if err != nil {
		return nil, nil, err
	}
	return toOptions(cap), toOptions(play), nil
}

func toOptions(devs []audio.DeviceInfo) []ui.DeviceOption {
	out := make([]ui.DeviceOption, len(devs))
	for i, d := range devs {
		out[i] = ui.DeviceOption{ID: d.ID, Name: d.Name, Default: d.Default}
	}
	return out
}

func (a *shellAudio) CalibratedOffset(captureID, playbackID string) (int, bool) {
	return a.prefs.offsetFor(captureID, playbackID)
}

// Calibrate satisfies ui.AudioServices. It runs uncancellable — callers
// that can abandon the wizard should use CalibrateContext instead.
func (a *shellAudio) Calibrate(captureID, playbackID string, progress func(float64)) (int, float64, error) {
	return a.CalibrateContext(context.Background(), captureID, playbackID, progress)
}

// CalibrateContext is the optional cancellable extension the settings
// screen probes for: cancelling ctx aborts the pass and releases the
// device promptly instead of holding it for the rest of the timeout after
// the screen that started it is gone.
//
// Only one pass runs at a time. A second attempt is refused with
// errCalibrationBusy rather than opening a second stream on the same
// device pair.
func (a *shellAudio) CalibrateContext(ctx context.Context, captureID, playbackID string, progress func(float64)) (int, float64, error) {
	if !a.calibrating.CompareAndSwap(false, true) {
		return 0, 0, errCalibrationBusy
	}
	defer a.calibrating.Store(false)

	off, conf, err := calibrationPass(ctx, a.backend, captureID, playbackID, progress)
	if err != nil {
		return off, conf, err
	}
	if err := a.prefs.StoreOffset(captureID, playbackID, off, conf); err != nil {
		return off, conf, fmt.Errorf("measured %d frames but could not save it: %w", off, err)
	}
	return off, conf, nil
}

// --- Opener ------------------------------------------------------------

// shellOpener builds a practice screen for a piece, and owns the audio
// that screen is playing through. Only one piece is open at a time: the
// Shell calls CloseCurrent when the practice screen is popped.
//
// The oto context is process-wide and can only be created once, so it is
// created lazily on first playback and reused for every later piece.
type shellOpener struct {
	prefs *shellPrefs
	shell *ui.Shell

	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error

	player  *oto.Player
	session *live.Session
}

func (o *shellOpener) Open(path string) (ui.Screen, []string, error) {
	// Whatever the last Open started is finished the moment another piece
	// is opened. This is the first statement, not something the success
	// path does at the end: the Shell pops a practice screen (and calls
	// CloseCurrent) only when the user leaves it, and nothing stops a
	// second Open — nor does a load that fails halfway — so any later
	// placement would strand the previous player or live session with its
	// device still held and no reference left to close it.
	o.CloseCurrent()

	sc, warns, err := load(path)
	if err != nil {
		return nil, warns, err
	}
	if err := ensureTracks(sc, "play"); err != nil {
		return nil, warns, err
	}
	fac, err := makeFactory(o.prefs.SoundFont())
	if err != nil {
		return nil, warns, err
	}
	countIn := o.prefs.CountIn()
	eng := engine.New(sc, engine.Options{
		SampleRate:   sampleRate,
		Voices:       fac,
		CountInBeats: countIn,
	})

	display := 0
	for i, t := range sc.Tracks {
		if t.Role == score.RoleUser {
			display = i
			break
		}
	}
	app := ui.New(eng, sc, display)
	app.SetCountIn(countIn)
	// The engine takes CountInBeats at construction, so a change mid-piece
	// cannot apply to the running engine — report that honestly rather
	// than let the view claim otherwise, and persist it for the re-open.
	app.SetCountInApplier(func(beats int) bool {
		o.prefs.SetCountIn(beats)
		_ = o.prefs.Save()
		return false
	})
	if o.shell != nil {
		o.shell.SetTitle("guitarTutor — " + strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	}

	// Live scoring turns on when the user has chosen a capture device in
	// settings; otherwise the piece plays back through oto. setupListen
	// resolves the backend itself and reports a missing one as an error,
	// so a build without live audio lands in the same warn-and-fall-back
	// path as a device that will not open, and says so.
	captureID, _ := o.prefs.Devices()
	if captureID != "" {
		session, err := setupListen(eng, app, display, "", "")
		if err != nil {
			// Losing live input must not stop practice: fall back to
			// playback and tell the user in the view.
			app.SetLiveWarning("live input unavailable: " + err.Error())
		} else {
			o.session = session
			o.warnOnSplitDevices(app)
			return app, warns, nil
		}
	}

	ctx, err := o.audioContext()
	if err != nil {
		return nil, warns, err
	}
	o.player = ctx.NewPlayer(eng)
	o.player.Play()
	return app, warns, nil
}

// warnOnSplitDevices surfaces the clock-drift risk in the UI rather than
// only in the docs (ROADMAP Phase 2 deferred item): capture and playback
// on different physical interfaces run on independent sample clocks that
// drift apart over a session, which a static calibration cannot fix.
func (o *shellOpener) warnOnSplitDevices(app *ui.App) {
	b, err := liveBackend()
	if err != nil {
		return
	}
	capture, playback, err := b.Devices()
	if err != nil {
		return
	}
	capID, playID := o.prefs.Devices()
	capName, playName := deviceLabel(capture, capID), deviceLabel(playback, playID)
	if sameInterface(capName, playName) {
		return
	}
	app.SetLiveWarning(fmt.Sprintf(
		"capture and playback are different devices (%s / %s) — their clocks drift apart over a session and timing scores will wander",
		capName, playName))
}

// sameInterface guesses whether two endpoint names belong to one physical
// interface by comparing their first significant word — "Focusrite USB
// (Focusrite USB Audio)" against "Speakers (Focusrite USB Audio)". A guess
// is the honest ceiling here: the backend exposes no grouping, so this
// errs toward warning rather than staying silent.
func sameInterface(a, b string) bool {
	if a == "" || b == "" {
		return true // nothing chosen yet; nothing to warn about
	}
	ka, kb := interfaceKey(a), interfaceKey(b)
	return ka != "" && ka == kb
}

func interfaceKey(name string) string {
	if i := strings.Index(name, "("); i >= 0 {
		if j := strings.Index(name[i:], ")"); j > 1 {
			name = name[i+1 : i+j]
		}
	}
	fields := strings.Fields(strings.ToLower(name))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (o *shellOpener) audioContext() (*oto.Context, error) {
	o.otoOnce.Do(func() {
		ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   sampleRate,
			ChannelCount: 2,
			Format:       oto.FormatFloat32LE,
		})
		if err != nil {
			o.otoErr = fmt.Errorf("opening audio output: %w", err)
			return
		}
		<-ready
		o.otoCtx = ctx
	})
	return o.otoCtx, o.otoErr
}

// CloseCurrent stops whatever the last Open started. Safe to call when
// nothing is open.
func (o *shellOpener) CloseCurrent() {
	if o.session != nil {
		o.session.Stop()
		o.session = nil
	}
	if o.player != nil {
		o.player.Close()
		o.player = nil
	}
}
