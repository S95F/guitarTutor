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

// TestScorerEarliestDeadlineNotNearest is the W5 regression: two same-key
// expectations, two detections, both matchable. The first detection lies
// in both windows but nearer the LATER expectation; nearest-in-time
// matching gave it away and starved the earlier expectation into a
// spurious Miss even though a two-Hit assignment exists. Oldest-deadline
// matching yields two Hits.
func TestScorerEarliestDeadlineNotNearest(t *testing.T) {
	s := NewScorer(testConfig()) // window 7200
	s.ExpectNote(ev(40), 24000)
	s.ExpectNote(ev(40), 33600)
	// 30000 is 6000 from the first, 3600 from the second (both in
	// window); 36000 is in the second's window only.
	s.Detected([]pitch.Note{det(30000, 40, 0), det(36000, 40, 0)})
	rs := drain(s)
	if len(rs) != 2 {
		t.Fatalf("got %d results, want 2", len(rs))
	}
	for i, r := range rs {
		if r.Verdict != VerdictHit {
			t.Errorf("result %d (out %d): verdict %v, want Hit", i, r.OutFrame, r.Verdict)
		}
	}
	if rs[0].OutFrame != 24000 || rs[0].ErrFrames != 6000 {
		t.Errorf("first pairing = (out %d, ErrFrames %d), want the older expectation (24000, +6000)",
			rs[0].OutFrame, rs[0].ErrFrames)
	}
	if rs[1].OutFrame != 33600 || rs[1].ErrFrames != 2400 {
		t.Errorf("second pairing = (out %d, ErrFrames %d), want (33600, +2400)",
			rs[1].OutFrame, rs[1].ErrFrames)
	}
}

// wev builds a track-0 wait-point expectation at a nonzero tick.
func wev(key int, start int64) score.NoteEvent {
	return score.NoteEvent{Track: 0, Key: key, Start: start}
}

// TestScorerWaitConfirmedStaccato is the W1 regression: at a wait point a
// staccato confirmation CLOSES (and reaches Detected) before the engine
// releases the held expectation, so the detection matched nothing and was
// dropped — the note then scored Miss. WaitConfirmed must record the
// pre-match so the released expectation finalizes Hit.
func TestScorerWaitConfirmedStaccato(t *testing.T) {
	s := NewScorer(testConfig())
	e := wev(40, 960)
	n := det(1000, 40, 5)
	s.Detected([]pitch.Note{n}) // expectation-free: consumed, matches nothing
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{n})
	s.ExpectNote(e, 2000) // the release frame, downstream of the playing
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	r := rs[0]
	if r.Verdict != VerdictHit || !r.Matched {
		t.Errorf("got verdict %v matched %v, want Hit and matched", r.Verdict, r.Matched)
	}
	if r.ErrFrames != 0 {
		t.Errorf("ErrFrames %d, want 0 (timing is not the player's error at a wait point)", r.ErrFrames)
	}
	if r.ErrCents != 5 {
		t.Errorf("ErrCents %v, want 5 (carried from the confirming note)", r.ErrCents)
	}
}

// TestScorerWaitConfirmedWithLatencyOffset is the W2 regression: at a wait
// point dt is structurally -(latency offset + confirmation latency), so a
// calibrated 4800-frame offset pushed every correctly-played wait note out
// of the window and into Miss. The pre-match verdict is pitch-only: Hit,
// ErrFrames 0.
func TestScorerWaitConfirmedWithLatencyOffset(t *testing.T) {
	s := NewScorer(Config{SampleRate: 48000, Track: 0, LatencyOffsetFrames: 4800})
	e := wev(40, 1920)
	n := det(10000, 40, 3) // input clock; confirmed the wait
	s.Detected([]pitch.Note{n})
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{n})
	// Release frame AFTER the confirming attack: mapped dt would be
	// (10000-4800) - 14000 = -8800, outside the +/-7200 window.
	s.ExpectNote(e, 14000)
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	if rs[0].Verdict != VerdictHit || rs[0].ErrFrames != 0 {
		t.Errorf("got verdict %v ErrFrames %d, want Hit with ErrFrames 0", rs[0].Verdict, rs[0].ErrFrames)
	}
}

