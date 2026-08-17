package integration

import (
	"testing"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/S95F/guitarTutor/internal/synth"
)

// The whole articulation chain on a real file: the parser puts a technique
// letter on a note, Events carries it through the flattening, the engine
// recognises it as continuing the string that is already ringing, and the
// voice is asked to slide rather than to pick. Each link has its own test;
// this is the one that fails if they stop agreeing about which note a
// technique belongs to.

// specVoice records the NoteSpecs it is asked to sound and renders silence.
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

// TestFixtureTechniquesReachTheVoice runs the rich fixture — whose second
// bar is deliberately every technique letter in a row on one string — and
// checks what the voice was actually asked to play.
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
	for i := 0; i < 200; i++ { // well past the end of the piece
		eng.RenderFrames(l, r)
	}

	// The lead track is the one carrying the techniques.
	lead := voices[0]
	if len(lead.specs) == 0 {
		t.Fatal("the lead voice was asked to play nothing")
	}

	// Bar 2 is "2.4.8h 4.4.8p 5.4.8s 7.4.8b 7.4.4v 9.4x". The pull-off and
	// the slide each follow a note on string 4 and must arrive as
	// continuations of it. The hammer-on that opens the bar must NOT:
	// the note before it is the last of bar 1, which is on string 5, and
	// a hand cannot hammer onto a string it was not already holding down.
	// That distinction is the whole reason the engine matches on string
	// and not merely on "the previous note", and it is worth pinning on a
	// real file rather than a fixture built to make it true.
	//
	// The bend and the vibrato are not continuations either: vibrato is an
	// oscillation about a note that is still struck normally, and bends
	// are not carried into the synthesis at all (the model has no bend
	// amount to carry).
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
	// The hammer-on opening bar 2, specifically: a pluck, because the note
	// before it is on another string.
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

	// Every note the voice was given is still released exactly once: a
	// continuation inherits its predecessor's entry rather than adding a
	// second one, so releases must equal note-ons and not exceed them.
	if len(lead.offs) > len(lead.specs) {
		t.Errorf("%d releases for %d note-ons: a note was released twice", len(lead.offs), len(lead.specs))
	}
}

// TestFixtureTechniquesAreOnTheDestination pins the convention the whole
// chain depends on: a technique letter marks the note it arrives AT, so the
// note it continues from is the one before it on the same string. Reading
// it the other way round would slide from the wrong note and still look
// plausible in every unit test above.
func TestFixtureTechniquesAreOnTheDestination(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_rich.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	evs := sc.Tracks[0].Bars[1].Beats // bar 2, the technique bar
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
