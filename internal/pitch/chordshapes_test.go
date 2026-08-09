package pitch

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// The chord-shape corpus. Phase 4's chord verification was shipped on the
// strength of a single E-shaped fixture (keys 40/47/52 and 40/47/56), and
// an adversarial review then showed the result did not generalize: on a
// correctly played open G the fold ranked UNSOUNDED classes above sounded
// ones, and on a realistic downstroke sweep half the strings scored Miss.
// docs/DECISIONS.md D5 makes a false "you missed" the worst failure mode
// in the project, so the corpus below exists to make that class of
// regression impossible to ship again: every common open shape, each at
// four strum spreads, asserted on the property the scorer actually needs.
//
// The property: for a correctly played chord, EVERY sounded pitch class
// must rank above EVERY unsounded one. Ordering, not absolute level — the
// scorer compares the chord's own classes against the rest, and a fold can
// never zero the unsounded classes (the third harmonic of E is B, the
// fifth is G#, the ninth is F#).

// A chordShape is one voicing: the MIDI keys of the strings that sound,
// lowest string first, in the order a downstroke hits them.
type chordShape struct {
	name string
	keys []int
}

// chordCorpus covers the open-position shapes a beginner learns first,
// plus a power chord. Standard tuning, MIDI keys (E2 = 40).
//
// Pitch-class content is deliberately varied: open E and Em differ only in
// the G#/G string, open G spans three classes across six strings with the
// root doubled at both ends, open C and open A put the third on an inner
// string, and open D is a four-string shape whose lowest sounded string is
// the D string.
var chordCorpus = []chordShape{
	// E B E G# B E
	{"open E", []int{40, 47, 52, 56, 59, 64}},
	// E B E G B E
	{"Em", []int{40, 47, 52, 55, 59, 64}},
	// G B D G B G
	{"open G", []int{43, 47, 50, 55, 59, 67}},
	// A E A C# E
	{"open A", []int{45, 52, 57, 61, 64}},
	// A E A C E
	{"Am", []int{45, 52, 57, 60, 64}},
	// C E G C E
	{"open C", []int{48, 52, 55, 60, 64}},
	// D A D F#
	{"open D", []int{50, 57, 62, 66}},
	// E B E (root, fifth, octave)
	{"E5", []int{40, 47, 52}},
}

// strumSpreads are the inter-string delays of a downstroke. 0 is the
// synthetic ideal every earlier fixture used; 5/12/20 ms bracket what a
// real pick sweep costs, so the whole corpus is exercised against signals
// where the last string speaks up to 100 ms after the first.
var strumSpreads = []struct {
	name   string
	spread float64
}{
	{"simultaneous", 0},
	{"5ms", 0.005},
	{"12ms", 0.012},
	{"20ms", 0.020},
}

// soundedClasses returns the set of pitch classes the shape sounds.
func (s chordShape) soundedClasses() map[int]bool {
	m := make(map[int]bool, len(s.keys))
	for _, k := range s.keys {
		m[ChromaOf(k)] = true
	}
	return m
}

// ksStrum renders keys struck spread seconds apart — a downstroke sweep —
// from ONE pluck voice, so each string gets its own excitation noise
// rather than the identical burst ksChord's per-note voices produce, and
// so the mix is the synth's own (soft-clipped) sum. spread 0 strikes them
// all on the same sample. The result is seconds long, mono (L+R).
func ksStrum(seconds, spread float64, keys ...int) []float32 {
	v := ksVoice()
	total := int(seconds * testSR)
	step := int(math.Round(spread * testSR))
	out := make([]float32, 0, total)
	next := 0 // index of the next key to strike
	for pos := 0; pos < total; {
		for next < len(keys) && next*step <= pos {
			v.NoteOn(keys[next], 0.8)
			next++
		}
		// Render up to the next strike (or the end).
		end := total
		if next < len(keys) {
			if at := next * step; at < end {
				end = at
			}
		}
		if end == pos {
			end = pos + 1
		}
		out = ksRender(v, float64(end-pos)/testSR, out)
		pos = end
	}
	return out
}

// chordMargin is the corpus's verdict for one chord/spread combination:
// the weakest sounded class against the strongest unsounded one, both read
// off the normalized Chroma.
type chordMargin struct {
	minSounded float64
	maxOther   float64
	worstClass int // the sounded class that scored lowest
	loudOther  int // the unsounded class that scored highest
	chroma     Chroma
}

