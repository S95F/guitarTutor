package pitch

// The dip-recovery onset (Config.OnsetDipDB): winds re-articulate by
// tonguing, and a tongue stroke is a level DIP that recovers — a cue the
// rise-only level trigger and the magnitude-based flux trigger cannot see,
// so repeated same-pitch notes used to merge into one detection and the
// second expectation deadlined into a false Miss (docs/DECISIONS.md D5,
// D8; ROADMAP "soft same-pitch tonguing"). These tests drive
// internal/synth's reed voice directly — NoteOff, a tongue-gap's worth of
// its 90 ms-T60 release, NoteOn — the articulation the engine (gapless by
// design) never renders but a player produces on every stroke.
//
// Everything here is measured on the synthesized reed and provisional
// until real recordings exist (docs/TESTDATA.md, wind/articulation).

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/synth"
)

// windTestConfig is the wind round trip's detector config: ConfigForKeys
// over the soprano sax's sounding compass, Ab3..E6 (MIDI 56..88) — spelled
// literally so the package stays free of an internal/score dependency.
func windTestConfig() Config {
	return ConfigForKeys(testSR, 56, 88)
}

// reedTongued renders n same-pitch tongued notes of noteSec each at
// 48 kHz: NoteOn holds for noteSec-gapSec, then NoteOff leaves gapSec of
// release before the next attack — the dip a tongue stroke leaves. 0.3 s
// of leading silence and 0.4 s of tail let the first attack and the last
// release resolve.
func reedTongued(key, n int, noteSec, gapSec float64) []float32 {
	v := synth.NewReed(testSR, 64)
	x := silence(0.3)
	for i := 0; i < n; i++ {
		v.NoteOn(key, 0.8)
		x = ksRender(v, noteSec-gapSec, x)
		v.NoteOff(key)
		x = ksRender(v, gapSec, x)
	}
	return ksRender(v, 0.4, x)
}

// softTongue scales x by a dip-and-recover envelope at boundary:
// exponential fall over fallMS, a hold at -depthDB, exponential rise over
// riseMS. It models the stroke the reed voice cannot render on its own — a
// tongue PARTIALLY occluding the reed mid-breath: no phase reset, no fresh
// attack transient, nothing but level.
func softTongue(x []float32, boundary int, depthDB, fallMS, holdMS, riseMS float64) []float32 {
	out := make([]float32, len(x))
	copy(out, x)
	fall := int(fallMS * testSR / 1000)
	hold := int(holdMS * testSR / 1000)
	rise := int(riseMS * testSR / 1000)
	floor := math.Pow(10, -depthDB/20)
	for i := 0; i < fall+hold+rise && boundary+i < len(out); i++ {
		g := floor
		switch {
		case i < fall:
			g = math.Pow(floor, float64(i)/float64(fall))
		case i >= fall+hold:
			g = math.Pow(floor, 1-float64(i-fall-hold)/float64(rise))
		}
		out[boundary+i] *= float32(g)
	}
	return out
}

// trackAll runs x through a fresh Detector and Tracker and returns every
// closed note, Flush included.
func trackAll(cfg Config, x []float32) []Note {
	d := NewDetector(cfg)
	trk := NewTracker(cfg)
	var notes []Note
	for off := 0; off < len(x); off += 480 {
		end := off + 480
		if end > len(x) {
			end = len(x)
		}
		notes = append(notes, trk.Feed(d.Process(x[off:end]))...)
	}
	return append(notes, trk.Flush()...)
}

