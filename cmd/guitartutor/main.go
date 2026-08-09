// Command guitartutor is the guitarTutor practice application.
//
//	guitartutor play [flags] <file.gtab|file.mid>   open the practice view
//	guitartutor render [flags] <file>               render the piece to a WAV
//	guitartutor version                             print the version
//
// A bare file argument is shorthand for play. See README.md and ROADMAP.md
// for where this is headed.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ebitengine/oto/v3"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/midiimport"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/S95F/guitarTutor/internal/synth"
	"github.com/S95F/guitarTutor/internal/ui"
	"github.com/S95F/guitarTutor/internal/wavio"
)

// sampleRate is the project-wide audio rate (ROADMAP "Guiding principles").
const sampleRate = 48000

// Tempo scale bounds accepted on the command line. The engine clamps out-of-
// range scales silently; the CLI refuses them loudly instead, so a typo does
// not render at an unexpected speed.
const (
	minScale = 0.25
	maxScale = 2.0
)

// maxRenderFrames caps the playing portion of a render (~30 minutes of
// audio) as a runaway guard.
const maxRenderFrames = 30 * 60 * sampleRate

var version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Println("guitartutor", version)
	case "help", "-h", "--help":
		usage()
	case "play":
		err = runPlay(os.Args[2:])
	case "render":
		err = runRender(os.Args[2:])
	default:
		if strings.HasPrefix(os.Args[1], "-") {
			usage()
			os.Exit(2)
		}
		err = runPlay(os.Args[1:]) // bare file argument
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "guitartutor:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `guitartutor — practice companion for guitarists

usage:
  guitartutor play [flags] <file.gtab|file.mid>
  guitartutor render [flags] <file.gtab|file.mid>
  guitartutor version

play flags:
  -sf2 <path>     SoundFont for synthesis (default: built-in pluck)
  -scale <f>      initial tempo scale, 0.25 to 2.0 (default 1.0)
  -met            start with the metronome on
  -countin <n>    count-in beats before playback starts
  -track <n>      tab track to display, 1-based (default: first user track)

render flags:
  -o <path>       output WAV (default out.wav)
  -sf2, -scale, -met, -countin   as above
  -tail <sec>     silence after the last note (default 2.0)
