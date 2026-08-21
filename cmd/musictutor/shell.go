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
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/ui"
)

// runShell opens the windowed application: the start screen lists recent
// pieces and browses for new ones, and settings are reachable without
// quitting. This is what a double-clicked binary does — and initialPath,
// when non-empty, is the piece a double-clicked FILE arrives as: it is
// opened once the window is up, through the same mailbox the OS file
// dialog answers into, so a file that will not load is an error shown on
// the start screen rather than one line on a stderr nobody can see.
func runShell(initialPath string) error {
	cfg, err := appconfig.Load()
	if err != nil {
		// A corrupt config must not stop the app from starting; the
		// settings screen surfaces the problem.
		fmt.Fprintln(os.Stderr, "warning: config unreadable, starting with defaults:", err)
	}
	prefs := &shellPrefs{cfg: cfg}
	opener := &shellOpener{prefs: prefs}
	defer opener.CloseCurrent()

	svc := ui.Services{Opener: opener, Prefs: prefs, Library: pieceLibrary{}}
	// A missing backend is normal (no cgo, or no audio system): the
	// settings screen explains it rather than showing an empty picker.
	if b := audio.Available(); b != nil {
		svc.Audio = &shellAudio{backend: b, prefs: prefs}
	}

	sh, browser := ui.NewBrowserShell(svc)
	opener.shell = sh
	opener.browser = browser
	browser.SetSettingsOpener(func() { opener.showSettings(nil) })
	// Opening a piece goes through the OS file dialog. It blocks while
	// the user browses, so it runs on its own goroutine and posts the
	// outcome to the browser's mailbox; the game loop drains it.
	browser.SetOpenDialog(func(startDir string) {
		go func() {
			browser.OfferDialogResult(pickPieceFile(startDir))
		}()
	})
	// Writing a piece by hand. The editor needs no audio of its own — it
	// hands a saved piece back to the Shell to be practised — so the only
	// thing the application has to lend it is the save dialog and the
	// recents list.
	browser.SetNewPiece(func() { opener.showEditor(nil, "") })
	browser.SetEditPiece(opener.editPiece)
	// The command-line piece goes through the dialog mailbox rather than a
	// path of its own: the browser already drains it on the game loop,
	// already shows a failed load inline, and already records a successful
	// open as recent. Posting before Run is safe — the mailbox holds one
	// result until the first Update — and the generation stamp matches
	// because nothing else can have opened a piece yet.
	if initialPath != "" {
		browser.OfferDialogResult(initialPath, "")
	}
	return sh.Run()
}

// showEditor opens the tablature editor, on a blank piece when sc is nil.
// path is where the piece came from; the editor keeps it as its save
// target only if it is a .gtab, since that is the only format the app can
// write.
func (o *shellOpener) showEditor(sc *score.Score, path string) {
	if o.shell == nil {
		return
	}
	var (
		ed  *ui.Editor
		err error
	)
	if sc == nil {
		ed = ui.NewEditor(o.shell)
	} else if ed, err = ui.NewEditorFor(o.shell, sc, path); err != nil {
		// A wind piece cannot open on the grid yet — the grid is a
		// strings-by-frets surface — but its text is fully editable, so
		// it opens in the text view instead of dead-ending: from its own
		// file when it has one, from a fresh serialization when it
		// arrived through an importer.
		if hasWindTrack(sc) {
			if strings.EqualFold(filepath.Ext(path), ".gtab") {
				if src, rerr := os.ReadFile(path); rerr == nil {
					o.showTextEditor(src, path)
					return
				}
			}
			if src, ferr := textfmt.Format(sc); ferr == nil {
				o.showTextEditor(src, "")
				return
			}
		}
		fmt.Fprintln(os.Stderr, "musictutor: cannot edit that piece:", err)
		return
	}
	o.installEditor(ed)
}

// hasWindTrack reports whether any track is a wind part — the pieces the
// notation grid cannot hold yet.
func hasWindTrack(sc *score.Score) bool {
	for _, tr := range sc.Tracks {
		if tr.Wind != nil {
			return true
		}
	}
	return false
}

