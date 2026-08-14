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
// — exact key preferred over a pitch-class/octave-off match, and the
// oldest-deadline rule applies within each tier, so a detection sitting
// in two same-key windows goes to the expectation about to expire rather
// than the nearest one (nearest-in-time let an early detection steal a
// later duplicate's expectation and starve the earlier one into a
// spurious Miss). Each pairing is final — one detection satisfies at most
// one expectation and vice versa.
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

// Detected feeds closed notes from the pitch tracker.
//
// Each note's Start (input frames) is mapped to the output clock by
// subtracting LatencyOffsetFrames, then matched greedily against the
// pending expectations: among expectations whose TimingWindowFrames
// window contains the detection, the earliest outFrame (oldest deadline)
// wins, exact key preferred over a pitch-class-only (octave-off) match.
// A pairing finalizes the expectation immediately — exact key within
// CentsTolerance is a Hit, exact key with looser intonation or an
// octave-off pitch-class match is a Close. Detections that match nothing
// are dropped (stray noise, or the player noodling — never penalized,
// per D5).
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
		bestExact, bestClass := -1, -1
		var bestExactOut, bestClassOut int64
		for j := range s.pending {
			exp := &s.pending[j]
			dt := detOut - exp.outFrame
			if dt < -win || dt > win {
				continue
			}
			switch {
			case exp.ev.Key == n.Key:
				if bestExact < 0 || exp.outFrame < bestExactOut {
					bestExact, bestExactOut = j, exp.outFrame
				}
			case ((exp.ev.Key-n.Key)%12+12)%12 == 0:
				if bestClass < 0 || exp.outFrame < bestClassOut {
					bestClass, bestClassOut = j, exp.outFrame
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
// them finalize from this one strum. The largest in-window chord group
// wins, nearest in time on a tie. Below the correlation gate nothing is
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

	// The chord group: the largest set of candidates sharing a Start tick
	// and outFrame, nearest in time on a tie.
	group, groupN := -1, 0
	for _, j := range cand {
		n := 0
		for _, k := range cand {
			if s.pending[k].ev.Start == s.pending[j].ev.Start && s.pending[k].outFrame == s.pending[j].outFrame {
				n++
			}
		}
		if n < 2 {
			continue
		}
		if group < 0 || n > groupN ||
			(n == groupN && absInt64(stOut-s.pending[j].outFrame) < absInt64(stOut-s.pending[group].outFrame)) {
			group, groupN = j, n
		}
	}
	if group >= 0 {
		s.verifyChord(st, stOut, s.pending[group].ev.Start, s.pending[group].outFrame, stats)
	}
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
		if best < 0 {
			continue
		}
		v := VerdictClose
		if bestExact && bestAbs <= s.cfg.CentsTolerance {
			v = VerdictHit
		}
		pm := preMatch{
			track:    ev.Track,
			key:      ev.Key,
			str:      ev.String,
			start:    ev.Start,
			verdict:  v,
			errCents: notes[best].Cents,
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
// works too — the gate only reads Key, Cents, and Start), and calls
// Engine.ConfirmWait when Offer reports true; it re-Arms at the next wait
// point. Rhythm is not judged while waiting — the position is frozen —
// but the ATTACK must be new: Offer ignores notes whose Start precedes
// the frame the gate was armed at, so a note still ringing from before
// the wait cannot auto-confirm a same-key wait point. Intonation stays
// lenient (CloseCents).
type WaitGate struct {
	mu         sync.Mutex
	closeCents float64
	events     []score.NoteEvent
	done       []bool
	nDone      int
	minStart   int64
}

// NewWaitGate builds a gate sharing the scorer's tolerances.
func NewWaitGate(cfg Config) *WaitGate {
	return &WaitGate{closeCents: cfg.withDefaults().CloseCents}
}

// Arm sets the events the engine is waiting on (from Engine.WaitingOn)
// and the earliest input-stream frame a confirming attack may start at —
// typically the capture position when the wait engaged. Offer ignores
// notes whose Start precedes minStart. The events are copied; any
// previous armed set and its progress are discarded.
func (g *WaitGate) Arm(events []score.NoteEvent, minStart int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events[:0], events...)
	g.done = append(g.done[:0], make([]bool, len(events))...)
	g.nDone = 0
	g.minStart = minStart
}

// Offer feeds detections; it returns true when the armed set is fully
// satisfied (the caller then calls Engine.ConfirmWait and re-arms at the
// next wait point).
//
// A detection satisfies one unsatisfied armed event on an octave-EXACT
// key match with |Cents| <= CloseCents when its attack is fresh (Start at
// or after the armed minStart) — waiting is lenient about intonation,
// strict about octave and about the attack: playing the wrong octave
// should not release the wait, and neither should a note that was already
// ringing when the wait engaged. A chord is satisfied by playing every
// note, in any order, across any number of Offer calls. With nothing
// armed, Offer reports false.
func (g *WaitGate) Offer(notes []pitch.Note) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) == 0 {
		return false
	}
	for i := range notes {
		n := &notes[i]
		if n.Start < g.minStart {
			continue
		}
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
