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
	"time"

	"github.com/ebitengine/oto/v3"

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/audio"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/live"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/ui"
)

func runShell(initialPath string) error {
	cfg, err := appconfig.Load()
	if err != nil {

		fmt.Fprintln(os.Stderr, "warning: config unreadable, starting with defaults:", err)
	}
	prefs := &shellPrefs{cfg: cfg}
	opener := &shellOpener{prefs: prefs}
	defer opener.CloseCurrent()

	svc := ui.Services{Opener: opener, Prefs: prefs, Library: pieceLibrary{}}

	if b := audio.Available(); b != nil {
		svc.Audio = &shellAudio{backend: b, prefs: prefs}
	}

	sh, browser := ui.NewBrowserShell(svc)
	opener.shell = sh
	opener.browser = browser
	browser.SetSettingsOpener(func() { opener.showSettings(nil) })

	browser.SetOpenDialog(func(startDir string) {
		go func() {
			browser.OfferDialogResult(pickPieceFile(startDir))
		}()
	})

	browser.SetNewPiece(func() { opener.showEditor(nil, "") })
	browser.SetEditPiece(opener.editPiece)

	if initialPath != "" {
		browser.OfferDialogResult(initialPath, "")
	}
	return sh.Run()
}

func (o *shellOpener) showEditor(sc *score.Score, path string) {
	if o.shell == nil {
		return
	}
	var (
		ed  *ui.Editor
		err error
	)
	if sc == nil {

		ed = ui.NewEditorChoosing(o.shell)
	} else if ed, err = ui.NewEditorFor(o.shell, sc, path); err != nil {

		fmt.Fprintln(os.Stderr, "musictutor: cannot edit that piece:", err)
		if o.browser != nil {
			o.browser.ShowError(fmt.Sprintf("cannot open %s for editing: %v", filepath.Base(path), err))
		}
		return
	}
	o.installEditor(ed)
}

func (o *shellOpener) showTextEditor(src []byte, path string) {
	if o.shell == nil {
		return
	}
	o.installEditor(ui.NewEditorForText(o.shell, src, path))
}

func (o *shellOpener) editPiece(path string) {
	sc, warns, err := load(path)
	if err != nil {

		if strings.EqualFold(filepath.Ext(path), ".gtab") {
			if src, rerr := os.ReadFile(path); rerr == nil {
				o.showTextEditor(src, path)
				return
			}
		}
		if o.browser != nil {
			o.browser.ShowError(fmt.Sprintf("cannot open %s for editing: %v", filepath.Base(path), err))
		}
		return
	}
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	o.showEditor(sc, path)
}

func (o *shellOpener) installEditor(ed *ui.Editor) {

	ed.SetSaveDialog(func(suggest string) {
		go func() { ed.OfferSavePath(pickSavePath(o.suggestSavePath(suggest))) }()
	})

	ed.SetOnSaved(func(p string) {
		o.prefs.AddCreated(p)
		o.prefs.AddRecent(p)
		_ = o.prefs.Save()
		o.rescanLibrary()
	})

	ed.SetPractice(func(p string) { o.practiseFromEditor(ed, p) })
	o.shell.Show(ed)
}

func (o *shellOpener) practiseFromEditor(ed *ui.Editor, path string) {
	if _, err := o.shell.OpenPiece(path); err != nil {
		fmt.Fprintln(os.Stderr, "musictutor: cannot practise that piece:", err)
		ed.ShowError(fmt.Sprintf("cannot practise %s: %v", filepath.Base(path), err))
	}
}

func (o *shellOpener) suggestSavePath(suggest string) string {
	if suggest == "" || filepath.IsAbs(suggest) {
		return suggest
	}
	dir, err := appconfig.EnsurePiecesDir()
	if err != nil {

		return suggest
	}
	return filepath.Join(dir, suggest)
}

func (o *shellOpener) rescanLibrary() {
	if o.browser != nil {
		o.browser.RefreshLibrary()
	}
}

type openTimeSettings struct {
	soundFont string
	countIn   int
	captureID string
	playID    string
}

