package pitch

import (
	"math"

	"gonum.org/v1/gonum/dsp/fourier"
)

// Detector tunables. Chosen from the MPM/YIN literature and the D4 survey;
// the tests pin the contract (accuracy, gating, octave choice), not these
// exact values.
const (
	// peakThresholdK is MPM's key-maximum threshold: the chosen peak is
	// the first NSDF key maximum at least this fraction of the global
	// maximum. Lower values bias toward lower octaves.
	peakThresholdK = 0.9
	// octaveLagTol is the relative lag tolerance when looking for the
	// lower-octave candidate near double the chosen lag.
	octaveLagTol = 0.08
	// octaveBetterMargin flips to the lower octave whenever its peak is
	// higher than the chosen one by at least this much. A margin (rather
	// than >=) matters: for a clean tone the NSDF peaks at the period
	// and at all its multiples are equal up to numerical noise, and a
	// marginless rule would octave-flip pure tones at random.
	octaveBetterMargin = 0.01
	// octaveWithinDelta additionally flips to a lower-octave peak within
	// this much *below* the chosen one, but only when the chosen peak is
	// itself measurably below the global maximum (distorted/clipped
	// guitar biases autocorrelation to harmonics; see docs/DECISIONS.md
	// D4). The extra condition keeps clean tones, whose chosen peak IS
	// the global maximum, from flipping down an octave.
	octaveWithinDelta = 0.04
	// yinDipThreshold is YIN's absolute-dip threshold on the cumulative
	// mean normalized difference; the first dip below it wins.
	yinDipThreshold = 0.15
	// yinAperiodicityMax: the YIN cross-check only vetoes MPM when YIN
	// itself is confident, i.e. its dip (aperiodicity) is below this.
	yinAperiodicityMax = 0.2
	// yinDisagreeCents is the MPM/YIN disagreement, folded to the
	// nearest octave, beyond which the frame is marked unvoiced.
	// Disagreement is folded because octave-related splits between the
	// two estimators are expected on harmonic-heavy signals and are
	// already arbitrated by the octave guard; the cross-check exists to
	// catch non-octave garbage (noise tracked as pitch).
	yinDisagreeCents = 100
	// onsetJumpDB is the per-hop RMS rise over the smoothed level that
	// counts as an attack.
	onsetJumpDB = 8
	// onsetRefractorySeconds suppresses re-triggering after an onset.
	onsetRefractorySeconds = 0.05
	// onsetSmoothing is the one-pole coefficient of the smoothed hop
	// level the jump is measured against (higher = slower).
	onsetSmoothing = 0.7
)

// Onset flux tunables. The RMS trigger above is level-based, and level is
// exactly what a chord change at practice tempo does NOT provide: strum a
// new shape while the old one still rings and the broadband level barely
// moves — worse, the smoothed level the jump is measured against was just
// raised by the chord that is still sounding. No onset fires, no Strum is
// emitted, and every note of the new chord scores a plain Miss, which
// docs/DECISIONS.md D5 calls the worst outcome this project can produce.
//
// Half-wave-rectified spectral flux is the level-independent companion:
// only bins that GAINED magnitude since the previous hop count, so a
// decaying note (every bin falling) scores ~0 no matter how loud it is,
// while new strings appearing score high no matter how quiet they are.
// Normalizing by the hop's own total magnitude makes the trigger scale
// free, so one threshold works from bedroom level to full output.
const (
	// onsetFluxThreshold is the fraction of a hop's (Hann-smoothed)
	// magnitude that must be NEW — risen since the previous hop — to
	// count as an attack on its own.
	//
	// Measured over the chord corpus: the smallest flux any chord change
	// over a ringing chord produced is 0.245, the largest any SUSTAINING
	// chord produced is 0.197, a single note's decay peaks at 0.003, a
	// ±30-cent vibrato at 0.027 and a two-semitone bend at 0.007. The
	// end-to-end sweep is flat from 0.20 to 0.24 (32/32 chord changes
	// caught, 0 spurious onsets over 32 sustained chords plus the note,
	// vibrato and bend signals); 0.20 is the low end of that plateau,
	// chosen because it also catches the shortest gap tested — a new
	// chord 300 ms after the last — 18/18 instead of 15/18.
	onsetFluxThreshold = 0.20
	// onsetFluxRatio additionally requires the hop to be this much
	// fluxier than the running average. The absolute threshold above was
	// measured on a synth whose strings beat against each other in a
	// known way; a real instrument, room, or amp will sit at some other
	// baseline, and this makes the trigger track it instead of assuming
	// it. Kept mild (1.2) so it cannot veto a genuine change.
	onsetFluxRatio = 1.20
	// onsetFluxSmoothing is the one-pole coefficient of that running
	// average (higher = slower). At the default hop its time constant is
	// ~33 ms, long enough to average the texture and short enough that
	// one attack's flux does not mask the next chord.
	onsetFluxSmoothing = 0.7
	// onsetFluxFloorMult keeps flux from arming on near-silence, where
	// the normalization divides by almost nothing and noise looks like an
	// attack: the hop must be at least this far above the noise floor.
	// The level trigger already covers attacks out of silence.
	onsetFluxFloorMult = 2.0
	// onsetFluxRefractorySeconds is the flux trigger's own, longer
	// refractory. A downstroke is not an instant: at a 20 ms spread the
	// sixth string speaks 100 ms after the first, and every one of those
	// strings is genuinely new spectral energy. Without a refractory
	// covering the whole sweep the flux trigger fires a second time
	// mid-strum and splits one chord into two Strums. The level
	// trigger's shorter refractory is unchanged, so fast repeated
	// picking still registers.
	onsetFluxRefractorySeconds = 0.15
	// onsetFluxMaxHz tops the flux band. Wider than chromaMaxHz on
	// purpose: dense upper partials are bleed to a pitch-class fold but
	// evidence to a "did the spectrum change" test.
	onsetFluxMaxHz = 2500
)

