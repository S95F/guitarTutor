package engine

import (
	"math"

	"github.com/S95F/guitarTutor/internal/score"
)

// A boundaryKind says what happens when a segment's end tick is reached.
type boundaryKind int

const (
	// bPlain is a tempo- or meter-map change: the next segment simply
	// rebuilds with the new conversion.
	bPlain boundaryKind = iota
	// bLoop is the loop end point B: playback wraps to A.
	bLoop
	// bEnd is the score end: the transport stops.
	bEnd
)

// render fills left/right (already zeroed) while the transport runs,
// alternating between count-in and segment rendering. Caller holds mu.
func (e *Engine) render(left, right []float32) {
	idx := 0
	for idx < len(left) && e.playing && !e.waiting {
		if e.ciBeatsLeft > 0 {
			idx += e.renderCountIn(left[idx:], right[idx:])
			continue
		}
		idx += e.renderSegment(left[idx:], right[idx:])
	}
	// Transport stopped (score end), halted at a wait point, or never
	// started: keep mixing so voice release tails ring out naturally
	// instead of being cut. Paused voices contribute silence (Pause sends
	// AllNotesOff).
	if idx < len(left) {
		e.mix(left[idx:], right[idx:])
	}
}

// renderSegment renders up to len(left) frames of the current segment and
// handles the segment boundary when it is reached. It may return 0 when the
// position sits exactly on a boundary (the boundary handler then advances
// state so the render loop makes progress) or exactly on a wait point (the
// waiting flag then breaks the render loop).
func (e *Engine) renderSegment(left, right []float32) int {
	if !e.segValid {
		e.buildSegment()
	}
	n := e.segEnd - e.segFrame
	if n > len(left) {
		n = len(left)
	}
	if n < 0 {
		n = 0
	}
	if n > 0 {
		// A wait point inside the span stops consumption early: n shrinks
		// to the frames actually produced before the position froze.
		n = e.processFrames(left[:n], right[:n])
	}
	if e.segFrame >= e.segEnd {
		e.handleBoundary()
	}
	return n
}

// buildSegment computes the current segment: the span from the current
// position to the nearest tick that changes the frames-per-tick conversion
// or the event stream (tempo change, meter change, loop end, score end).
//
// The segment is anchored at an exact position and all frame numbers within
// it are computed against that anchor, so where render-block boundaries fall
// never shifts an event by even one frame — the property that makes loop
// passes repeat with an exact frame period.
func (e *Engine) buildSegment() {
	pos := e.pos
	tick := int64(math.Floor(pos))
	usq := e.sc.Tempos.At(tick)
	e.fpt = float64(e.sampleRate) * float64(usq) / (1e6 * score.PPQ * e.scale)

	boundary := e.scoreEnd
	kind := bEnd
	for _, t := range e.sc.Tempos {
		if float64(t.Tick) > pos {
			if t.Tick < boundary {
				boundary, kind = t.Tick, bPlain
			}
			break
		}
	}
	for _, m := range e.sc.Meters {
		if float64(m.Tick) > pos {
			if m.Tick < boundary {
				boundary, kind = m.Tick, bPlain
			}
			break
		}
	}
	// >= pos, not > pos: a position parked exactly on B — the timeline
	// scrubbed to the loop end, the B edge dragged onto a beat-parked
	// playhead, or the B key pressed after playback ran out — must engage
	// the loop, not sail past it to the score end with the loop still
	// drawn as armed (audit E1). The zero-length segment this builds
	// renders nothing and falls straight into handleBoundary's wrap to A.
	// A position strictly PAST B stays out on purpose: a loop region
	// behind the playhead is dormant until a seek re-enters it.
	if e.loopOn && float64(e.loopB) >= pos && e.loopB <= boundary {
		boundary, kind = e.loopB, bLoop
	}
	e.boundary = boundary
	e.bKind = kind

	e.anchor = pos
	e.segFrame = 0
	e.segEnd = int(math.Ceil((float64(boundary) - pos) * e.fpt))
	if e.segEnd < 0 {
		e.segEnd = 0
	}

	// Anchor the backing track: the file sample position at the segment
	// anchor is score time at the anchor tick plus the alignment offset,
	// in file samples. Computed from the tempo map at every segment build,
	// so seeks and loop wraps land the file position exactly; within the
	// segment (constant tempo) each output frame advances score time by
	// scale/sampleRate seconds, i.e. the file position by scale samples.
	if len(e.backL) > 0 {
		sec := e.sc.Tempos.TimeAt(tick) + (pos-float64(tick))*float64(usq)/(1e6*score.PPQ) + e.backOffset
		e.backBase = sec * float64(e.sampleRate)
	}

	m := e.sc.Meters.At(tick)
	e.beatLen = m.BeatLen()
	e.barLen = int64(m.Num) * e.beatLen
	e.meterBase = m.Tick
	k := int64(math.Ceil((pos - float64(m.Tick)) / float64(e.beatLen)))
	if k < 0 {
		k = 0
	}
	e.nextBeat = m.Tick + k*e.beatLen

	e.segValid = true
}

