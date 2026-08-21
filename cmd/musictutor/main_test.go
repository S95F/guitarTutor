package main

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
	"github.com/S95F/musicTutor/internal/ui"
)

func oneBarScore(t *testing.T) *score.Score {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "guitar", Tuning: score.StandardTuning}
	sc.Tracks = append(sc.Tracks, tr)
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3})
	b.AddBeat(score.Half, score.Note{String: 5, Fret: 5})
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

func newEngine(sc *score.Score, opts engine.Options) *engine.Engine {
	opts.SampleRate = sampleRate
	opts.Voices = synth.NewPluck
	return engine.New(sc, opts)
}

func TestValidateScale(t *testing.T) {
	for _, s := range []float64{0.25, 0.7, 1.0, 2.0} {
		if err := validateScale(s); err != nil {
			t.Errorf("validateScale(%v) = %v, want nil", s, err)
		}
	}
	for _, s := range []float64{0, -1, 0.1, 0.24, 2.01, 3, math.NaN(), math.Inf(1)} {
		err := validateScale(s)
		if err == nil {
			t.Errorf("validateScale(%v) = nil, want error", s)
			continue
		}
		if !strings.Contains(err.Error(), "0.25") || !strings.Contains(err.Error(), "2") {
			t.Errorf("validateScale(%v) error %q does not name the accepted range", s, err)
		}
	}
}

func TestEnsureTracks(t *testing.T) {
	empty := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	if err := empty.Validate(); err != nil {
		t.Fatalf("track-less score should validate: %v", err)
	}
	if err := ensureTracks(empty, "play"); err == nil {
		t.Fatal("ensureTracks(track-less score) = nil, want error")
	}
	if err := ensureTracks(oneBarScore(t), "render"); err != nil {
		t.Fatalf("ensureTracks(one-track score) = %v, want nil", err)
	}
}

func TestRenderAllClampedScale(t *testing.T) {
	sc := oneBarScore(t)
	eng := newEngine(sc, engine.Options{})
	eng.SetTempoScale(0.25)

	const tailSec = 2.0
	left, right, err := renderAll(eng, tailSec, maxRenderFrames)
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}
	if len(left) != len(right) {
		t.Fatalf("channel length mismatch: %d vs %d", len(left), len(right))
	}

	const chunk = 4800
	wantPlay := 8 * sampleRate
	wantTail := int(tailSec * sampleRate)
	if len(left) < wantPlay+wantTail {
		t.Fatalf("rendered %d frames, want at least %d (piece truncated)", len(left), wantPlay+wantTail)
	}
	if len(left) >= wantPlay+wantTail+chunk {
		t.Fatalf("rendered %d frames, want under %d (runaway)", len(left), wantPlay+wantTail+chunk)
	}
	if eng.Playing() {
		t.Fatal("engine still playing after renderAll")
	}

	var sum float64
	for _, v := range left[wantPlay/2 : wantPlay] {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		t.Fatal("second half of the piece is pure silence; piece truncated")
	}
}

func TestRenderAllCountInAndTempoChange(t *testing.T) {
	sc := oneBarScore(t)

	sc.Tempos = score.TempoMap{
		{Tick: 0, USPerQuarter: 500000},
		{Tick: 480, USPerQuarter: 250000},
	}
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	eng := newEngine(sc, engine.Options{CountInBeats: 2})

	const tailSec = 0.5
	left, _, err := renderAll(eng, tailSec, maxRenderFrames)
	if err != nil {
		t.Fatalf("renderAll: %v", err)
	}

	const chunk = 4800
	wantPlay := 2*24000 + 12000 + 42000
	wantTail := int(tailSec * sampleRate)
	if len(left) < wantPlay+wantTail {
		t.Fatalf("rendered %d frames, want at least %d (count-in or tail truncated)", len(left), wantPlay+wantTail)
	}
	if len(left) >= wantPlay+wantTail+chunk {
		t.Fatalf("rendered %d frames, want under %d (runaway)", len(left), wantPlay+wantTail+chunk)
	}
}

func TestFlagFirstDiagnostic(t *testing.T) {
	got := flagFirstDiagnostic("-listen")
	for _, want := range []string{`"-listen"`, "musictutor play -listen <file>"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostic %q does not contain %q", got, want)
		}
	}
}

