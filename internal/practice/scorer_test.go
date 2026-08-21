package practice

import (
	"sync"
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
)

func testConfig() Config { return Config{SampleRate: 48000, Track: 0} }

func ev(key int) score.NoteEvent { return score.NoteEvent{Track: 0, Key: key} }

func det(start int64, key int, cents float64) pitch.Note {
	return pitch.Note{Start: start, End: start + 4800, Key: key, Cents: cents, Clarity: 0.95}
}

func drain(s *Scorer) []NoteResult {
	s.Advance(1 << 40)
	return s.Results(nil)
}

func slideTo(start, end int64, key, semitones int) pitch.Note {
	to := float64(semitones) * 100
	lo, hi := 0.0, to
	if to < 0 {
		lo, hi = to, 0
	}
	return pitch.Note{
		Start:    start,
		End:      end,
		Key:      key,
		Cents:    0,
		MinCents: lo,
		MaxCents: hi,
		EndCents: to,
		Clarity:  0.95,
	}
}

func slideEv(key int) score.NoteEvent {
	return score.NoteEvent{Track: 0, Key: key, Tech: score.TechSlide}
}

func TestScorerPerfectPerformance(t *testing.T) {
	s := NewScorer(testConfig())
	keys := []int{40, 43, 45, 40, 43, 45}
	for i, k := range keys {
		s.ExpectNote(ev(k), int64(i)*12000)
	}

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
		s.ExpectNote(ev(52), 24000)
		s.ExpectNote(ev(40), 24000)
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
	s.ExpectNote(ev(45), 24000)
	s.Reset()
	if st := s.Stats(); st != (Stats{}) {
		t.Errorf("stats after Reset: %+v, want zero", st)
	}
	if rs := drain(s); len(rs) != 0 {
		t.Errorf("results after Reset: %d, want 0 (pending cleared, no ghost Miss)", len(rs))
	}
}

func TestScorerAbandonBefore(t *testing.T) {

	const seekFrame = 30000

	tests := []struct {
		name      string
		expFrame  int64
		detect    bool
		wantCount int
		want      Verdict
	}{
		{
			name:      "sounded before the seek, unanswered: dropped, not missed",
			expFrame:  24000,
			wantCount: 0,
		},
		{
			name:      "sounded before the seek, answered in flight: still a Hit",
			expFrame:  24000,
			detect:    true,
			wantCount: 1,
			want:      VerdictHit,
		},
		{
			name:      "sounded after the seek, unanswered: still a Miss",
			expFrame:  36000,
			wantCount: 1,
			want:      VerdictMiss,
		},
		{
			name:      "sounded after the seek, answered: a Hit",
			expFrame:  36000,
			detect:    true,
			wantCount: 1,
			want:      VerdictHit,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			s := NewScorer(testConfig())
			s.ExpectNote(ev(40), c.expFrame)
			s.AbandonBefore(seekFrame)
			if c.detect {

				s.Detected([]pitch.Note{det(c.expFrame+900, 40, 3)})
			}
			rs := drain(s)
			if len(rs) != c.wantCount {
				t.Fatalf("got %d results %+v, want %d", len(rs), rs, c.wantCount)
			}
			if c.wantCount == 1 && rs[0].Verdict != c.want {
				t.Errorf("verdict = %v, want %v", rs[0].Verdict, c.want)
			}
		})
	}
}

func TestScorerAbandonKeepsEarnedStats(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 0)
	s.Detected([]pitch.Note{det(0, 40, 0)})
	s.ExpectNote(ev(45), 24000)

	s.AbandonBefore(30000)

	if st := s.Stats(); st != (Stats{Hit: 1}) {
		t.Errorf("stats after AbandonBefore = %+v, want one Hit preserved", st)
	}
	rs := drain(s)
	if len(rs) != 1 || rs[0].Verdict != VerdictHit {
		t.Fatalf("results = %+v, want the earned Hit and no ghost Miss", rs)
	}
	if st := s.Stats(); st.Miss != 0 {
		t.Errorf("stats = %+v, want no Miss invented by the seek", st)
	}
}

