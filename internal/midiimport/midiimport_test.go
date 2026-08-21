package midiimport

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/S95F/musicTutor/internal/score"
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

// buildSMF assembles an in-memory SMF for import tests and benchmarks.
// Each track is a sequence of (delta, message) pairs.
func buildSMF(t testing.TB, ppq uint16, tracks ...smf.Track) []byte {
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

func TestImportProgramChangeChannels(t *testing.T) {
	t.Run("percussion channel ignored", func(t *testing.T) {
		// A program change on channel 10 (wire channel 9) belongs to
		// the percussion kit, not the melodic notes: the track must
		// keep the default program.
		var tr smf.Track
		tr.Add(0, midi.ProgramChange(9, 40))
		tr.Add(0, midi.NoteOn(0, 52, 100))
		tr.Add(960, midi.NoteOff(0, 52))
		s, _, err := Import(buildSMF(t, 960, tr))
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if s.Tracks[0].Program != DefaultProgram {
			t.Errorf("Program = %d, want default %d", s.Tracks[0].Program, DefaultProgram)
		}
	})
	t.Run("melodic channel applied", func(t *testing.T) {
		var tr smf.Track
		tr.Add(0, midi.ProgramChange(0, 30))
		tr.Add(0, midi.NoteOn(0, 52, 100))
		tr.Add(960, midi.NoteOff(0, 52))
		s, _, err := Import(buildSMF(t, 960, tr))
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if s.Tracks[0].Program != 30 {
			t.Errorf("Program = %d, want 30", s.Tracks[0].Program)
		}
	})
}

func TestImportMidBarMeterRebasedToBarline(t *testing.T) {
	// A meter change at tick 960, inside the first 4/4 bar, is applied
	// to the bar structure at the next barline (3840). The stored meter
	// map must be rewritten to that tick so bars and Meters agree.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, smf.MetaMeter(3, 4))
	tr.Add(6720, midi.NoteOff(0, 52)) // note spans [0, 7680)
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	want := score.MeterMap{{Tick: 0, Num: 4, Den: 4}, {Tick: 3840, Num: 3, Den: 4}}
	if len(s.Meters) != len(want) {
		t.Fatalf("Meters = %v, want %v", s.Meters, want)
	}
	for i, m := range want {
		if s.Meters[i] != m {
			t.Errorf("Meters[%d] = %v, want %v", i, s.Meters[i], m)
		}
	}
	starts := map[int64]bool{}
	for _, bar := range s.Tracks[0].Bars {
		starts[bar.Start] = true
	}
	for _, m := range s.Meters {
		if !starts[m.Tick] {
			t.Errorf("meter at tick %d is not a bar start", m.Tick)
		}
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "not on a barline") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a mid-bar meter warning", warns)
	}
}

func TestImportMidBarMeterCollisionLastWins(t *testing.T) {
	// A mid-bar 3/4 at tick 960 shifts to the barline at 3840, where an
	// on-barline 6/8 already sits: the later change (the one the bar
	// structure actually used) must win.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, smf.MetaMeter(3, 4))
	tr.Add(2880, smf.MetaMeter(6, 8)) // tick 3840, on the barline
	tr.Add(3840, midi.NoteOff(0, 52)) // note spans [0, 7680)
	s, _, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	want := score.MeterMap{{Tick: 0, Num: 4, Den: 4}, {Tick: 3840, Num: 6, Den: 8}}
	if len(s.Meters) != len(want) {
		t.Fatalf("Meters = %v, want %v", s.Meters, want)
	}
	for i, m := range want {
		if s.Meters[i] != m {
			t.Errorf("Meters[%d] = %v, want %v", i, s.Meters[i], m)
		}
	}
	if got := s.Tracks[0].Bars[1]; got.Num != 6 || got.Den != 8 {
		t.Errorf("bar 1 meter = %d/%d, want 6/8", got.Num, got.Den)
	}
}

func TestImportAllNotesUnplayable(t *testing.T) {
	// Key 20 shifts up an octave to 32, still below the low E (40), so
	// fretting drops it. With every note dropped, Import must fail the
	// same way an empty file does, keeping the explanatory warnings.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 20, 100))
	tr.Add(960, midi.NoteOff(0, 20))
	_, warns, err := Import(buildSMF(t, 960, tr))
	if err == nil || !strings.Contains(err.Error(), "no playable notes") {
		t.Fatalf("err = %v, want 'no playable notes in file'", err)
	}
	if len(warns) == 0 {
		t.Error("want warnings explaining the dropped notes, got none")
	}
}

