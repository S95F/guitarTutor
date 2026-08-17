package synth

import (
	"math"
	"testing"
)

// The articulation contract: a slide travels, a hammer-on arrives at once,
// neither re-strikes the string, and anything with nothing to continue from
// falls back to being picked.

// pitchOver estimates the fundamental of a window of the rendered signal,
// in Hz. from and to are seconds.
func pitchOver(t *testing.T, mono []float32, sr int, from, to float64) float64 {
	t.Helper()
	lo, hi := int(from*float64(sr)), int(to*float64(sr))
	if hi > len(mono) {
		hi = len(mono)
	}
	return estimateFundamental(t, mono[lo:hi], sr)
}

// TestPluckSlideReachesTheDestination is the headline: a note marked as
// slid into ends up sounding the note it was slid to, without the string
// ever being picked again.
func TestPluckSlideReachesTheDestination(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25).(*pluck)
	v.NoteOn(52, 0.8) // E3
	left, right := renderFrames(v, sr/2, 512)
	v.NoteOnSpec(NoteSpec{Key: 57, Velocity: 0.8, Attack: AttackSlide, From: 52}) // up to A3
	l2, r2 := renderFrames(v, sr, 512)

	mono := monoSum(append(left, l2...), append(right, r2...))
	// Before the slide the string is at E3; well after it, at A3.
	if got, want := pitchOver(t, mono, sr, 0.25, 0.5), keyFreq(52); math.Abs(centsBetween(want, got)) > 10 {
		t.Errorf("before the slide: %.2f Hz, want E3 %.2f Hz", got, want)
	}
	if got, want := pitchOver(t, mono, sr, 0.7, 1.4), keyFreq(57); math.Abs(centsBetween(want, got)) > 10 {
		t.Errorf("after the slide: %.2f Hz, want A3 %.2f Hz", got, want)
	}
}

// TestPluckSlideDoesNotRestrike is what separates a slide from two notes.
// A pick puts a broadband burst of energy into the string; a slide puts in
// none at all, so the envelope over the slide must keep falling.
func TestPluckSlideDoesNotRestrike(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25).(*pluck)
	v.NoteOn(52, 0.8)
	before, br := renderFrames(v, sr/4, 512)
	v.NoteOnSpec(NoteSpec{Key: 57, Velocity: 0.8, Attack: AttackSlide, From: 52})
	after, ar := renderFrames(v, sr/4, 512)

	pre := energy(monoSum(before[len(before)-sr/20:], br[len(br)-sr/20:]))
	post := energy(monoSum(after[:sr/20], ar[:sr/20]))
	if post > pre {
		t.Errorf("energy rose across the slide (%.4f -> %.4f): the string was re-struck", pre, post)
	}

	// A plucked note at the same moment, by contrast, must add energy.
	w := NewPluck(sr, 25).(*pluck)
	w.NoteOn(52, 0.8)
	_, _ = renderFrames(w, sr/4, 512)
	w.NoteOn(57, 0.8)
	pl, pr := renderFrames(w, sr/4, 512)
	if plucked := energy(monoSum(pl[:sr/20], pr[:sr/20])); plucked <= post {
		t.Errorf("a pluck (%.4f) put no more energy in than a slide (%.4f); the test cannot tell them apart",
			plucked, post)
	}
}

// TestPluckSlideTravels pins the middle of the gesture: partway through,
// the pitch is between the two notes rather than already arrived. A slide
// that jumped would pass the destination test above and still be wrong.
func TestPluckSlideTravels(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25).(*pluck)
	v.NoteOn(40, 0.9) // low E, slow enough to measure mid-glide
	_, _ = renderFrames(v, sr/4, 512)
	v.NoteOnSpec(NoteSpec{Key: 47, Velocity: 0.9, Attack: AttackSlide, From: 40})

	// Halfway through the slide, in samples.
	half := int(pluckSlideSeconds * sr / 2)
	l, r := renderFrames(v, half, 64)
	mid := v.voices[0].d
	lo, hi := v.delayFor(47), v.delayFor(40)
	if !(mid > lo && mid < hi) {
		t.Errorf("mid-slide delay %.1f is not between the destination %.1f and the origin %.1f", mid, lo, hi)
	}
	if peak(monoSum(l, r)) == 0 {
		t.Error("the string fell silent during the slide")
	}

	// And it arrives exactly, not approximately: a glide that overshot
	// would leave the note permanently out of tune.
	_, _ = renderFrames(v, sr/4, 512)
	if got := v.voices[0].d; got != lo {
		t.Errorf("after the slide the delay is %.6f, want exactly the destination %.6f", got, lo)
	}
}

// TestPluckLegatoArrivesAtOnce: a hammer-on takes the new pitch
// immediately, unlike a slide.
func TestPluckLegatoArrivesAtOnce(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25).(*pluck)
	v.NoteOn(40, 0.9)
	_, _ = renderFrames(v, sr/4, 512)
	v.NoteOnSpec(NoteSpec{Key: 42, Velocity: 0.9, Attack: AttackLegato, From: 40})
	if got, want := v.voices[0].d, v.delayFor(42); got != want {
		t.Errorf("delay after a hammer-on = %.6f, want the destination %.6f at once", got, want)
	}
	if v.voices[0].dStep != 0 {
		t.Error("a hammer-on left a glide running")
	}
}

