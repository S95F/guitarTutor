package ui

import (
	"testing"

	"github.com/S95F/musicTutor/internal/score"
)

func pressAt(r rect) pointer {
	return pointer{x: r.x + r.w/2, y: r.y + r.h/2, down: true, pressed: true}
}

func heldAt(x, y float64) pointer {
	return pointer{x: x, y: y, down: true}
}

func chipRect(t *testing.T, a *App, label string) rect {
	t.Helper()
	l := a.layout()
	for i, c := range practiceChips {
		if c.label == label {
			return l.chips[i]
		}
	}
	t.Fatalf("no chip labelled %q", label)
	return rect{}
}

func TestRectContainsIsHalfOpen(t *testing.T) {
	r := rect{10, 20, 100, 50}
	if !r.contains(10, 20) {
		t.Error("the top-left corner should be inside")
	}
	if r.contains(110, 40) {
		t.Error("the right edge is exclusive, so two adjacent rects cannot both claim it")
	}
	if r.contains(50, 70) {
		t.Error("the bottom edge is exclusive")
	}
	var zero rect
	if !zero.empty() {
		t.Error("the zero rect is the no-region value and must report empty")
	}
}

func TestPointerHitStopsAtFirstMatch(t *testing.T) {
	first, second := 0, 0
	spots := []hotspot{
		{r: rect{0, 0, 100, 100}, on: func() { first++ }},
		{r: rect{0, 0, 100, 100}, on: func() { second++ }},
	}
	p := pointer{x: 50, y: 50, down: true, pressed: true}
	if !p.hit(spots) {
		t.Fatal("a press inside a hotspot should be taken")
	}
	if first != 1 || second != 0 {
		t.Errorf("first fired %d, second %d; want only the first", first, second)
	}
}

func TestActionlessHotspotSwallowsTheClick(t *testing.T) {
	p := pointer{x: 5, y: 5, down: true, pressed: true}
	if !p.hit([]hotspot{{r: rect{0, 0, 10, 10}}}) {
		t.Error("a hotspot with no action for this button must still swallow the press")
	}
	if p.hit([]hotspot{{r: rect{50, 50, 10, 10}}}) {
		t.Error("a press outside every hotspot must not be taken")
	}
}

func TestTimelineMapsTicksToPixelsAndBack(t *testing.T) {
	a := newApp(t, 8)
	tl := a.layout().timeline
	for _, tick := range []int64{0, 3840, 15360, a.pieceEnd()} {
		x := a.timelineX(tl, tick)
		if got := a.timelineTick(tl, x); abs64(got-tick) > 32 {
			t.Errorf("tick %d -> x %.1f -> tick %d, want it back within a rounding tick", tick, x, got)
		}
	}
	if x := a.timelineX(tl, -500); x != tl.x {
		t.Errorf("a tick before the piece should clamp to the strip's left edge, got %.1f", x)
	}
	if got := a.timelineTick(tl, tl.x+tl.w+400); got != a.pieceEnd() {
		t.Errorf("a click past the strip should clamp to the end, got %d", got)
	}
}

func TestClickOnTimelineSeeks(t *testing.T) {
	a := newApp(t, 8)
	tl := a.layout().timeline
	a.handleMouse(pressAt(tl), false)
	want := a.pieceEnd() / 2
	if got := a.eng.PosTick(); abs64(got-want) > 64 {
		t.Errorf("clicking the middle of the timeline seeked to %d, want about %d", got, want)
	}
	if a.drag != dragSeekTimeline {
		t.Error("the press should also start a scrub, so holding and moving keeps seeking")
	}
}

func TestDragKeepsThePointerItGrabbed(t *testing.T) {
	a := newApp(t, 8)
	tl := a.layout().timeline
	a.handleMouse(pressAt(tl), false)

	click := chipRect(t, a, "CLICK")
	before := a.metronome
	a.handleMouse(heldAt(click.x+click.w/2, click.y+click.h/2), false)
	if a.metronome != before {
		t.Error("dragging across a chip toggled it; the drag should own the pointer")
	}
	if a.drag != dragSeekTimeline {
		t.Error("the drag ended early")
	}

	a.handleMouse(pointer{x: click.x, y: click.y}, false)
	if a.drag != dragNone {
		t.Error("releasing the button should end the drag")
	}
}

