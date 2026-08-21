package practice

import (
	"sync"
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
)

const (
	pcC  = 0
	pcE  = 4
	pcF  = 5
	pcG  = 7
	pcA  = 9
	pcAs = 10
	pcB  = 11
)

func chroma(bg float32, classes ...int) pitch.Chroma {
	var ch pitch.Chroma
	for i := range ch {
		ch[i] = bg
	}
	for _, c := range classes {
		ch[c] = 1
	}
	return ch
}

func strumAt(frame int64, ch pitch.Chroma) pitch.Strum {
	return pitch.Strum{Frame: frame, Chroma: ch, RMS: 0.1, Clarity: 0.4}
}

var eChord = []int{40, 47, 52}

func expectChord(s *Scorer, keys []int, tick, outFrame int64) {
	for _, k := range keys {
		s.ExpectNote(score.NoteEvent{Track: 0, Key: k, Start: tick}, outFrame)
	}
}

func TestScorerChordVerifiedFromStrum(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, eChord, 1920, 24000)
	s.DetectedStrum(strumAt(24600, chroma(0.15, pcE, pcB)))

	rs := s.Results(nil)
	if len(rs) != 3 {
		t.Fatalf("got %d results from the strum, want all 3 chord notes finalized: %+v", len(rs), rs)
	}
	for i, r := range rs {
		if r.Verdict != VerdictHit {
			t.Errorf("result %d (key %d): verdict %v, want Hit", i, r.Event.Key, r.Verdict)
		}
		if !r.Matched {
			t.Errorf("result %d (key %d): Matched false, want true", i, r.Event.Key)
		}

		if r.ErrCents != 0 {
			t.Errorf("result %d: ErrCents %v, want 0 (chroma carries no cents)", i, r.ErrCents)
		}
		if r.ErrFrames != 600 {
			t.Errorf("result %d: ErrFrames %d, want 600 (measured from the onset)", i, r.ErrFrames)
		}
	}
	if st := s.Stats(); st != (Stats{Hit: 3}) {
		t.Errorf("stats %+v, want 3 Hits", st)
	}

	s.Detected([]pitch.Note{det(25000, 40, 2)})
	if extra := drain(s); len(extra) != 0 {
		t.Errorf("a late detection re-judged finalized chord notes: %+v", extra)
	}
}

func TestScorerChordMutedString(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, eChord, 1920, 24000)

	ch := chroma(0.15, pcE)
	ch[pcB] = 0.05
	s.DetectedStrum(strumAt(24000, ch))

	rs := s.Results(nil)
	if len(rs) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(rs), rs)
	}
	var missed []int
	for _, r := range rs {
		if r.Verdict == VerdictMiss {
			missed = append(missed, r.Event.Key)
			if r.Matched || r.ErrCents != 0 || r.ErrFrames != 0 {
				t.Errorf("chord Miss carries errors: %+v", r)
			}
		}
	}
	if len(missed) != 1 || missed[0] != 47 {
		t.Errorf("missed keys %v, want exactly [47] (the muted B string)", missed)
	}
	if st := s.Stats(); st != (Stats{Hit: 2, Miss: 1}) {
		t.Errorf("stats %+v, want 2 Hit + 1 Miss", st)
	}
}

func TestScorerWrongChordIsNotVerified(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, eChord, 1920, 24000)

	ch := chroma(0.15, pcC, pcF)
	ch[pcE] = 0.05
	ch[pcB] = 0.05
	s.DetectedStrum(strumAt(24000, ch))

	if rs := s.Results(nil); len(rs) != 0 {
		t.Fatalf("the wrong chord finalized %d expectations: %+v", len(rs), rs)
	}
	rs := drain(s)
	if len(rs) != 3 {
		t.Fatalf("got %d results at the deadline, want 3", len(rs))
	}
	for _, r := range rs {
		if r.Verdict != VerdictMiss || r.Matched {
			t.Errorf("key %d: got %+v, want an unmatched Miss", r.Event.Key, r)
		}
	}
}

func TestScorerStrumLeavesSingleNotesToTheMonophonicPath(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.DetectedStrum(strumAt(24300, chroma(0.15, pcE)))
	if rs := s.Results(nil); len(rs) != 0 {
		t.Fatalf("the strum finalized a single note: %+v", rs)
	}

	s.Detected([]pitch.Note{det(24900, 40, -18)})
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	if rs[0].Verdict != VerdictHit || rs[0].ErrCents != -18 || rs[0].ErrFrames != 900 {
		t.Errorf("got %+v, want a Hit with the monophonic path's cents (-18) and timing (+900)", rs[0])
	}
	if st := s.Stats(); st != (Stats{Hit: 1}) {
		t.Errorf("stats %+v, want a single Hit (the strum must not add a verdict)", st)
	}
}