// Dip-recovery onset tunables. The two triggers above both look for a
// RISE: the level trigger for an 8 dB jump in one hop, the flux trigger
// for magnitude that is new since the previous hop. A wind player's
// re-articulation provides neither. A tongue stroke occludes the reed for
// tens of milliseconds — the level DIPS and recovers — and with the reed's
// 90 ms release T60 the dip is partial, so the rise on the far side is far
// below 8 dB, and a soft stroke recovers slowly enough that the flux stays
// under its threshold too. Measured on the synthesized reed at the default
// hop, sweeping the stroke across eight phases of the hop grid
// (Config.OnsetDipDB gates the path; "excursion" is the arming variable —
// smoothed level over hop RMS, in dB — at its worst-case phase):
//
//	tongue gap 15 ms   hop RMS dips ~11 dB below steady, excursion  7.3+
//	tongue gap 25 ms   hop RMS dips ~13 dB,              excursion 10.8+
//	tongue gap 40 ms   hop RMS dips ~23 dB,              excursion 15.9+
//	soft stroke, 7 dB dip:                 excursion 5.3–6.4
//	soft stroke, 6 dB dip over 40–60 ms:   excursion 4.6–5.4
//	                                       (flux peaks at 0.162 — blind)
//	slur with a 4 dB velocity drop:        excursion 3.1–4.1, never recovers
//	gapless engine retrigger:              excursion ~4     (flux 0.32+)
//	vibrato / slide, 3 s:                  excursion  0.3
//
// The full-gap rows are caught by flux today (a fresh reed attack is new
// magnitude everywhere), but only by courtesy of the synth's phase-reset
// attack; the soft-stroke rows are the documented gap (DECISIONS D8) and
// nothing but the dip path sees them — at the wind tuning (windOnsetDipDB
// 5) strokes of 7 dB and deeper fire at every phase, and the 6 dB stroke
// is the threshold's visible edge (5 of 8 phases). The discriminator is
// dip AND recover: arming alone also happens on releases and on slurred
// velocity drops, and those simply never recover, so the candidate times
// out without firing.
const (
	// onsetDipRecoverDB is how close (dB) to the pre-dip reference the
	// level must climb back for the dip to count as a re-articulation.
	// 3 dB under the reference admits the next note being tongued a shade
	// softer; a velocity drop that STAYS down never gets there.
	onsetDipRecoverDB = 3
	// defaultOnsetDipRecoverHops is Config.OnsetDipRecoverHops's default:
	// the hops after arming within which the recovery must land. Measured
	// recovery times from arming: 3 hops at a 15 ms gap, 4 at 25 ms, 5 at
	// 40 ms, 7 at 60 ms — and at 60 ms the recovering edge reaches 8.8 dB
	// in one hop, so the plain level trigger takes over past that. 8
	// covers the whole tongue-gap regime with one hop to spare, and stays
	// short enough that a decrescendo-then-crescendo phrase (hundreds of
	// milliseconds) cannot connect its two halves into a phantom onset.
	defaultOnsetDipRecoverHops = 8
	// onsetDipRefractorySeconds is the dip trigger's own refractory.
	// Longer than the level trigger's (a dip-and-recover cycle spans
	// several hops and must not fire twice), shorter than the flux
	// trigger's: sixteenth-note tonguing at 140 bpm re-articulates every
	// 107 ms, and the dip path exists precisely for repeated notes.
	onsetDipRefractorySeconds = 0.10
	// onsetDipFloorMult keeps the dip path from arming near silence: the
	// smoothed reference must be at least this far (12 dB) above the noise
	// floor, or the "dip" is just the floor's own wobble. Attacks out of
	// silence belong to the level trigger.
	onsetDipFloorMult = 4.0
)

