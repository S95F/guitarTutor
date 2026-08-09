// Package ui is the Ebitengine practice view: a scrolling tablature with a
// fixed playhead, driven entirely by the engine's clock. The UI never keeps
// its own notion of time — every frame it asks the engine where it is
// (PosTick) and draws accordingly, so display and audio cannot drift.
package ui

import (
	"fmt"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/font/basicfont"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/score"
)

const (
	screenW = 1280
	screenH = 720

	playheadX  = 0.3 // fraction of width where "now" sits
	stringGap  = 26
	tabTop     = 160
	defaultPxQ = 96 // pixels per quarter note at zoom 1
)

var (
	colBG       = color.RGBA{18, 18, 24, 255}
	colString   = color.RGBA{90, 90, 100, 255}
	colBarline  = color.RGBA{60, 60, 72, 255}
	colNote     = color.RGBA{235, 235, 235, 255}
	colNoteBG   = color.RGBA{18, 18, 24, 255}
	colSounding = color.RGBA{255, 200, 60, 255}
	colInferred = color.RGBA{120, 200, 255, 255}
	colPlayhead = color.RGBA{230, 70, 70, 255}
	colLoop     = color.RGBA{60, 160, 90, 60}
	colLoopEdge = color.RGBA{60, 160, 90, 255}
	colHUD      = color.RGBA{200, 200, 210, 255}
	colCountIn  = color.RGBA{255, 200, 60, 255}

	// Phase 2 live feedback (live.go).
	colHit      = color.RGBA{80, 210, 120, 255}  // verdict: hit
	colClose    = color.RGBA{240, 160, 50, 255}  // verdict: close
	colMiss     = color.RGBA{235, 80, 80, 255}   // verdict: miss
	colWaitLo   = color.RGBA{180, 140, 40, 255}  // wait pulse, dim phase
	colWaitHi   = color.RGBA{255, 245, 180, 255} // wait pulse, bright phase
	colTuneZone = color.RGBA{40, 90, 55, 255}    // tuner in-tune band
)

var face = text.NewGoXFace(basicfont.Face7x13)

// App is the ebiten.Game for one practice session.
type App struct {
	eng   *engine.Engine
	sc    *score.Score
	track int // index of the displayed (tab) track

	zoom float64
	// The metronome and ramp have no engine-side getters (set-only
	// config), so the UI mirrors their state for the HUD and toggles.
	metronome bool
	ramp      bool

	// liveUI carries the Phase 2 live-feedback state and feed mailbox
	// (live.go). Its zero value is fully inert: no feeds, Phase 1
	// behavior.
	liveUI
}

// New builds the practice view. track is the index into sc.Tracks to
// display as tablature (play/mute always applies to all tracks).
func New(eng *engine.Engine, sc *score.Score, track int) *App {
	return &App{eng: eng, sc: sc, track: track, zoom: 1}
}

// Run opens the window and blocks until the user quits.
func (a *App) Run() error {
	ebiten.SetWindowSize(screenW, screenH)
	title := a.sc.Title
	if title == "" {
		title = "guitarTutor"
	} else {
		title = "guitarTutor — " + title
	}
	ebiten.SetWindowTitle(title)
	err := ebiten.RunGame(a)
	if err == errQuit {
		return nil
	}
	return err
}

var errQuit = fmt.Errorf("quit")

func (a *App) pxPerTick() float64 {
	return a.zoom * defaultPxQ / float64(score.PPQ)
}

func (a *App) displayed() *score.Track { return a.sc.Tracks[a.track] }

// barAt returns the index in the displayed track's bars containing tick.
func (a *App) barAt(tick int64) int {
	bars := a.displayed().Bars
	for i, b := range bars {
		if tick < b.Start+b.Len() {
			return i
		}
	}
	return len(bars) - 1
}