// frameOf returns the segment frame a tick lands in: the frame whose span
// of ticks [anchor + f/fpt, anchor + (f+1)/fpt) contains it.
func (e *Engine) frameOf(tick int64) int {
	f := int(math.Floor((float64(tick) - e.anchor) * e.fpt))
	if f < 0 {
		f = 0
	}
	return f
}

// processFrames renders up to len(left) frames of the current segment:
// audio is mixed in runs between action frames, and NoteOn/NoteOff/click
// actions fire on the exact frame their tick lands in. It returns the
// frames consumed — len(left), except when a wait point engages, where
// consumption stops exactly at the wait point's frame with its actions
// unfired (see wait.go).
func (e *Engine) processFrames(left, right []float32) int {
	n := len(left)
	base := e.segFrame
	cur := 0
	for {
		// Next action frame: the earliest pending NoteOn, NoteOff, or
		// metronome beat inside the segment; block end as sentinel.
		af := base + n
		if e.nextEvent < len(e.events) {
			if ev := &e.events[e.nextEvent]; ev.Start < e.boundary {
				if f := e.frameOf(ev.Start); f < af {
					af = f
				}
			}
		}
		for i := range e.active {
			if a := &e.active[i]; a.end < e.boundary {
				if f := e.frameOf(a.end); f < af {
					af = f
				}
			}
		}
		if e.metronome && e.nextBeat < e.boundary {
			if f := e.frameOf(e.nextBeat); f < af {
				af = f
			}
		}
		if rel := af - base; rel > cur {
			e.mix(left[cur:rel], right[cur:rel])
			// The backing track sounds only here: processFrames is the one
			// place the position advances, so a frozen position (paused,
			// count-in, waiting, stopped) contributes backing silence.
			e.mixBacking(left[cur:rel], right[cur:rel], base+cur)
			cur = rel
		}
		if af >= base+n {
			break
		}
		// Wait mode: halt exactly at the frame a user-track NoteOn would
		// fire, leaving every action on it (the NoteOns included) unfired
		// until ConfirmWait releases them.
		if e.waitMode && !e.waitReleased && e.waitPointAt(af) {
			e.beginWait(af)
			e.segFrame = af
			// The halted position is the wait tick itself, not the floored
			// frame's reconstruction: whenever (waitTick-anchor)*fpt is not
			// an exact integer, anchor + af/fpt lands fractionally short and
			// PosTick() reports waitTick-1 for the whole wait — the UI then
			// shows the bar BEFORE the note being waited on (audit D2). The
			// snap is sub-frame (under 1/fpt frames) and forward, a
			// relabeling of the same instant rather than a jump, so it is
			// not a discontinuity; and resume arithmetic is frame-based
			// (segFrame is the authority while the segment is valid), so
			// the release still fires on exactly this frame.
			e.pos = float64(e.waitTick)
			return cur
		}
		e.applyActionsAt(af)
	}
	e.segFrame = base + n
	e.pos = e.anchor + float64(e.segFrame)/e.fpt
	return n
}

