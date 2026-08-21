// Package fretting assigns string/fret fingerings to pitches that arrive
// without any — MIDI carries no string or fret information, so tab display
// needs a heuristic (ROADMAP Phase 1: the heuristic is swappable and its
// output is visually marked as inferred).
//
// The assignment is a small dynamic program over consecutive beats. For
// each beat every legal fingering of its chord is enumerated (distinct
// strings, frets 0..MaxFret, fretted-note span at most MaxSpan), and the
// cheapest chain of fingerings across beats is chosen. The cost model:
//
//   - each fretted note costs its fret number (open strings are free, so
//     low positions and open strings win);
//   - each fretted note above PreferMaxFret costs a further 8 per fret
//     beyond it (positions above fret 15 are a last resort);
//   - moving the hand between consecutive beats costs 2 per fret of
//     distance between the beats' mean fretted frets (all-open beats have
//     no hand position and move for free).
//
// A beat may also arrive with some of its notes already fingered — an
// import whose file authored part of a chord and left the rest to the
// heuristic. AssignWith takes those positions as context: their strings
// are unavailable, their frets count toward the beat's span and hand
// position, and every note placed beside them costs a further 2 per fret
// of distance from their mean fret. The notes filled in therefore land in
// the authored shape's position rather than wherever the fretboard happens
// to be cheapest.
//
// Ties are broken by enumeration order, which is fixed, so the assignment
// is deterministic: the same input always yields the same output.
package fretting

import (
	"sort"

	"github.com/S95F/musicTutor/internal/score"
)

// MaxFret is the highest fret considered playable.
const MaxFret = 24

// maxStrings is the package's string-count ceiling. Strings beyond it get
// no positions at all (options simply stops looking), because enumerate
// tracks taken strings in a uint64 bitmask: string numbers up to 63 map to
// distinct bits, anything higher would alias. No real fretted instrument
// comes close — the widest are around 15-18 strings — so the cap costs
// nothing and keeps the mask arithmetic honest.
const maxStrings = 63

// maxNodes caps how many recursion steps enumerate spends on one beat's
// joint-fingering search. The search space is permutational — a 15-note
// chord on a 40-string staff has ~5e22 leaves, effectively unbounded — but
// any realistic worst case (dense chord, many-stringed tuning) finishes in
// a couple of thousand steps, so 100k is ~40x headroom for real music
// while a hostile input hits the cap in well under a millisecond and falls
// through to the greedy per-note placement.
const maxNodes = 100_000

// maxCands caps how many joint fingerings one beat may collect. Every
// candidate is copied by finish and later scored against every candidate
// of the neighboring beats in the Viterbi pass, so an unbounded list turns
// into quadratic transition work. Enumeration order is fret-ascending,
// meaning the cheap, playable fingerings are found first — keeping only
// the first 512 loses nothing anyone would want to play.
const maxCands = 512

// MaxSpan is the largest allowed difference between the highest and lowest
// fretted fret within one chord. Open strings do not count toward the span.
// Fingerings passed to AssignWith count: a note placed beside an authored
// chord has to be reachable by the hand already holding it.
const MaxSpan = 4

// PreferMaxFret is the highest fret reachable without the extra
// high-position cost penalty.
const PreferMaxFret = 15

// A Position is one assigned fingering: a string number (1 = the
// highest-pitched string, tab convention) and a fret. The zero value
// (String 0) means "no assignment" and marks keys reported as Unplayable
// in Assign's result.
type Position struct {
	String int
	Fret   int
}

// An Unplayable reports a key Assign could not place: below the lowest
// sounding open string, above MaxFret on the highest string, or part of a
// chord with more notes than can be fingered at once.
type Unplayable struct {
	Beat   int    // index into the beats argument
	Key    int    // the offending MIDI key
	Reason string // human-readable explanation
}

