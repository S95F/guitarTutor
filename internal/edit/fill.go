package edit

import (
	"sort"

	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
)

// tickGrid is the coarsest tick unit every writable note value is a
// multiple of. It is 20 rather than the 40 of a triplet 32nd because of
// the dotted 32nd (180 ticks), which is 4½ of those — the one value that
// stops the note lengths from sharing a coarser grid.
const tickGrid = 20

// maxBarTicks is the longest bar the format can describe: 64 whole notes,
// its widest numerator over its coarsest denominator. It bounds the rest
// solver's table.
const maxBarTicks = 64 * 4 * score.PPQ

// writableDurations is every beat length .gtab can spell, longest first.
// It is taken from the writer, so the rests this package invents are by
// construction rests the file can hold.
var writableDurations = func() []int64 {
	names := textfmt.DurationNames()
	out := make([]int64, len(names))
	for i, d := range names {
		out[i] = d.Ticks
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}()

// restsFor decomposes a span of silence into the fewest writable rests
// that fill it exactly, longest first, and reports whether it can be done
// at all.
//
// The obvious greedy answer — take the longest rest that fits, repeat — is
// wrong here, and wrong in a way that only shows up on the values a
// guitarist actually reaches for. The lengths do not form a nice system:
// 180 ticks (a dotted 32nd) is the ONLY one that is not a multiple of 40
// (a triplet 32nd), so a remainder left by an odd number of dotted 32nds
// can only be finished by using another one — and greedy, having spent the
// long values first, walks itself down to a 20-tick remainder that nothing
// in the set can fill. A short exact search costs nothing at editing speed
// and cannot get stuck.
//
// The same arithmetic settles what is fillable at all. The values are 80
// (a triplet 32nd) and 120 (a 32nd) and multiples of 40 built from them,
// plus 180 and its own multiples-of-40 offspring, so a span is fillable
// exactly when it is
//
//	zero; or a multiple of 40 of at least 80; or 180; or 20 past a
//	multiple of 40 and at least 260.
//
// Everything else — 40 ticks, 220 ticks, the slivers 20 and 60 — is a
// remainder that no combination of written note values covers. Reaching
// one takes a bar deliberately packed to within a 32nd of full, and the
// only honest answer there is to say so, which is what callers do.
func restsFor(remaining int64) ([]int64, bool) {
	switch {
	case remaining == 0:
		return nil, true
	case remaining < 0 || remaining > maxBarTicks || remaining%tickGrid != 0:
		return nil, false
	}
	n := int(remaining / tickGrid)
	// best[k] is the fewest rests that fill k grid units, and from[k] the
	// length one of them has; 0 means unreachable.
	best := make([]int32, n+1)
	from := make([]int64, n+1)
	for k := 1; k <= n; k++ {
		best[k] = -1
		for _, d := range writableDurations {
			u := int(d / tickGrid)
			if u > k || best[k-u] < 0 {
				continue
			}
			if c := best[k-u] + 1; best[k] < 0 || c < best[k] {
				best[k], from[k] = c, d
			}
		}
	}
	if best[n] < 0 {
		return nil, false
	}
	out := make([]int64, 0, best[n])
	for k := n; k > 0; {
		out = append(out, from[k])
		k -= int(from[k] / tickGrid)
	}
	// Longest first reads as notation does — the long rest, then the
	// leftovers — rather than in the order the reconstruction found them.
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out, true
}
