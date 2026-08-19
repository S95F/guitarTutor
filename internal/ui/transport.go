package ui

// The practice view's chrome: the transport bar, the toggle chips, the
// track strip and the piece timeline — everything around the tab that the
// mouse can reach.
//
// Every rectangle is computed once, by layout(), and both Update and Draw
// read the result. That is the load-bearing decision in this file: a hit
// region derived separately from the rectangle it is drawn as goes out of
// true the moment either one moves, and the bug is invisible — the
// control simply stops answering a click a few pixels from its edge. One
// layout, two readers.
//
// Nothing here reads Ebitengine. The mouse arrives as a pointer value
// (pointer.go) and every decision is a plain method, so a test can click
// anything without opening a window.

import (
	"fmt"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/guitarTutor/internal/score"
)

// Vertical layout of the practice view. The tab sits low enough to leave
// the top of the window for controls, and the timeline sits directly
// under the tab because they say the same thing at two scales — where you
// are in the bar, and where you are in the piece.
const (
	ptTransportY = 58.0  // transport buttons and toggle chips, below the header rule
	ptTracksY    = 100.0 // track strip, live meter on the right
	ptWarnY      = 160.0 // live-warning banner (overlay)
	ptWarnH      = 56.0
	ptTimelineH  = 36.0

	// ptTabHeadroom is the margin above the first string that belongs to
	// the tab: bar numbers, the loop edges and the playhead all reach
	// into it, and it is part of the tab's clickable area so that a click
	// on a bar number seeks like a click on the string below it.
	ptTabHeadroom = 34.0
	// ptTabFooting is the matching margin below the last string.
	ptTabFooting = 26.0

	ptBtnW = 36.0 // transport button
	// ptBtnH matches chipH: the buttons and the toggle chips are one
	// visual row, and a 2px difference in a row of rounded panels reads
	// as a rendering fault rather than a design.
	ptBtnH   = chipH
	ptBtnGap = 6.0

	// ptGrab is how far either side of a loop edge counts as grabbing it.
	// The edge is drawn two pixels wide; a two-pixel target is not one a
	// mouse can hit.
	ptGrab = 6.0

	// ptTrackChipW is the fixed width of a track chip, so the strip does
	// not reflow as names change length.
	ptTrackChipW = 150.0
)

// dragTarget is what the mouse is currently moving. A drag is owned for
// as long as the button is held, so leaving the control's rectangle
// mid-gesture keeps dragging it rather than dropping it — which is what
// makes scrubbing past the end of the timeline behave.
type dragTarget int

const (
	dragNone dragTarget = iota
	dragSeekTab
	dragSeekTimeline
	dragLoopA
	dragLoopB
	// dragLoopNew is the shift+drag that creates a loop in one gesture:
	// the press anchors one edge and the drag carries the other. Without
	// it a mouse user needed three gestures — click LOOP, drag A, drag B
	// — for one intent.
	dragLoopNew
)

// A practiceChip is one toggle in the transport row. The table drives the
// drawing, the hit testing and the click action together, so a chip
// cannot end up drawn in one place and clickable in another.
type practiceChip struct {
	label string
	// key is the binding drawn under the label. The mouse is an addition
	// here, not a replacement: every chip teaches its own shortcut.
	key string
	// on reports whether the toggle is engaged; nil means it is an action
	// rather than a state (the tempo entry, the help overlay).
	on func(a *App) bool
	// disabled reports whether the chip cannot be used right now. nil
	// means always usable.
	disabled func(a *App) bool
	// whenOff is the transient line a click on the disabled chip posts.
	// The click is still swallowed — nothing behind the chip may take it
	// — but a greyed control that swallows it in silence reads as broken,
	// while a sentence naming what would enable it turns the dead click
	// into the answer. A func, because the right remedy depends on how
	// the window was started (see liveRemedy).
	whenOff func(a *App) string
	// act is what a click does.
	act func(a *App)
}

