package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
// setter writes through to the in-memory config; Save persists. Screens
// call it only from the game loop, so no locking is needed.
type shellPrefs struct {
	cfg appconfig.Config
}

func (p *shellPrefs) Recents() []string {
	out := make([]string, len(p.cfg.Recents))
	copy(out, p.cfg.Recents)
	return out
}

func (p *shellPrefs) AddRecent(path string) { p.cfg.AddRecent(path) }

// RemoveRecent is the optional extension the browser probes for, so
// forgetting a recent survives a restart instead of lasting one session.
func (p *shellPrefs) RemoveRecent(path string) { p.cfg.ForgetRecent(path) }

func (p *shellPrefs) SoundFont() string         { return p.cfg.SoundFontPath }
func (p *shellPrefs) SetSoundFont(path string)  { p.cfg.SoundFontPath = path }
func (p *shellPrefs) CountIn() int              { return p.cfg.CountInBeats }
func (p *shellPrefs) SetCountIn(beats int)      { p.cfg.CountInBeats = beats }
func (p *shellPrefs) Devices() (string, string) { return p.cfg.CaptureDeviceID, p.cfg.PlaybackDeviceID }
func (p *shellPrefs) SetDevices(cap, play string) {
	p.cfg.CaptureDeviceID, p.cfg.PlaybackDeviceID = cap, play
}
func (p *shellPrefs) Save() error { return p.cfg.Save() }

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

// shellAudio adapts the duplex backend to the UI's AudioServices.
type shellAudio struct {
	backend audio.Backend
	prefs   *shellPrefs
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
	return calibratedOffset(a.prefs.cfg, captureID, playbackID)
}

func (a *shellAudio) Calibrate(captureID, playbackID string, progress func(float64)) (int, float64, error) {
	off, conf, err := calibrationPass(a.backend, captureID, playbackID, progress)
	if err != nil {
		return off, conf, err
	}
	a.prefs.cfg.SetOffset(captureID, playbackID, off, conf)
	if err := a.prefs.Save(); err != nil {
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
	// settings; otherwise the piece plays back through oto.
	captureID, _ := o.prefs.Devices()
	if captureID != "" && audio.Available() != nil {
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
	b := audio.Available()
	if b == nil {
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
