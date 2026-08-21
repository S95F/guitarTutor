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
	"strings"
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
	warning       string
	// warnPosted records that SetLiveWarning was called at all since the
	// last drain, which is the only thing that tells a condition still
	// being asserted from one that has just happened again — the message
	// text is identical in both cases. syncLive clears it.
	warnPosted bool
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

	// Live warning banner. warnMsg is the game-loop copy of the message
	// the app layer last set and warnShown goes false when the user
	// dismisses it; see syncLive for what brings a dismissed banner back.
	warnMsg   string
	warnShown bool
	// warnAsserted is whether the previous drain saw a SetLiveWarning
	// call. A condition that is merely still true re-posts on every
	// frame, so this stays true for as long as it holds; a condition
	// that has happened again posts after a run of frames that did not,
	// and that lapse is what re-raises a dismissed banner.
	warnAsserted bool
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

// SetLiveWarning publishes a condition the user must see over the tab: a
// capture and playback device that are not the same clock, a hot-unplugged
// interface, a stream that died mid-session. The practice view only
// displays it — the app layer decides what is worth warning about, and
// clears the banner by setting "".
//
// Two shapes of caller share this one entry point, and the banner has to
// tell them apart (see syncLive): a condition that is polled re-posts the
// same text on every frame for as long as it holds, while an event —
// a failed reload, a device that would not open — posts once and then
// says nothing until it happens again. Anything polled must therefore
// keep posting every frame while the condition holds; a gap is read as
// the condition having gone away and come back.
//
// Safe from any goroutine.
func (a *App) SetLiveWarning(msg string) {
	a.feed.mu.Lock()
	a.feed.warning, a.feed.warnPosted = msg, true
	a.feed.mu.Unlock()
}

// warningVisible reports whether the banner should be drawn.
func (a *App) warningVisible() bool { return a.warnShown && a.warnMsg != "" }

// dismissWarning (the D key) hides the banner until the message changes.
func (a *App) dismissWarning() { a.warnShown = false }

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
	warn, posted := a.feed.warning, a.feed.warnPosted
	a.feed.warnPosted = false
	a.feed.mu.Unlock()

	// What re-raises a banner the user has dismissed.
	//
	// A different message always does. The hard case is the same message
	// twice, which is two different events wearing the same text: the
	// reload prompt fails, the user dismisses the banner and presses F5
	// again, and the identical error must come back — the key the prompt
	// is telling them to press has to do something visible — while a
	// dead stream that re-reports itself sixty times a second must stay
	// dismissed. The text cannot separate them, but the frame pattern
	// can: the polled condition posts on every frame, so a post that
	// follows a frame with no post at all is a fresh occurrence.
	switch {
	case warn != a.warnMsg:
		a.warnMsg, a.warnShown = warn, warn != ""
	case posted && !a.warnAsserted && warn != "":
		a.warnShown = true
	}
	a.warnAsserted = posted

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

// verdictAt returns the verdict to paint on the tab note at (start, str)
// with the playhead at pos.
//
// A verdict describes a note that has already sounded, so it only paints
// once the playhead has reached the note. The gate is what keeps loop
// passes honest: verdicts are keyed by tick and never cleared (results
// carry no pass number), so without it the second pass paints the first
// pass's hit or miss onto notes the player has not reached yet — an
// upcoming note showing green. Clearing the map at the loop boundary
// would not be enough on its own, because misses finalize seconds late
// (see advanceLagFrames in cmd/guitartutor) and the previous pass's
// results are still arriving well into the next one.
//
// Behind the playhead a stale verdict is the latest thing known about a
// note that has now been played again, and it is replaced when this
// pass's own result lands.
func (a *App) verdictAt(start int64, str int, pos int64) (practice.Verdict, bool) {
	if start > pos {
		return 0, false
	}
	v, ok := a.verdicts[noteKey{start, str}]
	return v, ok
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
	evs, _, ok := a.eng.WaitingOn()
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
	x0 := float32(screenW - meterW - uiPadX)
	y0 := float32(ptTracksY + 6)

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

	drawTextRight(screen, liveStatsLine(a.stats), screenW-uiPadX, float64(y0)+18, colHUD)
	if a.dropped > 0 {
		drawTextRight(screen, fmt.Sprintf("dropped %d samples", a.dropped), screenW-uiPadX, float64(y0)+34, colMiss)
	}
}

// liveStatsLine is the hit/close/miss readout. Accuracy() answers 1.0
// with nothing judged — the right neutral value for the scorer — but
// printed as "100%" the moment a session opens it is a grade nobody has
// earned yet, so the percentage waits for the first judged note.
func liveStatsLine(st practice.Stats) string {
	if st.Hit+st.Close+st.Miss == 0 {
		return "hit 0  close 0  miss 0"
	}
	return fmt.Sprintf("hit %d  close %d  miss %d  |  %.0f%%", st.Hit, st.Close, st.Miss, st.Accuracy()*100)
}