// practiceChips is the transport row, left to right. The order matches
// how often a practising guitarist reaches for each.
var practiceChips = []practiceChip{
	{label: "LOOP", key: "A B L",
		on:  func(a *App) bool { _, _, on := a.eng.Loop(); return on },
		act: func(a *App) { a.toggleLoop() }},
	{label: "CLICK", key: "M",
		on:  func(a *App) bool { return a.metronome },
		act: func(a *App) { a.toggleMetronome() }},
	{label: "RAMP", key: "R",
		on:  func(a *App) bool { return a.ramp },
		act: func(a *App) { a.toggleRamp() }},
	{label: "COUNT-IN", key: "C",
		on:  func(a *App) bool { return a.CountInBeats() > 0 },
		act: func(a *App) { a.toggleCountIn() }},
	{label: "WAIT", key: "W",
		on:       func(a *App) bool { return a.wait },
		disabled: func(a *App) bool { return !a.waitCtl },
		whenOff:  func(a *App) string { return "wait mode needs live input — " + a.liveRemedy() },
		act:      func(a *App) { a.toggleWait() }},
	{label: "TUNER", key: "T",
		on:  func(a *App) bool { return a.tunerView },
		act: func(a *App) { a.tunerView = !a.tunerView }},
	{label: "BPM", key: "shift+B",
		act: func(a *App) { a.openBPMEntry() }},
	{label: "SETTINGS", key: "S",
		disabled: func(a *App) bool { return a.settings == nil },
		whenOff:  func(a *App) string { return "settings need the full app — start guitarTutor without a file" },
		act:      func(a *App) { a.openSettings() }},
	{label: "HELP", key: "?",
		on:  func(a *App) bool { return a.helpOpen },
		act: func(a *App) { a.openHelp() }},
}

// chipStates projects the chip table into what the drawing code needs.
func (a *App) chipStates() []chipState {
	out := make([]chipState, len(practiceChips))
	for i, c := range practiceChips {
		out[i] = chipState{
			label:    c.label,
			key:      c.key,
			on:       c.on != nil && c.on(a),
			disabled: c.disabled != nil && c.disabled(a),
		}
	}
	return out
}

// A practiceLayout is every rectangle of the practice view's chrome.
type practiceLayout struct {
	// transport is to-start, previous bar, play/pause, next bar.
	transport [4]rect
	// chips is parallel to practiceChips.
	chips []rect
	// tracks is parallel to a.sc.Tracks, truncated to those that fit; a
	// track with no room gets the zero rect and is not clickable.
	tracks []rect
	// tab is the scrolling tablature, clickable to seek.
	tab rect
	// timeline is the whole-piece strip.
	timeline rect
	// loopA and loopB are grab handles on the tab's loop edges, empty
	// when no loop is set or the edge is off screen.
	loopA, loopB rect
	// tlLoopA and tlLoopB are the same handles on the timeline.
	tlLoopA, tlLoopB rect
}

// tabBottom is the y of the last string line. Everything below the tab
// is positioned from it rather than from a constant, because the string
// count comes from the piece: a bass has four and an extended-range
// guitar has eight, and a fixed timeline would collide with one of them.
func (a *App) tabBottom() float64 {
	return float64(tabTop + (len(a.displayed().Tuning)-1)*stringGap)
}

// tabRect is the tab's clickable area: the string block plus the margins
// above and below where the bar numbers, loop edges and playhead live.
func (a *App) tabRect() rect {
	top := tabTop - ptTabHeadroom
	return rect{0, top, screenW, a.tabBottom() + ptTabFooting - top}
}

// stateChipY is where the count-in and waiting indicators sit: under the
// tab, at the playhead. They never appear together — one is before
// playback and the other during it. It is the one thing below the tab
// that follows the string count, because it has to stay attached to the
// notation it is about.
func (a *App) stateChipY() float64 { return a.tabBottom() + 34 }

// The timeline group is anchored to the bottom of the window rather than
// hung under the tab. A staff is 130 pixels tall on a 720 pixel window,
// so something has to absorb the slack: putting it between the notation
// and the timeline reads as room around the staff, while leaving it under
// the timeline reads as an unfinished screen. Anchoring also means a bass
// and an eight-string do not move the controls the user reaches for.
const (
	ptCaptionY  = screenH - 158.0 // "bar 12 of 48   0:24 / 1:52"
	ptTimelineY = screenH - 134.0 // piece timeline strip
	ptLegendY   = screenH - 86.0  // verdict legend, live sessions only
	ptMsgY      = screenH - 64.0  // transient messages
)

// captionY is the "bar 12 of 48" line above the timeline.
func (a *App) captionY() float64 { return ptCaptionY }

// timelineY is the top of the piece timeline.
func (a *App) timelineY() float64 { return ptTimelineY }

// legendY is the verdict legend, drawn only in a live session.
func (a *App) legendY() float64 { return ptLegendY }

// msgY is the transient message line (the tempo-entry result, the
// reload offer).
func (a *App) msgY() float64 { return ptMsgY }

