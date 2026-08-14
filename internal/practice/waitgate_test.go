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
	// Completion is reported ONCE per arm. This assertion used to be its
	// opposite — "once satisfied it stays satisfied until re-armed" — and
	// that is precisely what made the gate dangerous: the caller's strum
	// path runs before the path that re-arms, so every attack landing
	// after a confirmation found a full gate, was told "satisfied", and
	// released the NEXT wait point the player had not answered. Answering
	// once makes that impossible for every caller rather than asking each
	// one to remember (see WaitGate.satisfied).
	if g.Offer(nil) {
		t.Error("an already-reported gate claimed satisfaction a second time; a later strum would skip the next wait point")
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
	// A genuinely new pluck, not a re-offer of the attack that released
	// the previous wait point — see TestWaitGateSpentAttackCannotRelease.
	if !g.Offer([]pitch.Note{det(4800, 40, 0)}) {
		t.Error("re-armed gate not satisfied by a new attack")
	}
}

// TestWaitGateSpentAttackCannotRelease is the C1 regression: one attack
// releases one wait point.
//
// The wiring arms each wait point with a fixed span of anticipation grace
// (~150 ms) so a player who leads the beat by a hair still confirms. At
// 16th notes — 6000 frames at 120 BPM — consecutive wait points are closer
// together than that grace, so the note that released the first one is
// still inside the second one's freshness bound AND still ringing, and gets
// re-offered every batch from Tracker.Current. It released the second wait
// point too, and Scorer.WaitConfirmed then awarded a Hit for a note nobody
// played. No value of the grace works at every tempo; identity does.
func TestWaitGateSpentAttackCannotRelease(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 40800) // wait noticed at 48000, less the grace
	played := det(48000, 40, 0)
	if !g.Offer([]pitch.Note{played}) {
		t.Fatal("the first wait point was not released")
	}

	// One 16th later, the same note again — and the still-ringing first
	// attack passes the freshness bound of this wait point too.
	g.Arm([]score.NoteEvent{ev(40)}, 46800)
	if g.Offer([]pitch.Note{played}) {
		t.Error("the previous wait point's attack released this one too")
	}
	if !g.Offer([]pitch.Note{det(54200, 40, 0)}) {
		t.Error("a genuine second pluck did not release the wait")
	}
}

// TestWaitGateSpentSetForgetsStaleAttacks: the spent set is pruned against
// each new minStart, so it cannot grow without bound over a session — and
// an attack old enough to be dropped from it was already being rejected on
// freshness anyway.
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

// TestWaitGateDeadNoteReleasedByStrum is the P3 regression. A wait point on
// a dead note (fixture_rich.gtab bar 2 ends in `9.4x`) could never be
// satisfied: the gate wanted a pitched detection at the note's derived key,
// and a damped string produces no trackable f0 at all. Playback halted
// there permanently, with nothing the player could do about it. The attack
// is the evidence a damped note is entitled to — the same signal the scorer
// already credits one from at its deadline.
func TestWaitGateDeadNoteReleasedByStrum(t *testing.T) {
	dead := score.NoteEvent{Track: 0, Key: 45, Tech: score.TechDead}
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{dead}, 48000)

	// Nothing pitched ever arrives for a dead note; if something did, it
	// would not be this note.
	if g.Offer([]pitch.Note{det(48200, 44, 0)}) {
		t.Error("a wrong-key detection released the wait")
	}
	// An attack stamped before the wait engaged is not this note's.
	if g.OfferStrum(pitch.Strum{Frame: 30000, RMS: 0.2}) {
		t.Error("a stale attack released the wait")
	}
	if !g.OfferStrum(pitch.Strum{Frame: 48200, RMS: 0.2}) {
		t.Error("a fresh attack did not release a dead-note wait point: playback would halt forever")
	}
}

// TestWaitGateDeadNoteInChord: the strum path speaks only for the dead
// notes. Every muted string in the chord goes down to one strike — one
// strum IS one strike of however many damped strings — while the pitched
// notes still have to be played.
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