// BenchmarkImportDense imports a long dense line — about 50k sequential
// 16th notes — the shape that made buildTrack's per-bar and per-segment
// full-note-list scans dominate import time before they were replaced
// with bucketed bar edges and an advancing active-note cursor.
func BenchmarkImportDense(b *testing.B) {
	const notes = 50_000
	keys := []uint8{40, 45, 50, 55, 59, 64}
	var tr smf.Track
	for i := 0; i < notes; i++ {
		k := keys[i%len(keys)]
		tr.Add(0, midi.NoteOn(0, k, 100))
		tr.Add(240, midi.NoteOff(0, k)) // one 16th at PPQ 960
	}
	data := buildSMF(b, 960, tr)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := Import(data); err != nil {
			b.Fatalf("Import: %v", err)
		}
	}
}

func TestImportZeroTempoSkipped(t *testing.T) {
	// A tempo meta event encoding 0 microseconds per quarter (which
	// gomidi decodes as +Inf BPM) must be skipped with a warning, with
	// the tick-0 default supplying 120 BPM.
	var tr smf.Track
	tr.Add(0, smf.MetaUndefined(0x51, []byte{0, 0, 0})) // raw tempo meta: 0 us per quarter
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, midi.NoteOff(0, 52))
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(s.Tempos) != 1 || s.Tempos[0] != (score.Tempo{Tick: 0, USPerQuarter: score.USPerQuarter(120)}) {
		t.Errorf("Tempos = %v, want single default 120 BPM at tick 0", s.Tempos)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "tempo") {
		t.Errorf("warnings = %v, want one skipped-tempo warning", warns)
	}
}

func TestImportHostileExtentCapped(t *testing.T) {
	// At PPQ 1 a modest file delta rescales to an enormous score extent
	// (250,000 file ticks become 240 million score ticks), which would
	// otherwise lay out tens of thousands of bars. Import must refuse.
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(250_000, midi.NoteOff(0, 52))
	_, _, err := Import(buildSMF(t, 1, tr))
	if err == nil || !strings.Contains(err.Error(), "score too long") {
		t.Fatalf("err = %v, want a 'score too long' error", err)
	}
}

func TestImportTinyBarsCapped(t *testing.T) {
	// A 1/128 meter makes 30-tick bars, so a note run well inside the
	// tick limit still explodes into an absurd number of bars: the bar
	// cap must catch what the tick cap cannot.
	var tr smf.Track
	tr.Add(0, smf.MetaMeter(1, 128))
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(4_000_000, midi.NoteOff(0, 52))
	_, _, err := Import(buildSMF(t, 960, tr))
	if err == nil || !strings.Contains(err.Error(), "score too long: more than 100000 bars") {
		t.Fatalf("err = %v, want a 'score too long: more than 100000 bars' error", err)
	}
}

// TestImportZeroDenMeterSkipped: a time-signature meta whose denominator
// byte is 8 (2^8 overflows uint8 to 0) decodes as 4/0. Placed past the
// last note it governs no bar, so barSpecs' meter check never sees it —
// pre-fix it survived into s.Meters and panicked BeatLen with a divide
// by zero when the user sought to the end of the piece. It must instead
// be skipped at read time with a warning.
func TestImportZeroDenMeterSkipped(t *testing.T) {
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, midi.NoteOff(0, 52))
	tr.Add(0, smf.MetaUndefined(0x58, []byte{4, 8, 24, 8})) // raw meter meta: 4/(2^8) = 4/0
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "time signature") {
		t.Errorf("warnings = %v, want one skipped-time-signature warning", warns)
	}
	if len(s.Meters) != 1 || s.Meters[0] != (score.Meter{Tick: 0, Num: 4, Den: 4}) {
		t.Errorf("Meters = %v, want single default 4/4 at tick 0", s.Meters)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if got := s.Meters.At(s.End()).BeatLen(); got <= 0 { // panicked pre-fix
		t.Errorf("BeatLen at score end = %d, want > 0", got)
	}
}

// TestImportZeroNumMeterSkipped: same escape path as the zero
// denominator, via an unvalidated zero numerator byte (0/4).
func TestImportZeroNumMeterSkipped(t *testing.T) {
	var tr smf.Track
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, midi.NoteOff(0, 52))
	tr.Add(0, smf.MetaUndefined(0x58, []byte{0, 2, 24, 8})) // raw meter meta: 0/(2^2) = 0/4
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "time signature") {
		t.Errorf("warnings = %v, want one skipped-time-signature warning", warns)
	}
	if len(s.Meters) != 1 || s.Meters[0] != (score.Meter{Tick: 0, Num: 4, Den: 4}) {
		t.Errorf("Meters = %v, want single default 4/4 at tick 0", s.Meters)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if got := s.Meters.At(s.End()).BeatLen(); got <= 0 {
		t.Errorf("BeatLen at score end = %d, want > 0", got)
	}
}

