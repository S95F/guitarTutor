package practice

import (
	"sync"
	"testing"

	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/score"
)

// Pitch classes used below, spelled out so the chords read as chords.
const (
	pcC  = 0
	pcE  = 4
	pcF  = 5
	pcG  = 7
	pcA  = 9
	pcAs = 10
	pcB  = 11
)

// chroma builds a normalized Chroma: the named pitch classes at 1, every
// other bin at bg (the harmonic mush a real chroma always carries — see
// the measured fixture values in defaultChordPresence's comment).
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

// strumAt builds a Strum at an input frame carrying ch.
func strumAt(frame int64, ch pitch.Chroma) pitch.Strum {
	return pitch.Strum{Frame: frame, Chroma: ch, RMS: 0.1, Clarity: 0.4}
}

// eChord is the fixture's bar-3 chord: E2, B2, E3 — three expectations,
// two pitch classes, all sharing one Start tick.
var eChord = []int{40, 47, 52}

// expectChord taps a chord's notes at one tick and output frame.
func expectChord(s *Scorer, keys []int, tick, outFrame int64) {
	for _, k := range keys {
		s.ExpectNote(score.NoteEvent{Track: 0, Key: k, Start: tick}, outFrame)
	}
}

// TestScorerChordVerifiedFromStrum is the headline Phase 4 regression: the
// monophonic engine can only ever report one note per moment, so through
// Phase 3 a perfectly strummed three-note chord scored 1 Hit and 2 Misses
// (TestScorerChordOneDetection, still there, pins that path unchanged).
// One Strum whose chroma carries every expected pitch class must now
// finalize all three as Hits.
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
		// Chroma is octave-folded energy: a chord Hit asserts presence,
		// never intonation, so it must not claim a cents figure.
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

	// Nothing may re-judge them: the tracker's belated single-f0 reading
	// of the same strum arrives seconds later and must be dropped.
	s.Detected([]pitch.Note{det(25000, 40, 2)})
	if extra := drain(s); len(extra) != 0 {
		t.Errorf("a late detection re-judged finalized chord notes: %+v", extra)
	}
}

// TestScorerChordMutedString: verification is per note, not per chord. One
// string that never spoke must Miss by name while its neighbours Hit.
func TestScorerChordMutedString(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, eChord, 1920, 24000)
	// B2 (key 47, class B) is dead: the class is down in the mush.
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

// TestScorerWrongChordIsNotVerified: the correlation gate exists so a
// chord that is simply not the expected one cannot be read note-by-note —
// with 12 bins and a low threshold, per-bin presence alone would credit
// stray harmonics. Nothing is finalized from the strum; the notes fall to
// their deadlines with no energy at their classes and Miss.
func TestScorerWrongChordIsNotVerified(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, eChord, 1920, 24000) // E and B expected
	// The player hit an F major shape instead: F, A, C. E and B are dead.
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

// TestScorerStrumLeavesSingleNotesToTheMonophonicPath: a Strum knows
// pitch class and nothing else, while the tracker's late note knows
// cents. A lone expectation must therefore survive the strum untouched
// and be judged, with intonation, when the note finally closes.
func TestScorerStrumLeavesSingleNotesToTheMonophonicPath(t *testing.T) {
	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.DetectedStrum(strumAt(24300, chroma(0.15, pcE)))
	if rs := s.Results(nil); len(rs) != 0 {
		t.Fatalf("the strum finalized a single note: %+v", rs)
	}
	// Seconds later the tracker closes the note and delivers it.
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

// TestScorerPalmMuteCredit is the D5 forgiveness case: a heavily damped
// note has an unmistakable attack and a fundamental the tracker can never
// open a note on, so the deadline used to call the most common
// rhythm-guitar technique in the repertoire a Miss. An onset in the window
// plus chroma energy at the expected class now scores Close.
func TestScorerPalmMuteCredit(t *testing.T) {
	// pcE at half the peak: smothered, but unmistakably there.
	muted := chroma(0.05, pcAs)
	muted[pcE] = 0.5
	// Something was struck, but nothing of this note is in it.
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
			s.ExpectNote(ev(40), 24000) // E2
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
			// Credit for the attack is never credit for intonation.
			if r.ErrCents != 0 {
				t.Errorf("ErrCents %v, want 0", r.ErrCents)
			}
		})
	}
}

