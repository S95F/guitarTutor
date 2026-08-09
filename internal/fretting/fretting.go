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
// Ties are broken by enumeration order, which is fixed, so the assignment
// is deterministic: the same input always yields the same output.
package fretting

import (
	"sort"

	"github.com/S95F/guitarTutor/internal/score"
)

// MaxFret is the highest fret considered playable.
const MaxFret = 24

// MaxSpan is the largest allowed difference between the highest and lowest
// fretted fret within one chord. Open strings do not count toward the span.
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
	out := make([][]Position, len(beats))
	var unplayable []Unplayable

	// One candidate list per beat, then a Viterbi pass over the lists.
	cands := make([][]candidate, len(beats))
	for i, keys := range beats {
		out[i] = make([]Position, len(keys))
		cands[i] = enumerate(i, keys, tuning, capo, &unplayable)
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

// options returns the legal positions for key on tuning+capo, sorted by
// fret then by string number, both ascending.
func options(key int, tuning score.Tuning, capo int) []Position {
	var opts []Position
	for s := 1; s <= len(tuning); s++ {
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

// enumerate lists every legal joint fingering of one beat, reporting keys
// with no position at all and falling back to a greedy per-key assignment
// when the chord admits no joint fingering. It always returns at least one
// candidate (possibly an empty fingering) so the beat chain stays intact.
func enumerate(beat int, keys []int, tuning score.Tuning, capo int, unplayable *[]Unplayable) []candidate {
	lowest, highest := tuning[0]+capo, tuning[0]+capo
	for _, open := range tuning {
		if open+capo < lowest {
			lowest = open + capo
		}
		if open+capo > highest {
			highest = open + capo
		}
	}

	var keyIdx []int      // playable keys, by index into keys
	var opts [][]Position // options per keyIdx entry
	for ki, key := range keys {
		o := options(key, tuning, capo)
		if len(o) == 0 {
			reason := "no playable position"
			switch {
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

	var cands []candidate
	pos := make([]Position, len(keyIdx))
	var used uint32 // bitmask of taken string numbers
	var rec func(k int)
	rec = func(k int) {
		if k == len(keyIdx) {
			if spanOK(pos) {
				cands = append(cands, finish(keyIdx, pos))
			}
			return
		}
		for _, p := range opts[k] {
			if used&(1<<p.String) != 0 {
				continue
			}
			used |= 1 << p.String
			pos[k] = p
			rec(k + 1)
			used &^= 1 << p.String
		}
	}
	rec(0)

	if len(cands) == 0 {
		// No joint fingering (too many notes, or the span rule ruled
		// every combination out): place notes greedily, lowest fret
		// first, and report whatever will not fit.
		var kis []int
		var ps []Position
		used = 0
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
		cands = append(cands, finish(kis, ps))
	}
	return cands
}

// spanOK reports whether the fretted notes of a fingering stay within
// MaxSpan frets of each other. Open strings are exempt.
func spanOK(pos []Position) bool {
	min, max := 0, 0
	first := true
	for _, p := range pos {
		if p.Fret == 0 {
			continue
		}
		if first {
			min, max = p.Fret, p.Fret
			first = false
			continue
		}
		if p.Fret < min {
			min = p.Fret
		}
		if p.Fret > max {
			max = p.Fret
		}
	}
	return max-min <= MaxSpan
}

// finish builds a candidate from a fingering, computing its intrinsic cost
// and hand position per the package cost model.
func finish(keyIdx []int, pos []Position) candidate {
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
		sum += p.Fret
		n++
	}
	if n > 0 {
		c.fretted = true
		c.avg = float64(sum) / float64(n)
	}
	return c
}

// movement is the hand-movement cost between consecutive beats' candidates:
// 2 per fret of distance between their mean fretted frets. A beat with no
// fretted notes has no hand position and moves for free.
func movement(a, b candidate) float64 {
	if !a.fretted || !b.fretted {
		return 0
	}
	d := a.avg - b.avg
	if d < 0 {
		d = -d
	}
	return 2 * d
}