// showTextEditor opens the editor straight into its text view on raw
// .gtab source — the entry for a file that will not parse and therefore
// has no score to hand showEditor.
func (o *shellOpener) showTextEditor(src []byte, path string) {
	if o.shell == nil {
		return
	}
	o.installEditor(ui.NewEditorForText(o.shell, src, path))
}

// editPiece opens a piece for editing — the action behind the browser's E
// key, its Edit button, and Enter on a library entry that will not parse.
func (o *shellOpener) editPiece(path string) {
	sc, warns, err := load(path)
	if err != nil {
		// A .gtab that will not parse is the one file the editor's text
		// view exists to repair — the browser sent it here for exactly
		// that — so re-parsing and refusing would dead-end the only
		// repair loop the app has. The raw text goes in instead, with
		// the pane's own reparse showing the problem.
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

// installEditor lends the editor the only things the application has to
// provide — the save dialog, the recents list, and a way to practise what
// was written — and shows it.
func (o *shellOpener) installEditor(ed *ui.Editor) {
	// The save dialog blocks while the user browses, so it runs on its own
	// goroutine and posts to the editor's mailbox — the same pattern the
	// start screen and the settings screen use. The guard against two
	// dialogs at once lives on the EDITOR rather than here: unlike the
	// SoundFont picker, an editor is not rebuilt on every visit, so its
	// own flag is the one that survives.
	ed.SetSaveDialog(func(suggest string) {
		go func() { ed.OfferSavePath(pickSavePath(o.suggestSavePath(suggest))) }()
	})
	// A saved piece joins BOTH lists: written here, and — because writing
	// a piece is how you end up practising it — recent. They answer
	// different questions and a piece is honestly in both.
	ed.SetOnSaved(func(p string) {
		o.prefs.AddCreated(p)
		o.prefs.AddRecent(p)
		_ = o.prefs.Save()
		o.rescanLibrary()
	})
	// Practising what has just been written pushes the practice screen ON
	// TOP of the editor, so Escape comes back to the editing rather than
	// to the start screen: write, hear it, fix it, hear it again.
	ed.SetPractice(func(p string) {
		if _, err := o.shell.OpenPiece(p); err != nil {
			fmt.Fprintln(os.Stderr, "musictutor: cannot practise that piece:", err)
		}
	})
	o.shell.Show(ed)
}

// suggestSavePath puts the editor's suggested file name inside the
// application's own pieces folder, so a piece written here lands in the
// library by default and shows up on the start screen without anyone
// having to know where it went. A name that is already a full path —
// re-saving a piece opened from somewhere else — is left where it is.
func (o *shellOpener) suggestSavePath(suggest string) string {
	if suggest == "" || filepath.IsAbs(suggest) {
		return suggest
	}
	dir, err := appconfig.EnsurePiecesDir()
	if err != nil {
		// Without a folder the dialog opens wherever it likes, which is
		// the same as before the library existed.
		return suggest
	}
	return filepath.Join(dir, suggest)
}

// rescanLibrary asks the start screen to re-read the pieces folder. The
// browser owns the scan (it runs off the game loop and posts back), so
// this only has to find it.
func (o *shellOpener) rescanLibrary() {
	if o.browser != nil {
		o.browser.RefreshLibrary()
	}
}

// openTimeSettings is the subset of the configuration that a piece reads
// when it is opened and cannot pick up afterwards: the engine takes its
// count-in and its synthesis voice at construction, and the live session
// binds its devices when the stream opens. Comparing one of these across
// a visit to the settings screen is how the practice view learns that
// what it is showing no longer matches what is configured.
type openTimeSettings struct {
	soundFont string
	countIn   int
	captureID string
	playID    string
}

// openTimeSnapshot reads the current values of those settings.
func (o *shellOpener) openTimeSnapshot() openTimeSettings {
	capID, playID := o.prefs.Devices()
	return openTimeSettings{
		soundFont: o.prefs.SoundFont(),
		countIn:   o.prefs.CountIn(),
		captureID: capID,
		playID:    playID,
	}
}

// showSettings opens the settings screen. When a practice screen asked
// for it, that screen is told on the way back whether anything it was
// built from has changed, so it can offer to re-open the piece instead of
// silently going on with the old configuration. app may be nil: the start
// screen has no piece to reconcile.
func (o *shellOpener) showSettings(app *ui.App) {
	if o.shell == nil {
		return
	}
	st := ui.NewSettings(o.shell)
	// The SoundFont row browses with the OS file dialog. The chosen
	// callback only posts to the settings screen's mailbox, so calling it
	// from the dialog goroutine is safe — and it is ALWAYS called, with
	// "" on cancel, because the row's busy guard re-arms on the outcome.
	//
	// The "one dialog at a time" guard has to live HERE, not only on the
	// screen: showSettings builds a fresh Settings on every visit, so a
	// screen-local flag was reset by leaving and re-entering settings
	// while a dialog still floated, and two dialogs could then both write
	// the config with whichever the user answered last winning.
	//
	// A real pick is persisted directly rather than only posted, because
	// the dialog can outlive the screen that opened it (Escape while it
	// sits behind the window) — and the practice view is told, so the
	// piece still playing the old voice offers its reload instead of
	// silently disagreeing with the config.
	st.SetFilePicker(func(exts []string, chosen func(string)) {
		if !o.sfDialog.CompareAndSwap(false, true) {
			chosen("") // already open; re-arm the row rather than stack a second
			return
		}
		go func() {
			defer o.sfDialog.Store(false)
			path := pickSoundFont()
			if path != "" {
				o.prefs.SetSoundFont(path)
				_ = o.prefs.Save()
				if app != nil {
					app.MarkSettingsChanged()
				}
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
			// The view keeps its own mirror of the count-in, and it is
			// the value the next press of C writes back — so a count-in
			// changed here has to reach it, or C would restore the
			// open-time value over what the user just chose.
			if after.countIn != before.countIn {
				app.SyncCountIn(after.countIn)
			}
			app.MarkSettingsChanged()
		})
	}
	o.shell.Show(st)
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

// Created and AddCreated are the written-pieces half of the recents
// facade: what was made here, as opposed to what was opened.
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

// HintHidden and SetHintHidden persist whether the start screen's
// getting-started strip has been put away.
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

// SyncTrim and SetSyncTrim are the optional Prefs extension the settings
// screen probes for: the manual audio/visual nudge, applied on top of the
// output buffering the application measures for itself.
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
	c.Created = slices.Clone(p.cfg.Created)
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

// confidenceFor reads a stored calibration's confidence under the lock,
// for the same reason offsetFor takes it: the Config's maps are shared.
func (p *shellPrefs) confidenceFor(captureID, playbackID string) (float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.ConfidenceFor(captureID, playbackID)
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
var errCalibrationBusy = errors.New("a calibration is already running on this device; wait for it to finish")

// The settings screen discovers cancellation — and the stored-confidence
// lookup its recalibration hint reads — by type-asserting for these
// method sets, so dropping either would not break the build: it would
// silently leave a calibration holding the audio device for its full
// timeout after the user has left the screen, or silently stop rating
// weak measurements. That is the exact class of silent degradation this
// project keeps finding in review, so assert the shapes at compile time
// instead of trusting a runtime probe.
var _ interface {
	ui.AudioServices
	CalibrateContext(ctx context.Context, captureID, playbackID string, progress func(float64)) (int, float64, error)
	StoredConfidence(captureID, playbackID string) (float64, bool)
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

// StoredConfidence is the optional extension the settings screen probes
// for to rate a stored measurement: latency.Estimate reports how sure it
// was, the config keeps that next to the offset, and a weak figure is
// what lets the screen suggest recalibrating instead of trusting it.
func (a *shellAudio) StoredConfidence(captureID, playbackID string) (float64, bool) {
	return a.prefs.confidenceFor(captureID, playbackID)
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
	// browser is the start screen, so a piece saved in the editor can ask
	// it to re-read the library folder it has just landed in.
	browser *ui.Browser

	// sfDialog is true while a SoundFont dialog is open. It lives on the
	// opener rather than the settings screen because the screen is
	// rebuilt on every visit, and the dialog can outlive it.
	sfDialog atomic.Bool

	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error

	player  *oto.Player
	session *live.Session
}

func (o *shellOpener) Open(path string) (ui.Screen, []string, error) {
	// Everything that can fail without needing the audio device happens
	// FIRST; the running piece's audio is released only once this open is
	// past those failure points. The old order — CloseCurrent as the very
	// first statement — meant a failed F5 reload tore down the audio of a
	// piece that then stayed on screen: frozen playhead, silent transport,
	// a header still claiming "playing" (audit A2). A failed load must
	// leave the previous session exactly as it was. The browser flow is
	// unaffected: the practice screen it came from was popped, and its
	// audio closed, before the browser could open anything, so there is
	// nothing running to preserve — and on the success path CloseCurrent
	// still runs before the new session or player is installed, so nothing
	// is ever stranded.
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
	// Settings and a reload are reachable from inside the piece, so
	// changing a device or a SoundFont no longer means quitting back to
	// the start screen and opening the file again.
	app.SetSettingsOpener(func() { o.showSettings(app) })
	app.SetReloader(func() {
		if _, err := o.shell.ReopenPiece(path); err != nil {
			app.SetLiveWarning("could not re-open the piece: " + err.Error())
		}
	})
	// Q's binding reads "Quit musicTutor", and under the Shell only this
	// wiring makes that true — without it Q silently behaved as Escape,
	// while the help overlay promised otherwise on adjacent lines (audit D4).
	app.SetQuitAll(func() {
		if o.shell != nil {
			o.shell.Quit()
		}
	})
	// The engine takes CountInBeats at construction, so a change mid-piece
	// cannot apply to the running engine — report that honestly rather
	// than let the view claim otherwise, and persist it for the re-open.
	app.SetCountInApplier(func(beats int) bool {
		o.prefs.SetCountIn(beats)
		_ = o.prefs.Save()
		return false
	})
	// Live scoring turns on when the user has chosen a capture device in
	// settings; otherwise the piece plays back through oto. setupListen
	// resolves the backend itself and reports a missing one as an error,
	// so a build without live audio lands in the same warn-and-fall-back
	// path as a device that will not open, and says so.
	captureID, _ := o.prefs.Devices()
	if captureID == "" {
		// Pre-flight the one audio resource the playback path still needs
		// while the previous piece is untouched. The oto context is
		// process-wide, created once and reused, so probing it here is
		// free when it already exists — and when its first creation fails,
		// this open fails before anything was torn down.
		if _, err := o.audioContext(); err != nil {
			return nil, warns, err
		}
	}

	// Past every failure point that can be checked in advance: the
	// previous piece's audio is released, and the new session or player
	// is installed in its place. The one residual window is the live
	// path, where the duplex device cannot be probed without opening it —
	// a setupListen failure there falls back to oto playback below, and
	// only a first-ever oto failure on that fallback can now leave the
	// open failed after the teardown.
	o.CloseCurrent()

	// Everything this open wants the user to see collects here and is
	// raised as ONE banner at the end. App.SetLiveWarning holds a single
	// message, last writer wins — reported a call apiece, the device
	// fallback vanished under the split-device warning and the missing
	// calibration never reached the screen at all. The order is the order
	// a person should read them in: which devices actually opened, what
	// that means for the timing verdicts, and only then the notes about
	// the import itself.
	var conds []string

	if captureID != "" {
		session, cond, err := setupListen(eng, app, sc, "", "", o.prefs.snapshot())
		if err != nil {
			// Losing live input must not stop practice: fall back to
			// playback and tell the user in the view.
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
	// The live path plays through its duplex session; every other way
	// here needs the oto player. CloseCurrent above nil'd o.session, so
	// its presence says exactly which open this was.
	if o.session == nil {
		ctx, err := o.audioContext()
		if err != nil {
			return nil, warns, err
		}
		o.player = ctx.NewPlayer(eng)
		o.player.SetBufferSize(playerBufferBytes())
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

// uncalibratedShellWarning is the practice view's wording for a live
// session whose round trip has never been measured. The stderr line
// setupListen prints says to run 'musictutor calibrate' — true in a
// terminal, useless in a window Explorer opened — so the shell's banner
// names the remedy that exists right here instead.
const uncalibratedShellWarning = "timing is not calibrated for these devices — press S for settings, then calibrate now"

// composeBanner folds every condition an open raises into the one
// message the warning banner holds, most important first: the banner is
// a fixed rectangle that ellipsizes what does not fit, so the tail is
// what a crowded open loses.
func composeBanner(conds []string) string {
	return strings.Join(conds, "; ")
}

// importSummary coalesces importer warnings into a banner-sized line:
// the first warning spelled out, and a count for the rest. The full list
// stays on the Open return value — this is the headline, raised where
// the user actually lands, because the browser's inline list is behind
// them the moment the practice screen opens.
func importSummary(warns []string) string {
	switch len(warns) {
	case 0:
		return ""
	case 1:
		return "imported with 1 warning: " + warns[0]
	}
	return fmt.Sprintf("imported with %d warnings: %s (and %d more)", len(warns), warns[0], len(warns)-1)
}

// watchOutputLatency tells the practice view how much rendered audio has
// not been heard yet, so the playhead can sit on the note that is sounding
// instead of the one being rendered (see internal/ui/playhead.go).
//
// The figure is measured, not assumed. On the oto path the player reports
// exactly what it is still holding, which is the whole of the lead the
// engine has over the speaker; on the duplex path the engine renders
// inside the device callback, so the lead is one period. What neither can
// see is the last few milliseconds inside the driver and the hardware, and
// that is what the trim in settings adds on top.
//
// The closure reads o.player and o.session on every display frame, and
// CloseCurrent clears both — so it has to tolerate finding them nil, which
// is what happens between a piece being closed and its view being popped.
func (o *shellOpener) watchOutputLatency(app *ui.App) {
	app.SetOutputLatency(func() time.Duration {
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

// bytesPerFrame is one stereo float32 frame, the format oto is configured
// for and therefore the unit BufferedSize counts in.
const bytesPerFrame = 2 * 4

// framesToDuration converts a frame count at the project sample rate into
// wall-clock time.
func framesToDuration(frames int) time.Duration {
	return time.Duration(frames) * time.Second / time.Duration(sampleRate)
}

// setTitleFor names the window after the piece. It runs only once an open
// has succeeded: a failed open must not retitle the window after a piece
// it never produced.
func (o *shellOpener) setTitleFor(path string) {
	if o.shell != nil {
		o.shell.SetTitle("musicTutor — " + strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	}
}

// splitDeviceWarning surfaces the clock-drift risk in the UI rather than
// only in the docs (ROADMAP Phase 2 deferred item): capture and playback
// on different physical interfaces run on independent sample clocks that
// drift apart over a session, which a static calibration cannot fix. It
// returns the warning text — "" when the pair shares an interface or
// cannot be judged — rather than setting the banner itself, so the open
// can compose it with the other conditions instead of overwriting them.
//
// The judgement is ui.SameAudioInterface — the same heuristic the
// settings screen uses. This file used to carry its own first-word
// comparison, and the two disagreed on ordinary Windows names: settings
// warned about a pair that the practice view, the screen where scoring
// actually happens, stayed silent about (audit C2).
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

// resolvedDeviceName names the device an ID will actually resolve to at
// open time: the enumerated device, or the system default when the ID is
// empty or no longer present (mirroring fillDeviceID's fallback). An
// unresolvable name comes back "", which SameAudioInterface treats as
// unknown-so-do-not-warn. The old code turned an unset ID into the
// literal display string "system default" and compared THAT — a device
// name that matches no real interface, raising a permanent unfounded
// banner over the tab (audit C4).
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
