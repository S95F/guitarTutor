// Package pitch is musicTutor's monophonic pitch engine: an MPM (McLeod
// pitch method) detector with a YIN-FFT cross-check, an onset/energy gate,
// and a note tracker that turns per-hop f0 estimates into discrete note
// events. Pure Go on gonum's FFT; designed for a live electric-guitar DI
// signal per docs/DECISIONS.md D4.
//
// The physics budget (ROADMAP Phase 2): low E is 82.4 Hz, detectors want
// 2–4 cycles, so expect ~25–50 ms from onset to a stable estimate. Frames
// and notes are stamped in input-stream sample frames so callers can align
// them against the playback clock after latency calibration.
//
// The f0 path is monophonic and always will be: it answers "which single
// note is sounding", so a strummed chord yields one note and N-1 misses.
// Phase 4 adds the second channel of evidence that fixes chord scoring
// without attempting real-time polyphonic transcription — Config.Strums
// turns each onset into a Strum carrying the octave-folded pitch-class
// energy (Chroma) that followed it. Callers that know which notes are
// EXPECTED can check them against that chroma; nothing here tries to infer
// a chord from scratch (docs/DECISIONS.md D4).
package pitch

import "math"

// Config parameterizes the detector and tracker.
type Config struct {
	// SampleRate in Hz of the input stream.
	SampleRate int
	// Window is the analysis window in samples (power of two). Sized
	// for the lowest supported note: 2048 at 48 kHz reaches ~70 Hz;
	// use 4096 for tunings below that (drop C and lower).
	Window int
	// Hop is the analysis stride in samples.
	Hop int
	// MinHz and MaxHz bound the f0 search range.
	MinHz, MaxHz float64
	// ClarityThreshold is the minimum MPM clarity (0–1) for a frame's
	// f0 to be trusted.
	ClarityThreshold float64
	// NoiseFloorDB is the energy gate in dBFS (negative): a window whose
	// RMS is below it is unvoiced without any pitch analysis, and onsets
	// never fire below it. 0 takes the default (-55 dBFS).
	NoiseFloorDB float64
	// OnsetDipDB enables the dip-recovery onset — the wind re-articulation
	// trigger — and sets its arming depth: a hop whose RMS falls at least
	// this many dB below the smoothed level opens a re-articulation
	// candidate, and the onset fires when the level climbs back (see the
	// onsetDip* tunables in detector.go). 0 is OFF, and off is the guitar
	// default on purpose: a plucked string cannot re-articulate without a
	// pick attack the level/flux triggers already see, so the dip path
	// would only add a way for tremolo picking to double-trigger.
	//
	// The wind tuning (windOnsetDipDB via ConfigForKeys) is measured on
	// the synthesized reed voice only and is provisional until real
	// recordings exist (docs/TESTDATA.md, wind/articulation).
	OnsetDipDB float64
	// OnsetDipRecoverHops is the dip trigger's recovery window: hops after
	// arming within which the level must climb back for the dip to count
	// as a re-articulation rather than a release. 0 takes the default
	// (8 hops ~ 80 ms at the default hop) when OnsetDipDB enables the
	// path; ignored otherwise.
	OnsetDipRecoverHops int
	// Strums enables chroma accumulation and Strum emission (Phase 4
	// chord verification). Off by default: it costs a spectral fold on
	// every hop — not just the hops of a span, because the hop BEFORE an
	// onset is the baseline the span subtracts, and which hop that is
	// only becomes known once the onset fires — and, with an Estimator
	// that would otherwise skip it, the FFT each of those folds needs.
	// Callers that only track single notes never look at the result.
	Strums bool
	// StrumWindowHops is how many hops of chroma are folded into a
	// Strum, once the span's lead-in (strumSkipHops) has elapsed. 0
	// takes the default (8 hops ~ 80 ms at the default hop). Together
	// with that lead-in it puts the Strum ~160 ms after the attack:
	// long enough for every string of a downstroke to speak and for the
	// pick transient to be well behind, short enough not to swallow the
	// next beat at practice tempos. See strumSkipHops for the measured
	// trade-off — shortening this is how you would trade chord-scoring
	// accuracy back for latency.
	StrumWindowHops int
	// Estimator swaps the single-window pitch estimator. Nil selects the
	// built-in MPM implementation; internal/pitchml provides an ONNX
	// backend behind a build tag.
	Estimator F0Estimator
}

