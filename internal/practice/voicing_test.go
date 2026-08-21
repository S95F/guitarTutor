package practice

import (
	"testing"

	"github.com/S95F/musicTutor/internal/pitch"
)

// Chord-verification coverage beyond the E shape.
//
// Every chord fixture this package had before this file was an E-shaped
// voicing on keys 40/47/52 (+56/59/64) — two or three pitch classes, the
// root doubled, the fold's cleanest possible case. The synthetic 16 hit /
// 0 miss round trip therefore generalized to exactly one chord shape, and
// the verdict ladder's real behavior on an open G, an Am or a C went
// unmeasured. The chromas below close that gap.
//
// PROVENANCE. These are not invented numbers. Each vector is the chroma of
// the FIRST pitch.Strum the real detector emitted for that voicing,
// rendered through the real Karplus-Strong pluck at 48 kHz with
// pitch.DefaultConfig and Strums on, at three strum speeds: 0 ms (a block
// chord, every string at once), 12 ms (a brisk downstroke — 2.4 ms between
// strings) and 25 ms (a relaxed one). They are frozen as literals on
// purpose: internal/pitch's fold is under active improvement, and the
// verdict LADDER has to be judged on fixed evidence, not on evidence that
// shifts underneath it. If the fold improves, these get re-measured; the
// assertions below are about what practice does with what it is handed.
//
// What the measurements show, and why the ladder needed three rungs:
// chroma quality falls off a cliff outside the E shape. On an open G at
// 25 ms the played B string lands at 0.39 of the peak while the UNPLAYED
// C# sits at 0.48 above it; on a 12 ms Am the played C (0.74) is outranked
// by an unplayed D# (0.80). Under the old two-outcome rule (>= 0.45 of
// peak or a hard Miss) those are false "you missed" verdicts on correctly
// fretted strings — the failure docs/DECISIONS.md D5 calls the worst one
// available to this package.
var (
	// E major, E shape, six strings: keys 40/47/52/56/59/64.
	chE6Block = pitch.Chroma{0.277, 0.360, 0.377, 0.552, 1.000, 0.499, 0.377, 0.203, 0.920, 0.318, 0.423, 0.778}
	chE6Sweep = pitch.Chroma{0.316, 0.324, 0.398, 0.421, 1.000, 0.571, 0.482, 0.193, 0.556, 0.233, 0.208, 0.795}
	// Open G: keys 43/47/50/55/59/67.
	chGBlock  = pitch.Chroma{0.161, 0.181, 0.816, 0.616, 0.152, 0.189, 0.341, 1.000, 0.433, 0.464, 0.382, 0.795}
	chGSweep  = pitch.Chroma{0.611, 0.786, 1.000, 0.607, 0.396, 0.493, 0.518, 1.000, 0.773, 0.538, 0.312, 0.891}
	chGSlow   = pitch.Chroma{0.252, 0.480, 0.736, 0.284, 0.175, 0.260, 0.352, 1.000, 0.454, 0.242, 0.341, 0.387}
	chAmBlock = pitch.Chroma{0.764, 0.525, 0.288, 0.306, 0.831, 0.083, 0.092, 0.231, 0.453, 1.000, 0.301, 0.254}
	chAmSweep = pitch.Chroma{0.736, 0.503, 0.303, 0.798, 0.870, 0.203, 0.236, 0.379, 0.486, 1.000, 0.478, 0.511}
	chCBlock  = pitch.Chroma{1.000, 0.219, 0.409, 0.086, 0.690, 0.191, 0.243, 0.498, 0.225, 0.196, 0.134, 0.209}
	chCSweep  = pitch.Chroma{1.000, 0.150, 0.466, 0.407, 0.763, 0.283, 0.243, 0.843, 0.402, 0.356, 0.255, 0.362}
	chCSlow   = pitch.Chroma{1.000, 0.189, 0.375, 0.326, 0.499, 0.195, 0.280, 0.598, 0.290, 0.195, 0.194, 0.516}
	chDSlow   = pitch.Chroma{0.287, 0.354, 0.668, 0.239, 0.251, 0.265, 0.446, 0.176, 0.189, 1.000, 0.235, 0.220}
	chEmBlock = pitch.Chroma{0.281, 0.320, 0.338, 0.454, 1.000, 0.341, 0.537, 0.624, 0.376, 0.154, 0.390, 0.637}
	chEmSweep = pitch.Chroma{0.278, 0.447, 0.401, 0.449, 0.955, 0.842, 0.288, 0.273, 0.428, 0.123, 0.328, 1.000}
	chEmSlow  = pitch.Chroma{0.237, 0.731, 0.511, 0.430, 1.000, 0.390, 0.414, 0.636, 0.414, 0.184, 0.431, 0.891}
)

