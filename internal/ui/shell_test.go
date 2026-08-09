package ui

import (
	"testing"

	"github.com/hajimehoshi/ebiten/v2"
)

// shellPlainScreen is a Screen with nothing to release: it must survive
// being popped exactly as it always did.
type shellPlainScreen struct {
	updates int
	err     error
}

func (s *shellPlainScreen) Update() error {
	s.updates++
	return s.err
}
func (s *shellPlainScreen) Draw(*ebiten.Image) {}

// shellClosingScreen implements the optional Closer extension.
type shellClosingScreen struct {
	shellPlainScreen
	closes int
}

func (s *shellClosingScreen) Close() { s.closes++ }

// TestShellPopClosesScreen is the regression test for the teardown hook:
// a popped screen that implements Closer must have Close called exactly
// once, so a settings screen can cancel the calibration it started rather
// than let it hold the audio device after the screen is gone. Before the
// hook existed Close was never called at all.
func TestShellPopClosesScreen(t *testing.T) {
	root := &shellPlainScreen{}
	sh := NewShell(Services{}, root)

	top := &shellClosingScreen{}
	sh.Show(top)
	// Show defers the push to the end of the frame.
	if err := sh.Update(); err != nil {
		t.Fatalf("Update (root, pushing): %v", err)
	}
	if sh.Depth() != 2 {
		t.Fatalf("depth %d after Show, want 2", sh.Depth())
	}
	if top.closes != 0 {
		t.Fatalf("Close called %d times before the screen was popped", top.closes)
	}

	// The top screen finishes.
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

	// Later frames run the root again and must not re-close the popped
	// screen.
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

// TestShellPopWithoutCloser: a screen that does not implement Closer is
// unaffected by the hook — it pops, and the shell keeps running.
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

	// And the last screen finishing still ends the application.
	root.err = errQuit
	if err := sh.Update(); err != errQuit {
		t.Errorf("Update (root finishing) = %v, want errQuit", err)
	}
}