// applyActionsAt fires every pending action that lands on segment frame af:
// note-offs first, then note-ons, then the metronome beat.
func (e *Engine) applyActionsAt(af int) {
	for i := 0; i < len(e.active); {
		a := e.active[i]
		if a.end < e.boundary && e.frameOf(a.end) == af {
			e.voices[a.track].NoteOff(a.key)
			last := len(e.active) - 1
			e.active[i] = e.active[last]
			e.active = e.active[:last]
			continue
		}
		i++
	}
	for e.nextEvent < len(e.events) {
		ev := &e.events[e.nextEvent]
		if ev.Start >= e.boundary || e.frameOf(ev.Start) != af {
			break
		}
		if e.tap != nil {
			e.tap(*ev, e.absFrame)
		}
		if !e.muted[ev.Track] {
			e.voices[ev.Track].NoteOn(ev.Key, ev.Velocity)
			e.active = append(e.active, activeNote{track: ev.Track, key: ev.Key, end: ev.End})
		}
		if e.waitReleased && e.userTrack[ev.Track] {
			// The released wait's held events are firing now: consume the
			// release so the next user NoteOn waits anew.
			e.waitReleased = false
		}
		e.nextEvent++
	}
	if e.metronome && e.nextBeat < e.boundary && e.frameOf(e.nextBeat) == af {
		e.startClick((e.nextBeat-e.meterBase)%e.barLen == 0)
		e.nextBeat += e.beatLen
	}
}

// handleBoundary runs when the position reaches the segment's end tick.
// The position snaps to the exact boundary tick, killing any accumulated
// float error, before the boundary's effect is applied.
func (e *Engine) handleBoundary() {
	e.pos = float64(e.boundary)
	e.segValid = false
	switch e.bKind {
	case bLoop:
		e.allNotesOff()
		// A degenerate wrap — the position was parked exactly on B by a
		// seek or a loop edit, so the "pass" played zero frames — must
		// not count as a completed pass, and above all must not ramp the
		// tempo: a scrub to the loop end is not an achievement. segEnd==0
		// discriminates exactly, because every natural bLoop segment has
		// segEnd = ceil((B-anchor)*fpt) >= 1 (audit E1).
		if e.segEnd > 0 {
			e.passes++
			e.applyRamp()
		}
		e.pos = float64(e.loopA)
		e.reindexFrom(e.loopA)
		if e.countInEveryPass && e.countInBeats > 0 {
			e.startCountIn()
		}
	case bEnd:
		// Release, don't silence: the final notes ring out their
		// natural decay past the barline (render keeps mixing).
		e.releaseAll()
		e.playing = false
		e.pos = float64(e.scoreEnd)
	case bPlain:
		// Tempo or meter change: the next buildSegment picks it up.
	}
}

// renderCountIn renders count-in click beats; the playback position does
// not advance. Voices still render (release tails), and returns the number
// of frames consumed.
func (e *Engine) renderCountIn(left, right []float32) int {
	n := 0
	for n < len(left) && e.ciBeatsLeft > 0 {
		if e.ciFrameIn == 0 {
			e.startClick(e.ciBeatsLeft == e.countInBeats)
		}
		c := e.ciFPB - e.ciFrameIn
		if c > len(left)-n {
			c = len(left) - n
		}
		e.mix(left[n:n+c], right[n:n+c])
		n += c
		e.ciFrameIn += c
		if e.ciFrameIn == e.ciFPB {
			e.ciFrameIn = 0
			e.ciBeatsLeft--
		}
	}
	return n
}

// startCountIn arms a count-in at the effective tempo and the meter in
// effect at the current position. Caller holds mu.
func (e *Engine) startCountIn() {
	e.ciFPB = e.countInFPB()
	e.ciBeatsLeft = e.countInBeats
	e.ciFrameIn = 0
}

