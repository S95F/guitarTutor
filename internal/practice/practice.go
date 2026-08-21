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
// Matching is greedy: each detection pairs with the oldest unmatched
// expectation (earliest scheduled frame) whose timing window contains it
// — a LIVE expectation preferred over one a discontinuity abandoned, then
// exact key over a pitch-class/octave-off match, and the oldest-deadline
// rule applies within each tier, so a detection sitting in two same-key
// windows goes to the expectation about to expire rather than the nearest
// one (nearest-in-time let an early detection steal a later duplicate's
// expectation and starve the earlier one into a spurious Miss). Each
// pairing is final — one detection satisfies at most one expectation and
// vice versa.
//
// Legato is the exception to that last rule, and has to be. A note the
// score writes with score.TechSlide is not attacked; it is a string
// already sounding, moved. The tracker reports that as ONE note keeping
// its origin key, so the destination is unmatchable by the ordinary path
// no matter how well it is played — a guaranteed false "you missed" on
// every slide in a piece. One detection therefore answers both the note it
// was picked for and any slide destination its pitch trajectory reached
// while it sounded. See matchSlides.
//
// Chords (Phase 4, D4). The pitch engine is monophonic, so a strummed
// chord can never produce one detection per chord note; through Phase 3
// that cost a chord one match and N-1 Misses on perfect audio. Chords are
// therefore scored from a different signal: pitch.Strum, an attack plus
// the octave-folded chroma of the audio that followed it. Because the app
// already knows which notes are expected, verification only has to ask
// whether each expected pitch class is present in that chroma —
// expected-note verification by chroma-template correlation (Oudre et
// al.), never blind polyphonic transcription. See DetectedStrum.
//
// Two clocks, two arrival times. A Strum reaches DetectedStrum at ONSET
// time; a monophonic Note reaches Detected only when the tracker CLOSES
// it, which for a ringing note is seconds later. Chord notes are
// therefore judged early, off the strum, and the later Detected call
// finds nothing pending and drops its detection. Single notes are left
// alone by the strum path precisely because the late monophonic
// detection scores them better — it carries cents, which chroma cannot.
//
// Damped notes (D5). A palm mute has an unmistakable attack and an
// unusable fundamental: the tracker never opens a note, so the old
// deadline path called it a Miss. An expectation that reaches its
// deadline unmatched but with a Strum recorded in its window, and with
// chroma energy at its own pitch class, now scores Close instead —
// erring toward "we heard you" is the direction D5 asks for.
//
// One evidence ladder, inside and outside a chord. Chroma presence is
// graded, never binary: a class that DOMINATES the strum is a Hit, one
// merely standing above the strum's own background is a Close, and only a
// class down in that background is a Miss. verifyChord and deadlineResult
// read the identical ladder, so the same audio means the same thing
// whether the note happened to belong to a chord group or not. The
// alternative — chord verification with only Hit and Miss — was both
// harsher than the fallback path it was meant to improve on and, because
// chroma weakens sharply for voicings wider than an E shape and for
// strings struck late in a sweep, a reliable source of the false "you
// missed" D5 names as the worst failure mode in this package.
//
// Background, not peak. Every threshold that decides "present" is measured
// from the chroma's own background level (its lower-quartile bin) up to
// its peak, because a strummed chord's octave fold is never quiet: on the
// measured corpus the QUIETEST bin of a six-string strum still sits at
// 0.19–0.28 of the peak, so any bare fraction-of-peak floor low enough to
// respect a real palm mute admits all twelve classes and decides nothing.
// See chromaStats.
package practice

import (
	"math"
	"sync"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
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
	// ChordPresenceRatio is the fraction of a Strum's LARGEST chroma bin
	// at or above which an expected pitch class counts as sounding
	// during chord verification. A fraction rather than an absolute
	// level because Chroma is normalized per strum, so only ratios are
	// comparable across strums. 0 takes the default (0.45).
	//
	// Clearing it earns a Hit only together with the discrimination test
	// (see verifyChord): the class must also be at least as strong as the
	// strongest pitch class the chord does NOT contain. Below it the class
	// is not condemned, only demoted to the MuteEnergyRatio test.
	ChordPresenceRatio float64
	// ChordCorrelationMin is the sanity gate on chord verification: the
	// Pearson correlation between the Strum's chroma and the expected
	// chord's binary pitch-class template must reach it before any
	// per-note verdict is drawn from that strum. It rejects the two ways
	// per-bin thresholding lies — a featureless chroma (noise, in which
	// every bin is "present") and a chord that is simply not the one
	// expected. Below it the strum still counts as an ONSET, so the
	// damped-note path can still credit it. 0 takes the default (0.30);
	// a perfect match scores 1, flat noise 0, a foreign chord negative.
	ChordCorrelationMin float64
	// MuteEnergyRatio is the weakest evidence that still scores Close
	// rather than Miss: the lowest chroma PROMINENCE — the class's
	// position on the scale running from the chroma's own background
	// (0) to its largest bin (1), see chromaStats — at which an expected
	// pitch class counts as heard at all. 0 takes the default (0.10).
	//
	// It is deliberately NOT a fraction of the peak. Chroma folds every
	// partial of every sounding string into twelve bins, so a full strum's
	// background floor alone reaches 0.19–0.28 of the peak on the measured
	// corpus: a peak fraction low enough not to deny a genuine palm mute
	// is cleared by every class including the ones nobody played, which is
	// how the correlation gate's rejection used to buy nothing (every note
	// of a rejected chord took a Close at its deadline anyway). Measuring
	// from the background instead makes the same leniency informative.
	//
	// Both the deadline path and chord verification use it, so weak
	// evidence means Close in both.
	MuteEnergyRatio float64
}

// Default tolerance values for zero Config fields.
const (
	defaultSampleRate     = 48000
	defaultWindowMillis   = 150
	defaultCentsTolerance = 35
	defaultCloseCents     = 70
	// defaultChordPresence is deliberately well below 1: chroma folds
	// every partial of every string into 12 bins, so a chord tone that is
	// not the loudest one still sits some way under the peak. Measured on
	// the synthetic round-trip fixture's E5 chord (roundtrip_test.go, real
	// detector), the two expected classes land at E 1.00 and B 0.68 while
	// the loudest unexpected bin reaches 0.36 — 0.45 sits in that gap with
	// room on both sides.
	defaultChordPresence = 0.45
	// defaultChordCorrMin is loose on purpose. The same fixture chord
	// correlates 0.93 with its template, so the gate is nowhere near
	// binding on a chord that is actually right; it exists to catch the
	// featureless-chroma and wrong-chord cases, which score 0 and below.
	defaultChordCorrMin = 0.30
	// defaultMuteEnergy is a PROMINENCE, not a peak fraction (see
	// Config.MuteEnergyRatio): a tenth of the way from the chroma's
	// background bin to its loudest one. Measured on the voicing corpus in
	// voicing_test.go, that admits every correctly played string bar one
	// while cutting the classes it admits overall from 10/12 to 7/12 —
	// the old 0.20-of-peak floor admitted 11/12 or 12/12 on a full strum,
	// which is no test at all.
	defaultMuteEnergy = 0.10
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
	if cfg.ChordPresenceRatio <= 0 {
		cfg.ChordPresenceRatio = defaultChordPresence
	}
	if cfg.ChordCorrelationMin <= 0 {
		cfg.ChordCorrelationMin = defaultChordCorrMin
	}
	if cfg.MuteEnergyRatio <= 0 {
		cfg.MuteEnergyRatio = defaultMuteEnergy
	}
	return cfg
}

