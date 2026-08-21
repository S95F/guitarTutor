package textfmt

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
)

// gtabFixtures are the .gtab sources in the tree. Every one of them has to
// survive the round trip, so adding a fixture automatically extends the
// coverage here.
var gtabFixtures = []string{
	"../../../testdata/fixture_riff.gtab",
	"../../../testdata/fixture_rich.gtab",
	"../../../testdata/fixture_sax.gtab",
}

// parseFixture reads and parses one fixture, failing the test if it does
// not parse — the parser's own tests own that, so a failure here means the
// fixture moved, not that the round trip is broken.
func parseFixture(t *testing.T, path string) *score.Score {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	sc, err := Parse(src, "fixture")
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return sc
}

// TestFormatRoundTrip is the contract the editor rests on: what Format
// writes, Parse reads back as the same piece. Not "an equivalent piece" —
// the same one, field for field, because the editor saves through Format
// and re-opens through Parse, and any difference between the two is a
// piece that quietly changes every time it is edited.
func TestFormatRoundTrip(t *testing.T) {
	for _, path := range gtabFixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			want := parseFixture(t, path)
			src, err := Format(want)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			got, err := Parse(src, "fixture")
			if err != nil {
				t.Fatalf("re-parsing formatted output: %v\n--- output ---\n%s", err, src)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("round trip changed the score\n--- output ---\n%s", src)
				diffScores(t, want, got)
			}
		})
	}
}

// TestFormatIsStable checks that formatting is a fixed point: a piece that
// has already been through Format writes identically the second time.
// Without this a save could keep rewriting the same file with different
// text — the thing that turns a git-diffable format into noise.
func TestFormatIsStable(t *testing.T) {
	for _, path := range gtabFixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			sc := parseFixture(t, path)
			first, err := Format(sc)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			reparsed, err := Parse(first, "fixture")
			if err != nil {
				t.Fatalf("re-parsing: %v", err)
			}
			second, err := Format(reparsed)
			if err != nil {
				t.Fatalf("Format (second pass): %v", err)
			}
			if string(first) != string(second) {
				t.Errorf("formatting is not stable\n--- first ---\n%s\n--- second ---\n%s", first, second)
			}
		})
	}
}

// diffScores reports the first structural difference it finds, so a failed
// round trip names the bar rather than dumping two whole scores.
func diffScores(t *testing.T, want, got *score.Score) {
	t.Helper()
	if want.Title != got.Title {
		t.Errorf("title: got %q, want %q", got.Title, want.Title)
	}
	if !reflect.DeepEqual(want.Tempos, got.Tempos) {
		t.Errorf("tempo map: got %v, want %v", got.Tempos, want.Tempos)
	}
	if !reflect.DeepEqual(want.Meters, got.Meters) {
		t.Errorf("meter map: got %v, want %v", got.Meters, want.Meters)
	}
	if len(want.Tracks) != len(got.Tracks) {
		t.Errorf("track count: got %d, want %d", len(got.Tracks), len(want.Tracks))
		return
	}
	for i := range want.Tracks {
		wt, gt := want.Tracks[i], got.Tracks[i]
		if wt.Name != gt.Name || wt.Capo != gt.Capo || wt.Program != gt.Program || wt.Role != gt.Role ||
			!reflect.DeepEqual(wt.Tuning, gt.Tuning) {
			t.Errorf("track %d header: got %+v, want %+v",
				i+1,
				score.Track{Name: gt.Name, Tuning: gt.Tuning, Capo: gt.Capo, Program: gt.Program, Role: gt.Role},
				score.Track{Name: wt.Name, Tuning: wt.Tuning, Capo: wt.Capo, Program: wt.Program, Role: wt.Role})
		}
		if len(wt.Bars) != len(gt.Bars) {
			t.Errorf("track %d: got %d bars, want %d", i+1, len(gt.Bars), len(wt.Bars))
			continue
		}
		for bi := range wt.Bars {
			if !reflect.DeepEqual(wt.Bars[bi], gt.Bars[bi]) {
				t.Errorf("track %d bar %d: got %+v, want %+v", i+1, bi+1, *gt.Bars[bi], *wt.Bars[bi])
			}
		}
	}
}

// TestFormatOmitsDefaults keeps the output readable: the things a .gtab
// file does not have to say, it does not say.
func TestFormatOmitsDefaults(t *testing.T) {
	sc := parseFixture(t, gtabFixtures[0])
	src, err := Format(sc)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	text := string(src)
	for _, unwanted := range []string{"\\tuning", "\\capo", "\\program", "\\backing", "\\track"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("output states the default %s:\n%s", unwanted, text)
		}
	}
	// The sticky duration is what keeps a run of eighth notes from
	// repeating ".8" eight times.
	if n := strings.Count(text, ".8"); n != 1 {
		t.Errorf("got %d explicit eighth durations, want 1 (the rest should stick):\n%s", n, text)
	}
}

