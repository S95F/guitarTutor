package pitch

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

func TestWindKeyFromFileName(t *testing.T) {
	for _, c := range []struct {
		name string
		key  int
		ok   bool
	}{

		{"c5.wav", 72, true},
		{"c5-mf-vibrato.wav", 72, true},
		{"72-altissimo.wav", 72, true},

		{"e2.wav", 40, true},
		{"A2.FLAC", 45, true},

		{"g#4.wav", 68, true},
		{"eb5-scoop.flac", 75, true},

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
