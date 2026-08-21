package engine

import "math"

type clickState struct {
	buf []float32
	pos int
}

const (
	clickLenSeconds = 0.006
	clickAccentHz   = 1568.0
	clickBeatHz     = 1046.0
	clickAmp        = 0.4
	clickDecay      = 6.0
)

func renderClickBurst(sampleRate int, freq float64) []float32 {
	n := int(clickLenSeconds * float64(sampleRate))
	if n < 1 {
		n = 1
	}
	buf := make([]float32, n)
	for i := range buf {
		env := math.Exp(-clickDecay * float64(i) / float64(n))
		buf[i] = float32(clickAmp * env * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)))
	}
	return buf
}

func (e *Engine) startClick(accent bool) {
	buf := e.beatBuf
	if accent {
		buf = e.accentBuf
	}
	for i := range e.clicks {
		c := &e.clicks[i]
		if c.buf == nil || c.pos >= len(c.buf) {
			c.buf, c.pos = buf, 0
			return
		}
	}
	e.clicks[0] = clickState{buf: buf}
}

func (e *Engine) mixClicks(left, right []float32) {
	for i := range e.clicks {
		c := &e.clicks[i]
		if c.buf == nil || c.pos >= len(c.buf) {
			continue
		}
		m := len(c.buf) - c.pos
		if m > len(left) {
			m = len(left)
		}
		for j := 0; j < m; j++ {
			s := c.buf[c.pos+j]
			left[j] += s
			right[j] += s
		}
		c.pos += m
	}
}

func (e *Engine) stopClicks() {
	for i := range e.clicks {
		e.clicks[i] = clickState{}
	}
}