// TestFormatDurations checks every note value the format can hold, in a
// piece built directly rather than parsed, so the duration table is
// exercised independently of what the fixtures happen to use.
func TestFormatDurations(t *testing.T) {
	for _, d := range DurationNames() {
		t.Run(d.Name, func(t *testing.T) {
			// A bar this duration tiles exactly. Counting the meter in
			// 32nds (den 32, one beat = 120 ticks) reaches every value in
			// the table — including the triplets and dotted values that do
			// not divide a quarter note — within the 64 beats the format
			// allows a bar.
			const den, beat = 32, 4 * score.PPQ / 32
			num := 0
			for n := 1; n <= 64; n++ {
				if int64(n)*beat%d.Ticks == 0 {
					num = n
					break
				}
			}
			if num == 0 {
				t.Fatalf("no bar of up to 64/%d is tiled by a %s (%d ticks)", den, d.Name, d.Ticks)
			}
			sc := &score.Score{
				Title:  "Durations",
				Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
				Meters: score.MeterMap{{Tick: 0, Num: num, Den: den}},
			}
			tr := &score.Track{
				Tuning:  append(score.Tuning(nil), score.StandardTuning...),
				Program: defaultProgram,
			}
			sc.Tracks = append(sc.Tracks, tr)
			bar := tr.AppendBar(num, den)
			for filled := int64(0); filled < bar.Len(); filled += d.Ticks {
				bar.AddBeat(d.Ticks, score.Note{String: 6, Fret: 0})
			}
			src, err := Format(sc)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			// The quarter is the sticky duration a track starts on, so a bar
			// of quarters states nothing at all — which is the point of the
			// sticky rule, and worth asserting rather than exempting.
			if d.Ticks == score.Quarter {
				if strings.Contains(string(src), ".4") {
					t.Errorf("a bar of quarter notes spells its duration; the quarter is the sticky default:\n%s", src)
				}
			} else if !strings.Contains(string(src), "."+d.Name) {
				t.Errorf("output does not spell the duration as %q:\n%s", "."+d.Name, src)
			}
			got, err := Parse(src, "Durations")
			if err != nil {
				t.Fatalf("re-parsing %q: %v", src, err)
			}
			if !reflect.DeepEqual(got, sc) {
				t.Errorf("round trip changed a %s beat\n--- output ---\n%s", d.Name, src)
				diffScores(t, sc, got)
			}
		})
	}
}

// TestDurationNamesCovers checks the palette the editor is built from is
// the whole format: six note values, each plain, dotted and triplet.
func TestDurationNamesCovers(t *testing.T) {
	got := DurationNames()
	if len(got) != 18 {
		t.Errorf("got %d durations, want 18 (6 values x plain/dotted/triplet)", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ticks <= got[i-1].Ticks {
			t.Errorf("durations are not shortest-first at %d: %v then %v", i, got[i-1], got[i])
		}
	}
	for _, d := range got {
		if _, ok := durationName(d.Ticks); !ok {
			t.Errorf("DurationNames offers %d ticks, which durationName cannot spell", d.Ticks)
		}
	}
}

// TestBPMStringRoundTrips checks the tempo spelling over the whole range
// the format accepts. Tempo is stored as microseconds per quarter and BPM
// is its reciprocal, so most of these are not round numbers; the point is
// that every one of them reads back as the exact value it came from.
func TestBPMStringRoundTrips(t *testing.T) {
	for us := int64(60000); us <= 6000000; us += 977 { // 1000 down to 10 BPM
		s, err := bpmString(us)
		if err != nil {
			continue // outside the 1-1000 BPM the format accepts
		}
		// strconv.ParseFloat then score.USPerQuarter is exactly what the
		// parser's \tempo case does, so this checks the real conversion and
		// not a lookalike.
		bpm, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatalf("bpmString(%d) = %q, which is not a number: %v", us, s, err)
		}
		if !(bpm >= 1 && bpm <= 1000) {
			t.Fatalf("bpmString(%d) = %q, outside the 1-1000 the parser accepts", us, s)
		}
		if got := score.USPerQuarter(bpm); got != us {
			t.Fatalf("bpmString(%d) = %q, which reads back as %d", us, s, got)
		}
	}
}