func TestDragOnTabSeeks(t *testing.T) {
	a := newApp(t, 8)
	tab := a.layout().tab

	x := screenW*playheadX + 200
	a.handleMouse(pointer{x: x, y: tab.y + tab.h/2, down: true, pressed: true}, false)
	want := int64(200 / a.pxPerTick())
	if got := a.eng.PosTick(); abs64(got-want) > 2 {
		t.Errorf("dragging the tab seeked to %d, want about %d", got, want)
	}
}

func TestTabDragIsInertInTheTuner(t *testing.T) {
	a := newApp(t, 8)
	a.tunerView = true
	tab := a.layout().tab
	a.handleMouse(pointer{x: screenW*playheadX + 200, y: tab.y + tab.h/2, down: true, pressed: true}, false)
	if a.drag != dragNone || a.eng.PosTick() != 0 {
		t.Errorf("the tuner view should not seek: drag %v, pos %d", a.drag, a.eng.PosTick())
	}
}

func TestDragLoopEdgeSnapsToBeat(t *testing.T) {
	a := newApp(t, 8)
	a.eng.SetLoop(0, 3840)

	l := a.layout()
	if l.loopB.empty() {
		t.Fatal("a set loop should have a grab handle on its end")
	}
	a.handleMouse(pressAt(l.loopB), false)
	if a.drag != dragLoopB {
		t.Fatalf("pressing the loop end started %v, want dragLoopB", a.drag)
	}

	a.handleMouse(heldAt(a.xAtTick(8160), l.tab.y+10), false)
	if _, end, _ := a.eng.Loop(); end != 7680 {
		t.Errorf("loop end snapped to %d, want the bar line at 7680", end)
	}

	a.handleMouse(heldAt(a.xAtTick(8160), l.tab.y+10), true)
	if _, end, _ := a.eng.Loop(); end != 8160 {
		t.Errorf("with shift held the loop end is %d, want the exact tick 8160", end)
	}
}

func TestLoopEdgeStopsAtItsPartner(t *testing.T) {
	a := newApp(t, 8)
	a.eng.SetLoop(0, 3840)
	l := a.layout()

	a.handleMouse(pressAt(l.loopA), false)
	a.handleMouse(heldAt(a.xAtTick(9000), l.tab.y+10), false)

	start, end, on := a.eng.Loop()
	if !on {
		t.Fatal("dragging the start past the end destroyed the loop")
	}
	if start >= end {
		t.Fatalf("loop is empty: [%d, %d)", start, end)
	}
	if want := end - a.minLoopLen(); start != want {
		t.Errorf("loop start clamped to %d, want %d — one beat short of the end", start, want)
	}
}

func TestLoopEdgesAreTestedBeforeTheSurfacesTheySitOn(t *testing.T) {
	a := newApp(t, 8)
	a.eng.SetLoop(3840, 7680)
	a.eng.SeekTick(3840)
	l := a.layout()

	pos := a.eng.PosTick()
	a.handleMouse(pressAt(l.loopA), false)
	if a.drag != dragLoopA {
		t.Fatalf("pressing the loop start began %v, want dragLoopA", a.drag)
	}
	if a.eng.PosTick() != pos {
		t.Error("grabbing a loop edge also seeked; the edge must win the hit test")
	}
}

func TestChipsToggleTheirState(t *testing.T) {
	a := newApp(t, 4)
	a.waitCtl = true

	cases := []struct {
		label string
		read  func() bool
	}{
		{"CLICK", func() bool { return a.metronome }},
		{"RAMP", func() bool { return a.ramp }},
		{"TUNER", func() bool { return a.tunerView }},
		{"WAIT", func() bool { return a.wait }},
	}
	for _, c := range cases {
		before := c.read()
		a.handleMouse(pressAt(chipRect(t, a, c.label)), false)
		if c.read() == before {
			t.Errorf("clicking %s did not change its state", c.label)
		}
	}
}

