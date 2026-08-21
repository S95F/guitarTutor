package midiimport

import (
	"strings"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func TestImportedLabelsAlwaysSave(t *testing.T) {
	var cond smf.Track
	cond.Add(0, smf.MetaTrackSequenceName("Song // Take 2"))
	var gtr smf.Track
	gtr.Add(0, smf.MetaTrackSequenceName("AC\nDC"))
	gtr.Add(0, midi.NoteOn(0, 52, 100))
	gtr.Add(960, midi.NoteOff(0, 52))
	s, warns, err := Import(buildSMF(t, 960, cond, gtr))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := s.Title; got != "Song / / Take 2" {
		t.Errorf("Title = %q, want the comment marker broken", got)
	}
	if got := s.Tracks[0].Name; got != "AC DC" {
		t.Errorf("Name = %q, want the line break replaced", got)
	}
	found := 0
	for _, w := range warns {
		if strings.Contains(w, "cannot") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("warnings = %v, want one per cleaned label", warns)
	}
	src, err := textfmt.Format(s)
	if err != nil {
		t.Fatalf("Format: %v, want the imported piece to save", err)
	}
	back, err := textfmt.Parse(src, "back")
	if err != nil {
		t.Fatalf("Parse of the saved piece: %v", err)
	}
	if back.Title != s.Title || back.Tracks[0].Name != s.Tracks[0].Name {
		t.Errorf("round trip changed labels: %q/%q -> %q/%q", s.Title, s.Tracks[0].Name, back.Title, back.Tracks[0].Name)
	}
}

func TestHostileMetaLengthRejected(t *testing.T) {

	data := []byte("MThd\x00\x00\x00\x06\x00\x00\x00\x01\x01\xe0" +
		"MTrk\x00\x00\x00\x0a\x00\xff\x01\xff\xff\xff\x7f\x00\xff\x2f\x00")
	done := make(chan struct{})
	var err error
	go func() {
		_, _, err = Import(data)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Import did not return within 5s: the hostile meta length was not bounded")
	}
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want a rejection naming the oversized event length", err)
	}
}

func TestValidFileWithMetaTextStillImports(t *testing.T) {
	var tr smf.Track
	tr.Add(0, smf.MetaTrackSequenceName("A perfectly ordinary track name"))
	tr.Add(0, smf.MetaText("some copyright-ish text that is a real meta event"))
	tr.Add(0, midi.NoteOn(0, 52, 100))
	tr.Add(960, midi.NoteOff(0, 52))
	s, _, err := Import(buildSMF(t, 480, tr))
	if err != nil {
		t.Fatalf("Import: %v, want the ordinary meta-text file to import", err)
	}
	if evs := s.Events(); len(evs) != 1 {
		t.Fatalf("events = %+v, want the one note", evs)
	}
}
