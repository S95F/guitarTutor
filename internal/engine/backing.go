package engine

import "math"

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
//
// Every float64 that enters here from a caller — the alignment offset and
// the mix gain — is checked for finiteness in its setter, because NaN
// defeats ordinary bounds tests: every comparison involving NaN is false,
// so `if g < 0` and `if p < 0 || p > last` both accept it. A NaN that
// reached the render loop was fatal in two different ways (an out-of-range
// index panic on the audio thread, and a mix of nothing but NaN), so the
// engine stores only finite values and the render loop's own guards are
// written in the NaN-rejecting form as a second line of defence.

// SetBackingTrack installs a stereo backing track and its alignment
// offset. left and right must be at the engine's sample rate (the
// project-standard 48 kHz — internal/audiofile.Load produces exactly
// this); if their lengths differ the extra tail of the longer one is
// ignored. offsetSec shifts the file against the score: file position =
// score time + offsetSec, so a negative offset delays the file's start
// past tick 0 and a positive offset skips into the file at tick 0. A
// non-finite offset is not an alignment and is refused (treated as 0);
// see below. The samples are copied; the caller keeps its slices.
func (e *Engine) SetBackingTrack(left, right []float32, offsetSec float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(left)
	if len(right) < n {
		n = len(right)
	}
	e.backL = append(e.backL[:0], left[:n]...)
	e.backR = append(e.backR[:0], right[:n]...)
	// Silence any non-finite sample once, here, rather than trusting every
	// producer. mixBacking does `left[i] += gain * backL[j]`, so a single
	// NaN in the file turns the ENTIRE mix non-finite from that frame on,
	// and the live path writes float32 straight to the device — a
	// full-scale rail in the player's headphones. internal/audiofile
	// already scrubs what it decodes, but this is an exported entry point
	// that any caller can reach with any slice, and the cost is one pass
	// at load time against a failure on the realtime thread.
	silenceNonFinite(e.backL)
	silenceNonFinite(e.backR)
	// The offset feeds backBase in buildSegment (backBase = (score time +
	// offset) * sampleRate), and backBase feeds every file position the
	// render loop computes. Storing NaN here made every backing read a NaN
	// position, which fell through the loop's bounds test, indexed backL
	// at int(NaN) — MinInt64 on amd64 — and panicked the audio thread;
	// ±Inf pins the position permanently outside the file, i.e. a backing
	// track that is silently never heard. Neither is an alignment a user
	// can mean, so both are refused at this boundary instead of leaving
	// the realtime path as the only line of defence.
	if math.IsNaN(offsetSec) || math.IsInf(offsetSec, 0) {
		offsetSec = 0
	}
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

// SetBackingGain sets the backing track's mix gain (default 1.0) so the
// backing can be balanced against the synthesized voices. A gain that is
// not a usable volume — negative, or non-finite — is refused and stored
// as 0 (silence), the only safe answer for input that names no volume.
func (e *Engine) SetBackingGain(g float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// The old clamp was `if g < 0 { g = 0 }`, which is false for NaN, so a
	// NaN gain was stored; mixBacking's `backGain == 0` early-out is false
	// for NaN too, so every backing sample became NaN and poisoned the
	// entire mix — a render came out as eight seconds of full-scale noise
	// (every sample at the -1.0 rail) into the user's headphones. +Inf is
	// the same hazard by a different route (Inf*sample = ±Inf, which
	// saturates to a rail), so finiteness is checked, not just the sign.
	// A CLI or UI is free to reject the value earlier with a message; the
	// engine must never store a gain it cannot multiply by.
	if math.IsNaN(g) || math.IsInf(g, 0) || g < 0 {
		g = 0
	}
	e.backGain = g
}

// mixBacking adds the backing track into left/right for a run of segment
// frames starting at segFrame. The file position of frame f is
// backBase + f*scale (see buildSegment for the anchor); samples between
// file positions are linearly interpolated, and positions outside the
// file — or not a number at all — are silent. Called only from
// processFrames — the one place the position advances — with mu held;
// allocation-free.
//
// Both tests below are written as negated inclusive ranges rather than as
// the direct rejections they replaced (`backGain == 0`, `p < 0 || p >
// last`). Every comparison involving NaN is false, so the direct forms
// ACCEPTED NaN: a NaN position fell through to j := int(p), which is
// MinInt64 on amd64, and indexed backL out of range — a panic raised on
// the audio thread, which the realtime contract forbids outright.
// !(p >= 0 && p <= last) is exactly right here: for any real p it is
// identical to the old test, and the two remaining non-finite positions
// belong on the reject side anyway (+Inf is past the end, -Inf is before
// the start). The setters already refuse a non-finite offset and gain,
// but scale reaches this loop from SetTempoScale, whose clamp has the
// same NaN blindness, so this guard is load-bearing rather than merely
// redundant.
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

// silenceNonFinite replaces every NaN and infinity in s with silence.
// A sample that is not a number names no amplitude, and the mixer has no
// way to carry one without poisoning everything downstream of it.
func silenceNonFinite(s []float32) {
	for i, v := range s {
		if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
			s[i] = 0
		}
	}
}