// TestWaitGateSlideDestination is the wait-mode half of P2. A slide
// destination is never attacked and never takes the tracker's key — the
// string is already sounding and simply moves — so an exact-key gate wedges
// on one exactly as it does on a dead note. The trajectory is the evidence.
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

// TestWaitGateSlideDestinationAcceptsTheSpentOriginAttack is the
// fix-verification regression for a collision between two fixes made in
// the same round: attack identity (one attack cannot release two wait
// points) and slide matching (a slide destination is evidenced by the
// origin note's trajectory).
//
// A legato slide has no attack of its own. The origin's attack releases
// the origin's wait point and is spent; the destination's wait point
// arrives a moment later, and the ONLY evidence it will ever have is that
// same, now-spent attack. Requiring a fresh one halted playback at every
// slide in the piece, permanently — the same class of failure the
// dead-note fix was for.
func TestWaitGateSlideDestinationAcceptsTheSpentOriginAttack(t *testing.T) {
	g := NewWaitGate(testConfig())

	// Wait N: the origin note, key 62.
	g.Arm([]score.NoteEvent{ev(62)}, 100000)
	origin := pitch.Note{Start: 100500, Key: 62, Cents: 2, MinCents: -3, MaxCents: 4, EndCents: 1}
	if !g.Offer([]pitch.Note{origin}) {
		t.Fatal("the origin attack did not release its own wait point")
	}

	// Wait N+1: the slide destination, key 64, armed a moment later. The
	// origin attack is now spent.
	g.Arm([]score.NoteEvent{{Key: 64, Tech: score.TechSlide}}, 94740)
	// The player slides: the SAME tracker note, trajectory now reaching
	// +200 cents (two semitones up, i.e. key 64).
	slid := pitch.Note{Start: 100500, Key: 62, Cents: 60, MinCents: -3, MaxCents: 203, EndCents: 199}
	if !g.Offer([]pitch.Note{slid}) {
		t.Error("the slide never released its wait point; playback would halt there forever")
	}
}

// TestWaitGateSpentAttackStillBarredFromAPitchedWait guards the other
// side: exempting slides from the spent check must not exempt ordinary
// notes, or a still-ringing note releases the next same-key wait point
// again — the very thing attack identity was added to stop.
func TestWaitGateSpentAttackStillBarredFromAPitchedWait(t *testing.T) {
	g := NewWaitGate(testConfig())
	g.Arm([]score.NoteEvent{ev(40)}, 0)
	n := det(1000, 40, 0)
	if !g.Offer([]pitch.Note{n}) {
		t.Fatal("the fresh attack did not release the first wait point")
	}
	g.Arm([]score.NoteEvent{ev(40)}, 0) // same key again, attack still "fresh"
	if g.Offer([]pitch.Note{n}) {
		t.Error("the spent attack released a second pitched wait point")
	}
}

// TestWaitGateSlideReachIsTight is the fix-verification regression for the
// slide exemption. Slide destinations are exempt from the freshness and
// identity checks, because a legato slide has no attack of its own — which
// leaves the pitch trajectory as the ONLY evidence, so what counts as
// "reached" is what decides whether a wait point can be skipped by
// accident.
//
// The gate asks whether the destination is what the string is sounding
// NOW: the position is frozen and the player is holding the note. Merely
// having swept over the pitch at some earlier moment is the SCORER's
// question — worth partial credit there, not worth releasing a halt here.
// Measured the loose way, ordinary vibrato on a held note released a wait
// the player never slid to, and a bend left ringing from an earlier phrase
// released one they had already finished.
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
		// Bending to the destination and HOLDING it is a different
		// technique, but the player is sounding the note the score asked
		// for, and a monophonic tracker cannot tell the two apart anyway.
		// Releasing is the forgiving answer and the one this project wants.
		{"a whole-step bend held at the destination", 64, 0, 200, 200, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWaitGate(testConfig())
			g.Arm([]score.NoteEvent{{Key: tt.destKey, Tech: score.TechSlide}}, 200000-7200)
			n := pitch.Note{
				Start:    100000, // attacked long before this wait armed
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