// DefaultConfig returns the guitar-tuned defaults for a sample rate.
func DefaultConfig(sampleRate int) Config {
	return Config{
		SampleRate:       sampleRate,
		Window:           2048,
		Hop:              480,
		MinHz:            60,
		MaxHz:            1500,
		ClarityThreshold: 0.80,
		NoiseFloorDB:     -55,
		StrumWindowHops:  defaultStrumWindowHops,
	}
}

// windOnsetDipDB is the dip-recovery arming depth ConfigForKeys enables.
// Measured on the synthesized reed across eight hop-grid phases (see the
// onsetDip* tunables in detector.go for the full table): the loudest
// excursion any non-rearticulation wind gesture produced is 4.1 dB (a
// slur with a 4 dB velocity drop), a 7 dB soft tongue stroke reaches
// 5.3–6.4 dB, and full tongue gaps of 15 ms and up reach 7.3 dB and
// beyond. 5 catches every stroke of 7 dB or deeper at every phase while
// keeping ~1 dB over the slur; the 6 dB stroke is the edge (5/8 phases).
// Provisional until real recordings exist (docs/TESTDATA.md,
// wind/articulation).
const windOnsetDipDB = 5

// ConfigForKeys returns the defaults fitted to a wind instrument whose
// sounding compass is lowKey..highKey (MIDI) — the per-instrument config
// the live session and the wind round trip share.
//
// The f0 search range follows the compass: the floor a whole tone under
// the lowest note, the ceiling a fourth over the highest (players
// overshoot, and altissimo exists above every published range). Fitting
// the range matters both ways: a floor far below the instrument enlarges
// the subharmonic-error surface for nothing, and the guitar default's
// 1500 Hz ceiling sits only three semitones over a soprano sax's top E6 —
// anything higher would be forced unvoiced by the alias guard.
//
// It also arms the dip-recovery onset (windOnsetDipDB): winds re-articulate
// by tonguing, a level dip-and-recover the guitar-tuned rise-only triggers
// cannot see, and without it repeated same-pitch notes merge into one
// detection and score a false Miss (docs/DECISIONS.md D5, D8).
func ConfigForKeys(sampleRate, lowKey, highKey int) Config {
	cfg := DefaultConfig(sampleRate)
	cfg.MinHz = keyHz(lowKey - 2)
	cfg.MaxHz = keyHz(highKey + 5)
	cfg.OnsetDipDB = windOnsetDipDB
	return cfg
}

// keyHz is the equal-tempered frequency of a MIDI key (A4 = 69 = 440 Hz).
func keyHz(key int) float64 {
	return 440 * math.Pow(2, float64(key-69)/12)
}

// withDefaults fills zero fields with DefaultConfig values and clamps the
// rest into a usable range. In particular Window is rounded UP (rather
// than erroring) to the minimum the MaxHz search bound needs — one lag
// past tauMin ~= SampleRate/MaxHz — so the detector's analysis buffers can
// always hold the search range.
func (cfg Config) withDefaults() Config {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 48000
	}
	def := DefaultConfig(cfg.SampleRate)
	if cfg.Window <= 0 {
		cfg.Window = def.Window
	}
	if cfg.Hop <= 0 {
		cfg.Hop = def.Hop
	}
	if cfg.Hop > cfg.Window {
		cfg.Hop = cfg.Window
	}
	if cfg.MinHz <= 0 {
		cfg.MinHz = def.MinHz
	}
	if cfg.MaxHz <= 0 {
		cfg.MaxHz = def.MaxHz
	}
	if cfg.MaxHz < cfg.MinHz {
		cfg.MaxHz = cfg.MinHz
	}
	// Minimum usable window: the shortest search lag (tauMin, with the
	// detector's >= 2 floor) plus the one extra lag the analysis computes
	// for interpolation. Anything smaller cannot represent even the top
	// of the search range.
	minTau := int(float64(cfg.SampleRate) / cfg.MaxHz)
	if minTau < 2 {
		minTau = 2
	}
	if cfg.Window < minTau+2 {
		cfg.Window = minTau + 2
	}
	if cfg.ClarityThreshold <= 0 {
		cfg.ClarityThreshold = def.ClarityThreshold
	}
	if cfg.NoiseFloorDB == 0 {
		cfg.NoiseFloorDB = def.NoiseFloorDB
	}
	// OnsetDipDB is NOT defaulted: zero means the dip path is off, which
	// is the guitar default. Its companion window only takes a value once
	// something turned the path on.
	if cfg.OnsetDipDB > 0 && cfg.OnsetDipRecoverHops <= 0 {
		cfg.OnsetDipRecoverHops = defaultOnsetDipRecoverHops
	}
	if cfg.StrumWindowHops <= 0 {
		cfg.StrumWindowHops = def.StrumWindowHops
	}
	return cfg
}