// TestTrackerTonguedRepeatsSplit is the wind money case at the tracker
// level: repeated same-pitch tongued notes must come out as exactly that
// many notes, one per stroke. A 25 ms gap against the reed's 90 ms release
// T60 dips the hop RMS only ~13 dB — nowhere near the 8 dB RISE the level
// trigger wants, and the merge that used to follow scored the second note
// a false Miss. The rest row exercises the other regime: gaps long enough
// that the level decays toward silence and the plain level trigger owns
// the attack, which the dip path must not double-fire on.
func TestTrackerTonguedRepeatsSplit(t *testing.T) {
	const key = 72 // sounding C5, mid-compass
	tests := []struct {
		name          string
		n             int
		noteSec, gapS float64
	}{
		{"4 quarters at 120 bpm, 25 ms strokes", 4, 0.5, 0.025},
		{"8 eighths at 120 bpm, 25 ms strokes", 8, 0.25, 0.025},
		{"4 quarters, light 15 ms strokes", 4, 0.5, 0.015},
		{"4 quarters, heavy 40 ms strokes", 4, 0.5, 0.040},
		{"4 quarters detached by 150 ms rests", 4, 0.5, 0.150},
	}
	cfg := windTestConfig()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notes := trackAll(cfg, reedTongued(key, tt.n, tt.noteSec, tt.gapS))
			if len(notes) != tt.n {
				t.Fatalf("tracked %d notes, want %d: tongued repeats must not merge or split", len(notes), tt.n)
			}
			for i, n := range notes {
				if n.Key != key {
					t.Errorf("note %d on key %d, want %d", i, n.Key, key)
				}
			}
		})
	}
}

// TestOnsetDipCatchesSoftTonguing pins the mechanism on the one signal
// only it can see: a soft stroke that dips the level 8 dB and recovers
// over 60 ms, mid-breath, with no phase reset and no attack transient.
// The flux over this signal peaks at 0.146 — under its 0.20 threshold —
// and the recovering edge never reaches the level trigger's 8 dB/hop, so
// with the dip path disabled the two notes merge into one; that contrast
// is asserted, not assumed. The onset must also stamp near the RECOVERY —
// the re-articulated note's start, the moment scoring aligns against —
// not at the dip's bottom.
func TestOnsetDipCatchesSoftTonguing(t *testing.T) {
	v := synth.NewReed(testSR, 64)
	x := silence(0.3)
	v.NoteOn(72, 0.8)
	x = ksRender(v, 1.5, x)
	boundary := int(0.9 * testSR)
	x = softTongue(x, boundary, 8, 10, 10, 60)

	cfg := windTestConfig()
	if notes := trackAll(cfg, x); len(notes) != 2 {
		t.Errorf("with the dip path: %d notes, want 2 (one per side of the stroke)", len(notes))
	}
	off := cfg
	off.OnsetDipDB = 0
	if notes := trackAll(off, x); len(notes) != 1 {
		t.Errorf("without the dip path: %d notes, want the 1 merged note (nothing else sees this stroke)", len(notes))
	}

	d := NewDetector(cfg)
	var stroke int64 = -1
	for _, f := range feedAll(d, x, 480) {
		if f.Onset && f.Frame > int64(boundary)-2400 {
			stroke = f.Frame
			break
		}
	}
	if stroke < 0 {
		t.Fatal("no onset at the stroke")
	}
	// The envelope reaches back within onsetDipRecoverDB of the reference
	// partway up its 60 ms rise; the stamp belongs inside the stroke's
	// 80 ms, at the recovering (not the falling) edge.
	if rel := stroke - int64(boundary); rel < 2*480 || rel > int64(0.1*testSR) {
		t.Errorf("onset stamped %+.0f ms after the stroke began, want within its recovery (20–100 ms)",
			float64(stroke-int64(boundary))*1000/testSR)
	}
}

// countOnsetsAndNotes runs x through a fresh Detector and Tracker under
// cfg and returns the total onset count and every closed note.
func countOnsetsAndNotes(cfg Config, x []float32) (int, []Note) {
	d := NewDetector(cfg)
	trk := NewTracker(cfg)
	onsets := 0
	var notes []Note
	for off := 0; off < len(x); off += 480 {
		end := off + 480
		if end > len(x) {
			end = len(x)
		}
		fr := d.Process(x[off:end])
		for _, f := range fr {
			if f.Onset {
				onsets++
			}
		}
		notes = append(notes, trk.Feed(fr)...)
	}
	return onsets, append(notes, trk.Flush()...)
}

