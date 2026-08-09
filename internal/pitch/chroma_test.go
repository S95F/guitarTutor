package pitch

import (
	"math"
	"testing"
)

// singleStrum feeds x through a strum-enabled detector and requires
// exactly one Strum, returning it.
func singleStrum(t *testing.T, x []float32) Strum {
	t.Helper()
	d := NewDetector(strumConfig())
	_, strums := feedStrums(d, x, 480)
	if len(strums) != 1 {
		t.Fatalf("got %d strums, want exactly 1", len(strums))
	}
	return strums[0]
}

// TestChromaPureSinePitchClass: a pure sine lands in its own pitch class,
// in any octave. A2 is the interesting one — at 110 Hz the semitone
// spacing is 6 Hz while the detector's FFT bins are 11.7 Hz apart, so a
// nearest-bin fold would file it under G# or A#; only the interpolated
// peak position gets it right.
func TestChromaPureSinePitchClass(t *testing.T) {
	tests := []struct {
		name string
		freq float64
	}{
		{"A4", 440},
		{"A2", 110},
		{"A5", 880},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := singleStrum(t, append(silence(0.2), sine(tt.freq, 0.4, 0.6)...))
			const wantClass = 9 // A
			rank := chromaRank(s.Chroma)
			if rank[0] != wantClass {
				t.Fatalf("strongest class %s, want A;%s", pitchClassNames[rank[0]], formatChroma(s.Chroma))
			}
			if got := s.Chroma[rank[1]]; got > 0.5 {
				t.Errorf("runner-up class %s at %.2f, want <= 0.5 (A is 1.00);%s",
					pitchClassNames[rank[1]], got, formatChroma(s.Chroma))
			}
		})
	}
}

// TestChromaTriadSoundedClasses points the fold at a strummed E5-shaped
// triad — low E, B, E an octave up (keys 40, 47, 52), i.e. pitch classes E
// and B — and requires both to stand clear of the ten classes nobody
// played, by a margin rather than by ordering alone.
//
// The unsounded classes are not zero and never will be: the third harmonic
// of E is B, the fifth is G#, the ninth is F#, and a chroma fold cannot
// tell a partial from a fundamental. That is exactly why Phase 4 verifies
// against the EXPECTED chord's template (Oudre et al., docs/DECISIONS.md
// D4) instead of thresholding classes one at a time — the template carries
// the same harmonic bleed.
func TestChromaTriadSoundedClasses(t *testing.T) {
	sounded := map[int]bool{4: true, 11: true} // E, B
	s := singleStrum(t, append(silence(0.2), ksChord(0.6, 40, 47, 52)...))

	minSounded, maxOther := 1.0, 0.0
	worst := 0
	for i, v := range s.Chroma {
		if sounded[i] {
			minSounded = math.Min(minSounded, float64(v))
			continue
		}
		if float64(v) > maxOther {
			maxOther, worst = float64(v), i
		}
	}
	if minSounded < maxOther+0.25 {
		t.Errorf("weakest sounded class %.2f vs strongest unsounded (%s) %.2f: want a margin >= 0.25;%s",
			minSounded, pitchClassNames[worst], maxOther, formatChroma(s.Chroma))
	}
	if minSounded < 1.25*maxOther {
		t.Errorf("weakest sounded class %.2f is under 1.25x the strongest unsounded %.2f;%s",
			minSounded, maxOther, formatChroma(s.Chroma))
	}
}