// Update handles input. All controls go straight to the engine.
func (a *App) Update() error {
	a.frame++
	a.syncLive()
	eng, bars := a.eng, a.displayed().Bars

	switch {
	case inpututil.IsKeyJustPressed(ebiten.KeyEscape), inpututil.IsKeyJustPressed(ebiten.KeyQ):
		return errQuit
	case inpututil.IsKeyJustPressed(ebiten.KeySpace):
		if eng.Playing() {
			eng.Pause()
		} else {
			eng.Play()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyHome):
		eng.SeekTick(0)
	case inpututil.IsKeyJustPressed(ebiten.KeyLeft):
		if i := a.barAt(eng.PosTick()); i >= 0 {
			// To the start of this bar; if already near it, the bar before.
			b := bars[i]
			if eng.PosTick()-b.Start < b.Len()/8 && i > 0 {
				eng.SeekTick(bars[i-1].Start)
			} else {
				eng.SeekTick(b.Start)
			}
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyRight):
		if i := a.barAt(eng.PosTick()); i+1 < len(bars) {
			eng.SeekTick(bars[i+1].Start)
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyUp):
		eng.SetTempoScale(eng.TempoScale() + 0.05)
	case inpututil.IsKeyJustPressed(ebiten.KeyDown):
		eng.SetTempoScale(eng.TempoScale() - 0.05)
	case inpututil.IsKeyJustPressed(ebiten.KeyA):
		a.loopSetA()
	case inpututil.IsKeyJustPressed(ebiten.KeyB):
		a.loopSetB()
	case inpututil.IsKeyJustPressed(ebiten.KeyL):
		if _, _, on := eng.Loop(); on {
			eng.ClearLoop()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyM):
		a.toggleMetronome()
	case inpututil.IsKeyJustPressed(ebiten.KeyR):
		a.toggleRamp()
	case inpututil.IsKeyJustPressed(ebiten.KeyT):
		a.tunerView = !a.tunerView
	case inpututil.IsKeyJustPressed(ebiten.KeyW):
		if a.waitCtl {
			a.toggleWait()
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyEqual), inpututil.IsKeyJustPressed(ebiten.KeyKPAdd):
		if a.zoom < 4 {
			a.zoom *= 1.25
		}
	case inpututil.IsKeyJustPressed(ebiten.KeyMinus), inpututil.IsKeyJustPressed(ebiten.KeyKPSubtract):
		if a.zoom > 0.3 {
			a.zoom /= 1.25
		}
	}
	for i := 0; i < len(a.sc.Tracks) && i < 9; i++ {
		if inpututil.IsKeyJustPressed(ebiten.Key1 + ebiten.Key(i)) {
			a.eng.SetTrackMuted(i, !a.eng.TrackMuted(i))
		}
	}
	return nil
}

// loopSetA (the A key) sets the loop start to the current bar, keeping the
// end if a loop is already set past it. No-op when the displayed track has
// no bars (barAt returns -1).
func (a *App) loopSetA() {
	i := a.barAt(a.eng.PosTick())
	if i < 0 {
		return
	}
	b := a.displayed().Bars[i]
	_, end, on := a.eng.Loop()
	if !on || end <= b.Start {
		end = b.Start + b.Len()
	}
	a.eng.SetLoop(b.Start, end)
}

// loopSetB (the B key) sets the loop end to the current bar's end, keeping
// the start if a loop is already set before it. No-op when the displayed
// track has no bars.
func (a *App) loopSetB() {
	i := a.barAt(a.eng.PosTick())
	if i < 0 {
		return
	}
	b := a.displayed().Bars[i]
	start, _, on := a.eng.Loop()
	if !on || start >= b.Start+b.Len() {
		start = b.Start
	}
	a.eng.SetLoop(start, b.Start+b.Len())
}

func (a *App) toggleMetronome() {
	a.metronome = !a.metronome
	a.eng.SetMetronome(a.metronome)
}

func (a *App) toggleRamp() {
	a.ramp = !a.ramp
	a.eng.SetRamp(engine.RampConfig{Enabled: a.ramp, Increment: 0.05, Target: 1.0})
}

// SetInitialMetronome records the metronome state chosen at startup so the
// M key toggles from the right place.
func (a *App) SetInitialMetronome(on bool) { a.metronome = on }

// Draw renders the frame from engine state. The tuner overlay (T)
// replaces the tab; the HUD and transport keys stay active either way.
func (a *App) Draw(screen *ebiten.Image) {
	screen.Fill(colBG)
	if a.tunerView {
		a.drawTuner(screen)
	} else {
		a.drawTab(screen)
	}
	a.drawHUD(screen)
}

