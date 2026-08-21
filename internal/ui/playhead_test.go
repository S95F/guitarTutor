package ui

import (
	"math"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/score"
)

// The playhead's contract: it fills in the gaps between the engine's
// publishes, it never invents motion the engine is not making, and it can
// never drift away from the engine's own position however long it runs.

// tickRate is the tick-per-second rate of 120 BPM at scale 1 — two beats a
// second, so 2*PPQ ticks.
const tickRate = 2 * float64(score.PPQ)

// display is one display frame at 60 ticks per second.
const display = 1.0 / 60

// playing builds a snapshot of a transport moving at tickRate.
func playing(tick float64) engine.PlayPos {
	return engine.PlayPos{Tick: tick, TicksPerSecond: tickRate, Advancing: true}
}

// TestPlayheadSlidesBetweenPublishes is the whole point: the engine's
// position only changes every few display frames, and the drawn one must
// move on every single one of them.
func TestPlayheadSlidesBetweenPublishes(t *testing.T) {
	var p playhead
	p.step(playing(0), 0)

	// The engine publishes once and then says nothing for three frames,
	// which is what a 50 ms render block looks like from 60 fps.
	var seen []float64
	for i := 0; i < 3; i++ {
		seen = append(seen, p.step(playing(0), display))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("frame %d did not move: %v", i, seen)
		}
	}
	// And it moves at about the music's own speed, not some fraction of it.
	if got, want := seen[len(seen)-1], 3*display*tickRate; math.Abs(got-want) > want*0.25 {
		t.Errorf("three frames of gliding covered %.1f ticks, want about %.1f", got, want)
	}
}

// TestPlayheadDoesNotDrift is the property that makes interpolating safe at
// all. Over a long run against a staircase target, the drawn position must
// track the engine's rather than accumulating its own error — a display
// clock that ran even 1% fast would be a bar out after a few minutes.
func TestPlayheadDoesNotDrift(t *testing.T) {
	var p playhead
	// The engine advances in 50 ms gulps; the display runs at 60 fps.
	const gulp = 0.05
	engineTick, sinceGulp, got := 0.0, 0.0, 0.0
	p.step(playing(0), 0)
	for i := 0; i < 60*120; i++ { // two minutes
		sinceGulp += display
		if sinceGulp >= gulp {
			engineTick += sinceGulp * tickRate
			sinceGulp = 0
		}
		got = p.step(playing(engineTick), display)
	}
	if off := math.Abs(got - engineTick); off > float64(score.PPQ)/8 {
		t.Errorf("after two minutes the drawn position is %.0f ticks from the engine's (%.0f vs %.0f)",
			off, got, engineTick)
	}
}

// TestPlayheadFreezesWhenTheEngineDoes: a count-in, a pause and a wait all
// keep frames flowing with the position deliberately parked. Gliding
// through a wait point would scroll the note the player is being waited on
// off the playhead.
func TestPlayheadFrozenStates(t *testing.T) {
	var p playhead
	p.step(playing(1000), 0)
	frozen := engine.PlayPos{Tick: 1000, TicksPerSecond: tickRate, Advancing: false}
	for i := 0; i < 120; i++ {
		if got := p.step(frozen, display); got != 1000 {
			t.Fatalf("frame %d of a frozen transport drew tick %.3f, want 1000", i, got)
		}
	}
}

// TestPlayheadSnapsOnSeek: a seek is a jump the position did not travel,
// and sliding across it would draw a scrub nobody performed.
func TestPlayheadSnapsOnSeek(t *testing.T) {
	var p playhead
	p.step(playing(1000), 0)
	p.step(playing(1000), display)

	seeked := engine.PlayPos{Tick: 9000, TicksPerSecond: tickRate, Advancing: true, Discontinuity: 4242}
	if got := p.step(seeked, display); got != 9000 {
		t.Errorf("after a seek the drawn position is %.1f, want the target 9000 at once", got)
	}
}

// TestPlayheadSnapsOnLoopWrap: the engine deliberately does NOT call a wrap
// a discontinuity, so the backward jump is all the playhead has to go on.
func TestPlayheadSnapsOnLoopWrap(t *testing.T) {
	var p playhead
	p.step(playing(7000), 0)
	p.step(playing(7000), display)
	if got := p.step(playing(100), display); got != 100 {
		t.Errorf("after a loop wrap the drawn position is %.1f, want the loop start 100", got)
	}
}

