// Command uishot renders one guitarTutor screen to a PNG.
//
// It exists so the layout can be looked at without a screen capture:
// Ebitengine needs a real graphics context, so the window does open, but
// what is saved is the game's own framebuffer rather than a region of the
// desktop. Nothing that happens to be in front of the window can end up
// in the image.
//
//	go run ./tools/uishot -screen practice -o practice.png
//
// screens: start, settings, practice, practice-live
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/practice"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/S95F/guitarTutor/internal/synth"
	"github.com/S95F/guitarTutor/internal/ui"
)

const (
	shotW = 1280
	shotH = 720
	// warmup frames let the screen settle before the image is taken:
	// pulses and hover states are driven by a frame counter.
	warmup = 12
)

func main() {
	which := flag.String("screen", "practice", "start, settings, practice or practice-live")
	out := flag.String("o", "shot.png", "output PNG")
	piece := flag.String("piece", "testdata/fixture_riff.gtab", "piece for the practice screens")
	flag.Parse()

	sc, err := build(*which, *piece)
	if err != nil {
		fmt.Fprintln(os.Stderr, "uishot:", err)
		os.Exit(1)
	}
	ebiten.SetWindowSize(shotW, shotH)
	ebiten.SetWindowTitle("uishot")
	g := &shot{inner: sc, out: *out}
	if err := ebiten.RunGame(g); err != nil && err != ebiten.Termination {
		fmt.Fprintln(os.Stderr, "uishot:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}

// build makes the screen to render.
func build(which, piece string) (ui.Screen, error) {
	switch which {
	case "start", "settings":
		sh, browser := ui.NewBrowserShell(ui.Services{Prefs: &memPrefs{}, Audio: fakeAudio{}})
		browser.SetSettingsOpener(func() {})
		browser.SetOpenDialog(func(string) {})
		if which == "settings" {
			st := ui.NewSettings(sh)
			st.SetFilePicker(func([]string, func(string)) {})
			return st, nil
		}
		return browser, nil
	}

	s, err := textfmt.ParseFile(piece)
	if err != nil {
		return nil, err
	}
	eng := engine.New(s, engine.Options{SampleRate: 48000, Voices: synth.NewPluck})
	app := ui.New(eng, s, 0)
	app.SetSettingsOpener(func() {})
	app.SetReloader(func() {})
	app.SetInitialMetronome(true)
	app.SetCountIn(4)
	// A loop over bars 2-4, and the playhead a bar in, so the timeline
	// and the loop handles have something to show.
	eng.SetLoop(s.Tracks[0].Bars[1].Start, s.Tracks[0].Bars[3].Start)
	eng.SeekTick(s.Tracks[0].Bars[1].Start + 480)

	switch which {
	case "practice":
		return app, nil
	case "practice-live":
		app.SetLiveStatus(func() (float64, int64) { return -14, 0 })
		app.SetWaitControl(true)
		app.OfferResults(sampleResults(s))
		app.OfferTuner(pitch.Note{Key: 45, Cents: -17}, true)
		app.SetLiveWarning("capture and playback are different devices: their clocks drift apart over a session")
		return app, nil
	}
	return nil, fmt.Errorf("unknown screen %q", which)
}

// sampleResults invents a pass over the first bar so the tab shows the
// three verdict colours and the accuracy block has numbers in it.
func sampleResults(s *score.Score) []practice.NoteResult {
	var out []practice.NoteResult
	verdicts := []practice.Verdict{practice.VerdictHit, practice.VerdictHit, practice.VerdictClose, practice.VerdictMiss}
	i := 0
	for _, bar := range s.Tracks[0].Bars[:2] {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				out = append(out, practice.NoteResult{
					Event:   score.NoteEvent{Start: beat.Start, String: n.String},
					Verdict: verdicts[i%len(verdicts)],
				})
				i++
			}
		}
	}
	return out
}

// shot drives a screen for a few frames and saves its framebuffer.
type shot struct {
	inner ui.Screen
	buf   *ebiten.Image
	n     int
	out   string
	done  bool
	err   error
}

func (s *shot) Update() error {
	if s.done {
		if s.err != nil {
			return s.err
		}
		return ebiten.Termination
	}
	// The screen's own Update may report that it is finished; for a
	// screenshot that is not interesting, so it is ignored.
	_ = s.inner.Update()
	return nil
}

func (s *shot) Draw(screen *ebiten.Image) {
	if s.buf == nil {
		s.buf = ebiten.NewImage(shotW, shotH)
	}
	s.inner.Draw(s.buf)
	screen.DrawImage(s.buf, nil)
	if s.n++; s.n == warmup {
		s.err = save(s.buf, s.out)
		s.done = true
	}
}

func (s *shot) Layout(int, int) (int, int) { return shotW, shotH }

// save writes the offscreen buffer as a PNG. ReadPixels is read from the
// buffer, never from the screen image, which Ebitengine does not allow.
func save(img *ebiten.Image, path string) error {
	pix := make([]byte, 4*shotW*shotH)
	img.ReadPixels(pix)
	rgba := &image.RGBA{Pix: pix, Stride: 4 * shotW, Rect: image.Rect(0, 0, shotW, shotH)}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, rgba)
}

// --- stand-ins for the services a real run would supply ------------------

type memPrefs struct {
	sf      string
	countIn int
	cap     string
	play    string
}

func (p *memPrefs) Recents() []string         { return nil }
func (p *memPrefs) AddRecent(string)          {}
func (p *memPrefs) SoundFont() string         { return p.sf }
func (p *memPrefs) SetSoundFont(v string)     { p.sf = v }
func (p *memPrefs) CountIn() int              { return p.countIn }
func (p *memPrefs) SetCountIn(v int)          { p.countIn = v }
func (p *memPrefs) Devices() (string, string) { return p.cap, p.play }
func (p *memPrefs) SetDevices(c, pl string)   { p.cap, p.play = c, pl }
func (p *memPrefs) Save() error               { return nil }
func (p *memPrefs) Path() string              { return `C:\Users\you\AppData\Roaming\guitartutor\config.json` }

type fakeAudio struct{}

func (fakeAudio) BackendName() string { return "WASAPI (shared)" }
func (fakeAudio) SampleRate() int     { return 48000 }
func (fakeAudio) Devices() ([]ui.DeviceOption, []ui.DeviceOption, error) {
	return []ui.DeviceOption{
			{ID: "cap-focus", Name: "Focusrite USB (Focusrite USB Audio)"},
			{ID: "cap-rt", Name: "Microphone (Realtek(R) Audio)", Default: true},
		}, []ui.DeviceOption{
			{ID: "play-focus", Name: "Speakers (Focusrite USB Audio)"},
			{ID: "play-rt", Name: "Speakers (Realtek(R) Audio)", Default: true},
		}, nil
}
func (fakeAudio) CalibratedOffset(string, string) (int, bool) { return 0, false }
func (fakeAudio) Calibrate(string, string, func(float64)) (int, float64, error) {
	return 0, 0, fmt.Errorf("not in a screenshot")
}
