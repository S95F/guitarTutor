package ui

// The drawn playback position.
//
// The engine republishes where it is once per rendered block: every 10 ms
// on the live duplex path, and in coarser bursts behind oto, which refills
// a read-ahead buffer whenever the device has drained some of it. Drawn
// straight, that is a staircase — the notation stands still for two or
// three display frames and then jumps — and a staircase is what "moving
// from one beat to the next" looks like.
//
// A playhead fills in the gaps. It is deliberately NOT a second clock, the
// thing this package's doc comment rules out: it advances at the rate the
// engine publishes, and every step is pulled back toward the engine's own
// tick, so however long a session runs the display cannot wander away from
// the audio. What it adds is motion between publishes — and a refusal to
// invent motion the engine is not making. A seek, a loop wrap, a pause, a
// count-in and a wait all pin the drawn position to the engine's exactly.
//
// # Where the sound actually is
//
// The engine publishes its position as it RENDERS, and rendered audio has
// not been heard yet: it is sitting in the output device's buffer waiting
// its turn. So the engine's tick is not where the music is, it is where
// the music will be a buffer from now, and a playhead drawn straight from
// it runs AHEAD of what you can hear — by whatever the output path is
// holding, which on the oto path is up to a tenth of a second. A tenth of
// a second is a sixteenth note at 150 BPM. It is exactly the error that
// makes a player think they are dragging when they are not.
//
// The fix is to draw the position that is SOUNDING: the rendered position
// less however much rendered audio has not reached the speakers yet. That
// figure is measurable rather than guessable — oto reports what it is
// holding, and the duplex path renders into the device callback, where the
// lead is one period — so the view asks for it (SetOutputLatency) rather
// than carrying a constant. What no software can measure is the hardware's
// own last few milliseconds, which is what the manual trim in settings is
// for.
//
// This is a DISPLAY correction and nothing else. Scoring does not go
// anywhere near it: expected notes are stamped with the output frame they
// sound on and matched against detected input through the round-trip
// offset the calibration wizard measures, which is a different quantity
// arrived at a different way. Compensating twice would be worse than not
// compensating at all.

import (
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/score"
)

const (
	// playheadCatchUp is how hard the drawn position is pulled toward the
	// engine's, per second of display time. The engine's tick is a
	// staircase, so tracking it tightly would only redraw the steps; at
	// this gain the ripple of a 10 ms (or a bursty 50 ms) publish interval
	// is smoothed away, while a real offset is gone in about a third of a
	// second. The feed-forward term below carries the actual speed, so
	// this only ever has to correct error — it is not what makes the
	// playhead move.
	playheadCatchUp = 3.0
	// playheadSnapTicks is how far the drawn position may sit from the
	// engine's before interpolating stops being smoothing and starts being
	// a wrong answer: one sixteenth note. It is also what bounds the
	// damage if publishing stops altogether (a device unplugged mid-piece)
	// — the drawn position can coast a sixteenth past the last thing the
	// engine actually rendered, and then it holds.
	playheadSnapTicks = float64(score.PPQ) / 4
	// playheadFallbackTPS is the display rate assumed when Ebitengine
	// reports a non-positive one (it returns SyncWithFPS when ticks are
	// tied to the monitor). It only sets the size of one interpolation
	// step, and the reconciliation absorbs the error either way.
	playheadFallbackTPS = 60
	// latencySmooth is how fast the output-latency estimate follows the
	// figure the player reports, per display frame. The reading is exact
	// but jagged — oto's buffer empties as the device drains it and jumps
	// back up when the refill loop next runs — and an offset that jittered
	// with it would shake the notation. A real change in buffering is
	// followed in about a fifth of a second, which is faster than any
	// device changes its mind.
	latencySmooth = 0.08
	// latencyCap is the most compensation that will ever be applied. Half
	// a second is far past any sane output path; a figure beyond it means
	// something is wrong with the measurement, and drawing the notation
	// half a second behind the engine on the strength of a bad number is
	// worse than not compensating at all.
	latencyCap = 0.5
)

// A playhead carries the drawn playback position between the engine's
// publishes. The zero value is unarmed: the first reading of it snaps to
// wherever the engine says it is, so a view that has never been updated
// still draws the right position.
type playhead struct {
	tick  float64 // drawn position, in ticks
	armed bool    // tick means something (false until the first reading)

	lastTick float64 // engine tick at the previous reading
	lastDisc int64   // engine discontinuity frame at the previous reading
}

