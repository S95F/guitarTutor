package engine

import "github.com/S95F/guitarTutor/internal/score"

// Wait mode is the "pause until you play it" practice feature (ROADMAP
// Phase 2): with it enabled, playback halts the position exactly at the
// output frame where a NoteOn of any Role==RoleUser track would fire —
// all events at that tick form one wait point, so a chord is a single
// wait — and holds there until ConfirmWait (in Phase 2, the detector
// hearing the right note) releases it.
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

// WaitingOn returns the wait point's events — every user-track NoteEvent
// at the tick being waited on (one note, or all notes of a chord) — and
// true, or nil and false when not waiting. The slice is a fresh copy owned
// by the caller.
func (e *Engine) WaitingOn() ([]score.NoteEvent, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.waiting {
		return nil, false
	}
	var evs []score.NoteEvent
	for i := e.nextEvent; i < len(e.events); i++ {
		ev := e.events[i]
		if ev.Start > e.waitTick {
			break
		}
		if ev.Start == e.waitTick && e.userTrack[ev.Track] {
			evs = append(evs, ev)
		}
	}
	return evs, true
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
		if e.userTrack[ev.Track] {
			return true
		}
	}
	return false
}

// beginWait enters the waiting state at segment frame af, recording the
// wait point's tick (the first pending user event on that frame) for
// WaitingOn. Caller holds mu and has established waitPointAt(af).
func (e *Engine) beginWait(af int) {
	e.waiting = true
	for i := e.nextEvent; i < len(e.events); i++ {
		ev := &e.events[i]
		if ev.Start >= e.boundary || e.frameOf(ev.Start) != af {
			break
		}
		if e.userTrack[ev.Track] {
			e.waitTick = ev.Start
			break
		}
	}
}
