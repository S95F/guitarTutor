package engine

import "github.com/S95F/musicTutor/internal/score"

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

func (e *Engine) Waiting() bool { return e.aWaiting.Load() }

func (e *Engine) WaitGeneration() uint64 { return e.aWaitGen.Load() }

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

	return evs, e.aWaitGen.Load(), true
}

func (e *Engine) SetWaitTrack(track int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 0 || track >= len(e.userTrack) {
		track = -1
	}
	e.waitTrack = track
}

func (e *Engine) releaseOwedTo(track int) bool {
	if e.waitRelTrack >= 0 {
		return track == e.waitRelTrack
	}
	return e.userTrack[track]
}

func (e *Engine) waitsOn(track int) bool {
	if e.waitTrack >= 0 {
		return track == e.waitTrack
	}
	return e.userTrack[track]
}

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

func (e *Engine) clearWait() {
	e.waiting = false
	e.waitReleased = false
	e.waitRelTrack = -1
}

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

			e.waitRelTrack = ev.Track
			break
		}
	}
}