// Assign chooses a string and fret for every MIDI key in beats, where
// beats[i] holds the keys struck together at beat i (several keys form a
// chord; an empty slice is a rest). The result is parallel to beats:
// out[i][j] is the fingering for beats[i][j]. Keys that cannot be placed
// are returned as the zero Position and reported in the second result —
// they are never silently mangled.
//
// Within a beat every note gets a distinct string. If a chord admits no
// joint fingering under the span rule, Assign falls back to placing its
// notes one at a time, lowest fret first, and reports the leftovers.
func Assign(beats [][]int, tuning score.Tuning, capo int) ([][]Position, []Unplayable) {
	return AssignWith(beats, nil, tuning, capo)
}

// AssignWith is Assign for beats that are partly fingered already:
// fixed[i] holds the positions other notes of beat i are known to occupy
// — an importer's authored fingerings, say — which this call must place
// around rather than replace. A short or nil fixed leaves the remaining
// beats unconstrained.
//
// The fixed positions are context only: they are not returned, and their
// keys are not in beats. Their strings are unavailable to the beat, their
// frets count toward its span (see MaxSpan) and its hand position, and
// notes placed beside them are pulled toward their mean fret by the same
// per-fret cost that moving the hand between beats pays.
//
// A fixed chord wider than MaxSpan (authored music does contain such
// stretches) widens the beat's span allowance to its own width instead of
// ruling every fingering out — a note that fits inside the authored range
// never makes the chord harder to hold. If nothing fits even so, the beat
// falls back to the same greedy per-note placement Assign uses, on the
// strings the fixed positions left free: an awkward fingering, but never a
// lost note.
func AssignWith(beats [][]int, fixed [][]Position, tuning score.Tuning, capo int) ([][]Position, []Unplayable) {
	out := make([][]Position, len(beats))
	var unplayable []Unplayable

	// One candidate list per beat, then a Viterbi pass over the lists.
	cands := make([][]candidate, len(beats))
	for i, keys := range beats {
		out[i] = make([]Position, len(keys))
		var pre []Position
		if i < len(fixed) {
			pre = fixed[i]
		}
		cands[i] = enumerate(i, keys, newAnchor(pre), tuning, capo, &unplayable)
	}

	// cost[c] is the cheapest total cost of any chain ending in candidate
	// c of the current beat; back[i][c] is the previous beat's candidate
	// index on that chain.
	back := make([][]int, len(beats))
	var cost []float64
	for i, cs := range cands {
		back[i] = make([]int, len(cs))
		next := make([]float64, len(cs))
		for ci, c := range cs {
			next[ci] = c.cost
			if i == 0 {
				back[i][ci] = -1
				continue
			}
			best := 0
			bestCost := cost[0] + movement(cands[i-1][0], c)
			for pi := 1; pi < len(cost); pi++ {
				if v := cost[pi] + movement(cands[i-1][pi], c); v < bestCost {
					best, bestCost = pi, v
				}
			}
			next[ci] += bestCost
			back[i][ci] = best
		}
		cost = next
	}

	// Walk the backpointers from the cheapest final candidate.
	if len(beats) > 0 {
		best := 0
		for ci := 1; ci < len(cost); ci++ {
			if cost[ci] < cost[best] {
				best = ci
			}
		}
		for i := len(beats) - 1; i >= 0; i-- {
			c := cands[i][best]
			for j, ki := range c.keyIdx {
				out[i][ki] = c.pos[j]
			}
			best = back[i][best]
		}
	}
	return out, unplayable
}

// A candidate is one legal fingering of one beat's playable keys.
type candidate struct {
	keyIdx  []int      // indices into the beat's key slice
	pos     []Position // fingering per keyIdx entry
	cost    float64    // intrinsic cost (position preference)
	avg     float64    // mean fret over fretted notes
	fretted bool       // any non-open note (avg is meaningful)
}

// An anchor summarizes the fingerings already fixed at one beat: the
// strings they hold and the fretted frets they occupy. The zero anchor
// means nothing is fixed, which is the ordinary Assign case.
type anchor struct {
	used       uint64 // bitmask of string numbers the fixed notes hold
	minF, maxF int    // fretted span of the fixed notes (0 = none fretted)
	sum, n     int    // fretted-fret total and count, for the mean
}

