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

func TestRoundTripFixtureSax(t *testing.T) {
	if testing.Short() {
		t.Skip("full synthesis+analysis round trip; skipped in -short")
	}
	const sr = 48000

	path := filepath.Join("..", "..", "testdata", "fixture_sax.gtab")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	sc, err := textfmt.Parse(src, "fixture_sax.gtab")
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	tr := sc.Tracks[0]
	if tr.Wind == nil || tr.Wind.Name != "soprano sax" {
		t.Fatalf("fixture track is %v, want a soprano sax", tr.Wind)
	}
	nEvents := len(sc.Events())
	if nEvents != 14 {
		t.Fatalf("fixture flattens to %d events, want 14 (the tie merges)", nEvents)
	}

	scorer := NewScorer(Config{SampleRate: sr, Track: 0, LatencyOffsetFrames: 0})

	e := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewBuiltin})
	e.SetEventTap(scorer.ExpectNote)
	e.Play()

	pcfg := pitch.ConfigForKeys(sr, tr.Wind.LowSounding, tr.Wind.LowSounding+tr.Wind.Span)
	det := pitch.NewDetector(pcfg)
	trk := pitch.NewTracker(pcfg)

	const total = 12 * sr
	const block = 2048
	left := make([]float32, block)
	right := make([]float32, block)
	mono := make([]float32, block)
	var fed int64
	for fed < total {
		n := block
		if rem := total - fed; rem < int64(block) {
			n = int(rem)
		}
		e.RenderFrames(left[:n], right[:n])
		for i := 0; i < n; i++ {
			mono[i] = 0.5 * (left[i] + right[i])
		}
		scorer.Detected(trk.Feed(det.Process(mono[:n])))
		fed += int64(n)

		scorer.Advance(fed - 3*sr)
	}
	scorer.Detected(trk.Flush())
	scorer.Advance(fed + int64(sr))

	results := scorer.Results(nil)
	st := scorer.Stats()
	t.Logf("wind round trip: %d results, stats %+v, accuracy %.3f", len(results), st, st.Accuracy())
	for _, r := range results {
		t.Logf("  key %3d out %7d -> verdict %d matched %v errCents %+6.1f errFrames %+6d",
			r.Event.Key, r.OutFrame, r.Verdict, r.Matched, r.ErrCents, r.ErrFrames)
	}

	if len(results) != nEvents {
		t.Fatalf("judged %d expectations, want %d", len(results), nEvents)
	}
	if st.Miss != 0 {
		t.Errorf("misses %d, want 0: every expectation must be explained", st.Miss)
	}
	if got := st.Accuracy(); got < 0.95 {
		t.Errorf("accuracy %.3f, want >= 0.95", got)
	}

	var last NoteResult
	for _, r := range results {
		if r.OutFrame > last.OutFrame {
			last = r
		}
	}
	if last.Verdict != VerdictHit {
		t.Errorf("tied whole note: %+v, want a Hit", last)
	}
}
