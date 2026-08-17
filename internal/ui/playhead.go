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

import (
	"math"

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

// stepPlayhead advances the drawn position one display frame. Update calls
// it, once, and nothing else may: every other reader goes through posF or
// posTick, which read the same rules with no time passing.
func (a *App) stepPlayhead() {
	a.ph.step(a.eng.Pos(), a.displayStep())
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
func (a *App) posF() float64 { return a.ph.step(a.eng.Pos(), 0) }

// posTick is the drawn playback position in whole ticks — posF for the
// callers that want the engine's own unit (which bar, which verdict).
func (a *App) posTick() int64 { return int64(math.Round(a.posF())) }