// The live-warning banner's interior. The banner is a fixed rectangle
// (ptWarnY/ptWarnH in transport.go) wedged between the track strip and
// the first string, so it cannot grow to fit its contents: the contents
// have to be fitted to it, in both directions.
const (
	// warnInsetX is how far inside the page margin the banner's own edges
	// sit, and warnPadX the inset from those edges to its text.
	warnInsetX = 16.0
	warnPadX   = 16.0
	// warnBorderW is the StrokeRect width. The stroke is centred on the
	// rectangle's edge, so it eats half that much off the interior at
	// the top and bottom — which is exactly what the dismiss hint's
	// descenders used to be drawn into.
	warnBorderW = 2.0
	// warnScale is the size the message is drawn at when it fits on one
	// line: a warning that invalidates every verdict below it should be
	// the largest thing on the screen when it can be.
	warnScale = 2.0
	// warnMinGapY is the smallest space allowed between the message
	// block and the hint. It only sets how many message lines are
	// affordable; any space left over beyond it is shared out evenly.
	warnMinGapY = 2.0
	warnHint    = "press D to dismiss"
)

// warnRect is the banner's outline. The drawing and the interior layout
// both read it so they cannot drift apart.
func warnRect() rect {
	x := uiPadX + warnInsetX
	return rect{x, ptWarnY, screenW - 2*x, ptWarnH}
}

// A warnLayout is the banner's interior, worked out from the faces' own
// metrics rather than from guessed pixel offsets. Both drawWarning and
// the layout tests read it, which is the only way a test can see what a
// window would show.
type warnLayout struct {
	// inner is the region inside the border and the side padding; every
	// line is centred in it and must fit its width.
	inner rect
	// lines is the message as it will actually be drawn: wrapped to
	// inner.w, and cut to the number of lines the fixed height affords
	// with the last one ellipsized so the loss is visible.
	lines []string
	// heading is true when the whole message fitted on one line and is
	// drawn at warnScale in the medium face; false when it had to wrap,
	// in which case the lines are body text. The distinction matters for
	// measurement: wrapTextW and truncateW measure the body face, Go
	// Medium is wider, and a line measured one way and drawn the other
	// overflows the box it was fitted to.
	heading bool
	// lineH is one message line's full box height (ascent + descent).
	lineH float64
	// msgY is the top of the first message line and hintY the top of the
	// hint, both absolute.
	msgY, hintY float64
}

// lineHeightOf is the vertical room one line drawn with f occupies.
// text/v2 puts the baseline an ascent below the y passed to Draw, so a
// descender's ink reaches ascent+descent below it — the measurement the
// banner's interior has to be built from, since its height is fixed.
func lineHeightOf(f *text.GoTextFace) float64 {
	m := f.Metrics()
	return m.HAscent + m.HDescent
}

// width is how wide s will be drawn in this layout's face.
func (l warnLayout) width(s string) float64 {
	if l.heading {
		return textWScaled(s, warnScale)
	}
	return textW(s)
}

// warnLayoutFor fits msg and the dismiss hint into the banner.
//
// The message is drawn large on one line when it fits that way. When it
// does not — a real split-device message names two Windows devices and
// runs some 1380 pixels even at scale 1, which used to be drawn straight
// across the window with its head and its tail clipped off both edges —
// it wraps to the banner's inner width instead, and is cut to the number
// of lines the fixed height affords. The vertical slack that is left is
// shared evenly above the message, between it and the hint, and below
// the hint, so the hint's descenders clear the bottom border instead of
// being drawn on it.
func warnLayoutFor(msg string) warnLayout {
	r := warnRect()
	l := warnLayout{inner: rect{
		r.x + warnPadX, r.y + warnBorderW,
		r.w - 2*warnPadX, r.h - 2*warnBorderW,
	}}

	// The hint and a wrapped message are both body text, so one line
	// height serves for both.
	bodyH := lineHeightOf(faceOf(srcBody, fontBody))
	headH := lineHeightOf(faceOf(srcMedium, fontBody*warnScale))
	// What is left for the message once the hint and the smallest
	// acceptable gap are reserved.
	budget := l.inner.h - bodyH - warnMinGapY

	if textWScaled(msg, warnScale) <= l.inner.w && headH <= budget {
		l.heading, l.lineH, l.lines = true, headH, []string{msg}
	} else {
		l.lineH = bodyH
		maxLines := int(budget / bodyH)
		if maxLines < 1 {
			maxLines = 1
		}
		lines := wrapTextW(msg, l.inner.w)
		if len(lines) > maxLines {
			// Fold everything that did not fit back into the last line
			// so truncateW ellipsizes it: a message that simply stopped
			// mid-sentence reads as the whole warning.
			tail := strings.Join(lines[maxLines-1:], " ")
			lines = append(lines[:maxLines-1], tail)
		}
		for i, s := range lines {
			// A single word wider than the banner survives wrapping
			// intact, so every line is fitted to the width, not just the
			// folded one.
			lines[i] = truncateW(s, l.inner.w)
		}
		l.lines = lines
	}

	gap := (l.inner.h - (float64(len(l.lines))*l.lineH + bodyH)) / 3
	l.msgY = l.inner.y + gap
	l.hintY = l.msgY + float64(len(l.lines))*l.lineH + gap
	return l
}