// buildSMF0 assembles a genuine type-0 (single-track) SMF. gomidi writes
// the format field as 1 whenever it is handed a track, so it is patched
// back to 0 in the MThd chunk (bytes 8-9) and the result read back to
// confirm it — this is the shape a downloaded MIDI file actually has,
// with the whole band on one track and only the channel telling the
// instruments apart.
func buildSMF0(t testing.TB, ppq uint16, tr smf.Track) []byte {
	t.Helper()
	data := buildSMF(t, ppq, tr)
	binary.BigEndian.PutUint16(data[8:10], 0)
	sm, err := smf.ReadFrom(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("reading back the patched type-0 file: %v", err)
	}
	if sm.Format() != 0 {
		t.Fatalf("fixture format = %d, want a type-0 file", sm.Format())
	}
	if len(sm.Tracks) != 1 {
		t.Fatalf("fixture has %d tracks, want the single type-0 track", len(sm.Tracks))
	}
	return data
}

// bandTrack is one MIDI track holding three instruments the way a type-0
// file does: a guitar on channel 1, a bass on channel 2, and a kick drum
// on channel 10. Each melodic channel plays two quarter notes.
func bandTrack() smf.Track {
	var tr smf.Track
	tr.Add(0, smf.MetaTrackSequenceName("Band"))
	tr.Add(0, midi.ProgramChange(0, 26)) // guitar
	tr.Add(0, midi.ProgramChange(1, 33)) // bass
	tr.Add(0, midi.NoteOn(0, 64, 100))
	tr.Add(0, midi.NoteOn(1, 40, 100))
	tr.Add(0, midi.NoteOn(9, 36, 100)) // percussion, never a pitch
	tr.Add(960, midi.NoteOff(0, 64))
	tr.Add(0, midi.NoteOff(1, 40))
	tr.Add(0, midi.NoteOff(9, 36))
	tr.Add(0, midi.NoteOn(0, 62, 100))
	tr.Add(0, midi.NoteOn(1, 43, 100))
	tr.Add(960, midi.NoteOff(0, 62))
	tr.Add(0, midi.NoteOff(1, 43))
	return tr
}

// eventKeys collects one track's events as (start, end, key) triples.
func eventKeys(s *score.Score, track int) [][3]int64 {
	var out [][3]int64
	for _, ev := range s.Events() {
		if ev.Track == track {
			out = append(out, [3]int64{ev.Start, ev.End, int64(ev.Key)})
		}
	}
	return out
}

// assertBandSplit checks that the guitar and bass came out as separate
// parts carrying only their own notes and their own programs.
func assertBandSplit(t *testing.T, s *score.Score, warns []string) {
	t.Helper()
	if len(s.Tracks) != 2 {
		var names []string
		for _, tr := range s.Tracks {
			names = append(names, tr.Name)
		}
		t.Fatalf("got %d tracks %v, want one per melodic channel", len(s.Tracks), names)
	}
	want := []struct {
		name    string
		program int
		role    score.TrackRole
		events  [][3]int64
	}{
		{"Band (channel 1)", 26, score.RoleUser, [][3]int64{{0, 960, 64}, {960, 1920, 62}}},
		{"Band (channel 2)", 33, score.RoleBacking, [][3]int64{{0, 960, 40}, {960, 1920, 43}}},
	}
	for i, w := range want {
		tr := s.Tracks[i]
		if tr.Name != w.name {
			t.Errorf("track %d name = %q, want %q", i, tr.Name, w.name)
		}
		if tr.Program != w.program {
			t.Errorf("track %d program = %d, want %d (the program change on its own channel)", i, tr.Program, w.program)
		}
		if tr.Role != w.role {
			t.Errorf("track %d role = %d, want %d", i, tr.Role, w.role)
		}
		got := eventKeys(s, i)
		if len(got) != len(w.events) {
			t.Fatalf("track %d has %d events %v, want %d — channels are still merged", i, len(got), got, len(w.events))
		}
		for j, ev := range w.events {
			if got[j] != ev {
				t.Errorf("track %d event %d = %v, want %v", i, j, got[j], ev)
			}
		}
	}
	if !hasWarn(warns, "split into one part per channel") {
		t.Errorf("warnings = %v, want one explaining the channel split", warns)
	}
	if !hasWarn(warns, "percussion") {
		t.Errorf("warnings = %v, want the channel-10 percussion warning", warns)
	}
}

