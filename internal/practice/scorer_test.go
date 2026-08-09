package practice

import (
	"sync"
	"testing"

	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/score"
)

// testConfig is the baseline test scorer config: 48 kHz, track 0, default
// tolerances (window 7200 frames = 150 ms, 35/70 cents), zero latency.
func testConfig() Config { return Config{SampleRate: 48000, Track: 0} }

// ev builds a track-0 expectation with the given key.
func ev(key int) score.NoteEvent { return score.NoteEvent{Track: 0, Key: key} }

// det builds a detection starting at the given input frame.
func det(start int64, key int, cents float64) pitch.Note {
	return pitch.Note{Start: start, End: start + 4800, Key: key, Cents: cents, Clarity: 0.95}
}

// drain finalizes everything pending far in the future and returns all
// results.
func drain(s *Scorer) []NoteResult {
	s.Advance(1 << 40)
	return s.Results(nil)
}

func TestScorerPerfectPerformance(t *testing.T) {
	s := NewScorer(testConfig())
	keys := []int{40, 43, 45, 40, 43, 45}
	for i, k := range keys {
		s.ExpectNote(ev(k), int64(i)*12000)
	}
	// Detected slightly late (physics: the detector needs a few cycles).
	for i, k := range keys {
		s.Detected([]pitch.Note{det(int64(i)*12000+900, k, 3)})
	}
	rs := drain(s)
	if len(rs) != len(keys) {
		t.Fatalf("got %d results, want %d", len(rs), len(keys))
	}
	for i, r := range rs {
		if r.Verdict != VerdictHit {
			t.Errorf("result %d: verdict %v, want Hit", i, r.Verdict)
		}
		if !r.Matched {
			t.Errorf("result %d: not matched", i)
		}
		if r.ErrFrames != 900 {
			t.Errorf("result %d: ErrFrames %d, want 900", i, r.ErrFrames)
		}
	}
	st := s.Stats()
	if st.Hit != len(keys) || st.Close != 0 || st.Miss != 0 {
		t.Errorf("stats %+v, want all Hit", st)
	}
	if st.Accuracy() != 1 {
		t.Errorf("accuracy %v, want 1", st.Accuracy())
	}
}

func TestScorerPitchVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		detKey  int
		cents   float64
		want    Verdict
		matched bool
	}{
		{"flat 50 cents", 40, -50, VerdictClose, true},
		{"flat 20 cents", 40, -20, VerdictHit, true},
		{"sharp 20 cents", 40, 20, VerdictHit, true},
		{"octave up", 52, 0, VerdictClose, true},
		{"octave down", 28, 0, VerdictClose, true},
		{"wrong key", 41, 0, VerdictMiss, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScorer(testConfig())
			s.ExpectNote(ev(40), 24000)
			s.Detected([]pitch.Note{det(24000, tt.detKey, tt.cents)})
			rs := drain(s)
			if len(rs) != 1 {
				t.Fatalf("got %d results, want 1 (the stray detection must not create results)", len(rs))
			}
			if rs[0].Verdict != tt.want {
				t.Errorf("verdict %v, want %v", rs[0].Verdict, tt.want)
			}
			if rs[0].Matched != tt.matched {
				t.Errorf("matched %v, want %v", rs[0].Matched, tt.matched)
			}
			if tt.matched && rs[0].ErrCents != tt.cents {
				t.Errorf("ErrCents %v, want %v", rs[0].ErrCents, tt.cents)
			}
			if !tt.matched && (rs[0].ErrCents != 0 || rs[0].ErrFrames != 0) {
				t.Errorf("pure miss carries errors: %+v", rs[0])
			}
		})
	}
}

