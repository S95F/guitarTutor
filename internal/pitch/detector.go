package pitch

import (
	"math"

	"gonum.org/v1/gonum/dsp/fourier"
)

const (
	peakThresholdK = 0.9

	octaveLagTol = 0.08

	octaveBetterMargin = 0.01

	octaveWithinDelta = 0.04

	yinDipThreshold = 0.15

	yinAperiodicityMax = 0.2

	yinDisagreeCents = 100

	onsetJumpDB = 8

	onsetRefractorySeconds = 0.05

	onsetSmoothing = 0.7
)

const (
	onsetFluxThreshold = 0.20

	onsetFluxRatio = 1.20

	onsetFluxSmoothing = 0.7

	onsetFluxFloorMult = 2.0

	onsetFluxRefractorySeconds = 0.15

	onsetFluxMaxHz = 2500
)

const (
	onsetDipRecoverDB = 3

	defaultOnsetDipRecoverHops = 8

	onsetDipRefractorySeconds = 0.10

	onsetDipFloorMult = 4.0
)

const strumSkipHops = 8

type Detector struct {
	cfg Config

	tauMin, tauMax int
	noiseFloor     float64
	jumpRatio      float64
	refractory     int64
	fluxRefractory int64

	total     int64
	untilNext int

	ring []float64
	pos  int

	fft    *fourier.FFT
	padded []float64
	coeff  []complex128
	acf    []float64
	win    []float64
	msum   []float64
	nsdf   []float64
	yin    []float64

	candLag []float64
	candVal []float64

	smoothedHop float64
	prevHop     float64
	lastOnset   int64

	dipRatio        float64
	dipRecoverRatio float64
	dipRefractory   int64
	dipArmed        bool
	dipRef          float64
	dipHops         int

	needSpectrum bool
	fluxLo       int
	fluxHi       int
	prevMag      []float64
	prevMagOK    bool
	curMag       []float64
	lastFlux     float64
	smoothedFlux float64

	chroma    *chromaFold
	acc       [PitchClasses]float64
	accEarly  [PitchClasses]float64
	accActive bool

	accHops    int
	accFrame   int64
	accRMS     float64
	accClarity float64
	strums     []Strum

	winF32 []float32

	out []Frame
}

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

	if d.tauMax > w-2 {
		d.tauMax = w - 2
	}
	if d.tauMax < 2 {
		d.tauMax = 2
	}

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

	d.needSpectrum = cfg.Estimator == nil || cfg.Strums
	if d.needSpectrum {

		binHz := float64(cfg.SampleRate) / float64(2*w)

		d.fluxLo = int(math.Ceil(cfg.MinHz / binHz))
		if d.fluxLo < 2 {
			d.fluxLo = 2
		}

		if d.fluxLo > w-2 {
			d.fluxLo = w - 2
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

func (d *Detector) Strums() []Strum { return d.strums }

func (d *Detector) EstimatorName() string {
	if d.cfg.Estimator != nil {
		return d.cfg.Estimator.Name()
	}
	return "mpm"
}

func (d *Detector) analyze() Frame {
	f := d.analyzeWindow()
	if d.chroma != nil {
		d.advanceStrum(&f)
	}
	return f
}

func (d *Detector) beginStrum(center int64) {
	d.finishStrum()
	d.acc = [PitchClasses]float64{}
	d.accEarly = [PitchClasses]float64{}
	d.accActive = true

	d.accHops = -strumSkipHops
	d.accFrame = center
	d.accRMS = 0
	d.accClarity = 0

	d.chroma.armBaseline()
}

func (d *Detector) advanceStrum(f *Frame) {
	if !d.accActive {
		return
	}

	if f.RMS > d.accRMS {
		d.accRMS = f.RMS
	}
	if f.Clarity > d.accClarity {
		d.accClarity = f.Clarity
	}
	if d.accHops < 0 {
		d.accHops++
		return
	}
	d.accHops++
	if d.accHops >= d.cfg.StrumWindowHops {
		d.finishStrum()
	}
}

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

func (d *Detector) hannMags() {
	for k := d.fluxLo; k <= d.fluxHi; k++ {
		c := 0.5*d.coeff[k] - 0.25*(d.coeff[k-2]+d.coeff[k+2])
		d.curMag[k] = math.Hypot(real(c), imag(c))
	}
}

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

func (d *Detector) analyzeWindow() Frame {
	w := d.cfg.Window

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

	flux := 0.0
	if gated || !d.needSpectrum {
		d.prevMagOK = false
	} else {
		d.spectrum()
		flux = d.spectralFlux()
	}

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

				dipOnset = true
			case d.dipHops >= d.cfg.OnsetDipRecoverHops:

				d.dipArmed = false
			}
		}
		if !d.dipArmed && !dipOnset && hopRMS*d.dipRatio < d.smoothedHop &&
			d.smoothedHop >= d.noiseFloor*onsetDipFloorMult {

			d.dipArmed = true
			d.dipRef = d.smoothedHop
			d.dipHops = 0
		}
	}
	if levelOnset || fluxOnset || dipOnset {
		frame.Onset = true
		d.lastOnset = center

		d.dipArmed = false
		if d.chroma != nil {
			d.beginStrum(center)
		}
	}
	d.prevHop = hopRMS
	d.smoothedHop = onsetSmoothing*d.smoothedHop + (1-onsetSmoothing)*hopRMS
	d.smoothedFlux = onsetFluxSmoothing*d.smoothedFlux + (1-onsetFluxSmoothing)*flux

	if d.chroma != nil {
		switch {
		case gated:
			d.chroma.clearBaseline()
		case !d.accActive:
			d.chroma.hop(d.coeff, nil)
		case d.accHops >= 0:
			d.chroma.hop(d.coeff, &d.acc)
		default:

			d.chroma.hop(d.coeff, &d.accEarly)
		}
	}

	if gated {
		return frame
	}

	if d.cfg.Estimator != nil {
		return d.estimateExternal(frame)
	}

	tauLim := d.tauMax + 1

	d.fft.Sequence(d.acf, d.coeff)
	inv := 1 / float64(2*w)
	for tau := 0; tau <= tauLim; tau++ {
		d.acf[tau] *= inv
	}

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
		return frame
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

	if subLag > 0 && subVal >= d.candVal[chosen]-octaveWithinDelta {
		mult := math.Round(d.candLag[chosen] / subLag)
		if mult >= 2 && math.Abs(d.candLag[chosen]-mult*subLag) <= octaveLagTol*d.candLag[chosen] {
			clarity = math.Min(clarity, d.cfg.ClarityThreshold*0.5)
		}
	}

	if yinF0, aper := d.yinEstimate(tauLim); yinF0 > 0 && aper < yinAperiodicityMax {
		diff := 1200 * math.Log2(yinF0/f0)
		folded := diff - 1200*math.Round(diff/1200)
		if math.Abs(folded) > yinDisagreeCents {

			clarity = math.Min(clarity, d.cfg.ClarityThreshold*0.5)
		}
	}

	frame.Clarity = clarity
	if clarity >= d.cfg.ClarityThreshold {
		frame.F0 = f0
	}
	return frame
}

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