// countInFPB returns the frames per count-in beat at the effective tempo
// and the meter in effect at the current position. Caller holds mu.
func (e *Engine) countInFPB() int {
	tick := int64(math.Floor(e.pos))
	usq := e.sc.Tempos.At(tick)
	fpt := float64(e.sampleRate) * float64(usq) / (1e6 * score.PPQ * e.scale)
	fpb := int(math.Round(float64(e.sc.Meters.At(tick).BeatLen()) * fpt))
	if fpb < 1 {
		fpb = 1
	}
	return fpb
}

// mix adds every track voice and the sounding clicks into left/right, and
// advances the absolute output-frame clock — every produced frame passes
// through here (segments, count-ins, and stopped-transport tails alike),
// so at any action point absFrame is exactly the output frame the action
// sounds on. Muted tracks still render — they are silent because they
// receive no NoteOns (and got AllNotesOff when muted) — so voice state
// stays warm for an unmute mid-play.
func (e *Engine) mix(left, right []float32) {
	for _, v := range e.voices {
		v.Render(left, right)
	}
	e.mixClicks(left, right)
	e.absFrame += int64(len(left))
}

// allNotesOff silences every voice immediately and forgets pending
// note-offs. Caller holds mu.
func (e *Engine) allNotesOff() {
	for _, v := range e.voices {
		v.AllNotesOff()
	}
	e.active = e.active[:0]
}

// releaseAll sends NoteOff to every still-active note — natural release,
// unlike allNotesOff's immediate silence — and forgets them. Caller holds mu.
func (e *Engine) releaseAll() {
	for _, a := range e.active {
		e.voices[a.track].NoteOff(a.key)
	}
	e.active = e.active[:0]
}

// reindexFrom binary-searches the sorted event list for the first event
// starting at or after tick. Caller holds mu.
func (e *Engine) reindexFrom(tick int64) {
	lo, hi := 0, len(e.events)
	for lo < hi {
		mid := (lo + hi) / 2
		if e.events[mid].Start < tick {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	e.nextEvent = lo
}

// applyRamp raises the tempo scale by the ramp increment, capped at the
// ramp target. Called after each completed loop pass; caller holds mu.
func (e *Engine) applyRamp() {
	if !e.ramp.Enabled || e.ramp.Increment <= 0 {
		return
	}
	s := e.scale + e.ramp.Increment
	if s > e.ramp.Target {
		s = e.ramp.Target
	}
	if s > e.scale {
		e.setScale(s)
	}
}

// setScale sets the tempo scale, clamped to [minTempoScale, maxTempoScale],
// and invalidates the current segment. Caller holds mu.
func (e *Engine) setScale(s float64) {
	if s < minTempoScale {
		s = minTempoScale
	}
	if s > maxTempoScale {
		s = maxTempoScale
	}
	if s == e.scale {
		return
	}
	e.scale = s
	e.segValid = false
	if e.ciBeatsLeft > 0 {
		// Retime an active count-in so the remaining beats sound at the
		// new tempo: keep the elapsed fraction of the current beat.
		oldFPB := e.ciFPB
		newFPB := e.countInFPB()
		e.ciFrameIn = e.ciFrameIn * newFPB / oldFPB
		if e.ciFrameIn >= newFPB {
			e.ciFrameIn = newFPB - 1
		}
		e.ciFPB = newFPB
	}
}

// publish refreshes the lock-free state snapshots. Caller holds mu.
func (e *Engine) publish() {
	e.aPos.Store(int64(math.Floor(e.pos)))
	e.aPlaying.Store(e.playing)
	e.aPasses.Store(int64(e.passes))
	usq := e.sc.Tempos.At(int64(math.Floor(e.pos)))
	e.aBPM.Store(math.Float64bits(60e6 / float64(usq) * e.scale))
	e.aCiOn.Store(e.playing && e.ciBeatsLeft > 0)
	e.aCiLeft.Store(int64(e.ciBeatsLeft))
	e.aFrames.Store(e.absFrame)
	e.aWaiting.Store(e.waiting)
}