func hasWarn(warns []string, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestImportType0SplitsByChannel (audit D1): a type-0 file separates its
// instruments by channel alone. Merged into a single part, the guitar,
// the bass and the drums are handed to the fretting heuristic at once,
// which crams what it can onto six strings and drops the rest — so the
// practice part is none of the three instruments, and its program is
// whichever instrument happened to send the first program change. Each
// channel must instead become its own selectable part.
func TestImportType0SplitsByChannel(t *testing.T) {
	s, warns, err := Import(buildSMF0(t, 960, bandTrack()))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	assertBandSplit(t, s, warns)
}

// TestImportMultiChannelTrackSplit (audit D1): the split keys off the
// channels a track actually uses, not off the file's format byte — a
// type-1 track carrying several channels is carrying several instruments
// for exactly the same reason, and writers get the format byte wrong
// often enough that it is not worth trusting.
func TestImportMultiChannelTrackSplit(t *testing.T) {
	s, warns, err := Import(buildSMF(t, 960, bandTrack()))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	assertBandSplit(t, s, warns)
}

// TestImportSingleChannelNotSplit: the ordinary one-instrument-per-track
// file must be untouched by the split — one part, its plain track name,
// and nothing said about channels.
func TestImportSingleChannelNotSplit(t *testing.T) {
	var tr smf.Track
	tr.Add(0, smf.MetaTrackSequenceName("Guitar"))
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, midi.NoteOff(0, 52))
	tr.Add(0, midi.NoteOn(0, 55, 100))
	tr.Add(960, midi.NoteOff(0, 55))
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(s.Tracks))
	}
	if s.Tracks[0].Name != "Guitar" {
		t.Errorf("track name = %q, want the file's own %q, unqualified", s.Tracks[0].Name, "Guitar")
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
}

// TestImportPerChannelProgram: a program change belongs to its channel.
// Applying the first one seen to the whole track labelled every split
// part with the first instrument in the file.
func TestImportPerChannelProgram(t *testing.T) {
	var tr smf.Track
	tr.Add(0, midi.ProgramChange(1, 33)) // bass, on channel 2 only
	tr.Add(0, midi.NoteOn(0, 52, 100))   // channel 1 has no program change
	tr.Add(960, midi.NoteOff(0, 52))
	tr.Add(0, midi.NoteOn(1, 40, 100))
	tr.Add(960, midi.NoteOff(1, 40))
	s, _, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(s.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(s.Tracks))
	}
	if s.Tracks[0].Program != DefaultProgram {
		t.Errorf("channel 1 program = %d, want the default %d — channel 2's program change is not its own",
			s.Tracks[0].Program, DefaultProgram)
	}
	if s.Tracks[1].Program != 33 {
		t.Errorf("channel 2 program = %d, want 33", s.Tracks[1].Program)
	}
}

// TestUserTrackIsNotAnEmptyChannel is the fix-verification regression for
// the channel split. RoleUser used to go to whichever part came first,
// decided before the tracks were built. Once a multi-channel track splits
// per channel that is no longer safe: a whole channel can sit outside
// guitar range — piccolo, bells, a synth lead two octaves up — so every
// one of its notes is dropped as unfrettable and the part that opens for
// practice is blank, with nothing to play and no explanation.
func TestUserTrackIsNotAnEmptyChannel(t *testing.T) {
	// Channel 1 carries only notes far above the highest fret; channel 3
	// carries an ordinary guitar line.
	var tr smf.Track
	tr.Add(0, smf.MetaTrackSequenceName("Band"))
	for i := 0; i < 4; i++ {
		tr.Add(0, midi.NoteOn(0, 120, 100)) // channel 1, key 120: unplayable
		tr.Add(0, midi.NoteOn(2, 52, 100))  // channel 3, key 52: fine
		tr.Add(960, midi.NoteOff(0, 120))
		tr.Add(0, midi.NoteOff(2, 52))
	}

	sm := smf.New()
	sm.Add(tr)
	var buf bytes.Buffer
	if _, err := sm.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	s, warns, err := Import(buf.Bytes())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	var user *score.Track
	for _, tr := range s.Tracks {
		if tr.Role == score.RoleUser {
			user = tr
			break
		}
	}
	if user == nil {
		t.Fatal("no RoleUser track at all")
	}
	if !trackHasNotes(user) {
		t.Errorf("the practice track %q is empty; the player opens a blank tab (tracks %d, warnings %v)",
			user.Name, len(s.Tracks), warns)
	}
}