// An expectation is a tapped note awaiting a detection or its deadline.
//
// abandoned marks one whose answering window was cut short by a seek or a
// loop change rather than by the player (see AbandonBefore): it can still
// be matched, but its deadline drops it instead of scoring a Miss.
//
// onset records that a Strum landed inside this expectation's timing
// window without finalizing it (DetectedStrum). onsetProm is the best
// chroma PROMINENCE seen at the expectation's OWN pitch class across those
// strums (chromaStats.prominence — background 0, peak 1), and onsetFrames
// the timing error of the strum that supplied it. Together they are the
// damped-note evidence Advance uses: an attack in the window plus energy
// standing above the strum's background at the right pitch class is a palm
// mute, not a miss (D5).
type expectation struct {
	ev          score.NoteEvent
	outFrame    int64
	abandoned   bool
	onset       bool
	onsetProm   float64
	onsetFrames int64
}

// A preMatch records a wait-confirming detection observed before its
// expectation existed (see WaitConfirmed), keyed by the expected note's
// Track+Key+String+Start tick. String is part of the key because a guitar
// unison — two strings sounding the same pitch at the same tick — is two
// expected notes that differ ONLY by string; keyed without it the second
// overwrote the first, one entry served two events, and the uncredited
// event aged into a false Miss (audit D3).
type preMatch struct {
	track    int
	key      int
	str      int   // guitar string of the expected note
	start    int64 // score tick of the expected note
	verdict  Verdict
	errCents float64
	born     int64 // output-clock frame the confirmation was recorded at
	expire   int64 // output-clock frame after which the entry is stale
}

// preMatchExpirySeconds bounds how long a recorded wait confirmation stays
// valid awaiting its released expectation: a seek can abandon the confirm,
// and the stale entry must not fire on a later pass over the same tick.
const preMatchExpirySeconds = 5

// A Scorer matches detections against expectations and emits results.
//
// Threading: ExpectNote is called from the engine's render goroutine (via
// the event tap — keep it cheap), Detected, Advance, and WaitConfirmed
// from the analysis/wiring goroutines, and Results/Stats from the UI
// goroutine. The scorer synchronizes internally with short critical
// sections.
type Scorer struct {
	mu       sync.Mutex
	cfg      Config
	pending  []expectation
	results  []NoteResult
	preMatch []preMatch // wait confirmations awaiting their released expectations
	clock    int64      // latest output-clock frame seen from any feed (approximate; see WaitConfirmed)
	stats    Stats
	// strumCand is DetectedStrum's scratch list of in-window pending
	// indices, kept to kill its per-strum allocation.
	strumCand []int
}

// NewScorer builds a scorer. Zero Config fields take the documented
// defaults.
func NewScorer(cfg Config) *Scorer {
	return &Scorer{
		cfg: cfg.withDefaults(),
		// Preallocated so ExpectNote (render goroutine) does not
		// allocate in steady state; growth beyond this is rare and
		// amortized.
		pending:   make([]expectation, 0, 256),
		results:   make([]NoteResult, 0, 256),
		preMatch:  make([]preMatch, 0, 8),
		strumCand: make([]int, 0, 16),
	}
}

// ExpectNote feeds one scheduled note from the engine's event tap. Notes
// from tracks other than Config.Track are ignored.
//
// An event pre-matched by WaitConfirmed finalizes immediately with the
// recorded pitch-only verdict instead of joining the pending set; the
// pre-match is consumed, so a repeat of the same tick (loop pass) is
// judged normally.
func (s *Scorer) ExpectNote(ev score.NoteEvent, outFrame int64) {
	if ev.Track != s.cfg.Track {
		return
	}
	s.mu.Lock()
	if outFrame > s.clock {
		s.clock = outFrame
	}
	for i := range s.preMatch {
		p := &s.preMatch[i]
		if p.track == ev.Track && p.key == ev.Key && p.str == ev.String && p.start == ev.Start && s.clock <= p.expire {
			s.finalize(NoteResult{
				Event:    ev,
				OutFrame: outFrame,
				Verdict:  p.verdict,
				Matched:  true,
				ErrCents: p.errCents,
			})
			last := len(s.preMatch) - 1
			s.preMatch[i] = s.preMatch[last]
			s.preMatch = s.preMatch[:last]
			s.mu.Unlock()
			return
		}
	}
	s.pending = append(s.pending, expectation{ev: ev, outFrame: outFrame})
	s.mu.Unlock()
}

// Match tiers, best (lowest) first. They are ORed together, so live-ness
// outranks exactness: an abandoned expectation may still be matched, but
// never at the cost of a live one (see Detected).
const (
	tierClass     = 1 // an octave-off pitch-class match, not the exact key
	tierAbandoned = 2 // an expectation a discontinuity already wrote off
)