// strumSkipHops is how many hops after the onset hop are dropped before
// chroma accumulation starts. With Config.StrumWindowHops it sets the
// Strum's latency: the span ends (strumSkipHops + StrumWindowHops) hops
// after the onset hop, and the Strum is emitted then.
//
// It used to be 1 — "skip the onset hop itself" — which folded the attack
// transient rather than the chord. Two things go wrong at 1. First, a
// window is wider than a hop: at Window 2048 and Hop 480 it spans 4.3
// hops, so hops 1 through 4 after the onset all STRADDLE the attack, each
// one part pre-attack (silence, or worse, the previous chord) and part
// pick noise. Second, a real downstroke is not an instant — at a 20 ms
// inter-string spread the sixth string speaks 100 ms, ten hops, after the
// first — so an early fold credits the last strings struck a fraction of
// what it credits the first, and the scorer calls them Miss.
//
// 8 hops puts the earliest folded window at 47–90 ms after the attack:
// clear of the transient, and past the sweep. Measured over the whole
// chord corpus (8 shapes x 4 spreads, TestChordCorpusSoundedClassesRankFirst):
//
//	skip=1  span=4   28 false misses, 19 of 32 combinations wrong
//	skip=4  span=4   10 false misses,  8 wrong
//	skip=6  span=8    0 false misses, worst margin +0.026
//	skip=8  span=8    0 false misses, worst margin +0.084   <- chosen
//	skip=8  span=12   0 false misses, worst margin +0.100
//
// The latency this buys costs 160 ms from attack to Strum (8 + 8 hops at
// 10 ms), against 50 ms before. That is a real cost and it was accepted
// deliberately: a Strum is not the feedback path a player feels — Frames
// and the f0 tracker still report within ~25–50 ms, and Strum.Frame
// carries the ONSET's stamp, so scoring places the event at the right
// moment however late the evidence arrives. What a longer span buys is
// the difference between telling a player who nailed an open G that they
// missed the D string and not doing that (docs/DECISIONS.md D5). 160 ms
// also stays clear of the next beat: eighth notes at 120 bpm are 250 ms
// apart, and an onset inside an open span truncates it rather than
// merging, so a faster player loses span length, not Strums.
const strumSkipHops = 8

// A Detector turns a live sample stream into per-hop Frames. Feed it
// arbitrary-length chunks; it buffers internally and emits one Frame per
// hop. Not safe for concurrent use; feed it from one goroutine.
type Detector struct {
	cfg Config

	tauMin, tauMax int
	noiseFloor     float64 // linear RMS gate from Config.NoiseFloorDB
	jumpRatio      float64 // linear form of onsetJumpDB
	refractory     int64   // onset refractory period in samples
	fluxRefractory int64   // flux-trigger refractory period in samples

	total     int64 // input samples consumed so far
	untilNext int   // samples until the next analysis window completes

	ring []float64 // the last Window input samples
	pos  int       // ring write position

	// FFT workspace, all preallocated: Process never allocates after
	// its returned slice has grown to a chunk's frame count once.
	fft    *fourier.FFT // shared plan for the 2*Window zero-padded FFTs
	padded []float64
	coeff  []complex128
	acf    []float64 // r(tau): FFT autocorrelation (scaled in analyze)
	win    []float64 // current window in chronological order
	msum   []float64 // MPM m'(tau), by downward recurrence
	nsdf   []float64 // normalized square difference function
	yin    []float64 // YIN cumulative-mean-normalized difference

	candLag []float64 // NSDF key-maximum lags (parabolic-interpolated)
	candVal []float64 // matching NSDF values

	smoothedHop float64 // smoothed per-hop RMS level
	prevHop     float64 // previous hop's RMS, for the rising-edge test
	lastOnset   int64   // center stamp of the last onset

	// Dip-recovery onset state; all zero unless Config.OnsetDipDB.
	dipRatio        float64 // linear form of Config.OnsetDipDB; 0 = path off
	dipRecoverRatio float64 // linear form of -onsetDipRecoverDB
	dipRefractory   int64   // dip-trigger refractory period in samples
	dipArmed        bool    // a dip candidate is open
	dipRef          float64 // pre-dip smoothed level the recovery is judged against
	dipHops         int     // hops since the candidate armed

	// Spectral-flux onset state; only used when the spectrum is computed
	// (needSpectrum), which is every ungated hop on the built-in path.
	needSpectrum bool
	fluxLo       int       // first bin of the flux band
	fluxHi       int       // last bin of the flux band (inclusive)
	prevMag      []float64 // previous hop's |X(k)| over that band
	prevMagOK    bool      // prevMag holds a usable previous hop
	curMag       []float64 // this hop's Hann-smoothed |X(k)| over that band
	lastFlux     float64   // most recent flux value (tests only)
	smoothedFlux float64   // smoothed flux, the adaptive trigger's baseline

	// Chroma/strum accumulation, all nil/zero unless Config.Strums.
	chroma    *chromaFold
	acc       [PitchClasses]float64 // unnormalized chroma of the open span
	accEarly  [PitchClasses]float64 // chroma of the skipped lead-in, as a fallback
	accActive bool
	// accHops counts hops since the onset hop: negative while the span
	// is still inside strumSkipHops, then the number folded so far.
	accHops    int
	accFrame   int64 // the onset's stamp, which the Strum carries
	accRMS     float64
	accClarity float64
	strums     []Strum

	winF32 []float32 // Config.Estimator's input window; nil without one

	out []Frame
}