// The voicings, by fretting hand rather than by pitch class.
var (
	keysE6 = []int{40, 47, 52, 56, 59, 64} // E major, open
	keysG  = []int{43, 47, 50, 55, 59, 67} // G major, open
	keysAm = []int{45, 52, 57, 60, 64}     // A minor, open
	keysA  = []int{45, 52, 57, 61, 64}     // A major, open
	keysC  = []int{48, 52, 55, 60, 64}     // C major, open
	keysD  = []int{50, 57, 62, 66}         // D major, open
	keysEm = []int{40, 47, 52, 55, 59, 64} // E minor, open
)

// near compares two float64s that came through float32 chroma bins.
func near(got, want float64) bool { return got-want < 1e-6 && want-got < 1e-6 }

// tally counts the verdicts in a result set.
func tally(rs []NoteResult) Stats {
	var st Stats
	for _, r := range rs {
		switch r.Verdict {
		case VerdictHit:
			st.Hit++
		case VerdictClose:
			st.Close++
		default:
			st.Miss++
		}
	}
	return st
}

// scoreVoicing taps keys as one chord and judges it from one strum
// carrying ch, draining anything the strum left pending.
func scoreVoicing(keys []int, ch pitch.Chroma) []NoteResult {
	s := NewScorer(testConfig())
	expectChord(s, keys, 1920, 24000)
	s.DetectedStrum(strumAt(24000, ch))
	return drain(s)
}

// missedKeys lists the keys a result set scored a hard Miss.
func missedKeys(rs []NoteResult) []int {
	var out []int
	for _, r := range rs {
		if r.Verdict == VerdictMiss {
			out = append(out, r.Event.Key)
		}
	}
	return out
}

// TestChordVoicingsNoFalseMiss is the D5 guard, and the reason chord
// verification has a Close rung at all: a correctly fretted, correctly
// strummed chord must never tell the player they missed a string.
//
// The expected distributions are pinned rather than merely bounded so a
// future tuning change has to say out loud what it costs. Hit means the
// class dominated the strum, Close means it was audible but outranked by
// mush the player did not play — honest partial credit, not a failure.
func TestChordVoicingsNoFalseMiss(t *testing.T) {
	tests := []struct {
		name string
		keys []int
		ch   pitch.Chroma
		want Stats
	}{
		{"E major, block chord", keysE6, chE6Block, Stats{Hit: 6}},
		// G#3 (key 56) is outranked by an unplayed F at 0.571: Close.
		{"E major, 12ms downstroke", keysE6, chE6Sweep, Stats{Hit: 5, Close: 1}},
		{"open G, block chord", keysG, chGBlock, Stats{Hit: 6}},
		{"open G, 12ms downstroke", keysG, chGSweep, Stats{Hit: 6}},
		// B at 0.387 sits under both the 0.45 ratio and an unplayed C# at
		// 0.480; two B strings in this voicing, so two Closes.
		{"open G, 25ms strum", keysG, chGSlow, Stats{Hit: 4, Close: 2}},
		{"A minor, block chord", keysAm, chAmBlock, Stats{Hit: 5}},
		// C4 (0.736) is outranked by an unplayed D# at 0.798: Close.
		{"A minor, 12ms downstroke", keysAm, chAmSweep, Stats{Hit: 4, Close: 1}},
		{"C major, block chord", keysC, chCBlock, Stats{Hit: 5}},
		{"C major, 12ms downstroke", keysC, chCSweep, Stats{Hit: 5}},
		// E (0.499) is outranked by an unplayed B at 0.516; two E strings.
		{"C major, 25ms strum", keysC, chCSlow, Stats{Hit: 3, Close: 2}},
		// F#4 at 0.446 just under the 0.45 ratio: Close, not a Miss.
		{"D major, 25ms strum", keysD, chDSlow, Stats{Hit: 3, Close: 1}},
		{"E minor, block chord", keysEm, chEmBlock, Stats{Hit: 6}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := scoreVoicing(tt.keys, tt.ch)
			got := tally(rs)
			t.Logf("%s: %d Hit, %d Close, %d Miss (accuracy %.3f)",
				tt.name, got.Hit, got.Close, got.Miss, got.Accuracy())
			if len(rs) != len(tt.keys) {
				t.Fatalf("judged %d of %d strings", len(rs), len(tt.keys))
			}
			// The D5 invariant. Everything else in this test is a pin;
			// this one is the contract.
			if got.Miss != 0 {
				t.Errorf("correctly played %s scored a hard Miss on keys %v — the false "+
					"negative D5 forbids; distribution %+v", tt.name, missedKeys(rs), got)
			}
			if got != tt.want {
				t.Errorf("distribution %+v, want %+v", got, tt.want)
			}
			for _, r := range rs {
				if r.Verdict != VerdictMiss && !r.Matched {
					t.Errorf("key %d: credited %v but Matched false", r.Event.Key, r.Verdict)
				}
				if r.ErrCents != 0 {
					t.Errorf("key %d: ErrCents %v, want 0 (chroma carries no cents)", r.Event.Key, r.ErrCents)
				}
			}
		})
	}
}