// Detected feeds closed notes from the pitch tracker.
//
// Each note's Start (input frames) is mapped to the output clock by
// subtracting LatencyOffsetFrames, then matched greedily against the
// pending expectations: among expectations whose TimingWindowFrames
// window contains the detection, the best tier wins and the earliest
// outFrame (oldest deadline) breaks ties within it. A pairing finalizes
// the expectation immediately — exact key within CentsTolerance is a Hit,
// exact key with looser intonation or an octave-off pitch-class match is a
// Close. Detections that match nothing are dropped (stray noise, or the
// player noodling — never penalized, per D5).
//
// LIVE BEFORE EXACT. A live expectation outranks an ABANDONED one
// (AbandonBefore) even when only the abandoned one matches the exact key,
// because the two are not symmetric: an abandoned expectation produces no
// verdict either way, so letting it win the detection costs the live one
// its only evidence and ages it into a Miss. A seek would then turn a
// silent drop plus a Hit into a Hit plus a false Miss — the outcome
// AbandonBefore exists to prevent. Abandoned expectations stay matchable,
// last, so a detection nothing live wants still credits the note the
// player did answer.
//
// SLIDES are matched separately, after the whole batch: see matchSlides.
func (s *Scorer) Detected(notes []pitch.Note) {
	s.mu.Lock()
	defer s.mu.Unlock()
	win := int64(s.cfg.TimingWindowFrames)
	off := int64(s.cfg.LatencyOffsetFrames)
	for i := range notes {
		n := &notes[i]
		detOut := n.Start - off
		if detOut > s.clock {
			s.clock = detOut
		}
		pick, pickTier := -1, 0
		var pickOut int64
		for j := range s.pending {
			exp := &s.pending[j]
			dt := detOut - exp.outFrame
			if dt < -win || dt > win {
				continue
			}
			tier := 0
			switch {
			case exp.ev.Key == n.Key:
			case ((exp.ev.Key-n.Key)%12+12)%12 == 0:
				tier |= tierClass
			default:
				continue
			}
			if exp.abandoned {
				tier |= tierAbandoned
			}
			if pick < 0 || tier < pickTier || (tier == pickTier && exp.outFrame < pickOut) {
				pick, pickTier, pickOut = j, tier, exp.outFrame
			}
		}
		if pick < 0 {
			continue
		}
		exp := s.pending[pick]
		s.pending = append(s.pending[:pick], s.pending[pick+1:]...)
		v := VerdictClose
		if pickTier&tierClass == 0 && math.Abs(n.Cents) <= s.cfg.CentsTolerance {
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
	// After the whole batch, so the ordinary path — the stronger evidence,
	// and the only one that carries honest timing — always gets first
	// refusal on every detection in it.
	for i := range notes {
		s.matchSlides(&notes[i], off, win)
	}
}

// matchSlides credits the slide DESTINATIONS one detection glided into.
// Caller holds mu.
//
// A note written with score.TechSlide is slid INTO: the player does not
// attack it, they move an already-sounding string onto it. The tracker
// reports that string as one continuous note keeping its ORIGIN key (see
// pitch.Tracker), so the destination — which the score writes at the
// destination pitch, on its own tick — can never be matched by the
// ordinary path, however perfectly it is played. Every slide in a piece
// was therefore a guaranteed false "you missed", the failure mode ROADMAP's
// guiding principles name as the #1 rage-quit cause.
//
// The evidence a slide destination is entitled to is a detection that (a)
// was SOUNDING across its window — started at or before the destination's
// late edge and had not closed before its early edge — and (b) whose pitch
// trajectory reached the destination key. Two rungs, mirroring the chord
// ladder in verifyChord:
//
//   - HIT — the note SETTLED there: its final hops (pitch.Note.EndCents)
//     sit within CentsTolerance of the destination key. That is a genuine
//     intonation reading and is reported as ErrCents.
//
//   - CLOSE — the note PASSED THROUGH: the destination lies inside the
//     trajectory's swept range, CloseCents either side. This is what
//     credits every step of a chained slide, and a slide the player left
//     early to move on to the next note. There is no meaningful cents
//     figure for a pitch the note only travelled over, so ErrCents is 0.
//
// ErrFrames is 0 rather than measured. The detection's Start is the ORIGIN
// note's attack, which is a whole beat away from the destination by
// construction; reporting that as the player's timing error would be
// reporting the notation, not the performance. The same reasoning as
// WaitConfirmed's.
//
// This cannot weaken ordinary matching: it runs only on expectations
// carrying TechSlide, only after the ordinary path has had every detection
// in the batch, and it can only turn a Miss into a verdict.
func (s *Scorer) matchSlides(n *pitch.Note, off, win int64) {
	startOut := n.Start - off
	// End is 0 on a note that is still sounding (pitch.Tracker.Current),
	// which has by definition not stopped before anything.
	endOut := int64(math.MaxInt64)
	if n.End > 0 {
		endOut = n.End - off
	}
	keep := s.pending[:0]
	for _, exp := range s.pending {
		v, cents, ok := s.slideVerdict(n, exp, startOut, endOut, win)
		if !ok {
			keep = append(keep, exp)
			continue
		}
		s.finalize(NoteResult{
			Event:    exp.ev,
			OutFrame: exp.outFrame,
			Verdict:  v,
			Matched:  true,
			ErrCents: cents,
		})
	}
	s.pending = keep
}

// slideVerdict judges one expectation against one detection on the legato
// ladder described in matchSlides, reporting ok false when the expectation
// is not a slide destination this detection can speak for. Caller holds mu.
func (s *Scorer) slideVerdict(n *pitch.Note, exp expectation, startOut, endOut, win int64) (Verdict, float64, bool) {
	if exp.ev.Tech&score.TechSlide == 0 {
		return 0, 0, false
	}
	// See reachedKey: equal keys mean the detection never moved, so it is
	// a sustained note and not a slide into anything. Without this a note
	// left ringing across the bar credits every later slide expectation at
	// its own pitch — one played note scoring as two.
	if exp.ev.Key == n.Key {
		return 0, 0, false
	}
	if startOut > exp.outFrame+win || endOut < exp.outFrame-win {
		return 0, 0, false
	}
	// Where the destination key sits on the detection's own cents scale.
	want := float64(exp.ev.Key-n.Key) * 100
	if err := n.EndCents - want; math.Abs(err) <= s.cfg.CentsTolerance {
		return VerdictHit, err, true
	}
	// CentsTolerance, not CloseCents. CloseCents is 70 — most of a
	// semitone — and added to the width of ordinary vibrato it reaches a
	// whole fret, so a note merely left ringing "passed through" every
	// slide destination a fret away and collected credit for each of
	// them. The player was given four hits for one sustained note.
	if want >= n.MinCents-s.cfg.CentsTolerance && want <= n.MaxCents+s.cfg.CentsTolerance {
		return VerdictClose, 0, true
	}
	return 0, 0, false
}

// DetectedStrum feeds one attack-plus-chroma observation from the pitch
// detector: the Phase 4 chord-verification path (D4).
//
// The strum's onset frame is mapped to the output clock the same way
// Detected maps a note's Start — minus LatencyOffsetFrames — and the
// pending expectations whose TimingWindowFrames window contains it are
// the ones it may speak for. With none, the strum is dropped: the player
// is noodling between phrases and D5 never penalizes that.
//
// CHORDS. Two or more in-window expectations sharing one Start tick (so
// one outFrame) are a chord, and this strum is the only evidence that can
// judge all of them — the monophonic tracker cannot report N simultaneous
// notes by construction. The strum's chroma is correlated against the
// chord's binary pitch-class template; if that clears
// ChordCorrelationMin, each expected note is verified INDEPENDENTLY on the
// three-rung ladder in verifyChord — dominant is a Hit, present is a
// Close, absent from the chroma's own background is a Miss — and all of
// them finalize from this one strum. The chord the strum lands NEAREST in
// time wins, size breaking ties between chords it is equally near (see
// chordGroup). Below the correlation gate nothing is
// finalized: the chroma is either featureless or belongs to some other
// chord, and the expectations are left to their deadlines (which see the
// recorded onset and grade it on the same ladder).
//
// A chord Hit means "this pitch class dominated the strum", and a chord
// Close "it was audible in the strum" — neither means "in tune to
// CentsTolerance". Chroma is octave-folded energy and carries no cents at
// all, so a chord-verified result reports ErrCents 0 with Matched true,
// and ErrFrames measured from the onset. Do not read a 0 there as perfect
// intonation.
//
// SINGLE NOTES are deliberately not finalized here. The monophonic path
// judges them strictly better — it has cents — and it is worth waiting
// for even though the tracker delivers it much later, when the note
// CLOSES. The strum only records that an onset happened inside the
// expectation's window, along with the chroma energy at that
// expectation's own pitch class; Advance turns that into damped-note
// credit if no detection ever arrives.
//
// Ordering. Because a strum lands at onset time and a note lands at close
// time, a chord's expectations are typically gone by the time Detected
// sees the tracker's belated single-f0 reading of the same strum. That
// detection then matches nothing and is dropped — the intended outcome,
// and the reason judgement cannot be doubled: finalizing removes an
// expectation from the pending set, so neither a later detection nor a
// later strum can reach it again.
func (s *Scorer) DetectedStrum(st pitch.Strum) {
	s.mu.Lock()
	defer s.mu.Unlock()
	win := int64(s.cfg.TimingWindowFrames)
	off := int64(s.cfg.LatencyOffsetFrames)
	stOut := st.Frame - off
	if stOut > s.clock {
		s.clock = stOut
	}

	cand := s.strumCand[:0]
	for j := range s.pending {
		if dt := stOut - s.pending[j].outFrame; dt >= -win && dt <= win {
			cand = append(cand, j)
		}
	}
	s.strumCand = cand
	if len(cand) == 0 {
		return
	}

	// Record the onset on every candidate BEFORE any finalizing, so the
	// indices in cand stay valid; whatever the chord path then finalizes
	// leaves the pending set and takes its recording with it.
	stats := chromaStatsOf(st.Chroma)
	for _, j := range cand {
		exp := &s.pending[j]
		p := stats.prominence(float64(st.Chroma[pitch.ChromaOf(exp.ev.Key)]))
		if !exp.onset || p > exp.onsetProm {
			exp.onsetProm = p
			exp.onsetFrames = stOut - exp.outFrame
		}
		exp.onset = true
	}

	if group := s.chordGroup(cand, stOut); group >= 0 {
		s.verifyChord(st, stOut, s.pending[group].ev.Start, s.pending[group].outFrame, stats)
	}
}

// chordProximityMillis is the slop inside which two chord ticks count as
// EQUALLY near a strum, and so the point below which chordGroup stops
// trusting proximity and falls back on group size.
//
// It is wider than the 25–50 ms the detection physics alone costs between
// an attack and a usable estimate (ROADMAP Phase 2, D4) — which is the
// precision with which a Strum's frame can be placed at all — and far
// narrower than any notated rhythm at practice tempos: the fastest figure
// this app is meant to score, 16ths at 120 BPM, spaces its attacks 125 ms
// apart. Two chord ticks closer together than this are a rolled chord, not
// two beats.
const chordProximityMillis = 50

// chordGroup picks the chord a strum at stOut speaks for, out of the
// in-window candidate indices cand. It returns an index into s.pending, or
// -1 when no candidate shares its tick with another (a lone note is not a
// chord — DetectedStrum leaves those to the monophonic path). Caller holds
// mu.
//
// TEMPORAL PROXIMITY IS THE PRIMARY CRITERION, and that is the whole point
// of this function. Choosing the largest in-window group instead let one
// strum finalize a DIFFERENT tick's chord: 16th-note chord stabs at 120 BPM
// sit 6000 frames apart, well inside the 7200-frame window, so a five-note
// chord on the beat the player actually strummed lost to a six-note chord
// an entire 16th later. That deleted expectations the player had not
// reached yet and judged them against audio that was not theirs, while the
// chord they DID play went unmatched into a Miss — the false "you missed"
// this package exists to prevent (D5).
//
// Size still decides between groups the strum is EQUALLY near, because pure
// proximity is the mirror bug: a strum landing between two chords would
// hand itself to whichever is nearer by a couple of milliseconds, a
// difference that is inside the onset detector's own placement error and
// therefore says nothing about what the player meant. "Equally near" is a
// slop of chordProximityMillis around the nearest group.
//
// Two passes rather than one running best-so-far comparison: "nearer,
// unless comparably near, in which case larger" is not a transitive order,
// so a single scan over three or more groups would depend on the order the
// candidates happen to arrive in.
func (s *Scorer) chordGroup(cand []int, stOut int64) int {
	nearest := int64(-1)
	for _, j := range cand {
		if s.chordSize(cand, j) < 2 {
			continue
		}
		if d := absInt64(stOut - s.pending[j].outFrame); nearest < 0 || d < nearest {
			nearest = d
		}
	}
	if nearest < 0 {
		return -1
	}
	slop := int64(s.cfg.SampleRate) * chordProximityMillis / 1000
	group, groupN, groupD := -1, 0, int64(0)
	for _, j := range cand {
		n := s.chordSize(cand, j)
		d := absInt64(stOut - s.pending[j].outFrame)
		if n < 2 || d > nearest+slop {
			continue
		}
		if group < 0 || n > groupN || (n == groupN && d < groupD) {
			group, groupN, groupD = j, n, d
		}
	}
	return group
}

// chordSize counts the candidates sharing cand entry j's Start tick and
// outFrame — the size of the chord j belongs to, itself included. Caller
// holds mu.
func (s *Scorer) chordSize(cand []int, j int) int {
	n := 0
	for _, k := range cand {
		if s.pending[k].ev.Start == s.pending[j].ev.Start && s.pending[k].outFrame == s.pending[j].outFrame {
			n++
		}
	}
	return n
}

// verifyChord judges every pending expectation at (start, outFrame) from
// one strum, or leaves them all pending if the strum is not credibly that
// chord. Caller holds mu.
//
// Three outcomes per note, the same ladder deadlineResult uses:
//
//   - HIT — the class DOMINATES the strum: at or above ChordPresenceRatio
//     of the peak AND at least as strong as the strongest class the chord
//     does not contain (the rival). The rival test is what stops a
//     genuinely different chord from verifying as a full set of Hits.
//     Correlation alone cannot: an Am strummed where an A was expected
//     correlates 0.73 with the A template — twice the gate — because two
//     of its three classes really are sounding, and the per-bin ratio then
//     passes the third on harmonic mush. Measured, that wrong chord earned
//     a clean 5-of-5 Hits, and an Em played against an expected open G
//     earned 6 of 6. Requiring each expected class to out-rank every
//     unexpected one puts the played C (0.76) above the expected C# (0.53)
//     and breaks both sweeps (voicing_test.go).
//
//   - CLOSE — present but not dominant: prominence at or above
//     MuteEnergyRatio. This is the outcome the path used to lack, and its
//     absence made chord verification HARSHER than no verification at all.
//     Measured on a correctly played open G: verifyChord scored the two B
//     strings a hard Miss (0.387, under the 0.45 ratio) while the very
//     same strum, offered to a lone expectation, scored them Close through
//     deadlineResult. Chroma thins out fast for voicings wider than an E
//     shape and for strings struck late in a downstroke, so "weak" is the
//     normal state of a correctly played string, not evidence of failure
//     (D5).
//
//   - MISS — the class is down in the strum's own background. That string
//     really did not speak.
//
// The rival test only ever demotes a Hit to the Close/Miss ladder; it can
// never turn evidence into a Miss on its own.
func (s *Scorer) verifyChord(st pitch.Strum, stOut, start, outFrame int64, stats chromaStats) {
	if stats.peak <= 0 {
		return // no energy to fold: the strum says nothing
	}
	var tmpl [pitch.PitchClasses]bool
	for j := range s.pending {
		if s.pending[j].ev.Start == start && s.pending[j].outFrame == outFrame {
			tmpl[pitch.ChromaOf(s.pending[j].ev.Key)] = true
		}
	}
	if chordCorrelation(st.Chroma, tmpl) < s.cfg.ChordCorrelationMin {
		return
	}
	hitThresh := s.cfg.ChordPresenceRatio * stats.peak
	rival := rivalBin(st.Chroma, tmpl)
	keep := s.pending[:0]
	for _, exp := range s.pending {
		if exp.ev.Start != start || exp.outFrame != outFrame {
			keep = append(keep, exp)
			continue
		}
		v := float64(st.Chroma[pitch.ChromaOf(exp.ev.Key)])
		r := NoteResult{Event: exp.ev, OutFrame: exp.outFrame, Verdict: VerdictMiss}
		switch {
		case v >= hitThresh && v >= rival:
			r.Verdict = VerdictHit
		case stats.prominence(v) >= s.cfg.MuteEnergyRatio:
			r.Verdict = VerdictClose
		}
		if r.Verdict != VerdictMiss {
			// ErrCents stays 0: chroma is octave-folded energy and
			// knows nothing about intonation (see DetectedStrum).
			r.Matched = true
			r.ErrFrames = stOut - exp.outFrame
		}
		s.finalize(r)
	}
	s.pending = keep
}

// chromaStats summarizes one strum's chroma for every presence decision in
// this file: its largest bin and its BACKGROUND level.
//
// The background is the lower-quartile bin (the fourth smallest of the
// twelve). It exists because a fraction of the peak is the wrong yardstick
// for "was this class sounding". Chroma folds every partial of every
// sounding string into twelve bins, so a strummed chord lights all twelve:
// across the measured voicing corpus (voicing_test.go) the quietest bin of
// a six-string strum sits at 0.19–0.28 of the peak, and a 0.20-of-peak
// floor therefore called all twelve classes present. Prominence measures a
// class on the scale from that background up to the peak instead, so a
// threshold on it means the same thing on a sparse two-note chroma and on
// a dense six-string one.
//
// A quartile rather than the median because the median of a chord's chroma
// sits well INSIDE the chord's own energy — on the corpus, correctly
// played strings routinely land just below it (an open G's B string at
// 0.39 against a median of 0.35), and a median-relative floor would score
// them hard Misses. The quartile tracks the noise floor, which is what the
// Miss verdict is supposed to be about.
type chromaStats struct {
	peak float64
	bg   float64
}

// chromaStatsOf measures a chroma. Chroma is documented normalized to a
// peak of 1, but nothing here assumes it: an un-normalized or all-zero
// chroma degrades safely instead of declaring every pitch class present.
func chromaStatsOf(ch pitch.Chroma) chromaStats {
	var sorted [pitch.PitchClasses]float64
	for i, v := range ch {
		sorted[i] = float64(v)
	}
	// Insertion sort: twelve elements on the stack, no allocation, called
	// once per strum.
	for i := 1; i < len(sorted); i++ {
		v := sorted[i]
		j := i - 1
		for ; j >= 0 && sorted[j] > v; j-- {
			sorted[j+1] = sorted[j]
		}
		sorted[j+1] = v
	}
	return chromaStats{peak: sorted[len(sorted)-1], bg: sorted[len(sorted)/4]}
}

// prominence places one bin on the scale from the chroma's background (0)
// to its peak (1). Below the background it goes negative; a featureless
// chroma (peak == background, every bin equal) yields 0 for every bin, so
// noise credits nothing.
func (s chromaStats) prominence(v float64) float64 {
	if s.peak <= s.bg {
		return 0
	}
	return (v - s.bg) / (s.peak - s.bg)
}

// rivalBin returns the strongest bin the template does NOT claim — the bar
// an expected class must clear to be called dominant rather than merely
// present. With every class templated (degenerate) there is no rival and
// it returns 0.
func rivalBin(ch pitch.Chroma, tmpl [pitch.PitchClasses]bool) float64 {
	rival := 0.0
	for i, v := range ch {
		if !tmpl[i] && float64(v) > rival {
			rival = float64(v)
		}
	}
	return rival
}

// chordCorrelation is the Pearson correlation between a strum's chroma
// and the expected chord's binary pitch-class template — the sanity gate
// on chord verification, following the chroma-template literature (Oudre
// et al.) cited in D4.
//
// Mean-centering is what makes it useful. A raw dot product rewards loud
// chroma regardless of shape, so noise with every bin lit scores well; a
// centered correlation scores a featureless chroma 0 (no variance to
// explain), a chord matching the template 1, and a chord whose energy
// sits everywhere EXCEPT the expected classes negative. A degenerate
// template (no classes, or all twelve) also yields 0, which the caller
// reads as "not verifiable".
func chordCorrelation(ch pitch.Chroma, tmpl [pitch.PitchClasses]bool) float64 {
	var sumC, sumT float64
	for i, v := range ch {
		sumC += float64(v)
		if tmpl[i] {
			sumT++
		}
	}
	meanC := sumC / pitch.PitchClasses
	meanT := sumT / pitch.PitchClasses
	var dot, normC, normT float64
	for i, v := range ch {
		a := float64(v) - meanC
		b := -meanT
		if tmpl[i] {
			b = 1 - meanT
		}
		dot += a * b
		normC += a * a
		normT += b * b
	}
	if normC <= 0 || normT <= 0 {
		return 0
	}
	return dot / math.Sqrt(normC*normT)
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
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
	if out := inFrame - off; out > s.clock {
		s.clock = out
	}
	keepPM := s.preMatch[:0]
	for _, p := range s.preMatch {
		if s.clock <= p.expire {
			keepPM = append(keepPM, p)
		}
	}
	s.preMatch = keepPM
	keep := s.pending[:0]
	for _, exp := range s.pending {
		if exp.outFrame+off+win < inFrame {
			// An abandoned expectation leaves no trace: the
			// player never got their window, so neither a Miss
			// nor a Hit is honest (AbandonBefore).
			if !exp.abandoned {
				s.finalize(s.deadlineResult(exp))
			}
			continue
		}
		keep = append(keep, exp)
	}
	s.pending = keep
}

// deadlineResult judges an expectation nothing ever matched.
//
// Plain Miss, unless a Strum landed in its window (DetectedStrum) AND
// that strum carried at least MuteEnergyRatio of chroma PROMINENCE at this
// note's pitch class — the class standing above the strum's own background
// (chromaStats), not merely above a fraction of its peak, which a full
// strum's harmonic mush clears at every class. This is the
// signature of a palm-muted or otherwise damped note,
// which has an unmistakable attack and a fundamental too smothered for
// the tracker to ever open a note on. Calling that a Miss is the false
// negative D5 names as the #1 rage-quit cause in this category, so it
// scores Close instead: credit for the attack landing in the right place
// on the right string, not for a pitch nobody could measure. Like a chord
// Hit it carries Matched true with ErrCents 0 — the onset was matched,
// the intonation was never observed.
//
// An onset with no energy at the expected class is still a plain Miss:
// something was struck, but not this note.
//
// verifyChord's Close rung is this same test on the same constant, so a
// note inside a chord group and a note outside one draw the same verdict
// from the same evidence.
func (s *Scorer) deadlineResult(exp expectation) NoteResult {
	r := NoteResult{Event: exp.ev, OutFrame: exp.outFrame, Verdict: VerdictMiss}
	if exp.onset && exp.onsetProm >= s.cfg.MuteEnergyRatio {
		r.Verdict = VerdictClose
		r.Matched = true
		r.ErrFrames = exp.onsetFrames
	}
	return r
}

// AbandonBefore marks every expectation stamped before outFrame as
// abandoned, and discards wait pre-matches recorded before it. Wiring
// calls it with Engine.DiscontinuityFrame whenever that changes — a seek,
// or a loop edit.
//
// A seek silences what is ringing and jumps the music out from under the
// player. Anything the tap stamped before that moment was still inside
// its ±150 ms answering window, and the player lost that window to a UI
// action rather than to their playing. Advance would otherwise age those
// expectations into Misses seconds later, which is the false "you missed"
// D5 calls the #1 rage-quit cause in this category, and the planned
// recognition layer would compound into a lost combo (docs/PROGRESS.md).
//
// Abandoning rather than dropping outright is the point. A detection
// already in flight — the player did answer, and the tracker simply has
// not closed the note yet — still matches and still scores a Hit, so a
// seek costs no credit that was genuinely earned. Only the deadline path
// changes: it drops the expectation silently instead of inventing a
// failure. Erring toward no verdict is the direction D5 asks for.
//
// The frame bound is what keeps this from over-reaching. The tap keeps
// running between the seek and the analysis goroutine observing it, so
// notes sounded after the jump are already pending by the time this is
// called; comparing against the discontinuity frame leaves those live and
// fully scorable. Loop wraps never reach here — those notes were heard
// and are still being answered.
func (s *Scorer) AbandonBefore(outFrame int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pending {
		if s.pending[i].outFrame < outFrame {
			s.pending[i].abandoned = true
		}
	}
	// A pre-match is a wait confirmation awaiting its released event. A
	// discontinuity abandons the wait, so the confirm must not fire on a
	// later pass over the same tick — the case preMatchExpirySeconds
	// could only approximate before this signal existed.
	keep := s.preMatch[:0]
	for _, p := range s.preMatch {
		if p.born >= outFrame {
			keep = append(keep, p)
		}
	}
	s.preMatch = keep
}

// WaitConfirmed records the detections that confirmed a wait point; the
// wiring calls it with the wait's events (Engine.WaitingOn) and the
// confirming notes right BEFORE Engine.ConfirmWait.
//
// At a wait point the player's notes are heard before the engine fires
// the expected events — the release frame is causally downstream of the
// playing — so a staccato confirmation reaches Detected while no
// expectation exists yet (consumed, matching nothing), and even a
// sustained one would be timing-judged against dt = -(latency offset +
// confirmation latency), an error that is the machinery's, not the
// player's. WaitConfirmed fixes both: for each expected event
// (Config.Track only) the best-cents matching note is recorded as a
// pre-match keyed by Track+Key+String+Start tick, and when ExpectNote later
// receives a pre-matched event it finalizes immediately with a pitch-only
// verdict — Hit if |cents| <= CentsTolerance, else Close (an octave-off
// pitch-class match is Close) — Matched true, ErrFrames 0. Each pre-match
// is consumed at most once.
//
// A DEAD note (score.TechDead) has no matching note to find, ever: the
// tracker cannot open one on a damped string. It is pre-matched Close
// unconditionally, on the strength of the caller's contract — this is
// called only once WaitGate reports the armed set fully satisfied, and the
// gate satisfies a dead note from its attack (WaitGate.OfferStrum). Left
// out, the note would fall through to its deadline and be judged on
// whether a Strum happened to land inside a window measured from the
// release frame, which is the machinery's timing, not the player's.
//
// Pre-matches expire after ~5 s so a seek that abandons the confirm
// cannot leak a stale entry into a later pass over the same tick. Expiry
// runs on the scorer's approximate output clock: the latest frame seen
// from any feed (tap outFrames directly; detections and Advance mapped
// through LatencyOffsetFrames). Input and output clocks differ by the
// round-trip latency, which is noise at a 5 s horizon.
func (s *Scorer) WaitConfirmed(evs []score.NoteEvent, notes []pitch.Note) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range evs {
		if ev.Track != s.cfg.Track {
			continue
		}
		best, bestExact := -1, false
		var bestAbs float64
		for i := range notes {
			n := &notes[i]
			exact := n.Key == ev.Key
			if !exact && ((ev.Key-n.Key)%12+12)%12 != 0 {
				continue
			}
			abs := math.Abs(n.Cents)
			switch {
			case best < 0, exact && !bestExact:
				best, bestExact, bestAbs = i, exact, abs
			case exact == bestExact && abs < bestAbs:
				best, bestAbs = i, abs
			}
		}
		slideIdx := -1
		if ev.Tech&score.TechSlide != 0 {
			slideIdx = slideConfirmer(notes, ev.Key, s.cfg.CentsTolerance)
		}
		v, cents := VerdictClose, 0.0
		switch {
		case best >= 0:
			if bestExact && bestAbs <= s.cfg.CentsTolerance {
				v = VerdictHit
			}
			cents = notes[best].Cents
		case slideIdx >= 0:
			// A slide destination is never attacked, so the confirming
			// detection carries the ORIGIN key and the key search above
			// cannot find it — the note was played correctly and scored a
			// MISS, at a wait point the player had just satisfied. Grade
			// it off the trajectory the way matchSlides does: settling at
			// the key is a Hit, merely sweeping through it is a Close.
			n := &notes[slideIdx]
			if err := n.EndCents - float64(ev.Key-n.Key)*100; math.Abs(err) <= s.cfg.CentsTolerance {
				v, cents = VerdictHit, err
			}
		case ev.Tech&score.TechDead != 0:
			// A dead note produces no trackable f0, so no detection can
			// ever appear here for it — WaitGate.OfferStrum confirmed it
			// on its ATTACK instead. This function is only called once
			// the gate reports the whole armed set satisfied, so a dead
			// event reaching this line WAS confirmed, and recording the
			// same Close the damped-note deadline path awards keeps it
			// out of a deadline that would judge it against the
			// machinery's own latency (see this function's doc). ErrCents
			// stays 0: the attack was heard, the pitch never was.
		default:
			continue
		}
		pm := preMatch{
			track:    ev.Track,
			key:      ev.Key,
			str:      ev.String,
			start:    ev.Start,
			verdict:  v,
			errCents: cents,
			born:     s.clock,
			expire:   s.clock + int64(preMatchExpirySeconds*s.cfg.SampleRate),
		}
		replaced := false
		for i := range s.preMatch {
			p := &s.preMatch[i]
			if p.track == pm.track && p.key == pm.key && p.str == pm.str && p.start == pm.start {
				*p = pm
				replaced = true
				break
			}
		}
		if !replaced {
			s.preMatch = append(s.preMatch, pm)
		}
	}
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
	s.preMatch = s.preMatch[:0]
	s.stats = Stats{}
}

// A WaitGate drives engine wait mode from detections: when the engine is
// waiting on a note or chord, the gate watches the detection stream and
// confirms once every expected key has been played (any octave-exact
// match within the cents tolerance).
//
// Wiring (cmd, Phase 2): the caller polls Engine.WaitingOn; when a wait
// point appears it Arms the gate with the returned events and the current
// capture frame, Offers each batch of tracker detections (Tracker.Current
// works too), OfferStrums each batch of pitch.Strums, and calls
// Engine.ConfirmWait when either reports true; it re-Arms at the next wait
// point. Rhythm is not judged while waiting — the position is frozen —
// but the ATTACK must be new: Offer ignores notes whose Start precedes
// the frame the gate was armed at, so a note still ringing from before
// the wait cannot auto-confirm a same-key wait point. Intonation stays
// lenient (CloseCents).
//
// A wait point that cannot be satisfied halts playback for good, so the
// two notes the pitched path structurally cannot see each have their own
// evidence:
//
//   - DEAD notes (score.TechDead) have no f0 to track at all. They are
//     confirmed by their ATTACK, through OfferStrum.
//
//   - SLIDE destinations (score.TechSlide) are never attacked and never
//     take the tracker's key: the string is already sounding and simply
//     moves. They are confirmed by a detection whose pitch TRAJECTORY
//     reached their key (pitch.Note.MinCents/MaxCents).
//
// One attack, one wait point. An attack that satisfied an event under a
// previous arm is spent and cannot satisfy another: a still-ringing note
// keeps being re-offered from Tracker.Current, and the caller's arming
// grace is a fixed span of frames, so at fast repeated notes the note that
// released one wait point can still look fresh at the next one and release
// that too — awarding, through Scorer.WaitConfirmed, a Hit nobody played.
// Freshness alone cannot rule that out at every tempo; identity can.
type WaitGate struct {
	mu         sync.Mutex
	closeCents float64
	// slideCents is the reach tolerance for a slide destination, and is
	// deliberately the TIGHT (full-credit) bound rather than closeCents.
	// closeCents is 70 — most of a semitone — so with it a note merely
	// held with ordinary vibrato "reached" a key a fret away and released
	// a slide wait point the player never slid to.
	slideCents float64
	events     []score.NoteEvent
	done       []bool
	nDone      int
	// fired records that this arm's completion has already been reported,
	// so it is reported once and not to every later caller (see satisfied).
	fired    bool
	minStart int64
	// credited holds the attacks that have satisfied an event under the
	// CURRENT arm, spent those from previous arms. The split is what lets
	// one detection still credit two unison events of one chord (audit
	// D3) while barring it from the next wait point entirely.
	credited []attack
	spent    []attack
}

// An attack identifies one pluck by the detection it produced: the same
// pair cmd's wiring uses to recognize re-offered snapshots of a
// still-sounding note.
type attack struct {
	start int64
	key   int
}

// NewWaitGate builds a gate sharing the scorer's tolerances.
func NewWaitGate(cfg Config) *WaitGate {
	c := cfg.withDefaults()
	return &WaitGate{closeCents: c.CloseCents, slideCents: c.CentsTolerance}
}

// Arm sets the events the engine is waiting on (from Engine.WaitingOn)
// and the earliest input-stream frame a confirming attack may start at —
// typically the capture position when the wait engaged. Offer ignores
// notes whose Start precedes minStart. The events are copied; any
// previous armed set and its progress are discarded.
//
// Whatever satisfied the previous armed set becomes spent here, so it can
// never release this wait point too. Spent attacks older than minStart are
// forgotten — freshness already rejects them — which is what keeps the set
// to a handful of entries however long the session runs.
func (g *WaitGate) Arm(events []score.NoteEvent, minStart int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.spent = append(g.spent, g.credited...)
	g.credited = g.credited[:0]
	keep := g.spent[:0]
	for _, a := range g.spent {
		if a.start >= minStart {
			keep = append(keep, a)
		}
	}
	g.spent = keep
	g.events = append(g.events[:0], events...)
	g.done = append(g.done[:0], make([]bool, len(events))...)
	g.nDone = 0
	g.fired = false
	g.minStart = minStart
}

// Offer feeds detections; it returns true when the armed set is fully
// satisfied (the caller then calls Engine.ConfirmWait and re-arms at the
// next wait point).
//
// A detection satisfies one unsatisfied armed event on an octave-EXACT
// key match with |Cents| <= CloseCents when its attack is fresh (Start at
// or after the armed minStart, and not already spent on an earlier wait
// point) — waiting is lenient about intonation, strict about octave and
// about the attack: playing the wrong octave should not release the wait,
// and neither should a note that was already ringing when the wait
// engaged. A chord is satisfied by playing every note, in any order,
// across any number of Offer calls. With nothing armed, Offer reports
// false.
//
// A slide destination is satisfied instead by a detection whose pitch
// trajectory REACHED its key, attacked or not — see the type doc.
func (g *WaitGate) Offer(notes []pitch.Note) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) == 0 {
		return false
	}
	for i := range notes {
		n := &notes[i]
		// Freshness and identity both ask "is this a NEW attack" — the
		// right question for a note that has to be struck, and the wrong
		// one for a slide (see below), so they are asked per event rather
		// than used to skip the detection outright.
		fresh := n.Start >= g.minStart && !g.isSpent(n)
		inTune := math.Abs(n.Cents) <= g.closeCents
		for j := range g.events {
			if g.done[j] {
				continue
			}
			ev := &g.events[j]
			switch {
			// A slide destination, exempt from BOTH freshness and
			// identity. A legato slide sounds on a string that was
			// already ringing when the wait engaged — that is what
			// legato means — and its attack was spent releasing the
			// origin's own wait point a beat earlier. Neither test can
			// ever speak for it: at any realistic tempo the origin's
			// attack is both stale (a quarter at 120 BPM is 24000
			// frames, far past the ~7200-frame grace) and spent, so
			// playback halted at every slide in the piece, forever. The
			// trajectory is the only evidence there is, and reaching a
			// key the note did not start on is evidence of something the
			// player did during the wait.
			case ev.Tech&score.TechSlide != 0 && settledAt(n, ev.Key, g.slideCents):
			// Anything else has to be struck, now, and not by an attack
			// that already released an earlier wait point.
			case fresh && inTune && ev.Key == n.Key:
			default:
				continue
			}
			g.done[j] = true
			g.nDone++
			g.credited = append(g.credited, attack{start: n.Start, key: n.Key})
			break
		}
	}
	return g.satisfied()
}