func (o *shellOpener) openTimeSnapshot() openTimeSettings {
	capID, playID := o.prefs.Devices()
	return openTimeSettings{
		soundFont: o.prefs.SoundFont(),
		countIn:   o.prefs.CountIn(),
		captureID: capID,
		playID:    playID,
	}
}

func (o *shellOpener) showSettings(app *ui.App) {
	if o.shell == nil {
		return
	}
	st := ui.NewSettings(o.shell)

	st.SetFilePicker(func(exts []string, chosen func(string)) {
		if !o.sfDialog.CompareAndSwap(false, true) {
			chosen("")
			return
		}
		go func() {
			defer o.sfDialog.Store(false)
			path := pickSoundFont()
			if path != "" {
				o.adoptSoundFont(path)
			}
			chosen(path)
		}()
	})
	if app != nil {
		before := o.openTimeSnapshot()
		st.SetOnClose(func() {
			after := o.openTimeSnapshot()
			if after == before {
				return
			}

			if after.countIn != before.countIn {
				app.SyncCountIn(after.countIn)
			}
			app.MarkSettingsChanged()
		})
	}
	o.shell.Show(st)
}

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

func (p *shellPrefs) RemoveRecent(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.ForgetRecent(path)
}

func (p *shellPrefs) Created() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.cfg.Created))
	copy(out, p.cfg.Created)
	return out
}

func (p *shellPrefs) AddCreated(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.AddCreated(path)
}

func (p *shellPrefs) HintHidden() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.HideStartHint
}

func (p *shellPrefs) SetHintHidden(hidden bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.HideStartHint = hidden
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

func (p *shellPrefs) SyncTrim() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.SyncTrimMS
}