// TestPlayheadSnapsWhenFarOut: a tempo that changed under a loop, a window
// that was suspended, or publishing that stopped altogether. Past a
// sixteenth note this is no longer smoothing.
func TestPlayheadSnapsWhenFarOut(t *testing.T) {
	var p playhead
	p.step(playing(0), 0)
	p.step(playing(0), display)
	// A big forward jump with no discontinuity marked — the engine simply
	// got a long way ahead.
	far := float64(score.PPQ) * 4
	if got := p.step(playing(far), display); got != far {
		t.Errorf("drawn position %.1f did not snap to the engine's %.1f", got, far)
	}
}

// TestPlayheadHoldsWhenPublishingStops: if the audio stream dies the engine
// stops publishing, and the drawn position must coast to a stop rather than
// scroll the piece to its end on its own.
func TestPlayheadHoldsWhenPublishingStops(t *testing.T) {
	var p playhead
	p.step(playing(0), 0)
	last := 0.0
	for i := 0; i < 60*10; i++ { // ten seconds with no new publish
		last = p.step(playing(0), display)
	}
	if last > playheadSnapTicks {
		t.Errorf("ten seconds after publishing stopped the playhead had run %.0f ticks, want at most %.0f",
			last, playheadSnapTicks)
	}
}

// TestPlayheadUnarmedReadsTheEngine: a view that has never been updated —
// every layout and hit test the ui tests do without a game loop — still has
// to report where the engine actually is.
func TestPlayheadUnarmedReadsTheEngine(t *testing.T) {
	var p playhead
	if got := p.step(playing(5555), 0); got != 5555 {
		t.Errorf("an unarmed playhead read %.1f, want the engine's 5555", got)
	}
}

// TestPlayheadReadIsIdempotent: everything that draws or hit-tests reads
// the playhead with no time passing, many times a frame. Those reads must
// not advance it between themselves, or the position would depend on how
// many things happened to ask.
func TestPlayheadReadIsIdempotent(t *testing.T) {
	var p playhead
	p.step(playing(0), 0)
	want := p.step(playing(0), display)
	for i := 0; i < 20; i++ {
		if got := p.step(playing(0), 0); got != want {
			t.Fatalf("read %d moved the playhead: %.6f, want %.6f", i, got, want)
		}
	}
}

// TestAppPositionFollowsASeek ties the smoother back to the view: a seek
// made through the App must be visible to everything that draws, on the
// next read, without waiting for an Update.
func TestAppPositionFollowsASeek(t *testing.T) {
	a := newApp(t, 4)
	a.stepPlayhead()
	a.eng.SeekTick(3840)
	if got := a.posTick(); got != 3840 {
		t.Errorf("posTick after a seek = %d, want 3840", got)
	}
	if got, want := a.xAtTick(3840), screenW*playheadX; math.Abs(got-want) > 0.5 {
		t.Errorf("the sought tick draws at x=%.1f, want the playhead's %.1f", got, want)
	}
}

// --- drawing where the sound is ------------------------------------------

// TestSoundingTickWindsBackByTheBuffer is the correction itself: the
// engine's position is where the music is being RENDERED, and the drawn
// position has to be where it is being heard.
func TestSoundingTickWindsBackByTheBuffer(t *testing.T) {
	// 120 BPM at PPQ 960 is 1920 ticks a second, so a tenth of a second of
	// buffering is 192 ticks — a full sixteenth note.
	s := engine.PlayPos{Tick: 5000, TicksPerSecond: 1920, Advancing: true}
	got := soundingTick(5000, s, 0.1)
	if want := 5000.0 - 192; math.Abs(got-want) > 0.001 {
		t.Errorf("got %.1f, want %.1f", got, want)
	}
}

// TestSoundingTickScalesWithTempo checks that the compensation is a fixed
// amount of TIME and not a fixed number of ticks: the same buffer is more
// notation at a faster tempo, which is the whole reason it cannot be a
// constant.
func TestSoundingTickScalesWithTempo(t *testing.T) {
	slow := soundingTick(10000, engine.PlayPos{Tick: 10000, TicksPerSecond: 960, Advancing: true}, 0.1)
	fast := soundingTick(10000, engine.PlayPos{Tick: 10000, TicksPerSecond: 1920, Advancing: true}, 0.1)
	if 10000-fast <= 10000-slow {
		t.Errorf("compensated %.1f ticks at double tempo and %.1f at single; the faster one has to be bigger",
			10000-fast, 10000-slow)
	}
}

