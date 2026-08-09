// Package practice scores what the guitarist plays against what the score
// expects, and drives wait mode. It sits between the engine's event tap
// (expected notes, output-frame-stamped) and the pitch tracker's output
// (detected notes, input-frame-stamped); the calibrated round-trip offset
// reconciles the two clocks (docs/DECISIONS.md D1).
//
// Scoring philosophy (DECISIONS D5): forgiving and optional. Timing
// windows are wide (~±150 ms — the detection physics alone costs 25–50 ms,
// see D4), a wrong verdict must never block practice, and everything is
// tunable.
package practice

import (
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/score"
)

// Verdict classifies one expected note.
type Verdict int

const (
	// VerdictHit: right key, within the cents tolerance, inside the
	// timing window.
	VerdictHit Verdict = iota
	// VerdictClose: right key but loose intonation, or right pitch
	// class an octave off — credit for learning, not for mastery.
	VerdictClose
	// VerdictMiss: nothing matching arrived in the window.
	VerdictMiss
)

// A NoteResult is the final judgement of one expected note.
type NoteResult struct {
	// Event is the expected note (Track, Key, tick span).
	Event score.NoteEvent
	// OutFrame is the output frame the note sounded on (engine tap).
	OutFrame int64
	// Verdict is the judgement.
	Verdict Verdict
	// Matched reports whether any detection paired with the note;
	// false for a pure miss, and then ErrCents/ErrFrames are 0.
	Matched bool
	// ErrCents is the matched detection's deviation from the expected
	// key (negative = flat).
	ErrCents float64
	// ErrFrames is the timing error in stream frames (positive = the
	// player was late), after latency compensation.
	ErrFrames int64
}

// Stats accumulates verdicts.
type Stats struct{ Hit, Close, Miss int }

// Accuracy returns hits (full) plus closes (half credit) over the total,
// in [0, 1]; 1 when nothing has been judged yet.
func (s Stats) Accuracy() float64 {
	n := s.Hit + s.Close + s.Miss
	if n == 0 {
		return 1
	}
	return (float64(s.Hit) + 0.5*float64(s.Close)) / float64(n)
}

// Config parameterizes a Scorer.
type Config struct {
	// SampleRate of the stream clock.
	SampleRate int
	// Track is the score track index being practiced; expected notes
	// from other tracks are ignored.
	Track int
	// TimingWindowFrames is the half-width of the acceptance window
	// around an expected note. 0 means 150 ms worth of frames.
	TimingWindowFrames int
	// CentsTolerance is the full-credit intonation bound. 0 means 35.
	CentsTolerance float64
	// CloseCents is the partial-credit bound. 0 means 70.
	CloseCents float64
	// LatencyOffsetFrames is the calibrated round-trip offset: a note
	// sounded by the engine at output frame F arrives back in the
	// capture stream near input frame F + offset.
	LatencyOffsetFrames int
}

// A Scorer matches detections against expectations and emits results.
//
// Threading: ExpectNote is called from the engine's render goroutine (via
// the event tap — keep it cheap), Detected and Advance from the analysis
// goroutine, and Results/Stats from the UI goroutine. The scorer
// synchronizes internally with short critical sections.
type Scorer struct {
	// contains filtered or unexported fields
}

// NewScorer builds a scorer. Zero Config fields take the documented
// defaults.
func NewScorer(cfg Config) *Scorer { panic("practice: not implemented") }

// ExpectNote feeds one scheduled note from the engine's event tap. Notes
// from tracks other than Config.Track are ignored.
func (s *Scorer) ExpectNote(ev score.NoteEvent, outFrame int64) { panic("practice: not implemented") }

// Detected feeds closed notes from the pitch tracker.
func (s *Scorer) Detected(notes []pitch.Note) { panic("practice: not implemented") }

// Advance finalizes expectations whose windows have passed as of the
// given input-stream frame (call it as capture progresses).
func (s *Scorer) Advance(inFrame int64) { panic("practice: not implemented") }

// Results appends finalized results since the last call to dst and
// returns it.
func (s *Scorer) Results(dst []NoteResult) []NoteResult { panic("practice: not implemented") }

// Stats returns the running totals since the last Reset.
func (s *Scorer) Stats() Stats { panic("practice: not implemented") }

// Reset clears stats and pending state (loop restart, seek).
func (s *Scorer) Reset() { panic("practice: not implemented") }

// A WaitGate drives engine wait mode from detections: when the engine is
// waiting on a note or chord, the gate watches the detection stream and
// confirms once every expected key has been played (any octave-exact
// match within the cents tolerance).
type WaitGate struct {
	// contains filtered or unexported fields
}

// NewWaitGate builds a gate sharing the scorer's tolerances.
func NewWaitGate(cfg Config) *WaitGate { panic("practice: not implemented") }

// Arm sets the events the engine is waiting on (from Engine.WaitingOn).
func (g *WaitGate) Arm(events []score.NoteEvent) { panic("practice: not implemented") }

// Offer feeds detections; it returns true when the armed set is fully
// satisfied (the caller then calls Engine.ConfirmWait and re-arms at the
// next wait point).
func (g *WaitGate) Offer(notes []pitch.Note) bool { panic("practice: not implemented") }
