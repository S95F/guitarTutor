package practice

import (
	"testing"

	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/score"
)

func TestWaitGateSingleNote(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)})
	if g.Offer([]pitch.Note{det(0, 41, 0)}) {
		t.Error("wrong key satisfied the gate")
	}
	if !g.Offer([]pitch.Note{det(0, 40, 10)}) {
		t.Error("right key did not satisfy the gate")
	}
}

func TestWaitGateChordAcrossOffers(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40), ev(47), ev(52)})
	// Any order, across calls.
	if g.Offer([]pitch.Note{det(0, 52, 0)}) {
		t.Error("satisfied after 1 of 3 notes")
	}
	if g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Error("satisfied after 2 of 3 notes")
	}
	if g.Offer([]pitch.Note{det(0, 52, 0)}) {
		t.Error("repeating an already-satisfied note completed the chord")
	}
	if !g.Offer([]pitch.Note{det(0, 47, 0)}) {
		t.Error("full chord did not satisfy the gate")
	}
	// Once satisfied it stays satisfied until re-armed.
	if !g.Offer(nil) {
		t.Error("satisfied gate reported false on a later empty Offer")
	}
}

func TestWaitGateCentsTolerance(t *testing.T) {
	// Default CloseCents = 70: waiting is lenient about intonation.
	tests := []struct {
		name  string
		cents float64
		want  bool
	}{
		{"in tune", 0, true},
		{"60 cents flat", -60, true},
		{"60 cents sharp", 60, true},
		{"80 cents flat", -80, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWaitGate(testConfig())
			g.Arm([]score.NoteEvent{ev(45)})
			if got := g.Offer([]pitch.Note{det(0, 45, tt.cents)}); got != tt.want {
				t.Errorf("Offer with %v cents = %v, want %v", tt.cents, got, tt.want)
			}
		})
	}
}

func TestWaitGateOctaveExact(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)})
	if g.Offer([]pitch.Note{det(0, 52, 0)}) {
		t.Error("octave up satisfied the gate; waiting requires the exact octave")
	}
	if g.Offer([]pitch.Note{det(0, 28, 0)}) {
		t.Error("octave down satisfied the gate")
	}
	if !g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Error("exact octave did not satisfy the gate")
	}
}

func TestWaitGateRearmResets(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)})
	if !g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Fatal("first arm not satisfied")
	}
	g.Arm([]score.NoteEvent{ev(40)})
	if g.Offer(nil) {
		t.Error("re-armed gate still satisfied without new notes")
	}
	if !g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Error("re-armed gate not satisfied by the note")
	}
}

func TestWaitGateUnarmed(t *testing.T) {
	g := NewWaitGate(testConfig())
	if g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Error("unarmed gate reported satisfied")
	}
	// An explicitly empty armed set also reports false: there is
	// nothing to confirm.
	g.Arm(nil)
	if g.Offer(nil) {
		t.Error("empty armed set reported satisfied")
	}
}