// TestFormatRefusesWhatItCannotWrite pins the three shapes the model
// allows and the format does not. Each has to name the problem rather than
// emit text the parser would reject — or, worse, text that reads back as a
// different piece.
func TestFormatRefusesWhatItCannotWrite(t *testing.T) {
	base := func() (*score.Score, *score.Track) {
		sc := &score.Score{
			Title:  "T",
			Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
			Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
		}
		tr := &score.Track{Tuning: append(score.Tuning(nil), score.StandardTuning...), Program: defaultProgram}
		sc.Tracks = append(sc.Tracks, tr)
		return sc, tr
	}

	t.Run("unwritable duration", func(t *testing.T) {
		sc, tr := base()
		bar := tr.AppendBar(4, 4)
		bar.AddBeat(1000, score.Note{String: 6, Fret: 0}) // not a note value
		bar.AddBeat(score.Whole-1000, score.Note{String: 6, Fret: 0})
		_, err := Format(sc)
		if err == nil || !strings.Contains(err.Error(), "not a note value") {
			t.Fatalf("got %v, want an error naming the duration", err)
		}
	})

	t.Run("tempo change off the barline", func(t *testing.T) {
		sc, tr := base()
		bar := tr.AppendBar(4, 4)
		bar.AddBeat(score.Whole, score.Note{String: 6, Fret: 0})
		sc.Tempos = append(sc.Tempos, score.Tempo{Tick: score.Quarter, USPerQuarter: score.USPerQuarter(90)})
		_, err := Format(sc)
		if err == nil || !strings.Contains(err.Error(), "barline") {
			t.Fatalf("got %v, want an error naming the barline rule", err)
		}
	})

	t.Run("one string twice in a beat", func(t *testing.T) {
		sc, tr := base()
		bar := tr.AppendBar(4, 4)
		bar.AddBeat(score.Whole,
			score.Note{String: 3, Fret: 0},
			score.Note{String: 3, Fret: 2})
		_, err := Format(sc)
		if err == nil || !strings.Contains(err.Error(), "played twice") {
			t.Fatalf("got %v, want an error naming the repeated string", err)
		}
	})
}

// TestFormatNamesLaterTracks checks that a nameless second track is given
// one. \track needs a name to exist, and without this the track's bars
// would be appended to its predecessor — a silently different piece.
func TestFormatNamesLaterTracks(t *testing.T) {
	sc := &score.Score{
		Title:  "Two",
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	for i := 0; i < 2; i++ {
		tr := &score.Track{Tuning: append(score.Tuning(nil), score.StandardTuning...), Program: defaultProgram}
		bar := tr.AppendBar(4, 4)
		bar.AddBeat(score.Whole, score.Note{String: 6, Fret: i})
		sc.Tracks = append(sc.Tracks, tr)
	}
	src, err := Format(sc)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	got, err := Parse(src, "Two")
	if err != nil {
		t.Fatalf("re-parsing: %v\n%s", err, src)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2\n%s", len(got.Tracks), src)
	}
	if got.Tracks[1].Name == "" {
		t.Errorf("the second track came back nameless\n%s", src)
	}
	if n := len(got.Tracks[0].Bars); n != 1 {
		t.Errorf("the first track has %d bars, want 1 — the second track's bars were merged into it\n%s", n, src)
	}
}

// TestWriteFileRoundTrip covers the save path end to end, including that
// the temporary file it writes through does not survive.
func TestWriteFileRoundTrip(t *testing.T) {
	sc := parseFixture(t, gtabFixtures[1])
	dir := t.TempDir()
	path := filepath.Join(dir, "piece.gtab")
	if err := WriteFile(path, sc); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	// ParseFile seeds the title from the file name; the fixture states one,
	// so this compares the real thing.
	if !reflect.DeepEqual(got, sc) {
		t.Error("WriteFile then ParseFile changed the score")
		diffScores(t, sc, got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("the save left %v behind; want only piece.gtab", names)
	}
}

// TestWriteFileOverwrites checks that saving over an existing piece
// replaces it whole, rather than leaving a longer previous version's tail
// behind it.
func TestWriteFileOverwrites(t *testing.T) {
	long := parseFixture(t, gtabFixtures[1])
	short := parseFixture(t, gtabFixtures[0])
	path := filepath.Join(t.TempDir(), "piece.gtab")
	if err := WriteFile(path, long); err != nil {
		t.Fatalf("WriteFile (long): %v", err)
	}
	if err := WriteFile(path, short); err != nil {
		t.Fatalf("WriteFile (short): %v", err)
	}
	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(got.Tracks) != 1 {
		t.Errorf("got %d tracks, want 1: the overwrite left the previous piece behind", len(got.Tracks))
	}
}