func TestScorerAbandonDropsStaleWaitConfirm(t *testing.T) {
	s := NewScorer(testConfig())
	waited := score.NoteEvent{Track: 0, Key: 40, Start: 960}
	s.WaitConfirmed([]score.NoteEvent{waited}, []pitch.Note{det(0, 40, 2)})

	s.AbandonBefore(1)

	s.ExpectNote(waited, 48000)
	if rs := s.Results(nil); len(rs) != 0 {
		t.Fatalf("stale wait confirm fired on a later pass: %+v", rs)
	}
	if rs := drain(s); len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Errorf("results = %+v, want the re-passed note judged normally (Miss, unplayed)", rs)
	}
}

func TestScorerLiveExpectationOutranksAbandoned(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.ExpectNote(ev(40), 30000)
	s.AbandonBefore(27000)

	s.Detected([]pitch.Note{det(27000, 40, 0)})

	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results %+v, want only the live expectation's", len(rs), rs)
	}
	if rs[0].OutFrame != 30000 || rs[0].Verdict != VerdictHit {
		t.Errorf("got %+v, want a Hit on the live expectation at 30000", rs[0])
	}
	if st := s.Stats(); st.Miss != 0 {
		t.Errorf("stats %+v, want no Miss: the seek must not invent one", st)
	}
}

func TestScorerSlideDestinationIsMatchable(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.ExpectNote(slideEv(41), 39000)
	s.Detected([]pitch.Note{slideTo(24000, 54000, 40, 1)})

	rs := drain(s)
	if len(rs) != 2 {
		t.Fatalf("got %d results %+v, want both notes judged", len(rs), rs)
	}
	byFrame := map[int64]NoteResult{}
	for _, r := range rs {
		byFrame[r.OutFrame] = r
	}
	if r := byFrame[24000]; r.Verdict != VerdictHit || !r.Matched {
		t.Errorf("picked note: got %+v, want a matched Hit", r)
	}
	r := byFrame[39000]
	if r.Verdict != VerdictHit || !r.Matched {
		t.Errorf("slide destination: got %+v, want a matched Hit — a slide played right cannot be a Miss", r)
	}

	if r.ErrFrames != 0 {
		t.Errorf("slide destination ErrFrames %d, want 0", r.ErrFrames)
	}
	if st := s.Stats(); st != (Stats{Hit: 2}) {
		t.Errorf("stats %+v, want 2 Hits", st)
	}
}

func TestScorerSlideChainCreditsEveryStep(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.ExpectNote(slideEv(41), 39000)
	s.ExpectNote(slideEv(43), 54000)
	s.Detected([]pitch.Note{slideTo(24000, 69000, 40, 3)})

	rs := drain(s)
	want := map[int64]Verdict{24000: VerdictHit, 39000: VerdictClose, 54000: VerdictHit}
	if len(rs) != len(want) {
		t.Fatalf("got %d results %+v, want %d", len(rs), rs, len(want))
	}
	for _, r := range rs {
		if r.Verdict != want[r.OutFrame] {
			t.Errorf("outFrame %d: verdict %v, want %v", r.OutFrame, r.Verdict, want[r.OutFrame])
		}
		if !r.Matched {
			t.Errorf("outFrame %d: Matched false", r.OutFrame)
		}
	}
}

func TestScorerSlideCreditIsNarrow(t *testing.T) {
	tests := []struct {
		name string
		exp  score.NoteEvent
		det  pitch.Note
	}{
		{

			name: "a plain expectation is never credited by a glide",
			exp:  ev(41),
			det:  slideTo(24000, 54000, 40, 1),
		},
		{
			name: "the trajectory never reached the destination",
			exp:  slideEv(45),
			det:  slideTo(24000, 54000, 40, 1),
		},
		{
			name: "the note never moved at all",
			exp:  slideEv(41),
			det:  det(24000, 40, 0),
		},
		{

			name: "the note had already stopped sounding",
			exp:  slideEv(41),
			det:  slideTo(24000, 30000, 40, 1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewScorer(testConfig())
			s.ExpectNote(tt.exp, 39000)
			s.Detected([]pitch.Note{tt.det})
			rs := drain(s)
			if len(rs) != 1 {
				t.Fatalf("got %d results, want 1: %+v", len(rs), rs)
			}
			if rs[0].Verdict != VerdictMiss || rs[0].Matched {
				t.Errorf("got %+v, want an unmatched Miss", rs[0])
			}
		})
	}
}