// layout computes every rectangle of the chrome. It is called once per
// Update and once per Draw; it reads state but changes none.
func (a *App) layout() practiceLayout {
	var l practiceLayout

	x := uiPadX
	for i := range l.transport {
		l.transport[i] = rect{x, ptTransportY, ptBtnW, ptBtnH}
		x += ptBtnW + ptBtnGap
	}
	x += 18 // a gap between "move the playhead" and "change the session"

	states := a.chipStates()
	l.chips = make([]rect, len(states))
	for i, c := range states {
		w := chipW(c)
		l.chips[i] = rect{x, ptTransportY, w, chipH}
		x += w + chipGap
	}

	l.tracks = make([]rect, len(a.sc.Tracks))
	tx := uiPadX
	for i := range a.sc.Tracks {
		// The live block owns the right end of this row; a track that
		// would run under it is left without a rectangle rather than
		// drawn where a click cannot reach it.
		if tx+ptTrackChipW > a.trackStripRight() {
			break
		}
		l.tracks[i] = rect{tx, ptTracksY, ptTrackChipW, chipH}
		tx += ptTrackChipW + chipGap
	}

	l.tab = a.tabRect()
	l.timeline = rect{uiPadX, a.timelineY(), screenW - 2*uiPadX, ptTimelineH}

	if la, lb, on := a.eng.Loop(); on {
		// The tab handles exist only while the tab itself is on screen:
		// in the tuner view the loop edges are not drawn, and an
		// invisible handle that still grabbed clicks let a click on the
		// cents bar silently move a loop end (audit A3). The timeline is
		// drawn in both views, so its handles stay.
		if !a.tunerView {
			l.loopA = a.loopHandleOnTab(la)
			l.loopB = a.loopHandleOnTab(lb)
		}
		l.tlLoopA = a.loopHandleOnTimeline(l.timeline, la)
		l.tlLoopB = a.loopHandleOnTimeline(l.timeline, lb)
	}
	return l
}

// trackStripRight is where the track strip must stop: short of the live
// meter when there is one, otherwise the page margin.
func (a *App) trackStripRight() float64 {
	if a.live {
		return screenW - uiPadX - 260
	}
	return screenW - uiPadX
}

// loopHandleOnTab is the grab region for a loop edge at tick, or the
// empty rect when it is off the visible tab.
func (a *App) loopHandleOnTab(tick int64) rect {
	x := a.xAtTick(tick)
	if x < -ptGrab || x > screenW+ptGrab {
		return rect{}
	}
	t := a.tabRect()
	return rect{x - ptGrab, t.y, 2 * ptGrab, t.h}
}

// loopHandleOnTimeline is the grab region for a loop edge on the strip.
func (a *App) loopHandleOnTimeline(tl rect, tick int64) rect {
	x := a.timelineX(tl, tick)
	return rect{x - ptGrab, tl.y, 2 * ptGrab, tl.h}
}

// --- tick and pixel conversions -------------------------------------------

// xAtTick is where a tick sits on the tab this frame. It is measured
// against the DRAWN position, not the engine's published one, so that a hit
// region and the pixels it belongs to agree even mid-glide — the same
// one-layout-two-readers rule this file opens with, applied to time.
func (a *App) xAtTick(tick int64) float64 {
	return screenW*playheadX + (float64(tick)-a.posF())*a.pxPerTick()
}

// tickAtX inverts xAtTick.
func (a *App) tickAtX(x float64) int64 {
	return int64(math.Round(a.posF() + (x-screenW*playheadX)/a.pxPerTick()))
}

// pieceEnd is the last tick of the displayed track — the end of the
// timeline. A track with no bars has no extent, and the timeline then
// reports 1 so nothing divides by zero.
func (a *App) pieceEnd() int64 {
	bars := a.displayed().Bars
	if len(bars) == 0 {
		return 1
	}
	last := bars[len(bars)-1]
	if end := last.Start + last.Len(); end > 0 {
		return end
	}
	return 1
}

// timelineX is where a tick sits on the timeline strip.
func (a *App) timelineX(tl rect, tick int64) float64 { return a.timelineXf(tl, float64(tick)) }

// timelineXf is timelineX for a fractional tick. The strip squeezes a whole
// piece into a few hundred pixels, so a whole-tick playhead there moves in
// jumps of well under a pixel — but it is the same position as the tab's and
// is drawn from the same number, rather than from a second rounding of it.
func (a *App) timelineXf(tl rect, tick float64) float64 {
	f := tick / float64(a.pieceEnd())
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	return tl.x + f*tl.w
}

