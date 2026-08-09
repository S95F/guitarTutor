package ui

// Live-session feedback (Phase 2): verdict painting, the accuracy HUD,
// the tuner overlay, and wait mode. The cmd layer feeds an App from its
// analysis goroutines through the Offer*/Set* methods below; those only
// fill a mutex-guarded mailbox, and syncLive — run at the top of every
// Update, on the game loop — merges the mailbox into plain fields that
// Draw reads without locking. With no feeds set the mailbox stays empty,
// live stays false, and the view behaves exactly as Phase 1.

import (
	"fmt"
	"image/color"
	"math"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/practice"
)

// noteKey identifies one tab note for verdict and wait-point matching:
// the attack tick and the string. Two strings sounding at the same tick
// (a chord) key independently.
type noteKey struct {
	start int64
	str   int
}

// feed is the mailbox written by non-game-loop goroutines and drained by
// syncLive. Critical sections are tiny: append a slice or copy a value.
type feed struct {
	mu            sync.Mutex
	results       []practice.NoteResult
	tunerNote     pitch.Note
	tunerSounding bool
	status        func() (levelDB float64, dropped int64)
	waitCtl       bool
}

// liveUI is the App's Phase 2 state: the mailbox plus the game-loop-owned
// merge of it. Everything below feed is written only by syncLive (game
// loop) and read by Update/Draw, so it needs no locking.
type liveUI struct {
	feed feed

	frame     int64 // Update counter, drives the wait pulse
	tunerView bool  // T: tuner overlay replaces the tab
	wait      bool  // mirrored engine wait mode (set-only config)

	live          bool // a live status fn is installed
	waitCtl       bool // W key enabled
	levelDB       float64
	dropped       int64
	tunerNote     pitch.Note
	tunerSounding bool
	stats         practice.Stats
	verdicts      map[noteKey]practice.Verdict
}

// OfferResults queues verdicts for the next frame. Safe from any
// goroutine. The latest verdict for a note wins: loops re-judge each
// pass, and every result also accumulates into the running stats.
func (a *App) OfferResults(rs []practice.NoteResult) {
	if len(rs) == 0 {
		return
	}
	a.feed.mu.Lock()
	a.feed.results = append(a.feed.results, rs...)
	a.feed.mu.Unlock()
}

// OfferTuner publishes the currently detected note for the tuner overlay.
// sounding=false means nothing is sounding right now (the overlay shows
// "listening..."). Safe from any goroutine; the latest call wins.
func (a *App) OfferTuner(n pitch.Note, sounding bool) {
	a.feed.mu.Lock()
	a.feed.tunerNote, a.feed.tunerSounding = n, sounding
	a.feed.mu.Unlock()
}

// SetLiveStatus installs the input-meter poll, called once per frame from
// the game loop for the level meter and dropped-sample warning. nil means
// no live session: the accuracy HUD and meter stay hidden. Safe from any
// goroutine.
func (a *App) SetLiveStatus(fn func() (levelDB float64, dropped int64)) {
	a.feed.mu.Lock()
	a.feed.status = fn
	a.feed.mu.Unlock()
}

// SetWaitControl enables or disables the W key (engine wait-mode toggle).
// Off by default: without a detector to confirm waits, toggling wait mode
// would freeze playback with no way forward. Safe from any goroutine.
func (a *App) SetWaitControl(enabled bool) {
	a.feed.mu.Lock()
	a.feed.waitCtl = enabled
	a.feed.mu.Unlock()
}

// syncLive drains the mailbox into game-loop-owned state. Update calls it
// every frame; tests call it directly so no window is ever needed. The
// status callback runs outside the mailbox lock — it may take the live
// session's own locks.
func (a *App) syncLive() {
	a.feed.mu.Lock()
	rs := a.feed.results
	a.feed.results = nil
	a.tunerNote, a.tunerSounding = a.feed.tunerNote, a.feed.tunerSounding
	status := a.feed.status
	a.waitCtl = a.feed.waitCtl
	a.feed.mu.Unlock()

	if len(rs) > 0 && a.verdicts == nil {
		a.verdicts = make(map[noteKey]practice.Verdict)
	}
	for _, r := range rs {
		a.verdicts[noteKey{r.Event.Start, r.Event.String}] = r.Verdict
		switch r.Verdict {
		case practice.VerdictHit:
			a.stats.Hit++
		case practice.VerdictClose:
			a.stats.Close++
		default:
			a.stats.Miss++
		}
	}

	a.live = status != nil
	if status != nil {
		a.levelDB, a.dropped = status()
	}
}

// toggleWait (the W key, only with SetWaitControl(true)) flips engine
// wait mode. Like the metronome, wait mode has no engine-side getter, so
// the UI mirrors it for the HUD.
func (a *App) toggleWait() {
	a.wait = !a.wait
	a.eng.SetWaitMode(a.wait)
}

// verdictColor maps a verdict to its tint.
func verdictColor(v practice.Verdict) color.RGBA {
	switch v {
	case practice.VerdictHit:
		return colHit
	case practice.VerdictClose:
		return colClose
	default:
		return colMiss
	}
}

// waitingKeys returns the note keys the engine is waiting on this frame,
// or nil when not waiting.
func (a *App) waitingKeys() map[noteKey]bool {
	evs, ok := a.eng.WaitingOn()
	if !ok || len(evs) == 0 {
		return nil
	}
	m := make(map[noteKey]bool, len(evs))
	for _, ev := range evs {
		m[noteKey{ev.Start, ev.String}] = true
	}
	return m
}

