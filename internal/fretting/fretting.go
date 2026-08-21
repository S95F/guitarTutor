package fretting

import (
	"sort"

	"github.com/S95F/musicTutor/internal/score"
)

const MaxFret = 24

const maxStrings = 63

const maxNodes = 100_000

const maxCands = 512

const MaxSpan = 4

const PreferMaxFret = 15

type Position struct {
	String int
	Fret   int
}

type Unplayable struct {
	Beat   int
	Key    int
	Reason string
}

func Assign(beats [][]int, tuning score.Tuning, capo int) ([][]Position, []Unplayable) {
	return AssignWith(beats, nil, tuning, capo)
}

func AssignWith(beats [][]int, fixed [][]Position, tuning score.Tuning, capo int) ([][]Position, []Unplayable) {
	out := make([][]Position, len(beats))
	var unplayable []Unplayable

	cands := make([][]candidate, len(beats))
	for i, keys := range beats {
		out[i] = make([]Position, len(keys))
		var pre []Position
		if i < len(fixed) {
			pre = fixed[i]
		}
		cands[i] = enumerate(i, keys, newAnchor(pre), tuning, capo, &unplayable)
	}

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

type candidate struct {
	keyIdx  []int
	pos     []Position
	cost    float64
	avg     float64
	fretted bool
}

type anchor struct {
	used       uint64
	minF, maxF int
	sum, n     int
}

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

func (a anchor) avg() float64 { return float64(a.sum) / float64(a.n) }

func (a anchor) span() int {
	if s := a.maxF - a.minF; s > MaxSpan {
		return s
	}
	return MaxSpan
}

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

	var keyIdx []int
	var opts [][]Position
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

	var cands []candidate
	pos := make([]Position, len(keyIdx))
	used := a.used
	span := a.span()
	budget := maxNodes

	var rec func(k, minF, maxF int) bool
	rec = func(k, minF, maxF int) bool {
		if budget <= 0 || len(cands) >= maxCands {
			return false
		}
		budget--
		if k == len(keyIdx) {

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

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func movement(a, b candidate) float64 {
	if !a.fretted || !b.fretted {
		return 0
	}
	return 2 * abs(a.avg-b.avg)
}
