package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ebitengine/oto/v3"

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/audiofile"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/gpimport"
	"github.com/S95F/musicTutor/internal/midiimport"
	"github.com/S95F/musicTutor/internal/mxlimport"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/synth"
	"github.com/S95F/musicTutor/internal/ui"
	"github.com/S95F/musicTutor/internal/wavio"
)

const sampleRate = 48000

const (
	minScale = 0.25
	maxScale = 2.0
)

const maxRenderFrames = 30 * 60 * sampleRate

const playerReadAhead = 100 * time.Millisecond

const playerBufferBytes = int(int64(playerReadAhead)*sampleRate/int64(time.Second)) * bytesPerFrame

var version = "0.1.0-dev"

func main() {

	if len(os.Args) < 2 {
		if err := runShell(""); err != nil {
			fmt.Fprintln(os.Stderr, "musictutor:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Println("musictutor", version)
	case "help", "-h", "--help":
		usage(os.Stdout)
	case "play":
		err = runPlay(os.Args[2:])
	case "render":
		err = runRender(os.Args[2:])
	case "devices":
		err = runDevices(os.Args[2:])
	case "calibrate":
		err = runCalibrate(os.Args[2:])
	default:
		if strings.HasPrefix(os.Args[1], "-") {

			fmt.Fprintln(os.Stderr, flagFirstDiagnostic(os.Args[1]))
			usage(os.Stderr)
			os.Exit(2)
		}

		if err = checkPieceArgument(os.Args[1]); err == nil {

			var note string
			if note, err = checkExtraArguments(os.Args[1], os.Args[2:]); err != nil {
				break
			}
			if note != "" {
				fmt.Fprintln(os.Stderr, "musictutor:", note)
			}
			err = runShell(os.Args[1])
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "musictutor:", err)
		os.Exit(1)
	}
}

func flagFirstDiagnostic(arg string) string {
	return fmt.Sprintf("musictutor: unknown command %q — flags follow a subcommand: musictutor play %s <file>", arg, arg)
}

func checkExtraArguments(piece string, rest []string) (note string, err error) {
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			return "", fmt.Errorf("flags go with the play subcommand: musictutor play [flags] %q", piece)
		}
	}
	if len(rest) == 0 {
		return "", nil
	}
	noun := "file"
	if len(rest) > 1 {
		noun = "files"
	}
	return fmt.Sprintf("opening %q; skipping %d more %s (%s) — the window opens one piece at a time; open the rest from its start screen",
		piece, len(rest), noun, strings.Join(rest, ", ")), nil
}

func checkPieceArgument(arg string) error {
	ext := strings.ToLower(filepath.Ext(arg))
	for _, e := range ui.PieceExtensions() {
		if ext == e {
			return nil
		}
	}
	if _, err := os.Stat(arg); err != nil {
		return fmt.Errorf("%q is not a command or a piece file (run 'musictutor help'; pieces are .gtab, .mid, .gp, .musicxml, .mxl)", arg)
	}
	return fmt.Errorf("%s: unsupported file type %q (want .gtab, .mid, .gp, .musicxml, or .mxl)", arg, filepath.Ext(arg))
}

func usage(w io.Writer) {
	fmt.Fprint(w, `musictutor — practice companion for guitarists and wind players

usage:
  musictutor play [flags] <file>
  musictutor render [flags] <file>
  musictutor devices
  musictutor calibrate [flags]
  musictutor version

`+piecesLine+`

play flags:
  -sf2 <path>     SoundFont for synthesis (default: built-in pluck and reed)
  -scale <f>      initial tempo scale, 0.25 to 2.0 (default 1.0)
  -met            start with the metronome on
  -countin <n>    count-in beats before playback starts
  -track <n>      the track you are practising, 1-based: the one shown, and
                  under -listen the one scored and waited on
                  (default: the first user track)
  -listen         hear your instrument: live pitch detection and scoring
  -in <id>        capture device for -listen (see devices; default system)
  -out <id>       playback device for -listen (default system)
  -backing <path>         backing-track audio (wav/flac/mp3), pinned to score time
  -backing-offset <sec>   file position at the piece's start (may be negative)
  -backing-gain <f>       backing volume (default 1.0)

render flags:
  -o <path>       output WAV (default out.wav)
  -sf2, -scale, -met, -countin, -backing*   as above
  -tail <sec>     silence after the last note (default 2.0)

calibrate flags:
  -in / -out      devices, as above

devices lists audio endpoints; calibrate measures the round-trip latency
offset used to align scoring (make the output audible to the input first:
point a mic at the speakers, or wire a loopback).
`)
}

const piecesLine = "pieces: .gtab (text tab), .mid (MIDI), .gp (Guitar Pro 7/8), .musicxml / .mxl (MusicXML)"

