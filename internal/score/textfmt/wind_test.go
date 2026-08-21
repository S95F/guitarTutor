package textfmt

// Wind tracks in .gtab: the \instrument directive, written-pitch beat
// tokens, the wind technique alphabet, and the refusals that keep a sax
// line from silently meaning something else. The round trip itself is
// covered by write_test.go's fixture sweep (fixture_sax.gtab).

import (
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
)

func TestParseWindTrack(t *testing.T) {
	src := `\instrument soprano sax
D5.4 E5.4 G5.4l r.4`
	s, err := Parse([]byte(src), "sax")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr := s.Tracks[0]
	if tr.Wind == nil || tr.Wind.Name != "soprano sax" {
		t.Fatalf("track instrument = %v, want soprano sax", tr.Wind)
	}
	if tr.Tuning != nil {
		t.Errorf("wind track kept a tuning: %v", tr.Tuning)
	}
	if tr.Program != 64 {
		t.Errorf("program = %d, want the soprano sax default 64", tr.Program)
	}
	// Written D5 (74) sounds C5 (72) on a B-flat soprano: offset 16 from
	// the low Ab3 (56).
	want := []score.Note{
		{String: 1, Fret: 16},
		{String: 1, Fret: 18},
		{String: 1, Fret: 21, Tech: score.TechSlur},
	}
	var got []score.Note
	for _, beat := range tr.Bars[0].Beats {
		got = append(got, beat.Notes...)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d notes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("note %d = %+v, want %+v", i, got[i], want[i])
		}
		if k := tr.Pitch(got[i]); k != 72+[]int{0, 2, 5}[i] {
			t.Errorf("note %d sounds %d, want %d", i, k, 72+[]int{0, 2, 5}[i])
		}
	}
}

func TestParseWindAccidentalsAndCase(t *testing.T) {
	src := `\instrument soprano sax
bb3.1 |
f#6.2 Gb6.2`
	s, err := Parse([]byte(src), "range")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr := s.Tracks[0]
	if k := tr.Pitch(tr.Bars[0].Beats[0].Notes[0]); k != 56 {
		t.Errorf("written Bb3 sounds %d, want 56 (the horn's lowest note)", k)
	}
	// F#6 and Gb6 are the same written key (90), sounding 88.
	for i, beat := range s.Tracks[0].Bars[1].Beats {
		if k := tr.Pitch(beat.Notes[0]); k != 88 {
			t.Errorf("bar 2 beat %d sounds %d, want 88", i, k)
		}
	}
}

// TestWindProgramOverride: an explicit \program survives \instrument in
// either order — the instrument only supplies the default.
func TestWindProgramOverride(t *testing.T) {
	for _, src := range []string{
		"\\program 70\n\\instrument soprano sax\nD5.1",
		"\\instrument soprano sax\n\\program 70\nD5.1",
	} {
		s, err := Parse([]byte(src), "prog")
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		if got := s.Tracks[0].Program; got != 70 {
			t.Errorf("Parse(%q): program = %d, want the explicit 70", src, got)
		}
	}
}

// TestWindRefusals: each source must fail, with a message that names the
// actual problem rather than a guitar concept the track does not have.
func TestWindRefusals(t *testing.T) {
	cases := []struct {
		name, src, wantSub string
	}{
		{"chord", "\\instrument soprano sax\n(D5 G5).1", "one note at a time"},
		{"tied chord", "\\instrument soprano sax\n~(D5 G5).1", "one note at a time"},
		{"guitar technique", "\\instrument soprano sax\nD5.1h", "valid: l s b v"},
		{"dead note", "\\instrument soprano sax\nD5.1x", "valid: l s b v"},
		{"slur letter on a guitar", "0.6.1l", "valid: h p s b v x"},
		// The lowest note is spelled A#3 rather than Bb3 because the
		// writer's pitchName has one spelling per pitch, sharps.
		{"below the horn", "\\instrument soprano sax\nA3.1", "below the soprano sax's lowest note, A#3"},
		{"fret token on a wind", "\\instrument soprano sax\n5.3.4", "written pitch name"},
		{"tuning after instrument", "\\instrument soprano sax\n\\tuning E2 A2 D3 G3 B3 E4\nD5.1", "no strings"},
		{"capo after instrument", "\\instrument soprano sax\n\\capo 2\nD5.1", "no capo"},
		{"instrument after tuning", "\\tuning E2 A2 D3 G3 B3 E4\n\\instrument soprano sax\nD5.1", "cannot follow"},
		{"instrument after bars", "\\instrument soprano sax\nD5.1 |\n\\instrument alto sax\nD5.1", "before the current track's first bar"},
		{"instrument twice", "\\instrument soprano sax\n\\instrument alto sax\nD5.1", "already a soprano sax"},
		{"unknown instrument", "\\instrument kazoo\nD5.1", "unknown instrument"},
		{"missing name", "\\instrument\nD5.1", "requires an instrument name"},
	}
	for _, tt := range cases {
		_, err := Parse([]byte(tt.src), tt.name)
		if err == nil {
			t.Errorf("%s: parsed", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error %q does not mention %q", tt.name, err, tt.wantSub)
		}
	}
}

// TestWindWriteRefusesForeignBits: Format goes through Validate, so a
// wind track that somehow acquired a pull-off is refused rather than
// written as a letter the parser would bounce.
func TestWindWriteRefusesForeignBits(t *testing.T) {
	s, err := Parse([]byte("\\instrument soprano sax\nD5.1"), "w")
	if err != nil {
		t.Fatal(err)
	}
	s.Tracks[0].Bars[0].Beats[0].Notes[0].Tech = score.TechPull
	if _, err := Format(s); err == nil {
		t.Error("Format wrote a pull-off on a soprano sax")
	}
}

// TestWindAltissimoParses: the standard range's top is the editor's cap,
// not the format's — a written G6, two semitones past the soprano's F#6,
// is altissimo and legal.
func TestWindAltissimoParses(t *testing.T) {
	s, err := Parse([]byte("\\instrument soprano sax\nG6.1"), "alt")
	if err != nil {
		t.Fatalf("altissimo G6 refused: %v", err)
	}
	tr := s.Tracks[0]
	if k := tr.Pitch(tr.Bars[0].Beats[0].Notes[0]); k != 89 {
		t.Errorf("G6 sounds %d, want 89", k)
	}
}