// OfferStrum feeds one attack-plus-chroma observation (the same pitch.Strum
// Scorer.DetectedStrum runs chord verification on); it returns true when
// the armed set is fully satisfied, exactly as Offer does.
//
// A fresh strum satisfies every unsatisfied DEAD note in the armed set. A
// damped string has an unmistakable attack and a fundamental too smothered
// for the tracker to ever open a note on, so the pitched path can never
// confirm one: a wait point on a dead note used to halt playback
// permanently, with nothing the player could do about it (fixture
// testdata/fixture_rich.gtab bar 2 ends in `9.4x`). Crediting the attack is
// the mechanism the scorer already uses for damped notes at their deadline
// (deadlineResult, ROADMAP Phase 4).
//
// Deliberately WITHOUT the chroma-prominence test deadlineResult applies.
// That test asks whether a strum was THIS note rather than something else,
// a question that only arises while the music is running. At a wait point
// the position is frozen and exactly one thing is being asked for, so any
// attack during the wait is the player's attempt at it — and demanding
// pitch-class energy from a note that is by definition pitchless risks the
// one failure worse than a wrong verdict: playback that never resumes.
//
// One strum satisfies ALL the dead notes in a chord, because one strum IS
// one strike of however many muted strings, the same reasoning that lets a
// single detection credit both halves of a unison (audit D3).
func (g *WaitGate) OfferStrum(st pitch.Strum) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) == 0 {
		return false
	}
	if st.Frame >= g.minStart {
		for j := range g.events {
			if !g.done[j] && g.events[j].Tech&score.TechDead != 0 {
				g.done[j] = true
				g.nDone++
			}
		}
	}
	return g.satisfied()
}

