package pitch

import (
	"math"
	"testing"
)

func singleStrum(t *testing.T, x []float32) Strum {
	t.Helper()
	d := NewDetector(strumConfig())
	_, strums := feedStrums(d, x, 480)
	if len(strums) != 1 {
		t.Fatalf("got %d strums, want exactly 1", len(strums))
	}
	return strums[0]
}

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
			const wantClass = 9
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

func TestChromaTriadSoundedClasses(t *testing.T) {
	sounded := map[int]bool{4: true, 11: true}
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
		first := ksNote(45, 1.0)
		x := append(append(lead, first...), ksNote(52, 0.5)...)
		attacks := []int64{int64(len(lead)), int64(len(lead) + len(first))}

		d := NewDetector(strumConfig())
		_, strums := feedStrums(d, x, 480)
		if len(strums) != 2 {
			t.Fatalf("got %d strums from two plucks, want 2", len(strums))
		}

		for i, s := range strums {
			if diff := attacks[i] - s.Frame; diff < 0 || diff > int64(d.cfg.Window/2)+int64(d.cfg.Hop) {
				t.Errorf("strum %d at %d, attack at %d: %+d samples off",
					i, s.Frame, attacks[i], -diff)
			}
		}
		if got, want := chromaRank(strums[0].Chroma)[0], 9; got != want {
			t.Errorf("first strum's strongest class %s, want A;%s",
				pitchClassNames[got], formatChroma(strums[0].Chroma))
		}
		if got, want := chromaRank(strums[1].Chroma)[0], 4; got != want {
			t.Errorf("second strum's strongest class %s, want E;%s",
				pitchClassNames[got], formatChroma(strums[1].Chroma))
		}
	})
}

func TestStrumChordVersusSingleNote(t *testing.T) {

	chord := singleStrum(t, append(silence(0.2), ksChord(0.6, 40, 47, 56)...))
	single := singleStrum(t, append(silence(0.2), ksNote(40, 0.6)...))

	want := []int{4, 11, 8}
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

	if got := len(d.Strums()); got != 0 {
		t.Errorf("Strums() = %d after a call that completed none, want 0", got)
	}
}

func TestStrumTruncatedByNextOnset(t *testing.T) {
	lead := silence(0.2)

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
	if got, want := chromaRank(strums[0].Chroma)[0], 9; got != want {
		t.Errorf("truncated strum's strongest class %s, want A;%s",
			pitchClassNames[got], formatChroma(strums[0].Chroma))
	}
}

func TestStrumsTinyWindowDoesNotPanic(t *testing.T) {
	cfg := strumConfig()
	cfg.Window = 16
	cfg.MaxHz = 20000
	d := NewDetector(cfg)
	if frames, _ := feedStrums(d, append(silence(0.05), ksNote(45, 0.2)...), 480); len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
}

func TestDetectorProcessDoesNotAllocateWithStrums(t *testing.T) {

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
	feed()
	if strums == 0 {
		t.Fatal("no strums in the measured signal; the test would prove nothing")
	}
	if allocs := testing.AllocsPerRun(20, feed); allocs != 0 {
		t.Errorf("Process allocates %v times per pass with strums enabled, want 0", allocs)
	}
}

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
