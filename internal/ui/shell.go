package ui

import (
	"github.com/hajimehoshi/ebiten/v2"
)

// A Screen is one full-window mode of the application: the start screen,
// the settings screen, the practice view. The methods are exactly
// ebiten.Game's minus Layout (the Shell owns the logical size), so the
// practice App satisfies this interface without modification.
//
// A Screen signals that it is finished by returning errQuit from Update.
// The Shell pops it and reveals whatever was underneath; when the last
// screen finishes, the application exits. A Screen that wants to open
// another one calls Shell.Show — screens are constructed by the package
// that hosts them, so they may capture the Shell they belong to.
//
// A Screen that owns something beyond memory — a background goroutine, an
// audio device — implements the optional Closer extension below, and the
// Shell releases it on the way out.
type Screen interface {
	Update() error
	Draw(*ebiten.Image)
}

// A Closer is the optional extension a Screen implements when leaving it
// must release something: the Shell calls Close exactly once, when the
// screen is popped, before the screen beneath resumes.
//
// This is how the settings screen cancels a calibration in flight instead
// of letting it hold the audio device for the rest of its 20 s timeout,
// long after the screen that started it is gone. Close must return
// promptly — it runs on the game loop, so any wait it does for a
// goroutine to unwind has to be bounded.
//
// It is declared as its own interface rather than folded into Screen so
// that every existing screen keeps satisfying Screen unchanged; the Shell
// probes for it with a type assertion.
type Closer interface {
	Close()
}

// An Opener turns a piece on disk into a practice screen, and tears the
// audio down again afterwards. It is the seam between the UI and
// everything that needs an engine, a synth, and an audio device: the UI
// package knows how to show a file browser, and nothing about how to
// build a playback pipeline. cmd/guitartutor implements it.
type Opener interface {
	// Open loads path and returns the practice screen for it, along with
	// any importer warnings worth showing the user.
	Open(path string) (Screen, []string, error)
	// CloseCurrent stops and releases the audio the last Open started.
	// The Shell calls it when the practice screen is popped, so leaving
	// a piece never leaks a stream.
	CloseCurrent()
}

// Services is what screens can ask of the application beyond opening a
// piece: the persisted preferences, and the audio devices to choose
// between. Implemented by cmd/guitartutor; a nil field means that
// facility is unavailable in this build or on this machine (no live
// audio backend, for instance), and screens must degrade rather than
// assume.
type Services struct {
	// Opener loads pieces. Required.
	Opener Opener
	// Prefs reads and writes persisted settings. Required.
	Prefs Prefs
	// Audio enumerates devices and runs calibration. Nil when the build
	// or machine has no duplex backend: the settings screen then shows
	// why live mode is unavailable instead of an empty device list.
	Audio AudioServices
}

// Prefs is the persisted-settings facade the UI works against, so no
// screen has to know about the config file's shape or location.
type Prefs interface {
	// Recents returns recently-opened piece paths, most recent first.
	Recents() []string
	// AddRecent records a piece as opened.
	AddRecent(path string)
	// SoundFont returns the configured .sf2 path ("" = built-in pluck).
	SoundFont() string
	// SetSoundFont records the .sf2 path ("" selects the built-in).
	SetSoundFont(path string)
	// CountIn returns the configured count-in beats.
	CountIn() int
	// SetCountIn records the count-in beats.
	SetCountIn(beats int)
	// Devices returns the preferred capture and playback device IDs.
	Devices() (captureID, playbackID string)
	// SetDevices records the preferred device IDs.
	SetDevices(captureID, playbackID string)
	// Save persists the current values, returning any write error for
	// the screen to surface.
	Save() error
}

// A DeviceOption is one selectable audio endpoint.
type DeviceOption struct {
	ID      string
	Name    string
	Default bool
}

// AudioServices exposes device selection and latency calibration.
type AudioServices interface {
	// BackendName identifies the audio backend for display.
	BackendName() string
	// Devices lists the capture and playback endpoints.
	Devices() (capture, playback []DeviceOption, err error)
	// CalibratedOffset reports the stored round-trip offset in frames
	// for a device pair and whether one has been measured.
	CalibratedOffset(captureID, playbackID string) (frames int, ok bool)
	// Calibrate measures the round trip for a device pair, storing the
	// result. It blocks for several seconds, so screens must run it off
	// the game loop and poll; progress is reported in [0, 1].
	Calibrate(captureID, playbackID string, progress func(float64)) (frames int, confidence float64, err error)
}