// timelineTick inverts timelineX, clamped to the piece.
func (a *App) timelineTick(tl rect, x float64) int64 {
	if tl.w <= 0 {
		return 0
	}
	return clampTick((x-tl.x)/tl.w*float64(a.pieceEnd()), a.pieceEnd())
}

// clampTick rounds a fractional tick into [0, end].
func clampTick(t float64, end int64) int64 {
	if t < 0 {
		return 0
	}
	if int64(t) > end {
		return end
	}
	return int64(t)
}

// snapToBeat returns the start of the beat nearest tick in the displayed
// track, so a dragged loop edge lands somewhere musical. A track with no
// beats snaps to bar starts instead, and one with neither is left alone.
func (a *App) snapToBeat(tick int64) int64 {
	best, found := int64(0), false
	consider := func(t int64) {
		if !found || abs64(t-tick) < abs64(best-tick) {
			best, found = t, true
		}
	}
	for _, bar := range a.displayed().Bars {
		consider(bar.Start)
		consider(bar.Start + bar.Len())
		for _, beat := range bar.Beats {
			consider(beat.Start)
		}
	}
	if !found {
		return tick
	}
	return best
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// seekTo moves the playhead, clamped to the piece.
func (a *App) seekTo(tick int64) {
	if tick < 0 {
		tick = 0
	}
	if end := a.pieceEnd(); tick > end {
		tick = end
	}
	if tick != a.eng.PosTick() {
		a.eng.SeekTick(tick)
	}
}

// --- mouse ----------------------------------------------------------------

// handleMouse applies one frame of pointer input. shift is passed in
// rather than read from Ebitengine so the whole gesture set is testable;
// it turns off the loop-edge beat snap for tick-exact placement.
//
// An in-flight drag is handled first and to the exclusion of everything
// else: once a gesture owns the mouse it keeps it until the button comes
// up, so sliding off a control mid-drag does not hand the mouse to
// whatever the cursor has wandered over.
func (a *App) handleMouse(p pointer, shift bool) {
	if a.drag != dragNone {
		// A drag ends the moment the button is up, and it ends without
		// acting. The modal layers and the settings screen can swallow
		// any number of frames mid-gesture, so a gesture that outlived
		// them would otherwise resume on the cursor's current position
		// with no button held and move the loop or the playhead on its
		// own (audit A8).
		if !p.down {
			a.drag = dragNone
			return
		}
		a.continueDrag(p, shift)
		return
	}
	l := a.layout()

	// The wheel zooms the tab only while the tab is the thing on screen.
	// Gated on the rectangle alone, a scroll over the tuner silently
	// rezoomed the tablature hidden behind it, and the change only
	// surfaced on the next press of T.
	if p.wheel != 0 && !a.tunerView && p.over(l.tab) {
		a.wheelAcc += p.wheel
		var steps int
		steps, a.wheelAcc = wheelSteps(a.wheelAcc)
		for ; steps > 0; steps-- {
			a.zoomIn()
		}
		for ; steps < 0; steps++ {
			a.zoomOut()
		}
	}
	if !p.pressed && !p.right {
		return
	}
	if a.hitChrome(p, l) {
		return
	}
	// Only the left button reaches the seek and loop gestures. The right
	// button means exactly one thing — solo a track — and a right press
	// that missed every chip must die here rather than fall through and
	// jump the playhead (audit A4).
	if !p.pressed {
		return
	}
	// Loop edges are tested before the surfaces they sit on, so grabbing
	// an edge never reads as a seek to the tick under it — and before the
	// new-loop gesture, so shift+dragging an existing edge still means
	// tick-exact resizing rather than a second loop.
	switch {
	case p.over(l.loopA) || p.over(l.tlLoopA):
		a.drag = dragLoopA
	case p.over(l.loopB) || p.over(l.tlLoopB):
		a.drag = dragLoopB
	case shift && p.over(l.timeline):
		a.beginLoopDraw(a.timelineTick(l.timeline, p.x), p.x, true)
	case shift && p.over(l.tab) && !a.tunerView:
		a.beginLoopDraw(a.tickAtX(p.x), p.x, false)
	case p.over(l.timeline):
		a.drag = dragSeekTimeline
		a.continueDrag(p, shift)
	case p.over(l.tab) && !a.tunerView:
		a.drag = dragSeekTab
		a.continueDrag(p, shift)
	}
}

// beginLoopDraw starts the shift+drag gesture that creates a loop. The
// press itself only anchors and seeks: committing a loop is deferred until
// the pointer has travelled loopDrawSlop, so a shift-press that never
// really moves stays the plain seek it would have been without shift —
// nobody is punished for a modifier held a moment too long. The seek
// happens up front, exactly as an unshifted press would, which also parks
// the playhead at the section about to be looped.
func (a *App) beginLoopDraw(tick int64, x float64, onTimeline bool) {
	a.drag = dragLoopNew
	a.loopDrawTick, a.loopDrawX, a.loopDrawing = tick, x, false
	a.loopDrawOnTimeline = onTimeline
	// The press-time scale rides the whole gesture: the zoom keys are
	// not gated during a drag, and a rescale mid-gesture would jump the
	// far edge with the mouse stationary.
	a.loopDrawPxPerTick = a.pxPerTick()
	a.seekTo(tick)
}

// loopDrawSlop is how far the pointer must travel before a shift+drag
// means "draw a loop" rather than "seek". A few pixels of hand tremor on
// a click must not leave a surprise loop behind.
const loopDrawSlop = 4.0

// continueLoopDraw advances the new-loop gesture: the loop runs from the
// anchored press tick to the tick under the pointer, both snapped to
// beats. Unlike an edge drag — where shift releases the snap — this whole
// gesture rides on shift, so it cannot also mean tick-exact; drawing is
// coarse placement, and the edges can be refined afterwards. The loop is
// never shorter than a beat, so it appears the moment the slop is crossed
// instead of waiting for the drag to span a beat boundary.
func (a *App) continueLoopDraw(p pointer) {
	if !a.loopDrawing && math.Abs(p.x-a.loopDrawX) <= loopDrawSlop {
		return
	}
	a.loopDrawing = true
	// The whole drag stays in the coordinate system it was anchored in,
	// the way dragSeekTimeline does. Two reasons: the pointer drifting a
	// few pixels off its surface must not flip the loop to the other
	// surface's very different scale mid-gesture — and on the tab the
	// press's own seek recentres the view, so the pressed tick is no
	// longer under the pressed pixel and tickAtX would measure from the
	// playhead column instead of from the press. Press-relative pixels
	// are immune to both.
	var cur int64
	if a.loopDrawOnTimeline {
		cur = a.timelineTick(a.layout().timeline, p.x)
	} else {
		cur = a.loopDrawTick + int64(math.Round((p.x-a.loopDrawX)/a.loopDrawPxPerTick))
	}
	lo, hi := a.loopDrawTick, cur
	if hi < lo {
		lo, hi = hi, lo
	}
	// snapToBeat only answers with bar and beat starts, so both edges land
	// inside the piece; all that is left to guard is the two collapsing
	// onto one tick, which the engine would read as "no loop".
	lo, hi = a.snapToBeat(lo), a.snapToBeat(hi)
	minLen := a.beatLenAt(lo)
	if hi < lo+minLen {
		hi = lo + minLen
	}
	if e := a.pieceEnd(); hi > e {
		hi = e
		if lo > hi-minLen {
			lo = hi - minLen
			if lo < 0 {
				lo = 0
			}
		}
	}
	a.eng.SetLoop(lo, hi)
}

// hitChrome dispatches a press against the buttons, chips and track
// rows, reporting whether one of them took it.
func (a *App) hitChrome(p pointer, l practiceLayout) bool {
	spots := make([]hotspot, 0, len(l.transport)+len(l.chips)+len(l.tracks))
	spots = append(spots,
		hotspot{r: l.transport[0], on: func() { a.seekTo(0) }},
		hotspot{r: l.transport[1], on: a.seekPrevBar},
		hotspot{r: l.transport[2], on: a.togglePlay},
		hotspot{r: l.transport[3], on: a.seekNextBar},
	)
	states := a.chipStates()
	for i := range practiceChips {
		c := practiceChips[i]
		if states[i].disabled {
			// Still a hotspot: a disabled control must swallow the click
			// rather than let it fall through to the tab behind it. But a
			// swallowed click that says nothing reads as a broken chip, so
			// it answers on the transient line with what would enable it.
			whenOff := c.whenOff
			spots = append(spots, hotspot{r: l.chips[i], on: func() {
				if whenOff != nil {
					a.setBPMMessage(whenOff(a))
				}
			}})
			continue
		}
		spots = append(spots, hotspot{r: l.chips[i], on: func() { c.act(a) }})
	}
	for i := range l.tracks {
		i := i
		spots = append(spots, hotspot{
			r:       l.tracks[i],
			on:      func() { a.toggleMute(i) },
			onRight: func() { a.toggleSolo(i) },
		})
	}
	return p.hit(spots)
}

// continueDrag advances whatever gesture owns the mouse, and ends it when
// the button comes up.
func (a *App) continueDrag(p pointer, shift bool) {
	l := a.layout()
	switch a.drag {
	case dragSeekTab:
		a.seekTo(a.tickAtX(p.x))
	case dragSeekTimeline:
		a.seekTo(a.timelineTick(l.timeline, p.x))
	case dragLoopNew:
		a.continueLoopDraw(p)
	case dragLoopA, dragLoopB:
		// The edge follows whichever surface the gesture started over —
		// the timeline when the cursor is on it, the tab otherwise — so a
		// coarse drag and a fine one are the same gesture at two scales.
		tick := a.tickAtX(p.x)
		if p.over(l.timeline) {
			tick = a.timelineTick(l.timeline, p.x)
		}
		if !shift {
			tick = a.snapToBeat(tick)
		}
		a.moveLoopEdge(a.drag, tick)
	}
	// Ending the drag lives in handleMouse's gate, which clears it on the
	// release frame before any action — not here, where the stale position
	// would already have been acted on.
}

// moveLoopEdge drags one end of the loop to tick, keeping the other where
// it is and refusing to push it past its partner: a loop whose end
// precedes its start is not a loop, and the engine would reject it.
func (a *App) moveLoopEdge(which dragTarget, tick int64) {
	start, end, on := a.eng.Loop()
	if !on {
		return
	}
	// One beat is the smallest loop worth having; below that the drag
	// would collapse the region and lose the other edge entirely.
	minLen := a.minLoopLen()
	if which == dragLoopA {
		if tick > end-minLen {
			tick = end - minLen
		}
		if tick < 0 {
			tick = 0
		}
		start = tick
	} else {
		if tick < start+minLen {
			tick = start + minLen
		}
		if e := a.pieceEnd(); tick > e {
			tick = e
		}
		end = tick
	}
	a.eng.SetLoop(start, end)
}

// minLoopLen is one beat of the meter at the loop's start, falling back
// to a quarter note when the meter map has nothing to say.
func (a *App) minLoopLen() int64 {
	start, _, _ := a.eng.Loop()
	return a.beatLenAt(start)
}

// beatLenAt is one beat of the meter at tick, with the quarter-note
// fallback. It exists apart from minLoopLen because the new-loop drag
// needs the answer before any loop exists to ask the engine about.
func (a *App) beatLenAt(tick int64) int64 {
	if n := a.sc.Meters.At(tick).BeatLen(); n > 0 {
		return n
	}
	return score.PPQ
}

// toggleLoop is the LOOP chip: it clears an existing loop, and with none
// set makes one over the current bar — the same thing A then B does, in
// one click.
func (a *App) toggleLoop() {
	if _, _, on := a.eng.Loop(); on {
		a.eng.ClearLoop()
		return
	}
	a.loopSetA()
}

// togglePlay is the play/pause button and the space bar.
func (a *App) togglePlay() {
	if a.eng.Playing() {
		a.eng.Pause()
	} else {
		a.eng.Play()
	}
}

// seekPrevBar jumps to the start of this bar, or to the bar before when
// the playhead is already sitting on it.
func (a *App) seekPrevBar() {
	bars := a.displayed().Bars
	pos := a.posTick()
	i := a.barAt(pos)
	if i < 0 || len(bars) == 0 {
		return
	}
	b := bars[i]
	if pos-b.Start < b.Len()/8 && i > 0 {
		a.eng.SeekTick(bars[i-1].Start)
		return
	}
	a.eng.SeekTick(b.Start)
}

// seekNextBar jumps to the start of the following bar — and inside an
// armed loop, stepping forward off the loop's last bar wraps to the loop
// start instead.
//
// The wrap is the UI's own decision, not a side effect: the engine now
// engages a loop at a position exactly on its end (audit E1), so a seek
// onto B would wrap anyway once playing — but only then, which made the
// paused and playing cases differ and read as a warp the user did not
// ask for (verification follow-up to E1). Doing it here makes bar
// navigation inside a practice loop deliberate and immediate: the loop
// is a boundary the user set, and forward motion respects it the same
// way playback does. Backward navigation is untouched — stepping behind
// the loop start is a rewind, and playback re-enters the loop naturally.
// Leaving forward is one keypress (L) or a timeline click past the end.
func (a *App) seekNextBar() {
	bars := a.displayed().Bars
	pos := a.posTick()
	i := a.barAt(pos)
	if i < 0 || i+1 >= len(bars) {
		return
	}
	target := bars[i+1].Start
	if la, lb, on := a.eng.Loop(); on && target == lb && pos >= la {
		a.eng.SeekTick(la)
		return
	}
	a.eng.SeekTick(target)
}

// seekLastBar (the End key) jumps to the start of the final bar. The
// ending of a piece is exactly where A/B loops get set, and with Home
// bound and End not, reaching it meant arrowing through every bar — the
// most expensive trip to the place practice most often starts.
func (a *App) seekLastBar() {
	bars := a.displayed().Bars
	if len(bars) == 0 {
		return
	}
	a.seekTo(bars[len(bars)-1].Start)
}

func (a *App) zoomIn() {
	if a.zoom < 4 {
		a.zoom *= 1.25
	}
}

func (a *App) zoomOut() {
	if a.zoom > 0.3 {
		a.zoom /= 1.25
	}
}

// --- drawing --------------------------------------------------------------

// drawTransport paints the transport buttons and the toggle chips.
func (a *App) drawTransport(dst *ebiten.Image, l practiceLayout, p pointer) {
	icons := [4]iconKind{iconToStart, iconPrevBar, iconPlay, iconNextBar}
	if a.eng.Playing() {
		icons[2] = iconPause
	}
	dt := uiFrameSeconds()
	names := [4]string{"transport:start", "transport:prev", "transport:play", "transport:next"}
	for i, r := range l.transport {
		drawIconButton(dst, r, icons[i], a.anim.step(names[i], p.over(r), p.down, dt))
	}
	for i, c := range a.chipStates() {
		r := l.chips[i]
		drawChip(dst, r, c, a.anim.step("chip:"+c.label, p.over(r), p.down, dt))
	}
}

// drawTrackStrip paints one chip per track: number, name, and the
// mute/solo marks. Muted tracks are dimmed so the strip reads at a
// glance rather than needing its legend.
func (a *App) drawTrackStrip(dst *ebiten.Image, l practiceLayout, p pointer) {
	for i, r := range l.tracks {
		if r.empty() {
			continue
		}
		name := a.sc.Tracks[i].Name
		if name == "" {
			// The chip already leads with the track number, so the
			// fallback must not repeat it: "1 track 1" reads as a bug.
			name = "untitled"
		}
		muted := a.mutedAudibly(i)
		fill, edge, col := colPanel, colPanelEdge, colNote
		switch {
		case a.solo == i+1:
			fill, edge, col = colOn, colOnEdge, colNote
		case muted:
			fill, edge, col = colBG, colBarline, colString
		case p.over(r):
			fill, edge = colHover, colDim
		}
		drawPanel(dst, r, fill, edge)
		if i == a.track {
			// The displayed track is the one the tab is showing; mark it
			// so a click on the wrong chip is an obvious mistake.
			vector.DrawFilledRect(dst, float32(r.x), float32(r.y), 3, float32(r.h), colInferred, false)
		}
		label := truncateW(fmt.Sprintf("%d %s", i+1, name), r.w-18)
		drawText(dst, label, r.x+9, r.y+3, col)
		// Dim, but legible: this line is the only thing that tells a
		// muted track from one silenced by another track's solo.
		sub := colDim
		if muted {
			sub = colBarline
		}
		drawTextSmall(dst, a.trackStateText(i), r.x+9, r.y+20, sub)
	}
	if n := a.hiddenTracks(l); n > 0 {
		drawText(dst, a.hiddenTracksHint(len(l.tracks)-n, n), uiPadX, ptTracksY+chipH+4, colHint)
	}
}

// hiddenTracksHint describes the tracks the strip had no room for. The
// mute/solo keys stop at 9, so the hint only promises keys for tracks
// that actually have one — the old wording named keys that could not
// reach anything past track 9 (audit A7).
func (a *App) hiddenTracksHint(shown, hidden int) string {
	first, total := shown+1, shown+hidden
	switch {
	case first > 9:
		return fmt.Sprintf("+%d more (no keys past track 9)", hidden)
	case total > 9:
		return fmt.Sprintf("+%d more (keys %d-9; tracks past 9 have no key)", hidden, first)
	default:
		return fmt.Sprintf("+%d more (keys %d-%d)", hidden, first, total)
	}
}

// hiddenTracks counts the tracks the strip had no room for.
func (a *App) hiddenTracks(l practiceLayout) int {
	n := 0
	for _, r := range l.tracks {
		if r.empty() {
			n++
		}
	}
	return n
}

// trackStateText is the small second line of a track chip.
func (a *App) trackStateText(i int) string {
	switch {
	case a.solo == i+1:
		return "solo"
	case a.userMutedAt(i):
		return "muted"
	case a.solo > 0:
		return "muted by solo"
	}
	return "audible"
}

// userMutedAt reports the user's own mute choice for track i.
func (a *App) userMutedAt(i int) bool {
	a.ensureMuteState()
	return i >= 0 && i < len(a.userMuted) && a.userMuted[i]
}

// drawTimeline paints the whole-piece strip: bar ticks, the loop region,
// and the playhead. It is the only thing in the view that answers "how
// far through the piece am I" — the tab shows a few bars either side of
// the playhead and nothing about the shape of the whole.
func (a *App) drawTimeline(dst *ebiten.Image, l practiceLayout, p pointer) {
	tl := l.timeline
	hover := p.over(tl)
	edge := colPanelEdge
	if hover {
		edge = colDim
	}
	drawPanel(dst, tl, colPanel, edge)

	// Bar ticks, thinned out so a 200-bar piece does not draw a solid
	// block: every bar while they are more than three pixels apart, and
	// every fourth otherwise.
	bars := a.displayed().Bars
	step := 1
	if len(bars) > 0 && tl.w/float64(len(bars)) < 3 {
		step = 4
	}
	for i := 0; i < len(bars); i += step {
		x := float32(a.timelineX(tl, bars[i].Start))
		vector.StrokeLine(dst, x, float32(tl.y+tl.h-8), x, float32(tl.y+tl.h-2), 1, colBarline, false)
	}

	if la, lb, on := a.eng.Loop(); on {
		x0, x1 := a.timelineX(tl, la), a.timelineX(tl, lb)
		vector.DrawFilledRect(dst, float32(x0), float32(tl.y+1), float32(x1-x0), float32(tl.h-2), colLoop, false)
		for _, x := range []float64{x0, x1} {
			vector.StrokeLine(dst, float32(x), float32(tl.y), float32(x), float32(tl.y+tl.h), 2, colLoopEdge, false)
		}
	}

	x := float32(a.timelineXf(tl, a.posF()))
	vector.StrokeLine(dst, x, float32(tl.y-4), x, float32(tl.y+tl.h+4), 2, colPlayhead, false)

	// A caret under the cursor shows where a click would land before it
	// is made, which is what makes the strip feel like a scrubber rather
	// than a picture.
	if hover && a.drag == dragNone {
		vector.StrokeLine(dst, float32(p.x), float32(tl.y+tl.h), float32(p.x), float32(tl.y+tl.h+4), 1, colDim, false)
	}
	drawTextMono(dst, a.positionCaption(), uiPadX, a.captionY(), colHUD)
	hint := a.timelineHint()
	drawTextSmall(dst, hint, screenW-uiPadX-textWSmall(hint), a.captionY()+2, colBarline)
}

// timelineHint is the small teaching line above the timeline. It follows
// the state because "drag the loop edges to resize" is a lie when no loop
// has edges — and the moment before a loop exists is exactly when the
// gesture that creates one needs teaching.
func (a *App) timelineHint() string {
	if _, _, on := a.eng.Loop(); on {
		return "click or drag to move   ·   drag the loop edges to resize"
	}
	return "click or drag to move   ·   shift-drag to loop a section"
}

// positionCaption is the "where am I" line above the timeline: the bar
// out of the total, and elapsed against total time.
//
// The times are score time divided by the practice scale, which is the
// honest answer to "how long will this take now" — at 60% a two-minute
// piece really does take three and a bit.
func (a *App) positionCaption() string {
	bars := a.displayed().Bars
	pos := a.posTick()
	bar := a.barAt(pos) + 1
	scale := a.eng.TempoScale()
	if scale <= 0 {
		scale = 1
	}
	at := a.sc.Tempos.TimeAt(pos) / scale
	total := a.sc.Tempos.TimeAt(a.pieceEnd()) / scale
	return fmt.Sprintf("bar %d of %d     %s / %s", bar, len(bars), clockText(at), clockText(total))
}

// clockText renders seconds as m:ss.
func clockText(sec float64) string {
	if sec < 0 || sec != sec { // negative or NaN from a degenerate tempo map
		sec = 0
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// drawStateChip paints the count-in or waiting indicator over the
// playhead — the two moments when the transport is doing something the
// player must react to immediately.
func (a *App) drawStateChip(dst *ebiten.Image, label string, col color.RGBA, y float64) {
	x := screenW*playheadX - textWScaled(label, 1.5)/2
	drawTextScaled(dst, label, x, y, 1.5, col)
}