func TestLoopChipMakesAndClearsALoop(t *testing.T) {
	a := newApp(t, 4)
	a.handleMouse(pressAt(chipRect(t, a, "LOOP")), false)
	if _, _, on := a.eng.Loop(); !on {
		t.Fatal("the LOOP chip should set a loop over the current bar")
	}
	a.handleMouse(pressAt(chipRect(t, a, "LOOP")), false)
	if _, _, on := a.eng.Loop(); on {
		t.Error("clicking LOOP again should clear the loop")
	}
}

func TestTransportButtonsMoveThePlayhead(t *testing.T) {
	a := newApp(t, 8)
	l := a.layout()

	a.handleMouse(pressAt(l.transport[3]), false)
	if got := a.eng.PosTick(); got != 3840 {
		t.Errorf("next-bar moved to %d, want 3840", got)
	}
	a.handleMouse(pressAt(l.transport[1]), false)
	if got := a.eng.PosTick(); got != 0 {
		t.Errorf("previous-bar from the top of bar 2 moved to %d, want 0", got)
	}
	a.handleMouse(pressAt(l.transport[3]), false)
	a.handleMouse(pressAt(l.transport[0]), false)
	if got := a.eng.PosTick(); got != 0 {
		t.Errorf("to-start moved to %d, want 0", got)
	}
	if a.eng.Playing() {
		t.Fatal("the fixture should start paused")
	}
	a.handleMouse(pressAt(l.transport[2]), false)
	if !a.eng.Playing() {
		t.Error("the play button did not start playback")
	}
}

func TestTrackChipMutesAndSolos(t *testing.T) {
	a := newAppTracks(t, 3, 4)
	l := a.layout()
	if l.tracks[1].empty() {
		t.Fatal("three tracks should all fit the strip")
	}

	a.handleMouse(pressAt(l.tracks[1]), false)
	if !a.userMutedAt(1) {
		t.Error("clicking a track chip did not mute it")
	}
	a.handleMouse(pressAt(l.tracks[1]), false)
	if a.userMutedAt(1) {
		t.Error("clicking a muted track chip did not unmute it")
	}

	r := l.tracks[2]
	a.handleMouse(pointer{x: r.x + r.w/2, y: r.y + r.h/2, right: true}, false)
	if a.solo != 3 {
		t.Errorf("right-clicking track 3 set solo to %d, want 3", a.solo)
	}
	if !a.mutedAudibly(0) {
		t.Error("a solo should silence the other tracks")
	}
}

func TestWheelOverTheTabZooms(t *testing.T) {
	a := newApp(t, 4)
	tab := a.layout().tab
	before := a.zoom
	a.handleMouse(pointer{x: 400, y: tab.y + tab.h/2, wheel: 1}, false)
	if a.zoom <= before {
		t.Errorf("wheel up over the tab left zoom at %v", a.zoom)
	}
	at := a.zoom
	a.handleMouse(pointer{x: 400, y: uiHeaderY, wheel: 1}, false)
	if a.zoom != at {
		t.Error("the wheel over the header should not zoom the tab")
	}
}