// newAnchor summarizes fixed positions. Open (and zero-value) positions
// hold their string but imply no hand position, exactly as open strings
// within a beat do. A string number outside the package's mask range is
// unrepresentable, and no option is ever generated for one either, so it
// simply reserves nothing.
func newAnchor(fixed []Position) anchor {
	var a anchor
	for _, p := range fixed {
		if p.String >= 1 && p.String <= maxStrings {
			a.used |= 1 << p.String
		}
		if p.Fret <= 0 {
			continue
		}
		if a.minF == 0 || p.Fret < a.minF {
			a.minF = p.Fret
		}
		if p.Fret > a.maxF {
			a.maxF = p.Fret
		}
		a.sum += p.Fret
		a.n++
	}
	return a
}

// avg is the fixed notes' mean fretted fret — the hand position they
// imply. Only meaningful when a.n > 0.
func (a anchor) avg() float64 { return float64(a.sum) / float64(a.n) }

// span is the fretted span this beat may cover: MaxSpan, widened to the
// fixed notes' own span when they already exceed it.
func (a anchor) span() int {
	if s := a.maxF - a.minF; s > MaxSpan {
		return s
	}
	return MaxSpan
}

// options returns the legal positions for key on tuning+capo, sorted by
// fret then by string number, both ascending.
func options(key int, tuning score.Tuning, capo int) []Position {
	var opts []Position
	for s := 1; s <= len(tuning) && s <= maxStrings; s++ {
		fret := key - tuning[s-1] - capo
		if fret >= 0 && fret <= MaxFret {
			opts = append(opts, Position{String: s, Fret: fret})
		}
	}
	sort.Slice(opts, func(i, j int) bool {
		if opts[i].Fret != opts[j].Fret {
			return opts[i].Fret < opts[j].Fret
		}
		return opts[i].String < opts[j].String
	})
	return opts
}

