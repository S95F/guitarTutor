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
	strums   []pitch.Strum
	order    []string // callback order within the run ("strums"/"notes")
	consumed int64
}

func newAnalyzeRun(ringSize int) *analyzeRun {
	cfg := pitch.DefaultConfig(testRate)
	cfg.Strums = true
	r := &analyzeRun{s: &Session{ringBuf: newRing(ringSize)}}
	r.a = newAnalyzer(r.s, cfg,
		func(closed []pitch.Note, _ pitch.Note, _ bool, consumed int64) {
			r.closed = append(r.closed, closed...)
			r.consumed = consumed
			r.order = append(r.order, "notes")
		},
		func(strums []pitch.Strum) {
			r.strums = append(r.strums, strums...)
			r.order = append(r.order, "strums")
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

	// Phase 4: the zeroed splice must not manufacture onsets, or a stall
	// would invent a strum — and a strum is a scoring event now (it can
	// verify a chord or credit a palm mute). It cannot: the detector's
	// onset test needs a hop RMS above max(smoothed, noise floor) times
	// the jump ratio, and a zeroed hop's RMS is 0. Both runs must see the
	// same strums, and neither may stamp one inside the gap.
	if len(over.strums) != len(ctrl.strums) {
		t.Fatalf("overflow run saw %d strums, control run %d: the splice changed the onset stream",
			len(over.strums), len(ctrl.strums))
	}
	for i := range ctrl.strums {
		if d := over.strums[i].Frame - ctrl.strums[i].Frame; d < -hop || d > hop {
			t.Errorf("strum %d: Frame = %d, want %d +/- one hop", i, over.strums[i].Frame, ctrl.strums[i].Frame)
		}
	}
	// Interior of the gap. A Strum is stamped at its onset hop's CENTER,
	// which trails the audio by half a window, so the attack that ends
	// the gap legitimately stamps up to a window BEFORE the gap does —
	// hence the upper bound backs off by one window.
	loGap, hiGap := int64(toneALen), int64(toneALen+gap-cfg.Window)
	for _, st := range over.strums {
		if st.Frame >= loGap && st.Frame < hiGap {
			t.Errorf("strum stamped at %d, inside the silent splice [%d, %d): the gap manufactured an onset",
				st.Frame, loGap, hiGap)
		}
	}
}

// TestAnalyzeDeliversStrums pins the Phase 4 plumbing: an attack produces
// a Strum on OnStrums, stamped near the attack, and delivered BEFORE the
// batch's OnNotes call — wiring drives Scorer.Advance off the consumed
// clock OnNotes carries, so a strum from the same batch has to reach the
// scorer first or its expectations can age out before it is seen.
func TestAnalyzeDeliversStrums(t *testing.T) {
	const lead = 9600 // silence, so the attack is a real rising edge
	r := newAnalyzeRun(1 << 17)
	r.feed(make([]float32, lead))
	if len(r.strums) != 0 {
		t.Fatalf("silence produced %d strums, want 0", len(r.strums))
	}
	r.feed(sineTone(440, 24000))
	r.feed(make([]float32, 4800))

	if len(r.strums) == 0 {
		t.Fatal("the attack produced no strum")
	}
	st := r.strums[0]
	cfg := pitch.DefaultConfig(testRate)
	win := int64(cfg.Window)
	if st.Frame < lead-win || st.Frame > lead+win {
		t.Errorf("first strum at frame %d, want within one window (%d) of the attack at %d", st.Frame, win, lead)
	}
	if st.RMS <= 0 {
		t.Errorf("strum RMS = %v, want the attack's energy", st.RMS)
	}
	var peak float32
	for _, v := range st.Chroma {
		if v > peak {
			peak = v
		}
	}
	if peak <= 0 {
		t.Error("strum chroma is all-zero: no spectral evidence was folded")
	}
	seen := false
	for i, tag := range r.order {
		if tag != "strums" {
			continue
		}
		seen = true
		if i+1 >= len(r.order) || r.order[i+1] != "notes" {
			t.Errorf("callback order %v: a strums call at %d was not immediately followed by its batch's notes call", r.order, i)
		}
	}
	if !seen {
		t.Error("no OnStrums call recorded")
	}
}