// drawWarning paints the live-warning banner across the top of the tab —
// deliberately in the way, because a split device pair or a dead stream
// invalidates every verdict below it. The text is scaled up when it fits
// on one line and wrapped to the banner's width when it does not, so a
// long message is never clipped by the window's edges.
func (a *App) drawWarning(screen *ebiten.Image) {
	r := warnRect()
	vector.DrawFilledRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), colWarnBG, false)
	vector.StrokeRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h), warnBorderW, colMiss, false)

	l := warnLayoutFor(a.warnMsg)
	y := l.msgY
	for _, s := range l.lines {
		if l.heading {
			drawTextScaled(screen, s, centreXScaled(s, l.inner.x, l.inner.w, warnScale), y, warnScale, colMiss)
		} else {
			drawText(screen, s, centreX(s, l.inner.x, l.inner.w), y, colMiss)
		}
		y += l.lineH
	}
	drawText(screen, warnHint, centreX(warnHint, l.inner.x, l.inner.w), l.hintY, colHUD)
}

// legendItems is the verdict colour key drawn under the timeline.
var legendItems = []struct {
	s   string
	col color.RGBA
}{{"legend:", colHUD}, {"hit", colHit}, {"close", colClose}, {"miss", colMiss}}

// legendGap is the space between one legend item and the next. It has to
// be a gap rather than a stride: the row used to advance by 7 pixels per
// character plus two cells, the bitmap font's arithmetic, which under a
// proportional face left the four items 15.7, 19.8 and 15.7 pixels apart
// — visibly ragged beside every other shaper-measured row, and wrong by
// however much a wider or non-ASCII label measures.
const legendGap = 14.0

// legendXs is where each legend item starts. Drawing and the spacing
// test read the same function, so the row cannot go ragged again without
// the test saying so.
func legendXs() []float64 {
	xs := make([]float64, len(legendItems))
	x := uiPadX
	for i, e := range legendItems {
		xs[i] = x
		x += textW(e.s) + legendGap
	}
	return xs
}

// drawLegend paints the verdict color legend under the timeline.
func (a *App) drawLegend(screen *ebiten.Image) {
	y := a.legendY()
	for i, x := range legendXs() {
		drawText(screen, legendItems[i].s, x, y, legendItems[i].col)
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
		s, scale := a.tunerIdleLine()
		drawTextScaled(screen, s, centreXScaled(s, 0, screenW, scale), 300, scale, colString)
		return
	}
	n := a.tunerNote
	inTune := math.Abs(n.Cents) <= 5
	col := colNote
	if inTune {
		col = colHit
	}
	// displayName, not keyName: a transposing wind player reads written
	// pitch, and a tuner that names the concert pitch instead would call
	// every correctly fingered note by the wrong name.
	name := fmt.Sprintf("%s %+.0fc", a.displayName(n.Key), n.Cents)
	scale := 6.0
	drawTextScaled(screen, name, centreXScaled(name, 0, screenW, scale), 250, scale, col)

	// Cents bar: -50 .. +50, center line, in-tune zone shaded.
	const barW, barH = 500, 20
	x0 := float32((screenW - barW) / 2)
	y0 := float32(370)
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

// tunerIdleLine is what the tuner overlay says while no note sounds, with
// the scale to draw it at. "listening..." is only true in a live session
// — without one the tuner feed never runs, and the overlay used to claim
// to listen forever. The honest line instead says what to change, so the
// message itself is the fix; being a sentence rather than a word, it is
// drawn smaller to stay inside the window.
func (a *App) tunerIdleLine() (string, float64) {
	if !a.live {
		return "no live input — " + a.liveRemedy(), 2
	}
	return "listening...", 3
}

// liveRemedy names the step that would turn live input on from where the
// user is standing. Naming the settings screen is only honest when one is
// wired behind S; a window started as `guitartutor play <file>` has no
// settings at all, and pointing its user at a greyed chip would send them
// in a circle.
func (a *App) liveRemedy() string {
	if a.settings != nil {
		return "choose your capture device in settings (S)"
	}
	return "quit and re-run with: guitartutor play -listen <file>"
}