func (a *App) drawTab(screen *ebiten.Image) {
	pos := a.eng.PosTick()
	ppt := a.pxPerTick()
	phX := float32(screenW * playheadX)
	tr := a.displayed()
	nStr := len(tr.Tuning)

	tickToX := func(t int64) float32 {
		return phX + float32(float64(t-pos)*ppt)
	}
	// Visible tick range with a little slack.
	minTick := pos - int64(float64(phX)/ppt) - score.PPQ
	maxTick := pos + int64(float64(screenW-float64(phX))/ppt) + score.PPQ

	// String lines.
	for si := 0; si < nStr; si++ {
		y := float32(tabTop + si*stringGap)
		vector.StrokeLine(screen, 0, y, screenW, y, 1, colString, false)
	}

	// Loop region.
	if la, lb, on := a.eng.Loop(); on {
		x0, x1 := tickToX(la), tickToX(lb)
		vector.DrawFilledRect(screen, x0, tabTop-20, x1-x0, float32(nStr-1)*stringGap+40, colLoop, false)
		vector.StrokeLine(screen, x0, tabTop-20, x0, float32(tabTop+(nStr-1)*stringGap+20), 2, colLoopEdge, false)
		vector.StrokeLine(screen, x1, tabTop-20, x1, float32(tabTop+(nStr-1)*stringGap+20), 2, colLoopEdge, false)
	}

	// Bars, beats, notes.
	waiting := a.waitingKeys()
	for bi, bar := range tr.Bars {
		barEnd := bar.Start + bar.Len()
		if barEnd < minTick || bar.Start > maxTick {
			continue
		}
		x := tickToX(bar.Start)
		vector.StrokeLine(screen, x, tabTop-14, x, float32(tabTop+(nStr-1)*stringGap+14), 1, colBarline, false)
		drawText(screen, fmt.Sprintf("%d", bi+1), float64(x)+3, tabTop-32, colBarline)

		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				nx := tickToX(beat.Start)
				ny := float32(tabTop + (n.String-1)*stringGap)
				label := fmt.Sprintf("%d", n.Fret)
				if n.Tied {
					label = "~" + label
				}
				if n.Tech&score.TechDead != 0 {
					label = "x"
				}
				// Sounding/inferred are the base; a verdict tints
				// over them once the playhead has reached the
				// note (latest wins — loops re-judge each pass);
				// an active wait point pulses over all.
				col := colNote
				if n.Inferred {
					col = colInferred
				}
				if beat.Start <= pos && pos < beat.Start+beat.Dur {
					col = colSounding
				}
				if v, ok := a.verdictAt(beat.Start, n.String, pos); ok {
					col = verdictColor(v)
				}
				if waiting[noteKey{beat.Start, n.String}] {
					col = a.pulseCol()
				}
				// Blank out the string line behind the number.
				w := float32(8 * len(label))
				vector.DrawFilledRect(screen, nx-2, ny-7, w+3, 14, colNoteBG, false)
				drawText(screen, label, float64(nx), float64(ny)-7, col)
			}
		}
	}
	// Final barline.
	if n := len(tr.Bars); n > 0 {
		endTick := tr.Bars[n-1].Start + tr.Bars[n-1].Len()
		x := tickToX(endTick)
		vector.StrokeLine(screen, x, tabTop-14, x, float32(tabTop+(nStr-1)*stringGap+14), 2, colBarline, false)
	}

	// Playhead.
	vector.StrokeLine(screen, phX, tabTop-24, phX, float32(tabTop+(nStr-1)*stringGap+24), 2, colPlayhead, false)

	if a.eng.Waiting() {
		drawText(screen, "WAITING", screenW*playheadX-24, tabTop-48, a.pulseCol())
	}
}

func (a *App) drawHUD(screen *ebiten.Image) {
	state := "paused"
	if a.eng.Playing() {
		state = "playing"
	}
	if in, left := a.eng.CountingIn(); in {
		drawText(screen, fmt.Sprintf("COUNT-IN %d", left), screenW*playheadX-30, tabTop-64, colCountIn)
		state = "count-in"
	}
	line1 := fmt.Sprintf("%s | %.0f BPM (x%.2f) | pass %d", state, a.eng.EffectiveBPM(), a.eng.TempoScale(), a.eng.PassCount())
	if _, _, on := a.eng.Loop(); on {
		line1 += " | LOOP"
	}
	if a.metronome {
		line1 += " | click"
	}
	if a.ramp {
		line1 += " | ramp"
	}
	if a.wait {
		line1 += " | wait"
	}
	if a.live {
		line1 += " | live"
	}
	drawText(screen, line1, 16, 16, colHUD)

	if a.live {
		a.drawLiveHUD(screen)
		a.drawLegend(screen)
	}

	for i, t := range a.sc.Tracks {
		name := t.Name
		if name == "" {
			name = fmt.Sprintf("track %d", i+1)
		}
		mark := " "
		if a.eng.TrackMuted(i) {
			mark = "M"
		}
		cur := " "
		if i == a.track {
			cur = ">"
		}
		drawText(screen, fmt.Sprintf("%s%d %s [%s]", cur, i+1, name, mark), 16, float64(40+16*i), colHUD)
	}

	help := "space play/pause  arrows seek  up/dn tempo  A/B loop  L clear  M click  R ramp  T tuner"
	if a.waitCtl {
		help += "  W wait"
	}
	help += "  +/- zoom  1-9 mute  Q quit"
	drawText(screen, help, 16, screenH-24, colBarline)
}

func drawText(dst *ebiten.Image, s string, x, y float64, col color.RGBA) {
	op := &text.DrawOptions{}
	op.GeoM.Translate(x, y)
	op.ColorScale.ScaleWithColor(col)
	text.Draw(dst, s, face, op)
}

// Layout implements ebiten.Game with a fixed logical size.
func (a *App) Layout(int, int) (int, int) { return screenW, screenH }
