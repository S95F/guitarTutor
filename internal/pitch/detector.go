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
	if d.tauMax < d.tauMin {
		d.tauMax = d.tauMin
	}
	d.msum = make([]float64, d.tauMax+2)
	d.nsdf = make([]float64, d.tauMax+2)
	d.yin = make([]float64, d.tauMax+2)
	return d
}

// Process consumes samples and returns the Frames completed by them. The
// returned slice is reused across calls — copy anything you keep.
func (d *Detector) Process(samples []float32) []Frame {
	out := d.out[:0]
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

// analyze runs the full per-hop pipeline — energy gate, onset detector,
// MPM NSDF via FFT autocorrelation, octave guard, YIN cross-check — over
// the window that just completed, and returns its Frame stamped at the
// window's center.
func (d *Detector) analyze() Frame {
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
	}
	d.prevHop = hopRMS
	d.smoothedHop = onsetSmoothing*d.smoothedHop + (1-onsetSmoothing)*hopRMS

	if rms < d.noiseFloor {
		return frame // gated: unvoiced, no pitch analysis
	}

	tauLim := d.tauMax + 1

	// r(tau) by FFT: zero-pad to 2W so the circular correlation equals
	// the linear one, forward transform, power spectrum, inverse. The
	// gonum transform is unnormalized, hence the 1/(2W).
	copy(d.padded, d.win)
	for i := w; i < 2*w; i++ {
		d.padded[i] = 0
	}
	d.fft.Coefficients(d.coeff, d.padded)
	for i, c := range d.coeff {
		re, im := real(c), imag(c)
		d.coeff[i] = complex(re*re+im*im, 0)
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
	// zero crossings, after skipping the zero-lag lobe.
	d.candLag = d.candLag[:0]
	d.candVal = d.candVal[:0]
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
