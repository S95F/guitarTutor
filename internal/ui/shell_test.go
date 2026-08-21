package ui

import (
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

type shellPlainScreen struct {
	updates int
	err     error
}

func (s *shellPlainScreen) Update() error {
	s.updates++
	return s.err
}
func (s *shellPlainScreen) Draw(*ebiten.Image) {}

type shellClosingScreen struct {
	shellPlainScreen
	closes int
}

func (s *shellClosingScreen) Close() { s.closes++ }

func TestShellPopClosesScreen(t *testing.T) {
	root := &shellPlainScreen{}
	sh := NewShell(Services{}, root)

	top := &shellClosingScreen{}
	sh.Show(top)

	if err := sh.Update(); err != nil {
		t.Fatalf("Update (root, pushing): %v", err)
	}
	if sh.Depth() != 2 {
		t.Fatalf("depth %d after Show, want 2", sh.Depth())
	}
	if top.closes != 0 {
		t.Fatalf("Close called %d times before the screen was popped", top.closes)
	}

	top.err = errQuit
	if err := sh.Update(); err != nil {
		t.Fatalf("Update (popping the closer): %v", err)
	}
	if sh.Depth() != 1 {
		t.Fatalf("depth %d after the top screen finished, want 1", sh.Depth())
	}
	if top.closes != 1 {
		t.Fatalf("Close called %d times on the popped screen, want exactly 1", top.closes)
	}

	if err := sh.Update(); err != nil {
		t.Fatalf("Update (root again): %v", err)
	}
	if top.closes != 1 {
		t.Errorf("Close called %d times in total, want exactly 1", top.closes)
	}
	if top.updates != 1 {
		t.Errorf("popped screen updated %d times, want 1", top.updates)
	}
}

func TestShellPopWithoutCloser(t *testing.T) {
	root := &shellPlainScreen{}
	sh := NewShell(Services{}, root)

	top := &shellPlainScreen{}
	sh.Show(top)
	if err := sh.Update(); err != nil {
		t.Fatalf("Update (root, pushing): %v", err)
	}

	top.err = errQuit
	if err := sh.Update(); err != nil {
		t.Fatalf("Update (popping a plain screen): %v", err)
	}
	if sh.Depth() != 1 {
		t.Fatalf("depth %d after the plain screen finished, want 1", sh.Depth())
	}

	root.err = errQuit
	if err := sh.Update(); err != errQuit {
		t.Errorf("Update (root finishing) = %v, want errQuit", err)
	}
}

func TestShellQuitDropsEveryScreen(t *testing.T) {
	root := &shellPlainScreen{}
	sh := NewShell(Services{}, root)
	top := &shellClosingScreen{}
	sh.Show(top)
	if err := sh.Update(); err != nil {
		t.Fatalf("Update (pushing): %v", err)
	}

	sh.Quit()
	if err := sh.Update(); err != errQuit {
		t.Fatalf("Update after Quit = %v, want errQuit", err)
	}
	if top.closes != 1 {
		t.Errorf("the closing screen was closed %d times on quit, want exactly 1", top.closes)
	}
	if sh.Depth() != 0 {
		t.Errorf("depth after quit = %d, want 0", sh.Depth())
	}
}
