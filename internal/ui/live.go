package ui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/practice"
)

type noteKey struct {
	start int64
	str   int
}

type feed struct {
	mu            sync.Mutex
	results       []practice.NoteResult
	tunerNote     pitch.Note
	tunerSounding bool
	status        func() (levelDB float64, dropped int64)
	waitCtl       bool
	warning       string

	warnPosted bool
}

type liveUI struct {
	feed feed

	frame     int64
	tunerView bool
	wait      bool

	live          bool
	waitCtl       bool
	levelDB       float64
	dropped       int64
	tunerNote     pitch.Note
	tunerSounding bool
	stats         practice.Stats
	verdicts      map[noteKey]practice.Verdict

	warnMsg   string
	warnShown bool

	warnAsserted bool

	stallLastFrames int64
	stallCount      int
	stallPrev       string
	stalled         bool
}

const (
	audioStallWarning = "no sound is coming out — playback is stalled; press S for settings and check the playback device"
	audioStallAfter   = 150
)

func (a *App) checkAudioStalled() {
	if !a.eng.Playing() {
		a.stallCount = 0
		return
	}
	f := a.eng.TotalFrames()
	if f != a.stallLastFrames {
		a.stallLastFrames = f
		a.stallCount = 0
		if a.stalled {
			a.stalled = false
			a.SetLiveWarning(a.stallPrev)
		}
		return
	}
	if a.stalled {
		return
	}
	a.stallCount++
	if a.stallCount >= audioStallAfter {
		a.stalled = true
		a.stallPrev = a.warnMsg
		a.SetLiveWarning(audioStallWarning)
	}
}

func (a *App) OfferResults(rs []practice.NoteResult) {
	if len(rs) == 0 {
		return
	}
	a.feed.mu.Lock()
	a.feed.results = append(a.feed.results, rs...)
	a.feed.mu.Unlock()
}

func (a *App) OfferTuner(n pitch.Note, sounding bool) {
	a.feed.mu.Lock()
	a.feed.tunerNote, a.feed.tunerSounding = n, sounding
	a.feed.mu.Unlock()
}

func (a *App) SetLiveStatus(fn func() (levelDB float64, dropped int64)) {
	a.feed.mu.Lock()
	a.feed.status = fn
	a.feed.mu.Unlock()
}

func (a *App) SetWaitControl(enabled bool) {
	a.feed.mu.Lock()
	a.feed.waitCtl = enabled
	a.feed.mu.Unlock()
}

func (a *App) SetLiveWarning(msg string) {
	a.feed.mu.Lock()
	a.feed.warning, a.feed.warnPosted = msg, true
	a.feed.mu.Unlock()
}

func (a *App) warningVisible() bool { return a.warnShown && a.warnMsg != "" }

func (a *App) dismissWarning() { a.warnShown = false }

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

func (a *App) verdictAt(start int64, str int, pos int64) (practice.Verdict, bool) {
	if start > pos {
		return 0, false
	}
	v, ok := a.verdicts[noteKey{start, str}]
	return v, ok
}

func (a *App) toggleWait() {
	a.wait = !a.wait
	a.eng.SetWaitMode(a.wait)
}

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

func (a *App) pulseCol() color.RGBA {
	p := 0.5 + 0.5*math.Sin(float64(a.frame)*0.15)
	return lerpRGBA(colWaitLo, colWaitHi, p)
}

func lerpRGBA(c0, c1 color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x) + t*(float64(y)-float64(x)) + 0.5) }
	return color.RGBA{l(c0.R, c1.R), l(c0.G, c1.G), l(c0.B, c1.B), 255}
}

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

	tx := x0 + meterW*float32(57.0/60.0)
	vector.StrokeLine(screen, tx, y0-2, tx, y0+meterH+2, 1, colBarline, false)

	drawTextRight(screen, liveStatsLine(a.stats), screenW-uiPadX, float64(y0)+18, colHUD)
	if a.dropped > 0 {
		drawTextRight(screen, fmt.Sprintf("dropped %d samples", a.dropped), screenW-uiPadX, float64(y0)+34, colMiss)
	}
}

func liveStatsLine(st practice.Stats) string {
	if st.Hit+st.Close+st.Miss == 0 {
		return "hit 0  close 0  miss 0"
	}
	return fmt.Sprintf("hit %d  close %d  miss %d  |  %.0f%%", st.Hit, st.Close, st.Miss, st.Accuracy()*100)
}

const (
	warnInsetX = 16.0
	warnPadX   = 16.0

	warnBorderW = 2.0

	warnScale = 2.0

	warnMinGapY = 2.0
	warnHint    = "press D to dismiss"
)

func warnRect() rect {
	x := uiPadX + warnInsetX
	return rect{x, ptWarnY, screenW - 2*x, ptWarnH}
}

type warnLayout struct {
	inner rect

	lines []string

	heading bool

	lineH float64

	msgY, hintY float64
}

func lineHeightOf(f *text.GoTextFace) float64 {
	m := f.Metrics()
	return m.HAscent + m.HDescent
}

func (l warnLayout) width(s string) float64 {
	if l.heading {
		return textWScaled(s, warnScale)
	}
	return textW(s)
}

func warnLayoutFor(msg string) warnLayout {
	r := warnRect()
	l := warnLayout{inner: rect{
		r.x + warnPadX, r.y + warnBorderW,
		r.w - 2*warnPadX, r.h - 2*warnBorderW,
	}}

	bodyH := lineHeightOf(faceOf(srcBody, fontBody))
	headH := lineHeightOf(faceOf(srcMedium, fontBody*warnScale))

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

			tail := strings.Join(lines[maxLines-1:], " ")
			lines = append(lines[:maxLines-1], tail)
		}
		for i, s := range lines {

			lines[i] = truncateW(s, l.inner.w)
		}
		l.lines = lines
	}

	gap := (l.inner.h - (float64(len(l.lines))*l.lineH + bodyH)) / 3
	l.msgY = l.inner.y + gap
	l.hintY = l.msgY + float64(len(l.lines))*l.lineH + gap
	return l
}

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

var legendItems = []struct {
	s   string
	col color.RGBA
}{{"legend:", colHUD}, {"hit", colHit}, {"close", colClose}, {"miss", colMiss}}

const legendGap = 14.0

func legendXs() []float64 {
	xs := make([]float64, len(legendItems))
	x := uiPadX
	for i, e := range legendItems {
		xs[i] = x
		x += textW(e.s) + legendGap
	}
	return xs
}

func (a *App) drawLegend(screen *ebiten.Image) {
	y := a.legendY()
	for i, x := range legendXs() {
		drawText(screen, legendItems[i].s, x, y, legendItems[i].col)
	}
}

var keyNames = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

func keyName(key int) string {
	return fmt.Sprintf("%s%d", keyNames[((key%12)+12)%12], key/12-1)
}

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

	name := fmt.Sprintf("%s %+.0fc", a.displayName(n.Key), n.Cents)
	scale := 6.0
	drawTextScaled(screen, name, centreXScaled(name, 0, screenW, scale), 250, scale, col)

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

func (a *App) tunerIdleLine() (string, float64) {
	if !a.live {
		return "no live input — " + a.liveRemedy(), 2
	}
	return "listening...", 3
}

func (a *App) liveRemedy() string {
	if a.settings != nil {
		return "choose your capture device in settings (S)"
	}
	return "quit and re-run with: musictutor play -listen <file>"
}
