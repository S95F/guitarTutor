package engine

import "math"

func (e *Engine) SetBackingTrack(left, right []float32, offsetSec float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(left)
	if len(right) < n {
		n = len(right)
	}
	e.backL = append(e.backL[:0], left[:n]...)
	e.backR = append(e.backR[:0], right[:n]...)

	silenceNonFinite(e.backL)
	silenceNonFinite(e.backR)

	if math.IsNaN(offsetSec) || math.IsInf(offsetSec, 0) {
		offsetSec = 0
	}
	e.backOffset = offsetSec
	e.segValid = false
}

func (e *Engine) ClearBackingTrack() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.backL, e.backR = nil, nil
	e.segValid = false
}

func (e *Engine) SetBackingGain(g float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if math.IsNaN(g) || math.IsInf(g, 0) || g < 0 {
		g = 0
	}
	e.backGain = g
}

func (e *Engine) mixBacking(left, right []float32, segFrame int) {
	n := len(e.backL)
	if n == 0 || !(e.backGain > 0) {
		return
	}
	g := float32(e.backGain)
	last := float64(n - 1)
	for i := range left {
		p := e.backBase + float64(segFrame+i)*e.scale
		if !(p >= 0 && p <= last) {
			continue
		}
		j := int(p)
		l, r := e.backL[j], e.backR[j]
		if j+1 < n {
			f := float32(p - float64(j))
			l += f * (e.backL[j+1] - l)
			r += f * (e.backR[j+1] - r)
		}
		left[i] += g * l
		right[i] += g * r
	}
}

func silenceNonFinite(s []float32) {
	for i, v := range s {
		if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
			s[i] = 0
		}
	}
}
