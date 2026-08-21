package practice

import (
	"sync"
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
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

// slideTo builds the detection a legato slide produces: ONE note that keeps
// the origin key and moves semitones away from it (pitch.Tracker). Cents —
// the median over the whole note — sits at the origin, where the note spent
// most of its length, which is precisely why it cannot report the slide;
// only the trajectory fields can.
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

// slideEv builds a track-0 slide DESTINATION expectation: the note the
// score writes at the destination pitch, on its own tick.
func slideEv(key int) score.NoteEvent {
	return score.NoteEvent{Track: 0, Key: key, Tech: score.TechSlide}
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

// TestScorerAbandonBefore is the seek regression: a seek or loop edit
// truncates the answering window of whatever just sounded, and Advance
// used to age those expectations into Misses seconds later — a false "you
// missed" for notes the player never got their window on (D5).
func TestScorerAbandonBefore(t *testing.T) {
	// A seek at output frame 30000, with one expectation on each side.
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
				// The tracker closes the note after the seek:
				// the player did answer, so the credit stands.
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

// TestScorerAbandonKeepsEarnedStats: abandoning must not touch accumulated
// judgements — the accuracy HUD would otherwise reset every time the user
// pressed an arrow key. This is why the fix is not Scorer.Reset, which
// clears stats and results wholesale.
func TestScorerAbandonKeepsEarnedStats(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 0)
	s.Detected([]pitch.Note{det(0, 40, 0)}) // judged Hit before the seek
	s.ExpectNote(ev(45), 24000)             // still pending when the seek lands

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

// TestScorerAbandonDropsStaleWaitConfirm: a seek abandons a wait point, so
// its recorded confirmation must not fire on a later pass over the same
// tick. preMatchExpirySeconds could only approximate this before the
// engine exposed a discontinuity frame.
func TestScorerAbandonDropsStaleWaitConfirm(t *testing.T) {
	s := NewScorer(testConfig())
	waited := score.NoteEvent{Track: 0, Key: 40, Start: 960}
	s.WaitConfirmed([]score.NoteEvent{waited}, []pitch.Note{det(0, 40, 2)})

	s.AbandonBefore(1) // the confirm was recorded at clock 0

	// The same tick comes round again on a later pass: it must be judged
	// on its merits, not released by the abandoned confirmation.
	s.ExpectNote(waited, 48000)
	if rs := s.Results(nil); len(rs) != 0 {
		t.Fatalf("stale wait confirm fired on a later pass: %+v", rs)
	}
	if rs := drain(s); len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Errorf("results = %+v, want the re-passed note judged normally (Miss, unplayed)", rs)
	}
}

// TestScorerLiveExpectationOutranksAbandoned is the C2 regression. The
// oldest-deadline rule used to run over the whole pending set, abandoned
// entries included, so a seek could hand a detection to an expectation
// already marked "must produce no verdict" — starving the live one beside
// it into a Miss. AbandonBefore exists to prevent exactly that Miss, so it
// must not be the thing that causes one: a silent drop plus a Hit turned
// into a Hit plus a false Miss.
func TestScorerLiveExpectationOutranksAbandoned(t *testing.T) {
	s := NewScorer(testConfig()) // window 7200
	s.ExpectNote(ev(40), 24000)  // truncated by the seek
	s.ExpectNote(ev(40), 30000)  // sounded after it: live and scorable
	s.AbandonBefore(27000)
	// One detection, sitting in both windows, nearer the abandoned one's
	// deadline.
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

// TestScorerSlideDestinationIsMatchable is the P2 regression, and the
// bluntest of the lot: before it, EVERY slide in a piece was an expectation
// that could not be matched however perfectly it was played.
//
// A note written with TechSlide is slid INTO — the player does not attack
// it, they move a string that is already sounding. The tracker reports that
// as one continuous note keeping its ORIGIN key (deliberately: see
// pitch.Tracker), while the score writes the destination at the DESTINATION
// pitch on its own tick, so the two could never meet. The fixture case is
// testdata/fixture_rich.gtab bar 2, `4.4.8p 5.4.8s`: two eighths at 96 BPM,
// 15000 frames apart, one sounded string.
func TestScorerSlideDestinationIsMatchable(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)      // picked
	s.ExpectNote(slideEv(41), 39000) // slid into, an eighth later
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
	// The detection's Start is the ORIGIN's attack, a whole beat from the
	// destination by construction; reporting it as timing error would be
	// reporting the notation, not the performance.
	if r.ErrFrames != 0 {
		t.Errorf("slide destination ErrFrames %d, want 0", r.ErrFrames)
	}
	if st := s.Stats(); st != (Stats{Hit: 2}) {
		t.Errorf("stats %+v, want 2 Hits", st)
	}
}

// TestScorerSlideChainCreditsEveryStep: a chained slide is one sounded
// string passing through several written notes. The step it SETTLES on is
// a Hit (its intonation was actually observed); the ones it travelled over
// are Closes — heard, but never held long enough to measure.
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

// TestScorerSlideCreditIsNarrow pins the other half of the P2 fix: legato
// credit must not become a way for any sounding note to satisfy anything
// near it. Each case here is a Miss that SHOULD be a Miss.
func TestScorerSlideCreditIsNarrow(t *testing.T) {
	tests := []struct {
		name string
		exp  score.NoteEvent
		det  pitch.Note
	}{
		{
			// The whole rule is gated on the technique. An ordinary
			// note an eighth away is out of window and stays out.
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
			// A slide is reached from a string still sounding. One
			// that stopped before the destination's window did not
			// reach it, whatever pitch it ended on.
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

// TestScorerWaitConfirmedDeadNote is the scoring half of the P3 fix. A dead
// note produces no trackable f0, so WaitConfirmed can never find a
// detection for one — it used to skip the event entirely, leaving it to a
// deadline that judges it against a window measured from the RELEASE frame,
// which is the machinery's timing rather than the player's. The gate
// confirmed it from its attack (WaitGate.OfferStrum), so the same Close the
// damped-note deadline path awards is recorded up front.
func TestScorerWaitConfirmedDeadNote(t *testing.T) {
	s := NewScorer(testConfig())
	// fixture_rich.gtab bar 2 ends in `9.4x`.
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
	// Consumed like any other pre-match: a later pass over the tick is
	// judged on its own merits.
	s.ExpectNote(dead, 96000)
	if rs := drain(s); len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Errorf("second pass results %+v, want a normal Miss", rs)
	}
}

// TestScorerWaitConfirmedSkipsUnconfirmablePitchedNotes: the dead-note
// path above must stay a dead-note path. A pitched event with no matching
// detection still records nothing.
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

// TestScorerWaitConfirmedUnison is the regression test for audit D3: a
// guitar unison — two expected notes sharing a tick and a MIDI key,
// differing only by string — must produce two pre-matches, so the single
// detection the monophonic tracker can offer credits BOTH notes. The
// pre-match used to be keyed without the string, the second event
// overwrote the first's entry, and the uncredited note aged into a false
// Miss — the project's number-one named rage-quit cause.
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

// TestScorerWaitConfirmedUnisonConsumedExactly: re-confirming the same
// unison updates the two entries in place (never four), and each release
// consumes exactly one — a third expectation at the same tick, a loop
// pass, is judged normally rather than inheriting a stale pre-match.
func TestScorerWaitConfirmedUnisonConsumedExactly(t *testing.T) {
	s := NewScorer(Config{SampleRate: 48000, Track: 0})
	e1 := score.NoteEvent{Track: 0, Key: 64, Start: 960, String: 1}
	e2 := score.NoteEvent{Track: 0, Key: 64, Start: 960, String: 2}
	det := []pitch.Note{{Key: 64, Cents: 1, Start: 1000, End: 3000}}

	s.WaitConfirmed([]score.NoteEvent{e1, e2}, det)
	s.WaitConfirmed([]score.NoteEvent{e1, e2}, det) // re-confirm: replace, not append
	s.ExpectNote(e1, 2000)
	s.ExpectNote(e2, 2000)
	if rs := s.Results(nil); len(rs) != 2 {
		t.Fatalf("after consuming both entries got %d results, want 2", len(rs))
	}

	// The next pass over the same tick has no pre-match left: it joins
	// pending and, never played again, ages into a Miss.
	s.ExpectNote(e1, 10000)
	s.Advance(1 << 40) // far past every window: the deadline judges it
	rs := s.Results(nil)
	if len(rs) != 1 || rs[0].Verdict != VerdictMiss {
		t.Fatalf("loop-pass expectation = %+v, want exactly one normally-judged Miss", rs)
	}
}

// TestScorerSlideNotCreditedByASustainedNote is the fix-verification
// regression for slide matching. A note left ringing across the bar sits
// at its own pitch and never moves; before this, its trajectory trivially
// "reached" any later slide expectation AT THAT SAME PITCH, so one played
// note was credited twice — once through ordinary matching and once as a
// slide the player never performed.
//
// A legato slide is tracked as one note keeping the ORIGIN key, so a real
// slide destination is never the detection's own key. Equal keys mean the
// note did not move.
func TestScorerSlideNotCreditedByASustainedNote(t *testing.T) {
	s := NewScorer(Config{SampleRate: 48000})
	// Beat 1: an ordinary E4, let ring.
	s.ExpectNote(score.NoteEvent{Key: 64, Start: 0}, 0)
	// A second later: a slide INTO E4 (from somewhere else).
	s.ExpectNote(score.NoteEvent{Key: 64, Start: 960, Tech: score.TechSlide}, 48000)
	// The player plays only the first note and lets it ring two seconds.
	s.Detected([]pitch.Note{{Start: 0, End: 96000, Key: 64, Cents: 5, MinCents: -4, MaxCents: 6, EndCents: 2}})

	res := s.Results(nil)
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1 — one played note must not also credit an unplayed slide: %+v", len(res), res)
	}
	if res[0].Event.Start != 0 {
		t.Errorf("credited the expectation at tick %d, want the one actually played (tick 0)", res[0].Event.Start)
	}
}