// NewDetector builds a detector. Config values of 0 take defaults.
func NewDetector(cfg Config) *Detector {
	cfg = cfg.withDefaults()
	w := cfg.Window
	d := &Detector{
		cfg:            cfg,
		noiseFloor:     math.Pow(10, cfg.NoiseFloorDB/20),
		jumpRatio:      math.Pow(10, float64(onsetJumpDB)/20),
		refractory:     int64(onsetRefractorySeconds * float64(cfg.SampleRate)),
		fluxRefractory: int64(onsetFluxRefractorySeconds * float64(cfg.SampleRate)),
		untilNext:      w,
		ring:           make([]float64, w),
		fft:            fourier.NewFFT(2 * w),
		padded:         make([]float64, 2*w),
		coeff:          make([]complex128, w+1),
		acf:            make([]float64, 2*w),
		win:            make([]float64, w),
		candLag:        make([]float64, 0, 64),
		candVal:        make([]float64, 0, 64),
		lastOnset:      math.MinInt64 / 2,
	}
	if cfg.OnsetDipDB > 0 {
		d.dipRatio = math.Pow(10, cfg.OnsetDipDB/20)
		d.dipRecoverRatio = math.Pow(10, -onsetDipRecoverDB/20.0)
		d.dipRefractory = int64(onsetDipRefractorySeconds * float64(cfg.SampleRate))
	}
	d.tauMin = int(float64(cfg.SampleRate) / cfg.MaxHz)
	if d.tauMin < 2 {
		d.tauMin = 2
	}
	d.tauMax = int(math.Ceil(float64(cfg.SampleRate) / cfg.MinHz))
	// The functions are computed one lag past tauMax so a peak sitting
	// exactly on tauMax still has a neighbor to interpolate against.
	if d.tauMax > w-2 {
		d.tauMax = w - 2
	}
	if d.tauMax < 2 {
		d.tauMax = 2
	}
	// A window too short for the range shrinks the range, never the
	// other way: raising tauMax back above w-2 here would index past the
	// analysis buffers on the first analyze (it used to, before
	// withDefaults grew tiny windows). Clamp tauMin down instead; the
	// tauMax floor above keeps the >= 2 floor intact.
	if d.tauMin > d.tauMax {
		d.tauMin = d.tauMax
	}
	d.msum = make([]float64, d.tauMax+2)
	d.nsdf = make([]float64, d.tauMax+2)
	d.yin = make([]float64, d.tauMax+2)
	if cfg.Strums {
		hi := float64(chromaMaxHz)
		if cfg.MaxHz > hi {
			hi = cfg.MaxHz
		}
		d.chroma = newChromaFold(cfg.SampleRate, 2*w, cfg.MinHz, hi)
		d.strums = make([]Strum, 0, 8)
	}
	// The spectrum feeds three consumers now: the MPM autocorrelation,
	// the chroma fold, and the flux onset trigger. The built-in path
	// computes it on every ungated hop anyway; an external estimator
	// only pays for it when Strums is on, and then on every hop rather
	// than only inside a span, because the pre-onset baseline and the
	// flux trigger both need the hops BEFORE an onset is known about.
	d.needSpectrum = cfg.Estimator == nil || cfg.Strums
	if d.needSpectrum {
		// Flux band: the same range the chroma folds, which is where a
		// guitar's note-defining partials live. Going higher would let
		// pick scrape and string squeak trigger onsets.
		binHz := float64(cfg.SampleRate) / float64(2*w)
		// The Hann kernel reads two bins either side, so the band keeps
		// that much headroom inside the transform.
		d.fluxLo = int(math.Ceil(cfg.MinHz / binHz))
		if d.fluxLo < 2 {
			d.fluxLo = 2
		}
		fhi := float64(onsetFluxMaxHz)
		if cfg.MaxHz > fhi {
			fhi = cfg.MaxHz
		}
		d.fluxHi = int(math.Floor(fhi / binHz))
		if d.fluxHi > w-2 {
			d.fluxHi = w - 2
		}
		if d.fluxHi < d.fluxLo {
			d.fluxHi = d.fluxLo
		}
		d.prevMag = make([]float64, d.fluxHi+1)
		d.curMag = make([]float64, d.fluxHi+1)
	}
	if cfg.Estimator != nil {
		d.winF32 = make([]float32, w)
	}
	return d
}

// Process consumes samples and returns the Frames completed by them. The
// returned slice is reused across calls — copy anything you keep.
func (d *Detector) Process(samples []float32) []Frame {
	out := d.out[:0]
	d.strums = d.strums[:0]
	for _, s := range samples {
		d.ring[d.pos] = float64(s)
		d.pos++
		if d.pos == len(d.ring) {
			d.pos = 0
		}
		d.total++
		d.untilNext--
		if d.untilNext == 0 {
			out = append(out, d.analyze())
			d.untilNext = d.cfg.Hop
		}
	}
	d.out = out
	return out
}

// Strums returns the Strums COMPLETED by the most recent Process call —
// same convention as the Frames Process returns, including the reused
// backing array: copy anything you keep. It is always empty unless
// Config.Strums is set.
//
// "Completed" means the strum's whole accumulation window has been fed, so
// a Strum surfaces about (strumSkipHops + StrumWindowHops) hops after its
// onset — ~160 ms at the defaults — and carries that onset's frame stamp,
// not the stamp of the hop that completed it. An onset in the last few
// hops of a stream never completes and is never reported.
func (d *Detector) Strums() []Strum { return d.strums }

// EstimatorName identifies the pitch estimator in use — Config.Estimator's
// Name, or "mpm" for the built-in one — for logs and the settings UI.
func (d *Detector) EstimatorName() string {
	if d.cfg.Estimator != nil {
		return d.cfg.Estimator.Name()
	}
	return "mpm"
}

// analyze runs one hop: the per-window pipeline, then the strum
// accumulator that spans several hops.
func (d *Detector) analyze() Frame {
	f := d.analyzeWindow()
	if d.chroma != nil {
		d.advanceStrum(&f)
	}
	return f
}

