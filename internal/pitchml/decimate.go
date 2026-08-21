package pitchml

import (
	"fmt"
	"math"
)

const ModelSampleRate = 16000

const (
	antiAliasCutoffHz     = 6000.0
	antiAliasTransitionHz = 2000.0

	hammingTransitionTaps = 3.3
	minAntiAliasTaps      = 21
	maxAntiAliasTaps      = 401
)

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	px := math.Pi * x
	return math.Sin(px) / px
}

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

type decimator struct {
	ratio int
	taps  []float64
	delay int
}

func newDecimator(sampleRate int) (*decimator, error) {
	if sampleRate < ModelSampleRate || sampleRate%ModelSampleRate != 0 {
		return nil, fmt.Errorf("%w: %d Hz is not a whole multiple of %d Hz", ErrSampleRate, sampleRate, ModelSampleRate)
	}
	d := &decimator{ratio: sampleRate / ModelSampleRate}
	if d.ratio == 1 {
		return d, nil
	}
	d.taps = antiAliasTaps(sampleRate)
	d.delay = (len(d.taps) - 1) / 2
	return d, nil
}

func (d *decimator) outLen(n int) int { return n / d.ratio }

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
