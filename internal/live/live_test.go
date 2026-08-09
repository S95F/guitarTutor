package live

import (
	"math"
	"testing"

	"github.com/S95F/guitarTutor/internal/pitch"
)

const testRate = 48000

// sineTone renders n samples of a sine at hz, amplitude 0.5 (~-9 dBFS
// RMS, comfortably above the detector's noise gate).
func sineTone(hz float64, n int) []float32 {
	buf := make([]float32, n)
	for i := range buf {
		buf[i] = float32(0.5 * math.Sin(2*math.Pi*hz*float64(i)/testRate))
	}
	return buf
}

// analyzeRun drives an analyzer synchronously and records what it
// reported: every closed note and the final consumed clock.
type analyzeRun struct {
	s        *Session
	a        *analyzer
	closed   []pitch.Note
	consumed int64
}

func newAnalyzeRun(ringSize int) *analyzeRun {
	r := &analyzeRun{s: &Session{ringBuf: newRing(ringSize)}}
	r.a = newAnalyzer(r.s, pitch.DefaultConfig(testRate), func(closed []pitch.Note, _ pitch.Note, _ bool, consumed int64) {
		r.closed = append(r.closed, closed...)
		r.consumed = consumed
	})
	return r
}

// feed writes samples through the ring in callback-sized writes,
// draining after each so nothing overflows.
func (r *analyzeRun) feed(samples []float32) {
	const cb = 1024
	for off := 0; off < len(samples); off += cb {
		end := off + cb
		if end > len(samples) {
			end = len(samples)
		}
		r.s.ringBuf.write(samples[off:end])
		r.a.drain()
	}
}

// TestAnalyzeOverflowKeepsDeviceClock reproduces the stall that used to
// desynchronize the scoring clock for good: the ring fills while the
// analysis goroutine is wedged, a second of capture is dropped, and then
// audio resumes. The dropped stretch must still advance the detector's
// clock (fed as silence), so the note that was sounding closes and the
// note played after the stall is stamped on the device clock — not early
// by the cumulative loss.
func TestAnalyzeOverflowKeepsDeviceClock(t *testing.T) {
	const (
		ringSize = 8192  // small ring so the test's "stall" overflows it
		gap      = 48000 // one second of capture lost to the stall
		toneALen = ringSize
		toneBLen = 24000
		tailLen  = 4800 // silence long enough to close the last note
	)
	cfg := pitch.DefaultConfig(testRate)
	toneA := sineTone(440, toneALen) // A4, MIDI 69
	toneB := sineTone(330, toneBLen) // E4, MIDI 64
	tail := make([]float32, tailLen)

	// Overflow run: toneA fills the ring with nobody draining, then the
	// whole gap is dropped on the floor; only then does analysis wake.
	over := newAnalyzeRun(ringSize)
	over.s.ringBuf.write(toneA)
	over.s.ringBuf.write(make([]float32, gap))
	if d := over.s.ringBuf.Dropped(); d != gap {
		t.Fatalf("Dropped = %d after the stall, want %d", d, gap)
	}
	over.a.drain()
	over.feed(toneB)
	over.feed(tail)

	// Control run: the identical device stream with no drops — the gap
	// arrives as real silence through a ring big enough to hold it all.
	ctrl := newAnalyzeRun(1 << 17)
	ctrl.feed(toneA)
	ctrl.feed(make([]float32, gap))
	ctrl.feed(toneB)
	ctrl.feed(tail)

	if len(ctrl.closed) != 2 || ctrl.closed[0].Key != 69 || ctrl.closed[1].Key != 64 {
		t.Fatalf("control run closed %+v, want an A4 then an E4", ctrl.closed)
	}
	if len(over.closed) != 2 {
		t.Fatalf("overflow run closed %+v, want 2 notes like the control run", over.closed)
	}

	// The gap must have closed toneA's note and re-stamped nothing: both
	// notes land where the drop-free control run put them, within a hop.
	hop := int64(cfg.Hop)
	for i := range ctrl.closed {
		want, got := ctrl.closed[i], over.closed[i]
		if got.Key != want.Key {
			t.Errorf("note %d: Key = %d, want %d", i, got.Key, want.Key)
		}
		if d := got.Start - want.Start; d < -hop || d > hop {
			t.Errorf("note %d: Start = %d, want %d +/- one hop (%d)", i, got.Start, want.Start, hop)
		}
		if d := got.End - want.End; d < -hop || d > hop {
			t.Errorf("note %d: End = %d, want %d +/- one hop (%d)", i, got.End, want.End, hop)
		}
	}

	// Direct device-clock check: toneB started at frame toneALen+gap of
	// the device stream; before the fix its note was stamped a full gap
	// early (toneALen + a few hops).
	if min := int64(toneALen + gap - cfg.Hop); over.closed[1].Start < min {
		t.Errorf("post-stall Start = %d, want >= %d: the dropped stretch no longer advanced the clock", over.closed[1].Start, min)
	}

	// And the consumed clock reported to OnNotes counts the loss too.
	total := int64(toneALen + gap + toneBLen + tailLen)
	if over.consumed != total {
		t.Errorf("overflow run consumed = %d, want %d (device clock including the dropped stretch)", over.consumed, total)
	}
	if ctrl.consumed != total {
		t.Errorf("control run consumed = %d, want %d", ctrl.consumed, total)
	}
}