const (
	inFlagHelp  = "capture device for live input: a unique part of a name from 'musictutor devices' (default: the system default)"
	outFlagHelp = "playback device: a unique part of a name from 'musictutor devices' (default: the system default)"
)

func setUsage(fs *flag.FlagSet, synopsis string, extra ...string) {
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintln(w, "usage:", synopsis)
		for _, line := range extra {
			fmt.Fprintln(w, line)
		}
		fs.PrintDefaults()
	}
}

func load(path string) (*score.Score, []string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gtab":
		sc, err := textfmt.ParseFile(path)
		return sc, nil, err
	case ".mid", ".midi", ".smf":
		return midiimport.ImportFile(path)
	case ".gp":
		return gpimport.ImportFile(path)
	case ".musicxml", ".mxl", ".xml":
		return mxlimport.ImportFile(path)
	default:
		return nil, nil, fmt.Errorf("unsupported file type %q (want .gtab, .mid, .gp, .musicxml, or .mxl)", filepath.Ext(path))
	}
}

func loadBacking(eng *engine.Engine, path string, offsetSec, gain float64) error {
	left, right, warns, err := audiofile.Load(path)
	if err != nil {
		return fmt.Errorf("backing track: %w", err)
	}
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	eng.SetBackingTrack(left, right, offsetSec)
	eng.SetBackingGain(gain)
	return nil
}

func makeFactory(sf2 string) (synth.Factory, error) {
	if sf2 == "" {
		return synth.NewBuiltin, nil
	}
	sf, err := synth.LoadSoundFont(sf2)
	if err != nil {
		return nil, fmt.Errorf("loading SoundFont: %w", err)
	}
	return synth.NewSoundFontFactory(sf), nil
}

func validateScale(s float64) error {
	if !(s >= minScale && s <= maxScale) {
		return fmt.Errorf("-scale %v: accepted range is %g to %g", s, minScale, maxScale)
	}
	return nil
}

func ensureTracks(sc *score.Score, action string) error {
	if len(sc.Tracks) == 0 {
		return fmt.Errorf("the piece has no tracks; nothing to %s", action)
	}
	return nil
}

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
	track := fs.Int("track", 0, "track to practise: displayed, scored and waited on (1-based)")
	listen := fs.Bool("listen", false, "live pitch detection and scoring")
	inQ := fs.String("in", "", inFlagHelp)
	outQ := fs.String("out", "", outFlagHelp)
	backing := fs.String("backing", "", "backing-track audio file (wav/flac/mp3)")
	backingOff := fs.Float64("backing-offset", 0, "backing start offset in seconds (positive skips into the file)")
	backingGain := fs.Float64("backing-gain", 1.0, "backing track volume")
	setUsage(fs, "musictutor play [flags] <file>", piecesLine)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: musictutor play [flags] <file>")
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
	if *backing != "" {
		if err := loadBacking(eng, *backing, *backingOff, *backingGain); err != nil {
			return err
		}
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

	app := ui.New(eng, sc, display)
	app.SetInitialMetronome(*met)

	app.SetCountIn(*countIn)

	if *listen {

		cfg, cfgErr := appconfig.Load()
		if cfgErr != nil {
			fmt.Fprintln(os.Stderr, "warning: existing config unreadable, ignoring it:", cfgErr)
		}
		session, cond, err := setupListen(eng, app, sc, *inQ, *outQ, cfg)
		if err != nil {
			return err
		}
		defer session.Stop()

		conds := cond.notes
		if cond.uncalibrated {
			conds = append(conds, "timing is not calibrated for these devices — quit and run 'musictutor calibrate'")
		}
		if msg := composeBanner(conds); msg != "" {
			app.SetLiveWarning(msg)
		}
		return app.Run()
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
	player.SetBufferSize(playerBufferBytes)
	player.Play()
	defer player.Close()

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
	backing := fs.String("backing", "", "backing-track audio file (wav/flac/mp3)")
	backingOff := fs.Float64("backing-offset", 0, "backing start offset in seconds")
	backingGain := fs.Float64("backing-gain", 1.0, "backing track volume")
	setUsage(fs, "musictutor render [flags] <file>", piecesLine)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: musictutor render [flags] <file>")
	}
	if err := validateScale(*scale); err != nil {
		return err
	}

	sc, eng, err := setup(fs.Arg(0), *sf2, *scale, *met, *countIn)
	if err != nil {
		return err
	}
	if *backing != "" {
		if err := loadBacking(eng, *backing, *backingOff, *backingGain); err != nil {
			return err
		}
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
	if err := wavio.Write(f, sampleRate, left, right); err != nil {
		f.Close()
		return err
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", *out, err)
	}
	fmt.Printf("rendered %.1fs to %s\n", float64(len(left))/sampleRate, *out)
	return nil
}

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