// TestChordVoicingEmSweepKnownWeakness documents the one correctly played
// string in the measured corpus that this package cannot save, so nobody
// reads the test above as a claim that false misses are impossible.
//
// On a 12 ms Em downstroke the fold puts the open G string's class at
// 0.273 — the SECOND QUIETEST of the twelve bins, below the chroma's own
// background. There is no verdict policy that can distinguish that from a
// string which never sounded; the evidence is simply not in the chroma,
// and the fix belongs in internal/pitch (strum span, pre-onset baseline).
// What this package can guarantee is that the damage is bounded to that
// one string.
func TestChordVoicingEmSweepKnownWeakness(t *testing.T) {
	rs := scoreVoicing(keysEm, chEmSweep)
	got := tally(rs)
	t.Logf("Em 12ms downstroke: %d Hit, %d Close, %d Miss on keys %v", got.Hit, got.Close, got.Miss, missedKeys(rs))
	if got.Miss > 1 {
		t.Errorf("%d hard Misses on a correctly played Em, want at most the known G-string one: %+v", got.Miss, got)
	}
	for _, k := range missedKeys(rs) {
		if k != 55 {
			t.Errorf("hard Miss on key %d; only the G string (55) is a known-weak class here", k)
		}
	}
}

// TestWrongChordDoesNotSweepHits: presence judged only against the strum's
// own peak cannot tell a chord from its relative minor, because two of the
// three expected classes really are sounding and harmonic mush lifts the
// third over the ratio. Both pairs below scored a CLEAN SWEEP of Hits
// before the discrimination test existed — the app cheerfully certifying a
// chord the player did not play.
//
// The bar is deliberately low and one-sided: a wrong chord must not be
// fully certified. Shared notes are still real, so partial credit for them
// is correct, and D5 forbids turning this into a punishment path.
func TestWrongChordDoesNotSweepHits(t *testing.T) {
	tests := []struct {
		name     string
		expected []int
		played   pitch.Chroma
	}{
		{"Am played, A major expected (block)", keysA, chAmBlock},
		{"Am played, A major expected (12ms)", keysA, chAmSweep},
		{"Em played, open G expected (block)", keysG, chEmBlock},
		{"Em played, open G expected (25ms)", keysG, chEmSlow},
		{"C played, open G expected (block)", keysG, chCBlock},
		{"G played, A major expected (block)", keysA, chGBlock},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tmpl [pitch.PitchClasses]bool
			for _, k := range tt.expected {
				tmpl[pitch.ChromaOf(k)] = true
			}
			rs := scoreVoicing(tt.expected, tt.played)
			got := tally(rs)
			t.Logf("%s: %d Hit, %d Close, %d Miss (correlation %.2f, %d strings)",
				tt.name, got.Hit, got.Close, got.Miss, chordCorrelation(tt.played, tmpl), len(tt.expected))
			if got.Hit == len(tt.expected) {
				t.Errorf("a chord the player did not play verified as %d/%d Hits", got.Hit, len(tt.expected))
			}
		})
	}
}