// pulseCol oscillates between two ambers with the frame counter — the
// highlight for notes being waited on and the WAITING banner.
func (a *App) pulseCol() color.RGBA {
	p := 0.5 + 0.5*math.Sin(float64(a.frame)*0.15)
	return lerpRGBA(colWaitLo, colWaitHi, p)
}

func lerpRGBA(c0, c1 color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x) + t*(float64(y)-float64(x)) + 0.5) }
	return color.RGBA{l(c0.R, c1.R), l(c0.G, c1.G), l(c0.B, c1.B), 255}
}

// drawLiveHUD paints the top-right live block: input level meter,
// hit/close/miss counts with accuracy, and the dropped-samples warning.
// Only called when a.live.
func (a *App) drawLiveHUD(screen *ebiten.Image) {
	const meterW, meterH = 180, 10
	x0 := float32(screenW - meterW - 24)
	y0 := float32(18)

	drawText(screen, "in", float64(x0)-22, float64(y0)-3, colHUD)
	vector.StrokeRect(screen, x0, y0, meterW, meterH, 1, colString, false)
	lv := a.levelDB
	if lv < -60 {
		lv = -60
	} else if lv > 0 {
		lv = 0
	}
	fc := colHit
	if a.levelDB > -3 {
		fc = colMiss
	}
	vector.DrawFilledRect(screen, x0+1, y0+1, (meterW-2)*float32((lv+60)/60), meterH-2, fc, false)
	// The -3 dBFS hot threshold.
	tx := x0 + meterW*float32(57.0/60.0)
	vector.StrokeLine(screen, tx, y0-2, tx, y0+meterH+2, 1, colBarline, false)

	st := a.stats
	s := fmt.Sprintf("hit %d  close %d  miss %d  |  %.0f%%", st.Hit, st.Close, st.Miss, st.Accuracy()*100)
	drawText(screen, s, screenW-24-float64(7*len(s)), 36, colHUD)
	if a.dropped > 0 {
		w := fmt.Sprintf("dropped %d samples", a.dropped)
		drawText(screen, w, screenW-24-float64(7*len(w)), 54, colMiss)
	}
}

// drawLegend paints the verdict color legend above the help line.
func (a *App) drawLegend(screen *ebiten.Image) {
	y := float64(screenH - 44)
	x := 16.0
	for _, e := range []struct {
		s   string
		col color.RGBA
	}{{"legend:", colHUD}, {"hit", colHit}, {"close", colClose}, {"miss", colMiss}} {
		drawText(screen, e.s, x, y, e.col)
		x += float64(7 * (len(e.s) + 2))
	}
}

var keyNames = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// keyName renders a MIDI key as a note name with octave (E2 = 40).
func keyName(key int) string {
	return fmt.Sprintf("%s%d", keyNames[((key%12)+12)%12], key/12-1)
}

// drawTuner paints the tuner overlay in place of the tab: a big note name
// with the cents deviation and a -50..+50 cents bar. basicfont is
// ASCII-only, so cents take a plain "c" suffix rather than a cent sign.
func (a *App) drawTuner(screen *ebiten.Image) {
	if !a.tunerSounding {
		s := "listening..."
		scale := 3.0
		drawTextScaled(screen, s, (screenW-float64(7*len(s))*scale)/2, 280, scale, colString)
		return
	}
	n := a.tunerNote
	inTune := math.Abs(n.Cents) <= 5
	col := colNote
	if inTune {
		col = colHit
	}
	name := fmt.Sprintf("%s %+.0fc", keyName(n.Key), n.Cents)
	scale := 6.0
	drawTextScaled(screen, name, (screenW-float64(7*len(name))*scale)/2, 200, scale, col)

	// Cents bar: -50 .. +50, center line, in-tune zone shaded.
	const barW, barH = 500, 20
	x0 := float32((screenW - barW) / 2)
	y0 := float32(360)
	cx := x0 + barW/2
	zone := float32(5.0 / 50.0 * barW / 2)
	vector.DrawFilledRect(screen, cx-zone, y0, 2*zone, barH, colTuneZone, false)
	vector.StrokeRect(screen, x0, y0, barW, barH, 1, colString, false)
	vector.StrokeLine(screen, cx, y0-6, cx, y0+barH+6, 1, colHUD, false)

	c := n.Cents
	if c > 50 {
		c = 50
	} else if c < -50 {
		c = -50
	}
	mcol := colClose
	if inTune {
		mcol = colHit
	}
	vector.DrawFilledRect(screen, cx+float32(c/50)*barW/2-3, y0-4, 6, barH+8, mcol, false)
	drawText(screen, "-50", float64(x0)-30, float64(y0)+4, colBarline)
	drawText(screen, "+50", float64(x0)+barW+8, float64(y0)+4, colBarline)
}

// drawTextScaled is drawText with a uniform scale (basicfont has no
// sizes, so big text is scaled-up 7x13).
func drawTextScaled(dst *ebiten.Image, s string, x, y, scale float64, col color.RGBA) {
	op := &text.DrawOptions{}
	op.GeoM.Scale(scale, scale)
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(col)
	text.Draw(dst, s, face, op)
}
