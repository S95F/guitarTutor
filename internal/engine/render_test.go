package engine

import (
	"math"
	"testing"
)

func TestSetTempoScaleRefusesNonFinite(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		e, _ := newFixtureEngine(t, Options{})
		e.SetTempoScale(1.5)
		e.SetTempoScale(bad)
		if got := e.TempoScale(); got != 1.5 {
			t.Errorf("SetTempoScale(%v) changed the scale to %v, want it left at 1.5", bad, got)
		}
		if bpm := e.EffectiveBPM(); math.IsNaN(bpm) || math.IsInf(bpm, 0) {
			t.Errorf("SetTempoScale(%v) made EffectiveBPM %v", bad, bpm)
		}

		e.Play()
		l := make([]float32, 512)
		r := make([]float32, 512)
		e.RenderFrames(l, r)
		for i := range l {
			if f := float64(l[i]); math.IsNaN(f) || math.IsInf(f, 0) {
				t.Fatalf("SetTempoScale(%v): rendered sample %d is %v", bad, i, l[i])
			}
		}
	}
}
