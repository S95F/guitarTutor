package practice

// The wind money test, mirroring roundtrip_test.go for the first
// non-guitar instrument: the soprano sax fixture is parsed from .gtab,
// rendered offline through the real engine and the sustained reed voice,
// and the audio is fed straight back through the real detector and tracker
// into the scorer. One clock, LatencyOffsetFrames 0.
//
// The wiring differs from the guitar round trip exactly where a live wind
// session will differ:
//   - the detector's search range is fitted to the instrument
//     (pitch.ConfigForKeys over the track's sounding compass);
//   - Strums stay OFF. Chords cannot occur on a wind track, so chroma
//     verification has nothing to verify — and leaving it off also keeps
//     the palm-mute deadline credit (a guitar concept) from crediting
//     breath noise.
//
// What the fixture exercises beyond plain notes: two slurred changes
// (l -> TechSlur -> AttackLegato: the tracker splits them on the pitch
// jump with no onset to help), a slide (s -> AttackSlide: the reed glides
// and matchSlides judges the destination off the cents trajectory), two
// vibrato notes (the tracker must hold one note through a +/-25 cent
// wobble), and a tied pair merged into one long expectation.
//
// Every note in the fixture must be explained and none may Miss: a false
// "you missed" on correct playing is the #1 rage-quit cause (D5), and the
// synthetic round trip is exactly the case with no excuse.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/S95F/guitarTutor/internal/synth"
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

	// NewBuiltin so the test proves the whole default path: the track's
	// program (64, from the instrument) must select the reed on its own.
	e := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewBuiltin})
	e.SetEventTap(scorer.ExpectNote)
	e.Play()

	pcfg := pitch.ConfigForKeys(sr, tr.Wind.LowSounding, tr.Wind.LowSounding+tr.Wind.Span)
	det := pitch.NewDetector(pcfg)
	trk := pitch.NewTracker(pcfg)

	// 4 bars of 4/4 at 96 BPM = 10 s; render 12 s so the final vibrato
	// note releases and the tracker closes it before Flush.
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
		// Lag the miss deadline behind capture (see roundtrip_test.go).
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
	// The tied whole note is the last expectation and the easiest note in
	// the piece: a sustained voice holding one pitch for 2.5 seconds.
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
