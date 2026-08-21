package pitchml

import (
	"errors"
	"math"
	"testing"
)

func responseDB(taps []float64, f, sampleRate float64) float64 {
	var re, im float64
	for n, h := range taps {
		w := 2 * math.Pi * f / sampleRate * float64(n)
		re += h * math.Cos(w)
		im -= h * math.Sin(w)
	}
	return 20 * math.Log10(math.Hypot(re, im))
}

func TestAntiAliasTapsAreSymmetricAndUnityGain(t *testing.T) {
	for _, rate := range []int{32000, 48000, 96000} {
		taps := antiAliasTaps(rate)
		if len(taps)%2 == 0 {
			t.Errorf("%d Hz: %d taps, want an odd count for a whole-sample group delay", rate, len(taps))
		}
		sum := 0.0
		for i, h := range taps {
			sum += h

			if mirror := taps[len(taps)-1-i]; math.Abs(h-mirror) > 1e-15 {
				t.Fatalf("%d Hz: taps[%d]=%g != taps[%d]=%g, filter is not symmetric", rate, i, h, len(taps)-1-i, mirror)
			}
		}
		if math.Abs(sum-1) > 1e-12 {
			t.Errorf("%d Hz: sum(taps) = %.15f, want 1 (unity DC gain)", rate, sum)
		}
	}
}

func TestAntiAliasFrequencyResponse(t *testing.T) {
	const rate = 48000
	taps := antiAliasTaps(rate)
	if len(taps) != 81 {
		t.Errorf("taps = %d, want 81 at 48 kHz (3.3*Fs/2000 rounded up to odd)", len(taps))
	}

	for _, f := range []float64{0, 82.4, 110, 440, 1000, 2000, 3000, 4000, 5000} {
		if db := responseDB(taps, f, rate); math.Abs(db) > 0.1 {
			t.Errorf("passband: %.1f Hz is %.2f dB, want within 0.1 dB", f, db)
		}
	}

	if db := responseDB(taps, antiAliasCutoffHz, rate); math.Abs(db+6) > 0.5 {
		t.Errorf("cutoff: %.0f Hz is %.2f dB, want about -6 dB", antiAliasCutoffHz, db)
	}

	for f := 7000.0; f <= rate/2; f += 250 {
		if db := responseDB(taps, f, rate); db > -40 {
			t.Errorf("stopband: %.0f Hz is only %.2f dB down, want at least 40", f, db)
		}
	}
}

func TestAntiAliasTapCountIsClamped(t *testing.T) {

	if n := antiAliasTapCount(1_000_000); n != maxAntiAliasTaps {
		t.Errorf("antiAliasTapCount(1 MHz) = %d, want the %d cap", n, maxAntiAliasTaps)
	}
	if n := antiAliasTapCount(1000); n != minAntiAliasTaps {
		t.Errorf("antiAliasTapCount(1 kHz) = %d, want the %d floor", n, minAntiAliasTaps)
	}
}

func TestNewDecimatorRejectsNonMultipleRates(t *testing.T) {
	for _, rate := range []int{0, 8000, 44100, 22050} {
		if _, err := newDecimator(rate); !errors.Is(err, ErrSampleRate) {
			t.Errorf("newDecimator(%d) err = %v, want ErrSampleRate", rate, err)
		}
	}
	d, err := newDecimator(ModelSampleRate)
	if err != nil {
		t.Fatalf("newDecimator(16000): %v", err)
	}
	if d.ratio != 1 || d.taps != nil {

		t.Errorf("16 kHz decimator = %+v, want ratio 1 and no filter", d)
	}
}

func sine(n int, f, rate float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(math.Sin(2 * math.Pi * f * float64(i) / rate))
	}
	return out
}

func middleRMS(x []float32) float64 {
	lo, hi := len(x)/4, 3*len(x)/4
	sum := 0.0
	for _, v := range x[lo:hi] {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(hi-lo))
}

func TestDecimatePassesGuitarBandAndKillsAliases(t *testing.T) {
	const rate = 48000
	d, err := newDecimator(rate)
	if err != nil {
		t.Fatalf("newDecimator: %v", err)
	}
	const n = 4800
	dst := make([]float32, d.outLen(n))

	for _, f := range []float64{82.4, 440, 1318.5} {
		out := d.decimate(sine(n, f, rate), dst)
		if got := middleRMS(out); math.Abs(got-math.Sqrt2/2) > 0.02 {
			t.Errorf("%.1f Hz: output RMS %.4f, want ~0.707", f, got)
		}
	}

	for _, f := range []float64{10000, 17000, 20000} {
		out := d.decimate(sine(n, f, rate), dst)
		if got := middleRMS(out); got > 0.01 {
			t.Errorf("%.0f Hz: output RMS %.4f, want < 0.01 (alias rejected)", f, got)
		}
	}
}

func TestDecimateIsTimeAligned(t *testing.T) {

	const rate = 48000
	d, _ := newDecimator(rate)
	const n = 2400
	in := sine(n, 200, rate)
	out := d.decimate(in, make([]float32, d.outLen(n)))
	for k := 200; k < len(out)-200; k++ {
		want := float64(in[k*d.ratio])
		if diff := math.Abs(float64(out[k]) - want); diff > 0.01 {
			t.Fatalf("out[%d] = %.4f, want %.4f (input sample %d)", k, out[k], want, k*d.ratio)
		}
	}
}

func TestDecimateHandlesShortAndOversizedBuffers(t *testing.T) {
	d, _ := newDecimator(48000)

	if out := d.decimate(make([]float32, 2), make([]float32, 8)); len(out) != 0 {
		t.Errorf("2 samples in gave %d out, want 0", len(out))
	}

	if out := d.decimate(make([]float32, 300), make([]float32, 10)); len(out) != 10 {
		t.Errorf("truncated destination gave %d samples, want 10", len(out))
	}

	in := sine(64, 300, 48000)
	if out := d.decimate(in, make([]float32, 64)); len(out) != 21 {
		t.Errorf("64 samples in gave %d out, want 21", len(out))
	}
}

func TestDecimatePassthroughAtModelRate(t *testing.T) {
	d, _ := newDecimator(ModelSampleRate)
	in := sine(100, 440, ModelSampleRate)
	out := d.decimate(in, make([]float32, 100))
	if len(out) != len(in) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("out[%d] = %v, want the input sample %v unchanged", i, out[i], in[i])
		}
	}
}

func TestDecimateDoesNotAllocate(t *testing.T) {
	d, _ := newDecimator(48000)
	in := sine(2048, 440, 48000)
	dst := make([]float32, d.outLen(len(in)))
	if n := testing.AllocsPerRun(100, func() { d.decimate(in, dst) }); n != 0 {
		t.Errorf("decimate allocated %v times per call, want 0", n)
	}
}
