package pitch

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/synth"
)

func windTestConfig() Config {
	return ConfigForKeys(testSR, 56, 88)
}

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

func TestTrackerTonguedRepeatsSplit(t *testing.T) {
	const key = 72
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

	if rel := stroke - int64(boundary); rel < 2*480 || rel > int64(0.1*testSR) {
		t.Errorf("onset stamped %+.0f ms after the stroke began, want within its recovery (20–100 ms)",
			float64(stroke-int64(boundary))*1000/testSR)
	}
}

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

	{
		v := synth.NewReed(testSR, 64)
		a := v.(synth.Articulator)
		x := silence(0.3)
		a.NoteOnSpec(synth.NoteSpec{Key: 72, Velocity: 0.8, Vibrato: true})
		x = ksRender(v, 3.0, x)
		add("held vibrato note", x, 1, "one attack, one 3 s note")
	}

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

func TestOnsetDipSeparation(t *testing.T) {
	cfg := windTestConfig().withDefaults()
	phases := []int{0, 120, 240, 360}

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

func TestDetectorDipPathDoesNotAllocate(t *testing.T) {
	d := NewDetector(windTestConfig())
	v := synth.NewReed(testSR, 64)
	v.NoteOn(72, 0.8)
	x := ksRender(v, 1.0, nil)
	feedAll(d, x, 480)
	chunk := x[:480]
	if allocs := testing.AllocsPerRun(200, func() {
		d.Process(chunk)
	}); allocs != 0 {
		t.Errorf("Process allocates %v times per call after warmup, want 0", allocs)
	}
}
