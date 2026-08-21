package pitch

import (
	"fmt"
	"math"
	"testing"
)

// TestDetectorPureSines sweeps a pure sine over every semitone in guitar
// range — open low E (82.41 Hz, key 40) up to the 24th-fret high E
// (1318.5 Hz, key 88) — plus low B (61.74 Hz, 7-string / drop tunings'
// edge, still inside the default MinHz of 60). Each must be detected
// within ±3 cents with high clarity.
func TestDetectorPureSines(t *testing.T) {
	keys := []int{35} // low B 61.74 Hz
	for k := 40; k <= 88; k++ {
		keys = append(keys, k)
	}
	for _, key := range keys {
		freq := keyToFreq(key)
		t.Run(fmt.Sprintf("key%d_%.1fHz", key, freq), func(t *testing.T) {
			d := NewDetector(DefaultConfig(testSR))
			frames := feedAll(d, sine(freq, 0.4, 0.35), 480)
			got := medianF0Of(t, frames, 10)
			if off := cents(freq, got); math.Abs(off) > 3 {
				t.Errorf("f0 = %.3f Hz, %+.2f cents from %.3f Hz; want within ±3", got, off, freq)
			}
			// Clarity high on steady-state frames (skip the attack edge).
			voiced := voicedFrames(frames)
			for _, f := range voiced[2:] {
				if f.Clarity < 0.9 {
					t.Errorf("frame %d: clarity %.3f, want >= 0.9", f.Frame, f.Clarity)
				}
			}
		})
	}
}

// TestDetectorKarplusStrongNotes points the detector at the repo's own
// Karplus-Strong pluck voice — a guitar-like signal with a broadband
// attack, rich harmonics, and decay — for every key from open low E to
// the 24th-fret high E. The detected key must be exact.
//
// Low-key caveat: with the default Window 2048 / MinHz 60, the search
// range bottoms out around key 34 (~58 Hz); anything lower (drop C and
// below) needs Window 4096 per the Config doc. Keys 40–88 sit comfortably
// inside the default window.
func TestDetectorKarplusStrongNotes(t *testing.T) {
	for key := 40; key <= 88; key++ {
		t.Run(fmt.Sprintf("key%d", key), func(t *testing.T) {
			d := NewDetector(DefaultConfig(testSR))
			frames := feedAll(d, ksNote(key, 0.5), 480)
			got := medianF0Of(t, frames, 8)
			if gotKey := nearestKey(got); gotKey != key {
				t.Errorf("detected key %d (%.2f Hz), want %d (%.2f Hz)",
					gotKey, got, key, keyToFreq(key))
			}
		})
	}
}

// TestDetectorDetunedSine: a sine 20 cents sharp of A3 must come back
// within the detector's ±3 cents of the detuned frequency, i.e. the cents
// deviation against the key is +20, not 0.
func TestDetectorDetunedSine(t *testing.T) {
	const key = 57 // A3, 220 Hz
	freq := keyToFreq(key) * math.Pow(2, 20.0/1200)
	d := NewDetector(DefaultConfig(testSR))
	frames := feedAll(d, sine(freq, 0.4, 0.4), 480)
	got := medianF0Of(t, frames, 10)
	dev := (midiPitch(got) - key) * 100
	if math.Abs(dev-20) > 3 {
		t.Errorf("deviation = %+.2f cents from key %d, want +20 ±3", dev, key)
	}
}

// TestDetectorOnsetGate: silence, then a Karplus-Strong pluck. The Onset
// flag must fire on a frame stamped within 60 ms of the physical attack,
// and never during the leading silence.
func TestDetectorOnsetGate(t *testing.T) {
	lead := silence(0.3)
	attack := int64(len(lead))
	x := append(lead, ksNote(45, 0.4)...)

	d := NewDetector(DefaultConfig(testSR))
	frames := feedAll(d, x, 480)

	const tolerance = 60 * testSR / 1000 // 60 ms in samples
	var onsets []int64
	for _, f := range frames {
		if f.Onset {
			onsets = append(onsets, f.Frame)
		}
	}
	if len(onsets) == 0 {
		t.Fatal("no onset detected")
	}
	if diff := onsets[0] - attack; diff < -tolerance || diff > tolerance {
		t.Errorf("first onset at frame %d, attack at %d: %+d samples (%.1f ms), want within ±60 ms",
			onsets[0], attack, diff, float64(diff)*1000/testSR)
	}
	for _, o := range onsets {
		if o < attack-tolerance {
			t.Errorf("onset at frame %d during leading silence", o)
		}
	}
}