// TestSoundingTickLeavesAStoppedTransportAlone: a paused playhead is a
// cursor, not a clock. It is what a click seeks to and what A and B set
// the loop from, so it has to mean the tick the engine will resume at.
func TestSoundingTickLeavesAStoppedTransportAlone(t *testing.T) {
	for _, tt := range []struct {
		name string
		pos  engine.PlayPos
	}{
		{"paused", engine.PlayPos{Tick: 5000, TicksPerSecond: 1920, Advancing: false}},
		{"counting in", engine.PlayPos{Tick: 0, TicksPerSecond: 1920, Advancing: false}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := soundingTick(tt.pos.Tick, tt.pos, 0.1); got != tt.pos.Tick {
				t.Errorf("got %.1f, want the engine's own %.1f", got, tt.pos.Tick)
			}
		})
	}
}

// TestSoundingTickNeverGoesNegative: the first tenth of a second of a
// piece is buffer that has not reached the speakers, so the compensated
// position would be before the start of the score.
func TestSoundingTickNeverGoesNegative(t *testing.T) {
	s := engine.PlayPos{Tick: 50, TicksPerSecond: 1920, Advancing: true}
	if got := soundingTick(50, s, 0.1); got != 0 {
		t.Errorf("got %.1f, want 0", got)
	}
}

// TestSoundingTickIgnoresNothingToCompensate covers the duplex path, where
// the engine renders into the device callback and there is essentially no
// lead at all.
func TestSoundingTickIgnoresNothingToCompensate(t *testing.T) {
	s := engine.PlayPos{Tick: 5000, TicksPerSecond: 1920, Advancing: true}
	if got := soundingTick(5000, s, 0); got != 5000 {
		t.Errorf("got %.1f, want the position unchanged", got)
	}
}

// TestTrackLatencyTakesTheFirstReadingWhole: easing in from zero would
// draw the first second of every piece with a playhead sliding backwards
// into place.
func TestTrackLatencyTakesTheFirstReadingWhole(t *testing.T) {
	a := &App{outLatency: func() time.Duration { return 100 * time.Millisecond }}
	a.trackLatency()
	if math.Abs(a.latency-0.1) > 1e-9 {
		t.Errorf("the first reading settled at %.4f s, want the reported 0.1", a.latency)
	}
}

// TestTrackLatencySmoothsAndConverges: the reported figure is exact but
// jagged, so it is followed rather than taken, and it must actually get
// there.
func TestTrackLatencySmoothsAndConverges(t *testing.T) {
	reported := 100 * time.Millisecond
	a := &App{outLatency: func() time.Duration { return reported }}
	a.trackLatency()
	reported = 50 * time.Millisecond
	a.trackLatency()
	if a.latency >= 0.1 || a.latency <= 0.05 {
		t.Fatalf("after one step the estimate is %.4f s; it should be between the old 0.1 and the new 0.05", a.latency)
	}
	// A fifth of a second of display frames is enough to arrive.
	for i := 0; i < 12; i++ {
		a.trackLatency()
	}
	if math.Abs(a.latency-0.05) > 0.02 {
		t.Errorf("the estimate settled at %.4f s, want about 0.05", a.latency)
	}
}

// TestTrackLatencyRefusesNonsense guards against a reading that would
// drag the notation somewhere useless. Compensating by an absurd figure is
// worse than not compensating.
func TestTrackLatencyRefusesNonsense(t *testing.T) {
	for _, tt := range []struct {
		name     string
		reported time.Duration
		want     float64
	}{
		{"negative", -50 * time.Millisecond, 0},
		{"absurd", 5 * time.Second, latencyCap},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{outLatency: func() time.Duration { return tt.reported }}
			a.trackLatency()
			if math.Abs(a.latency-tt.want) > 1e-9 {
				t.Errorf("got %.4f s, want %.4f", a.latency, tt.want)
			}
		})
	}
}

// TestNoLatencyHookLeavesThePlayheadAlone: an integrator that wires
// nothing gets exactly the behaviour that existed before this was added.
func TestNoLatencyHookLeavesThePlayheadAlone(t *testing.T) {
	a := &App{}
	a.trackLatency()
	if a.latency != 0 || a.latencyArmed {
		t.Errorf("latency=%v armed=%v, want an untouched estimate", a.latency, a.latencyArmed)
	}
}