// TestPluckContinuationKeepsOneVoice: a slid or hammered note is the same
// string still ringing, so it must not take a second voice out of the pool
// — and the destination key is what NoteOff has to name afterwards.
func TestPluckContinuationKeepsOneVoice(t *testing.T) {
	const sr = 48000
	for _, tt := range []struct {
		name   string
		attack Attack
	}{{"slide", AttackSlide}, {"legato", AttackLegato}} {
		t.Run(tt.name, func(t *testing.T) {
			v := NewPluck(sr, 25).(*pluck)
			v.NoteOn(52, 0.8)
			_, _ = renderFrames(v, 4800, 512)
			v.NoteOnSpec(NoteSpec{Key: 57, Velocity: 0.8, Attack: tt.attack, From: 52})
			if n := activeVoices(v); n != 1 {
				t.Errorf("%d voices sounding after a continuation, want 1", n)
			}
			if got := v.voices[0].key; got != 57 {
				t.Errorf("the voice reports key %d, want the destination 57", got)
			}
			// The origin key must no longer stop it; the destination must.
			v.NoteOff(52)
			if v.voices[0].decay != pluckSustain {
				t.Error("NoteOff on the origin key released a note that has moved on from it")
			}
			v.NoteOff(57)
			if v.voices[0].decay != pluckRelease {
				t.Error("NoteOff on the destination key did not release the note")
			}
		})
	}
}

// TestPluckContinuationWithoutOriginPlucks: a score that slides into the
// first note of a piece, or whose previous note has already decayed away,
// has nothing to continue from. Sounding nothing would be the one
// unacceptable answer.
func TestPluckContinuationWithoutOriginPlucks(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25).(*pluck)
	v.NoteOnSpec(NoteSpec{Key: 57, Velocity: 0.8, Attack: AttackSlide, From: 52})
	l, r := renderFrames(v, sr/2, 512)
	if peak(monoSum(l, r)) == 0 {
		t.Fatal("a slide with nothing to slide from sounded nothing")
	}
	if got, want := pitchOver(t, monoSum(l, r), sr, 0.2, 0.5), keyFreq(57); math.Abs(centsBetween(want, got)) > 10 {
		t.Errorf("fell back to %.2f Hz, want the written note %.2f Hz", got, want)
	}
}

// TestPluckVibratoMovesThePitchAroundTheNote: vibrato is an oscillation
// ABOUT the written pitch, so the note is still the note — the average must
// land on it while the instantaneous delay moves either side.
func TestPluckVibratoMovesThePitchAroundTheNote(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25).(*pluck)
	v.NoteOnSpec(NoteSpec{Key: 45, Velocity: 0.8, Vibrato: true})

	rest := v.delayFor(45)
	var lo, hi float64 = math.Inf(1), math.Inf(-1)
	sum, n := 0.0, 0
	// One vibrato cycle, sampled finely.
	cycle := int(math.Round(float64(sr) / pluckVibratoHz))
	for i := 0; i < cycle; i++ {
		v.voices[0].tick()
		d := v.voices[0].effectiveDelay()
		lo, hi = math.Min(lo, d), math.Max(hi, d)
		sum += d
		n++
	}
	if hi-lo < 1.8*rest*vibratoDepth {
		t.Errorf("vibrato swing %.3f samples is far short of the %.1f cents it claims", hi-lo, pluckVibratoCents)
	}
	if mean := sum / float64(n); math.Abs(mean-rest) > rest*vibratoDepth/4 {
		t.Errorf("vibrato centred on %.2f samples, want the written pitch's %.2f", mean, rest)
	}
}

// TestPluckArticulatedRenderDoesNotAllocate: the articulated paths are on
// the same realtime thread as everything else.
func TestPluckArticulatedRenderDoesNotAllocate(t *testing.T) {
	const sr = 48000
	v := NewPluck(sr, 25).(*pluck)
	left, right := make([]float32, 512), make([]float32, 512)
	v.NoteOn(45, 0.8)
	v.Render(left, right)
	got := testing.AllocsPerRun(200, func() {
		v.NoteOnSpec(NoteSpec{Key: 47, Velocity: 0.8, Attack: AttackSlide, From: 45, Vibrato: true})
		v.NoteOnSpec(NoteSpec{Key: 45, Velocity: 0.8, Attack: AttackLegato, From: 47})
		v.Render(left, right)
	})
	if got != 0 {
		t.Errorf("articulated note-ons and Render allocate %.1f times per run, want 0", got)
	}
}

// activeVoices counts the sounding voices in a pool.
func activeVoices(p *pluck) int {
	n := 0
	for i := range p.voices {
		if p.voices[i].active {
			n++
		}
	}
	return n
}
