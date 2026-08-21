package pitch

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/synth"
)

const testSR = 48000

func sine(freq, amp, seconds float64) []float32 {
	n := int(seconds * testSR)
	x := make([]float32, n)
	w := 2 * math.Pi * freq / testSR
	for i := range x {
		x[i] = float32(amp * math.Sin(w*float64(i)))
	}
	return x
}

func chirp(f0, f1, amp, seconds float64) []float32 {
	n := int(seconds * testSR)
	x := make([]float32, n)
	phase := 0.0
	for i := range x {
		f := f0 + (f1-f0)*float64(i)/float64(n)
		x[i] = float32(amp * math.Sin(phase))
		phase += 2 * math.Pi * f / testSR
	}
	return x
}

func harmonicMix(freq, ampF, amp2F, seconds float64) []float32 {
	n := int(seconds * testSR)
	x := make([]float32, n)
	w := 2 * math.Pi * freq / testSR
	for i := range x {
		ti := float64(i)
		x[i] = float32(ampF*math.Sin(w*ti) + amp2F*math.Sin(2*w*ti))
	}
	return x
}

func whiteNoise(rmsLevel, seconds float64) []float32 {
	n := int(seconds * testSR)
	x := make([]float32, n)
	rng := uint64(0x9E3779B97F4A7C15)

	a := rmsLevel * math.Sqrt(3)
	for i := range x {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		u := float64(int32(uint32(rng>>32))) / (1 << 31)
		x[i] = float32(a * u)
	}
	return x
}

func ksVoice() synth.Voice {
	return synth.NewPluck(testSR, 25)
}

func ksRender(v synth.Voice, seconds float64, dst []float32) []float32 {
	n := int(seconds * testSR)
	const chunk = 512
	l := make([]float32, chunk)
	r := make([]float32, chunk)
	for off := 0; off < n; off += chunk {
		c := chunk
		if off+c > n {
			c = n - off
		}
		for i := 0; i < c; i++ {
			l[i], r[i] = 0, 0
		}
		v.Render(l[:c], r[:c])
		for i := 0; i < c; i++ {
			dst = append(dst, l[i]+r[i])
		}
	}
	return dst
}

func ksNote(key int, seconds float64) []float32 {
	v := ksVoice()
	v.NoteOn(key, 0.8)
	return ksRender(v, seconds, nil)
}

func silence(seconds float64) []float32 {
	return make([]float32, int(seconds*testSR))
}

func scale(x []float32, g float32) []float32 {
	out := make([]float32, len(x))
	for i, v := range x {
		out[i] = v * g
	}
	return out
}

func ksChord(seconds float64, keys ...int) []float32 {
	return ksStrum(seconds, 0, keys...)
}

func strumConfig() Config {
	cfg := DefaultConfig(testSR)
	cfg.Strums = true
	return cfg
}

func feedStrums(d *Detector, x []float32, chunk int) ([]Frame, []Strum) {
	var frames []Frame
	var strums []Strum
	for off := 0; off < len(x); off += chunk {
		end := off + chunk
		if end > len(x) {
			end = len(x)
		}
		frames = append(frames, d.Process(x[off:end])...)
		strums = append(strums, d.Strums()...)
	}
	return frames, strums
}

func chromaRank(c Chroma) []int {
	order := make([]int, PitchClasses)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case c[a] > c[b]:
			return -1
		case c[a] < c[b]:
			return 1
		}
		return 0
	})
	return order
}

var pitchClassNames = [PitchClasses]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

func formatChroma(c Chroma) string {
	var b strings.Builder
	for i, v := range c {
		fmt.Fprintf(&b, " %s=%.2f", pitchClassNames[i], v)
	}
	return b.String()
}

func feedAll(d *Detector, x []float32, chunk int) []Frame {
	var frames []Frame
	for off := 0; off < len(x); off += chunk {
		end := off + chunk
		if end > len(x) {
			end = len(x)
		}
		frames = append(frames, d.Process(x[off:end])...)
	}
	return frames
}

func voicedFrames(frames []Frame) []Frame {
	var v []Frame
	for _, f := range frames {
		if f.F0 > 0 {
			v = append(v, f)
		}
	}
	return v
}

func medianF0Of(t *testing.T, frames []Frame, min int) float64 {
	t.Helper()
	voiced := voicedFrames(frames)
	if len(voiced) < min {
		t.Fatalf("only %d voiced frames, want >= %d", len(voiced), min)
	}
	f0s := make([]float64, len(voiced))
	for i, f := range voiced {
		f0s[i] = f.F0
	}
	var scratch []float64
	return median(&scratch, f0s)
}

func cents(a, b float64) float64 {
	return 1200 * math.Log2(b/a)
}

func keyToFreq(key int) float64 {
	return 440 * math.Pow(2, float64(key-69)/12)
}

func nearestKey(f0 float64) int {
	return int(math.Round(midiPitch(f0)))
}
