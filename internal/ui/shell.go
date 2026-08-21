package ui

import (
	"fmt"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

type Screen interface {
	Update() error
	Draw(*ebiten.Image)
}

type Closer interface {
	Close()
}

type Opener interface {
	Open(path string) (Screen, []string, error)

	CloseCurrent()
}

type Services struct {
	Opener Opener

	Prefs Prefs

	Audio AudioServices

	Library Library
}

type PieceInfo struct {
	Path  string
	Name  string
	Title string

	Summary string

	Modified time.Time

	Problem string
}

type Library interface {
	Dir() string

	Scan() ([]PieceInfo, error)
}

type Prefs interface {
	Recents() []string

	Created() []string

	AddCreated(path string)

	HintHidden() bool
	SetHintHidden(hidden bool)

	AddRecent(path string)

	SoundFont() string

	SetSoundFont(path string)

	CountIn() int

	SetCountIn(beats int)

	Devices() (captureID, playbackID string)

	SetDevices(captureID, playbackID string)

	Save() error
}

type DeviceOption struct {
	ID      string
	Name    string
	Default bool
}

type AudioServices interface {
	BackendName() string

	Devices() (capture, playback []DeviceOption, err error)

	CalibratedOffset(captureID, playbackID string) (frames int, ok bool)

	Calibrate(captureID, playbackID string, progress func(float64)) (frames int, confidence float64, err error)
}

type Shell struct {
	svc   Services
	stack []Screen

	pending []func()

	quitting bool
	title    string
}

func NewShell(svc Services, root Screen) *Shell {
	return &Shell{svc: svc, stack: []Screen{root}, title: "musicTutor"}
}

func (s *Shell) Services() Services { return s.svc }

func (s *Shell) Show(sc Screen) {
	s.pending = append(s.pending, func() {
		if s.quitting {
			return
		}
		s.stack = append(s.stack, sc)
	})
}

func (s *Shell) Pop() {
	s.pending = append(s.pending, func() {
		if n := len(s.stack); n > 0 {
			s.stack = s.stack[:n-1]
		}
	})
}

func (s *Shell) Replace(sc Screen) {
	s.pending = append(s.pending, func() {
		if s.quitting {
			return
		}
		if n := len(s.stack); n > 0 {
			s.stack[n-1] = sc
		} else {
			s.stack = []Screen{sc}
		}
	})
}

func (s *Shell) SetTitle(t string) {
	if t == s.title {
		return
	}
	s.title = t
	ebiten.SetWindowTitle(t)
}

func (s *Shell) Depth() int { return len(s.stack) }

func (s *Shell) Quit() {
	s.pending = append(s.pending, func() {
		s.quitting = true
		for i := len(s.stack) - 1; i >= 0; i-- {
			if c, ok := s.stack[i].(Closer); ok {
				c.Close()
			}
		}
		s.stack = nil
	})
}

func (s *Shell) OpenPiece(path string) ([]string, error) {
	sc, warns, err := s.loadPiece(path)
	if err != nil {
		return warns, err
	}
	s.Show(sc)
	return warns, nil
}

func (s *Shell) ReopenPiece(path string) ([]string, error) {
	sc, warns, err := s.loadPiece(path)
	if err != nil {
		return warns, err
	}
	s.Replace(sc)
	return warns, nil
}

func (s *Shell) loadPiece(path string) (Screen, []string, error) {
	if s.svc.Opener == nil {
		return nil, nil, errNoOpener
	}
	sc, warns, err := s.svc.Opener.Open(path)
	if err != nil {
		return nil, warns, err
	}
	if s.svc.Prefs != nil {
		s.svc.Prefs.AddRecent(path)

		_ = s.svc.Prefs.Save()
	}
	return sc, warns, nil
}

var errNoOpener = fmt.Errorf("no importer is available in this build")

func (s *Shell) Update() error {
	if len(s.stack) == 0 {
		return errQuit
	}
	top := s.stack[len(s.stack)-1]
	err := top.Update()

	if err == errQuit {
		if len(s.stack) == 1 && len(s.pending) == 0 {
			return errQuit
		}
		s.Pop()

		if _, isPractice := top.(*App); isPractice && s.svc.Opener != nil {
			s.svc.Opener.CloseCurrent()
			s.SetTitle("musicTutor")
		}

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

func (s *Shell) Draw(dst *ebiten.Image) {
	if n := len(s.stack); n > 0 {
		s.stack[n-1].Draw(dst)
	}
}

func (s *Shell) Layout(int, int) (int, int) { return screenW, screenH }

func (s *Shell) Run() error {
	ebiten.SetWindowSize(screenW, screenH)
	ebiten.SetWindowTitle(s.title)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	if err := ebiten.RunGame(s); err != nil && err != errQuit {
		return err
	}
	return nil
}
