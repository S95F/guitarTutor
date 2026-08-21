package practice

import (
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
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

	if g.Offer(nil) {
		t.Error("an already-reported gate claimed satisfaction a second time; a later strum would skip the next wait point")
	}
}

func TestWaitGateCentsTolerance(t *testing.T) {

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

	if !g.Offer([]pitch.Note{det(4800, 40, 0)}) {
		t.Error("re-armed gate not satisfied by a new attack")
	}
}

func TestWaitGateSpentAttackCannotRelease(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 40800)
	played := det(48000, 40, 0)
	if !g.Offer([]pitch.Note{played}) {
		t.Fatal("the first wait point was not released")
	}

	g.Arm([]score.NoteEvent{ev(40)}, 46800)
	if g.Offer([]pitch.Note{played}) {
		t.Error("the previous wait point's attack released this one too")
	}
	if !g.Offer([]pitch.Note{det(54200, 40, 0)}) {
		t.Error("a genuine second pluck did not release the wait")
	}
}

func TestWaitGateSpentSetForgetsStaleAttacks(t *testing.T) {
	g := NewWaitGate(testConfig())
	for i := int64(0); i < 100; i++ {
		g.Arm([]score.NoteEvent{ev(40)}, i*6000)
		if !g.Offer([]pitch.Note{det(i*6000+100, 40, 0)}) {
			t.Fatalf("wait point %d not released", i)
		}
	}
	if n := len(g.spent); n > 4 {
		t.Errorf("spent set holds %d attacks after 100 wait points, want a handful", n)
	}
}

func TestWaitGateDeadNoteReleasedByStrum(t *testing.T) {
	dead := score.NoteEvent{Track: 0, Key: 45, Tech: score.TechDead}
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{dead}, 48000)

	if g.Offer([]pitch.Note{det(48200, 44, 0)}) {
		t.Error("a wrong-key detection released the wait")
	}

	if g.OfferStrum(pitch.Strum{Frame: 30000, RMS: 0.2}) {
		t.Error("a stale attack released the wait")
	}
	if !g.OfferStrum(pitch.Strum{Frame: 48200, RMS: 0.2}) {
		t.Error("a fresh attack did not release a dead-note wait point: playback would halt forever")
	}
}

func TestWaitGateDeadNoteInChord(t *testing.T) {
	dead := func(key int) score.NoteEvent {
		return score.NoteEvent{Track: 0, Key: key, Tech: score.TechDead}
	}
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40), dead(45), dead(50)}, 0)

	if g.OfferStrum(pitch.Strum{Frame: 100, RMS: 0.2}) {
		t.Error("the strum released the pitched note too")
	}
	if !g.Offer([]pitch.Note{det(200, 40, 0)}) {
		t.Error("both dead strings plus the pitched note did not complete the chord")
	}
}

func TestWaitGateSlideDestination(t *testing.T) {
	slid := score.NoteEvent{Track: 0, Key: 41, Tech: score.TechSlide}
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{slid}, 0)

	if g.Offer([]pitch.Note{det(1000, 40, 0)}) {
		t.Error("a note that swept nowhere released a slide wait point")
	}
	if g.Offer([]pitch.Note{slideTo(1000, 0, 40, -1)}) {
		t.Error("a slide in the wrong direction released the wait")
	}
	if !g.Offer([]pitch.Note{slideTo(1000, 0, 40, 1)}) {
		t.Error("a string slid onto the destination did not release the wait: playback would halt forever")
	}
}

func TestWaitGateRejectsPreArmAttack(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 48000)

	ringing := pitch.Note{Start: 20000, Key: 40, Cents: 0, Clarity: 0.95}
	if g.Offer([]pitch.Note{ringing}) {
		t.Error("a note attacked before minStart satisfied the gate")
	}

	if g.Offer([]pitch.Note{ringing}) {
		t.Error("re-offering the pre-arm note satisfied the gate")
	}
	if !g.Offer([]pitch.Note{det(48500, 40, 0)}) {
		t.Error("a fresh attack after minStart did not satisfy the gate")
	}
}

func TestWaitGateChordWithMinStart(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40), ev(47), ev(52)}, 48000)

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

	g.Arm(nil, 0)
	if g.Offer(nil) {
		t.Error("empty armed set reported satisfied")
	}
}

func TestWaitGateSlideDestinationAcceptsTheSpentOriginAttack(t *testing.T) {
	g := NewWaitGate(testConfig())

	g.Arm([]score.NoteEvent{ev(62)}, 100000)
	origin := pitch.Note{Start: 100500, Key: 62, Cents: 2, MinCents: -3, MaxCents: 4, EndCents: 1}
	if !g.Offer([]pitch.Note{origin}) {
		t.Fatal("the origin attack did not release its own wait point")
	}

	g.Arm([]score.NoteEvent{{Key: 64, Tech: score.TechSlide}}, 94740)

	slid := pitch.Note{Start: 100500, Key: 62, Cents: 60, MinCents: -3, MaxCents: 203, EndCents: 199}
	if !g.Offer([]pitch.Note{slid}) {
		t.Error("the slide never released its wait point; playback would halt there forever")
	}
}

func TestWaitGateSpentAttackStillBarredFromAPitchedWait(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 0)
	n := det(1000, 40, 0)
	if !g.Offer([]pitch.Note{n}) {
		t.Fatal("the fresh attack did not release the first wait point")
	}
	g.Arm([]score.NoteEvent{ev(40)}, 0)
	if g.Offer([]pitch.Note{n}) {
		t.Error("the spent attack released a second pitched wait point")
	}
}

func TestWaitGateSlideReachIsTight(t *testing.T) {
	tests := []struct {
		name     string
		destKey  int
		min, max float64
		end      float64
		release  bool
	}{
		{"ordinary vibrato, one fret up", 63, -30, 30, 0, false},
		{"wide vibrato, one fret up", 63, -60, 60, 0, false},
		{"a note that never moved", 63, 0, 0, 0, false},
		{"a bend swept past the pitch and came back", 64, 0, 200, 0, false},
		{"a genuine one-fret slide, held", 63, 0, 104, 104, true},

		{"a whole-step bend held at the destination", 64, 0, 200, 200, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWaitGate(testConfig())
			g.Arm([]score.NoteEvent{{Key: tt.destKey, Tech: score.TechSlide}}, 200000-7200)
			n := pitch.Note{
				Start:    100000,
				Key:      62,
				MinCents: tt.min,
				MaxCents: tt.max,
				EndCents: tt.end,
			}
			if got := g.Offer([]pitch.Note{n}); got != tt.release {
				t.Errorf("Offer = %v, want %v (swept %v..%v, settled at %v, destination key %d)",
					got, tt.release, tt.min, tt.max, tt.end, tt.destKey)
			}
		})
	}
}
