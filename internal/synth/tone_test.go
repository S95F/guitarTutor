package synth

// Tests for what the pluck voice SOUNDS like, as opposed to whether it is
// in tune and does not clip (karplus_test.go covers those).
//
// Every assertion here is a property with a physical reason, not a
// snapshot of the current tuning: how long a note rings is stated in
// seconds and compared against the range a guitar occupies, the pick
// filter is checked by the notch it puts in the spectrum, and velocity is
// checked by the direction it moves the brightness. Retuning the voice by
// ear should not have to touch this file; changing what the voice IS
// should.

import (
	"math"
	"math/cmplx"
	"testing"
)

// decayTime measures the -60 dB time of a rendered note: the point where
// its envelope has fallen a thousandfold from its peak, extrapolated from
// the slope so a note that has not finished decaying still gives an
// answer.
func decayTime(x []float32, sampleRate int) float64 {
	const win = 2048
	var peakE, lastE float64
	peakAt, lastAt := 0, 0
	for off := 0; off+win <= len(x); off += win {
		var e float64
		for _, s := range x[off : off+win] {
			e += float64(s) * float64(s)
		}
		e = math.Sqrt(e / win)
		if e > peakE {
			peakE, peakAt = e, off
		}
		if e > 0 {
			lastE, lastAt = e, off
		}
	}
	if peakE <= 0 || lastE <= 0 || lastAt <= peakAt {
		return math.Inf(1)
	}
	// dB per second between the peak and the last measured window.
	span := float64(lastAt-peakAt) / float64(sampleRate)
	drop := 20 * math.Log10(peakE/lastE)
	if drop <= 0 {
		return math.Inf(1)
	}
	return 60 * span / drop
}

// TestPluckDecayIsGuitarLike pins the thing that was most wrong with this
// voice: a constant loop gain per PERIOD gave every note the same number
// of cycles of ring, so the low E rang for twenty seconds and the high E
// for five. A guitar is nothing like that — the whole range lands between
// about five seconds and two, and the bass rings LONGER than the treble
// but not by an order of magnitude.
func TestPluckDecayIsGuitarLike(t *testing.T) {
	const sr = 48000
	for _, tt := range []struct {
		name          string
		key           int
		lowSec, hiSec float64
		renderSeconds float64
	}{
		{"low E", 40, 3.0, 7.0, 3.0},
		{"A", 45, 2.5, 6.5, 3.0},
		{"open high E", 64, 1.8, 5.0, 2.5},
		{"high E, 12th fret", 76, 1.2, 3.5, 2.0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := NewPluck(sr, 25)
			v.NoteOn(tt.key, 0.9)
			l, r := renderFrames(v, int(tt.renderSeconds*sr), 512)
			got := decayTime(monoSum(l, r), sr)
			if got < tt.lowSec || got > tt.hiSec {
				t.Errorf("key %d rings for %.1f s (-60 dB); a guitar's is %.1f to %.1f",
					tt.key, got, tt.lowSec, tt.hiSec)
			}
		})
	}
}

// TestPluckBassRingsLongerThanTreble is the ordering the decay times have
// to keep whatever they are individually tuned to.
func TestPluckBassRingsLongerThanTreble(t *testing.T) {
	const sr = 48000
	measure := func(key int) float64 {
		v := NewPluck(sr, 25)
		v.NoteOn(key, 0.9)
		l, r := renderFrames(v, 3*sr, 512)
		return decayTime(monoSum(l, r), sr)
	}
	low, high := measure(40), measure(76)
	if low <= high {
		t.Errorf("the low E rings %.1f s and the top of the neck %.1f s; the bass has to ring longer", low, high)
	}
}

// TestPluckReleaseIsTheSameEverywhere checks that lifting a finger damps
// a high note and a low one at about the same rate. The old per-period
// release damped the top of the neck four times faster than the bottom,
// which is the opposite of what a hand does.
func TestPluckReleaseIsTheSameEverywhere(t *testing.T) {
	const sr = 48000
	measure := func(key int) float64 {
		p := NewPluck(sr, 25).(*pluck)
		p.NoteOn(key, 0.9)
		renderFrames(p, sr/4, 512)
		p.NoteOff(key)
		l, r := renderFrames(p, sr, 512)
		return decayTime(monoSum(l, r), sr)
	}
	low, high := measure(40), measure(76)
	if ratio := math.Max(low, high) / math.Min(low, high); ratio > 1.6 {
		t.Errorf("release takes %.2f s low and %.2f s high, a factor of %.1f apart; a lifting finger is one gesture",
			low, high, ratio)
	}
}

