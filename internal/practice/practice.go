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
//
// Matching is greedy: each detection pairs with the nearest-in-time
// unmatched expectation inside the timing window (exact key preferred over
// a pitch-class/octave-off match), and each pairing is final — one
// detection satisfies at most one expectation and vice versa. Chords are
// scored one expectation per chord note; the pitch engine is monophonic
// (D4), so a strummed chord typically yields one match and the remaining
// chord notes go Close/Miss. That is the honest Phase 2 behavior — chord
// verification proper is Phase 4.
package practice

import (
	"math"
	"sync"

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

// Default tolerance values for zero Config fields.
const (
	defaultSampleRate     = 48000
	defaultWindowMillis   = 150
	defaultCentsTolerance = 35
	defaultCloseCents     = 70
)

// withDefaults fills zero Config fields with the documented defaults.
func (cfg Config) withDefaults() Config {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = defaultSampleRate
	}
	if cfg.TimingWindowFrames <= 0 {
		cfg.TimingWindowFrames = cfg.SampleRate * defaultWindowMillis / 1000
	}
	if cfg.CentsTolerance <= 0 {
		cfg.CentsTolerance = defaultCentsTolerance
	}
	if cfg.CloseCents <= 0 {
		cfg.CloseCents = defaultCloseCents
	}
	if cfg.CloseCents < cfg.CentsTolerance {
		cfg.CloseCents = cfg.CentsTolerance
	}
	return cfg
}

// An expectation is a tapped note awaiting a detection or its deadline.
type expectation struct {
	ev       score.NoteEvent
	outFrame int64
}

// A Scorer matches detections against expectations and emits results.
//
// Threading: ExpectNote is called from the engine's render goroutine (via
// the event tap — keep it cheap), Detected and Advance from the analysis
// goroutine, and Results/Stats from the UI goroutine. The scorer
// synchronizes internally with short critical sections.
type Scorer struct {
	mu      sync.Mutex
	cfg     Config
	pending []expectation
	results []NoteResult
	stats   Stats
}

// NewScorer builds a scorer. Zero Config fields take the documented
// defaults.
func NewScorer(cfg Config) *Scorer {
	return &Scorer{
		cfg: cfg.withDefaults(),
		// Preallocated so ExpectNote (render goroutine) does not
		// allocate in steady state; growth beyond this is rare and
		// amortized.
		pending: make([]expectation, 0, 256),
		results: make([]NoteResult, 0, 256),
	}
}

// ExpectNote feeds one scheduled note from the engine's event tap. Notes
// from tracks other than Config.Track are ignored.
func (s *Scorer) ExpectNote(ev score.NoteEvent, outFrame int64) {
	if ev.Track != s.cfg.Track {
		return
	}
	s.mu.Lock()
	s.pending = append(s.pending, expectation{ev: ev, outFrame: outFrame})
	s.mu.Unlock()
}

// Detected feeds closed notes from the pitch tracker.
//
// Each note's Start (input frames) is mapped to the output clock by
// subtracting LatencyOffsetFrames, then matched greedily against the
// pending expectations: nearest in time within TimingWindowFrames, exact
// key preferred over a pitch-class-only (octave-off) match. A pairing
// finalizes the expectation immediately — exact key within CentsTolerance
// is a Hit, exact key with looser intonation or an octave-off pitch-class
// match is a Close. Detections that match nothing are dropped (stray
// noise, or the player noodling — never penalized, per D5).
func (s *Scorer) Detected(notes []pitch.Note) {
	s.mu.Lock()
	defer s.mu.Unlock()
	win := int64(s.cfg.TimingWindowFrames)
	off := int64(s.cfg.LatencyOffsetFrames)
	for i := range notes {
		n := &notes[i]
		detOut := n.Start - off
		bestExact, bestClass := -1, -1
		var bestExactAbs, bestClassAbs int64
		for j := range s.pending {
			exp := &s.pending[j]
			dt := detOut - exp.outFrame
			adt := dt
			if adt < 0 {
				adt = -adt
			}
			if adt > win {
				continue
			}
			switch {
			case exp.ev.Key == n.Key:
				if bestExact < 0 || adt < bestExactAbs {
					bestExact, bestExactAbs = j, adt
				}
			case ((exp.ev.Key-n.Key)%12+12)%12 == 0:
				if bestClass < 0 || adt < bestClassAbs {
					bestClass, bestClassAbs = j, adt
				}
			}
		}
		pick, exact := bestClass, false
		if bestExact >= 0 {
			pick, exact = bestExact, true
		}
		if pick < 0 {
			continue
		}
		exp := s.pending[pick]
		s.pending = append(s.pending[:pick], s.pending[pick+1:]...)
		v := VerdictClose
		if exact && math.Abs(n.Cents) <= s.cfg.CentsTolerance {
			v = VerdictHit
		}
		s.finalize(NoteResult{
			Event:     exp.ev,
			OutFrame:  exp.outFrame,
			Verdict:   v,
			Matched:   true,
			ErrCents:  n.Cents,
			ErrFrames: detOut - exp.outFrame,
		})
	}
}