// ok reports whether every sounded class outranks every unsounded one.
func (m chordMargin) ok() bool { return m.minSounded > m.maxOther }

func (m chordMargin) String() string {
	return fmt.Sprintf("weakest sounded %s=%.2f vs strongest unsounded %s=%.2f (margin %+.2f)",
		pitchClassNames[m.worstClass], m.minSounded,
		pitchClassNames[m.loudOther], m.maxOther, m.minSounded-m.maxOther)
}

// measureShape folds one shape at one spread through a strum-enabled
// detector and reports the sounded/unsounded margin of the Strum it
// produced. It fails the test if the chord does not produce exactly one
// Strum: a chord that never becomes a Strum is the same false "you missed"
// the margin is guarding against.
func measureShape(t *testing.T, cfg Config, s chordShape, spread float64) chordMargin {
	t.Helper()
	x := append(silence(0.2), ksStrum(1.0, spread, s.keys...)...)
	d := NewDetector(cfg)
	_, strums := feedStrums(d, x, 480)
	if len(strums) != 1 {
		t.Fatalf("%s: got %d strums, want exactly 1", s.name, len(strums))
	}
	return marginOf(strums[0].Chroma, s.soundedClasses())
}

// marginOf scores a chroma against a shape's sounded classes.
func marginOf(c Chroma, sounded map[int]bool) chordMargin {
	m := chordMargin{minSounded: math.Inf(1), maxOther: math.Inf(-1), chroma: c}
	for i, v := range c {
		if sounded[i] {
			if float64(v) < m.minSounded {
				m.minSounded, m.worstClass = float64(v), i
			}
			continue
		}
		if float64(v) > m.maxOther {
			m.maxOther, m.loudOther = float64(v), i
		}
	}
	return m
}

// TestChordCorpusSoundedClassesRankFirst is the regression the E-shaped
// fixtures could not catch: across every shape in chordCorpus and every
// strum spread, no unsounded pitch class may outrank a sounded one.
//
// A failure here means the scorer would tell a player who played the chord
// correctly that they missed a string.
func TestChordCorpusSoundedClassesRankFirst(t *testing.T) {
	cfg := strumConfig()
	for _, shape := range chordCorpus {
		for _, sp := range strumSpreads {
			t.Run(shape.name+"/"+sp.name, func(t *testing.T) {
				m := measureShape(t, cfg, shape, sp.spread)
				if !m.ok() {
					t.Errorf("%s;%s", m, formatChroma(m.chroma))
				}
			})
		}
	}
}

// TestChordCorpusMarginTable prints the measured margin for every
// combination. It never fails: its job is to keep the real numbers in
// front of whoever changes the fold next, so a regression shows up as a
// shrinking margin in the log before it shows up as a false Miss.
func TestChordCorpusMarginTable(t *testing.T) {
	cfg := strumConfig()
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-10s", "shape")
	for _, sp := range strumSpreads {
		fmt.Fprintf(&b, " %14s", sp.name)
	}
	b.WriteByte('\n')
	for _, shape := range chordCorpus {
		fmt.Fprintf(&b, "%-10s", shape.name)
		for _, sp := range strumSpreads {
			m := measureShape(t, cfg, shape, sp.spread)
			fmt.Fprintf(&b, " %+14.3f", m.minSounded-m.maxOther)
		}
		b.WriteByte('\n')
	}
	t.Log(b.String())
}

// A strumEvent is one chord struck at time at (seconds), with sp seconds
// between consecutive strings.
type strumEvent struct {
	at   float64
	keys []int
	sp   float64
}

// renderStrumSeq renders a sequence of strums from ONE pluck voice, so a
// chord struck while the previous one is still ringing really does mix
// with it — the condition P2 and P3 are about.
func renderStrumSeq(total float64, events ...strumEvent) []float32 {
	v := ksVoice()
	type strike struct{ at, key int }
	var strikes []strike
	for _, e := range events {
		for i, k := range e.keys {
			strikes = append(strikes, strike{int((e.at + float64(i)*e.sp) * testSR), k})
		}
	}
	n := int(total * testSR)
	out := make([]float32, 0, n)
	pos, next := 0, 0
	for pos < n {
		for next < len(strikes) && strikes[next].at <= pos {
			v.NoteOn(strikes[next].key, 0.8)
			next++
		}
		end := n
		if next < len(strikes) && strikes[next].at < end {
			end = strikes[next].at
		}
		if end == pos {
			end++
		}
		out = ksRender(v, float64(end-pos)/testSR, out)
		pos = end
	}
	return out
}