// A Frame is one analysis hop's estimate. F0 is 0 when no pitch cleared
// the clarity threshold (silence, noise, or the gate closed).
type Frame struct {
	// Frame is the input-stream sample frame of the window's center.
	Frame int64
	// F0 in Hz; 0 means unvoiced.
	F0 float64
	// Clarity is the MPM peak clarity in [0, 1].
	Clarity float64
	// RMS of the analysis window.
	RMS float64
	// Onset marks an attack transient detected at (or near) this hop.
	Onset bool
}

// PitchClasses is the number of chroma bins: one per semitone of the
// octave, C at index 0.
const PitchClasses = 12

// A Chroma is octave-folded spectral energy, one bin per pitch class
// (C, C#, D, ... B), normalized so the largest bin is 1 (all-zero when
// there was no energy to fold).
type Chroma [PitchClasses]float32

// A Strum is an attack with the harmonic content that followed it: the
// evidence chord verification runs on (ROADMAP Phase 4, docs/DECISIONS.md
// D4). The detector emits one per onset, after accumulating chroma over a
// short window, so a strummed chord yields a single Strum whose Chroma
// holds every string that sounded — something the monophonic f0 path
// cannot report by construction.
//
// A Strum is also the honest signal for a palm-muted or heavily damped
// note: the attack is unambiguous even when the fundamental never gets
// clear enough to track, so scoring can credit the onset without claiming
// to know the pitch.
type Strum struct {
	// Frame is the input-stream sample frame of the onset.
	Frame int64
	// Chroma is the normalized pitch-class energy over the window
	// following the onset.
	Chroma Chroma
	// RMS is the peak window RMS over that same span.
	RMS float64
	// Clarity is the best monophonic clarity seen in the span: high
	// means one note dominated, low means a chord, a mute, or noise.
	Clarity float64
}

// An F0Estimator is the swappable single-window pitch estimator behind
// the detector: the built-in MPM implementation by default, or an ML
// backend (internal/pitchml's SwiftF0, build-tagged so the base app ships
// without an ONNX runtime). Implementations must be allocation-free in
// steady state and safe to call from the analysis goroutine only.
type F0Estimator interface {
	// Name identifies the estimator for logs and the settings UI.
	Name() string
	// EstimateF0 returns the fundamental in Hz and a confidence in
	// [0, 1] for one analysis window. Returning 0 means unvoiced.
	EstimateF0(window []float32) (f0 float64, clarity float64)
}

// ChromaOf folds a MIDI key into its pitch class (C = 0).
func ChromaOf(key int) int { return ((key % PitchClasses) + PitchClasses) % PitchClasses }

// A Note is a discrete note event assembled from consecutive voiced
// frames: quantized to the nearest MIDI key with a cents deviation.
type Note struct {
	// Start and End are input-stream sample frames. End is 0 while the
	// note is still sounding (only from Tracker.Current).
	Start, End int64
	// Key is the nearest MIDI note.
	Key int
	// Cents is the median deviation from Key, negative = flat.
	Cents float64
	// MinCents and MaxCents bound the note's cents TRAJECTORY — the
	// lowest and highest deviation from Key it reached — and EndCents is
	// the deviation it settled on (the median of its final hops).
	//
	// They exist because Cents is a median over the WHOLE note, which
	// averages away the one thing a bend or a legato slide is about:
	// where the pitch went. The tracker deliberately keeps a continuously
	// sounded note as one Note on its ORIGIN key (see Tracker), so
	// without a trajectory summary a caller cannot tell a slide from a
	// note played badly out of tune — and a score's slide-DESTINATION
	// note, which is written at the destination pitch, becomes
	// structurally unmatchable however well it was played. Key plus
	// EndCents/100 is the key the note arrived on; a key whose deviation
	// falls inside [MinCents, MaxCents] is one the note passed through.
	MinCents, MaxCents, EndCents float64
	// Clarity is the median frame clarity over the note.
	Clarity float64
}
