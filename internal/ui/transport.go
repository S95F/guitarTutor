package ui

import (
	"fmt"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/score"
)

const (
	ptTransportY = 58.0
	ptTracksY    = 100.0
	ptWarnY      = 160.0
	ptWarnH      = 56.0
	ptTimelineH  = 36.0

	ptTabHeadroom = 34.0

	ptTabFooting = 26.0

	ptBtnW = 36.0

	ptBtnH   = chipH
	ptBtnGap = 6.0

	ptGrab = 6.0

	ptTrackChipW = 150.0
)

type dragTarget int

const (
	dragNone dragTarget = iota
	dragSeekTab
	dragSeekTimeline
	dragLoopA
	dragLoopB

	dragLoopNew
)

type practiceChip struct {
	label string

	key string

	on func(a *App) bool

	disabled func(a *App) bool

	whenOff func(a *App) string

	act func(a *App)
}

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
		whenOff:  func(a *App) string { return "settings need the full app — start musicTutor without a file" },
		act:      func(a *App) { a.openSettings() }},
	{label: "HELP", key: "?",
		on:  func(a *App) bool { return a.helpOpen },
		act: func(a *App) { a.openHelp() }},
}

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

type practiceLayout struct {
	transport [4]rect

	chips []rect

	tracks []rect

	tab rect

	timeline rect

	loopA, loopB rect

	tlLoopA, tlLoopB rect
}

func (a *App) tabBottom() float64 {
	return float64(tabTop + (a.laneLines()-1)*stringGap)
}

func (a *App) tabRect() rect {
	top := tabTop - ptTabHeadroom
	return rect{0, top, screenW, a.tabBottom() + ptTabFooting - top}
}

func (a *App) stateChipY() float64 { return a.tabBottom() + 34 }

const (
	ptCaptionY  = screenH - 158.0
	ptTimelineY = screenH - 134.0
	ptLegendY   = screenH - 86.0
	ptMsgY      = screenH - 64.0
)

func (a *App) captionY() float64 { return ptCaptionY }

func (a *App) timelineY() float64 { return ptTimelineY }

func (a *App) legendY() float64 { return ptLegendY }

func (a *App) msgY() float64 { return ptMsgY }

func (a *App) layout() practiceLayout {
	var l practiceLayout

	x := uiPadX
	for i := range l.transport {
		l.transport[i] = rect{x, ptTransportY, ptBtnW, ptBtnH}
		x += ptBtnW + ptBtnGap
	}
	x += 18

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

		if tx+ptTrackChipW > a.trackStripRight() {
			break
		}
		l.tracks[i] = rect{tx, ptTracksY, ptTrackChipW, chipH}
		tx += ptTrackChipW + chipGap
	}

	l.tab = a.tabRect()
	l.timeline = rect{uiPadX, a.timelineY(), screenW - 2*uiPadX, ptTimelineH}

	if la, lb, on := a.eng.Loop(); on {

		if !a.tunerView {
			l.loopA = a.loopHandleOnTab(la)
			l.loopB = a.loopHandleOnTab(lb)
		}
		l.tlLoopA = a.loopHandleOnTimeline(l.timeline, la)
		l.tlLoopB = a.loopHandleOnTimeline(l.timeline, lb)
	}
	return l
}

func (a *App) trackStripRight() float64 {
	if a.live {
		return screenW - uiPadX - 260
	}
	return screenW - uiPadX
}

func (a *App) loopHandleOnTab(tick int64) rect {
	x := a.xAtTick(tick)
	if x < -ptGrab || x > screenW+ptGrab {
		return rect{}
	}
	t := a.tabRect()
	return rect{x - ptGrab, t.y, 2 * ptGrab, t.h}
}

func (a *App) loopHandleOnTimeline(tl rect, tick int64) rect {
	x := a.timelineX(tl, tick)
	return rect{x - ptGrab, tl.y, 2 * ptGrab, tl.h}
}

func (a *App) xAtTick(tick int64) float64 {
	return screenW*playheadX + (float64(tick)-a.posF())*a.pxPerTick()
}

func (a *App) tickAtX(x float64) int64 {
	return int64(math.Round(a.posF() + (x-screenW*playheadX)/a.pxPerTick()))
}

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

func (a *App) timelineX(tl rect, tick int64) float64 { return a.timelineXf(tl, float64(tick)) }

func (a *App) timelineXf(tl rect, tick float64) float64 {
	f := tick / float64(a.pieceEnd())
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	return tl.x + f*tl.w
}

func (a *App) timelineTick(tl rect, x float64) int64 {
	if tl.w <= 0 {
		return 0
	}
	return clampTick((x-tl.x)/tl.w*float64(a.pieceEnd()), a.pieceEnd())
}

func clampTick(t float64, end int64) int64 {
	if t < 0 {
		return 0
	}
	if int64(t) > end {
		return end
	}
	return int64(t)
}

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