// satisfied reports the armed set complete — ONCE per arm.
//
// The gate stays full from the moment it fills until the next Arm, so a
// caller asking again got "yes" a second time and released a wait the
// player never answered. That is how it bit: OnStrums runs before
// OnNotes, and only OnNotes arms, so every strum landing after a
// confirmation and before the next arm found a full gate and skipped the
// next wait point. Answering once per arm makes that impossible for every
// caller, instead of asking each one to remember. Caller holds mu.
func (g *WaitGate) satisfied() bool {
	if g.fired || len(g.events) == 0 || g.nDone != len(g.events) {
		return false
	}
	g.fired = true
	return true
}

// isSpent reports whether this attack already released an earlier wait
// point. Caller holds mu.
func (g *WaitGate) isSpent(n *pitch.Note) bool {
	for _, a := range g.spent {
		if a.start == n.Start && a.key == n.Key {
			return true
		}
	}
	return false
}

// settledAt reports whether a detection's pitch has ARRIVED at key and is
// still there — its final hops sit within slop of it.
//
// This is the wait gate's test, and it is deliberately stricter than
// reachedKey. Releasing a halt is not the same question as awarding
// credit: the position is frozen and the player is holding the note, so
// the destination must be what is sounding NOW. Merely having swept over
// the pitch at some earlier moment let a note bent up from a previous
// phrase, still ringing, release a wait point the player did nothing for.
func settledAt(n *pitch.Note, key int, slop float64) bool {
	if key == n.Key {
		return false
	}
	return math.Abs(n.EndCents-float64(key-n.Key)*100) <= slop
}