// beginStrum opens a strum accumulation at an onset stamped at center. An
// onset inside an open span truncates it: the evidence for the earlier
// attack is whatever sounded before this one.
func (d *Detector) beginStrum(center int64) {
	d.finishStrum()
	d.acc = [PitchClasses]float64{}
	d.accEarly = [PitchClasses]float64{}
	d.accActive = true
	// Hops -strumSkipHops .. -1 are dropped: the onset hop's window and
	// the few after it straddle the attack (a window is wider than a
	// hop), and a real downstroke is still sweeping across the strings
	// through them. See strumSkipHops.
	d.accHops = -strumSkipHops
	d.accFrame = center
	d.accRMS = 0
	d.accClarity = 0
	// Arm the pre-onset baseline: the last hop observed before this
	// onset, whose window ends where the attack begins, so it holds the
	// PREVIOUS chord alone (or, from silence, nothing at all).
	d.chroma.armBaseline()
}

// advanceStrum credits one analyzed hop to the open span and emits the
// Strum once the span is full.
func (d *Detector) advanceStrum(f *Frame) {
	if !d.accActive {
		return
	}
	// RMS and Clarity cover the WHOLE span, lead-in included: the peak
	// level of a strum is at the attack, in exactly the hops the chroma
	// fold skips, and a Strum that under-reported its own loudness would
	// misinform anything scoring dynamics. It also means a span
	// truncated before its folding hops still carries a real level.
	if f.RMS > d.accRMS {
		d.accRMS = f.RMS
	}
	if f.Clarity > d.accClarity {
		d.accClarity = f.Clarity
	}
	if d.accHops < 0 {
		d.accHops++ // still inside the skipped lead-in
		return
	}
	d.accHops++
	if d.accHops >= d.cfg.StrumWindowHops {
		d.finishStrum()
	}
}

// finishStrum emits the open span, if any, as a Strum.
//
// A span truncated by the next onset before strumSkipHops had elapsed has
// nothing in the main accumulator, and reporting an all-zero Chroma would
// be the worst possible answer: the scorer would find none of the expected
// classes and call every note of a chord the player did play a Miss
// (docs/DECISIONS.md D5). So the lead-in is accumulated too, separately,
// and stands in when the span never reached its folding hops. That
// evidence is exactly the attack-transient-contaminated fold this change
// set out to stop using — but as the fallback for playing faster than
// strumSkipHops it is far better than nothing.
func (d *Detector) finishStrum() {
	if !d.accActive {
		return
	}
	acc := &d.acc
	if d.accHops <= 0 {
		acc = &d.accEarly
	}
	d.strums = append(d.strums, Strum{
		Frame:   d.accFrame,
		Chroma:  normalizeChroma(acc),
		RMS:     d.accRMS,
		Clarity: d.accClarity,
	})
	d.accActive = false
}

// spectrum fills d.coeff with the power spectrum |X(k)|^2 of the current
// window, zero-padded to 2W. Three consumers run off this one transform:
// the MPM autocorrelation (which inverts it), the chroma fold (which reads
// it), and the flux onset trigger — which reads the COMPLEX coefficients
// first, in hannMags, before they are squared in place.
func (d *Detector) spectrum() {
	w := d.cfg.Window
	copy(d.padded, d.win)
	for i := w; i < 2*w; i++ {
		d.padded[i] = 0
	}
	d.fft.Coefficients(d.coeff, d.padded)
	if d.curMag != nil {
		d.hannMags()
	}
	for i, c := range d.coeff {
		re, im := real(c), imag(c)
		d.coeff[i] = complex(re*re+im*im, 0)
	}
}

// hannMags fills d.curMag over the flux band with the magnitudes the
// transform WOULD have produced under a Hann window, at the cost of three
// multiply-adds per bin and no second FFT.
//
// Why the flux trigger needs one when nothing else here does: the analysis
// window is rectangular, so a partial sitting between bin centers both
// leaks across the spectrum and loses amplitude to scalloping. The chroma
// fold is immune — it picks peaks and interpolates, and never compares one
// hop against another — but flux is a hop-to-hop DIFFERENCE, and under a
// rectangular window a partial that merely SLIDES (vibrato, a bend, a
// string settling after the attack) redistributes enough energy to look
// like a new note. Measured on a 220 Hz sine with a ±30-cent 5 Hz vibrato:
// rectangular flux peaks at 0.244 and fires seven spurious onsets in 1.4 s;
// the Hann-smoothed flux over the same signal stays low enough to fire
// none, while a chord change over a ringing chord is barely affected.
//
// The kernel: a periodic Hann of length W is 0.5 - 0.5*cos(2*pi*n/W), and
// that cosine is bin 2 of the 2W-point transform the detector already
// computes, so windowing is the convolution
// X_hann(k) = 0.5*X(k) - 0.25*X(k-2) - 0.25*X(k+2).
func (d *Detector) hannMags() {
	for k := d.fluxLo; k <= d.fluxHi; k++ {
		c := 0.5*d.coeff[k] - 0.25*(d.coeff[k-2]+d.coeff[k+2])
		d.curMag[k] = math.Hypot(real(c), imag(c))
	}
}

