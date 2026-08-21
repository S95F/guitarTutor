package score

import "testing"

func TestWindRegistrySane(t *testing.T) {
	names := map[string]bool{}
	programs := map[int]bool{}
	for _, w := range WindInstruments {
		if names[w.Name] {
			t.Errorf("%s: duplicate name", w.Name)
		}
		names[w.Name] = true
		if programs[w.Program] {
			t.Errorf("%s: program %d already claimed", w.Name, w.Program)
		}
		programs[w.Program] = true
		if w.Span <= 0 {
			t.Errorf("%s: span %d", w.Name, w.Span)
		}
		if w.LowSounding < 0 || w.LowSounding+w.Span > 127 {
			t.Errorf("%s: sounding range %d-%d leaves MIDI", w.Name, w.LowSounding, w.LowSounding+w.Span)
		}
		if lw := w.Written(w.LowSounding); lw < 0 || w.Written(w.LowSounding+w.Span) > 127 {
			t.Errorf("%s: written range %d-%d leaves MIDI", w.Name, lw, w.Written(w.LowSounding+w.Span))
		}
		if w.Program < 0 || w.Program > 127 {
			t.Errorf("%s: program %d outside 0-127", w.Name, w.Program)
		}
	}
}

func TestSopranoSaxNumbers(t *testing.T) {
	w := WindByName("soprano sax")
	if w == nil {
		t.Fatal("no soprano sax in the registry")
	}
	if w.LowSounding != 56 {
		t.Errorf("lowest sounding = %d, want 56 (Ab3)", w.LowSounding)
	}
	if got := w.Written(w.LowSounding); got != 58 {
		t.Errorf("lowest written = %d, want 58 (Bb3)", got)
	}
	if got := w.Written(w.LowSounding + w.Span); got != 90 {
		t.Errorf("highest standard written = %d, want 90 (F#6)", got)
	}
	if w.Program != 64 {
		t.Errorf("program = %d, want 64 (GM soprano sax)", w.Program)
	}
}

func TestWindLookups(t *testing.T) {
	if WindByName("Soprano Sax") == nil || WindByName("  soprano sax ") == nil {
		t.Error("WindByName is not case- and space-forgiving")
	}
	if WindByName("theremin") != nil {
		t.Error("WindByName invented an instrument")
	}
	if w := WindByProgram(64); w == nil || w.Name != "soprano sax" {
		t.Errorf("WindByProgram(64) = %v, want soprano sax", w)
	}
	if WindByProgram(25) != nil {
		t.Error("WindByProgram(25) found a wind instrument in a steel-string guitar")
	}
}

func TestWindPitchDerivation(t *testing.T) {
	tr := &Track{Wind: WindByName("soprano sax")}
	if p := tr.Pitch(Note{String: 1, Fret: 0}); p != 56 {
		t.Errorf("offset 0 = %d, want 56", p)
	}

	if p := tr.Pitch(Note{String: 1, Fret: 16}); p != 72 {
		t.Errorf("offset 16 = %d, want 72 (C5)", p)
	}
}

func windScore() *Score {
	s := &Score{
		Tempos: TempoMap{{Tick: 0, USPerQuarter: USPerQuarter(120)}},
		Meters: MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &Track{Name: "Sax", Wind: WindByName("soprano sax"), Program: 64}
	s.Tracks = []*Track{tr}
	b := tr.AppendBar(4, 4)
	for i := 0; i < 4; i++ {
		b.AddBeat(Quarter, Note{String: 1, Fret: 2 + i})
	}
	return s
}

func TestWindValidate(t *testing.T) {
	if err := windScore().Validate(); err != nil {
		t.Fatalf("valid wind score fails: %v", err)
	}

	breakIt := []struct {
		name  string
		mutil func(s *Score)
	}{
		{"two notes in a beat", func(s *Score) {
			s.Tracks[0].Bars[0].Beats[0].Notes = []Note{{String: 1, Fret: 2}, {String: 1, Fret: 6}}
		}},
		{"string other than 1", func(s *Score) {
			s.Tracks[0].Bars[0].Beats[0].Notes[0].String = 2
		}},
		{"negative offset", func(s *Score) {
			s.Tracks[0].Bars[0].Beats[0].Notes[0].Fret = -1
		}},
		{"pull-off on a wind", func(s *Score) {
			s.Tracks[0].Bars[0].Beats[0].Notes[0].Tech = TechPull
		}},
		{"dead note on a wind", func(s *Score) {
			s.Tracks[0].Bars[0].Beats[0].Notes[0].Tech = TechDead
		}},
		{"stray tuning", func(s *Score) {
			s.Tracks[0].Tuning = StandardTuning
		}},
		{"stray capo", func(s *Score) {
			s.Tracks[0].Capo = 2
		}},
		{"sounding above MIDI", func(s *Score) {
			s.Tracks[0].Bars[0].Beats[0].Notes[0].Fret = 128
		}},
	}
	for _, tt := range breakIt {
		s := windScore()
		tt.mutil(s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: Validate accepted it", tt.name)
		}
	}

	s := windScore()
	s.Tracks[0].Bars[0].Beats[1].Notes[0].Tech = TechSlur | TechSlide | TechBend | TechVibrato
	if err := s.Validate(); err != nil {
		t.Errorf("slur/slide/bend/vibrato refused on a wind track: %v", err)
	}
}

func TestWindEvents(t *testing.T) {
	s := windScore()
	tr := s.Tracks[0]
	b := tr.AppendBar(4, 4)
	b.AddBeat(Half, Note{String: 1, Fret: 5})
	b.AddBeat(Half, Note{String: 1, Fret: 5, Tied: true})

	evs := s.Events()
	if len(evs) != 5 {
		t.Fatalf("got %d events, want 5 (the tie merges)", len(evs))
	}
	if evs[0].Key != 58 {
		t.Errorf("first key = %d, want 58", evs[0].Key)
	}
	last := evs[len(evs)-1]
	if last.Key != 61 || last.End-last.Start != Whole {
		t.Errorf("tied note: key %d dur %d, want 61 for a whole note", last.Key, last.End-last.Start)
	}
}
