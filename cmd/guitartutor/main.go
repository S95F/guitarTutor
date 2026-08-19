// Command guitartutor is the guitarTutor practice application.
//
//	guitartutor <file>                    open a piece in the windowed shell
//	guitartutor play [flags] <file>       open the practice view from the terminal
//	guitartutor render [flags] <file>     render the piece to a WAV
//	guitartutor devices                   list audio endpoints
//	guitartutor calibrate [flags]         measure the round-trip latency
//	guitartutor version                   print the version
//
// Pieces are .gtab, .mid, .gp, .musicxml, or .mxl files.
//
// A bare file argument — the invocation a double-clicked piece arrives
// as — opens the windowed shell on that piece, so a file that will not
// load is reported on the start screen instead of on a console that
// closes itself. See README.md and ROADMAP.md for where this is headed.
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

	"github.com/S95F/guitarTutor/internal/appconfig"
	"github.com/S95F/guitarTutor/internal/audiofile"
	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/gpimport"
	"github.com/S95F/guitarTutor/internal/midiimport"
	"github.com/S95F/guitarTutor/internal/mxlimport"
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

// playerReadAhead is how far ahead of the speaker oto is allowed to pull
// audio out of the engine.
//
// It matters far more than a buffer size usually does, because the engine
// publishes its playback position as it RENDERS: whatever oto has read but
// not yet played is music the tab has already scrolled past. oto's own
// default is half a second, which put the playhead half a second ahead of
// what you could hear and moved it in whatever gulps the refill loop
// happened to take. A tenth of a second is still a generous cushion for a
// non-realtime source — the synthesis costs microseconds per block, so the
// buffer only ever has to cover a scheduling hiccup — and it brings the
// display to within a frame or two of the sound.
//
// The live duplex path does not go through oto at all: there the engine
// renders straight into the device buffer, and the lead is one period.
const playerReadAhead = 100 * time.Millisecond

// playerBufferBytes is playerReadAhead as oto's SetBufferSize wants it:
// bytes of interleaved stereo float32 at the project sample rate.
func playerBufferBytes() int {
	const bytesPerFrame = 2 * 4 // stereo, float32
	n := int(int64(playerReadAhead) * sampleRate / int64(time.Second))
	return n * bytesPerFrame
}

var version = "0.1.0-dev"