// TestOnsetDipNoFalseSplits holds the other side of the bargain: every
// wind gesture that is NOT a re-articulation must survive the dip path
// intact. Two assertions per signal: the note count the scorer depends
// on, and — because a slurred change and a slide already carry a flux
// onset at their boundary (a re-pitched tone is new magnitude in new
// bins; measured 0.24–0.44 on these signals), so a phantom dip could hide
// inside a count that comes out right — that the dip path changes NOTHING:
// onset and note counts are identical with OnsetDipDB on and off.
func TestOnsetDipNoFalseSplits(t *testing.T) {
	tests := []struct {
		name        string
		x           []float32
		notes       int
		description string
	}{}
	add := func(name string, x []float32, notes int, why string) {
		tests = append(tests, struct {
			name        string
			x           []float32
			notes       int
			description string
		}{name, x, notes, why})
	}

	// A 3 s held note with vibrato: one attack, one note. The ±25 cent
	// wobble moves pitch, not level; a dip fire here would shatter every
	// long tone in a piece.
	{
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		a.NoteOnSpec(synth.NoteSpec{Key: 72, Velocity: 0.8, Vibrato: true})
		x = ksRender(v, 3.0, x)
		add("held vibrato note", x, 1, "one attack, one 3 s note")
	}
	// A slurred change: the standing tone re-pitched, no fresh attack and
	// no level dip. Two notes, split by the discontinuous pitch jump.
	{
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		v.NoteOn(72, 0.8)
		x = ksRender(v, 1.0, x)
		a.NoteOnSpec(synth.NoteSpec{Key: 76, Velocity: 0.8, From: 72, Attack: synth.AttackLegato})
		x = ksRender(v, 1.0, x)
		add("slurred change", x, 2, "split by pitch, not by a phantom dip")
	}
	// The same slur backing off to velocity 0.5 — a 4 dB step down, the
	// loudest non-rearticulation excursion measured (4.1 dB at its worst
	// hop phase). It sits under the 5 dB arming depth, and even armed it
	// could never fire: a step down does not recover.
	{
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		v.NoteOn(72, 0.8)
		x = ksRender(v, 1.0, x)
		a.NoteOnSpec(synth.NoteSpec{Key: 76, Velocity: 0.5, From: 72, Attack: synth.AttackLegato})
		x = ksRender(v, 1.0, x)
		add("slur with a velocity drop", x, 2, "the step down never recovers")
	}
	// A scoop: the level is constant while the pitch travels, so the dip
	// path must see nothing (measured excursion 0.2 dB). The flux trigger
	// DOES see the 300-cent partial sweep and splits origin from
	// destination — pre-existing behavior the dip path must not add to.
	{
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		v.NoteOn(72, 0.8)
		x = ksRender(v, 1.0, x)
		a.NoteOnSpec(synth.NoteSpec{Key: 75, Velocity: 0.8, From: 72, Attack: synth.AttackSlide})
		x = ksRender(v, 1.0, x)
		add("slide", x, 2, "split by the flux trigger's view of the sweep, not by a dip")
	}

	cfg := windTestConfig()
	noDip := cfg
	noDip.OnsetDipDB = 0
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			onsets, notes := countOnsetsAndNotes(cfg, tt.x)
			if len(notes) != tt.notes {
				t.Errorf("%d notes, want %d (%s)", len(notes), tt.notes, tt.description)
			}
			baseOnsets, baseNotes := countOnsetsAndNotes(noDip, tt.x)
			if onsets != baseOnsets || len(notes) != len(baseNotes) {
				t.Errorf("dip path changed the outcome: %d onsets/%d notes with it, %d/%d without (%s)",
					onsets, len(notes), baseOnsets, len(baseNotes), tt.description)
			}
		})
	}
}

