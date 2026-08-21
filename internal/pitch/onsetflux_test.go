package pitch

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func vibrato(freq, cents, rate, amp, seconds float64) []float32 {
	n := int(seconds * testSR)
	x := make([]float32, n)
	phase := 0.0
	for i := range x {
		f := freq * math.Pow(2, cents/1200*math.Sin(2*math.Pi*rate*float64(i)/testSR))
		phase += 2 * math.Pi * f / testSR
		x[i] = float32(amp * math.Sin(phase))
	}
	return x
}

func countOnsets(cfg Config, x []float32) int {
	d := NewDetector(cfg)
	n := 0
	for _, f := range feedAll(d, x, 480) {
		if f.Onset {
			n++
		}
	}
	return n
}

func TestOnsetFluxNoSpuriousTriggers(t *testing.T) {
	tests := []struct {
		name string
		x    []float32
	}{
		{"single note decaying", append(silence(0.1), ksNote(45, 1.6)...)},
		{"vibrato +-30 cents at 5 Hz", append(silence(0.1), vibrato(220, 30, 5, 0.3, 1.4)...)},
		{"vibrato +-60 cents at 6 Hz", append(silence(0.1), vibrato(196, 60, 6, 0.3, 1.4)...)},
		{"whole-tone bend", append(silence(0.1), chirp(220, 247, 0.3, 1.0)...)},
		{"chord ringing", renderStrumSeq(2.2, strumEvent{0.2, chordCorpus[2].keys, 0.012})},
		{"chord ringing, 20 ms sweep", renderStrumSeq(2.2, strumEvent{0.2, chordCorpus[0].keys, 0.020})},
	}
	cfg := strumConfig()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countOnsets(cfg, tt.x); got != 1 {
				t.Errorf("got %d onsets, want exactly 1 (the attack)", got)
			}
		})
	}
}

func TestOnsetFluxSeparation(t *testing.T) {
	cfg := strumConfig()
	peak := func(x []float32, from, to float64) float64 {
		d := NewDetector(cfg)
		lo, hi := int64(from*testSR), int64(to*testSR)
		best := 0.0
		for off := 0; off+480 <= len(x); off += 480 {
			for _, f := range d.Process(x[off : off+480]) {
				if f.Frame >= lo && f.Frame <= hi && d.lastFlux > best {
					best = d.lastFlux
				}
			}
		}
		return best
	}

	minChange, minName := math.Inf(1), ""
	for _, sp := range []float64{0, 0.005, 0.012, 0.020} {
		for _, pr := range changePairs {
			a, b := chordCorpus[pr[0]], chordCorpus[pr[1]]
			x := renderStrumSeq(2.0, strumEvent{0.2, a.keys, sp}, strumEvent{0.8, b.keys, sp})
			if v := peak(x, 0.79, 0.92); v < minChange {
				minChange, minName = v, fmt.Sprintf("%s->%s at %.0f ms", a.name, b.name, sp*1000)
			}
		}
	}

	maxHold, maxName := 0.0, ""
	for _, sh := range chordCorpus {
		for _, sp := range strumSpreads {
			x := renderStrumSeq(2.2, strumEvent{0.2, sh.keys, sp.spread})
			if v := peak(x, 0.45, 2.1); v > maxHold {
				maxHold, maxName = v, sh.name+"/"+sp.name
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nquietest chord change: %.3f (%s)\n", minChange, minName)
	fmt.Fprintf(&b, "loudest sustain:       %.3f (%s)\n", maxHold, maxName)
	fmt.Fprintf(&b, "threshold:             %.3f\n", onsetFluxThreshold)
	t.Log(b.String())

	if minChange <= onsetFluxThreshold {
		t.Errorf("quietest chord change flux %.3f is at or below the threshold %.3f (%s)",
			minChange, onsetFluxThreshold, minName)
	}
	if maxHold >= onsetFluxThreshold {
		t.Errorf("loudest sustain flux %.3f reaches the threshold %.3f (%s)",
			maxHold, onsetFluxThreshold, maxName)
	}
}

func TestStrumLatencyBudget(t *testing.T) {
	cfg := strumConfig()
	lead := silence(0.3)
	attack := len(lead)
	x := append(lead, ksStrum(1.0, 0.012, chordCorpus[2].keys...)...)

	d := NewDetector(cfg)
	fed, reported := 0, -1
	for off := 0; off < len(x); off += 480 {
		end := off + 480
		if end > len(x) {
			end = len(x)
		}
		d.Process(x[off:end])
		fed = end
		if len(d.Strums()) > 0 && reported < 0 {
			reported = fed
		}
	}
	if reported < 0 {
		t.Fatal("no strum reported")
	}
	latency := float64(reported-attack) / testSR
	want := float64(strumSkipHops+cfg.StrumWindowHops) * float64(cfg.Hop) / testSR
	t.Logf("Strum reported %.0f ms after the attack (span is %.0f ms)", latency*1000, want*1000)
	if latency > 0.250 {
		t.Errorf("Strum latency %.0f ms exceeds the 250 ms budget", latency*1000)
	}
}

func TestStrumFallbackWhenTruncatedEarly(t *testing.T) {

	lead := silence(0.2)
	first := scale(ksNote(45, 0.05), 0.15)
	x := append(append(lead, first...), ksNote(52, 0.5)...)

	d := NewDetector(strumConfig())
	_, strums := feedStrums(d, x, 480)
	if len(strums) != 2 {
		t.Fatalf("got %d strums, want 2 (the first truncated by the second attack)", len(strums))
	}
	var sum float32
	for _, v := range strums[0].Chroma {
		sum += v
	}
	if sum == 0 {
		t.Fatal("truncated strum has an all-zero chroma: the scorer would call every note a Miss")
	}
	if got, want := chromaRank(strums[0].Chroma)[0], 9; got != want {
		t.Errorf("truncated strum's strongest class %s, want A;%s",
			pitchClassNames[got], formatChroma(strums[0].Chroma))
	}
}
