package edit

import (
	"sort"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

const tickGrid = 20

const maxBarTicks = 64 * 4 * score.PPQ

var writableDurations = func() []int64 {
	names := textfmt.DurationNames()
	out := make([]int64, len(names))
	for i, d := range names {
		out[i] = d.Ticks
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}()

func restsFor(remaining int64) ([]int64, bool) {
	switch {
	case remaining == 0:
		return nil, true
	case remaining < 0 || remaining > maxBarTicks || remaining%tickGrid != 0:
		return nil, false
	}
	n := int(remaining / tickGrid)

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

	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out, true
}