// step advances the drawn position by dt seconds of display time against
// the engine snapshot s, and returns it.
//
// dt is a parameter rather than something read here so that the whole rule
// is a plain function of its inputs: Update passes the display step, and
// the accessors that merely want to know where the playhead is pass 0.
// With dt == 0 the call is idempotent — it still applies every rule that
// says "do not interpolate, follow the engine exactly", which is what lets
// a caller read a correct position immediately after a seek without
// waiting for the next update.
func (p *playhead) step(s engine.PlayPos, dt float64) float64 {
	switch {
	case !p.armed:
		// Nothing to interpolate from yet.
		return p.snap(s)
	case s.Discontinuity != p.lastDisc:
		// A seek or a loop edit. The position did not travel there, and
		// gliding across the gap would draw a scrub nobody performed.
		return p.snap(s)
	case s.Tick < p.lastTick:
		// The engine went backwards. A loop wrap is the ordinary cause —
		// the engine deliberately does not call that a discontinuity,
		// because the notes near the loop end were still being answered
		// (see engine.DiscontinuityFrame) — but whatever the cause, the
		// display follows it whole rather than rewinding smoothly.
		return p.snap(s)
	case !s.Advancing:
		// Paused, stopped, counting in, or halted at a wait point. This is
		// the case the frame clock alone cannot see: frames keep flowing
		// and the engine keeps publishing, and the position must not move.
		return p.snap(s)
	}

	// Ordinary motion. The rate is the feed-forward term and does all the
	// travelling; the pull toward the engine's tick only removes error,
	// which is why a staircase target does not make the drawn position
	// step with it.
	p.tick += s.TicksPerSecond * dt
	p.tick += (s.Tick - p.tick) * playheadCatchUp * dt
	if math.Abs(s.Tick-p.tick) > playheadSnapTicks {
		// Too far out to be interpolation any more: a tempo that changed
		// under the loop, a window that was suspended, or publishing that
		// stopped. Truth wins.
		return p.snap(s)
	}
	p.lastTick, p.lastDisc = s.Tick, s.Discontinuity
	return p.tick
}

// snap pins the drawn position to the engine's and returns it.
func (p *playhead) snap(s engine.PlayPos) float64 {
	p.tick, p.armed = s.Tick, true
	p.lastTick, p.lastDisc = s.Tick, s.Discontinuity
	return p.tick
}

// --- what the view reads --------------------------------------------------

// stepPlayhead advances the drawn position one display frame, and follows
// the output latency the same frame. Update calls it, once, and nothing
// else may: every other reader goes through posF or posTick, which read
// the same rules with no time passing.
func (a *App) stepPlayhead() {
	a.ph.step(a.eng.Pos(), a.displayStep())
	a.trackLatency()
}

// SetOutputLatency installs the hook that reports how much rendered audio
// has not been heard yet. The view subtracts it so the playhead sits on
// the note that is sounding rather than on the one being rendered (see the
// file comment). It is called once per display frame and must be cheap and
// non-blocking; a nil hook, or one that reports zero, leaves the playhead
// exactly where the engine says it is.
//
// The UI cannot work this out for itself: what the number is depends on
// which output path the piece was opened on, and both of those live in the
// application rather than here.
func (a *App) SetOutputLatency(fn func() time.Duration) { a.outLatency = fn }

// trackLatency follows the reported output latency with a slow filter.
func (a *App) trackLatency() {
	if a.outLatency == nil {
		return
	}
	want := a.outLatency().Seconds()
	if !(want > 0) { // also catches NaN
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

// soundingTick converts a rendered position into the one that is audible,
// by winding it back through the output buffer at the rate the position is
// moving.
//
// Only while the position is ADVANCING. A paused or stopped transport is
// not filling the buffer with anything, so there is nothing in flight to
// compensate for — and more to the point, the playhead is then a cursor
// rather than a clock: it is what a click seeks to and what A and B set
// the loop from, and those have to mean the tick the engine will resume
// at. The cost is a hop of one buffer when playback stops, which is a
// tenth of a second of notation and the honest place for the head to
// land.
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

// displayStep is one display frame in seconds. Ebitengine calls Update at a
// fixed rate in game time — dropping calls rather than shortening them when
// it cannot keep up — so the nominal step is the honest one to advance by,
// and any shortfall is error the reconciliation removes.
func (a *App) displayStep() float64 {
	tps := float64(ebiten.TPS())
	if tps <= 0 {
		tps = playheadFallbackTPS
	}
	return 1 / tps
}

// posF is the drawn playback position in ticks, fractional. Everything the
// view draws, and everything the pointer is measured against, comes from
// here: what the user clicks has to be measured against the tab they can
// see, not against a position it is a few milliseconds away from.
func (a *App) posF() float64 {
	s := a.eng.Pos()
	return soundingTick(a.ph.step(s, 0), s, a.latency)
}

// posTick is the drawn playback position in whole ticks — posF for the
// callers that want the engine's own unit (which bar, which verdict).
func (a *App) posTick() int64 { return int64(math.Round(a.posF())) }