// TestDetectorSilenceAndNoiseUnvoiced pins the gate against the failure
// mode of naive pitch trackers (DECISIONS D4): silence or quiet noise must
// produce zero voiced frames and zero onsets, not garbage pitch.
func TestDetectorSilenceAndNoiseUnvoiced(t *testing.T) {
	tests := []struct {
		name string
		x    []float32
	}{
		{"pure silence", silence(1.0)},
		{"white noise at -70 dBFS", whiteNoise(math.Pow(10, -70.0/20), 1.0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDetector(DefaultConfig(testSR))
			frames := feedAll(d, tt.x, 480)
			if len(frames) == 0 {
				t.Fatal("no frames emitted")
			}
			for _, f := range frames {
				if f.F0 > 0 {
					t.Errorf("frame %d: voiced (%.2f Hz) on %s", f.Frame, f.F0, tt.name)
				}
				if f.Onset {
					t.Errorf("frame %d: onset on %s", f.Frame, tt.name)
				}
			}
		})
	}
}

// TestDetectorOctaveGuard: signals with a strong 2nd harmonic — the
// distorted/clipped-guitar shape that biases autocorrelation toward 2f —
// must still detect f. The second row (0.1/0.9) is harmonic-dominant
// enough that the raw first-peak rule picks 2f and only the octave guard
// recovers f.
func TestDetectorOctaveGuard(t *testing.T) {
	const freq = 110.0 // A2
	tests := []struct {
		name       string
		ampF, amp2 float64
	}{
		{"strong 2nd harmonic", 0.6, 0.8},
		{"dominant 2nd harmonic", 0.1, 0.9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDetector(DefaultConfig(testSR))
			frames := feedAll(d, harmonicMix(freq, tt.ampF, tt.amp2, 0.4), 480)
			got := medianF0Of(t, frames, 10)
			if gotKey := nearestKey(got); gotKey != nearestKey(freq) {
				t.Errorf("detected key %d (%.2f Hz), want key %d (%.2f Hz): octave error",
					gotKey, got, nearestKey(freq), freq)
			}
		})
	}
}

// TestDetectorTinyWindowDoesNotPanic is the regression test for the
// tauMax-clamp undo: with Window < SampleRate/MaxHz + 2 the clamp of
// tauMax to Window-2 was reversed by the tauMax >= tauMin fixup, and the
// first analysis indexed past the window buffer. withDefaults now rounds
// the window up to the minimum the MaxHz bound needs, and NewDetector
// shrinks tauMin instead of growing tauMax.
func TestDetectorTinyWindowDoesNotPanic(t *testing.T) {
	cfg := DefaultConfig(testSR)
	cfg.Window = 16 // far below SampleRate/MaxHz + 2 = 34: used to panic
	d := NewDetector(cfg)

	// withDefaults' guarantee: the window holds the whole search range.
	minWindow := testSR/1500 + 2 // tauMin + 2 at the default MaxHz
	if d.cfg.Window < minWindow {
		t.Errorf("Window = %d, want >= %d (rounded up by withDefaults)", d.cfg.Window, minWindow)
	}
	if d.tauMin < 2 || d.tauMin > d.tauMax || d.tauMax > d.cfg.Window-2 {
		t.Errorf("tau range [%d, %d] not inside 2..Window-2 (Window %d)", d.tauMin, d.tauMax, d.cfg.Window)
	}

	// The first analysis used to panic with index out of range.
	frames := feedAll(d, sine(1000, 0.4, 0.05), 480)
	if len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
}

// TestDetectorNearNyquistRangeDoesNotPanic: a tiny window with a search
// range pushed up near Nyquist put the flux band's low edge past the last
// bin the Hann kernel can read, and the fluxHi >= fluxLo fixup then
// dragged the high edge back out of the clamped range with it — the first
// loud hop indexed coeff out of range inside hannMags. The band must stay
// inside [2, Window-2] whatever the config asks for.
func TestDetectorNearNyquistRangeDoesNotPanic(t *testing.T) {
	cfg := DefaultConfig(testSR)
	cfg.Window = 4
	cfg.Hop = 4
	cfg.MinHz = 23000
	cfg.MaxHz = 24000
	d := NewDetector(cfg)
	if w := d.cfg.Window; d.fluxLo < 2 || d.fluxHi < d.fluxLo || d.fluxHi > w-2 {
		t.Errorf("flux band [%d, %d] not inside 2..Window-2 (Window %d)", d.fluxLo, d.fluxHi, w)
	}
	// Loud enough that hops are never gated, so hannMags actually runs.
	if frames := feedAll(d, sine(1000, 0.4, 0.5), 4); len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
}