func TestScorerPalmMuteCredit(t *testing.T) {

	muted := chroma(0.05, pcAs)
	muted[pcE] = 0.5

	elsewhere := chroma(0.05, pcAs)
	elsewhere[pcE] = 0.02

	tests := []struct {
		name    string
		strum   bool
		ch      pitch.Chroma
		want    Verdict
		matched bool
		frames  int64
	}{
		{"damped attack with energy at the class", true, muted, VerdictClose, true, 500},
		{"attack with nothing at the class", true, elsewhere, VerdictMiss, false, 0},
		{"no attack at all", false, muted, VerdictMiss, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScorer(testConfig())
			s.ExpectNote(ev(40), 24000)
			if tt.strum {
				s.DetectedStrum(strumAt(24500, tt.ch))
			}
			rs := drain(s)
			if len(rs) != 1 {
				t.Fatalf("got %d results, want 1", len(rs))
			}
			r := rs[0]
			if r.Verdict != tt.want || r.Matched != tt.matched || r.ErrFrames != tt.frames {
				t.Errorf("got (verdict %v, matched %v, errFrames %d), want (%v, %v, %d)",
					r.Verdict, r.Matched, r.ErrFrames, tt.want, tt.matched, tt.frames)
			}

			if r.ErrCents != 0 {
				t.Errorf("ErrCents %v, want 0", r.ErrCents)
			}
		})
	}
}

func TestScorerPalmMuteNeverOverridesADetection(t *testing.T) {
	muted := chroma(0.05, pcAs)
	muted[pcE] = 0.9

	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.DetectedStrum(strumAt(24100, muted))
	s.Detected([]pitch.Note{det(24400, 40, 55)})
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	if rs[0].Verdict != VerdictClose || rs[0].ErrCents != 55 || rs[0].ErrFrames != 400 {
		t.Errorf("got %+v, want the monophonic Close (55 cents, +400 frames), not the onset's", rs[0])
	}
}

func TestScorerStrumTimingWindow(t *testing.T) {
	const out = 24000

	tests := []struct {
		name  string
		frame int64
		hits  int
	}{
		{"late, on the edge", out + 7200, 3},
		{"late, one frame past", out + 7201, 0},
		{"early, on the edge", out - 7200, 3},
		{"early, one frame past", out - 7201, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScorer(testConfig())
			expectChord(s, eChord, 1920, out)
			s.DetectedStrum(strumAt(tt.frame, chroma(0.15, pcE, pcB)))
			if got := len(s.Results(nil)); got != tt.hits {
				t.Errorf("the strum finalized %d expectations, want %d", got, tt.hits)
			}
		})
	}

	t.Run("noodling with nothing pending", func(t *testing.T) {
		s := NewScorer(testConfig())
		s.DetectedStrum(strumAt(50000, chroma(0.15, pcC, pcG)))
		if rs := drain(s); len(rs) != 0 {
			t.Errorf("a strum with no pending expectations produced %+v", rs)
		}
		if st := s.Stats(); st != (Stats{}) {
			t.Errorf("stats %+v, want zero: noodling is never penalized", st)
		}
	})

	t.Run("empty chroma verifies nothing", func(t *testing.T) {
		s := NewScorer(testConfig())
		expectChord(s, eChord, 1920, out)
		s.DetectedStrum(strumAt(out, pitch.Chroma{}))
		if rs := s.Results(nil); len(rs) != 0 {
			t.Errorf("an all-zero chroma finalized %+v; every bin must not read as present", rs)
		}
	})
}

func TestScorerStrumPicksTheNearestChord(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, []int{40, 47}, 1920, 24000)
	expectChord(s, []int{48, 55}, 2880, 28000)
	s.DetectedStrum(strumAt(27900, chroma(0.15, pcC, pcG)))

	rs := s.Results(nil)
	if len(rs) != 2 {
		t.Fatalf("got %d results, want the nearer chord's 2: %+v", len(rs), rs)
	}
	for _, r := range rs {
		if r.OutFrame != 28000 || r.Verdict != VerdictHit {
			t.Errorf("got %+v, want a Hit on the chord at 28000", r)
		}
	}

	rest := drain(s)
	if len(rest) != 2 {
		t.Fatalf("got %d further results, want the earlier chord's 2", len(rest))
	}
	for _, r := range rest {
		if r.OutFrame != 24000 {
			t.Errorf("unexpected leftover %+v", r)
		}
	}
}

