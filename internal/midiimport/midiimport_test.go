package midiimport

import (
	"bytes"
	"testing"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/S95F/guitarTutor/internal/score"
)

// canonical is the fixture riff's exact flattened event table (see
// docs/TEXTFORMAT.md and ROADMAP Phase 0): Start/End in PPQ-960 ticks and
// derived MIDI key, in Events() order.
var canonical = []struct {
	start, end int64
	key        int
}{
	// Bar 1: eight eighth notes.
	{0, 480, 40}, {480, 960, 43}, {960, 1440, 50}, {1440, 1920, 40},
	{1920, 2400, 43}, {2400, 2880, 50}, {2880, 3360, 43}, {3360, 3840, 40},
	// Bar 2: quarter, quarter, half.
	{3840, 4800, 47}, {4800, 5760, 45}, {5760, 7680, 47},
	// Bar 3: E5 power chord, rest, quarter note.
	{7680, 9600, 40}, {7680, 9600, 47}, {7680, 9600, 52},
	{10560, 11520, 43},
	// Bar 4: whole-note low E.
	{11520, 15360, 40},
}

func TestImportFixtureRiff(t *testing.T) {
	s, warns, err := ImportFile("../../testdata/fixture_riff.mid")
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.Title != "Fixture Riff" {
		t.Errorf("Title = %q, want %q", s.Title, "Fixture Riff")
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(s.Tracks))
	}
	tr := s.Tracks[0]
	if tr.Name != "Guitar" || tr.Role != score.RoleUser || tr.Program != 25 {
		t.Errorf("track = %q role %d program %d, want Guitar role %d program 25", tr.Name, tr.Role, tr.Program, score.RoleUser)
	}
	if len(s.Tempos) != 1 || s.Tempos[0].Tick != 0 || s.Tempos[0].USPerQuarter != score.USPerQuarter(120) {
		t.Errorf("Tempos = %v, want single 120 BPM at tick 0", s.Tempos)
	}
	if len(s.Meters) != 1 || s.Meters[0] != (score.Meter{Tick: 0, Num: 4, Den: 4}) {
		t.Errorf("Meters = %v, want single 4/4 at tick 0", s.Meters)
	}

	evs := s.Events()
	if len(evs) != len(canonical) {
		t.Fatalf("got %d events, want %d", len(evs), len(canonical))
	}
	for i, want := range canonical {
		ev := evs[i]
		if ev.Start != want.start || ev.End != want.end || ev.Key != want.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, ev.Start, ev.End, ev.Key, want.start, want.end, want.key)
		}
	}

	// The bar-3 chord must carry the canonical inferred fingering:
	// strings 6/5/4 at frets 0/2/2.
	chord := map[int][2]int{40: {6, 0}, 47: {5, 2}, 52: {4, 2}}
	for _, ev := range evs {
		if ev.Start != 7680 {
			continue
		}
		want, ok := chord[ev.Key]
		if !ok {
			t.Errorf("unexpected chord key %d", ev.Key)
			continue
		}
		if ev.String != want[0] || ev.Fret != want[1] {
			t.Errorf("chord key %d = %d/%d, want %d/%d", ev.Key, ev.String, ev.Fret, want[0], want[1])
		}
	}

	// Every imported note is a heuristic fingering.
	for _, tr := range s.Tracks {
		for _, bar := range tr.Bars {
			for _, beat := range bar.Beats {
				for _, n := range beat.Notes {
					if !n.Inferred {
						t.Fatalf("note at tick %d on string %d is not marked Inferred", beat.Start, n.String)
					}
				}
			}
		}
	}
}

// buildSMF assembles an in-memory SMF for import tests. Each track is a
// sequence of (delta, message) pairs.
func buildSMF(t *testing.T, ppq uint16, tracks ...smf.Track) []byte {
	t.Helper()
	s := smf.NewSMF1()
	s.TimeFormat = smf.MetricTicks(ppq)
	for _, tr := range tracks {
		tr.Close(0)
		if err := s.Add(tr); err != nil {
			t.Fatalf("adding track: %v", err)
		}
	}
	var buf bytes.Buffer
	if _, err := s.WriteTo(&buf); err != nil {
		t.Fatalf("writing SMF: %v", err)
	}
	return buf.Bytes()
}

