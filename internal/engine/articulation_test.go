package engine

import (
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

// The engine's half of articulation: which notes are handed to a voice as
// continuations of a ringing string, and — the part that matters most —
// that turning any of it on changes nothing about WHEN events fire, since
// the scorer is watching the same schedule.

// specRec is one NoteOnSpec as an articulating voice received it.
type specRec struct {
	frame int64
	spec  synth.NoteSpec
}

// articVoice is a stubVoice that can also sound a NoteSpec, so a test can
// see exactly what the engine asked for.
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

// techScore is two consecutive quarter notes on one string, the second
// carrying tech. Consecutive beats tile, so the first note's end and the
// second note's start are the same tick — the case a continuation has to
// survive, because the note-off would otherwise land on the same frame.
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

// renderBar drives e through one 4/4 bar at 120 BPM plus a little slack.
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
			// The first note is picked; the second continues it; the third
			// carries no technique and is picked again.
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

// TestContinuationSuppressesTheRelease is the regression guard for the
// interaction that makes or breaks a slide: the ringing note's NoteOff
// falls on the very frame the slide fires, and note-offs are applied first.
// Left alone it damps the string a moment before the note that is supposed
// to keep it ringing.
func TestContinuationSuppressesTheRelease(t *testing.T) {
	var reg []*articVoice
	e := New(techScore(t, 6, 0, 3, score.TechSlide), Options{Voices: newArticFactory(&reg)})
	renderBar(e)
	v := reg[0]

	// Three note-ons, but only two releases: the slid-into note inherits
	// the first note's entry and is released once, at its own end.
	if len(v.offs) != 2 {
		t.Fatalf("got %d note-offs for 3 notes, want 2 (the slide inherits the first)", len(v.offs))
	}
	for _, off := range v.offs {
		if off.key == 40 && off.frame == v.specs[1].frame {
			t.Error("the origin note was released on the very frame it was slid away from")
		}
	}
	// The release that does happen names the destination key, not the
	// origin: the string moved, and NoteOff has to follow it.
	if got := v.offs[0].key; got != 43 {
		t.Errorf("first note-off names key %d, want the slide destination 43", got)
	}
}

// TestArticulationLeavesTheScheduleAlone is the one that protects the
// scorer. Wiring that judges timing hangs off the event tap and the wait
// points; articulation is about timbre and must not move either. The same
// score is rendered through a voice that articulates and one that does not,
// and the tapped frames must match exactly.
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

// TestPlainVoiceStillSoundsEveryNote: a Voice that cannot articulate is
// driven exactly as it always was, including the notes carrying techniques
// — which must be attacked, not skipped.
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

// TestContinuationNeedsTheSameString: a technique on one string cannot
// continue a note ringing on another. Two strings sounding at once is the
// ordinary case, and picking the wrong one to slide would silence a note
// and bend an unrelated one.
func TestContinuationNeedsTheSameString(t *testing.T) {
	s := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "Guitar", Tuning: score.StandardTuning}
	s.Tracks = []*score.Track{tr}
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 5, Fret: 2}) // ringing on string 5
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

// TestVibratoReachesTheVoice: vibrato is a sustain-phase technique and
// rides along on an otherwise ordinary attack.
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