`)
}

// load parses a piece by file extension.
func load(path string) (*score.Score, []string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gtab":
		sc, err := textfmt.ParseFile(path)
		return sc, nil, err
	case ".mid", ".midi", ".smf":
		return midiimport.ImportFile(path)
	default:
		return nil, nil, fmt.Errorf("unsupported file type %q (want .gtab or .mid)", filepath.Ext(path))
	}
}

// makeFactory picks the synthesis path: SoundFont when supplied, otherwise
// the built-in Karplus-Strong pluck (no assets needed).
func makeFactory(sf2 string) (synth.Factory, error) {
	if sf2 == "" {
		return synth.NewPluck, nil
	}
	sf, err := synth.LoadSoundFont(sf2)
	if err != nil {
		return nil, fmt.Errorf("loading SoundFont: %w", err)
	}
	return synth.NewSoundFontFactory(sf), nil
}

// validateScale rejects tempo scales outside [minScale, maxScale] (NaN
// included) with an error naming the accepted range.
func validateScale(s float64) error {
	if !(s >= minScale && s <= maxScale) {
		return fmt.Errorf("-scale %v: accepted range is %g to %g", s, minScale, maxScale)
	}
	return nil
}

// ensureTracks returns a clean error for a track-less score. textfmt rejects
// bar-less pieces at parse time, but MIDI import is a second path that can
// legitimately produce one; downstream code indexes Tracks unconditionally.
func ensureTracks(sc *score.Score, action string) error {
	if len(sc.Tracks) == 0 {
		return fmt.Errorf("the piece has no tracks; nothing to %s", action)
	}
	return nil
}

// setup loads the piece and builds an engine from shared flags.
func setup(file, sf2 string, scale float64, met bool, countIn int) (*score.Score, *engine.Engine, error) {
	sc, warns, err := load(file)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	fac, err := makeFactory(sf2)
	if err != nil {
		return nil, nil, err
	}
	eng := engine.New(sc, engine.Options{
		SampleRate:   sampleRate,
		Voices:       fac,
		Metronome:    met,
		CountInBeats: countIn,
	})
	eng.SetTempoScale(scale)
	return sc, eng, nil
}

func runPlay(args []string) error {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	sf2 := fs.String("sf2", "", "SoundFont file")
	scale := fs.Float64("scale", 1.0, "initial tempo scale")
	met := fs.Bool("met", false, "metronome on")
	countIn := fs.Int("countin", 0, "count-in beats")
	track := fs.Int("track", 0, "tab track to display (1-based)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: guitartutor play [flags] <file.gtab|file.mid>")
	}
	if err := validateScale(*scale); err != nil {
		return err
	}

	sc, eng, err := setup(fs.Arg(0), *sf2, *scale, *met, *countIn)
	if err != nil {
		return err
	}
	if err := ensureTracks(sc, "play"); err != nil {
		return err
	}

	display := 0
	if *track > 0 {
		if *track > len(sc.Tracks) {
			return fmt.Errorf("-track %d: the piece has %d tracks", *track, len(sc.Tracks))
		}
		display = *track - 1
	} else {
		for i, t := range sc.Tracks {
			if t.Role == score.RoleUser {
				display = i
				break
			}
		}
	}

	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   sampleRate,
		ChannelCount: 2,
		Format:       oto.FormatFloat32LE,
	})
	if err != nil {
		return fmt.Errorf("opening audio output: %w", err)
	}
	<-ready
	player := ctx.NewPlayer(eng)
	player.Play()
	defer player.Close()

	app := ui.New(eng, sc, display)
	app.SetInitialMetronome(*met)
	return app.Run()
}

func runRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	out := fs.String("o", "out.wav", "output WAV path")
	sf2 := fs.String("sf2", "", "SoundFont file")
	scale := fs.Float64("scale", 1.0, "tempo scale")
	met := fs.Bool("met", false, "metronome on")
	countIn := fs.Int("countin", 0, "count-in beats")
	tail := fs.Float64("tail", 2.0, "tail seconds")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: guitartutor render [flags] <file.gtab|file.mid>")
	}
	if err := validateScale(*scale); err != nil {
		return err
	}

	sc, eng, err := setup(fs.Arg(0), *sf2, *scale, *met, *countIn)
	if err != nil {
		return err
	}
	if err := ensureTracks(sc, "render"); err != nil {
		return err
	}

	left, right, err := renderAll(eng, *tail, maxRenderFrames)
	if err != nil {
		return err
	}

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := wavio.Write(f, sampleRate, left, right); err != nil {
		return err
	}
	fmt.Printf("rendered %.1fs to %s\n", float64(len(left))/sampleRate, *out)
	return nil
}

// renderAll starts eng and renders until the transport stops on its own,
// then renders tailSec seconds more so release tails ring out. Letting the
// engine decide the end makes the length exact for any count-in, tempo map,
// and tempo scale — no precomputed-duration formula to drift out of sync
// with engine semantics. maxFrames caps the playing portion (a looping or
// otherwise never-ending engine would grow the buffers forever).
func renderAll(eng *engine.Engine, tailSec float64, maxFrames int) (left, right []float32, err error) {
	const chunk = 4800
	bufL := make([]float32, chunk)
	bufR := make([]float32, chunk)
	eng.Play()
	for eng.Playing() {
		if len(left) >= maxFrames {
			return nil, nil, fmt.Errorf("render exceeded %.0f minutes of audio; is a loop enabled?",
				float64(maxFrames)/sampleRate/60)
		}
		eng.RenderFrames(bufL, bufR)
		left = append(left, bufL...)
		right = append(right, bufR...)
	}
	tailFrames := int(tailSec*sampleRate + 0.5)
	for off := 0; off < tailFrames; off += chunk {
		n := min(chunk, tailFrames-off)
		eng.RenderFrames(bufL[:n], bufR[:n])
		left = append(left, bufL[:n]...)
		right = append(right, bufR[:n]...)
	}
	return left, right, nil
}