func TestScorerTimingWindow(t *testing.T) {
	// Default window is 150 ms = 7200 frames at 48 kHz.
	tests := []struct {
		name      string
		detStart  int64
		want      Verdict
		errFrames int64
	}{
		{"late beyond window", 24000 + 8000, VerdictMiss, 0},
		{"late within window", 24000 + 5000, VerdictHit, 5000},
		{"early within window", 24000 - 5000, VerdictHit, -5000},
		{"early beyond window", 24000 - 8000, VerdictMiss, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScorer(testConfig())
			s.ExpectNote(ev(45), 24000)
			s.Detected([]pitch.Note{det(tt.detStart, 45, 0)})
			rs := drain(s)
			if len(rs) != 1 {
				t.Fatalf("got %d results, want 1", len(rs))
			}
			if rs[0].Verdict != tt.want {
				t.Errorf("verdict %v, want %v", rs[0].Verdict, tt.want)
			}
			if rs[0].ErrFrames != tt.errFrames {
				t.Errorf("ErrFrames %d, want %d", rs[0].ErrFrames, tt.errFrames)
			}
		})
	}
}

func TestScorerLatencyOffsetShiftsWindow(t *testing.T) {
	// The same expectation/detection stream judged with and without
	// latency compensation. The note sounds at output frame 24000 and
	// arrives back in the capture stream at 24000 + 4800; with a tight
	// 2000-frame window only the compensated scorer matches it.
	expect := ev(40)
	note := det(24000+4800, 40, 0)

	uncomp := NewScorer(Config{SampleRate: 48000, TimingWindowFrames: 2000})
	uncomp.ExpectNote(expect, 24000)
	uncomp.Detected([]pitch.Note{note})
	if rs := drain(uncomp); rs[0].Verdict != VerdictMiss {
		t.Errorf("uncompensated: verdict %v, want Miss (offset unapplied)", rs[0].Verdict)
	}

	comp := NewScorer(Config{SampleRate: 48000, TimingWindowFrames: 2000, LatencyOffsetFrames: 4800})
	comp.ExpectNote(expect, 24000)
	comp.Detected([]pitch.Note{note})
	rs := drain(comp)
	if rs[0].Verdict != VerdictHit {
		t.Errorf("compensated: verdict %v, want Hit", rs[0].Verdict)
	}
	if rs[0].ErrFrames != 0 {
		t.Errorf("compensated: ErrFrames %d, want 0", rs[0].ErrFrames)
	}
}

func TestScorerChordOneDetection(t *testing.T) {
	// Three chord notes are three expectations; the monophonic detector
	// reports one note, so one Hit and two Misses (docs: honest Phase 2
	// behavior).
	s := NewScorer(testConfig())
	for _, k := range []int{40, 47, 52} {
		s.ExpectNote(ev(k), 24000)
	}
	s.Detected([]pitch.Note{det(24000, 40, 0)})
	rs := drain(s)
	if len(rs) != 3 {
		t.Fatalf("got %d results, want 3", len(rs))
	}
	st := s.Stats()
	if st.Hit != 1 || st.Miss != 2 || st.Close != 0 {
		t.Errorf("stats %+v, want 1 Hit + 2 Miss", st)
	}
	if rs[0].Event.Key != 40 || rs[0].Verdict != VerdictHit {
		t.Errorf("first judgement %+v, want Hit on key 40", rs[0])
	}
}

