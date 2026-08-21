package midiimport

import (
	"strings"
	"testing"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// TestImportWindTrack: a channel whose program change is 64 (soprano sax)
// imports as a wind track — the wind header with no strings and no capo,
// every note on the single lane at Fret = key − LowSounding, and none of
// them Inferred, since the lane is arithmetic rather than a heuristic
// guess. Key 92 sits above the standard Span (56+32 = 88): altissimo is
// real and must import as written, not be clamped or dropped.
func TestImportWindTrack(t *testing.T) {
	var tr smf.Track
	tr.Add(0, smf.MetaTrackSequenceName("Sax"))
	tr.Add(0, midi.ProgramChange(0, 64))
	for _, key := range []uint8{56, 68, 92} {
		tr.Add(0, midi.NoteOn(0, key, 100))
		tr.Add(960, midi.NoteOff(0, key))
	}
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if len(s.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(s.Tracks))
	}
	trk := s.Tracks[0]
	if trk.Wind == nil || trk.Wind.Name != "soprano sax" {
		t.Fatalf("Wind = %v, want the soprano sax", trk.Wind)
	}
	if trk.Tuning != nil || trk.Capo != 0 {
		t.Errorf("Tuning = %v Capo = %d, want no strings and no capo on a wind track", trk.Tuning, trk.Capo)
	}
	if trk.Name != "Sax" || trk.Program != 64 || trk.Role != score.RoleUser {
		t.Errorf("track = %q program %d role %d, want Sax program 64 role %d", trk.Name, trk.Program, trk.Role, score.RoleUser)
	}
	evs := s.Events()
	if len(evs) != 3 {
		t.Fatalf("got %d events, want 3", len(evs))
	}
	for i, key := range []int{56, 68, 92} {
		ev := evs[i]
		if ev.Key != key || ev.String != 1 || ev.Fret != key-56 {
			t.Errorf("event %d = key %d string %d fret %d, want key %d on string 1 fret %d",
				i, ev.Key, ev.String, ev.Fret, key, key-56)
		}
	}
	for _, bar := range trk.Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if n.Inferred {
					t.Fatalf("note at tick %d is marked Inferred; a wind lane is not a guess", beat.Start)
				}
			}
		}
	}
}