// reachedKey reports whether a detection's pitch trajectory passed through
// key, slop cents either side of the swept range. A slide destination is
// the case it exists for: the tracker keeps the whole slide as one note on
// the ORIGIN key, so the destination only ever shows up in the trajectory.
// The SCORER uses this — a slide the player left early to move on still
// earns partial credit; see settledAt for why the gate does not.
func reachedKey(n *pitch.Note, key int, slop float64) bool {
	// A legato slide is tracked as ONE note that keeps the ORIGIN key, so
	// a genuine slide destination is never the detection's own key. Equal
	// keys mean the note never moved — it is a sustained note sitting on
	// that pitch — and crediting it would award a slide the player did not
	// play, on the strength of a note they were already credited for.
	if key == n.Key {
		return false
	}
	want := float64(key-n.Key) * 100
	return want >= n.MinCents-slop && want <= n.MaxCents+slop
}

// slideConfirmer returns the index of the detection whose pitch trajectory
// reached key, or -1. It is how a slide destination is recognized among
// the notes that confirmed a wait point: the destination is never
// attacked, so no detection ever carries its key.
func slideConfirmer(notes []pitch.Note, key int, slop float64) int {
	for i := range notes {
		if reachedKey(&notes[i], key, slop) {
			return i
		}
	}
	return -1
}

// ConfirmsSlide reports whether detection n is the evidence for slide
// destination ev — the same test WaitGate.Offer accepts a slide on.
//
// It is exported for the live wiring, which decides which detections to
// hand WaitConfirmed. That wiring keeps only FRESH attacks, which is right
// for every note that has to be struck and exactly wrong for a slide: the
// string was struck a beat earlier, so the detection proving the slide is
// never fresh, and filtering it out left WaitConfirmed with nothing to
// grade. The slide the gate had just accepted then aged into a false Miss
// — the player slid correctly, playback resumed, and the note was marked
// missed.
func ConfirmsSlide(n pitch.Note, ev score.NoteEvent, cfg Config) bool {
	if ev.Tech&score.TechSlide == 0 {
		return false
	}
	return settledAt(&n, ev.Key, cfg.withDefaults().CentsTolerance)
}
