package integration

import (
	"testing"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/synth"
)

type specVoice struct {
	specs []synth.NoteSpec
	offs  []int
}

func (v *specVoice) NoteOn(key int, velocity float64) {
	v.NoteOnSpec(synth.NoteSpec{Key: key, Velocity: velocity})
}
func (v *specVoice) NoteOnSpec(s synth.NoteSpec)  { v.specs = append(v.specs, s) }
func (v *specVoice) NoteOff(key int)              { v.offs = append(v.offs, key) }
func (v *specVoice) AllNotesOff()                 {}
func (v *specVoice) Render(left, right []float32) {}

var _ synth.Articulator = (*specVoice)(nil)

func TestFixtureTechniquesReachTheVoice(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_rich.gtab"))
	if err != nil {
		t.Fatal(err)
	}

	var voices []*specVoice
	const sr = 48000
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: func(int, int) synth.Voice {
		v := &specVoice{}
		voices = append(voices, v)
		return v
	}})
	eng.Play()
	l, r := make([]float32, 4096), make([]float32, 4096)
	for i := 0; i < 200; i++ {
		eng.RenderFrames(l, r)
	}

	lead := voices[0]
	if len(lead.specs) == 0 {
		t.Fatal("the lead voice was asked to play nothing")
	}

	var continuations, slides, vibratos int
	for i, s := range lead.specs {
		switch s.Attack {
		case synth.AttackSlide:
			slides++
			continuations++
		case synth.AttackLegato:
			continuations++
		}
		if s.Attack != synth.AttackPluck && s.From == 0 {
			t.Errorf("spec %d is a continuation of nothing: %+v", i, s)
		}
		if s.Vibrato {
			vibratos++
		}
	}
	if continuations != 2 {
		t.Errorf("got %d continuations, want 2 (the pull-off and the slide)", continuations)
	}
	if slides != 1 {
		t.Errorf("got %d slides, want 1", slides)
	}

	hammer := -1
	for i, s := range lead.specs {
		if s.Key == sc.Tracks[0].Pitch(sc.Tracks[0].Bars[1].Beats[0].Notes[0]) {
			hammer = i
			break
		}
	}
	if hammer < 0 {
		t.Fatal("the hammer-on that opens bar 2 never reached the voice")
	}
	if got := lead.specs[hammer].Attack; got != synth.AttackPluck {
		t.Errorf("the bar-2 hammer-on arrived as %v; there is nothing on its string to hammer from", got)
	}
	if vibratos != 1 {
		t.Errorf("got %d vibrato notes, want 1", vibratos)
	}

	if len(lead.offs) > len(lead.specs) {
		t.Errorf("%d releases for %d note-ons: a note was released twice", len(lead.offs), len(lead.specs))
	}
}

func TestFixtureTechniquesAreOnTheDestination(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_rich.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	evs := sc.Tracks[0].Bars[1].Beats
	want := []struct {
		fret int
		tech score.Technique
	}{
		{2, score.TechHammer}, {4, score.TechPull}, {5, score.TechSlide},
		{7, score.TechBend}, {7, score.TechVibrato}, {9, score.TechDead},
	}
	if len(evs) != len(want) {
		t.Fatalf("bar 2 has %d beats, want %d", len(evs), len(want))
	}
	for i, w := range want {
		n := evs[i].Notes[0]
		if n.Fret != w.fret || n.Tech&w.tech == 0 {
			t.Errorf("beat %d: fret %d tech %b, want fret %d carrying %b", i, n.Fret, n.Tech, w.fret, w.tech)
		}
	}
}