// TestChordVerificationNeverHarsherThanTheFallback is the C1/C2 contract in
// one assertion, and the regression that made a Close rung mandatory.
//
// deadlineResult already graded an unmatched expectation with an onset in
// its window on chroma evidence, and gave it Close. verifyChord, the path
// added to score chords BETTER, had only Hit and Miss — so the identical
// strum could score a note Close when it stood alone and a hard Miss when
// it belonged to a chord group. Same audio, same pitch class, same
// instant, opposite verdicts, with the harsher one on the path advertised
// as the improvement. Whatever else the two paths disagree about, chord
// verification may never be the harsher of the two.
func TestChordVerificationNeverHarsherThanTheFallback(t *testing.T) {
	corpus := []struct {
		name string
		keys []int
		ch   pitch.Chroma
	}{
		{"E major block", keysE6, chE6Block},
		{"E major 12ms", keysE6, chE6Sweep},
		{"open G block", keysG, chGBlock},
		{"open G 12ms", keysG, chGSweep},
		{"open G 25ms", keysG, chGSlow},
		{"Am block", keysAm, chAmBlock},
		{"Am 12ms", keysAm, chAmSweep},
		{"C block", keysC, chCBlock},
		{"C 12ms", keysC, chCSweep},
		{"C 25ms", keysC, chCSlow},
		{"D 25ms", keysD, chDSlow},
		{"Em 12ms", keysEm, chEmSweep},
		{"Am against A", keysA, chAmBlock},
		{"Em against G", keysG, chEmBlock},
	}
	for _, c := range corpus {
		t.Run(c.name, func(t *testing.T) {
			chordV := map[int]Verdict{}
			for _, r := range scoreVoicing(c.keys, c.ch) {
				chordV[r.Event.Key] = r.Verdict
			}
			// The same strum, but each key alone on its own tick, so no
			// chord group forms and every note falls to deadlineResult.
			for i, k := range c.keys {
				s := NewScorer(testConfig())
				out := int64(24000 + i*48000)
				s.ExpectNote(ev(k), out)
				s.DetectedStrum(strumAt(out, c.ch))
				rs := drain(s)
				if len(rs) != 1 {
					t.Fatalf("key %d: %d results, want 1", k, len(rs))
				}
				if chordV[k] == VerdictMiss && rs[0].Verdict != VerdictMiss {
					t.Errorf("key %d: chord verification says Miss but the fallback path says %v "+
						"on the identical strum — chord scoring must not be the harsher path",
						k, rs[0].Verdict)
				}
			}
		})
	}
}

