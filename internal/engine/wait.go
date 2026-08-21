package engine

import "github.com/S95F/musicTutor/internal/score"

// Wait mode is the "pause until you play it" practice feature (ROADMAP
// Phase 2): with it enabled, playback halts the position exactly at the
// output frame where a NoteOn of a waiting track would fire — all events
// at that tick form one wait point, so a chord is a single wait — and
// holds there until ConfirmWait (in Phase 2, the detector hearing the
// right note) releases it. The waiting track is every Role==RoleUser
// track by default, or the single track SetWaitTrack names, which is what
// a practicing caller should set.
//
// While waiting the position is frozen: voices keep rendering so release
// tails ring out, the metronome is silent (no beats pass), count-in state
// is untouched, and backing-track events do not advance. The wait point's
// events — including any other action sharing its frame — do not fire on
// arrival; they fire on the exact frame the release is rendered, so the
// event tap reports the true sounding frame. A muted user track still
// waits: wait mode follows the musical part, like the tap.
//
// Controls that move or stop the position (SeekTick, Pause, SetLoop,
// ClearLoop) clear an active wait without firing its held events; wait
// mode stays enabled and re-arms at the next user NoteOn the position
// reaches. With looping on, each pass waits again at the first user note
// after the wrap.

// SetWaitMode enables or disables wait mode. Disabling it while a wait is
// active releases the wait immediately, exactly as ConfirmWait would: the
// held NoteOns fire on the next rendered frame.
func (e *Engine) SetWaitMode(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if on == e.waitMode {
		return
	}
	e.waitMode = on
	if !on && e.waiting {
		e.waiting = false
		e.waitReleased = true
	}
	e.publish()
}

// Waiting reports whether playback is halted at a wait point. Like the
// other state queries it is a lock-free snapshot, safe from any goroutine.
func (e *Engine) Waiting() bool { return e.aWaiting.Load() }

// WaitGeneration returns a counter that increments every time a wait
// engages. Two distinct waits can share a tick — a seek clears a wait and
// the position re-waits at the same spot, or a loop wrap returns to the
// same first note — and the wait's events alone cannot tell them apart.
// Wiring that accumulates per-wait progress (the practice WaitGate)
// snapshots the generation when it arms and re-arms whenever it changes,
// so no stale progress leaks across waits. Lock-free snapshot, safe from
// any goroutine.
//
// Prefer WaitingOn, which returns the generation ALONGSIDE the events it
// belongs to. Reading the two separately is a race: the render thread can
// release one wait and engage the next in between, and wiring that arms
// with the old events under the new generation waits forever for notes
// the position has already passed (bug review W1). This accessor remains
// for callers that only want to notice change, never to pair with events
// fetched by a second call.
func (e *Engine) WaitGeneration() uint64 { return e.aWaitGen.Load() }

// WaitingOn returns the wait point's events — every waiting-track
// NoteEvent at the tick being waited on (one note, or all notes of a
// chord) — the generation those events belong to, and true; or nil, 0 and
// false when not waiting. The slice is a fresh copy owned by the caller.
//
// Events and generation come from one locked snapshot deliberately: they
// are the two halves of "which wait is this, and what does it want", and
// a caller that fetched them separately could pair the events of the wait
// that just ended with the generation of the one that just began. See
// WaitGeneration.
func (e *Engine) WaitingOn() ([]score.NoteEvent, uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.waiting {
		return nil, 0, false
	}
	var evs []score.NoteEvent
	for i := e.nextEvent; i < len(e.events); i++ {
		ev := e.events[i]
		if ev.Start > e.waitTick {
			break
		}
		if ev.Start == e.waitTick && e.waitsOn(ev.Track) {
			evs = append(evs, ev)
		}
	}
	// aWaitGen is only ever bumped by beginWait, which runs under mu, so
	// this load sees the generation these events were gathered under.
	return evs, e.aWaitGen.Load(), true
}