// Advance finalizes expectations whose windows have passed as of the
// given input-stream frame (call it as capture progresses).
//
// Caution: the tracker delivers a note only when it CLOSES, which for a
// sustained note is long after the Start frame the match uses. Pass a
// frame that lags the live capture position by the longest note the
// player might hold (a few seconds), or a Hit still ringing at its
// deadline is finalized as a Miss before its detection arrives.
func (s *Scorer) Advance(inFrame int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	win := int64(s.cfg.TimingWindowFrames)
	off := int64(s.cfg.LatencyOffsetFrames)
	keep := s.pending[:0]
	for _, exp := range s.pending {
		if exp.outFrame+off+win < inFrame {
			s.finalize(NoteResult{
				Event:    exp.ev,
				OutFrame: exp.outFrame,
				Verdict:  VerdictMiss,
			})
			continue
		}
		keep = append(keep, exp)
	}
	s.pending = keep
}

// finalize records one judgement. Caller holds mu.
func (s *Scorer) finalize(r NoteResult) {
	s.results = append(s.results, r)
	switch r.Verdict {
	case VerdictHit:
		s.stats.Hit++
	case VerdictClose:
		s.stats.Close++
	case VerdictMiss:
		s.stats.Miss++
	}
}

// Results appends finalized results since the last call to dst and
// returns it.
func (s *Scorer) Results(dst []NoteResult) []NoteResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst = append(dst, s.results...)
	s.results = s.results[:0]
	return dst
}

// Stats returns the running totals since the last Reset.
func (s *Scorer) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Reset clears stats and pending state (loop restart, seek).
func (s *Scorer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = s.pending[:0]
	s.results = s.results[:0]
	s.stats = Stats{}
}

// A WaitGate drives engine wait mode from detections: when the engine is
// waiting on a note or chord, the gate watches the detection stream and
// confirms once every expected key has been played (any octave-exact
// match within the cents tolerance).
//
// Wiring (cmd, Phase 2): the caller polls Engine.WaitingOn; when a wait
// point appears it Arms the gate with the returned events, Offers each
// batch of tracker detections (Tracker.Current works too — the gate only
// reads Key and Cents), and calls Engine.ConfirmWait when Offer reports
// true; it re-Arms at the next wait point. Timing is irrelevant while
// waiting — the position is frozen — so the gate ignores frame stamps.
type WaitGate struct {
	mu         sync.Mutex
	closeCents float64
	events     []score.NoteEvent
	done       []bool
	nDone      int
}

// NewWaitGate builds a gate sharing the scorer's tolerances.
func NewWaitGate(cfg Config) *WaitGate {
	return &WaitGate{closeCents: cfg.withDefaults().CloseCents}
}

// Arm sets the events the engine is waiting on (from Engine.WaitingOn).
// The events are copied; any previous armed set and its progress are
// discarded.
func (g *WaitGate) Arm(events []score.NoteEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events[:0], events...)
	g.done = append(g.done[:0], make([]bool, len(events))...)
	g.nDone = 0
}

// Offer feeds detections; it returns true when the armed set is fully
// satisfied (the caller then calls Engine.ConfirmWait and re-arms at the
// next wait point).
//
// A detection satisfies one unsatisfied armed event on an octave-EXACT
// key match with |Cents| <= CloseCents (waiting is lenient about
// intonation, strict about octave — playing the wrong octave should not
// release the wait). A chord is satisfied by playing every note, in any
// order, across any number of Offer calls. With nothing armed, Offer
// reports false.
func (g *WaitGate) Offer(notes []pitch.Note) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) == 0 {
		return false
	}
	for i := range notes {
		n := &notes[i]
		if math.Abs(n.Cents) > g.closeCents {
			continue
		}
		for j := range g.events {
			if !g.done[j] && g.events[j].Key == n.Key {
				g.done[j] = true
				g.nDone++
				break
			}
		}
	}
	return g.nDone == len(g.events)
}