func TestScorerWaitConfirmedDeadNote(t *testing.T) {
	s := NewScorer(testConfig())

	dead := score.NoteEvent{Track: 0, Key: 45, String: 4, Start: 3840, Tech: score.TechDead}
	s.WaitConfirmed([]score.NoteEvent{dead}, nil)
	s.ExpectNote(dead, 48000)

	rs := s.Results(nil)
	if len(rs) != 1 {
		t.Fatalf("got %d results %+v, want the dead note finalized on arrival", len(rs), rs)
	}
	r := rs[0]
	if r.Verdict != VerdictClose || !r.Matched {
		t.Errorf("got %+v, want a matched Close: the attack was heard, the pitch never was", r)
	}
	if r.ErrCents != 0 || r.ErrFrames != 0 {
		t.Errorf("got %+v, want no error figures for a note with no measurable pitch", r)
	}

	s.ExpectNote(dead, 96000)
	if rs := drain(s); len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Errorf("second pass results %+v, want a normal Miss", rs)
	}
}

func TestScorerWaitConfirmedSkipsUnconfirmablePitchedNotes(t *testing.T) {
	s := NewScorer(testConfig())
	waited := score.NoteEvent{Track: 0, Key: 40, Start: 960}
	s.WaitConfirmed([]score.NoteEvent{waited}, []pitch.Note{det(0, 47, 0)})
	s.ExpectNote(waited, 48000)
	if rs := s.Results(nil); len(rs) != 0 {
		t.Fatalf("a pitched event with no matching detection was pre-matched: %+v", rs)
	}
}

func TestScorerIgnoresOtherTracks(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(score.NoteEvent{Track: 1, Key: 40}, 24000)
	if rs := drain(s); len(rs) != 0 {
		t.Errorf("track-1 event judged on a track-0 scorer: %+v", rs)
	}
}

func TestScorerEarliestDeadlineNotNearest(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.ExpectNote(ev(40), 33600)

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

func wev(key int, start int64) score.NoteEvent {
	return score.NoteEvent{Track: 0, Key: key, Start: start}
}

func TestScorerWaitConfirmedStaccato(t *testing.T) {
	s := NewScorer(testConfig())
	e := wev(40, 960)
	n := det(1000, 40, 5)
	s.Detected([]pitch.Note{n})
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{n})
	s.ExpectNote(e, 2000)
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

func TestScorerWaitConfirmedWithLatencyOffset(t *testing.T) {
	s := NewScorer(Config{SampleRate: 48000, Track: 0, LatencyOffsetFrames: 4800})
	e := wev(40, 1920)
	n := det(10000, 40, 3)
	s.Detected([]pitch.Note{n})
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{n})

	s.ExpectNote(e, 14000)
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	if rs[0].Verdict != VerdictHit || rs[0].ErrFrames != 0 {
		t.Errorf("got verdict %v ErrFrames %d, want Hit with ErrFrames 0", rs[0].Verdict, rs[0].ErrFrames)
	}
}

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

func TestScorerWaitPreMatchConsumedOnce(t *testing.T) {
	s := NewScorer(testConfig())
	e := wev(40, 960)
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{det(1000, 40, 0)})
	s.ExpectNote(e, 2000)
	s.ExpectNote(e, 50000)
	rs := drain(s)
	if len(rs) != 2 {
		t.Fatalf("got %d results, want 2", len(rs))
	}
	st := s.Stats()
	if st.Hit != 1 || st.Miss != 1 {
		t.Errorf("stats %+v, want exactly 1 Hit and 1 Miss (pre-match consumed once)", st)
	}
}

func TestScorerWaitPreMatchExpires(t *testing.T) {
	s := NewScorer(testConfig())
	e := wev(40, 960)
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{det(1000, 40, 0)})
	s.Advance(6 * 48000)
	s.ExpectNote(e, 6*48000+2000)
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	if rs[0].Verdict != VerdictMiss {
		t.Errorf("verdict %v, want Miss (stale pre-match must not credit a later pass)", rs[0].Verdict)
	}
}

