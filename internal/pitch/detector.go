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

// A Detector turns a live sample stream into per-hop Frames. Feed it
// arbitrary-length chunks; it buffers internally and emits one Frame per
// hop. Not safe for concurrent use; feed it from one goroutine.
type Detector struct {
	cfg Config

	tauMin, tauMax int
	noiseFloor     float64 // linear RMS gate from Config.NoiseFloorDB
	jumpRatio      float64 // linear form of onsetJumpDB
	refractory     int64   // onset refractory period in samples

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

	// Chroma/strum accumulation, all nil/zero unless Config.Strums.
	chroma     *chromaFold
	acc        [PitchClasses]float64 // unnormalized chroma of the open span
	accActive  bool
	accHops    int   // folded hops so far; -1 skips the onset hop itself
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
		cfg:        cfg,
		noiseFloor: math.Pow(10, cfg.NoiseFloorDB/20),
		jumpRatio:  math.Pow(10, float64(onsetJumpDB)/20),
		refractory: int64(onsetRefractorySeconds * float64(cfg.SampleRate)),
		untilNext:  w,
		ring:       make([]float64, w),
		fft:        fourier.NewFFT(2 * w),
		padded:     make([]float64, 2*w),
		coeff:      make([]complex128, w+1),
		acf:        make([]float64, 2*w),
		win:        make([]float64, w),
		candLag:    make([]float64, 0, 64),
		candVal:    make([]float64, 0, 64),
		lastOnset:  math.MinInt64 / 2,
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
// a Strum surfaces about StrumWindowHops hops after its onset and carries
// that onset's frame stamp. An onset in the last few hops of a stream
// never completes and is never reported.
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
	d.accActive = true
	// -1 skips the onset hop's own window, which is mostly pre-attack
	// (the attack only just entered its last hop) and whose spectrum is
	// therefore the pick transient rather than the strings.
	d.accHops = -1
	d.accFrame = center
	d.accRMS = 0
	d.accClarity = 0
}

// advanceStrum credits one analyzed hop to the open span and emits the
// Strum once the span is full.
func (d *Detector) advanceStrum(f *Frame) {
	if !d.accActive {
		return
	}
	if d.accHops < 0 {
		d.accHops = 0 // the onset hop, skipped
		return
	}
	if f.RMS > d.accRMS {
		d.accRMS = f.RMS
	}
	if f.Clarity > d.accClarity {
		d.accClarity = f.Clarity
	}
	d.accHops++
	if d.accHops >= d.cfg.StrumWindowHops {
		d.finishStrum()
	}
}

// finishStrum emits the open span, if any, as a Strum.
func (d *Detector) finishStrum() {
	if !d.accActive {
		return
	}
	d.strums = append(d.strums, Strum{
		Frame:   d.accFrame,
		Chroma:  normalizeChroma(&d.acc),
		RMS:     d.accRMS,
		Clarity: d.accClarity,
	})
	d.accActive = false
}

// spectrum fills d.coeff with the power spectrum |X(k)|^2 of the current
// window, zero-padded to 2W. Both the MPM autocorrelation (which inverts
// it) and the chroma fold (which reads it) run off this one transform.
func (d *Detector) spectrum() {
	w := d.cfg.Window
	copy(d.padded, d.win)
	for i := w; i < 2*w; i++ {
		d.padded[i] = 0
	}
	d.fft.Coefficients(d.coeff, d.padded)
	for i, c := range d.coeff {
		re, im := real(c), imag(c)
		d.coeff[i] = complex(re*re+im*im, 0)
	}
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

	// Onset: the hop's RMS jumps by onsetJumpDB over the smoothed level
	// (never comparing against less than the noise floor, so silence
	// cannot arm hair-trigger onsets), on a rising edge, outside the
	// refractory period.
	ref := d.smoothedHop
	if ref < d.noiseFloor {
		ref = d.noiseFloor
	}
	if hopRMS > ref*d.jumpRatio && hopRMS > d.prevHop && center-d.lastOnset >= d.refractory {
		frame.Onset = true
		d.lastOnset = center
		if d.chroma != nil {
			d.beginStrum(center)
		}
	}
	d.prevHop = hopRMS
	d.smoothedHop = onsetSmoothing*d.smoothedHop + (1-onsetSmoothing)*hopRMS

	if rms < d.noiseFloor {
		return frame // gated: unvoiced, no pitch analysis
	}

	// Chroma needs the same spectrum the autocorrelation does, so hops
	// inside an open strum span pay for the transform even on the
	// external-estimator path (which otherwise skips it entirely).
	foldChroma := d.chroma != nil && d.accActive && d.accHops >= 0

	if d.cfg.Estimator != nil {
		if foldChroma {
			d.spectrum()
			d.chroma.fold(d.coeff, &d.acc)
		}
		return d.estimateExternal(frame)
	}

	tauLim := d.tauMax + 1

	// r(tau) by FFT: spectrum zero-pads to 2W so the circular correlation
	// equals the linear one and leaves the power spectrum in d.coeff;
	// Sequence inverts it. The gonum transform is unnormalized, hence the
	// 1/(2W).
	d.spectrum()
	if foldChroma {
		// Before Sequence, which reads d.coeff without touching it —
		// the fold and the autocorrelation share one transform.
		d.chroma.fold(d.coeff, &d.acc)
	}
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