// spectrumAt returns the magnitude of a rendered signal at freq, by
// correlating against a complex exponential over a whole number of cycles
// — a one-bin DFT, which is all these tests need and needs no FFT.
func spectrumAt(x []float32, sampleRate int, freq float64) float64 {
	cycles := math.Floor(float64(len(x)) * freq / float64(sampleRate))
	if cycles < 1 {
		return 0
	}
	n := int(cycles * float64(sampleRate) / freq)
	if n > len(x) {
		n = len(x)
	}
	var acc complex128
	w := 2 * math.Pi * freq / float64(sampleRate)
	for i := 0; i < n; i++ {
		acc += complex(float64(x[i]), 0) * cmplx.Exp(complex(0, -w*float64(i)))
	}
	return cmplx.Abs(acc) / float64(n)
}

// TestPluckPickPositionNotchesItsHarmonic is the pick filter's whole
// reason for existing: a string struck a fraction b of its length from
// the bridge cannot sound the harmonic with a node there. With b at about
// a sixth, that is the sixth harmonic, and it has to be markedly weaker
// than the harmonics either side of it — which is what makes a plucked
// note sound plucked rather than filtered.
func TestPluckPickPositionNotchesItsHarmonic(t *testing.T) {
	const sr = 48000
	const key = 45 // open A, 110 Hz: enough harmonics under Nyquist to see
	v := NewPluck(sr, 25)
	v.NoteOn(key, 1.0)
	// The first tenth of a second, where the excitation's own spectrum is
	// still what is sounding rather than what the loop's lowpass has left.
	l, r := renderFrames(v, sr/10, 512)
	x := monoSum(l, r)
	f0 := keyFreq(key)

	notched := int(math.Round(1 / pluckPickPos))
	at := func(n int) float64 { return spectrumAt(x, sr, f0*float64(n)) }
	neighbours := math.Max(at(notched-1), at(notched+1))
	if neighbours <= 0 {
		t.Fatal("the harmonics either side of the notch are silent")
	}
	depth := 20 * math.Log10(at(notched)/neighbours)
	if depth > -4 {
		t.Errorf("harmonic %d is %.1f dB against its neighbours; the pick position should notch it by several dB",
			notched, depth)
	}
}

// TestPluckVelocityChangesBrightness checks that a harder pick is not
// just a louder one. Comparing the two at the SAME level is the point:
// what has to move is the balance between the top of the spectrum and the
// bottom, not the whole thing together.
func TestPluckVelocityChangesBrightness(t *testing.T) {
	const sr = 48000
	const key = 45
	tilt := func(velocity float64) float64 {
		v := NewPluck(sr, 25)
		v.NoteOn(key, velocity)
		l, r := renderFrames(v, sr/20, 512)
		x := monoSum(l, r)
		f0 := keyFreq(key)
		var low, high float64
		for n := 1; n <= 4; n++ {
			low += spectrumAt(x, sr, f0*float64(n))
		}
		for n := 9; n <= 16; n++ {
			high += spectrumAt(x, sr, f0*float64(n))
		}
		if low <= 0 {
			t.Fatal("the note has no low harmonics")
		}
		return high / low
	}
	soft, hard := tilt(0.25), tilt(1.0)
	if hard <= soft {
		t.Errorf("a hard pick's high-to-low balance is %.4f and a soft one's %.4f; harder has to be brighter", hard, soft)
	}
}

// TestPluckLevelIsIndependentOfTheFilters guards the calibration the rest
// of the pipeline depends on. The excitation's filters change the tone of
// a note; they must not change how loud it is, because the input gate and
// the onset detector in internal/pitch are both tuned against what this
// voice puts out at a given velocity.
func TestPluckLevelIsIndependentOfTheFilters(t *testing.T) {
	const sr = 48000
	for _, key := range []int{40, 45, 52, 64, 76} {
		v := NewPluck(sr, 25)
		v.NoteOn(key, 1.0)
		l, r := renderFrames(v, sr/20, 512)
		p := peak(monoSum(l, r))
		// The burst is scaled by pluckExcite at velocity 1, and the two
		// channels sum back to about one whole note; the bound is loose
		// because the loop's own filter and the pan law both touch it. What
		// it catches is a filter that has quietly multiplied the level by
		// several times, which is what an energy-preserving rescale did.
		if p < 0.1 || p > 1.0 {
			t.Errorf("key %d peaks at %.3f; a velocity-1 note should sit well inside the headroom", key, p)
		}
	}
}
