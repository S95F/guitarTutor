package practice

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/synth"
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
	pcfg.Strums = true
	det := pitch.NewDetector(pcfg)
	trk := pitch.NewTracker(pcfg)

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

		for _, st := range det.Strums() {
			t.Logf("strum @%7d rms %.4f clarity %.2f chroma %v", st.Frame, st.RMS, st.Clarity, st.Chroma)
			scorer.DetectedStrum(st)
			nStrums++
		}
		scorer.Detected(trk.Feed(frames))
		fed += int64(n)

		scorer.Advance(fed - 3*sr)
	}
	scorer.Detected(trk.Flush())
	scorer.Advance(fed + int64(sr))

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

	if got := st.Accuracy(); got < 0.95 {
		t.Errorf("accuracy %.3f, want >= 0.95 (Phase 3 measured 0.8125 with the chord unscored)", got)
	}
	if st.Hit < 16 {
		t.Errorf("hits %d, want all 16 events hit (13 single notes + the 3-note chord)", st.Hit)
	}
	if st.Miss != 0 {
		t.Errorf("misses %d, want 0: every expectation must be explained", st.Miss)
	}

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