// spectralFlux returns this hop's half-wave-rectified spectral flux — the
// share of the hop's total magnitude that ROSE since the previous hop —
// and rolls d.prevMag forward. It returns 0 when there is no usable
// previous hop (start of stream, or the first hop after a gated one), so
// the level trigger alone handles attacks out of silence.
//
// Half-wave rectification is what makes this a chord-change detector
// rather than a second loudness meter: falling bins contribute nothing, so
// a note decaying from full volume scores near zero while a new string
// added under a ringing chord scores high. Normalizing by the hop's own
// total magnitude removes absolute level, so one threshold serves every
// playing dynamic.
func (d *Detector) spectralFlux() float64 {
	sum, risen := 0.0, 0.0
	for k := d.fluxLo; k <= d.fluxHi; k++ {
		m := d.curMag[k]
		sum += m
		if diff := m - d.prevMag[k]; diff > 0 {
			risen += diff
		}
		d.prevMag[k] = m
	}
	usable := d.prevMagOK
	d.prevMagOK = true
	if !usable || sum <= 0 {
		d.lastFlux = 0
		return 0
	}
	d.lastFlux = risen / sum
	return d.lastFlux
}

// analyzeWindow runs the full per-hop pipeline — energy gate, onset
// detector, MPM NSDF via FFT autocorrelation, octave guard, YIN
// cross-check — over the window that just completed, and returns its Frame
// stamped at the window's center. A configured Config.Estimator replaces
// everything from the NSDF onward; see estimateExternal.
func (d *Detector) analyzeWindow() Frame {
	w := d.cfg.Window
	// Unroll the ring into chronological order: the oldest sample sits
	// at the write position.
	n := copy(d.win, d.ring[d.pos:])
	copy(d.win[n:], d.ring[:d.pos])

	var sumsq float64
	for _, v := range d.win {
		sumsq += v * v
	}
	rms := math.Sqrt(sumsq / float64(w))

	var hopsq float64
	for _, v := range d.win[w-d.cfg.Hop:] {
		hopsq += v * v
	}
	hopRMS := math.Sqrt(hopsq / float64(d.cfg.Hop))

	center := d.total - int64(w/2)
	frame := Frame{Frame: center, RMS: rms}

	gated := rms < d.noiseFloor

	// The transform comes first now: the flux onset trigger reads it,
	// the chroma fold reads it, and (on the built-in path) the
	// autocorrelation inverts it. A gated window skips it entirely, so
	// silence still costs nothing.
	flux := 0.0
	if gated || !d.needSpectrum {
		d.prevMagOK = false
	} else {
		d.spectrum()
		flux = d.spectralFlux()
	}

	// Onset, three independent triggers sharing one refractory stamp:
	//
	//   level — the hop's RMS jumps by onsetJumpDB over the smoothed
	//   level (never compared against less than the noise floor, so
	//   silence cannot arm hair-trigger onsets), on a rising edge. This
	//   catches attacks out of silence and dynamic accents.
	//
	//   flux — a large fraction of the hop's magnitude is NEW since the
	//   previous hop. This catches the case the level trigger cannot see
	//   at all: a chord change at constant loudness, where the smoothed
	//   level was already raised by the chord that is still sounding.
	//
	//   dip — the level DIPPED below the smoothed reference and climbed
	//   back within the recovery window: a wind re-articulation, which
	//   raises neither of the other two (Config.OnsetDipDB, and the
	//   onsetDip* tunables' measured table). Off unless configured.
	ref := d.smoothedHop
	if ref < d.noiseFloor {
		ref = d.noiseFloor
	}
	levelOnset := hopRMS > ref*d.jumpRatio && hopRMS > d.prevHop &&
		center-d.lastOnset >= d.refractory
	fluxOnset := flux > onsetFluxThreshold && flux > onsetFluxRatio*d.smoothedFlux &&
		hopRMS > d.noiseFloor*onsetFluxFloorMult &&
		center-d.lastOnset >= d.fluxRefractory
	dipOnset := false
	if d.dipRatio > 0 {
		if d.dipArmed {
			d.dipHops++
			switch {
			case hopRMS >= d.dipRef*d.dipRecoverRatio && hopRMS > d.prevHop &&
				center-d.lastOnset >= d.dipRefractory:
				// Recovered to within onsetDipRecoverDB of the pre-dip
				// reference, on a rising edge: the new note is speaking.
				// Fired HERE rather than at the dip's bottom, so the
				// stamp lands at the re-articulated note's start — the
				// moment scoring should align against — exactly as the
				// other triggers stamp the hop that showed the attack.
				dipOnset = true
			case d.dipHops >= d.cfg.OnsetDipRecoverHops:
				// Never recovered: a release, a rest, or a slurred
				// velocity drop — not a tongue stroke. (A gap longer
				// than the window can still re-arm below, against the
				// decayed reference, and past ~60 ms the recovering
				// edge is steep enough for the level trigger anyway.)
				d.dipArmed = false
			}
		}
		if !d.dipArmed && !dipOnset && hopRMS*d.dipRatio < d.smoothedHop &&
			d.smoothedHop >= d.noiseFloor*onsetDipFloorMult {
			// The hop fell OnsetDipDB below the smoothed level while that
			// level was well above the floor: a re-articulation candidate.
			// The smoothed level still lags the dip here (smoothing is
			// slower than a tongue stroke), so it is the honest pre-dip
			// reference for the recovery test.
			d.dipArmed = true
			d.dipRef = d.smoothedHop
			d.dipHops = 0
		}
	}
	if levelOnset || fluxOnset || dipOnset {
		frame.Onset = true
		d.lastOnset = center
		// Whatever fired explains any dip in progress; leaving the
		// candidate armed would let the same stroke fire twice.
		d.dipArmed = false
		if d.chroma != nil {
			d.beginStrum(center)
		}
	}
	d.prevHop = hopRMS
	d.smoothedHop = onsetSmoothing*d.smoothedHop + (1-onsetSmoothing)*hopRMS
	d.smoothedFlux = onsetFluxSmoothing*d.smoothedFlux + (1-onsetFluxSmoothing)*flux

	// The chroma pass: every ungated hop refreshes the fold's baseline
	// candidate, and hops inside an open span (beginStrum, just above,
	// left the onset hop and strumSkipHops after it outside one) are
	// also folded into the accumulator.
	if d.chroma != nil {
		switch {
		case gated:
			d.chroma.clearBaseline()
		case !d.accActive:
			d.chroma.hop(d.coeff, nil)
		case d.accHops >= 0:
			d.chroma.hop(d.coeff, &d.acc)
		default:
			// Inside the skipped lead-in: fold it anyway, into the
			// fallback accumulator finishStrum uses only for a span
			// truncated before it reached its real hops.
			d.chroma.hop(d.coeff, &d.accEarly)
		}
	}

	if gated {
		return frame // unvoiced, no pitch analysis
	}

	if d.cfg.Estimator != nil {
		return d.estimateExternal(frame)
	}

	tauLim := d.tauMax + 1

	// r(tau) from the power spectrum in d.coeff: the window zero-pads to
	// 2W so the circular correlation equals the linear one, and Sequence
	// inverts it. The gonum transform is unnormalized, hence the 1/(2W).
	d.fft.Sequence(d.acf, d.coeff)
	inv := 1 / float64(2*w)
	for tau := 0; tau <= tauLim; tau++ {
		d.acf[tau] *= inv
	}

	// m'(tau) = sum(x[i]^2 + x[i+tau]^2) over the overlap, by peeling
	// one term off each end per lag; then NSDF = 2r/m'.
	d.msum[0] = 2 * d.acf[0]
	d.nsdf[0] = 1
	for tau := 1; tau <= tauLim; tau++ {
		a := d.win[tau-1]
		b := d.win[w-tau]
		d.msum[tau] = d.msum[tau-1] - a*a - b*b
		if d.msum[tau] > 1e-12 {
			d.nsdf[tau] = 2 * d.acf[tau] / d.msum[tau]
		} else {
			d.nsdf[tau] = 0
		}
	}

	// Key maxima: the largest NSDF value between each pair of positive
	// zero crossings, after skipping the zero-lag lobe. Maxima below
	// tauMin can never be candidates, but the strongest one is kept as
	// the alias guard's evidence of a tone above MaxHz.
	d.candLag = d.candLag[:0]
	d.candVal = d.candVal[:0]
	subLag, subVal := 0.0, 0.0
	tau := 1
	for tau <= d.tauMax && d.nsdf[tau] > 0 {
		tau++
	}
	for tau <= d.tauMax {
		for tau <= d.tauMax && d.nsdf[tau] <= 0 {
			tau++
		}
		if tau > d.tauMax {
			break
		}
		best := tau
		for tau <= d.tauMax && d.nsdf[tau] > 0 {
			if d.nsdf[tau] > d.nsdf[best] {
				best = tau
			}
			tau++
		}
		if best >= d.tauMin {
			lag, val := parabolicExtremum(d.nsdf[:tauLim+1], best)
			d.candLag = append(d.candLag, lag)
			d.candVal = append(d.candVal, val)
		} else if lag, val := parabolicExtremum(d.nsdf[:tauLim+1], best); val > subVal {
			subLag, subVal = lag, val
		}
	}
	if len(d.candVal) == 0 {
		return frame // no periodicity evidence at all: unvoiced
	}

	gmax := d.candVal[0]
	for _, v := range d.candVal[1:] {
		if v > gmax {
			gmax = v
		}
	}
	chosen := 0
	for i, v := range d.candVal {
		if v >= peakThresholdK*gmax {
			chosen = i
			break
		}
	}

	// Octave guard: prefer a candidate near DOUBLE the chosen lag (an
	// octave lower) when it is genuinely better, or nearly as good while
	// the chosen peak is not itself the global maximum. Iterated for the
	// (rare) two-octave error.
	for range d.candLag {
		target := 2 * d.candLag[chosen]
		tol := octaveLagTol * target
		best := -1
		for j := chosen + 1; j < len(d.candLag); j++ {
			if math.Abs(d.candLag[j]-target) <= tol &&
				(best < 0 || math.Abs(d.candLag[j]-target) < math.Abs(d.candLag[best]-target)) {
				best = j
			}
		}
		if best < 0 {
			break
		}
		better := d.candVal[best] > d.candVal[chosen]+octaveBetterMargin
		within := d.candVal[chosen] < gmax-octaveBetterMargin &&
			d.candVal[best] >= d.candVal[chosen]-octaveWithinDelta
		if !better && !within {
			break
		}
		chosen = best
	}

	f0 := float64(d.cfg.SampleRate) / d.candLag[chosen]
	clarity := d.candVal[chosen]
	if clarity > 1 {
		clarity = 1
	} else if clarity < 0 {
		clarity = 0
	}

	// Sub-range alias guard: a below-tauMin key maximum that rivals the
	// chosen peak is the true period of a tone above MaxHz, whose NSDF
	// also peaks at every multiple of that lag — so the chosen in-range
	// peak is a confident-looking subharmonic (f/2, f/3, ...). The YIN
	// cross-check cannot veto this: it folds disagreements to the
	// nearest octave. When the chosen lag sits near an integer multiple
	// of the sub-range lag, treat the frame like a confident
	// disagreement and cap clarity below the threshold. A legitimate
	// in-range tone never trips this — its NSDF has no near-unity
	// maximum below its own period (r there is fundamental-negative).
	if subLag > 0 && subVal >= d.candVal[chosen]-octaveWithinDelta {
		mult := math.Round(d.candLag[chosen] / subLag)
		if mult >= 2 && math.Abs(d.candLag[chosen]-mult*subLag) <= octaveLagTol*d.candLag[chosen] {
			clarity = math.Min(clarity, d.cfg.ClarityThreshold*0.5)
		}
	}

	// YIN-FFT cross-check, nearly free given r and m': the difference
	// function is d(tau) = m'(tau) - 2r(tau).
	if yinF0, aper := d.yinEstimate(tauLim); yinF0 > 0 && aper < yinAperiodicityMax {
		diff := 1200 * math.Log2(yinF0/f0)
		folded := diff - 1200*math.Round(diff/1200)
		if math.Abs(folded) > yinDisagreeCents {
			// A confident, non-octave disagreement: don't guess.
			clarity = math.Min(clarity, d.cfg.ClarityThreshold*0.5)
		}
	}

	frame.Clarity = clarity
	if clarity >= d.cfg.ClarityThreshold {
		frame.F0 = f0
	}
	return frame
}

