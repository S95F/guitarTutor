package pitchml

import (
	"fmt"
	"math"
)

// ModelSampleRate is the rate SwiftF0 was trained on and the only rate it
// accepts. Everything handed to the model passes through the decimator
// below to get here.
const ModelSampleRate = 16000

// Anti-alias filter design. The decimator is a linear-phase FIR low-pass
// evaluated only at the samples it keeps (a polyphase decimator in the
// simplest possible form), designed as a Hamming-windowed sinc:
//
//	h[n] = W(n) * 2*fc/Fs * sinc(2*fc/Fs * (n - (N-1)/2)),  sum(h) = 1
//	W(n) = 0.54 - 0.46*cos(2*pi*n/(N-1))                    (Hamming)
//
// with fc = 6 kHz (the -6 dB point) and N odd, sized so the transition
// width is about 2 kHz. Hamming's transition width is ~3.3*Fs/N, so
// N = 3.3*Fs/2000 rounded up to odd: 81 taps at 48 kHz, 53 at 32 kHz.
//
// Measured response at 48 kHz: flat to within 0.05 dB from DC to 5 kHz,
// -6 dB at 6 kHz, and 55 dB or more of rejection everywhere from 7 kHz to
// Nyquist. The band that survives decimation to 16 kHz is therefore clean
// up to ~7 kHz, and the only content that folds back sits above that —
// harmonic dust, well clear of the fundamentals SwiftF0 reports (its range
// tops out near 2 kHz) and of a guitar's first several harmonics.
//
// Why not something sharper: a steeper filter costs taps and group delay
// for stopband margin nothing in this signal chain needs. Why not a
// cheaper 3-tap box: a box filter is barely 10 dB down at 8 kHz, which
// would fold pick attack and amp hiss straight into the pitch band.
const (
	antiAliasCutoffHz     = 6000.0
	antiAliasTransitionHz = 2000.0
	// hammingTransitionTaps is the 3.3 in the transition-width rule.
	hammingTransitionTaps = 3.3
	minAntiAliasTaps      = 21
	maxAntiAliasTaps      = 401
)

// sinc is the normalized cardinal sine, sin(pi*x)/(pi*x), with sinc(0)=1.
func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

// antiAliasTapCount returns the odd tap count used at an input rate.
func antiAliasTapCount(sampleRate int) int {
	n := int(math.Ceil(hammingTransitionTaps * float64(sampleRate) / antiAliasTransitionHz))
	if n%2 == 0 {
		n++
	}
	if n < minAntiAliasTaps {
		n = minAntiAliasTaps
	}
	if n > maxAntiAliasTaps {
		n = maxAntiAliasTaps
	}
	return n
}

// antiAliasTaps designs the decimation low-pass for an input rate: a
// Hamming-windowed sinc, symmetric (hence linear phase), normalized to
// unity DC gain.
func antiAliasTaps(sampleRate int) []float64 {
	n := antiAliasTapCount(sampleRate)
	h := make([]float64, n)
	mid := float64(n-1) / 2
	ft := 2 * antiAliasCutoffHz / float64(sampleRate)
	sum := 0.0
	for i := range h {
		w := 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		h[i] = w * ft * sinc(ft*(float64(i)-mid))
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	return h
}

// A decimator converts one analysis window from the input rate down to
// ModelSampleRate. It is stateless between calls: each window is filtered
// as if surrounded by silence. That is deliberate — the detector's windows
// overlap heavily (a 2048-sample window every 480 samples at 48 kHz), so
// no audio goes unseen, and it keeps EstimateF0 a pure function of the
// window it was handed, which is what the pitch.F0Estimator contract
// implies and what makes it testable.
type decimator struct {
	ratio int
	taps  []float64
	delay int // (len(taps)-1)/2, the filter's group delay in input samples
}

// newDecimator builds the decimator for an input sample rate, which must
// be a whole multiple of ModelSampleRate (resolve enforces that; this
// re-checks so the type is safe on its own).
func newDecimator(sampleRate int) (*decimator, error) {
	if sampleRate < ModelSampleRate || sampleRate%ModelSampleRate != 0 {
		return nil, fmt.Errorf("%w: %d Hz is not a whole multiple of %d Hz", ErrSampleRate, sampleRate, ModelSampleRate)
	}
	d := &decimator{ratio: sampleRate / ModelSampleRate}
	if d.ratio == 1 {
		return d, nil // already at the model rate: nothing to filter
	}
	d.taps = antiAliasTaps(sampleRate)
	d.delay = (len(d.taps) - 1) / 2
	return d, nil
}

// outLen is how many 16 kHz samples a window of n input samples yields.
func (d *decimator) outLen(n int) int { return n / d.ratio }

// decimate filters and downsamples in, writing into dst and returning the
// filled prefix. dst must have capacity for outLen(len(in)); it is never
// grown here, so the caller's buffer is the only allocation.
//
// Output sample k is the filtered value at input index k*ratio: the
// symmetric taps are applied around that point, so the result is aligned
// with the input (zero net delay) and the window's center stays its
// center, which is what the detector stamps its frames on. Taps that
// reach outside the window read as zero.
func (d *decimator) decimate(in []float32, dst []float32) []float32 {
	n := d.outLen(len(in))
	if n > cap(dst) {
		n = cap(dst)
	}
	out := dst[:n]
	if d.ratio == 1 {
		copy(out, in)
		return out
	}
	last := len(in) - 1
	for k := range out {
		center := k*d.ratio + d.delay
		lo := 0
		if v := center - last; v > lo {
			lo = v
		}
		hi := len(d.taps) - 1
		if center < hi {
			hi = center
		}
		acc := 0.0
		for j := lo; j <= hi; j++ {
			acc += d.taps[j] * float64(in[center-j])
		}
		out[k] = float32(acc)
	}
	return out
}
