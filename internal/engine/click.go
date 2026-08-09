package engine

import "math"

// A clickState is one sounding metronome click: a read cursor into one of
// the two prerendered burst buffers. A nil buf (or an exhausted cursor) is
// silent and free for reuse.
type clickState struct {
	buf []float32
	pos int
}

// Click synthesis parameters: short sine bursts with an exponential decay,
// prerendered once at construction so the render path never computes them.
const (
	clickLenSeconds = 0.006  // burst length
	clickAccentHz   = 1568.0 // beat 1 of a bar (G6)
	clickBeatHz     = 1046.0 // other beats (C6)
	clickAmp        = 0.4    // peak amplitude
	clickDecay      = 6.0    // decay exponent over the burst
)

// renderClickBurst prerenders one click: a sine at freq under an
// exponential-decay envelope.
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

// startClick begins sounding a click, accented or not, reusing a finished
// slot. Caller holds mu.
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

// mixClicks adds the sounding clicks into left/right and advances their
// cursors. Caller holds mu.
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

// stopClicks silences all sounding clicks. Caller holds mu.
func (e *Engine) stopClicks() {
	for i := range e.clicks {
		e.clicks[i] = clickState{}
	}
}