// estimateExternal fills in frame's pitch from Config.Estimator instead of
// the built-in MPM peak picking, and is only reached with one configured.
//
// What the estimator replaces: everything downstream of the FFT in the
// monophonic path — NSDF key-maximum picking, the octave guard, the
// sub-range alias guard, and the YIN cross-check. Those all arbitrate
// between autocorrelation peaks, and an external estimator reports no
// peaks to arbitrate; asking it for a second opinion on itself would be
// theatre. The transform itself is skipped too unless a strum span needs
// the spectrum for chroma.
//
// What still applies, unchanged: the RMS noise gate (a gated window never
// reaches here), the onset detector and its refractory period, strum
// accumulation, ClarityThreshold as the voicing rule, the MinHz/MaxHz
// search range — enforced here so an out-of-range estimate is unvoiced
// rather than a note the config says cannot exist, exactly as the built-in
// path's bounded lag search guarantees — and, downstream, the Tracker's
// median filter and note hysteresis, which only ever see Frames.
func (d *Detector) estimateExternal(frame Frame) Frame {
	for i, v := range d.win {
		d.winF32[i] = float32(v)
	}
	f0, clarity := d.cfg.Estimator.EstimateF0(d.winF32)
	if clarity > 1 {
		clarity = 1
	} else if clarity < 0 {
		clarity = 0
	}
	frame.Clarity = clarity
	if f0 >= d.cfg.MinHz && f0 <= d.cfg.MaxHz && clarity >= d.cfg.ClarityThreshold {
		frame.F0 = f0
	}
	return frame
}

