package practice

import (
	"testing"

	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/score"
)

func TestWaitGateSingleNote(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 0)
	if g.Offer([]pitch.Note{det(0, 41, 0)}) {
		t.Error("wrong key satisfied the gate")
	}
	if !g.Offer([]pitch.Note{det(0, 40, 10)}) {
		t.Error("right key did not satisfy the gate")
	}
}

func TestWaitGateChordAcrossOffers(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40), ev(47), ev(52)}, 0)
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
			g.Arm([]score.NoteEvent{ev(45)}, 0)
			if got := g.Offer([]pitch.Note{det(0, 45, tt.cents)}); got != tt.want {
				t.Errorf("Offer with %v cents = %v, want %v", tt.cents, got, tt.want)
			}
		})
	}
}

func TestWaitGateOctaveExact(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 0)
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
	g.Arm([]score.NoteEvent{ev(40)}, 0)
	if !g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Fatal("first arm not satisfied")
	}
	g.Arm([]score.NoteEvent{ev(40)}, 0)
	if g.Offer(nil) {
		t.Error("re-armed gate still satisfied without new notes")
	}
	if !g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Error("re-armed gate not satisfied by the note")
	}
}

// TestWaitGateRejectsPreArmAttack is the W3 regression: a note attacked
// BEFORE the wait engaged — e.g. the previous phrase's same-key note still
// ringing when the position froze — must not auto-confirm the wait point.
// Only a fresh attack (Start at or after the armed minStart) releases it.
func TestWaitGateRejectsPreArmAttack(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 48000)
	// Still-sounding note from Tracker.Current: End 0, attacked long
	// before the wait engaged.
	ringing := pitch.Note{Start: 20000, Key: 40, Cents: 0, Clarity: 0.95}
	if g.Offer([]pitch.Note{ringing}) {
		t.Error("a note attacked before minStart satisfied the gate")
	}
	// The same still-Current note re-offered on a later poll holds too.
	if g.Offer([]pitch.Note{ringing}) {
		t.Error("re-offering the pre-arm note satisfied the gate")
	}
	if !g.Offer([]pitch.Note{det(48500, 40, 0)}) {
		t.Error("a fresh attack after minStart did not satisfy the gate")
	}
}

// TestWaitGateChordWithMinStart checks the attack filter composes with
// chord progress across multiple Offers: stale attacks never count, fresh
// ones accumulate as before.
func TestWaitGateChordWithMinStart(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40), ev(47), ev(52)}, 48000)
	// A stale same-key note alongside one fresh attack: only the fresh
	// one counts.
	if g.Offer([]pitch.Note{det(20000, 47, 0), det(48100, 40, 0)}) {
		t.Error("satisfied with only 1 fresh note of 3 (stale 47 counted)")
	}
	if g.Offer([]pitch.Note{det(50000, 52, 0)}) {
		t.Error("satisfied after 2 fresh notes of 3")
	}
	if !g.Offer([]pitch.Note{det(52000, 47, 0)}) {
		t.Error("full chord of fresh attacks did not satisfy the gate")
	}
}

func TestWaitGateUnarmed(t *testing.T) {
	g := NewWaitGate(testConfig())
	if g.Offer([]pitch.Note{det(0, 40, 0)}) {
		t.Error("unarmed gate reported satisfied")
	}
	// An explicitly empty armed set also reports false: there is
	// nothing to confirm.
	g.Arm(nil, 0)
	if g.Offer(nil) {
		t.Error("empty armed set reported satisfied")
	}
}
