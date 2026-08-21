package textfmt

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
)

// fixture returns the path of a fixture in the repository's testdata
// directory.
func fixture(name string) string {
	return filepath.Join("..", "..", "..", "testdata", name)
}

// mustParse parses src and fails the test on error.
func mustParse(t *testing.T, src string) *score.Score {
	t.Helper()
	s, err := Parse([]byte(src), "test")
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return s
}

func TestParseCanonicalFixture(t *testing.T) {
	s, err := ParseFile(fixture("fixture_riff.gtab"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if s.Title != "Fixture Riff" {
		t.Errorf("title = %q, want %q", s.Title, "Fixture Riff")
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.Role != score.RoleUser {
		t.Errorf("role = %v, want RoleUser", tr.Role)
	}
	if tr.Capo != 0 {
		t.Errorf("capo = %d, want 0", tr.Capo)
	}
	if len(tr.Tuning) != len(score.StandardTuning) {
		t.Fatalf("tuning has %d strings, want %d", len(tr.Tuning), len(score.StandardTuning))
	}
	for i, k := range score.StandardTuning {
		if tr.Tuning[i] != k {
			t.Errorf("tuning[%d] = %d, want %d", i, tr.Tuning[i], k)
		}
	}
	if len(s.Tempos) != 1 || s.Tempos[0] != (score.Tempo{Tick: 0, USPerQuarter: score.USPerQuarter(120)}) {
		t.Errorf("tempo map = %v, want one entry at tick 0, 120 BPM", s.Tempos)
	}
	if len(s.Meters) != 1 || s.Meters[0] != (score.Meter{Tick: 0, Num: 4, Den: 4}) {
		t.Errorf("meter map = %v, want one entry at tick 0, 4/4", s.Meters)
	}

	// The canonical event table from docs/TEXTFORMAT.md and the task spec.
	want := []struct {
		start, end int64
		key        int
	}{
		{0, 480, 40}, {480, 960, 43}, {960, 1440, 50}, {1440, 1920, 40},
		{1920, 2400, 43}, {2400, 2880, 50}, {2880, 3360, 43}, {3360, 3840, 40},
		{3840, 4800, 47}, {4800, 5760, 45}, {5760, 7680, 47},
		{7680, 9600, 40}, {7680, 9600, 47}, {7680, 9600, 52},
		{10560, 11520, 43},
		{11520, 15360, 40},
	}
	evs := s.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(want), evs)
	}
	for i, w := range want {
		ev := evs[i]
		if ev.Start != w.start || ev.End != w.end || ev.Key != w.key {
			t.Errorf("event %d = (%d,%d) key %d, want (%d,%d) key %d",
				i, ev.Start, ev.End, ev.Key, w.start, w.end, w.key)
		}
		if ev.Track != 0 {
			t.Errorf("event %d track = %d, want 0", i, ev.Track)
		}
		if ev.Velocity != score.DefaultVelocity {
			t.Errorf("event %d velocity = %v, want %v", i, ev.Velocity, score.DefaultVelocity)
		}
	}
}

func TestParseRichFixture(t *testing.T) {
	s, err := ParseFile(fixture("fixture_rich.gtab"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if s.Title != "Rich Fixture" {
		t.Errorf("title = %q, want %q", s.Title, "Rich Fixture")
	}
	if len(s.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(s.Tracks))
	}
	lead, rhythm := s.Tracks[0], s.Tracks[1]

	if lead.Name != "Lead" || lead.Role != score.RoleUser || lead.Capo != 2 || lead.Program != 27 {
		t.Errorf("lead = %q role %v capo %d program %d, want Lead/RoleUser/2/27",
			lead.Name, lead.Role, lead.Capo, lead.Program)
	}
	wantTuning := score.Tuning{64, 59, 55, 50, 45, 38} // drop D, highest first
	if len(lead.Tuning) != len(wantTuning) {
		t.Fatalf("lead tuning has %d strings, want %d", len(lead.Tuning), len(wantTuning))
	}
	for i, k := range wantTuning {
		if lead.Tuning[i] != k {
			t.Errorf("lead tuning[%d] = %d, want %d", i, lead.Tuning[i], k)
		}
	}
	if rhythm.Name != "Rhythm" || rhythm.Role != score.RoleBacking || rhythm.Capo != 0 || rhythm.Program != 33 {
		t.Errorf("rhythm = %q role %v capo %d program %d, want Rhythm/RoleBacking/0/33",
			rhythm.Name, rhythm.Role, rhythm.Capo, rhythm.Program)
	}
	for i, k := range score.StandardTuning {
		if rhythm.Tuning[i] != k {
			t.Errorf("rhythm tuning[%d] = %d, want %d (standard)", i, rhythm.Tuning[i], k)
		}
	}

	wantTempos := score.TempoMap{
		{Tick: 0, USPerQuarter: score.USPerQuarter(96)},
		{Tick: 7680, USPerQuarter: score.USPerQuarter(132)},
	}
	if len(s.Tempos) != len(wantTempos) {
		t.Fatalf("tempo map = %v, want %v", s.Tempos, wantTempos)
	}
	for i, w := range wantTempos {
		if s.Tempos[i] != w {
			t.Errorf("tempo[%d] = %v, want %v", i, s.Tempos[i], w)
		}
	}
	wantMeters := score.MeterMap{{Tick: 0, Num: 4, Den: 4}, {Tick: 7680, Num: 3, Den: 4}}
	if len(s.Meters) != len(wantMeters) {
		t.Fatalf("meter map = %v, want %v", s.Meters, wantMeters)
	}
	for i, w := range wantMeters {
		if s.Meters[i] != w {
			t.Errorf("meter[%d] = %v, want %v", i, s.Meters[i], w)
		}
	}

	// Lead bar 1: dotted and (sticky) triplet durations.
	if len(lead.Bars) != 4 {
		t.Fatalf("lead has %d bars, want 4", len(lead.Bars))
	}
	wantDurs := []int64{1440, 480, 320, 320, 320, 960}
	b1 := lead.Bars[0]
	if len(b1.Beats) != len(wantDurs) {
		t.Fatalf("lead bar 1 has %d beats, want %d", len(b1.Beats), len(wantDurs))
	}
	for i, w := range wantDurs {
		if b1.Beats[i].Dur != w {
			t.Errorf("lead bar 1 beat %d dur = %d, want %d", i, b1.Beats[i].Dur, w)
		}
	}

	// Lead bar 2: all six technique letters, in order.
	wantTech := []score.Technique{
		score.TechHammer, score.TechPull, score.TechSlide,
		score.TechBend, score.TechVibrato, score.TechDead,
	}
	b2 := lead.Bars[1]
	if len(b2.Beats) != len(wantTech) {
		t.Fatalf("lead bar 2 has %d beats, want %d", len(b2.Beats), len(wantTech))
	}
	for i, w := range wantTech {
		if got := b2.Beats[i].Notes[0].Tech; got != w {
			t.Errorf("lead bar 2 beat %d tech = %b, want %b", i, got, w)
		}
	}

	// Lead bars 3-4 start under the 3/4 meter.
	if b3 := lead.Bars[2]; b3.Start != 7680 || b3.Num != 3 || b3.Den != 4 {
		t.Errorf("lead bar 3 = start %d meter %d/%d, want 7680 3/4", b3.Start, b3.Num, b3.Den)
	}
	// Rhythm bar starts and meters follow the same maps.
	wantStarts := []int64{0, 3840, 7680, 10560}
	if len(rhythm.Bars) != len(wantStarts) {
		t.Fatalf("rhythm has %d bars, want %d", len(rhythm.Bars), len(wantStarts))
	}
	for i, w := range wantStarts {
		if rhythm.Bars[i].Start != w {
			t.Errorf("rhythm bar %d start = %d, want %d", i+1, rhythm.Bars[i].Start, w)
		}
	}
	if b := rhythm.Bars[2]; b.Num != 3 || b.Den != 4 {
		t.Errorf("rhythm bar 3 meter = %d/%d, want 3/4", b.Num, b.Den)
	}

	evs := s.Events()
	// Capo + drop D: lead opens on low D (38) + capo 2 = 40 at tick 0;
	// the rhythm track's standard low E (40) sounds at tick 0 too, and
	// same-tick order is broken by track.
	if evs[0].Track != 0 || evs[0].Start != 0 || evs[0].Key != 40 {
		t.Errorf("event 0 = track %d (%d, key %d), want track 0 (0, key 40)", evs[0].Track, evs[0].Start, evs[0].Key)
	}
	if evs[1].Track != 1 || evs[1].Start != 0 || evs[1].Key != 40 {
		t.Errorf("event 1 = track %d (%d, key %d), want track 1 (0, key 40)", evs[1].Track, evs[1].Start, evs[1].Key)
	}
	// Lead bar 3: the tied chord merges into three events spanning the
	// whole bar. Keys include the capo: 50+2+2, 55+2+3, 59+2+2.
	var chordKeys []int
	for _, ev := range evs {
		if ev.Track == 0 && ev.Start == 7680 {
			if ev.End != 10560 {
				t.Errorf("lead chord event ends at %d, want 10560", ev.End)
			}
			chordKeys = append(chordKeys, ev.Key)
		}
	}
	if want := []int{54, 60, 63}; len(chordKeys) != len(want) {
		t.Errorf("lead chord keys = %v, want %v", chordKeys, want)
	} else {
		for i, w := range want {
			if chordKeys[i] != w {
				t.Errorf("lead chord key %d = %d, want %d", i, chordKeys[i], w)
			}
		}
	}
	// Lead bar 4: quarter + tied eighth merge into one event on G3 fret 5
	// (55+2+5 = 62).
	found := false
	for _, ev := range evs {
		if ev.Track == 0 && ev.Start == 11520 {
			found = true
			if ev.End != 12960 || ev.Key != 62 {
				t.Errorf("lead tied event = (%d,%d) key %d, want (11520,12960) key 62", ev.Start, ev.End, ev.Key)
			}
		}
	}
	if !found {
		t.Error("no lead event at tick 11520")
	}
	// Rhythm bar 3: one dotted-half D3 (50+2) filling the 3/4 bar.
	found = false
	for _, ev := range evs {
		if ev.Track == 1 && ev.Start == 7680 {
			found = true
			if ev.End != 10560 || ev.Key != 52 {
				t.Errorf("rhythm bar 3 event = (%d,%d) key %d, want (7680,10560) key 52", ev.Start, ev.End, ev.Key)
			}
		}
	}
	if !found {
		t.Error("no rhythm event at tick 7680")
	}
	if end := s.End(); end != 13440 {
		t.Errorf("End = %d, want 13440", end)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		line, col int
		sub       string
	}{
		{"underfull bar at separator", "0.6.4 |", 1, 7, "underfull"},
		{"underfull bar at EOF", "0.6.4", 1, 6, "underfull"},
		{"overfull bar", "0.6.1 0.6.4", 1, 7, "overfull"},
		{"string too high", "0.7.4", 1, 3, "string 7 out of range"},
		{"string zero", "5.0.4", 1, 3, "string 0 out of range"},
		{"fret too high", "31.6.4", 1, 1, "fret 31 out of range"},
		{"unknown directive", "\\foo bar", 1, 1, `unknown directive \foo`},
		{"malformed fret", "x.5.4", 1, 1, "expected a fret number"},
		{"malformed string", "5..4", 1, 3, "expected a string number"},
		{"invalid duration", "0.6.7", 1, 5, "invalid duration 7"},
		{"dotted triplet", "0.6.8t.", 1, 7, "unknown technique"},
		{"unknown technique", "0.6.4q", 1, 6, "unknown technique"},
		{"tempo not a number", "\\tempo fast", 1, 8, "invalid tempo"},
		{"tempo zero", "\\tempo 0", 1, 8, "invalid tempo"},
		{"tempo NaN", "\\tempo NaN", 1, 8, "invalid tempo"},
		{"tempo denormal", "\\tempo 1e-300", 1, 8, "invalid tempo"},
		{"tempo infinity", "\\tempo Inf", 1, 8, "invalid tempo"},
		{"tempo below one", "\\tempo 0.5", 1, 8, "invalid tempo"},
		{"note above MIDI range", "\\tuning 40 45 50 55 59 100\n\\capo 5\n30.1.1", 3, 1, "MIDI key 135"},
		{"chord note above MIDI range", "\\tuning 100 101\n(0.2 30.1).1", 2, 6, "MIDI key 131"},
		{"bad time signature", "\\time 5/3", 1, 7, "invalid time signature"},
		{"mid-bar tempo", "0.6.2 0.6.4\n\\tempo 140\n0.6.4 |", 2, 1, "between bars"},
		{"mid-bar time", "0.6.2 0.6.4\n\\time 3/4\n0.6.4 |", 2, 1, "between bars"},
		{"mid-bar track", "0.6.4\n\\track Two", 2, 1, "between bars"},
		{"tuning after bars", "0.6.1 |\n\\tuning D2 A2 D3 G3 B3 E4", 2, 1, "before the current track's first bar"},
		{"capo after bars", "0.6.1 |\n\\capo 2", 2, 1, "before the current track's first bar"},
		{"backing after bars", "0.6.1 |\n\\backing", 2, 1, "before the current track's first bar"},
		{"title after bars", "0.6.1 |\n\\title Late", 2, 1, "before the first bar"},
		{"bad tuning note", "\\tuning E2 A2 D3 G3 B3 H4", 1, 24, "invalid tuning note"},
		{"tuning MIDI out of range", "\\tuning 40 45 50 55 59 128", 1, 24, "out of range"},
		{"negative capo", "\\capo -1", 1, 7, "invalid capo"},
		{"program out of range", "\\program 200", 1, 10, "invalid program"},
		{"empty bar", "0.6.1 | | 0.6.1", 1, 9, "empty bar"},
		{"unterminated chord", "(0.6 2.5", 1, 1, "unterminated chord"},
		{"bar line inside chord", "(0.6 | 2.5).4", 1, 6, "inside a chord"},
		{"empty chord", "().4", 1, 1, "empty chord"},
		{"chord note with duration", "(0.6.4 2.5).4", 1, 5, "no duration"},
		{"doubled string in chord", "(0.6 2.6).2", 1, 6, "twice"},
		{"technique on rest", "r.4h", 1, 4, "after rest"},
		{"tied rest", "~r.4", 1, 2, "tie a rest"},
		{"lone tie", "~ 0.6.4", 1, 2, "must be followed"},
		{"unexpected close paren", "0.6.4 )", 1, 7, `")"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src), "test")
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error", tt.src)
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error type %T, want *ParseError (%v)", err, err)
			}
			if pe.Line != tt.line || pe.Col != tt.col {
				t.Errorf("position %d:%d, want %d:%d (err: %v)", pe.Line, pe.Col, tt.line, tt.col, err)
			}
			if !strings.Contains(pe.Msg, tt.sub) {
				t.Errorf("message %q does not contain %q", pe.Msg, tt.sub)
			}
			if want := "test:"; !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Error() = %q, want %q prefix", err.Error(), want)
			}
		})
	}
}

func TestStickyDuration(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []int64 // beat durations across all bars, in order
	}{
		{"initially a quarter", "0.6 0.6 0.6 0.6", []int64{960, 960, 960, 960}},
		{"explicit then sticky", "0.6.8 0.6 0.6 0.6 0.6.4 0.6", []int64{480, 480, 480, 480, 960, 960}},
		{"dotted sticks", "0.6.4. 0.6 0.6.8 0.6", []int64{1440, 1440, 480, 480}},
		{"triplet sticks", "0.6.4t 0.6 0.6 0.6 0.6 0.6", []int64{640, 640, 640, 640, 640, 640}},
		{"sticks across bars", "0.6.2 0.6 | 0.6 0.6", []int64{1920, 1920, 1920, 1920}},
		{"sticks through rests", "0.6.2 r | r 0.6", []int64{1920, 1920, 1920, 1920}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustParse(t, tt.src)
			var got []int64
			for _, bar := range s.Tracks[0].Bars {
				for _, b := range bar.Beats {
					got = append(got, b.Dur)
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d beats %v, want %v", len(got), got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Errorf("beat %d dur = %d, want %d", i, got[i], w)
				}
			}
		})
	}
}

func TestTieSemantics(t *testing.T) {
	// A tie within a bar merges into one event.
	s := mustParse(t, "0.6.2 ~0.6.2")
	if got := s.Tracks[0].Bars[0].Beats[1].Notes[0]; !got.Tied {
		t.Error("second beat's note is not marked Tied")
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 3840 || evs[0].Key != 40 {
		t.Errorf("events = %+v, want one event (0,3840) key 40", evs)
	}

	// A tie merges across a bar line.
	s = mustParse(t, "0.6.1 | ~0.6.1")
	evs = s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 7680 {
		t.Errorf("events = %+v, want one event (0,7680)", evs)
	}

	// A tie to a different fret degrades to a fresh attack.
	s = mustParse(t, "0.6.2 ~3.6.2")
	if evs = s.Events(); len(evs) != 2 {
		t.Errorf("got %d events, want 2 (mismatched tie must not merge)", len(evs))
	}

	// "~" on a chord ties every note.
	s = mustParse(t, "(0.6 2.5).2 ~(0.6 2.5).2")
	evs = s.Events()
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2 merged chord events", len(evs))
	}
	for i, key := range []int{40, 47} {
		if evs[i].Start != 0 || evs[i].End != 3840 || evs[i].Key != key {
			t.Errorf("chord event %d = (%d,%d) key %d, want (0,3840) key %d",
				i, evs[i].Start, evs[i].End, evs[i].Key, key)
		}
	}

	// "~" on a single chord note ties just that string.
	s = mustParse(t, "(0.6 2.5).2 (~0.6 4.5).2")
	evs = s.Events()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	if evs[0].Key != 40 || evs[0].Start != 0 || evs[0].End != 3840 {
		t.Errorf("merged event = (%d,%d) key %d, want (0,3840) key 40", evs[0].Start, evs[0].End, evs[0].Key)
	}
	if evs[1].Key != 47 || evs[1].End != 1920 {
		t.Errorf("first chord's B = (%d,%d) key %d, want (0,1920) key 47", evs[1].Start, evs[1].End, evs[1].Key)
	}
	if evs[2].Key != 49 || evs[2].Start != 1920 || evs[2].End != 3840 {
		t.Errorf("second chord's C# = (%d,%d) key %d, want (1920,3840) key 49", evs[2].Start, evs[2].End, evs[2].Key)
	}
}

func TestDefaultsAndTitleSeeding(t *testing.T) {
	s := mustParse(t, "0.6.1")
	if s.Title != "test" {
		t.Errorf("title = %q, want the name %q", s.Title, "test")
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.Name != "" || tr.Role != score.RoleUser || tr.Capo != 0 || tr.Program != 25 {
		t.Errorf("default track = %q role %v capo %d program %d, want unnamed/RoleUser/0/25",
			tr.Name, tr.Role, tr.Capo, tr.Program)
	}
	if len(s.Tempos) != 1 || s.Tempos[0] != (score.Tempo{Tick: 0, USPerQuarter: score.USPerQuarter(120)}) {
		t.Errorf("tempo map = %v, want default 120 BPM at tick 0", s.Tempos)
	}
	if len(s.Meters) != 1 || s.Meters[0] != (score.Meter{Tick: 0, Num: 4, Den: 4}) {
		t.Errorf("meter map = %v, want default 4/4 at tick 0", s.Meters)
	}
}

func TestMIDINumberTuning(t *testing.T) {
	s := mustParse(t, "\\tuning 38 45 50 55 59 64\n0.6.1")
	want := score.Tuning{64, 59, 55, 50, 45, 38}
	tr := s.Tracks[0]
	if len(tr.Tuning) != len(want) {
		t.Fatalf("tuning has %d strings, want %d", len(tr.Tuning), len(want))
	}
	for i, k := range want {
		if tr.Tuning[i] != k {
			t.Errorf("tuning[%d] = %d, want %d", i, tr.Tuning[i], k)
		}
	}
	if evs := s.Events(); len(evs) != 1 || evs[0].Key != 38 {
		t.Errorf("events = %+v, want one event with key 38", evs)
	}
}

func TestMisalignedTimeChange(t *testing.T) {
	// The \time in the second track's section lands at tick 3840, inside
	// the first track's already-parsed second 4/4 bar boundary layout.
	src := "0.6.1 | 0.6.1 |\n\\track B\n0.6.1 |\n\\time 3/4\n0.6.2. |"
	_, err := Parse([]byte(src), "test")
	if err == nil {
		t.Fatal("Parse succeeded, want a meter-alignment error")
	}
	if !strings.Contains(err.Error(), "align") {
		t.Errorf("error = %v, want it to mention alignment", err)
	}
}

func TestTempoThenTrackAnchorsAtPieceTick(t *testing.T) {
	// A \tempo written between one track's last bar and a \track directive
	// takes effect at the piece tick where it was written (the current
	// track's end), not at the new track's first bar. Applying it at the
	// new track's first bar — tick 0 — used to silently replace the header
	// tempo and retroactively re-tempo the whole piece.
	s := mustParse(t, "0.6.1 |\n\\tempo 140\n\\track B\n0.6.1 | 0.6.1 |")
	want := score.TempoMap{
		{Tick: 0, USPerQuarter: score.USPerQuarter(120)},
		{Tick: 3840, USPerQuarter: score.USPerQuarter(140)},
	}
	if len(s.Tempos) != len(want) {
		t.Fatalf("tempo map = %v, want %v", s.Tempos, want)
	}
	for i, w := range want {
		if s.Tempos[i] != w {
			t.Errorf("tempo[%d] = %v, want %v", i, s.Tempos[i], w)
		}
	}
}

func TestTimeThenTrackAnchorsAtPieceTick(t *testing.T) {
	// \time anchors the same way: written between the first track's last
	// bar and \track, it takes effect at the first track's end (tick
	// 3840), so the new track's first bar is still 4/4 and its second bar
	// picks up the 3/4.
	s := mustParse(t, "0.6.1 |\n\\time 3/4\n\\track B\n0.6.1 | 0.6.2. |")
	wantMeters := score.MeterMap{{Tick: 0, Num: 4, Den: 4}, {Tick: 3840, Num: 3, Den: 4}}
	if len(s.Meters) != len(wantMeters) {
		t.Fatalf("meter map = %v, want %v", s.Meters, wantMeters)
	}
	for i, w := range wantMeters {
		if s.Meters[i] != w {
			t.Errorf("meter[%d] = %v, want %v", i, s.Meters[i], w)
		}
	}
	bars := s.Tracks[1].Bars
	if len(bars) != 2 {
		t.Fatalf("track B has %d bars, want 2", len(bars))
	}
	if b := bars[0]; b.Start != 0 || b.Num != 4 || b.Den != 4 {
		t.Errorf("track B bar 1 = start %d meter %d/%d, want 0 4/4", b.Start, b.Num, b.Den)
	}
	if b := bars[1]; b.Start != 3840 || b.Num != 3 || b.Den != 4 {
		t.Errorf("track B bar 2 = start %d meter %d/%d, want 3840 3/4", b.Start, b.Num, b.Den)
	}

	// A \time-then-track whose new track cannot fill the still-4/4 first
	// bar stays a loud, positioned error rather than silently re-metering
	// the piece.
	_, err := Parse([]byte("0.6.1 |\n\\time 3/4\n\\track B\n0.6.2. |"), "test")
	if err == nil {
		t.Fatal("misaligned \\time-then-track parsed, want an error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error type %T, want *ParseError (%v)", err, err)
	}
}

func TestNoteAtMIDICeiling(t *testing.T) {
	// tuning 97 + capo 0 + fret 30 = exactly 127 is still accepted.
	s := mustParse(t, "\\tuning 97\n30.1.1")
	if evs := s.Events(); len(evs) != 1 || evs[0].Key != 127 {
		t.Errorf("events = %+v, want one event with key 127", evs)
	}
}

func TestNoBars(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"empty source", ""},
		{"header only", "\\title Header Only\n\\tempo 90\n\\time 3/4\n"},
		{"track directives only", "\\track Lead\n\\tuning D2 A2 D3 G3 B3 E4\n\\capo 2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src), "test")
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want %q error", tt.src, "piece has no bars")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error type %T, want *ParseError (%v)", err, err)
			}
			if !strings.Contains(pe.Msg, "piece has no bars") {
				t.Errorf("message %q does not contain %q", pe.Msg, "piece has no bars")
			}
		})
	}
}

// TestPendingDirectiveConflictRejected: the parser holds ONE pending
// \tempo and one pending \time. Two of a kind at the same anchor are
// last-wins (the map would replace a same-tick entry anyway), but when a
// \track switch moves the anchor the two apply at different piece ticks —
// keeping only the second silently erased the first from the piece (the
// track-A-end \tempo 140 below simply vanished). That is now a loud,
// positioned error.
func TestPendingDirectiveConflictRejected(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{
			name: "tempo across track switch",
			src:  "0.6.1 |\n\\tempo 140\n\\track B\n\\tempo 100\n0.6.1 | 0.6.1 |",
			want: `\tempo`,
		},
		{
			name: "time across track switch",
			src:  "0.6.1 |\n\\time 3/4\n\\track B\n\\time 4/4\n0.6.1 | 0.6.1 |",
			want: `\time`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src), "test")
			if err == nil {
				t.Fatal("conflicting pending directives parsed, want an error")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error type %T, want *ParseError (%v)", err, err)
			}
			if !strings.Contains(pe.Msg, "discard") || !strings.Contains(pe.Msg, tc.want) {
				t.Errorf("error = %v, want it to name the discarded %s", pe, tc.want)
			}
		})
	}

	// Same anchor, no track switch: the second replaces the first, which
	// is what the map would do with two same-tick entries anyway.
	s := mustParse(t, "0.6.1 |\n\\tempo 140\n\\tempo 100\n0.6.1 |")
	want := score.TempoMap{
		{Tick: 0, USPerQuarter: score.USPerQuarter(120)},
		{Tick: 3840, USPerQuarter: score.USPerQuarter(100)},
	}
	if len(s.Tempos) != len(want) {
		t.Fatalf("tempo map = %v, want %v", s.Tempos, want)
	}
	for i, w := range want {
		if s.Tempos[i] != w {
			t.Errorf("tempo[%d] = %v, want %v", i, s.Tempos[i], w)
		}
	}
}

// TestEndAnchoredDirectiveRejected: a \tempo or \time whose anchor is the
// very END of the piece changes nothing audible, and Format refuses to
// write the entry (no bar starts there) — so accepting it parsed a piece
// that could never be saved. Both spellings of the trap — flushed through
// a shorter second track, and still pending at end of input — are now
// loud, positioned errors.
func TestEndAnchoredDirectiveRejected(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{
			name: "tempo flushed by a track that ends at its anchor",
			src:  "0.6.1 |\n\\tempo 140\n\\track B\n0.6.1 |",
			want: `\tempo`,
		},
		{
			name: "tempo still pending at end of input",
			src:  "0.6.1 |\n\\tempo 140",
			want: `\tempo`,
		},
		{
			name: "time still pending at end of input",
			src:  "0.6.1 |\n\\time 3/4",
			want: `\time`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src), "test")
			if err == nil {
				t.Fatal("end-anchored directive parsed, want an error")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error type %T, want *ParseError (%v)", err, err)
			}
			if !strings.Contains(pe.Msg, "end of the piece") || !strings.Contains(pe.Msg, tc.want) {
				t.Errorf("error = %v, want it to name the end-anchored %s", pe, tc.want)
			}
			if pe.Line != 2 {
				t.Errorf("error at line %d, want the directive's line 2", pe.Line)
			}
		})
	}

	// The flush itself is the fix's other half: a directive left pending
	// at end of input whose anchor sits MID-piece (another track goes on
	// past it) used to be dropped silently — authored text ignored in
	// playback. It now lands at its anchor like any flushed directive.
	s := mustParse(t, "0.6.1 | 0.6.1 |\n\\track B\n0.6.1 |\n\\tempo 90")
	want := score.Tempo{Tick: 3840, USPerQuarter: score.USPerQuarter(90)}
	found := false
	for _, e := range s.Tempos {
		if e == want {
			found = true
		}
	}
	if !found {
		t.Errorf("tempo map = %v, want the trailing \\tempo applied at its tick-3840 anchor", s.Tempos)
	}
}

// TestBareCarriageReturnEndsLine: the scanner broke tokens and beats on a
// bare "\r" everywhere EXCEPT restOfLine and args — so a classic-Mac file
// (lines terminated by "\r" alone) ran its whole directive line into the
// title, and a lone "\r" inside a \track line survived into Track.Name,
// text Format refuses: a piece that parsed and could never be saved.
func TestBareCarriageReturnEndsLine(t *testing.T) {
	// A classic-Mac piece parses like its "\n" twin.
	s := mustParse(t, "\\title Mac Piece\r\\tempo 100\r\\track Lead\r0.6.1 |\r")
	if s.Title != "Mac Piece" {
		t.Errorf("Title = %q, want the title line ended at the bare CR", s.Title)
	}
	if got := s.Tracks[0].Name; got != "Lead" {
		t.Errorf("Name = %q, want %q", got, "Lead")
	}
	if got := s.Tempos[0].USPerQuarter; got != score.USPerQuarter(100) {
		t.Errorf("tempo = %d, want 100 BPM applied", got)
	}

	// Whatever restOfLine now hands back can always be written again.
	for _, src := range []string{
		"\\title A\rB\n0.6.1 |",
		"\\track A\r0.6.1 |\n",
	} {
		s, err := Parse([]byte(src), "test")
		if err != nil {
			continue // the CR-split remainder may well be a parse error
		}
		if _, err := Format(s); err != nil {
			t.Errorf("Parse accepted %q but Format refused: %v", src, err)
		}
	}
}