func TestImportDefaultsAndPPQConversion(t *testing.T) {
	// PPQ 480 file with no tempo or meter events: ticks double, maps get
	// the 120 BPM and 4/4 defaults.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 60, 100))
	tr.Add(480, midi.NoteOff(0, 60)) // one file quarter = 960 score ticks
	s, _, err := Import(buildSMF(t, 480, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(s.Tempos) != 1 || s.Tempos[0] != (score.Tempo{Tick: 0, USPerQuarter: score.USPerQuarter(120)}) {
		t.Errorf("Tempos = %v, want default 120 BPM", s.Tempos)
	}
	if len(s.Meters) != 1 || s.Meters[0] != (score.Meter{Tick: 0, Num: 4, Den: 4}) {
		t.Errorf("Meters = %v, want default 4/4", s.Meters)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != score.Quarter || evs[0].Key != 60 {
		t.Errorf("events = %v, want one quarter note key 60 at tick 0", evs)
	}
}

func TestImportQuantization(t *testing.T) {
	tests := []struct {
		name               string
		on, off            int64 // file ticks at PPQ 960
		wantStart, wantEnd int64
		wantWarn           bool
	}{
		{"snaps to grid", 100, 1030, 120, 1080, false},
		{"zero length stretched to a grid step", 100, 130, 120, 240, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tr smf.Track
			tr.Add(uint32(tt.on), midi.NoteOn(0, 60, 100))
			tr.Add(uint32(tt.off-tt.on), midi.NoteOff(0, 60))
			s, warns, err := Import(buildSMF(t, 960, tr))
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			evs := s.Events()
			if len(evs) != 1 || evs[0].Start != tt.wantStart || evs[0].End != tt.wantEnd {
				t.Errorf("events = %v, want one spanning [%d,%d)", evs, tt.wantStart, tt.wantEnd)
			}
			if got := len(warns) > 0; got != tt.wantWarn {
				t.Errorf("warnings = %v, wantWarn = %v", warns, tt.wantWarn)
			}
		})
	}
}

func TestImportPercussionSkipped(t *testing.T) {
	// Track 1 is all channel-10 percussion, track 2 a guitar line: the
	// guitar track must become RoleUser and the percussion track must be
	// skipped with a warning.
	var perc smf.Track
	perc.Add(0, midi.NoteOn(9, 36, 100))
	perc.Add(480, midi.NoteOff(9, 36))
	var gtr smf.Track
	gtr.Add(0, midi.NoteOn(0, 52, 100))
	gtr.Add(960, midi.NoteOff(0, 52))
	s, warns, err := Import(buildSMF(t, 960, perc, gtr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(s.Tracks) != 1 || s.Tracks[0].Role != score.RoleUser {
		t.Fatalf("got %d tracks, want 1 RoleUser track", len(s.Tracks))
	}
	if len(warns) != 1 {
		t.Errorf("warnings = %v, want one percussion warning", warns)
	}
}

func TestImportRoles(t *testing.T) {
	// First track with notes is the practice part, later ones backing.
	var a, b smf.Track
	a.Add(0, midi.NoteOn(0, 52, 100))
	a.Add(960, midi.NoteOff(0, 52))
	b.Add(0, midi.NoteOn(0, 45, 100))
	b.Add(960, midi.NoteOff(0, 45))
	s, _, err := Import(buildSMF(t, 960, a, b))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(s.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(s.Tracks))
	}
	if s.Tracks[0].Role != score.RoleUser || s.Tracks[1].Role != score.RoleBacking {
		t.Errorf("roles = %d,%d, want %d,%d", s.Tracks[0].Role, s.Tracks[1].Role, score.RoleUser, score.RoleBacking)
	}
}

func TestImportOctaveShift(t *testing.T) {
	// A1 (33) is below the guitar's low E (40): shifted up an octave to
	// A2 (45) with a warning.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 33, 100))
	tr.Add(960, midi.NoteOff(0, 33))
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 45 {
		t.Fatalf("events = %v, want one note key 45", evs)
	}
	if len(warns) != 1 {
		t.Errorf("warnings = %v, want one octave-shift warning", warns)
	}
}

func TestImportUnplayableDropped(t *testing.T) {
	// Key 20 shifts up an octave to 32, still below low E: fretting
	// reports it and the note is dropped, alongside a playable note that
	// must survive.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 20, 100))
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, midi.NoteOff(0, 20))
	tr.Add(0, midi.NoteOff(0, 52))
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 52 {
		t.Fatalf("events = %v, want only the playable key 52", evs)
	}
	if len(warns) != 2 { // octave shift + dropped unplayable
		t.Errorf("warnings = %v, want octave-shift and dropped-note warnings", warns)
	}
}

func TestImportBarlineTie(t *testing.T) {
	// A note sounding across a barline becomes tied beats, and Events()
	// merges them back into one event: the round-trip invariant.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 47, 100))
	tr.Add(7680, midi.NoteOff(0, 47)) // two 4/4 bars at PPQ 960
	s, _, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	trk := s.Tracks[0]
	if len(trk.Bars) != 2 {
		t.Fatalf("got %d bars, want 2", len(trk.Bars))
	}
	first := trk.Bars[0].Beats[0].Notes[0]
	second := trk.Bars[1].Beats[0].Notes[0]
	if first.Tied || !second.Tied {
		t.Errorf("tie flags = %v,%v, want false,true", first.Tied, second.Tied)
	}
	if first.String != second.String || first.Fret != second.Fret {
		t.Errorf("fingering differs across the tie: %d/%d vs %d/%d", first.String, first.Fret, second.String, second.Fret)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Start != 0 || evs[0].End != 7680 {
		t.Errorf("events = %v, want one merged event [0,7680)", evs)
	}
}

func TestImportRestsFillBars(t *testing.T) {
	// A lone quarter note leaves the rest of the bar as a rest beat and
	// the result still validates.
	var tr smf.Track
	tr.Add(960, midi.NoteOn(0, 45, 100))
	tr.Add(960, midi.NoteOff(0, 45))
	s, _, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	bar := s.Tracks[0].Bars[0]
	if len(bar.Beats) != 3 {
		t.Fatalf("got %d beats, want 3 (rest, note, rest)", len(bar.Beats))
	}
	if len(bar.Beats[0].Notes) != 0 || len(bar.Beats[1].Notes) != 1 || len(bar.Beats[2].Notes) != 0 {
		t.Errorf("beat notes = %d,%d,%d, want 0,1,0",
			len(bar.Beats[0].Notes), len(bar.Beats[1].Notes), len(bar.Beats[2].Notes))
	}
}

func TestImportNoNotes(t *testing.T) {
	var tr smf.Track
	tr.Add(0, smf.MetaTempo(90))
	if _, _, err := Import(buildSMF(t, 960, tr)); err == nil {
		t.Error("Import accepted a file with no notes")
	}
}

func TestImportGarbage(t *testing.T) {
	if _, _, err := Import([]byte("not a midi file")); err == nil {
		t.Error("Import accepted garbage")
	}
}