// TestMuteEvidenceIsBackgroundRelative pins C4: the Close rung has to be a
// test, not a formality.
//
// A fraction of the PEAK is not a test on a full strum. The octave fold
// lights all twelve bins, so the quietest class of a six-string chord
// still measures 0.19-0.28 of the peak — a 0.20-of-peak floor admits
// essentially every pitch class, which is why the correlation gate's
// rejection bought nothing: every note of the rejected chord collected a
// Close at its deadline regardless. Prominence (background 0, peak 1) is
// scale-free, so the same constant means the same thing on a sparse chroma
// and a dense one.
func TestMuteEvidenceIsBackgroundRelative(t *testing.T) {
	corpus := []struct {
		name string
		ch   pitch.Chroma
	}{
		{"E major block", chE6Block}, {"E major 12ms", chE6Sweep},
		{"open G block", chGBlock}, {"open G 12ms", chGSweep}, {"open G 25ms", chGSlow},
		{"Am block", chAmBlock}, {"Am 12ms", chAmSweep},
		{"C block", chCBlock}, {"C 12ms", chCSweep}, {"C 25ms", chCSlow},
		{"D 25ms", chDSlow},
		{"Em block", chEmBlock}, {"Em 12ms", chEmSweep}, {"Em 25ms", chEmSlow},
	}
	cfg := testConfig().withDefaults()
	oldAdmitted, newAdmitted, bins := 0, 0, 0
	worstFloor := 1.0
	for _, c := range corpus {
		stats := chromaStatsOf(c.ch)
		floor := 1.0
		for _, v := range c.ch {
			bins++
			if float64(v)/stats.peak < floor {
				floor = float64(v) / stats.peak
			}
			if float64(v) >= 0.20*stats.peak { // the superseded rule
				oldAdmitted++
			}
			if stats.prominence(float64(v)) >= cfg.MuteEnergyRatio {
				newAdmitted++
			}
		}
		if floor < worstFloor {
			worstFloor = floor
		}
		// The saturation itself: on a full strum the QUIETEST bin already
		// clears a fifth of the peak, so the old rule could not fail.
		t.Logf("%-14s peak %.3f background %.3f quietest-bin/peak %.3f", c.name, stats.peak, stats.bg, floor)
	}
	t.Logf("mute rung admits %d/%d pitch classes (was %d/%d under 0.20-of-peak); "+
		"loosest quietest-bin/peak across the corpus %.3f", newAdmitted, bins, oldAdmitted, bins, worstFloor)
	if newAdmitted >= oldAdmitted {
		t.Errorf("the background-relative rule admits %d/%d classes, no fewer than the "+
			"0.20-of-peak rule's %d/%d: it is still a formality", newAdmitted, bins, oldAdmitted, bins)
	}
	// A featureless chroma must credit nothing at all, on either path.
	var flat pitch.Chroma
	for i := range flat {
		flat[i] = 0.7
	}
	fs := chromaStatsOf(flat)
	for i := range flat {
		if fs.prominence(float64(flat[i])) >= cfg.MuteEnergyRatio {
			t.Fatalf("a flat chroma credited class %d: noise must never be evidence", i)
		}
	}
}

// TestChromaStats pins the two statistics every verdict reads.
func TestChromaStats(t *testing.T) {
	// Background is the lower-quartile bin (4th smallest of 12), not the
	// median: on a real chord the median sits inside the chord's own
	// energy, and correctly played strings land below it.
	ch := pitch.Chroma{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0, 1.1, 1.2}
	st := chromaStatsOf(ch)
	if !near(st.peak, 1.2) {
		t.Errorf("peak %v, want 1.2", st.peak)
	}
	if !near(st.bg, 0.4) {
		t.Errorf("background %v, want the 4th smallest bin (0.4)", st.bg)
	}
	if p := st.prominence(1.2); !near(p, 1) {
		t.Errorf("prominence of the peak = %v, want 1", p)
	}
	if p := st.prominence(0.4); !near(p, 0) {
		t.Errorf("prominence of the background = %v, want 0", p)
	}
	if p := st.prominence(0.1); p >= 0 {
		t.Errorf("prominence below the background = %v, want negative", p)
	}
	// Degenerate chromas must not credit anything.
	if p := (chromaStatsOf(pitch.Chroma{})).prominence(0); p != 0 {
		t.Errorf("all-zero chroma prominence = %v, want 0", p)
	}
}

// TestRivalBin: the discrimination bar is the loudest class the chord does
// not contain.
func TestRivalBin(t *testing.T) {
	var tmpl [pitch.PitchClasses]bool
	tmpl[pcE], tmpl[pcB] = true, true
	ch := chroma(0.15, pcE, pcB)
	ch[pcG] = 0.62
	if got := rivalBin(ch, tmpl); !near(got, 0.62) {
		t.Errorf("rival = %v, want the loudest non-template bin (0.62)", got)
	}
	var all [pitch.PitchClasses]bool
	for i := range all {
		all[i] = true
	}
	if got := rivalBin(ch, all); got != 0 {
		t.Errorf("rival with everything templated = %v, want 0", got)
	}
}