func TestScorerOutOfOrderAndDuplicates(t *testing.T) {
	t.Run("out of order", func(t *testing.T) {
		s := NewScorer(testConfig())
		s.ExpectNote(ev(40), 0)
		s.ExpectNote(ev(45), 24000)
		s.ExpectNote(ev(50), 48000)
		// Detections arrive shuffled; each pairs with its own
		// expectation (nearest in time, exact key).
		s.Detected([]pitch.Note{det(48200, 50, 0)})
		s.Detected([]pitch.Note{det(300, 40, 0), det(23900, 45, 0)})
		rs := drain(s)
		if len(rs) != 3 {
			t.Fatalf("got %d results, want 3", len(rs))
		}
		for _, r := range rs {
			if r.Verdict != VerdictHit {
				t.Errorf("key %d: verdict %v, want Hit", r.Event.Key, r.Verdict)
			}
		}
	})
	t.Run("duplicate detections", func(t *testing.T) {
		s := NewScorer(testConfig())
		s.ExpectNote(ev(40), 24000)
		// The same note re-picked: only one detection pairs, the other
		// matches nothing (and no phantom result appears).
		s.Detected([]pitch.Note{det(24100, 40, 0), det(25000, 40, 0)})
		rs := drain(s)
		if len(rs) != 1 {
			t.Fatalf("got %d results, want 1", len(rs))
		}
		if rs[0].Verdict != VerdictHit || rs[0].ErrFrames != 100 {
			t.Errorf("got %+v, want Hit paired with the nearer detection (ErrFrames 100)", rs[0])
		}
		st := s.Stats()
		if st.Hit+st.Close+st.Miss != 1 {
			t.Errorf("stats %+v, want exactly one judgement", st)
		}
	})
	t.Run("same-frame chord prefers exact key", func(t *testing.T) {
		s := NewScorer(testConfig())
		s.ExpectNote(ev(52), 24000) // octave of the detection, same frame
		s.ExpectNote(ev(40), 24000) // exact key
		s.Detected([]pitch.Note{det(24000, 40, 0)})
		rs := s.Results(nil)
		if len(rs) != 1 || rs[0].Event.Key != 40 || rs[0].Verdict != VerdictHit {
			t.Fatalf("got %+v, want the exact-key expectation (40) judged Hit", rs)
		}
	})
}

func TestScorerReset(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 0)
	s.Detected([]pitch.Note{det(0, 40, 0)})
	s.ExpectNote(ev(45), 24000) // still pending
	s.Reset()
	if st := s.Stats(); st != (Stats{}) {
		t.Errorf("stats after Reset: %+v, want zero", st)
	}
	if rs := drain(s); len(rs) != 0 {
		t.Errorf("results after Reset: %d, want 0 (pending cleared, no ghost Miss)", len(rs))
	}
}

func TestScorerIgnoresOtherTracks(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(score.NoteEvent{Track: 1, Key: 40}, 24000)
	if rs := drain(s); len(rs) != 0 {
		t.Errorf("track-1 event judged on a track-0 scorer: %+v", rs)
	}
}

// TestScorerConcurrencySmoke hammers the scorer from three goroutines in
// the production roles (tap, analysis, UI) and checks the totals are
// logically consistent afterward. Written race-clean for -race in CI.
func TestScorerConcurrencySmoke(t *testing.T) {
	const n = 2000
	s := NewScorer(testConfig())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []NoteResult

	wg.Add(3)
	go func() { // engine tap
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.ExpectNote(ev(40+i%12), int64(i)*12000)
		}
	}()
	go func() { // analysis: detections + advance
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.Detected([]pitch.Note{det(int64(i)*12000+500, 40+i%12, 5)})
			if i%64 == 0 {
				s.Advance(int64(i) * 12000)
			}
		}
	}()
	go func() { // UI: drain results
		defer wg.Done()
		buf := make([]NoteResult, 0, 256)
		for i := 0; i < n/10; i++ {
			buf = s.Results(buf[:0])
			mu.Lock()
			all = append(all, buf...)
			mu.Unlock()
		}
	}()
	wg.Wait()

	all = append(all, drain(s)...)
	if len(all) != n {
		t.Fatalf("got %d results, want %d (every expectation judged exactly once)", len(all), n)
	}
	st := s.Stats()
	if st.Hit+st.Close+st.Miss != n {
		t.Errorf("stats total %d, want %d", st.Hit+st.Close+st.Miss, n)
	}
	// Interleaving decides how many detections arrived before their
	// expectations (those go Miss); no verdict distribution is asserted,
	// only conservation.
	seen := make(map[int64]int)
	for _, r := range all {
		seen[r.OutFrame]++
	}
	for f, c := range seen {
		if c != 1 {
			t.Errorf("expectation at frame %d judged %d times", f, c)
		}
	}
}