// SetWaitTrack restricts wait mode to one track's notes. Pass -1 (the
// default) to wait at every user track's notes.
//
// Waiting on "any user track" is wrong as soon as more than one track is
// RoleUser — a hand-written .gtab may mark several — because only ONE
// track is scored and drawn. The engine would halt at a note the scorer
// was never told to expect, the wait gate could not be satisfied, and
// playback stopped for good. The same mismatch appears through `play
// -track n`, which chooses the scored and displayed track directly. The
// practicing caller names that track here so the halt and the expectation
// can never describe different music (bug review W2/W3).
//
// A wait already engaged is left alone: it is honoured under the rules it
// engaged with, and the next wait follows the new track.
func (e *Engine) SetWaitTrack(track int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 0 || track >= len(e.userTrack) {
		track = -1
	}
	e.waitTrack = track
}

// releaseOwedTo reports whether an event on this track consumes the
// pending release. It is the track the wait ENGAGED on, not whatever
// waitsOn would say now — see beginWait. Caller holds mu.
func (e *Engine) releaseOwedTo(track int) bool {
	if e.waitRelTrack >= 0 {
		return track == e.waitRelTrack
	}
	return e.userTrack[track]
}

// waitsOn reports whether a track's NoteOns are wait points. Every part of
// wait mode — arming, the event list, and consuming the release — routes
// through here, so they cannot disagree about which notes a wait is about.
// Caller holds mu.
func (e *Engine) waitsOn(track int) bool {
	if e.waitTrack >= 0 {
		return track == e.waitTrack
	}
	return e.userTrack[track]
}

// ConfirmWait releases the current wait: playback continues from the exact
// frozen position, and the held NoteOns fire on the release frame. No-op
// when not waiting.
func (e *Engine) ConfirmWait() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.waiting {
		return
	}
	e.waiting = false
	e.waitReleased = true
	e.publish()
}

// clearWait forgets any active wait without firing its held events: the
// control that called it moved or stopped the position, so the held events
// no longer describe the next thing to play. A pending release
// (ConfirmWait not yet rendered) is dropped too. Wait mode itself stays
// enabled. Caller holds mu.
func (e *Engine) clearWait() {
	e.waiting = false
	e.waitReleased = false
	e.waitRelTrack = -1
}

// waitPointAt reports whether a pending NoteOn of a user track lands on
// segment frame af — the definition of a wait point. Events are sorted by
// start tick and af is never past the earliest pending event, so the scan
// stops at the first event beyond the frame. Caller holds mu.
func (e *Engine) waitPointAt(af int) bool {
	for i := e.nextEvent; i < len(e.events); i++ {
		ev := &e.events[i]
		if ev.Start >= e.boundary || e.frameOf(ev.Start) != af {
			break
		}
		if e.waitsOn(ev.Track) {
			return true
		}
	}
	return false
}

// beginWait enters the waiting state at segment frame af, recording the
// wait point's tick (the first pending user event on that frame) for
// WaitingOn, and bumps the wait generation (see WaitGeneration). Caller
// holds mu and has established waitPointAt(af).
func (e *Engine) beginWait(af int) {
	e.waiting = true
	e.aWaitGen.Add(1)
	for i := e.nextEvent; i < len(e.events); i++ {
		ev := &e.events[i]
		if ev.Start >= e.boundary || e.frameOf(ev.Start) != af {
			break
		}
		if e.waitsOn(ev.Track) {
			e.waitTick = ev.Start
			// Remember whose release this will be. Consuming it by asking
			// waitsOn again would ask the CURRENT setting, and
			// SetWaitTrack between the confirm and the release frame then
			// left the release owed to a track no longer waited on: it
			// was never consumed there, and was instead spent suppressing
			// the NEXT wait point, which the player silently sailed
			// through. A wait is honoured under the rules it engaged
			// with.
			e.waitRelTrack = ev.Track
			break
		}
	}
}
