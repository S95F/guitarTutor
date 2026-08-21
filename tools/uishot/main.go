// Command uishot renders one musicTutor screen to a PNG.
//
// It exists so the layout can be looked at without a screen capture:
// Ebitengine needs a real graphics context, so the window does open, but
// what is saved is the game's own framebuffer rather than a region of the
// desktop. Nothing that happens to be in front of the window can end up
// in the image.
//
//	go run ./tools/uishot -screen practice -o practice.png
//
// screens: glyphs, start, start-bare, settings, editor, editor-new,
// editor-pick, editor-text, editor-help, practice, practice-live, help,
// tuner
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/S95F/musicTutor/internal/edit"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/practice"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/synth"
	"github.com/S95F/musicTutor/internal/ui"
)

const (
	shotW = 1280
	shotH = 720
	// warmup frames let the screen settle before the image is taken:
	// pulses and hover states are driven by a frame counter.
	warmup = 12
	// And real time to let the tooltip dwell elapse (see
	// internal/ui/tooltip.go). The dwell is measured in SECONDS, not in
	// frames, so a window rendering faster than the display can run out
	// its warmup frames long before a third of a second has passed.
	settle = 700 * time.Millisecond
)

func main() {
	which := flag.String("screen", "practice", "glyphs, start, start-bare, settings, editor, editor-new, editor-pick, editor-text, editor-help, practice, practice-live, help or tuner")
	out := flag.String("o", "shot.png", "output PNG")
	piece := flag.String("piece", "testdata/fixture_riff.gtab", "piece for the practice screens")
	hover := flag.String("hover", "", "pin the cursor at x,y so hover states and tooltips are drawn")
	flag.Parse()

	if *hover != "" {
		var x, y float64
		if _, err := fmt.Sscanf(*hover, "%f,%f", &x, &y); err != nil {
			fmt.Fprintln(os.Stderr, "uishot: -hover wants x,y")
			os.Exit(2)
		}
		ui.ForceCursor(x, y, false)
	}

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
	case "start", "start-bare", "settings":
		prefs := &memPrefs{}
		lib := shotLibrary{}
		if which == "start" {
			// Real files in a temp folder, so the shot shows what the
			// screen looks like in use rather than a column of "missing".
			dir, err := shotPieceFiles()
			if err != nil {
				return nil, err
			}
			prefs.recents = []string{
				filepath.Join(dir, "Black Dog.gp"),
				filepath.Join(dir, "little-wing.musicxml"),
				filepath.Join(dir, "Sultans of Swing.mid"),
			}
			prefs.created = []string{
				filepath.Join(dir, "Warmup in A.gtab"),
				filepath.Join(dir, "Blues turnaround.gtab"),
			}
			lib.dir = dir
			lib.pieces = shotPieces(dir)
		}
		sh, browser := ui.NewBrowserShell(ui.Services{Prefs: prefs, Audio: fakeAudio{}, Library: lib})
		browser.SetSettingsOpener(func() {})
		browser.SetOpenDialog(func(string) {})
		browser.SetNewPiece(func() {})
		browser.SetEditPiece(func(string) {})
		if which == "settings" {
			st := ui.NewSettings(sh)
			st.SetFilePicker(func([]string, func(string)) {})
			return st, nil
		}
		return browser, nil

	case "glyphs":
		return ui.NewGlyphSheet(), nil

	case "editor-new":
		ed := ui.NewEditor(nil)
		ed.SetSaveDialog(func(string) {})
		ed.SetPractice(func(string) {})
		return ed, nil

	case "editor-pick":
		// A new piece as it actually opens: the instrument question first.
		ed := ui.NewEditorChoosing(nil)
		ed.SetSaveDialog(func(string) {})
		ed.SetPractice(func(string) {})
		return ed, nil

	case "editor", "editor-text", "editor-help":
		s, err := textfmt.ParseFile(piece)
		if err != nil {
			return nil, err
		}
		ed, err := ui.NewEditorFor(nil, s, piece)
		if err != nil {
			return nil, err
		}
		ed.SetSaveDialog(func(string) {})
		ed.SetPractice(func(string) {})
		// A cursor a couple of beats in, so the caret and the lit cell are
		// somewhere the eye can find them.
		ed.Doc().GoTo(edit.Cursor{Bar: 0, Beat: 2, Str: 5})
		switch which {
		case "editor-text":
			ed.ShowTextView(true)
		case "editor-help":
			ed.SetHelpOpen(true)
		}
		return ed, nil
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
	case "help":
		app.SetWaitControl(true)
		app.SetHelpOpen(true)
		return app, nil
	case "tuner":
		app.SetLiveStatus(func() (float64, int64) { return -18, 0 })
		app.SetWaitControl(true)
		app.OfferTuner(pitch.Note{Key: 45, Cents: -17}, true)
		app.SetTunerView(true)
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
	start time.Time
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
	if s.start.IsZero() {
		s.start = time.Now()
	}
	if s.n++; s.n >= warmup && time.Since(s.start) >= settle {
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
	sf       string
	countIn  int
	cap      string
	play     string
	trim     int
	recents  []string
	created  []string
	hideHint bool
}

func (p *memPrefs) Recents() []string         { return p.recents }
func (p *memPrefs) AddRecent(path string)     { p.recents = append([]string{path}, p.recents...) }
func (p *memPrefs) Created() []string         { return p.created }
func (p *memPrefs) AddCreated(path string)    { p.created = append([]string{path}, p.created...) }
func (p *memPrefs) HintHidden() bool          { return p.hideHint }
func (p *memPrefs) SetHintHidden(h bool)      { p.hideHint = h }
func (p *memPrefs) SoundFont() string         { return p.sf }
func (p *memPrefs) SetSoundFont(v string)     { p.sf = v }
func (p *memPrefs) CountIn() int              { return p.countIn }
func (p *memPrefs) SetCountIn(v int)          { p.countIn = v }
func (p *memPrefs) Devices() (string, string) { return p.cap, p.play }
func (p *memPrefs) SetDevices(c, pl string)   { p.cap, p.play = c, pl }
func (p *memPrefs) SyncTrim() int             { return p.trim }
func (p *memPrefs) SetSyncTrim(ms int)        { p.trim = ms }
func (p *memPrefs) Save() error               { return nil }
func (p *memPrefs) Path() string              { return `C:\Users\you\AppData\Roaming\musictutor\config.json` }

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

// shotLibrary stands in for the pieces folder in a screenshot.
type shotLibrary struct {
	dir    string
	pieces []ui.PieceInfo
}

func (l shotLibrary) Dir() string                   { return l.dir }
func (l shotLibrary) Scan() ([]ui.PieceInfo, error) { return l.pieces, nil }

// shotPieceFiles writes the files the start-screen shot lists, so the
// entries are real rather than flagged missing.
func shotPieceFiles() (string, error) {
	dir := filepath.Join(os.TempDir(), "musictutor-shot", "pieces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, name := range []string{
		"Black Dog.gp", "little-wing.musicxml", "Sultans of Swing.mid",
		"Warmup in A.gtab", "Blues turnaround.gtab",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("placeholder"), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// shotPieces is a plausible library: a few written pieces, described the
// way the real scanner describes them, and one that will not parse. The
// subtitles are what cmd/musictutor's describePiece would say and the
// problem line is what its textfmt.ProblemLine would render for the parser's
// own message — a screenshot of words the app never produces is a
// screenshot of a different app.
func shotPieces(dir string) []ui.PieceInfo {
	return []ui.PieceInfo{
		{Path: filepath.Join(dir, "warmup.gtab"), Name: "warmup", Title: "Warmup in A",
			Summary: "4/4 · 92 BPM · 8 bars"},
		{Path: filepath.Join(dir, "turnaround.gtab"), Name: "turnaround", Title: "Blues turnaround",
			Summary: "12/8 · 76 BPM · 4 bars · 2 tracks"},
		{Path: filepath.Join(dir, "dropd.gtab"), Name: "dropd", Title: "Drop D riff",
			Summary: "4/4 · 148 BPM · 16 bars · drop D"},
		{Path: filepath.Join(dir, "etude.gtab"), Name: "etude", Title: "Etude no. 1",
			Summary: "3/4 · 60 BPM · 24 bars · capo 2"},
		{Path: filepath.Join(dir, "broken.gtab"), Name: "broken",
			Problem: "line 7, column 14 — bar underfull: the notes add up to 3 of the 4 beats a 4/4 bar holds"},
	}
}