// TestStrumOnePerPluck: one attack, one Strum, stamped on the onset. The
// stamp is the onset FRAME's stamp, which sits at its window's center —
// up to half a window before the physical attack — so the Strum is early
// by construction, the same way Frame.Onset is.
func TestStrumOnePerPluck(t *testing.T) {
	t.Run("single pluck", func(t *testing.T) {
		lead := silence(0.3)
		attack := int64(len(lead))
		x := append(append(lead, ksNote(45, 0.5)...), silence(0.3)...)

		d := NewDetector(strumConfig())
		frames, strums := feedStrums(d, x, 480)
		if len(strums) != 1 {
			t.Fatalf("got %d strums from one pluck, want 1", len(strums))
		}
		var onsets []int64
		for _, f := range frames {
			if f.Onset {
				onsets = append(onsets, f.Frame)
			}
		}
		if len(onsets) != 1 {
			t.Fatalf("got %d onsets, want 1", len(onsets))
		}
		if strums[0].Frame != onsets[0] {
			t.Errorf("strum stamped at %d, onset frame at %d: want the onset's stamp",
				strums[0].Frame, onsets[0])
		}
		if diff := attack - strums[0].Frame; diff < 0 || diff > int64(d.cfg.Window/2) {
			t.Errorf("strum at %d, attack at %d: %+d samples, want 0..%d early (window-center stamping)",
				strums[0].Frame, attack, -diff, d.cfg.Window/2)
		}
		if strums[0].RMS <= 0 {
			t.Errorf("strum RMS = %v, want the peak level of the span", strums[0].RMS)
		}
	})

	t.Run("two plucks a second apart", func(t *testing.T) {
		lead := silence(0.2)
		first := ksNote(45, 1.0) // exactly 1 s, so the attacks are 1 s apart
		x := append(append(lead, first...), ksNote(52, 0.5)...)
		attacks := []int64{int64(len(lead)), int64(len(lead) + len(first))}

		d := NewDetector(strumConfig())
		_, strums := feedStrums(d, x, 480)
		if len(strums) != 2 {
			t.Fatalf("got %d strums from two plucks, want 2", len(strums))
		}
		// Each stamp lands on its own attack, up to the window-center
		// offset. The two are NOT exactly a second apart: the second
		// attack has to climb over the first note still ringing, which
		// can cost the onset detector an extra hop.
		for i, s := range strums {
			if diff := attacks[i] - s.Frame; diff < 0 || diff > int64(d.cfg.Window/2)+int64(d.cfg.Hop) {
				t.Errorf("strum %d at %d, attack at %d: %+d samples off",
					i, s.Frame, attacks[i], -diff)
			}
		}
		if got, want := chromaRank(strums[0].Chroma)[0], 9; got != want { // A
			t.Errorf("first strum's strongest class %s, want A;%s",
				pitchClassNames[got], formatChroma(strums[0].Chroma))
		}
		if got, want := chromaRank(strums[1].Chroma)[0], 4; got != want { // E
			t.Errorf("second strum's strongest class %s, want E;%s",
				pitchClassNames[got], formatChroma(strums[1].Chroma))
		}
	})
}

