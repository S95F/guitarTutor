package practice

// The money test: prove the whole Phase 2 chain hangs together with no
// microphone in the loop. The fixture riff is parsed from .gtab, rendered
// offline through the real engine and Karplus-Strong synth, and the
// rendered audio is fed straight back through the real pitch detector and
// tracker into the scorer. Output and input share one clock (the audio
// never leaves the process), so LatencyOffsetFrames is 0.
//
// Observed accounting over the fixture's 16 note events (documented from
// the actual run; the numbers are stable because everything is
// deterministic):
//   - The 13 single notes — 8 eighths in bar 1, three notes in bar 2,
//     the lone quarter in bar 3, and bar 4's tied whole note (which
//     score.Events merges into ONE expectation) — all judge Hit, with
//     ErrFrames of roughly +1000..+3000 (the detector needs 2-4 cycles
//     plus tracker hysteresis before a note opens: D4's physics budget).
//   - The bar-3 chord is 3 expectations (E2+B2+E3, keys 40/47/52) and
//     all 3 judge Miss. The design allowed for 1 match + 2 misses
//     (monophonic detector, one note per moment), but in practice MPM
//     cannot lock onto ANY single f0 while all three tones sound: it
//     first reports a stable E2 ~0.8 s after the strum, once the upper
//     tones have decayed — far outside the ±150 ms window, so even the
//     one hoped-for match does not materialize. That detection (and a
//     second re-opened E2 during the release tail) matches nothing and
//     is dropped, never mispaired. Chord verification proper is Phase 4
//     (docs/DECISIONS.md D4/D5).
//
// Observed accuracy is therefore (13 Hit + 0.5*0 Close)/16 = 0.8125; the
// asserted floor is 0.80 — the 0.875 best case less the chord's possible
// match. The run must also explain itself: the test asserts that the ONLY
// Misses are the three chord notes, so a regression anywhere else in the
// chain fails even when accuracy stays above the floor.
//
// Advance is driven with a LAGGED frame during streaming: the tracker
// only delivers a note when it CLOSES, which for a long note is seconds
// after its Start, so finalizing against the live capture frame would
// Miss expectations whose detections are still open. The final Advance
// past the stream end (after Flush) finalizes the rest.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/S95F/guitarTutor/internal/synth"
)

func TestRoundTripFixtureRiff(t *testing.T) {
	if testing.Short() {
		t.Skip("full synthesis+analysis round trip; skipped in -short")
	}
	const sr = 48000

	path := filepath.Join("..", "..", "testdata", "fixture_riff.gtab")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	sc, err := textfmt.Parse(src, "fixture_riff.gtab")
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	nEvents := len(sc.Events())
	if nEvents != 16 {
		t.Fatalf("fixture flattens to %d events, want 16 (accounting below assumes it)", nEvents)
	}

	scorer := NewScorer(Config{SampleRate: sr, Track: 0, LatencyOffsetFrames: 0})

	e := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})
	e.SetEventTap(scorer.ExpectNote)
	e.Play()

	det := pitch.NewDetector(pitch.DefaultConfig(sr))
	trk := pitch.NewTracker(pitch.DefaultConfig(sr))

	// 4 bars of 4/4 at 120 BPM = 8 s; render 9 s so the last note decays
	// and the tracker closes it before Flush.
	const total = 9 * sr
	const block = 2048
	left := make([]float32, block)
	right := make([]float32, block)
	mono := make([]float32, block)
	var fed int64
	for fed < total {
		n := block
		if rem := total - fed; rem < block {
			n = int(rem)
		}
		e.RenderFrames(left[:n], right[:n])
		for i := 0; i < n; i++ {
			mono[i] = 0.5 * (left[i] + right[i])
		}
		scorer.Detected(trk.Feed(det.Process(mono[:n])))
		fed += int64(n)
		// Lag the miss deadline behind capture by 3 s (longer than any
		// note in the piece) so still-open notes are not misjudged; see
		// the file comment.
		scorer.Advance(fed - 3*sr)
	}
	scorer.Detected(trk.Flush())
	scorer.Advance(fed + int64(sr)) // push every deadline past the end

	results := scorer.Results(nil)
	st := scorer.Stats()
	t.Logf("round trip: %d results, stats %+v, accuracy %.3f", len(results), st, st.Accuracy())
	for _, r := range results {
		t.Logf("  key %3d out %7d -> verdict %d matched %v errCents %+6.1f errFrames %+6d",
			r.Event.Key, r.OutFrame, r.Verdict, r.Matched, r.ErrCents, r.ErrFrames)
	}

	if len(results) != nEvents {
		t.Fatalf("judged %d expectations, want %d", len(results), nEvents)
	}
	// Floor 0.80: 13/16 single-note Hits with the whole chord missed
	// (see the file comment for why the chord contributes 0, not 1).
	if got := st.Accuracy(); got < 0.80 {
		t.Errorf("accuracy %.3f, want >= 0.80", got)
	}
	if st.Hit < 13 {
		t.Errorf("hits %d, want all 13 single notes hit", st.Hit)
	}
	// Every Miss must be explained: the only expectations allowed to miss
	// are chord notes (bar 3 starts at tick of the chord; all three chord
	// events share one OutFrame, and exactly one of them may match).
	chordFrame := int64(-1)
	frames := map[int64]int{}
	for _, r := range results {
		frames[r.OutFrame]++
	}
	for f, c := range frames {
		if c == 3 {
			chordFrame = f
		}
	}
	if chordFrame < 0 {
		t.Fatal("no 3-event chord frame found in results")
	}
	for _, r := range results {
		if r.Verdict == VerdictMiss && r.OutFrame != chordFrame {
			t.Errorf("unexplained Miss: key %d at out frame %d", r.Event.Key, r.OutFrame)
		}
	}
	// The tied whole note must be one expectation, judged once, and not a
	// Miss (a long ringing E2 is the easiest note in the piece).
	var last NoteResult
	for _, r := range results {
		if r.OutFrame > last.OutFrame {
			last = r
		}
	}
	if last.Verdict == VerdictMiss {
		t.Errorf("tied whole note missed: %+v", last)
	}
}