// enumerate lists the legal joint fingerings of one beat — capped at
// maxCands fingerings found within maxNodes search steps — reporting keys
// with no position at all and falling back to a greedy per-key assignment
// when the search yields no joint fingering (the chord admits none, or a
// pathological input exhausted the budget first). It always returns at
// least one candidate (possibly an empty fingering) so the beat chain
// stays intact. Positions already fixed at this beat arrive as a, whose
// strings are off limits and whose frets seed the span and hand position.
//
// A tuning with no strings places nothing rather than panicking on its
// first entry: Assign is exported, and its mxlimport caller no longer
// screens the tuning it passes.
func enumerate(beat int, keys []int, a anchor, tuning score.Tuning, capo int, unplayable *[]Unplayable) []candidate {
	lowest, highest := 0, 0
	for i, open := range tuning {
		k := open + capo
		if i == 0 || k < lowest {
			lowest = k
		}
		if i == 0 || k > highest {
			highest = k
		}
	}

	var keyIdx []int      // playable keys, by index into keys
	var opts [][]Position // options per keyIdx entry
	for ki, key := range keys {
		o := options(key, tuning, capo)
		if len(o) == 0 {
			reason := "no playable position"
			switch {
			case len(tuning) == 0:
				reason = "the instrument has no strings"
			case key < lowest:
				reason = "below the lowest open string"
			case key > highest+MaxFret:
				reason = "above the highest fret"
			}
			*unplayable = append(*unplayable, Unplayable{Beat: beat, Key: key, Reason: reason})
			continue
		}
		keyIdx = append(keyIdx, ki)
		opts = append(opts, o)
	}

	// The exhaustive search runs under a node budget and a candidate cap:
	// rec reports whether the search may keep going, and a false return
	// unwinds the whole recursion. A hostile beat (a huge chord over a
	// many-stringed tuning) therefore stops after maxNodes steps instead
	// of running for years; if it stops with nothing, the greedy fallback
	// below takes over, so the beat chain still stays intact.
	//
	// The span rule is enforced INSIDE the recursion, not only at the
	// leaves: a branch dies the moment two of its fretted notes sit more
	// than MaxSpan apart. This matters far beyond speed. With leaf-only
	// checking, the fret-ascending walk burned the whole node budget in
	// low-fret subtrees whose every leaf was span-illegal, and a
	// perfectly playable 8-note chord on a real 14-string tuning — one
	// the pre-budget code fingered in milliseconds — exhausted the budget
	// with zero candidates and had a note falsely dropped as unplayable
	// (verification follow-up to B4). Pruning is admissible because a
	// partial assignment's fretted span only ever grows.
	var cands []candidate
	pos := make([]Position, len(keyIdx))
	used := a.used // bitmask of taken string numbers (options caps strings at maxStrings=63)
	span := a.span()
	budget := maxNodes
	// minF/maxF carry the fretted-note span of the partial assignment,
	// seeded with the fixed notes' span so a note placed beside them has
	// to be within reach of the same hand; zero means no fretted note yet
	// (fretted frets are always >= 1).
	var rec func(k, minF, maxF int) bool
	rec = func(k, minF, maxF int) bool {
		if budget <= 0 || len(cands) >= maxCands {
			return false
		}
		budget--
		if k == len(keyIdx) {
			// Span-legal by construction: every fretted note was checked
			// against the running span on the way down.
			cands = append(cands, finish(keyIdx, pos, a))
			return true
		}
		for _, p := range opts[k] {
			if used&(1<<p.String) != 0 {
				continue
			}
			nMin, nMax := minF, maxF
			if p.Fret > 0 {
				if nMin == 0 || p.Fret < nMin {
					nMin = p.Fret
				}
				if p.Fret > nMax {
					nMax = p.Fret
				}
				if nMax-nMin > span {
					continue
				}
			}
			used |= 1 << p.String
			pos[k] = p
			ok := rec(k+1, nMin, nMax)
			used &^= 1 << p.String
			if !ok {
				return false
			}
		}
		return true
	}
	rec(0, a.minF, a.maxF)

	if len(cands) == 0 {
		// No joint fingering (too many notes, the span rule ruled every
		// combination out, or the node budget ran dry before a fingering
		// was found): place notes greedily, lowest fret first, and
		// report whatever will not fit. The fixed strings stay off limits
		// even here — an inferred note stacked onto an authored one is a
		// note the merge downstream would silently lose.
		var kis []int
		var ps []Position
		used = a.used
		for k, ki := range keyIdx {
			placed := false
			for _, p := range opts[k] {
				if used&(1<<p.String) == 0 {
					used |= 1 << p.String
					kis = append(kis, ki)
					ps = append(ps, p)
					placed = true
					break
				}
			}
			if !placed {
				*unplayable = append(*unplayable, Unplayable{
					Beat: beat, Key: keys[ki], Reason: "no free string in chord",
				})
			}
		}
		cands = append(cands, finish(kis, ps, a))
	}
	return cands
}

// finish builds a candidate from a fingering, computing its intrinsic cost
// and hand position per the package cost model. Notes already fixed at the
// beat (a) are not part of the fingering but are part of the hand: they
// pull each placed note toward their mean fret at the hand-movement rate,
// and they count toward the candidate's own hand position, so the beats
// around a partly authored one move relative to where the hand really is.
func finish(keyIdx []int, pos []Position, a anchor) candidate {
	c := candidate{
		keyIdx: append([]int(nil), keyIdx...),
		pos:    append([]Position(nil), pos...),
	}
	sum, n := 0, 0
	for _, p := range c.pos {
		if p.Fret == 0 {
			continue
		}
		c.cost += float64(p.Fret)
		if p.Fret > PreferMaxFret {
			c.cost += 8 * float64(p.Fret-PreferMaxFret)
		}
		if a.n > 0 {
			c.cost += 2 * abs(float64(p.Fret)-a.avg())
		}
		sum += p.Fret
		n++
	}
	if n+a.n > 0 {
		c.fretted = true
		c.avg = float64(sum+a.sum) / float64(n+a.n)
	}
	return c
}

// abs is the absolute value of a fret distance.
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// movement is the hand-movement cost between consecutive beats' candidates:
// 2 per fret of distance between their mean fretted frets. A beat with no
// fretted notes has no hand position and moves for free.
func movement(a, b candidate) float64 {
	if !a.fretted || !b.fretted {
		return 0
	}
	return 2 * abs(a.avg-b.avg)
}