func TestPositionCaptionReportsBarAndTime(t *testing.T) {
	a := newApp(t, 8)
	if got, want := a.positionCaption(), "bar 1 of 8"; len(got) < len(want) || got[:len(want)] != want {
		t.Errorf("caption %q should start with %q", got, want)
	}
	a.eng.SeekTick(3840 * 3)
	if got, want := a.positionCaption(), "bar 4 of 8"; got[:len(want)] != want {
		t.Errorf("after seeking to bar 4 the caption is %q", got)
	}

	a.eng.SetTempoScale(0.5)
	if got := a.positionCaption(); !contains(got, "0:32") {
		t.Errorf("at half speed the total should read 0:32, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestClockText(t *testing.T) {
	for _, c := range []struct {
		sec  float64
		want string
	}{{0, "0:00"}, {9.4, "0:09"}, {61, "1:01"}, {600, "10:00"}, {-5, "0:00"}} {
		if got := clockText(c.sec); got != c.want {
			t.Errorf("clockText(%v) = %q, want %q", c.sec, got, c.want)
		}
	}
}

func TestSnapToBeatPrefersTheNearestCandidate(t *testing.T) {
	a := newApp(t, 4)
	for _, c := range []struct{ in, want int64 }{
		{0, 0},
		{100, 0},
		{3800, 3840},
		{3840 + 100, 3840},
		{score.PPQ * 100, 4 * 3840},
	} {
		if got := a.snapToBeat(c.in); got != c.want {
			t.Errorf("snapToBeat(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestReloadPromptNeedsAReloader(t *testing.T) {
	a := newApp(t, 4)
	a.MarkSettingsChanged()
	if a.reloadPrompt() != "" {
		t.Error("a view with no reloader wired should not offer a reload")
	}

	reloaded := 0
	a.SetReloader(func() { reloaded++ })
	a.MarkSettingsChanged()
	if a.reloadPrompt() == "" {
		t.Fatal("after a settings change the view should offer a reload")
	}
	a.reloadPiece()
	if reloaded != 1 {
		t.Errorf("reload ran %d times, want 1", reloaded)
	}

	if a.reloadPrompt() == "" {
		t.Error("a failed reload must leave the offer standing")
	}
}

func TestSettingsChipEnablesWithAnOpener(t *testing.T) {
	a := newApp(t, 4)
	find := func() chipState {
		for i, c := range practiceChips {
			if c.label == "SETTINGS" {
				return a.chipStates()[i]
			}
		}
		t.Fatal("no SETTINGS chip")
		return chipState{}
	}
	if !find().disabled {
		t.Error("with no opener wired the SETTINGS chip should be disabled")
	}
	opened := 0
	a.SetSettingsOpener(func() { opened++ })
	if find().disabled {
		t.Error("with an opener wired the chip should be usable")
	}
	a.handleMouse(pressAt(chipRect(t, a, "SETTINGS")), false)
	if opened != 1 {
		t.Errorf("the SETTINGS chip opened settings %d times, want 1", opened)
	}
}

func TestTrackStripLeavesRoomForTheLiveMeter(t *testing.T) {
	a := newAppTracks(t, 9, 2)
	wide := a.layout()
	a.live = true
	narrow := a.layout()

	if a.hiddenTracks(narrow) <= a.hiddenTracks(wide) {
		t.Errorf("a live session hides %d tracks and a quiet one %d; the meter should cost room",
			a.hiddenTracks(narrow), a.hiddenTracks(wide))
	}
	for i, r := range narrow.tracks {
		if !r.empty() && r.x+r.w > a.trackStripRight() {
			t.Errorf("track %d's chip runs under the live meter", i)
		}
	}
}

func TestTunerHidesTabLoopHandles(t *testing.T) {
	a := newApp(t, 8)
	a.eng.SetLoop(3840, 7680)

	l := a.layout()
	if l.loopA.empty() || l.loopB.empty() {
		t.Fatal("with the tab showing, both loop edges should have grab handles")
	}

	a.tunerView = true
	l = a.layout()
	if !l.loopA.empty() || !l.loopB.empty() {
		t.Error("the tuner view must not keep invisible tab loop handles")
	}
	if l.tlLoopA.empty() || l.tlLoopB.empty() {
		t.Error("the timeline is drawn in the tuner view, so its handles must stay")
	}

	a.eng.SeekTick(3840)
	tab := a.tabRect()
	a.handleMouse(pointer{x: a.xAtTick(3840), y: tab.y + tab.h/2, down: true, pressed: true}, false)
	if a.drag == dragLoopA || a.drag == dragLoopB {
		t.Errorf("clicking the invisible loop edge started drag %v", a.drag)
	}
}

func TestRightClickNeverSeeksOrGrabs(t *testing.T) {
	a := newApp(t, 8)
	a.eng.SetLoop(0, 3840)
	l := a.layout()

	rightAt := func(x, y float64) pointer { return pointer{x: x, y: y, right: true} }

	a.handleMouse(rightAt(l.timeline.x+l.timeline.w/2, l.timeline.y+l.timeline.h/2), false)
	if got := a.eng.PosTick(); got != 0 {
		t.Errorf("right-clicking the timeline seeked to %d", got)
	}
	if a.drag != dragNone {
		t.Errorf("right-clicking the timeline started drag %v", a.drag)
	}

	tab := a.tabRect()
	a.handleMouse(rightAt(screenW*playheadX+200, tab.y+tab.h/2), false)
	if got := a.eng.PosTick(); got != 0 {
		t.Errorf("right-clicking the tab seeked to %d", got)
	}

	a.handleMouse(rightAt(l.loopB.x+l.loopB.w/2, l.loopB.y+10), false)
	if a.drag != dragNone {
		t.Errorf("right-clicking a loop edge started drag %v", a.drag)
	}
}

func TestStaleDragDiesWithoutActing(t *testing.T) {
	a := newApp(t, 8)
	tl := a.layout().timeline
	a.handleMouse(pressAt(tl), false)
	if a.drag != dragSeekTimeline {
		t.Fatal("the press should have started a scrub")
	}
	pos := a.eng.PosTick()

	a.handleMouse(pointer{x: tl.x + tl.w - 1, y: tl.y + 2}, false)
	if a.drag != dragNone {
		t.Error("a drag with the button up should be dropped")
	}
	if got := a.eng.PosTick(); got != pos {
		t.Errorf("the dead drag still seeked from %d to %d", pos, got)
	}

	a.handleMouse(pressAt(tl), false)
	a.openHelp()
	if err := a.Update(); err != nil {
		t.Fatalf("Update with help open: %v", err)
	}
	if a.drag != dragNone {
		t.Error("a modal taking the frame should cancel the drag")
	}
}

func TestNextBarInsideLoopWraps(t *testing.T) {
	a := newApp(t, 8)
	a.eng.SetLoop(3840, 11520)
	a.eng.SeekTick(7680)

	a.seekNextBar()
	if got := a.eng.PosTick(); got != 3840 {
		t.Errorf("next-bar from the loop's last bar landed at %d, want the loop start 3840", got)
	}

	a.eng.SeekTick(0)
	a.seekNextBar()
	if got := a.eng.PosTick(); got != 3840 {
		t.Errorf("next-bar from bar 1 landed at %d, want 3840", got)
	}
	a.eng.SeekTick(11520)
	a.seekNextBar()
	if got := a.eng.PosTick(); got != 15360 {
		t.Errorf("next-bar past the loop landed at %d, want 15360", got)
	}

	a.eng.SeekTick(3840)
	a.seekPrevBar()
	if got := a.eng.PosTick(); got != 0 {
		t.Errorf("prev-bar from the loop start landed at %d, want 0", got)
	}
}

func TestHiddenTracksHint(t *testing.T) {
	a := newApp(t, 1)
	for _, c := range []struct {
		shown, hidden int
		want          string
	}{
		{5, 3, "+3 more (keys 6-8)"},
		{6, 3, "+3 more (keys 7-9)"},
		{6, 6, "+6 more (keys 7-9; tracks past 9 have no key)"},
		{9, 3, "+3 more (no keys past track 9)"},
	} {
		if got := a.hiddenTracksHint(c.shown, c.hidden); got != c.want {
			t.Errorf("hiddenTracksHint(%d, %d) = %q, want %q", c.shown, c.hidden, got, c.want)
		}
	}
}

func TestWheelDoesNotZoomUnderTheTuner(t *testing.T) {
	a := newApp(t, 4)
	tab := a.layout().tab
	over := pointer{x: 400, y: tab.y + tab.h/2, wheel: 1}

	a.tunerView = true
	before := a.zoom
	for i := 0; i < 4; i++ {
		a.handleMouse(over, false)
	}
	if a.zoom != before {
		t.Errorf("the wheel zoomed the hidden tab to %v", a.zoom)
	}

	a.tunerView = false
	a.handleMouse(over, false)
	if a.zoom <= before {
		t.Error("with the tab showing the wheel should still zoom")
	}
}

func TestShiftDragDrawsALoopOnTheTimeline(t *testing.T) {
	a := newApp(t, 8)
	tl := a.layout().timeline
	xFor := func(tick int64) float64 { return a.timelineX(tl, tick) }

	a.handleMouse(pointer{x: xFor(3840), y: tl.y + tl.h/2, down: true, pressed: true}, true)
	if a.drag != dragLoopNew {
		t.Fatalf("a shift-press on the timeline started %v, want dragLoopNew", a.drag)
	}
	if _, _, on := a.eng.Loop(); on {
		t.Fatal("the press alone made a loop; it must wait for the drag to mean one")
	}

	a.handleMouse(heldAt(xFor(11520), tl.y+tl.h/2), true)
	la, lb, on := a.eng.Loop()
	if !on || la != 3840 || lb != 11520 {
		t.Errorf("shift-drag made loop [%d, %d) on=%v, want [3840, 11520) on=true", la, lb, on)
	}

	a.handleMouse(heldAt(xFor(0), tl.y+tl.h/2), true)
	la, lb, on = a.eng.Loop()
	if !on || la != 0 || lb != 3840 {
		t.Errorf("dragging left of the anchor made [%d, %d) on=%v, want [0, 3840) on=true", la, lb, on)
	}

	a.handleMouse(pointer{x: xFor(0), y: tl.y + 2}, true)
	if a.drag != dragNone {
		t.Error("releasing the button should end the gesture")
	}
	if _, _, on := a.eng.Loop(); !on {
		t.Error("the loop must survive the release")
	}
}

func TestShiftPressWithinSlopStaysAPlainSeek(t *testing.T) {
	a := newApp(t, 8)
	tl := a.layout().timeline
	a.handleMouse(pressAt(tl), true)
	want := a.pieceEnd() / 2
	if got := a.eng.PosTick(); abs64(got-want) > 64 {
		t.Errorf("the shift-press seeked to %d, want about %d", got, want)
	}
	a.handleMouse(heldAt(tl.x+tl.w/2+loopDrawSlop-1, tl.y+2), true)
	if _, _, on := a.eng.Loop(); on {
		t.Error("a wobble inside the slop drew a loop")
	}
	a.handleMouse(pointer{x: tl.x + tl.w/2, y: tl.y + 2}, true)
	if _, _, on := a.eng.Loop(); on {
		t.Error("releasing inside the slop drew a loop")
	}
	if a.drag != dragNone {
		t.Error("the gesture should be over after the release")
	}
}

func TestShiftDragDrawsALoopOnTheTab(t *testing.T) {
	a := newApp(t, 8)
	tab := a.layout().tab
	y := tab.y + tab.h/2
	a.handleMouse(pointer{x: screenW*playheadX + 400, y: y, down: true, pressed: true}, true)
	if a.drag != dragLoopNew {
		t.Fatalf("a shift-press on the tab started %v, want dragLoopNew", a.drag)
	}
	a.handleMouse(heldAt(screenW*playheadX+500, y), true)
	la, lb, on := a.eng.Loop()
	if !on {
		t.Fatal("shift-dragging the tab drew no loop")
	}
	if la >= lb || la%960 != 0 || lb%960 != 0 {
		t.Errorf("loop [%d, %d) is not snapped to beats", la, lb)
	}

	dragged := int64(100 / a.pxPerTick())
	if got := lb - la; got > dragged+2*960 {
		t.Errorf("a 100px drag drew a %d-tick loop, want about %d (± a beat of snap)", got, dragged)
	}

	b := newApp(t, 8)
	b.tunerView = true
	b.handleMouse(pointer{x: screenW*playheadX + 200, y: y, down: true, pressed: true}, true)
	if b.drag != dragNone {
		t.Errorf("shift-press over the tuner started %v", b.drag)
	}
}

func TestShiftDragStaysOnItsAnchorSurface(t *testing.T) {

	a := newApp(t, 8)
	l := a.layout()
	tabY := l.tab.y + l.tab.h/2
	tlY := l.timeline.y + l.timeline.h/2

	a.handleMouse(pointer{x: screenW*playheadX + 400, y: tabY, down: true, pressed: true}, true)
	a.handleMouse(heldAt(screenW*playheadX+500, tlY), true)
	la, lb, on := a.eng.Loop()
	if !on {
		t.Fatal("the drifted tab drag drew no loop")
	}
	dragged := int64(100 / a.loopDrawPxPerTick)
	if got := lb - la; got > dragged+2*960 {
		t.Errorf("drifting onto the timeline blew the loop up to %d ticks, want about %d", got, dragged)
	}

	b := newApp(t, 8)
	l = b.layout()
	start := l.timeline.x + l.timeline.w/2
	b.handleMouse(pointer{x: start, y: tlY, down: true, pressed: true}, true)
	b.handleMouse(heldAt(start+l.timeline.w/10, tabY), true)
	la, lb, on = b.eng.Loop()
	if !on {
		t.Fatal("the drifted timeline drag drew no loop")
	}
	tenth := b.pieceEnd() / 10
	if got := lb - la; got < tenth/2 || got > tenth*2 {
		t.Errorf("a tenth-of-the-strip drag spans %d ticks, want near %d — it fell back to the tab's scale", got, tenth)
	}
}

func TestShiftPressOnALoopEdgeStillResizes(t *testing.T) {
	a := newApp(t, 8)
	a.eng.SetLoop(3840, 7680)
	l := a.layout()
	a.handleMouse(pressAt(l.loopA), true)
	if a.drag != dragLoopA {
		t.Errorf("shift-pressing the loop start began %v, want dragLoopA", a.drag)
	}
}

func TestTimelineHintTeachesTheMissingGesture(t *testing.T) {
	a := newApp(t, 4)
	if got := a.timelineHint(); !contains(got, "shift-drag") {
		t.Errorf("with no loop the hint is %q, want it to teach shift-drag", got)
	}
	a.eng.SetLoop(0, 3840)
	if got := a.timelineHint(); !contains(got, "drag the loop edges") {
		t.Errorf("with a loop the hint is %q, want it to teach the edge drag", got)
	}
}

func TestSeekLastBarJumpsToTheEnding(t *testing.T) {
	a := newApp(t, 8)
	a.seekLastBar()
	if got := a.eng.PosTick(); got != 7*3840 {
		t.Errorf("End landed at %d, want the last bar's start %d", got, 7*3840)
	}

	empty := newApp(t, 0)
	empty.seekLastBar()
	if got := empty.eng.PosTick(); got != 0 {
		t.Errorf("End on a bar-less track moved to %d", got)
	}

	found := false
	for _, b := range practiceBindings {
		if b.Keys == "home / end" {
			found = true
		}
	}
	if !found {
		t.Error("no binding row teaches home / end together")
	}
}

func TestDisabledChipClickExplains(t *testing.T) {
	a := newApp(t, 4)
	if a.waitCtl || a.settings != nil {
		t.Fatal("the fixture should start with WAIT and SETTINGS disabled")
	}

	a.handleMouse(pressAt(chipRect(t, a, "WAIT")), false)
	if a.wait {
		t.Error("the disabled WAIT chip engaged wait mode")
	}
	if got := a.bpmMessage(); !contains(got, "live input") {
		t.Errorf("clicking greyed WAIT said %q, want it to name the missing live input", got)
	}

	a.handleMouse(pressAt(chipRect(t, a, "SETTINGS")), false)
	if got := a.bpmMessage(); !contains(got, "full app") {
		t.Errorf("clicking greyed SETTINGS said %q, want it to explain the standalone window", got)
	}
}

func TestTransportRowSharesOneHeight(t *testing.T) {
	a := newApp(t, 4)
	l := a.layout()
	for i, r := range l.transport {
		if r.h != chipH {
			t.Errorf("transport button %d is %v tall, chips are %v", i, r.h, chipH)
		}
		if got, want := r.y+r.h, l.chips[0].y+l.chips[0].h; got != want {
			t.Errorf("transport button %d bottom %v, chip bottom %v", i, got, want)
		}
	}
}