// TestScorerWaitConfirmedVerdicts checks the pitch-only verdict ladder and
// best-cents selection of WaitConfirmed.
func TestScorerWaitConfirmedVerdicts(t *testing.T) {
	tests := []struct {
		name  string
		notes []pitch.Note
		want  Verdict
		cents float64
	}{
		{"in tolerance", []pitch.Note{det(1000, 40, 20)}, VerdictHit, 20},
		{"loose intonation", []pitch.Note{det(1000, 40, 60)}, VerdictClose, 60},
		{"octave off", []pitch.Note{det(1000, 52, 0)}, VerdictClose, 0},
		{"best cents wins", []pitch.Note{det(1000, 40, 60), det(1200, 40, -4)}, VerdictHit, -4},
		{"exact beats octave", []pitch.Note{det(1000, 52, 0), det(1200, 40, 50)}, VerdictClose, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScorer(testConfig())
			e := wev(40, 960)
			s.WaitConfirmed([]score.NoteEvent{e}, tt.notes)
			s.ExpectNote(e, 2000)
			rs := drain(s)
			if len(rs) != 1 {
				t.Fatalf("got %d results, want 1", len(rs))
			}
			if rs[0].Verdict != tt.want || rs[0].ErrCents != tt.cents {
				t.Errorf("got (%v, %v cents), want (%v, %v cents)",
					rs[0].Verdict, rs[0].ErrCents, tt.want, tt.cents)
			}
		})
	}
}

// TestScorerWaitPreMatchConsumedOnce: a pre-match satisfies exactly one
// expectation; a repeat of the same tick (loop pass) is judged normally,
// never double-credited.
func TestScorerWaitPreMatchConsumedOnce(t *testing.T) {
	s := NewScorer(testConfig())
	e := wev(40, 960)
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{det(1000, 40, 0)})
	s.ExpectNote(e, 2000)  // consumes the pre-match
	s.ExpectNote(e, 50000) // same tick again: pre-match is gone
	rs := drain(s)
	if len(rs) != 2 {
		t.Fatalf("got %d results, want 2", len(rs))
	}
	st := s.Stats()
	if st.Hit != 1 || st.Miss != 1 {
		t.Errorf("stats %+v, want exactly 1 Hit and 1 Miss (pre-match consumed once)", st)
	}
}

// TestScorerWaitPreMatchExpires: a seek can abandon a confirm; when the
// position returns to the same tick seconds later the stale pre-match
// must not fire.
func TestScorerWaitPreMatchExpires(t *testing.T) {
	s := NewScorer(testConfig())
	e := wev(40, 960)
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{det(1000, 40, 0)})
	s.Advance(6 * 48000) // > 5 s of clock: the pre-match expires
	s.ExpectNote(e, 6*48000+2000)
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	if rs[0].Verdict != VerdictMiss {
		t.Errorf("verdict %v, want Miss (stale pre-match must not credit a later pass)", rs[0].Verdict)
	}
}

// TestScorerWaitConfirmedIgnoresOtherTracks mirrors ExpectNote's track
// filter.
func TestScorerWaitConfirmedIgnoresOtherTracks(t *testing.T) {
	s := NewScorer(testConfig())
	e := score.NoteEvent{Track: 1, Key: 40, Start: 960}
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{det(1000, 40, 0)})
	// The track-0 twin of the event must NOT be pre-matched.
	s.ExpectNote(wev(40, 960), 2000)
	rs := drain(s)
	if len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Errorf("got %+v, want one Miss (track-1 confirmation must not pre-match track 0)", rs)
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