func main() {
	// No arguments is the double-clicked-binary case: open the windowed
	// app with the start screen, where a piece can be chosen and settings
	// changed without ever touching a terminal.
	if len(os.Args) < 2 {
		if err := runShell(""); err != nil {
			fmt.Fprintln(os.Stderr, "guitartutor:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Println("guitartutor", version)
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
			// A flag in command position is almost always a subcommand
			// forgotten, not a file — say what is actually wrong before
			// the wall of usage text, or the one line that matters is
			// the one line that is missing.
			fmt.Fprintln(os.Stderr, flagFirstDiagnostic(os.Args[1]))
			usage(os.Stderr)
			os.Exit(2)
		}
		// A bare argument is a piece to practise — the invocation Windows
		// Explorer uses for a double-clicked file — so it opens through
		// the windowed shell, where a file that will not load is an error
		// on the start screen rather than a console that flashes and
		// vanishes. Anything that cannot be a piece is diagnosed here
		// first: opening a window to say "devcies is not a file" would be
		// worse than saying it where the typo was made.
		if err = checkPieceArgument(os.Args[1]); err == nil {
			// Whatever follows the piece would be silently lost — the
			// shell opens exactly one file. A flag anywhere in the tail
			// means the play subcommand was meant, and refusing puts the
			// diagnostic where the flag was typed; extra files mean a
			// multi-select drop onto the executable, and there refusing
			// would leave a vanishing console as the only witness, so
			// the first file still opens and stderr names the rest for
			// whoever can see it.
			var note string
			if note, err = checkExtraArguments(os.Args[1], os.Args[2:]); err != nil {
				break
			}
			if note != "" {
				fmt.Fprintln(os.Stderr, "guitartutor:", note)
			}
			err = runShell(os.Args[1])
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "guitartutor:", err)
		os.Exit(1)
	}
}

// flagFirstDiagnostic is the one line printed above the usage text when
// the first argument is a flag: what was misunderstood, and the shape of
// the invocation that would have worked.
func flagFirstDiagnostic(arg string) string {
	return fmt.Sprintf("guitartutor: unknown command %q — flags follow a subcommand: guitartutor play %s <file>", arg, arg)
}

// checkExtraArguments diagnoses everything after the one piece a bare
// invocation can open. Any flag in the tail refuses the whole invocation
// — the generic shape is suggested rather than the user's arguments
// respliced, because value-taking flags make any reordering wrong — while
// extra files come back as a stderr note naming what was skipped, and the
// first piece still opens: from Explorer, a refusal's console vanishes
// before it can be read.
func checkExtraArguments(piece string, rest []string) (note string, err error) {
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			return "", fmt.Errorf("flags go with the play subcommand: guitartutor play [flags] %q", piece)
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

// checkPieceArgument reports why a bare argument cannot be opened as a
// piece, or nil when it carries an extension an importer reads. The
// extension list is the browser's own (ui.PieceExtensions), so the
// command line and the start screen cannot disagree about what counts as
// a piece — and a load-time test pins that list to the importer switch.
//
// The two failure shapes are told apart with a stat, because they are
// different mistakes: a path that does not exist is almost always a
// mistyped subcommand and is answered with where the commands are
// listed, while a file that really exists in a format the importers do
// not read is named by its path — "unsupported file type """ named
// neither the file nor the problem.
func checkPieceArgument(arg string) error {
	ext := strings.ToLower(filepath.Ext(arg))
	for _, e := range ui.PieceExtensions() {
		if ext == e {
			return nil
		}
	}
	if _, err := os.Stat(arg); err != nil {
		return fmt.Errorf("%q is not a command or a piece file (run 'guitartutor help'; pieces are .gtab, .mid, .gp, .musicxml, .mxl)", arg)
	}
	return fmt.Errorf("%s: unsupported file type %q (want .gtab, .mid, .gp, .musicxml, or .mxl)", arg, filepath.Ext(arg))
}

// usage writes the help text to w. `help` and -h ask for it, so they get
// it on stdout where a pager or a grep can reach it; only the
// unknown-flag path, where the help is a diagnostic, writes to stderr.
func usage(w io.Writer) {
	fmt.Fprint(w, `guitartutor — practice companion for guitarists

usage:
  guitartutor play [flags] <file>
  guitartutor render [flags] <file>
  guitartutor devices
  guitartutor calibrate [flags]
  guitartutor version

`+piecesLine+`

play flags:
  -sf2 <path>     SoundFont for synthesis (default: built-in pluck)
  -scale <f>      initial tempo scale, 0.25 to 2.0 (default 1.0)
  -met            start with the metronome on
  -countin <n>    count-in beats before playback starts
  -track <n>      the track you are practising, 1-based: shown as tab, and
                  under -listen the one scored and waited on
                  (default: the first user track)
  -listen         hear your guitar: live pitch detection and scoring
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

// piecesLine names the piece formats, once, for every usage text that
// takes a <file>: the top-level help and each subcommand's -h say the
// same thing because they print the same line.
const piecesLine = "pieces: .gtab (text tab), .mid (MIDI), .gp (Guitar Pro 7/8), .musicxml / .mxl (MusicXML)"

// inFlagHelp and outFlagHelp describe the device flags the same way
// everywhere they appear (play and calibrate): naming where the device
// names come from, because "name fragment" alone left the user to guess
// which names the fragment was a fragment of.
const (
	inFlagHelp  = "capture device for live input: a unique part of a name from 'guitartutor devices' (default: the system default)"
	outFlagHelp = "playback device: a unique part of a name from 'guitartutor devices' (default: the system default)"
)

// setUsage replaces fs's -h output with one that starts from the
// synopsis. Go's default dump lists the flags and nothing else — no hint
// that a <file> is required, no word on what kind of file — so
// 'guitartutor play -h' answered every question except the one a person
// asking for help actually has. extra lines (the pieces line, a
// subcommand's one-sentence description) follow the synopsis, then the
// flag defaults.
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

// load parses a piece by file extension.
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

// loadBacking decodes a backing track and installs it on the engine.
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
	track := fs.Int("track", 0, "track to practise: displayed, scored and waited on (1-based)")
	listen := fs.Bool("listen", false, "live pitch detection and scoring")
	inQ := fs.String("in", "", inFlagHelp)
	outQ := fs.String("out", "", outFlagHelp)
	backing := fs.String("backing", "", "backing-track audio file (wav/flac/mp3)")
	backingOff := fs.Float64("backing-offset", 0, "backing start offset in seconds (positive skips into the file)")
	backingGain := fs.Float64("backing-gain", 1.0, "backing track volume")
	setUsage(fs, "guitartutor play [flags] <file>", piecesLine)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: guitartutor play [flags] <file>")
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
	// The view mirrors the count-in the engine was built with, and only
	// the shell path used to sync it — `play -countin 4` drew the
	// COUNT-IN toggle as off while the engine counted in, and pressing C
	// to "turn it on" turned it off (audit C5).
	app.SetCountIn(*countIn)

	if *listen {
		// Live mode: the duplex stream renders the engine inside its own
		// callback — oto stays out of the picture entirely. The CLI's
		// config source is the file on disk; the shell passes its
		// in-memory config instead (see setupListen).
		cfg, cfgErr := appconfig.Load()
		if cfgErr != nil {
			fmt.Fprintln(os.Stderr, "warning: existing config unreadable, ignoring it:", cfgErr)
		}
		session, cond, err := setupListen(eng, app, *inQ, *outQ, cfg)
		if err != nil {
			return err
		}
		defer session.Stop()
		// The stderr warnings setupListen printed also go over the tab:
		// this path opens the same window the shell does, and the person
		// watching it is not necessarily watching the terminal it was
		// started from. The remedy named is the CLI's — this view has no
		// settings screen to press S for.
		conds := cond.notes
		if cond.uncalibrated {
			conds = append(conds, "timing is not calibrated for these devices — quit and run 'guitartutor calibrate'")
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
	player.SetBufferSize(playerBufferBytes())
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
	setUsage(fs, "guitartutor render [flags] <file>", piecesLine)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: guitartutor render [flags] <file>")
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
	// Close before claiming success. The OS buffers the write, and on a
	// network share or a full disk the failure only surfaces on the final
	// flush — a deferred Close discarded that error and the success line
	// below had already printed, handing the user a truncated WAV and
	// exit status 0 (audit D1).
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", *out, err)
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