// yinEstimate computes the YIN cumulative-mean-normalized difference from
// the already-computed msum and acf, and returns YIN's f0 estimate with
// its aperiodicity (the dip's depth; lower = more periodic).
func (d *Detector) yinEstimate(tauLim int) (f0, aperiodicity float64) {
	sum := 0.0
	d.yin[0] = 1
	for tau := 1; tau <= tauLim; tau++ {
		dt := d.msum[tau] - 2*d.acf[tau]
		if dt < 0 {
			dt = 0
		}
		sum += dt
		if sum > 0 {
			d.yin[tau] = dt * float64(tau) / sum
		} else {
			d.yin[tau] = 1
		}
	}
	// First dip below the threshold, descended to its local minimum;
	// fall back to the global minimum when nothing dips that far.
	yTau := -1
	for tau := d.tauMin; tau <= d.tauMax; tau++ {
		if d.yin[tau] < yinDipThreshold {
			for tau < d.tauMax && d.yin[tau+1] < d.yin[tau] {
				tau++
			}
			yTau = tau
			break
		}
	}
	if yTau < 0 {
		yTau = d.tauMin
		for tau := d.tauMin + 1; tau <= d.tauMax; tau++ {
			if d.yin[tau] < d.yin[yTau] {
				yTau = tau
			}
		}
	}
	lag, val := parabolicExtremum(d.yin[:tauLim+1], yTau)
	if lag <= 0 {
		return 0, 1
	}
	return float64(d.cfg.SampleRate) / lag, val
}

// parabolicExtremum refines the extremum of a at integer index i by
// fitting a parabola through a[i-1], a[i], a[i+1], returning the
// sub-sample position and interpolated value. It works for maxima and
// minima alike; at the array boundary it returns the integer point.
func parabolicExtremum(a []float64, i int) (pos, val float64) {
	if i <= 0 || i >= len(a)-1 {
		return float64(i), a[i]
	}
	d2 := a[i-1] - 2*a[i] + a[i+1]
	if d2 == 0 {
		return float64(i), a[i]
	}
	delta := 0.5 * (a[i-1] - a[i+1]) / d2
	if delta > 0.5 {
		delta = 0.5
	} else if delta < -0.5 {
		delta = -0.5
	}
	return float64(i) + delta, a[i] - 0.25*(a[i-1]-a[i+1])*delta
}