// A Shell hosts a stack of screens and is the ebiten.Game the window
// actually runs. The screen on top receives Update and Draw; the ones
// beneath it are suspended, not destroyed, so returning from settings or
// from a piece lands exactly where the user left off.
type Shell struct {
	svc   Services
	stack []Screen
	// pending defers stack edits made during Update until the end of the
	// frame, so a screen can Show or finish from inside its own Update
	// without the slice changing under the caller.
	pending []func()
	title   string
}

// NewShell creates the application host with root as its first screen.
func NewShell(svc Services, root Screen) *Shell {
	return &Shell{svc: svc, stack: []Screen{root}, title: "guitarTutor"}
}

// Services exposes the application facilities to screens.
func (s *Shell) Services() Services { return s.svc }

// Show pushes a screen on top of the current one. Safe to call from
// inside a screen's Update: the push takes effect at the end of the frame.
func (s *Shell) Show(sc Screen) {
	s.pending = append(s.pending, func() { s.stack = append(s.stack, sc) })
}

// Pop finishes the current screen and reveals the one beneath. Popping
// the last screen exits the application. Safe to call from inside Update.
func (s *Shell) Pop() {
	s.pending = append(s.pending, func() {
		if n := len(s.stack); n > 0 {
			s.stack = s.stack[:n-1]
		}
	})
}

// Replace swaps the current screen for another without growing the stack.
func (s *Shell) Replace(sc Screen) {
	s.pending = append(s.pending, func() {
		if n := len(s.stack); n > 0 {
			s.stack[n-1] = sc
		} else {
			s.stack = []Screen{sc}
		}
	})
}

// SetTitle updates the window title (e.g. to the open piece's name).
func (s *Shell) SetTitle(t string) {
	if t == s.title {
		return
	}
	s.title = t
	ebiten.SetWindowTitle(t)
}

// Depth reports how many screens are stacked; 1 means the root is
// showing. Screens use it to decide whether "back" or "quit" is the
// honest label for leaving.
func (s *Shell) Depth() int { return len(s.stack) }

// OpenPiece loads a piece through the Opener and shows its practice
// screen, recording it as recently used. The returned warnings are the
// importer's; the error is for the caller to display.
func (s *Shell) OpenPiece(path string) ([]string, error) {
	sc, warns, err := s.svc.Opener.Open(path)
	if err != nil {
		return warns, err
	}
	if s.svc.Prefs != nil {
		s.svc.Prefs.AddRecent(path)
		// A failed save must not block practice; the settings screen
		// surfaces persistence problems.
		_ = s.svc.Prefs.Save()
	}
	s.Show(sc)
	return warns, nil
}

// Update advances the top screen and applies any stack changes it made.
func (s *Shell) Update() error {
	if len(s.stack) == 0 {
		return errQuit
	}
	top := s.stack[len(s.stack)-1]
	err := top.Update()
	// A screen that finished pops itself; the last one ends the app.
	if err == errQuit {
		if len(s.stack) == 1 && len(s.pending) == 0 {
			return errQuit
		}
		s.Pop()
		// Leaving a practice screen releases its audio.
		if _, isPractice := top.(*App); isPractice && s.svc.Opener != nil {
			s.svc.Opener.CloseCurrent()
			s.SetTitle("guitarTutor")
		}
		// A screen that owns resources of its own releases them here, so
		// nothing it started outlives it (see Closer).
		if c, ok := top.(Closer); ok {
			c.Close()
		}
		err = nil
	}
	for _, fn := range s.pending {
		fn()
	}
	s.pending = s.pending[:0]
	if err != nil {
		return err
	}
	if len(s.stack) == 0 {
		return errQuit
	}
	return nil
}

// Draw renders the top screen.
func (s *Shell) Draw(dst *ebiten.Image) {
	if n := len(s.stack); n > 0 {
		s.stack[n-1].Draw(dst)
	}
}

// Layout fixes the logical resolution for every screen.
func (s *Shell) Layout(int, int) (int, int) { return screenW, screenH }

// Run opens the window and blocks until the user quits.
func (s *Shell) Run() error {
	ebiten.SetWindowSize(screenW, screenH)
	ebiten.SetWindowTitle(s.title)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	if err := ebiten.RunGame(s); err != nil && err != errQuit {
		return err
	}
	return nil
}