func (a *App) handleMouse(p pointer, shift bool) {
	if a.drag != dragNone {

		if !p.down {
			a.drag = dragNone
			return
		}
		a.continueDrag(p, shift)
		return
	}
	l := a.layout()

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

	if !p.pressed {
		return
	}

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

func (a *App) beginLoopDraw(tick int64, x float64, onTimeline bool) {
	a.drag = dragLoopNew
	a.loopDrawTick, a.loopDrawX, a.loopDrawing = tick, x, false
	a.loopDrawOnTimeline = onTimeline

	a.loopDrawPxPerTick = a.pxPerTick()
	a.seekTo(tick)
}

const loopDrawSlop = 4.0

func (a *App) continueLoopDraw(p pointer, l practiceLayout) {
	if !a.loopDrawing && math.Abs(p.x-a.loopDrawX) <= loopDrawSlop {
		return
	}
	a.loopDrawing = true

	var cur int64
	if a.loopDrawOnTimeline {
		cur = a.timelineTick(l.timeline, p.x)
	} else {
		cur = a.loopDrawTick + int64(math.Round((p.x-a.loopDrawX)/a.loopDrawPxPerTick))
	}
	lo, hi := a.loopDrawTick, cur
	if hi < lo {
		lo, hi = hi, lo
	}

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

func (a *App) continueDrag(p pointer, shift bool) {
	l := a.layout()
	switch a.drag {
	case dragSeekTab:
		a.seekTo(a.tickAtX(p.x))
	case dragSeekTimeline:
		a.seekTo(a.timelineTick(l.timeline, p.x))
	case dragLoopNew:
		a.continueLoopDraw(p, l)
	case dragLoopA, dragLoopB:

		tick := a.tickAtX(p.x)
		if p.over(l.timeline) {
			tick = a.timelineTick(l.timeline, p.x)
		}
		if !shift {
			tick = a.snapToBeat(tick)
		}
		a.moveLoopEdge(a.drag, tick)
	}

}

func (a *App) moveLoopEdge(which dragTarget, tick int64) {
	start, end, on := a.eng.Loop()
	if !on {
		return
	}

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

func (a *App) minLoopLen() int64 {
	start, _, _ := a.eng.Loop()
	return a.beatLenAt(start)
}

func (a *App) beatLenAt(tick int64) int64 {
	if n := a.sc.Meters.At(tick).BeatLen(); n > 0 {
		return n
	}
	return score.PPQ
}

func (a *App) toggleLoop() {
	if _, _, on := a.eng.Loop(); on {
		a.eng.ClearLoop()
		return
	}
	a.loopSetA()
}

func (a *App) togglePlay() {
	if a.eng.Playing() {
		a.eng.Pause()
	} else {
		a.eng.Play()
	}
}

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

func (a *App) drawTrackStrip(dst *ebiten.Image, l practiceLayout, p pointer) {
	for i, r := range l.tracks {
		if r.empty() {
			continue
		}
		name := a.sc.Tracks[i].Name
		if name == "" {

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

			vector.DrawFilledRect(dst, float32(r.x), float32(r.y), 3, float32(r.h), colInferred, false)
		}
		label := truncateW(fmt.Sprintf("%d %s", i+1, name), r.w-18)
		drawText(dst, label, r.x+9, r.y+3, col)

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

func (a *App) hiddenTracks(l practiceLayout) int {
	n := 0
	for _, r := range l.tracks {
		if r.empty() {
			n++
		}
	}
	return n
}

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

func (a *App) userMutedAt(i int) bool {
	a.ensureMuteState()
	return i >= 0 && i < len(a.userMuted) && a.userMuted[i]
}

func (a *App) drawTimeline(dst *ebiten.Image, l practiceLayout, p pointer) {
	tl := l.timeline
	hover := p.over(tl)
	edge := colPanelEdge
	if hover {
		edge = colDim
	}
	drawPanel(dst, tl, colPanel, edge)

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

	if hover && a.drag == dragNone {
		vector.StrokeLine(dst, float32(p.x), float32(tl.y+tl.h), float32(p.x), float32(tl.y+tl.h+4), 1, colDim, false)
	}
	drawTextMono(dst, a.positionCaption(), uiPadX, a.captionY(), colHUD)
	hint := a.timelineHint()
	drawTextSmall(dst, hint, screenW-uiPadX-textWSmall(hint), a.captionY()+2, colBarline)
}

func (a *App) timelineHint() string {
	if _, _, on := a.eng.Loop(); on {
		return "click or drag to move   ·   drag the loop edges to resize"
	}
	return "click or drag to move   ·   shift-drag to loop a section"
}

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

func clockText(sec float64) string {
	if sec < 0 || sec != sec {
		sec = 0
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func (a *App) drawStateChip(dst *ebiten.Image, label string, col color.RGBA, y float64) {
	x := screenW*playheadX - textWScaled(label, 1.5)/2
	drawTextScaled(dst, label, x, y, 1.5, col)
}
