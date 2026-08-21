package ui

import (
	"math"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/score"
)

const tickRate = 2 * float64(score.PPQ)

const display = 1.0 / 60

func playing(tick float64) engine.PlayPos {
	return engine.PlayPos{Tick: tick, TicksPerSecond: tickRate, Advancing: true}
}

func TestPlayheadSlidesBetweenPublishes(t *testing.T) {
	var p playhead
	p.step(playing(0), 0)

	var seen []float64
	for i := 0; i < 3; i++ {
		seen = append(seen, p.step(playing(0), display))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("frame %d did not move: %v", i, seen)
		}
	}

	if got, want := seen[len(seen)-1], 3*display*tickRate; math.Abs(got-want) > want*0.25 {
		t.Errorf("three frames of gliding covered %.1f ticks, want about %.1f", got, want)
	}
}

func TestPlayheadDoesNotDrift(t *testing.T) {
	var p playhead

	const gulp = 0.05
	engineTick, sinceGulp, got := 0.0, 0.0, 0.0
	p.step(playing(0), 0)
	for i := 0; i < 60*120; i++ {
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

func TestPlayheadSnapsOnSeek(t *testing.T) {
	var p playhead
	p.step(playing(1000), 0)
	p.step(playing(1000), display)

	seeked := engine.PlayPos{Tick: 9000, TicksPerSecond: tickRate, Advancing: true, Discontinuity: 4242}
	if got := p.step(seeked, display); got != 9000 {
		t.Errorf("after a seek the drawn position is %.1f, want the target 9000 at once", got)
	}
}

func TestPlayheadSnapsOnLoopWrap(t *testing.T) {
	var p playhead
	p.step(playing(7000), 0)
	p.step(playing(7000), display)
	if got := p.step(playing(100), display); got != 100 {
		t.Errorf("after a loop wrap the drawn position is %.1f, want the loop start 100", got)
	}
}

func TestPlayheadSnapsWhenFarOut(t *testing.T) {
	var p playhead
	p.step(playing(0), 0)
	p.step(playing(0), display)

	far := float64(score.PPQ) * 4
	if got := p.step(playing(far), display); got != far {
		t.Errorf("drawn position %.1f did not snap to the engine's %.1f", got, far)
	}
}

func TestPlayheadHoldsWhenPublishingStops(t *testing.T) {
	var p playhead
	p.step(playing(0), 0)
	last := 0.0
	for i := 0; i < 60*10; i++ {
		last = p.step(playing(0), display)
	}
	if last > playheadSnapTicks {
		t.Errorf("ten seconds after publishing stopped the playhead had run %.0f ticks, want at most %.0f",
			last, playheadSnapTicks)
	}
}

func TestPlayheadUnarmedReadsTheEngine(t *testing.T) {
	var p playhead
	if got := p.step(playing(5555), 0); got != 5555 {
		t.Errorf("an unarmed playhead read %.1f, want the engine's 5555", got)
	}
}

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

func TestSoundingTickWindsBackByTheBuffer(t *testing.T) {

	s := engine.PlayPos{Tick: 5000, TicksPerSecond: 1920, Advancing: true}
	got := soundingTick(5000, s, 0.1)
	if want := 5000.0 - 192; math.Abs(got-want) > 0.001 {
		t.Errorf("got %.1f, want %.1f", got, want)
	}
}

func TestSoundingTickScalesWithTempo(t *testing.T) {
	slow := soundingTick(10000, engine.PlayPos{Tick: 10000, TicksPerSecond: 960, Advancing: true}, 0.1)
	fast := soundingTick(10000, engine.PlayPos{Tick: 10000, TicksPerSecond: 1920, Advancing: true}, 0.1)
	if 10000-fast <= 10000-slow {
		t.Errorf("compensated %.1f ticks at double tempo and %.1f at single; the faster one has to be bigger",
			10000-fast, 10000-slow)
	}
}

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

func TestSoundingTickNeverGoesNegative(t *testing.T) {
	s := engine.PlayPos{Tick: 50, TicksPerSecond: 1920, Advancing: true}
	if got := soundingTick(50, s, 0.1); got != 0 {
		t.Errorf("got %.1f, want 0", got)
	}
}

func TestSoundingTickIgnoresNothingToCompensate(t *testing.T) {
	s := engine.PlayPos{Tick: 5000, TicksPerSecond: 1920, Advancing: true}
	if got := soundingTick(5000, s, 0); got != 5000 {
		t.Errorf("got %.1f, want the position unchanged", got)
	}
}

func TestTrackLatencyTakesTheFirstReadingWhole(t *testing.T) {
	a := &App{outLatency: func() time.Duration { return 100 * time.Millisecond }}
	a.trackLatency()
	if math.Abs(a.latency-0.1) > 1e-9 {
		t.Errorf("the first reading settled at %.4f s, want the reported 0.1", a.latency)
	}
}

func TestTrackLatencySmoothsAndConverges(t *testing.T) {
	reported := 100 * time.Millisecond
	a := &App{outLatency: func() time.Duration { return reported }}
	a.trackLatency()
	reported = 50 * time.Millisecond
	a.trackLatency()
	if a.latency >= 0.1 || a.latency <= 0.05 {
		t.Fatalf("after one step the estimate is %.4f s; it should be between the old 0.1 and the new 0.05", a.latency)
	}

	for i := 0; i < 12; i++ {
		a.trackLatency()
	}
	if math.Abs(a.latency-0.05) > 0.02 {
		t.Errorf("the estimate settled at %.4f s, want about 0.05", a.latency)
	}
}

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

func TestNoLatencyHookLeavesThePlayheadAlone(t *testing.T) {
	a := &App{}
	a.trackLatency()
	if a.latency != 0 || a.latencyArmed {
		t.Errorf("latency=%v armed=%v, want an untouched estimate", a.latency, a.latencyArmed)
	}
}