// armExcursion is the dip trigger's arming variable, recomputed on the
// detector's own hop grid (hops end at Window + k*Hop): the peak, over
// [from, to], of the smoothed level over the hop RMS in dB, with the
// reference taken before the hop's own one-pole update — exactly what
// analyzeWindow compares Config.OnsetDipDB against.
func armExcursion(x []float32, cfg Config, from, to int) float64 {
	smoothed, peak := 0.0, 0.0
	for end := cfg.Window; end <= len(x); end += cfg.Hop {
		var sq float64
		for _, s := range x[end-cfg.Hop : end] {
			sq += float64(s) * float64(s)
		}
		rms := math.Sqrt(sq / float64(cfg.Hop))
		if smoothed > 0 && rms > 0 && end >= from && end <= to {
			if a := 20 * math.Log10(smoothed/rms); a > peak {
				peak = a
			}
		}
		smoothed = onsetSmoothing*smoothed + (1-onsetSmoothing)*rms
	}
	return peak
}

// TestOnsetDipSeparation records the measured arming excursion on both
// sides of the decision, so windOnsetDipDB's headroom is visible rather
// than folklore — the same contract TestOnsetFluxSeparation holds over
// its threshold. Each signal is swept across four phases of the hop grid
// (a hop boundary landing mid-dip shallows the measured excursion);
// must-catch rows keep their worst (smallest) phase, must-not rows their
// worst (largest). The 7 and 6 dB soft strokes are logged but not
// asserted: they are the threshold's published edge, not its contract.
func TestOnsetDipSeparation(t *testing.T) {
	cfg := windTestConfig().withDefaults()
	phases := []int{0, 120, 240, 360}

	// Must catch: full tongue gaps over the reed's release, and the
	// 8 dB soft stroke the flux trigger is blind to.
	minCatch, minName := math.Inf(1), ""
	for _, gapMS := range []float64{15, 25, 40} {
		for _, ph := range phases {
			gap := gapMS / 1000
			v := synth.NewReed(testSR, 64)
			x := silence(0.3)
			v.NoteOn(72, 0.8)
			x = ksRender(v, 0.6+float64(ph)/testSR-gap, x)
			b := len(x)
			v.NoteOff(72)
			x = ksRender(v, gap, x)
			v.NoteOn(72, 0.8)
			x = ksRender(v, 0.6, x)
			if a := armExcursion(x, cfg, b-2400, b+9600); a < minCatch {
				minCatch, minName = a, fmt.Sprintf("%.0f ms tongue gap", gapMS)
			}
		}
	}
	soft := func(depth float64) (lo float64) {
		lo = math.Inf(1)
		for _, ph := range phases {
			v := synth.NewReed(testSR, 64)
			x := silence(0.3)
			v.NoteOn(72, 0.8)
			x = ksRender(v, 1.5, x)
			b := int(0.9*testSR) + ph
			x = softTongue(x, b, depth, 10, 10, 60)
			if a := armExcursion(x, cfg, b-2400, b+9600); a < lo {
				lo = a
			}
		}
		return lo
	}
	if a := soft(8); a < minCatch {
		minCatch, minName = a, "8 dB soft stroke"
	}

	// Must not catch: the loudest excursion any held or slurred gesture
	// produces.
	maxHold, maxName := 0.0, ""
	for _, ph := range phases {
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		v.NoteOn(72, 0.8)
		x = ksRender(v, 0.6+float64(ph)/testSR, x)
		b := len(x)
		a.NoteOnSpec(synth.NoteSpec{Key: 76, Velocity: 0.5, From: 72, Attack: synth.AttackLegato})
		x = ksRender(v, 0.8, x)
		if e := armExcursion(x, cfg, b-2400, b+9600); e > maxHold {
			maxHold, maxName = e, "slur, velocity 0.8->0.5"
		}
	}
	{
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		a.NoteOnSpec(synth.NoteSpec{Key: 72, Velocity: 0.8, Vibrato: true})
		x = ksRender(v, 3.0, x)
		if e := armExcursion(x, cfg, int(0.5*testSR), len(x)); e > maxHold {
			maxHold, maxName = e, "held vibrato"
		}
	}
	{
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		v.NoteOn(72, 0.8)
		x = ksRender(v, 0.8, x)
		a.NoteOnSpec(synth.NoteSpec{Key: 75, Velocity: 0.8, From: 72, Attack: synth.AttackSlide})
		x = ksRender(v, 0.8, x)
		if e := armExcursion(x, cfg, int(0.5*testSR), len(x)); e > maxHold {
			maxHold, maxName = e, "slide"
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nquietest re-articulation: %.2f dB (%s)\n", minCatch, minName)
	fmt.Fprintf(&b, "edge (not asserted):      %.2f dB (7 dB soft stroke), %.2f dB (6 dB soft stroke)\n", soft(7), soft(6))
	fmt.Fprintf(&b, "loudest held gesture:     %.2f dB (%s)\n", maxHold, maxName)
	fmt.Fprintf(&b, "arming depth:             %.2f dB\n", float64(windOnsetDipDB))
	t.Log(b.String())

	if minCatch <= windOnsetDipDB {
		t.Errorf("quietest re-articulation excursion %.2f dB is at or below the arming depth %d (%s)",
			minCatch, windOnsetDipDB, minName)
	}
	if maxHold >= windOnsetDipDB {
		t.Errorf("loudest held-gesture excursion %.2f dB reaches the arming depth %d (%s)",
			maxHold, windOnsetDipDB, maxName)
	}
}

// TestDefaultConfigDipDisabled: the dip path is a wind trigger, and the
// guitar path must be untouched by construction — the zero value is off,
// DefaultConfig leaves it zero, and withDefaults must not invent a value
// (unlike every other Config field, zero is meaningful here).
func TestDefaultConfigDipDisabled(t *testing.T) {
	cfg := DefaultConfig(testSR)
	if cfg.OnsetDipDB != 0 || cfg.OnsetDipRecoverHops != 0 {
		t.Errorf("DefaultConfig dip fields = (%v, %v), want (0, 0): guitar keeps the dip path off",
			cfg.OnsetDipDB, cfg.OnsetDipRecoverHops)
	}
	filled := cfg.withDefaults()
	if filled.OnsetDipDB != 0 || filled.OnsetDipRecoverHops != 0 {
		t.Errorf("withDefaults dip fields = (%v, %v), want (0, 0): zero means off, not defaulted",
			filled.OnsetDipDB, filled.OnsetDipRecoverHops)
	}
	if wind := windTestConfig().withDefaults(); wind.OnsetDipRecoverHops != defaultOnsetDipRecoverHops {
		t.Errorf("wind OnsetDipRecoverHops = %d, want the %d default once the path is on",
			wind.OnsetDipRecoverHops, defaultOnsetDipRecoverHops)
	}
}

// TestDetectorDipPathDoesNotAllocate: the dip trigger runs on every hop of
// a wind session, inside the same realtime pipeline the guitar path pins
// with TestDetectorProcessDoesNotAllocate — a few comparisons of state
// already in the struct, nothing more.
func TestDetectorDipPathDoesNotAllocate(t *testing.T) {
	d := NewDetector(windTestConfig())
	v := synth.NewReed(testSR, 64)
	v.NoteOn(72, 0.8)
	x := ksRender(v, 1.0, nil)
	feedAll(d, x, 480) // warmup: grows the reused frame slice once
	chunk := x[:480]
	if allocs := testing.AllocsPerRun(200, func() {
		d.Process(chunk)
	}); allocs != 0 {
		t.Errorf("Process allocates %v times per call after warmup, want 0", allocs)
	}
}