// TestDetectorAboveRangeToneUnvoiced is the regression test for the
// subharmonic alias: a 2 kHz tone with the default MaxHz of 1500 has its
// true NSDF peak below tauMin, so the first in-range peak is the f/2
// alias at 1 kHz — reported with high clarity, because the YIN
// cross-check folds octaves and cannot veto it. The sub-range alias guard
// must mark such frames unvoiced instead.
func TestDetectorAboveRangeToneUnvoiced(t *testing.T) {
	d := NewDetector(DefaultConfig(testSR)) // MaxHz 1500
	frames := feedAll(d, sine(2000, 0.4, 0.4), 480)
	if len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
	for _, f := range frames {
		if f.F0 > 0 {
			t.Errorf("frame %d: voiced %.1f Hz (clarity %.3f) from a 2 kHz tone above MaxHz",
				f.Frame, f.F0, f.Clarity)
		}
	}
}

// TestDetectorInRangeToneNearMaxHz pins the other side of the alias
// guard: a legitimate tone just inside MaxHz must still be detected, at
// full accuracy — its NSDF has no rival maximum below its own period, so
// the guard stays idle.
func TestDetectorInRangeToneNearMaxHz(t *testing.T) {
	const freq = 1400.0
	d := NewDetector(DefaultConfig(testSR))
	frames := feedAll(d, sine(freq, 0.4, 0.4), 480)
	got := medianF0Of(t, frames, 10)
	if off := cents(freq, got); math.Abs(off) > 3 {
		t.Errorf("f0 = %.3f Hz, %+.2f cents from %.3f Hz; want within ±3", got, off, freq)
	}
}

// TestDetectorFrameStamps: frames are stamped at the window CENTER in
// input-stream samples — first at Window - Window/2, then every Hop —
// regardless of the chunk sizes Process is fed.
func TestDetectorFrameStamps(t *testing.T) {
	cfg := DefaultConfig(testSR)
	d := NewDetector(cfg)
	x := sine(220, 0.4, 0.5)
	frames := feedAll(d, x, 313) // deliberately awkward chunking
	wantCount := (len(x)-cfg.Window)/cfg.Hop + 1
	if len(frames) != wantCount {
		t.Fatalf("got %d frames from %d samples, want %d", len(frames), len(x), wantCount)
	}
	for i, f := range frames {
		want := int64(cfg.Window-cfg.Window/2) + int64(i*cfg.Hop)
		if f.Frame != want {
			t.Fatalf("frame %d stamped at %d, want %d", i, f.Frame, want)
		}
	}
}

// TestDetectorProcessDoesNotAllocate: after warmup, Process is
// allocation-free — the FFT plans, work buffers, and the returned slice
// are all reused. The detector feeds the realtime pipeline; a per-frame
// allocation would be a regression toward GC hitches.
func TestDetectorProcessDoesNotAllocate(t *testing.T) {
	d := NewDetector(DefaultConfig(testSR))
	x := sine(196, 0.4, 1.0)
	feedAll(d, x, 480) // warmup: grows the reused frame slice once
	chunk := x[:480]
	if allocs := testing.AllocsPerRun(200, func() {
		d.Process(chunk)
	}); allocs != 0 {
		t.Errorf("Process allocates %v times per call after warmup, want 0", allocs)
	}
}

// TestDetectorLatencyKey40 documents the detection latency bound at the
// worst supported case, open low E (key 40, 82.4 Hz): the first voiced
// frame after a Karplus-Strong attack must be stamped within 60 ms of the
// physical onset. Physics (D4): a 12 ms period and a detector wanting 2–4
// cycles put the floor around 25–50 ms; the frame's center stamp lands
// well inside 60 ms.
func TestDetectorLatencyKey40(t *testing.T) {
	lead := silence(0.3)
	attack := int64(len(lead))
	x := append(lead, ksNote(40, 0.4)...)

	d := NewDetector(DefaultConfig(testSR))
	frames := feedAll(d, x, 480)
	voiced := voicedFrames(frames)
	if len(voiced) == 0 {
		t.Fatal("no voiced frames")
	}
	const tolerance = 60 * testSR / 1000 // 60 ms in samples
	if diff := voiced[0].Frame - attack; diff < -tolerance || diff > tolerance {
		t.Errorf("first voiced frame at %d, attack at %d: %+d samples (%.1f ms), want within ±60 ms",
			voiced[0].Frame, attack, diff, float64(diff)*1000/testSR)
	}
}