func TestCheckPieceArgument(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(existing, []byte("not a piece"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	cases := []struct {
		name      string
		arg       string
		wantParts []string
	}{
		{name: "mistyped subcommand", arg: "devcies",
			wantParts: []string{`"devcies" is not a command or a piece file`, "musictutor help", ".gtab"}},
		{name: "nonexistent non-piece path", arg: filepath.Join(t.TempDir(), "song.pdf"),
			wantParts: []string{"is not a command or a piece file"}},
		{name: "existing file in a format nothing imports", arg: existing,
			wantParts: []string{existing, "unsupported file type"}},
		{name: "missing piece still goes to the shell", arg: "missing.gtab"},
		{name: "case-insensitive extension", arg: "SONG.GP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkPieceArgument(c.arg)
			if c.wantParts == nil {
				if err != nil {
					t.Fatalf("checkPieceArgument(%q) = %v, want it accepted", c.arg, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkPieceArgument(%q) = nil, want a diagnostic", c.arg)
			}
			for _, want := range c.wantParts {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("diagnostic %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadAgreesWithPieceExtensions(t *testing.T) {
	dir := t.TempDir()
	for _, ext := range ui.PieceExtensions() {
		_, _, err := load(filepath.Join(dir, "missing"+ext))
		if err == nil {
			t.Fatalf("load of a missing %s file returned nil error", ext)
		}
		if strings.Contains(err.Error(), "unsupported file type") {
			t.Errorf("load rejects %s as unsupported, but the dispatch and the file dialog both offer it", ext)
		}
	}
}

func TestSetUsageNamesThePositionalArgument(t *testing.T) {
	fs := flag.NewFlagSet("play", flag.ContinueOnError)
	fs.String("sf2", "", "SoundFont file")
	var buf strings.Builder
	fs.SetOutput(&buf)
	setUsage(fs, "musictutor play [flags] <file>", piecesLine)
	fs.Usage()
	out := buf.String()
	for _, want := range []string{"usage: musictutor play [flags] <file>", "pieces: .gtab", "-sf2"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage output %q does not contain %q", out, want)
		}
	}
	if !strings.HasPrefix(out, "usage:") {
		t.Errorf("usage output starts %q, want the synopsis first", out[:min(len(out), 40)])
	}
}

func TestDeviceFlagHelpNamesTheDevicesCommand(t *testing.T) {
	for _, help := range []string{inFlagHelp, outFlagHelp} {
		if !strings.Contains(help, "musictutor devices") {
			t.Errorf("flag help %q does not say to run 'musictutor devices'", help)
		}
	}
}

func TestRenderAllRunawayGuard(t *testing.T) {
	sc := oneBarScore(t)
	eng := newEngine(sc, engine.Options{})
	eng.SetLoop(0, sc.End())

	if _, _, err := renderAll(eng, 0, sampleRate); err == nil {
		t.Fatal("renderAll with a looping engine returned nil error, want runaway-guard error")
	}
}

func TestCheckExtraArguments(t *testing.T) {
	for _, tt := range []struct {
		name, piece string
		rest        []string
		wantNote    []string
		wantErr     []string
	}{
		{name: "nothing extra", piece: "song.gtab"},
		{

			name: "trailing flag", piece: "song.gtab", rest: []string{"-listen"},
			wantErr: []string{"play [flags]", `"song.gtab"`},
		},
		{

			name: "flag after extra file", piece: "a.gtab", rest: []string{"b.gtab", "-listen"},
			wantErr: []string{"play [flags]"},
		},
		{
			name: "one extra file", piece: "a.gtab", rest: []string{"b.gtab"},
			wantNote: []string{"1 more file (b.gtab)", "start screen"},
		},
		{
			name: "several extra files", piece: "My Song.gtab", rest: []string{"b.gtab", "c.mid"},
			wantNote: []string{`"My Song.gtab"`, "2 more files (b.gtab, c.mid)"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			note, err := checkExtraArguments(tt.piece, tt.rest)
			if (err != nil) != (tt.wantErr != nil) {
				t.Fatalf("err = %v, want error: %v", err, tt.wantErr != nil)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err %q does not mention %q", err, w)
				}
			}
			if (note != "") != (tt.wantNote != nil) {
				t.Fatalf("note = %q, want a note: %v", note, tt.wantNote != nil)
			}
			for _, w := range tt.wantNote {
				if !strings.Contains(note, w) {
					t.Errorf("note %q does not mention %q", note, w)
				}
			}
		})
	}
}
