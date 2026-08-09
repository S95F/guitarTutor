package practice

// The money test: prove the whole chain hangs together with no microphone
// in the loop. The fixture riff is parsed from .gtab, rendered offline
// through the real engine and Karplus-Strong synth, and the rendered audio
// is fed straight back through the real pitch detector and tracker into
// the scorer. Output and input share one clock (the audio never leaves the
// process), so LatencyOffsetFrames is 0.
//
// This is also the test that measures Phase 4. Through Phase 3 the run
// scored 13 Hit / 3 Miss, accuracy 0.8125: the 13 single notes all hit,
// and the bar-3 chord (E2+B2+E3, keys 40/47/52 — three expectations on one
// tick) contributed nothing at all. The design had allowed for 1 match + 2
// misses, since a monophonic detector reports one note per moment, but in
// practice MPM could not lock onto ANY single f0 while all three tones
// sounded: it first reported a stable E2 ~0.8 s after the strum, once the
// upper tones had decayed, far outside the +/-150 ms window. Even the one
// hoped-for match never materialized.
//
// With pitch.Config.Strums on, the detector also emits a Strum per onset —
// the attack plus the chroma of the audio that followed it — and
// Scorer.DetectedStrum verifies the expected chord against it
// (docs/DECISIONS.md D4, ROADMAP Phase 4). Observed accounting over the
// fixture's 16 note events, from the actual run:
//   - The 13 single notes still judge Hit, with ErrFrames of roughly
//     +1000..+3000 (the detector needs 2-4 cycles plus tracker hysteresis
//     before a note opens: D4's physics budget). Their strums record only
//     an onset and leave the cents-bearing monophonic verdict alone.
//   - The bar-3 chord's 3 expectations now all judge Hit, off ONE strum
//     stamped 896 frames (~19 ms) before the chord's output frame — the
//     onset hop's center trails the attack by half an analysis window.
//     Its chroma puts the two expected classes at E 1.00 and B 0.68 with
//     no unexpected bin above 0.36, correlating 0.93 with the expected
//     template: clear of the 0.45 presence ratio and the 0.30 correlation
//     gate with margin on both, on a Karplus-Strong pluck rich enough in
//     partials to be a fair test of the fold.
//   - The tracker's belated E2 reading of that same chord (and a second
//     re-opened E2 in the release tail) now finds nothing pending and is
//     dropped, as before — never mispaired.
//
// Observed accuracy is therefore 16/16 = 1.000, up from 0.8125. The
// asserted floor is 0.95, which cannot be reached without the chord: 13
// Hits out of 16 caps at 0.8125, and even 13 Hits plus 3 Closes only
// reaches 0.906. The run must also explain itself: the test asserts there
// are no Misses at all, so a regression anywhere in the chain fails.
//
// Advance is driven with a LAGGED frame during streaming: the tracker only
// delivers a note when it CLOSES, which for a long note is seconds after
// its Start, so finalizing against the live capture frame would Miss
// expectations whose detections are still open. The final Advance past the
// stream end (after Flush) finalizes the rest.

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

	pcfg := pitch.DefaultConfig(sr)
	pcfg.Strums = true // Phase 4: chord verification needs the chroma
	det := pitch.NewDetector(pcfg)
	trk := pitch.NewTracker(pcfg)

	// 4 bars of 4/4 at 120 BPM = 8 s; render 9 s so the last note decays
	// and the tracker closes it before Flush.
	const total = 9 * sr
	const block = 2048
	left := make([]float32, block)
	right := make([]float32, block)
	mono := make([]float32, block)
	var fed int64
	var nStrums int
	for fed < total {
		n := block
		if rem := total - fed; rem < block {
			n = int(rem)
		}
		e.RenderFrames(left[:n], right[:n])
		for i := 0; i < n; i++ {
			mono[i] = 0.5 * (left[i] + right[i])
		}
		frames := det.Process(mono[:n])
		// Strums first, exactly as live.analyzer.process delivers them:
		// a strum is stamped at its onset, a note only when the tracker
		// closes it, and Advance below must not age a chord out before
		// its strum has been offered.
		for _, st := range det.Strums() {
			t.Logf("strum @%7d rms %.4f clarity %.2f chroma %v", st.Frame, st.RMS, st.Clarity, st.Chroma)
			scorer.DetectedStrum(st)
			nStrums++
		}
		scorer.Detected(trk.Feed(frames))
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
	t.Logf("round trip: %d results, %d strums, stats %+v, accuracy %.3f", len(results), nStrums, st, st.Accuracy())
	for _, r := range results {
		t.Logf("  key %3d out %7d -> verdict %d matched %v errCents %+6.1f errFrames %+6d",
			r.Event.Key, r.OutFrame, r.Verdict, r.Matched, r.ErrCents, r.ErrFrames)
	}

	if len(results) != nEvents {
		t.Fatalf("judged %d expectations, want %d", len(results), nEvents)
	}
	if nStrums == 0 {
		t.Fatal("the detector emitted no strums: chord verification never ran")
	}
	// Floor 0.95: unreachable without the chord (13 single-note Hits out
	// of 16 cap at 0.8125, and 13 Hits + 3 Closes only reach 0.906), so
	// this asserts the Phase 4 result rather than the Phase 3 one.
	if got := st.Accuracy(); got < 0.95 {
		t.Errorf("accuracy %.3f, want >= 0.95 (Phase 3 measured 0.8125 with the chord unscored)", got)
	}
	if st.Hit < 16 {
		t.Errorf("hits %d, want all 16 events hit (13 single notes + the 3-note chord)", st.Hit)
	}
	if st.Miss != 0 {
		t.Errorf("misses %d, want 0: every expectation must be explained", st.Miss)
	}

	// The chord specifically: three expectations on one output frame, all
	// judged Hit off the strum, and none of them claiming a cents figure
	// chroma cannot supply.
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
		if r.OutFrame != chordFrame {
			continue
		}
		if r.Verdict != VerdictHit || !r.Matched {
			t.Errorf("chord note key %d: got %+v, want a matched Hit from the strum", r.Event.Key, r)
		}
		if r.ErrCents != 0 {
			t.Errorf("chord note key %d: ErrCents %v, want 0 (chroma carries no intonation)", r.Event.Key, r.ErrCents)
		}
		if r.ErrFrames > 0 {
			t.Errorf("chord note key %d: ErrFrames %d, want the onset's lead over the output frame", r.Event.Key, r.ErrFrames)
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