// changePairs are the chord transitions the P2/P3 tests run: indices into
// chordCorpus, first chord then second.
var changePairs = [][2]int{{2, 5}, {5, 2}, {0, 3}, {4, 6}, {6, 0}, {3, 1}}

// strumNear returns the Strum stamped within tol seconds of at, or nil.
func strumNear(strums []Strum, at, tol float64) *Strum {
	for i := range strums {
		d := strums[i].Frame - int64(at*testSR)
		if d < 0 {
			d = -d
		}
		if d < int64(tol*testSR) {
			return &strums[i]
		}
	}
	return nil
}

// TestChordChangeOverRingingChordEmitsStrum is P3's regression: a chord
// struck while the previous one still rings barely moves the broadband
// level — and the smoothed level the RMS trigger compares against was
// raised by that very chord — so the level trigger alone sees nothing.
// Before the flux trigger, 1 of 18 such changes at a 300 ms gap produced a
// Strum; every other one left the whole new chord scoring plain Misses.
func TestChordChangeOverRingingChordEmitsStrum(t *testing.T) {
	cfg := strumConfig()
	for _, gap := range []float64{0.3, 0.45, 0.6} {
		for _, sp := range []float64{0, 0.012, 0.020} {
			for _, pr := range changePairs {
				a, b := chordCorpus[pr[0]], chordCorpus[pr[1]]
				name := fmt.Sprintf("%s->%s/%.0fms/gap%.0fms", a.name, b.name, sp*1000, gap*1000)
				t.Run(name, func(t *testing.T) {
					x := renderStrumSeq(gap+1.2,
						strumEvent{0.2, a.keys, sp},
						strumEvent{0.2 + gap, b.keys, sp})
					d := NewDetector(cfg)
					_, strums := feedStrums(d, x, 480)
					if strumNear(strums, 0.2+gap, 0.12) == nil {
						t.Errorf("no Strum within 120 ms of the second chord (%d strums total)", len(strums))
					}
				})
			}
		}
	}
}

// TestChordChangeMarginTable is P2's record. Subtracting the pre-onset
// spectrum lifts the new chord's classes clear of the old chord's in most
// transitions, but NOT all: this table is the honest limit, measured
// against a previous chord left ringing at close to full amplitude (the
// Karplus-Strong voice sustains far longer than a real guitar chord a
// player is in the middle of changing away from, so these are worst-case
// numbers). The assertion is on the count, so the next change to the fold
// is measured against what is actually achieved today rather than against
// a wish.
func TestChordChangeMarginTable(t *testing.T) {
	cfg := strumConfig()
	var b strings.Builder
	b.WriteString("\nchord change over a still-ringing chord, gap 600 ms\n")
	negative, total := 0, 0
	for _, sp := range []float64{0, 0.012, 0.020} {
		for _, pr := range changePairs {
			a, c := chordCorpus[pr[0]], chordCorpus[pr[1]]
			x := renderStrumSeq(2.0, strumEvent{0.2, a.keys, sp}, strumEvent{0.8, c.keys, sp})
			d := NewDetector(cfg)
			_, strums := feedStrums(d, x, 480)
			s := strumNear(strums, 0.8, 0.12)
			if s == nil {
				t.Errorf("%s -> %s at %.0f ms spread: no Strum", a.name, c.name, sp*1000)
				continue
			}
			m := marginOf(s.Chroma, c.soundedClasses())
			total++
			if !m.ok() {
				negative++
			}
			fmt.Fprintf(&b, "%4.0fms %-9s -> %-9s %+.3f  (%s)\n",
				sp*1000, a.name, c.name, m.minSounded-m.maxOther, m)
		}
	}
	fmt.Fprintf(&b, "%d of %d transitions still rank an unsounded class first\n", negative, total)
	t.Log(b.String())
	// Measured today: 6 of 18. Before the pre-onset baseline subtraction
	// (and with the old 1-hop skip) it was 18 of 18, because 14 of them
	// produced no Strum at all.
	if negative > 6 {
		t.Errorf("%d of %d transitions rank an unsounded class first, want <= 6", negative, total)
	}
}