// TestImportWindChordKeepsHighest: a same-tick chord cannot sound on a
// monophonic instrument, so the highest key survives — melody on top, the
// convention arrangers write by — and the drops are counted into one
// aggregate warning instead of one warning per note.
func TestImportWindChordKeepsHighest(t *testing.T) {
	var tr smf.Track
	tr.Add(0, midi.ProgramChange(0, 64))
	tr.Add(0, midi.NoteOn(0, 60, 100))
	tr.Add(0, midi.NoteOn(0, 64, 100))
	tr.Add(0, midi.NoteOn(0, 67, 100))
	tr.Add(960, midi.NoteOff(0, 60))
	tr.Add(0, midi.NoteOff(0, 64))
	tr.Add(0, midi.NoteOff(0, 67))
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 67 || evs[0].String != 1 || evs[0].Fret != 11 {
		t.Fatalf("events = %v, want only the chord's highest note (key 67) on the lane", evs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "dropped 2 chord note(s), keeping the highest") {
		t.Errorf("warnings = %v, want one aggregate chord warning counting 2 drops", warns)
	}
}

// TestImportWindOctaveShift: D3 (key 50) sits below the soprano sax's
// lowest sounding note, A♭3 (56), and is rescued up an octave to D4 (62)
// exactly as a guitar part's low notes are; E2 (40) is still below the
// horn after one shift (52 < 56) and is dropped, each with its warning.
func TestImportWindOctaveShift(t *testing.T) {
	var tr smf.Track
	tr.Add(0, midi.ProgramChange(0, 64))
	tr.Add(0, midi.NoteOn(0, 50, 100))
	tr.Add(960, midi.NoteOff(0, 50))
	tr.Add(0, midi.NoteOn(0, 40, 100))
	tr.Add(960, midi.NoteOff(0, 40))
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 62 || evs[0].String != 1 || evs[0].Fret != 6 {
		t.Fatalf("events = %v, want only the rescued D4 (key 62, fret 6)", evs)
	}
	if len(warns) != 2 {
		t.Fatalf("warnings = %v, want octave-shift and dropped-note warnings", warns)
	}
	if !hasWarn(warns, "shifted 2 note(s) below the instrument's range up an octave") {
		t.Errorf("warnings = %v, want the octave-shift warning counting both notes", warns)
	}
	if !hasWarn(warns, "dropped unplayable note (key 52) at tick 960: below the soprano sax's lowest note") {
		t.Errorf("warnings = %v, want the still-below note dropped with the horn named", warns)
	}
}

// windBandTrack is one MIDI track holding a guitar on channel 1 (program
// 25) and a soprano sax on channel 2 (program 64), the way a type-0 file
// interleaves a band. Each channel plays two quarter notes.
func windBandTrack() smf.Track {
	var tr smf.Track
	tr.Add(0, smf.MetaTrackSequenceName("Band"))
	tr.Add(0, midi.ProgramChange(0, 25))
	tr.Add(0, midi.ProgramChange(1, 64))
	tr.Add(0, midi.NoteOn(0, 64, 100))
	tr.Add(0, midi.NoteOn(1, 70, 100))
	tr.Add(960, midi.NoteOff(0, 64))
	tr.Add(0, midi.NoteOff(1, 70))
	tr.Add(0, midi.NoteOn(0, 62, 100))
	tr.Add(0, midi.NoteOn(1, 68, 100))
	tr.Add(960, midi.NoteOff(0, 62))
	tr.Add(0, midi.NoteOff(1, 68))
	return tr
}

// TestImportBandWithWind: a track carrying a guitar channel and a sax
// channel splits into one part per channel, and each part keeps its own
// family — the guitar its standard tuning and Inferred fingerings, the sax
// its wind lane — with the mixed score passing Validate whole.
func TestImportBandWithWind(t *testing.T) {
	s, warns, err := Import(buildSMF(t, 960, windBandTrack()))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(s.Tracks) != 2 {
		t.Fatalf("got %d tracks, want one per channel", len(s.Tracks))
	}
	want := []struct {
		name    string
		program int
		role    score.TrackRole
		wind    string // wind instrument name, "" for a fretted part
		events  [][3]int64
	}{
		{"Band (channel 1)", 25, score.RoleUser, "", [][3]int64{{0, 960, 64}, {960, 1920, 62}}},
		{"Band (channel 2)", 64, score.RoleBacking, "soprano sax", [][3]int64{{0, 960, 70}, {960, 1920, 68}}},
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
		if w.wind == "" {
			if tr.Wind != nil || !tr.Tuning.Equal(score.StandardTuning) {
				t.Errorf("track %d = wind %v tuning %v, want a standard-tuning guitar part", i, tr.Wind, tr.Tuning)
			}
		} else {
			if tr.Wind == nil || tr.Wind.Name != w.wind {
				t.Errorf("track %d wind = %v, want the %s", i, tr.Wind, w.wind)
			}
			if tr.Tuning != nil || tr.Capo != 0 {
				t.Errorf("track %d tuning = %v capo = %d, want no strings and no capo", i, tr.Tuning, tr.Capo)
			}
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
	// The families' Inferred flags must not bleed into each other: every
	// guitar note is a heuristic guess, no sax note is.
	for bi, bar := range s.Tracks[0].Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if !n.Inferred {
					t.Errorf("guitar bar %d: note not marked Inferred", bi)
				}
			}
		}
	}
	for bi, bar := range s.Tracks[1].Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				if n.Inferred {
					t.Errorf("sax bar %d: note marked Inferred; the lane is arithmetic", bi)
				}
				if n.String != 1 {
					t.Errorf("sax bar %d: note on string %d, want the single lane", bi, n.String)
				}
			}
		}
	}
	if !hasWarn(warns, "split into one part per channel") {
		t.Errorf("warnings = %v, want the channel-split warning", warns)
	}
}

// TestImportWindWrittenCeiling: a transposing horn reads above what it
// sounds, and a sounding key whose written pitch would pass MIDI 127 has
// no note name the text format could ever save — it is dropped, loudly.
func TestImportWindWrittenCeiling(t *testing.T) {
	var tr smf.Track
	tr.Add(0, midi.ProgramChange(0, 67)) // baritone sax: written = sounding + 21
	tr.Add(0, midi.NoteOn(0, 110, 100))  // written 131
	tr.Add(960, midi.NoteOff(0, 110))
	tr.Add(0, midi.NoteOn(0, 60, 100)) // written 81: fine
	tr.Add(960, midi.NoteOff(0, 60))
	s, warns, err := Import(buildSMF(t, 960, tr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	track := s.Tracks[0]
	var keys []int
	for _, bar := range track.Bars {
		for _, beat := range bar.Beats {
			for _, n := range beat.Notes {
				keys = append(keys, track.Pitch(n))
			}
		}
	}
	if len(keys) != 1 || keys[0] != 60 {
		t.Fatalf("kept keys %v, want just 60", keys)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "written pitch") && strings.Contains(w, "past MIDI 127") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %v never explain the unwritable note", warns)
	}
	// The whole point: what the importer keeps, the format can save.
	if _, err := textfmt.Format(s); err != nil {
		t.Errorf("the imported piece cannot be written back: %v", err)
	}
}
