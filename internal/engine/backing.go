package engine

// Backing-track support (ROADMAP Phase 3): a decoded audio file — WAV,
// FLAC, or best-effort MP3, decoded to the engine sample rate by
// internal/audiofile — mixed under the synthesized voices, pinned to
// SCORE TIME rather than to the output stream:
//
//	file position (seconds) = Tempos.TimeAt(current tick) + offsetSec
//
// so seeks and loop wraps move the file position exactly with the score,
// and while the position is frozen (paused, count-in, wait mode) or the
// transport is stopped the backing contributes silence — no DC hold, no
// drift. One output frame advances score time by tempoScale/sampleRate
// seconds, so within a segment the file position advances by exactly
// tempoScale samples per frame; at practice scale != 1 the backing is
// therefore resampled by the scale factor with linear interpolation,
// which shifts its pitch. That is the documented Phase 3 limitation (no
// mature pure-Go time-stretch exists) — synthesis remains the primary
// practice path, the backing track is an accompaniment aid.
//
// Reads outside the file (before its start under a negative offset, or
// past its end) are silence. All buffers are preallocated by
// SetBackingTrack (a control call); the render path stays allocation-free.

// SetBackingTrack installs a stereo backing track and its alignment
// offset. left and right must be at the engine's sample rate (the
// project-standard 48 kHz — internal/audiofile.Load produces exactly
// this); if their lengths differ the extra tail of the longer one is
// ignored. offsetSec shifts the file against the score: file position =
// score time + offsetSec, so a negative offset delays the file's start
// past tick 0 and a positive offset skips into the file at tick 0. The
// samples are copied; the caller keeps its slices.
func (e *Engine) SetBackingTrack(left, right []float32, offsetSec float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(left)
	if len(right) < n {
		n = len(right)
	}
	e.backL = append(e.backL[:0], left[:n]...)
	e.backR = append(e.backR[:0], right[:n]...)
	e.backOffset = offsetSec
	e.segValid = false // recompute the backing anchor at the next render
}

// ClearBackingTrack removes the backing track.
func (e *Engine) ClearBackingTrack() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.backL, e.backR = nil, nil
	e.segValid = false
}

// SetBackingGain sets the backing track's mix gain (default 1.0, clamped
// at 0) so the backing can be balanced against the synthesized voices.
func (e *Engine) SetBackingGain(g float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g < 0 {
		g = 0
	}
	e.backGain = g
}

// BackingGain returns the backing track's mix gain.
func (e *Engine) BackingGain() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.backGain
}

// mixBacking adds the backing track into left/right for a run of segment
// frames starting at segFrame. The file position of frame f is
// backBase + f*scale (see buildSegment for the anchor); samples between
// file positions are linearly interpolated, and positions outside the
// file are silent. Called only from processFrames — the one place the
// position advances — with mu held; allocation-free.
func (e *Engine) mixBacking(left, right []float32, segFrame int) {
	n := len(e.backL)
	if n == 0 || e.backGain == 0 {
		return
	}
	g := float32(e.backGain)
	last := float64(n - 1)
	for i := range left {
		p := e.backBase + float64(segFrame+i)*e.scale
		if p < 0 || p > last {
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
