package pitch

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/S95F/guitarTutor/internal/synth"
)

// testSR is the pipeline's standard rate; all tests run at it.
const testSR = 48000

// sine synthesizes seconds of a pure sine at freq Hz and amplitude amp.
func sine(freq, amp, seconds float64) []float32 {
	n := int(seconds * testSR)
	x := make([]float32, n)
	w := 2 * math.Pi * freq / testSR
	for i := range x {
		x[i] = float32(amp * math.Sin(w*float64(i)))
	}
	return x
}

// chirp synthesizes a phase-continuous linear frequency sweep from f0 to
// f1 Hz over seconds.
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

// harmonicMix synthesizes a fundamental plus its second harmonic with the
// given amplitudes — the octave-guard test signal.
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

// whiteNoise synthesizes seconds of uniform white noise scaled to the
// given RMS level (linear, e.g. 3.16e-4 for -70 dBFS), from a fixed-seed
// xorshift64 so runs are reproducible.
func whiteNoise(rmsLevel, seconds float64) []float32 {
	n := int(seconds * testSR)
	x := make([]float32, n)
	rng := uint64(0x9E3779B97F4A7C15)
	// Uniform noise in [-a, a] has RMS a/sqrt(3).
	a := rmsLevel * math.Sqrt(3)
	for i := range x {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		u := float64(int32(uint32(rng>>32))) / (1 << 31) // [-1, 1)
		x[i] = float32(a * u)
	}
	return x
}

// ksVoice returns a fresh Karplus-Strong pluck voice — the repo's own
// guitar-like synthesizer, used to build test signals without recording.
func ksVoice() synth.Voice {
	return synth.NewPluck(testSR, 25)
}

// ksRender renders seconds of output from v mixed down to mono (left +
// right), appending to dst.
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

// ksNote renders a single freshly-plucked KS note at key for seconds.
func ksNote(key int, seconds float64) []float32 {
	v := ksVoice()
	v.NoteOn(key, 0.8)
	return ksRender(v, seconds, nil)
}

// silence returns seconds of zeros.
func silence(seconds float64) []float32 {
	return make([]float32, int(seconds*testSR))
}

// mix sums signals sample-wise into a new buffer as long as the longest —
// several strings sounding at once.
func mix(sigs ...[]float32) []float32 {
	n := 0
	for _, s := range sigs {
		if len(s) > n {
			n = len(s)
		}
	}
	out := make([]float32, n)
	for _, s := range sigs {
		for i, v := range s {
			out[i] += v
		}
	}
	return out
}

// scale returns x with every sample multiplied by g.
func scale(x []float32, g float32) []float32 {
	out := make([]float32, len(x))
	for i, v := range x {
		out[i] = v * g
	}
	return out
}

// ksChord renders keys plucked simultaneously, mixed to mono.
//
// It renders them from ONE pluck voice (see ksStrum). The obvious
// alternative — one ksNote per key, summed — is what this used to do, and
// it lies: every pluck voice seeds its excitation noise identically, so
// N strings came out as N perfectly correlated copies of the same noise
// burst. The interference between them is not something a guitar can
// produce, and it moved the chroma enough to matter: on E2/B2/G#3 the
// correlated mix ranked F# (a harmonic of E) 0.09 ABOVE the played G#,
// while the same chord from independent excitations puts G# on top with a
// 0.30 margin over the strongest unsounded class.
func ksChord(seconds float64, keys ...int) []float32 {
	return ksStrum(seconds, 0, keys...)
}

// strumConfig is the default config with Phase 4 strum emission on.
func strumConfig() Config {
	cfg := DefaultConfig(testSR)
	cfg.Strums = true
	return cfg
}

// feedStrums drives d with x in fixed-size chunks and returns copies of
// every emitted Frame and Strum (both slices are reused by the detector).
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

// chromaRank returns the pitch classes of c ordered by descending energy.
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

// pitchClassNames labels chroma bins in test failures.
var pitchClassNames = [PitchClasses]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// formatChroma renders a Chroma for a failure message.
func formatChroma(c Chroma) string {
	var b strings.Builder
	for i, v := range c {
		fmt.Fprintf(&b, " %s=%.2f", pitchClassNames[i], v)
	}
	return b.String()
}

// feedAll drives d with x in fixed-size chunks and returns copies of every
// emitted frame (Process reuses its returned slice).
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

// voicedFrames returns the subset of frames with a trusted f0.
func voicedFrames(frames []Frame) []Frame {
	var v []Frame
	for _, f := range frames {
		if f.F0 > 0 {
			v = append(v, f)
		}
	}
	return v
}

// medianF0Of returns the median F0 of the voiced frames, failing the test
// when fewer than min are voiced.
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

// cents returns the musical interval from a to b in cents.
func cents(a, b float64) float64 {
	return 1200 * math.Log2(b/a)
}

// keyToFreq is the inverse of nearestKey (A4 = 69 = 440 Hz).
func keyToFreq(key int) float64 {
	return 440 * math.Pow(2, float64(key-69)/12)
}

// nearestKey quantizes a frequency to the nearest MIDI key.
func nearestKey(f0 float64) int {
	return int(math.Round(midiPitch(f0)))
}