// TestScorerPalmMuteNeverOverridesADetection: the damped-note path is a
// deadline fallback only. A note the tracker did resolve keeps the verdict
// its cents earned, in either direction.
func TestScorerPalmMuteNeverOverridesADetection(t *testing.T) {
	muted := chroma(0.05, pcAs)
	muted[pcE] = 0.9

	s := NewScorer(testConfig())
	s.ExpectNote(ev(40), 24000)
	s.DetectedStrum(strumAt(24100, muted))
	s.Detected([]pitch.Note{det(24400, 40, 55)}) // right note, sour: Close on cents
	rs := drain(s)
	if len(rs) != 1 {
		t.Fatalf("got %d results, want 1", len(rs))
	}
	if rs[0].Verdict != VerdictClose || rs[0].ErrCents != 55 || rs[0].ErrFrames != 400 {
		t.Errorf("got %+v, want the monophonic Close (55 cents, +400 frames), not the onset's", rs[0])
	}
}

// TestScorerStrumTimingWindow: the acceptance window bounds a Strum just
// as it bounds a detection, and a Strum matching nothing is dropped
// without a trace — the player noodling between phrases is never
// penalized (D5).
func TestScorerStrumTimingWindow(t *testing.T) {
	const out = 24000
	// Default window is 150 ms = 7200 frames at 48 kHz.
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

// TestScorerStrumPicksTheNearestChord: two chords can share a strum's
// window at practice tempos. The strum speaks for the one it landed on.
func TestScorerStrumPicksTheNearestChord(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, []int{40, 47}, 1920, 24000) // E + B
	expectChord(s, []int{48, 55}, 2880, 28000) // C + G
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
	// The earlier chord is untouched and still answerable.
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

// TestScorerStrumBelongsToTheChordItLandedOn is the P1 regression, and the
// nastiest shape the old chooser could take: it picked the LARGEST group
// sharing the strum's window regardless of how far away in time it sat.
//
// 16th-note chord stabs at 120 BPM are 6000 frames apart, comfortably
// inside the 7200-frame window, so a five-note Am on the beat the player
// actually strummed lost to a six-note C/G an entire 16th later. The strum
// then finalized notes the player had not reached yet — judging them
// against audio that was not theirs, two of them into hard Misses — while
// the Am they DID play was left unmatched to age into five more. Proximity
// has to decide.
func TestScorerStrumBelongsToTheChordItLandedOn(t *testing.T) {
	s := NewScorer(testConfig())
	am := []int{45, 52, 57, 60, 64}         // A2 E3 A3 C4 E4 — five notes
	cOverG := []int{43, 48, 52, 55, 60, 64} // G2 C3 E3 G3 C4 E4 — six
	expectChord(s, am, 1920, 24000)
	expectChord(s, cOverG, 2040, 30000)

	// The player strums the Am, a hair late. It is 600 frames from the Am
	// and 5400 from the C/G — both in window, one obviously meant.
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

	// The C/G is untouched, and answered on its own beat.
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

// TestScorerStrumSizeBreaksProximityTies guards the mirror bug. Making
// proximity decide OUTRIGHT would be just as wrong: a strum landing between
// two chords would hand itself to whichever is nearer by a couple of
// milliseconds, which is inside the onset detector's own placement error
// and says nothing about what the player meant. Here a two-note chord is
// 100 frames away and a five-note chord 1100 — 20 ms apart, far below any
// notated rhythm — and the chroma is unmistakably the five-note one.
func TestScorerStrumSizeBreaksProximityTies(t *testing.T) {
	s := NewScorer(testConfig())
	expectChord(s, []int{40, 47}, 1920, 24000)             // E + B
	expectChord(s, []int{43, 48, 52, 55, 60}, 1930, 25200) // C/G

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

// TestScorerStrumAbandonedChord: a seek that cut a chord's answering
// window short still drops it silently rather than inventing a verdict —
// the strum path must not resurrect the false Miss AbandonBefore exists to
// prevent. A strum that genuinely arrived still scores it, exactly as an
// in-flight detection does.
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

// TestChordCorrelation pins the gate's behavior at the three shapes it
// exists to tell apart.
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
	// A degenerate template is not verifiable.
	if got := chordCorrelation(chroma(0.1, pcE), [pitch.PitchClasses]bool{}); got != 0 {
		t.Errorf("empty-template correlation = %v, want 0", got)
	}
}

// TestScorerStrumConcurrencySmoke hammers the scorer from the three
// production roles with DetectedStrum in the analysis mix, and checks
// conservation: every chord expectation judged exactly once, whichever
// path got to it. Written race-clean for -race in CI.
func TestScorerStrumConcurrencySmoke(t *testing.T) {
	const n = 1000 // chords, two notes each
	s := NewScorer(testConfig())
	ch := chroma(0.15, pcE, pcB)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []NoteResult

	wg.Add(3)
	go func() { // engine tap
		defer wg.Done()
		for i := 0; i < n; i++ {
			expectChord(s, []int{40, 47}, int64(i)*480, int64(i)*12000)
		}
	}()
	go func() { // analysis: strums, detections, advance
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.DetectedStrum(strumAt(int64(i)*12000+300, ch))
			s.Detected([]pitch.Note{det(int64(i)*12000+900, 40, 5)})
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