func (p *shellPrefs) SetSyncTrim(ms int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.SyncTrimMS = ms
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

func (p *shellPrefs) snapshot() appconfig.Config {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.cfg
	c.LatencyOffsets = maps.Clone(p.cfg.LatencyOffsets)
	c.LatencyConfidence = maps.Clone(p.cfg.LatencyConfidence)
	c.Recents = slices.Clone(p.cfg.Recents)
	c.Created = slices.Clone(p.cfg.Created)
	return c
}

func (p *shellPrefs) offsetFor(captureID, playbackID string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return calibratedOffset(p.cfg, captureID, playbackID)
}

func (p *shellPrefs) confidenceFor(captureID, playbackID string) (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.ConfidenceFor(captureID, playbackID)
}

func (p *shellPrefs) StoreOffset(captureID, playbackID string, offsetFrames int, confidence float64) error {
	p.mu.Lock()
	p.cfg.SetOffset(captureID, playbackID, offsetFrames, confidence)
	p.mu.Unlock()
	return p.Save()
}

func (p *shellPrefs) Path() string {
	path, err := appconfig.Path()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return path
}

var errCalibrationBusy = errors.New("a calibration is already running on this device; wait for it to finish")

var _ interface {
	ui.AudioServices
	CalibrateContext(ctx context.Context, captureID, playbackID string, progress func(float64)) (int, float64, error)
	StoredConfidence(captureID, playbackID string) (float64, bool)
} = (*shellAudio)(nil)

type shellAudio struct {
	backend audio.Backend
	prefs   *shellPrefs

	calibrating atomic.Bool
}

func (a *shellAudio) BackendName() string { return a.backend.Name() }

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

func (a *shellAudio) StoredConfidence(captureID, playbackID string) (float64, bool) {
	return a.prefs.confidenceFor(captureID, playbackID)
}

func (a *shellAudio) Calibrate(captureID, playbackID string, progress func(float64)) (int, float64, error) {
	return a.CalibrateContext(context.Background(), captureID, playbackID, progress)
}

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

type shellOpener struct {
	prefs *shellPrefs
	shell *ui.Shell

	browser *ui.Browser

	sfDialog atomic.Bool

	sfPicked atomic.Bool

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

	o.sfPicked.Store(false)
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

	app.SetSettingsOpener(func() { o.showSettings(app) })
	app.SetReloader(func() {
		if _, err := o.shell.ReopenPiece(path); err != nil {
			app.SetLiveWarning("could not re-open the piece: " + err.Error())
		}
	})

	app.SetQuitAll(func() {
		if o.shell != nil {
			o.shell.Quit()
		}
	})

	app.SetCountInApplier(func(beats int) bool {
		o.prefs.SetCountIn(beats)
		_ = o.prefs.Save()
		return false
	})

	captureID, _ := o.prefs.Devices()
	if captureID == "" {

		if _, err := o.audioContext(); err != nil {
			return nil, warns, err
		}
	}

	o.CloseCurrent()

	var conds []string

	if captureID != "" {
		session, cond, err := setupListen(eng, app, sc, "", "", o.prefs.snapshot())
		if err != nil {

			conds = append(conds, "live input unavailable: "+err.Error())
		} else {
			o.session = session
			conds = append(conds, cond.notes...)
			if cond.uncalibrated {
				conds = append(conds, uncalibratedShellWarning)
			}
			if w := o.splitDeviceWarning(); w != "" {
				conds = append(conds, w)
			}
		}
	}

	if o.session == nil {
		ctx, err := o.audioContext()
		if err != nil {
			return nil, warns, err
		}
		o.player = ctx.NewPlayer(eng)
		o.player.SetBufferSize(playerBufferBytes)
		o.player.Play()
	}
	o.watchOutputLatency(app)
	o.setTitleFor(path)
	if s := importSummary(warns); s != "" {
		conds = append(conds, s)
	}
	if msg := composeBanner(conds); msg != "" {
		app.SetLiveWarning(msg)
	}
	return app, warns, nil
}

const uncalibratedShellWarning = "timing is not calibrated for these devices — press S for settings, then calibrate now"

func composeBanner(conds []string) string {
	return strings.Join(conds, "; ")
}

func importSummary(warns []string) string {
	switch len(warns) {
	case 0:
		return ""
	case 1:
		return "imported with 1 warning: " + warns[0]
	}
	return fmt.Sprintf("imported with %d warnings: %s (and %d more)", len(warns), warns[0], len(warns)-1)
}

func (o *shellOpener) adoptSoundFont(path string) {
	o.prefs.SetSoundFont(path)
	_ = o.prefs.Save()
	o.sfPicked.Store(true)
}

func (o *shellOpener) drainSettingsMark(app *ui.App) {
	if o.sfPicked.CompareAndSwap(true, false) {
		app.MarkSettingsChanged()
	}
}

func (o *shellOpener) watchOutputLatency(app *ui.App) {
	app.SetOutputLatency(func() time.Duration {

		o.drainSettingsMark(app)
		trim := time.Duration(o.prefs.SyncTrim()) * time.Millisecond
		switch {
		case o.session != nil:
			if period := o.session.Config().PeriodFrames; period > 0 {
				return framesToDuration(period) + trim
			}
		case o.player != nil:
			return framesToDuration(o.player.BufferedSize()/bytesPerFrame) + trim
		}
		return trim
	})
}

const bytesPerFrame = 2 * 4

func framesToDuration(frames int) time.Duration {
	return time.Duration(frames) * time.Second / time.Duration(sampleRate)
}

func (o *shellOpener) setTitleFor(path string) {
	if o.shell != nil {
		o.shell.SetTitle("musicTutor — " + strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	}
}

func (o *shellOpener) splitDeviceWarning() string {
	b, err := liveBackend()
	if err != nil {
		return ""
	}
	capture, playback, err := b.Devices()
	if err != nil {
		return ""
	}
	capID, playID := o.prefs.Devices()
	capName := resolvedDeviceName(capture, capID)
	playName := resolvedDeviceName(playback, playID)
	if ui.SameAudioInterface(capName, playName) {
		return ""
	}
	return fmt.Sprintf(
		"capture and playback are different devices (%s / %s): their clocks drift apart over a session and timing scores wander — pick the same interface for capture and playback in settings (S)",
		capName, playName)
}

func resolvedDeviceName(devs []audio.DeviceInfo, id string) string {
	for _, d := range devs {
		if d.ID == id {
			return d.Name
		}
	}
	for _, d := range devs {
		if d.Default {
			return d.Name
		}
	}
	return ""
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
