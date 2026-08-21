package pitch

import (
	"fmt"
	"math"
	"testing"
)

func TestDetectorPureSines(t *testing.T) {
	keys := []int{35}
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

			voiced := voicedFrames(frames)
			for _, f := range voiced[2:] {
				if f.Clarity < 0.9 {
					t.Errorf("frame %d: clarity %.3f, want >= 0.9", f.Frame, f.Clarity)
				}
			}
		})
	}
}

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

func TestDetectorDetunedSine(t *testing.T) {
	const key = 57
	freq := keyToFreq(key) * math.Pow(2, 20.0/1200)
	d := NewDetector(DefaultConfig(testSR))
	frames := feedAll(d, sine(freq, 0.4, 0.4), 480)
	got := medianF0Of(t, frames, 10)
	dev := (midiPitch(got) - key) * 100
	if math.Abs(dev-20) > 3 {
		t.Errorf("deviation = %+.2f cents from key %d, want +20 ±3", dev, key)
	}
}

func TestDetectorOnsetGate(t *testing.T) {
	lead := silence(0.3)
	attack := int64(len(lead))
	x := append(lead, ksNote(45, 0.4)...)

	d := NewDetector(DefaultConfig(testSR))
	frames := feedAll(d, x, 480)

	const tolerance = 60 * testSR / 1000
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

func TestDetectorOctaveGuard(t *testing.T) {
	const freq = 110.0
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

func TestDetectorTinyWindowDoesNotPanic(t *testing.T) {
	cfg := DefaultConfig(testSR)
	cfg.Window = 16
	d := NewDetector(cfg)

	minWindow := testSR/1500 + 2
	if d.cfg.Window < minWindow {
		t.Errorf("Window = %d, want >= %d (rounded up by withDefaults)", d.cfg.Window, minWindow)
	}
	if d.tauMin < 2 || d.tauMin > d.tauMax || d.tauMax > d.cfg.Window-2 {
		t.Errorf("tau range [%d, %d] not inside 2..Window-2 (Window %d)", d.tauMin, d.tauMax, d.cfg.Window)
	}

	frames := feedAll(d, sine(1000, 0.4, 0.05), 480)
	if len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
}

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

	if frames := feedAll(d, sine(1000, 0.4, 0.5), 4); len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
}

func TestDetectorAboveRangeToneUnvoiced(t *testing.T) {
	d := NewDetector(DefaultConfig(testSR))
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

func TestDetectorInRangeToneNearMaxHz(t *testing.T) {
	const freq = 1400.0
	d := NewDetector(DefaultConfig(testSR))
	frames := feedAll(d, sine(freq, 0.4, 0.4), 480)
	got := medianF0Of(t, frames, 10)
	if off := cents(freq, got); math.Abs(off) > 3 {
		t.Errorf("f0 = %.3f Hz, %+.2f cents from %.3f Hz; want within ±3", got, off, freq)
	}
}

func TestDetectorFrameStamps(t *testing.T) {
	cfg := DefaultConfig(testSR)
	d := NewDetector(cfg)
	x := sine(220, 0.4, 0.5)
	frames := feedAll(d, x, 313)
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

func TestDetectorProcessDoesNotAllocate(t *testing.T) {
	d := NewDetector(DefaultConfig(testSR))
	x := sine(196, 0.4, 1.0)
	feedAll(d, x, 480)
	chunk := x[:480]
	if allocs := testing.AllocsPerRun(200, func() {
		d.Process(chunk)
	}); allocs != 0 {
		t.Errorf("Process allocates %v times per call after warmup, want 0", allocs)
	}
}

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
	const tolerance = 60 * testSR / 1000
	if diff := voiced[0].Frame - attack; diff < -tolerance || diff > tolerance {
		t.Errorf("first voiced frame at %d, attack at %d: %+d samples (%.1f ms), want within ±60 ms",
			voiced[0].Frame, attack, diff, float64(diff)*1000/testSR)
	}
}
