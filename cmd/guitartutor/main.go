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
  -scale <f>      initial tempo scale, e.g. 0.7 (default 1.0)
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

	sc, eng, err := setup(fs.Arg(0), *sf2, *scale, *met, *countIn)
	if err != nil {
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

	sc, eng, err := setup(fs.Arg(0), *sf2, *scale, *met, *countIn)
	if err != nil {
		return err
	}
	eng.Play()

	ciSec := float64(*countIn) * sc.Tempos.TimeAt(sc.Meters.At(0).BeatLen())
	endSec := (sc.Tempos.TimeAt(sc.End())+ciSec)/(*scale) + *tail
	total := int(endSec*sampleRate + 0.5)
	left := make([]float32, total)
	right := make([]float32, total)
	const chunk = 4800
	for off := 0; off < total; off += chunk {
		n := min(chunk, total-off)
		eng.RenderFrames(left[off:off+n], right[off:off+n])
	}

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := wavio.Write(f, sampleRate, left, right); err != nil {
		return err
	}
	fmt.Printf("rendered %.1fs to %s\n", endSec, *out)
	return nil
}
