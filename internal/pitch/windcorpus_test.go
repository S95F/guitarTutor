package pitch

// The real-wind corpus test. Every wind detection number in the tree is
// synthesis-validated — the reed voice holding machine-perfect pitches —
// and docs/TESTDATA.md explains why that has already flattered the
// project once on the guitar side. This file is the landing zone: the
// FIRST real sax recording dropped into testdata/real/wind/tones/
// produces coverage with no further code, and corpus.Require skips the
// whole test until then, so a fresh clone (and CI) stays green.
//
// Naming contract (docs/TESTDATA.md): a recording's base name starts
// with its pitch and everything after the first '-' is free description
// — c5.wav, c5-mf-vibrato.wav, 72-altissimo.wav. The pitch token is the
// grammar the .gtab \tuning directive reads (textfmt's parsePitch):
// scientific pitch notation with an optional # or b (middle C = C4 =
// MIDI 60, letter case-insensitive) or a bare MIDI key number. It names
// the CONCERT (sounding) pitch, never the written one — a sax player
// thinks in written pitch, so convert before naming: written D5 on a
// soprano sax = concert C5 = key 72, file c5.wav. This is the guitar
// corpus's own convention (e2.wav is the low E string, key 40), extended
// with the MIDI-number spelling parsePitch already accepts.

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/audiofile"
	"github.com/S95F/musicTutor/internal/corpus"
	"github.com/S95F/musicTutor/internal/score"
)

// keyFromFileName recovers the expected concert-pitch MIDI key from a
// corpus recording's base name, per the contract above.
func keyFromFileName(name string) (int, bool) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if i := strings.IndexByte(stem, '-'); i >= 0 {
		stem = stem[:i]
	}
	if stem == "" {
		return 0, false
	}
	if c := stem[0]; c >= '0' && c <= '9' {
		n, err := strconv.Atoi(stem)
		if err != nil || n < 0 || n > 127 {
			return 0, false
		}
		return n, true
	}
	c := stem[0]
	if c >= 'a' && c <= 'g' {
		c -= 'a' - 'A'
	}
	var base int
	switch c {
	case 'C':
		base = 0
	case 'D':
		base = 2
	case 'E':
		base = 4
	case 'F':
		base = 5
	case 'G':
		base = 7
	case 'A':
		base = 9
	case 'B':
		base = 11
	default:
		return 0, false
	}
	i, acc := 1, 0
	if i < len(stem) {
		switch stem[i] {
		case '#':
			acc, i = 1, i+1
		case 'b':
			acc, i = -1, i+1
		}
	}
	oct, err := strconv.Atoi(stem[i:])
	if err != nil {
		return 0, false
	}
	key := (oct+1)*12 + base + acc
	if key < 0 || key > 127 {
		return 0, false
	}
	return key, true
}

// TestWindKeyFromFileName pins the naming contract itself, corpus or no
// corpus: the parser must agree with the doc's worked example and with
// the guitar corpus's existing spellings before any recording relies on
// it.
func TestWindKeyFromFileName(t *testing.T) {
	for _, c := range []struct {
		name string
		key  int
		ok   bool
	}{
		// The doc's worked example: written D5 on a soprano sax =
		// concert C5 = key 72, three equivalent spellings.
		{"c5.wav", 72, true},
		{"c5-mf-vibrato.wav", 72, true},
		{"72-altissimo.wav", 72, true},
		// The guitar corpus's own style (case-insensitive, .flac too).
		{"e2.wav", 40, true},
		{"A2.FLAC", 45, true},
		// Accidentals: # and b, exactly parsePitch's grammar.
		{"g#4.wav", 68, true},
		{"eb5-scoop.flac", 75, true},
		// Not pitches: named-for-what-it-is guitar files must not parse
		// as expectations, and garbage must not slip through.
		{"open-g-slow-strum.wav", 0, false},
		{"take3.wav", 0, false},
		{"h4.wav", 0, false},
		{"200.wav", 0, false},
		{"-c5.wav", 0, false},
	} {
		key, ok := keyFromFileName(c.name)
		if ok != c.ok || (ok && key != c.key) {
			t.Errorf("keyFromFileName(%q) = %d, %v; want %d, %v", c.name, key, ok, c.key, c.ok)
		}
	}
}

// TestWindTonesCorpusTracksConcertKey walks every recording in the
// wind/tones corpus through the real detector and tracker — configured
// exactly as a live soprano sax session configures them (ConfigForKeys
// over the instrument's sounding compass, Strums off; see
// cmd/musictutor/live.go and internal/practice/windtrip_test.go) — and
// asserts the tracked tone lands on the file name's concert key.
//
// The judged note is the longest one tracked: a real take carries breath
// before the tone and a release after it, and the long tone is by
// definition the longest voiced stretch.
//
// Tolerance: ±50 cents, NOT the ±5 the tracker's sine tests use — that
// figure is synthesis-specific (a machine-perfect source), and a human
// long tone drifts and carries vibrato. 50 cents is the semitone
// quantization boundary: any looser and a semitone error could pass,
// any tighter and the test starts grading intonation when the question
// is whether the tracker found the right NOTE. It also brackets the
// scorer's own bounds (35-cent Hit, 70-cent Close), so a recording that
// passes here is one the app would at worst score Close.
func TestWindTonesCorpusTracksConcertKey(t *testing.T) {
	files := corpus.Require(t, "../..", corpus.WindTones, ".wav", ".flac")

	sax := score.WindByName("soprano sax")
	if sax == nil {
		t.Fatal("soprano sax missing from score.WindInstruments")
	}
	cfg := ConfigForKeys(audiofile.SampleRate, sax.LowSounding, sax.LowSounding+sax.Span)

	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			want, ok := keyFromFileName(name)
			if !ok {
				t.Fatalf("%s does not name its pitch — see docs/TESTDATA.md (c5.wav, c5-mf.wav, 72-altissimo.wav)", name)
			}

			left, right, warnings, err := audiofile.Load(path)
			if err != nil {
				t.Fatalf("loading %s: %v", name, err)
			}
			for _, w := range warnings {
				t.Logf("%s: %s", name, w)
			}
			mono := make([]float32, len(left))
			for i := range mono {
				mono[i] = 0.5 * (left[i] + right[i])
			}

			d := NewDetector(cfg)
			tr := NewTracker(cfg)
			var notes []Note
			for off := 0; off < len(mono); off += 480 {
				end := off + 480
				if end > len(mono) {
					end = len(mono)
				}
				notes = append(notes, tr.Feed(d.Process(mono[off:end]))...)
			}
			notes = append(notes, tr.Flush()...)
			if len(notes) == 0 {
				t.Fatalf("no note tracked in %s (expected key %d)", name, want)
			}

			longest := notes[0]
			for _, n := range notes[1:] {
				if n.End-n.Start > longest.End-longest.Start {
					longest = n
				}
			}
			dev := float64(longest.Key-want)*100 + longest.Cents
			t.Logf("%s: %d notes, longest key %d %+.1f cents (want %d), clarity %.2f",
				name, len(notes), longest.Key, longest.Cents, want, longest.Clarity)
			if math.Abs(dev) > 50 {
				t.Errorf("tracked key %d %+.1f cents is %+.1f cents from expected key %d, want within ±50",
					longest.Key, longest.Cents, dev, want)
			}
		})
	}
}