// TestStrumChordVersusSingleNote is the chord-vs-single signal the scorer
// keys on: three strings struck together produce ONE Strum whose chroma
// covers all three pitch classes and whose Clarity is far below a single
// note's, because no one period explains the waveform.
func TestStrumChordVersusSingleNote(t *testing.T) {
	// E major: E2, B2, G#3 — three distinct pitch classes.
	chord := singleStrum(t, append(silence(0.2), ksChord(0.6, 40, 47, 56)...))
	single := singleStrum(t, append(silence(0.2), ksNote(40, 0.6)...))

	// Measured: G# 1.00, B 0.92, E 0.78, then a 0.62 gap down to the
	// strongest unsounded class (F#, the ninth harmonic of E).
	want := []int{4, 11, 8} // E, B, G#
	top := chromaRank(chord.Chroma)[:3]
	for _, class := range want {
		if chord.Chroma[class] < 0.65 {
			t.Errorf("chord chroma: %s = %.2f, want >= 0.65;%s",
				pitchClassNames[class], chord.Chroma[class], formatChroma(chord.Chroma))
		}
		if !contains(top, class) {
			t.Errorf("chord chroma: %s is not among the three strongest classes;%s",
				pitchClassNames[class], formatChroma(chord.Chroma))
		}
	}

	if single.Clarity < 0.9 {
		t.Errorf("single note clarity %.3f, want >= 0.9", single.Clarity)
	}
	if chord.Clarity > 0.75 {
		t.Errorf("chord clarity %.3f, want <= 0.75 (a chord has no single period)", chord.Clarity)
	}
	if single.Clarity-chord.Clarity < 0.25 {
		t.Errorf("clarity gap %.3f (single %.3f, chord %.3f), want >= 0.25",
			single.Clarity-chord.Clarity, single.Clarity, chord.Clarity)
	}
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// TestStrumsDisabledMatchesEnabled: Config.Strums is purely additive.
// Without it Strums() is always empty, and with it every Frame — f0,
// clarity, RMS, onset — is bit-for-bit what it was, because the fold reads
// the same power spectrum the autocorrelation does and writes nothing back.
func TestStrumsDisabledMatchesEnabled(t *testing.T) {
	x := append(silence(0.2), ksChord(0.6, 40, 47, 52)...)

	off := NewDetector(DefaultConfig(testSR))
	offFrames, offStrums := feedStrums(off, x, 480)
	if len(offStrums) != 0 {
		t.Errorf("got %d strums with Config.Strums false, want none", len(offStrums))
	}

	on := NewDetector(strumConfig())
	onFrames, onStrums := feedStrums(on, x, 480)
	if len(onStrums) == 0 {
		t.Fatal("no strums with Config.Strums true")
	}
	if len(offFrames) != len(onFrames) {
		t.Fatalf("frame counts differ: %d without strums, %d with", len(offFrames), len(onFrames))
	}
	for i := range offFrames {
		if offFrames[i] != onFrames[i] {
			t.Fatalf("frame %d differs: %+v without strums, %+v with", i, offFrames[i], onFrames[i])
		}
	}
}

// TestStrumsEmptyWithoutOnsets: silence produces no strums, and Strums()
// only ever reports what the LAST Process call completed.
func TestStrumsEmptyWithoutOnsets(t *testing.T) {
	d := NewDetector(strumConfig())
	_, strums := feedStrums(d, silence(1.0), 480)
	if len(strums) != 0 {
		t.Errorf("got %d strums from silence, want none", len(strums))
	}

	x := append(silence(0.2), ksNote(45, 0.5)...)
	d = NewDetector(strumConfig())
	var completing int
	for off := 0; off < len(x); off += 480 {
		end := off + 480
		if end > len(x) {
			end = len(x)
		}
		d.Process(x[off:end])
		if len(d.Strums()) > 0 {
			completing++
		}
	}
	if completing != 1 {
		t.Errorf("%d Process calls reported a strum, want exactly 1", completing)
	}
	// The last call reported nothing, so Strums() is empty again.
	if got := len(d.Strums()); got != 0 {
		t.Errorf("Strums() = %d after a call that completed none, want 0", got)
	}
}

// TestStrumTruncatedByNextOnset: with a span longer than the onset
// refractory period, a second attack arriving mid-span closes the first
// Strum early rather than merging the two attacks into one.
func TestStrumTruncatedByNextOnset(t *testing.T) {
	lead := silence(0.2)
	// 80 ms apart: inside a 12-hop (120 ms) span but outside the onset
	// refractory period. The first note is quiet so the second attack
	// still clears the onset detector's rise threshold over it.
	first := scale(ksNote(45, 0.08), 0.1)
	x := append(append(lead, first...), ksNote(52, 0.4)...)

	cfg := strumConfig()
	cfg.StrumWindowHops = 12
	d := NewDetector(cfg)
	_, strums := feedStrums(d, x, 480)
	if len(strums) != 2 {
		t.Fatalf("got %d strums, want 2 (the first truncated by the second attack)", len(strums))
	}
	if strums[0].Frame >= strums[1].Frame {
		t.Errorf("strums out of order: %d then %d", strums[0].Frame, strums[1].Frame)
	}
	if got, want := chromaRank(strums[0].Chroma)[0], 9; got != want { // A
		t.Errorf("truncated strum's strongest class %s, want A;%s",
			pitchClassNames[got], formatChroma(strums[0].Chroma))
	}
}

// TestStrumsTinyWindowDoesNotPanic: the chroma tables cope with a window
// so short that barely any bin falls in the folded range — the same
// degenerate config TestDetectorTinyWindowDoesNotPanic pins for the f0
// path.
func TestStrumsTinyWindowDoesNotPanic(t *testing.T) {
	cfg := strumConfig()
	cfg.Window = 16
	cfg.MaxHz = 20000
	d := NewDetector(cfg)
	if frames, _ := feedStrums(d, append(silence(0.05), ksNote(45, 0.2)...), 480); len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
}

// TestDetectorProcessDoesNotAllocateWithStrums is the strum-path twin of
// TestDetectorProcessDoesNotAllocate: chroma folding, accumulation, and
// Strum emission all run out of preallocated state, so the realtime path
// stays allocation-free with Phase 4 enabled.
func TestDetectorProcessDoesNotAllocateWithStrums(t *testing.T) {
	// Repeated plucks, so the measured calls keep hitting onsets, chroma
	// folding, and strum completion rather than steady-state sustain.
	var x []float32
	for i := 0; i < 8; i++ {
		x = append(x, ksNote(40+i, 0.1)...)
	}
	d := NewDetector(strumConfig())
	chunks := make([][]float32, 0, len(x)/480+1)
	for off := 0; off+480 <= len(x); off += 480 {
		chunks = append(chunks, x[off:off+480])
	}
	strums := 0
	feed := func() {
		for _, c := range chunks {
			d.Process(c)
			strums += len(d.Strums())
		}
	}
	feed() // warmup: grows the reused frame and strum slices once
	if strums == 0 {
		t.Fatal("no strums in the measured signal; the test would prove nothing")
	}
	if allocs := testing.AllocsPerRun(20, feed); allocs != 0 {
		t.Errorf("Process allocates %v times per pass with strums enabled, want 0", allocs)
	}
}

// BenchmarkProcess and BenchmarkProcessStrums bound the cost of Phase 4:
// with Config.Strums false the chroma tables are never built and no fold
// runs, so the two should be indistinguishable outside strum spans.
func BenchmarkProcess(b *testing.B) {
	benchmarkProcess(b, DefaultConfig(testSR))
}

func BenchmarkProcessStrums(b *testing.B) {
	benchmarkProcess(b, strumConfig())
}

func benchmarkProcess(b *testing.B, cfg Config) {
	var x []float32
	for i := 0; i < 8; i++ {
		x = append(x, ksNote(40+i, 0.1)...)
	}
	d := NewDetector(cfg)
	chunks := make([][]float32, 0, len(x)/480+1)
	for off := 0; off+480 <= len(x); off += 480 {
		chunks = append(chunks, x[off:off+480])
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Process(chunks[i%len(chunks)])
	}
}
