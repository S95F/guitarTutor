package engine

import (
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

type specRec struct {
	frame int64
	spec  synth.NoteSpec
}

type articVoice struct {
	stubVoice
	specs []specRec
}

func (v *articVoice) NoteOnSpec(spec synth.NoteSpec) {
	v.specs = append(v.specs, specRec{v.frame, spec})
	v.NoteOn(spec.Key, spec.Velocity)
}

func newArticFactory(reg *[]*articVoice) synth.Factory {
	return func(sampleRate, program int) synth.Voice {
		v := &articVoice{
			stubVoice: stubVoice{
				ons:     make([]noteRec, 0, 4096),
				offs:    make([]noteRec, 0, 4096),
				allOffs: make([]int64, 0, 256),
			},
			specs: make([]specRec, 0, 4096),
		}
		*reg = append(*reg, v)
		return v
	}
}

func techScore(t *testing.T, str, fret1, fret2 int, tech score.Technique) *score.Score {
	t.Helper()
	s := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "Guitar", Tuning: score.StandardTuning}
	s.Tracks = []*score.Track{tr}
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: str, Fret: fret1})
	b.AddBeat(score.Quarter, score.Note{String: str, Fret: fret2, Tech: tech})
	b.AddBeat(score.Half, score.Note{String: str, Fret: fret1})
	if err := s.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	return s
}

func renderBar(e *Engine) {
	l, r := make([]float32, 512), make([]float32, 512)
	e.Play()
	for i := 0; i < 250; i++ {
		e.RenderFrames(l, r)
	}
}

func TestTechniqueBecomesAContinuation(t *testing.T) {
	tests := []struct {
		name   string
		tech   score.Technique
		attack synth.Attack
	}{
		{"slide", score.TechSlide, synth.AttackSlide},
		{"hammer-on", score.TechHammer, synth.AttackLegato},
		{"pull-off", score.TechPull, synth.AttackLegato},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reg []*articVoice
			e := New(techScore(t, 6, 0, 3, tt.tech), Options{Voices: newArticFactory(&reg)})
			renderBar(e)
			v := reg[0]
			if len(v.specs) != 3 {
				t.Fatalf("got %d note-ons, want 3", len(v.specs))
			}

			if got := v.specs[0].spec.Attack; got != synth.AttackPluck {
				t.Errorf("first note attack = %v, want a pluck", got)
			}
			second := v.specs[1].spec
			if second.Attack != tt.attack {
				t.Errorf("second note attack = %v, want %v", second.Attack, tt.attack)
			}
			if want := v.specs[0].spec.Key; second.From != want {
				t.Errorf("second note continues from key %d, want the ringing %d", second.From, want)
			}
			if got := v.specs[2].spec.Attack; got != synth.AttackPluck {
				t.Errorf("third note attack = %v, want a pluck", got)
			}
		})
	}
}

func TestContinuationSuppressesTheRelease(t *testing.T) {
	var reg []*articVoice
	e := New(techScore(t, 6, 0, 3, score.TechSlide), Options{Voices: newArticFactory(&reg)})
	renderBar(e)
	v := reg[0]

	if len(v.offs) != 2 {
		t.Fatalf("got %d note-offs for 3 notes, want 2 (the slide inherits the first)", len(v.offs))
	}
	for _, off := range v.offs {
		if off.key == 40 && off.frame == v.specs[1].frame {
			t.Error("the origin note was released on the very frame it was slid away from")
		}
	}

	if got := v.offs[0].key; got != 43 {
		t.Errorf("first note-off names key %d, want the slide destination 43", got)
	}
}

func TestArticulationLeavesTheScheduleAlone(t *testing.T) {
	sc := techScore(t, 6, 0, 3, score.TechSlide)

	tapped := func(f synth.Factory) []noteRec {
		e := New(sc, Options{Voices: f})
		var got []noteRec
		e.SetEventTap(func(ev score.NoteEvent, outFrame int64) {
			got = append(got, noteRec{outFrame, ev.Key})
		})
		renderBar(e)
		return got
	}

	var artic []*articVoice
	var plain []*stubVoice
	withSpec := tapped(newArticFactory(&artic))
	without := tapped(newStubFactory(&plain))

	if len(withSpec) == 0 {
		t.Fatal("no events were tapped")
	}
	if len(withSpec) != len(without) {
		t.Fatalf("articulating tapped %d events, plain tapped %d", len(withSpec), len(without))
	}
	for i := range withSpec {
		if withSpec[i] != without[i] {
			t.Errorf("event %d: articulating tapped %+v, plain tapped %+v", i, withSpec[i], without[i])
		}
	}
}

func TestPlainVoiceStillSoundsEveryNote(t *testing.T) {
	var reg []*stubVoice
	e := New(techScore(t, 6, 0, 3, score.TechSlide), Options{Voices: newStubFactory(&reg)})
	renderBar(e)
	v := reg[0]
	if len(v.ons) != 3 {
		t.Fatalf("got %d note-ons, want all 3", len(v.ons))
	}
	if len(v.offs) != 3 {
		t.Errorf("got %d note-offs, want 3: a voice that cannot articulate keeps the old one-off-per-note pairing", len(v.offs))
	}
}

func TestContinuationNeedsTheSameString(t *testing.T) {
	s := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "Guitar", Tuning: score.StandardTuning}
	s.Tracks = []*score.Track{tr}
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 5, Fret: 2})
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3, Tech: score.TechSlide})
	b.AddBeat(score.Half, score.Note{String: 6, Fret: 0})
	if err := s.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}

	var reg []*articVoice
	e := New(s, Options{Voices: newArticFactory(&reg)})
	renderBar(e)
	v := reg[0]
	if len(v.specs) < 2 {
		t.Fatalf("got %d note-ons, want at least 2", len(v.specs))
	}
	if got := v.specs[1].spec.Attack; got != synth.AttackPluck {
		t.Errorf("a slide on string 6 continued a note on string 5 (attack %v); want a pluck", got)
	}
}

func TestVibratoReachesTheVoice(t *testing.T) {
	var reg []*articVoice
	e := New(techScore(t, 6, 0, 3, score.TechVibrato), Options{Voices: newArticFactory(&reg)})
	renderBar(e)
	v := reg[0]
	if len(v.specs) < 2 {
		t.Fatalf("got %d note-ons, want at least 2", len(v.specs))
	}
	if !v.specs[1].spec.Vibrato {
		t.Error("the vibrato note reached the voice without it")
	}
	if got := v.specs[1].spec.Attack; got != synth.AttackPluck {
		t.Errorf("vibrato turned the note into a %v; it is not a continuation", got)
	}
}