func TestScorerStrumBelongsToTheChordItLandedOn(t *testing.T) {
	s := NewScorer(testConfig())
	am := []int{45, 52, 57, 60, 64}
	cOverG := []int{43, 48, 52, 55, 60, 64}
	expectChord(s, am, 1920, 24000)
	expectChord(s, cOverG, 2040, 30000)

	s.DetectedStrum(strumAt(24600, chroma(0.15, pcA, pcC, pcE)))

	rs := s.Results(nil)
	if len(rs) != len(am) {
		t.Fatalf("the strum finalized %d expectations, want the Am's %d: %+v", len(rs), len(am), rs)
	}
	for _, r := range rs {
		if r.OutFrame != 24000 {
			t.Errorf("finalized a note at outFrame %d; only the chord at 24000 was strummed: %+v", r.OutFrame, r)
		}
		if r.Verdict != VerdictHit {
			t.Errorf("key %d: verdict %v, want Hit", r.Event.Key, r.Verdict)
		}
	}
	if st := s.Stats(); st != (Stats{Hit: len(am)}) {
		t.Errorf("stats %+v, want %d Hits and nothing else", st, len(am))
	}

	s.DetectedStrum(strumAt(30300, chroma(0.15, pcC, pcE, pcG)))
	rs = s.Results(nil)
	if len(rs) != len(cOverG) {
		t.Fatalf("the second strum finalized %d expectations, want the C/G's %d: %+v", len(rs), len(cOverG), rs)
	}
	for _, r := range rs {
		if r.OutFrame != 30000 || r.Verdict != VerdictHit {
			t.Errorf("got %+v, want a Hit on the chord at 30000", r)
		}
	}
}

func TestScorerStrumSizeBreaksProximityTies(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, []int{40, 47}, 1920, 24000)
	expectChord(s, []int{43, 48, 52, 55, 60}, 1930, 25200)

	s.DetectedStrum(strumAt(24100, chroma(0.15, pcC, pcE, pcG)))

	rs := s.Results(nil)
	if len(rs) != 5 {
		t.Fatalf("the strum finalized %d expectations, want the 5-note chord's: %+v", len(rs), rs)
	}
	for _, r := range rs {
		if r.OutFrame != 25200 || r.Verdict != VerdictHit {
			t.Errorf("got %+v, want a Hit on the chord at 25200", r)
		}
	}
}

func TestScorerStrumAbandonedChord(t *testing.T) {
	t.Run("no strum: dropped", func(t *testing.T) {
		s := NewScorer(testConfig())
		expectChord(s, eChord, 1920, 24000)
		s.AbandonBefore(30000)
		if rs := drain(s); len(rs) != 0 {
			t.Errorf("abandoned chord produced %+v, want nothing", rs)
		}
	})
	t.Run("strum in flight: still scored", func(t *testing.T) {
		s := NewScorer(testConfig())
		expectChord(s, eChord, 1920, 24000)
		s.AbandonBefore(30000)
		s.DetectedStrum(strumAt(24200, chroma(0.15, pcE, pcB)))
		rs := drain(s)
		if len(rs) != 3 {
			t.Fatalf("got %d results, want 3 Hits the player earned", len(rs))
		}
		for _, r := range rs {
			if r.Verdict != VerdictHit {
				t.Errorf("key %d: verdict %v, want Hit", r.Event.Key, r.Verdict)
			}
		}
	})
}

func TestChordCorrelation(t *testing.T) {
	var tmpl [pitch.PitchClasses]bool
	tmpl[pcE], tmpl[pcB] = true, true

	perfect := chordCorrelation(chroma(0, pcE, pcB), tmpl)
	if perfect < 0.999 {
		t.Errorf("exact template correlation = %v, want ~1", perfect)
	}
	var flat pitch.Chroma
	for i := range flat {
		flat[i] = 1
	}
	if got := chordCorrelation(flat, tmpl); got != 0 {
		t.Errorf("featureless chroma correlation = %v, want 0 (no variance to explain)", got)
	}
	foreign := chroma(0, pcC, pcF, pcG)
	if got := chordCorrelation(foreign, tmpl); got >= 0 {
		t.Errorf("foreign-chord correlation = %v, want negative", got)
	}

	if got := chordCorrelation(chroma(0.1, pcE), [pitch.PitchClasses]bool{}); got != 0 {
		t.Errorf("empty-template correlation = %v, want 0", got)
	}
}

func TestScorerStrumConcurrencySmoke(t *testing.T) {
	const n = 1000
	s := NewScorer(testConfig())
	ch := chroma(0.15, pcE, pcB)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []NoteResult

	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			expectChord(s, []int{40, 47}, int64(i)*480, int64(i)*12000)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.DetectedStrum(strumAt(int64(i)*12000+300, ch))
			s.Detected([]pitch.Note{det(int64(i)*12000+900, 40, 5)})
			if i%64 == 0 {
				s.Advance(int64(i) * 12000)
			}
		}
	}()
	go func() {
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
	if len(all) != 2*n {
		t.Fatalf("got %d results, want %d (every expectation judged exactly once)", len(all), 2*n)
	}
	st := s.Stats()
	if st.Hit+st.Close+st.Miss != 2*n {
		t.Errorf("stats total %d, want %d", st.Hit+st.Close+st.Miss, 2*n)
	}
	seen := make(map[[2]int64]int)
	for _, r := range all {
		seen[[2]int64{r.OutFrame, int64(r.Event.Key)}]++
	}
	for k, c := range seen {
		if c != 1 {
			t.Errorf("expectation (frame %d, key %d) judged %d times", k[0], k[1], c)
		}
	}
}