func TestScorerWaitConfirmedIgnoresOtherTracks(t *testing.T) {
	s := NewScorer(testConfig())
	e := score.NoteEvent{Track: 1, Key: 40, Start: 960}
	s.WaitConfirmed([]score.NoteEvent{e}, []pitch.Note{det(1000, 40, 0)})

	s.ExpectNote(wev(40, 960), 2000)
	rs := drain(s)
	if len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Errorf("got %+v, want one Miss (track-1 confirmation must not pre-match track 0)", rs)
	}
}

func TestScorerConcurrencySmoke(t *testing.T) {
	const n = 2000
	s := NewScorer(testConfig())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []NoteResult

	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.ExpectNote(ev(40+i%12), int64(i)*12000)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.Detected([]pitch.Note{det(int64(i)*12000+500, 40+i%12, 5)})
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
	if len(all) != n {
		t.Fatalf("got %d results, want %d (every expectation judged exactly once)", len(all), n)
	}
	st := s.Stats()
	if st.Hit+st.Close+st.Miss != n {
		t.Errorf("stats total %d, want %d", st.Hit+st.Close+st.Miss, n)
	}

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

func TestScorerWaitConfirmedUnison(t *testing.T) {
	s := NewScorer(Config{SampleRate: 48000, Track: 0})
	e1 := score.NoteEvent{Track: 0, Key: 64, Start: 960, String: 1}
	e2 := score.NoteEvent{Track: 0, Key: 64, Start: 960, String: 2}
	det := []pitch.Note{{Key: 64, Cents: 3, Start: 1000, End: 3000}}

	s.WaitConfirmed([]score.NoteEvent{e1, e2}, det)
	s.ExpectNote(e1, 2000)
	s.ExpectNote(e2, 2000)

	rs := s.Results(nil)
	if len(rs) != 2 {
		t.Fatalf("got %d results, want 2 — both unison notes credited: %+v", len(rs), rs)
	}
	for i, r := range rs {
		if r.Verdict != VerdictHit || !r.Matched {
			t.Errorf("result %d = %+v, want a matched Hit", i, r)
		}
		if r.ErrCents != 3 {
			t.Errorf("result %d ErrCents = %v, want the detection's 3", i, r.ErrCents)
		}
	}
	if st := s.Stats(); st.Hit != 2 || st.Miss != 0 {
		t.Errorf("stats = %+v, want 2 hits and no misses", st)
	}
}

func TestScorerWaitConfirmedUnisonConsumedExactly(t *testing.T) {
	s := NewScorer(Config{SampleRate: 48000, Track: 0})
	e1 := score.NoteEvent{Track: 0, Key: 64, Start: 960, String: 1}
	e2 := score.NoteEvent{Track: 0, Key: 64, Start: 960, String: 2}
	det := []pitch.Note{{Key: 64, Cents: 1, Start: 1000, End: 3000}}

	s.WaitConfirmed([]score.NoteEvent{e1, e2}, det)
	s.WaitConfirmed([]score.NoteEvent{e1, e2}, det)
	s.ExpectNote(e1, 2000)
	s.ExpectNote(e2, 2000)
	if rs := s.Results(nil); len(rs) != 2 {
		t.Fatalf("after consuming both entries got %d results, want 2", len(rs))
	}

	s.ExpectNote(e1, 10000)
	s.Advance(1 << 40)
	rs := s.Results(nil)
	if len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Fatalf("loop-pass expectation = %+v, want exactly one normally-judged Miss", rs)
	}
}

func TestScorerSlideNotCreditedByASustainedNote(t *testing.T) {
	s := NewScorer(Config{SampleRate: 48000})

	s.ExpectNote(score.NoteEvent{Key: 64, Start: 0}, 0)

	s.ExpectNote(score.NoteEvent{Key: 64, Start: 960, Tech: score.TechSlide}, 48000)

	s.Detected([]pitch.Note{{Start: 0, End: 96000, Key: 64, Cents: 5, MinCents: -4, MaxCents: 6, EndCents: 2}})

	res := s.Results(nil)
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1 — one played note must not also credit an unplayed slide: %+v", len(res), res)
	}
	if res[0].Event.Start != 0 {
		t.Errorf("credited the expectation at tick %d, want the one actually played (tick 0)", res[0].Event.Start)
	}
}
