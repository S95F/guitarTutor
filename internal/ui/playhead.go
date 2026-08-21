package ui

import (
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/score"
)

const (
	playheadCatchUp = 3.0

	playheadSnapTicks = float64(score.PPQ) / 4

	playheadFallbackTPS = 60

	latencySmooth = 0.08

	latencyCap = 0.5
)

type playhead struct {
	tick  float64
	armed bool

	lastTick float64
	lastDisc int64
}

func (p *playhead) step(s engine.PlayPos, dt float64) float64 {
	switch {
	case !p.armed:

		return p.snap(s)
	case s.Discontinuity != p.lastDisc:

		return p.snap(s)
	case s.Tick < p.lastTick:

		return p.snap(s)
	case !s.Advancing:

		return p.snap(s)
	}

	p.tick += s.TicksPerSecond * dt
	p.tick += (s.Tick - p.tick) * playheadCatchUp * dt
	if math.Abs(s.Tick-p.tick) > playheadSnapTicks {

		return p.snap(s)
	}
	p.lastTick, p.lastDisc = s.Tick, s.Discontinuity
	return p.tick
}

func (p *playhead) snap(s engine.PlayPos) float64 {
	p.tick, p.armed = s.Tick, true
	p.lastTick, p.lastDisc = s.Tick, s.Discontinuity
	return p.tick
}

func (a *App) stepPlayhead() {
	a.ph.step(a.eng.Pos(), a.displayStep())
	a.trackLatency()
}

func (a *App) SetOutputLatency(fn func() time.Duration) { a.outLatency = fn }

func (a *App) trackLatency() {
	if a.outLatency == nil {
		return
	}
	want := a.outLatency().Seconds()
	if !(want > 0) {
		want = 0
	}
	if want > latencyCap {
		want = latencyCap
	}
	if !a.latencyArmed {
		a.latency, a.latencyArmed = want, true
		return
	}
	a.latency += (want - a.latency) * latencySmooth
}

func soundingTick(tick float64, s engine.PlayPos, latencySec float64) float64 {
	if !s.Advancing || latencySec <= 0 {
		return tick
	}
	tick -= latencySec * s.TicksPerSecond
	if tick < 0 {
		tick = 0
	}
	return tick
}

func (a *App) displayStep() float64 {
	tps := float64(ebiten.TPS())
	if tps <= 0 {
		tps = playheadFallbackTPS
	}
	return 1 / tps
}

func (a *App) posF() float64 {
	s := a.eng.Pos()
	return soundingTick(a.ph.step(s, 0), s, a.latency)
}

func (a *App) posTick() int64 { return int64(math.Round(a.posF())) }
