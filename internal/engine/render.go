package engine

import (
	"math"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

type boundaryKind int

const (
	bPlain boundaryKind = iota

	bLoop

	bEnd
)

func (e *Engine) render(left, right []float32) {
	idx := 0
	for idx < len(left) && e.playing && !e.waiting {
		if e.ciBeatsLeft > 0 {
			idx += e.renderCountIn(left[idx:], right[idx:])
			continue
		}
		idx += e.renderSegment(left[idx:], right[idx:])
	}

	if idx < len(left) {
		e.mix(left[idx:], right[idx:])
	}
}

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

		n = e.processFrames(left[:n], right[:n])
	}
	if e.segFrame >= e.segEnd {
		e.handleBoundary()
	}
	return n
}

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

func (e *Engine) frameOf(tick int64) int {
	f := int(math.Floor((float64(tick) - e.anchor) * e.fpt))
	if f < 0 {
		f = 0
	}
	return f
}

func (e *Engine) processFrames(left, right []float32) int {
	n := len(left)
	base := e.segFrame
	cur := 0
	for {

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

			e.mixBacking(left[cur:rel], right[cur:rel], base+cur)
			cur = rel
		}
		if af >= base+n {
			break
		}

		if e.waitMode && !e.waitReleased && e.waitPointAt(af) {
			e.beginWait(af)
			e.segFrame = af

			e.pos = float64(e.waitTick)
			return cur
		}
		e.applyActionsAt(af)
	}
	e.segFrame = base + n
	e.pos = e.anchor + float64(e.segFrame)/e.fpt
	return n
}

func (e *Engine) applyActionsAt(af int) {
	for i := 0; i < len(e.active); {
		a := e.active[i]
		if a.end < e.boundary && e.frameOf(a.end) == af {
			if e.continuedAt(af, i) {

				i++
				continue
			}
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
			e.soundEvent(ev)
		}
		if e.waitReleased && e.releaseOwedTo(ev.Track) {

			e.waitReleased = false
		}
		e.nextEvent++
	}
	if e.metronome && e.nextBeat < e.boundary && e.frameOf(e.nextBeat) == af {
		e.startClick((e.nextBeat-e.meterBase)%e.barLen == 0)
		e.nextBeat += e.beatLen
	}
}

func (e *Engine) soundEvent(ev *score.NoteEvent) {
	artic := e.artic[ev.Track]
	if artic == nil {

		e.voices[ev.Track].NoteOn(ev.Key, ev.Velocity)
		e.active = append(e.active, activeNote{track: ev.Track, key: ev.Key, str: ev.String, end: ev.End})
		return
	}

	spec := synth.NoteSpec{
		Key:      ev.Key,
		Velocity: ev.Velocity,
		Vibrato:  ev.Tech&score.TechVibrato != 0,
	}

	prev := -1
	if attack, ok := continuationAttack(ev.Tech); ok {
		if i := e.activeOn(ev.Track, ev.String); i >= 0 {
			spec.Attack, spec.From, prev = attack, e.active[i].key, i
		}
	}
	continued := true
	if rep := e.articRep[ev.Track]; rep != nil {
		continued = rep.NoteOnSpecReport(spec)
	} else {

		artic.NoteOnSpec(spec)
	}
	if prev >= 0 {
		if !continued {

			e.voices[ev.Track].NoteOff(spec.From)
		}

		e.active[prev].key, e.active[prev].end = ev.Key, ev.End
		return
	}
	e.active = append(e.active, activeNote{track: ev.Track, key: ev.Key, str: ev.String, end: ev.End})
}

func continuationAttack(t score.Technique) (synth.Attack, bool) {
	switch {
	case t&score.TechSlide != 0:
		return synth.AttackSlide, true
	case t&(score.TechHammer|score.TechPull) != 0:
		return synth.AttackLegato, true
	}
	return synth.AttackPluck, false
}

func (e *Engine) activeOn(track, str int) int {
	if str == 0 {
		return -1
	}
	for i := range e.active {
		if a := &e.active[i]; a.track == track && a.str == str {
			return i
		}
	}
	return -1
}

func (e *Engine) continuedAt(af, i int) bool {
	a := &e.active[i]
	if a.str == 0 || e.artic[a.track] == nil {
		return false
	}
	if e.activeOn(a.track, a.str) != i {
		return false
	}
	for ei := e.nextEvent; ei < len(e.events); ei++ {
		ev := &e.events[ei]
		if ev.Start >= e.boundary || e.frameOf(ev.Start) != af {
			break
		}
		if ev.Track != a.track || ev.String != a.str {
			continue
		}
		if _, ok := continuationAttack(ev.Tech); ok {
			return true
		}
	}
	return false
}

func (e *Engine) handleBoundary() {
	e.pos = float64(e.boundary)
	e.segValid = false
	switch e.bKind {
	case bLoop:
		e.allNotesOff()

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

		e.releaseAll()
		e.playing = false
		e.pos = float64(e.scoreEnd)
	case bPlain:

	}
}

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

func (e *Engine) startCountIn() {
	e.ciFPB = e.countInFPB()
	e.ciBeatsLeft = e.countInBeats
	e.ciFrameIn = 0
}

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

func (e *Engine) mix(left, right []float32) {
	for _, v := range e.voices {
		v.Render(left, right)
	}
	e.mixClicks(left, right)
	e.absFrame += int64(len(left))
}

func (e *Engine) allNotesOff() {
	for _, v := range e.voices {
		v.AllNotesOff()
	}
	e.active = e.active[:0]
}

func (e *Engine) releaseAll() {
	for _, a := range e.active {
		e.voices[a.track].NoteOff(a.key)
	}
	e.active = e.active[:0]
}

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

func (e *Engine) setScale(s float64) {
	if math.IsNaN(s) || math.IsInf(s, 0) {
		return
	}
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

		oldFPB := e.ciFPB
		newFPB := e.countInFPB()
		e.ciFrameIn = e.ciFrameIn * newFPB / oldFPB
		if e.ciFrameIn >= newFPB {
			e.ciFrameIn = newFPB - 1
		}
		e.ciFPB = newFPB
	}
}

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
	e.publishPos(usq)
}

func (e *Engine) publishPos(usq int64) {
	rate := 0.0
	if usq > 0 {
		rate = 1e6 * score.PPQ * e.scale / float64(usq)
	}

	advancing := e.playing && !e.waiting && e.ciBeatsLeft == 0

	e.aPosSeq.Add(1)
	e.aPosTick.Store(math.Float64bits(e.pos))
	e.aPosRate.Store(math.Float64bits(rate))
	e.aPosAdv.Store(advancing)
	e.aPosDisc.Store(e.aDiscont.Load())
	e.aPosSeq.Add(1)
}
