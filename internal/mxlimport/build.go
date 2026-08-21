package mxlimport

import (
	"fmt"
	"sort"

	"github.com/S95F/musicTutor/internal/score"
)

type barSpec struct {
	start    int64
	num, den int
}

const MaxTicks = 100_000_000

const maxBars = 100_000

func barSpecs(meters score.MeterMap, end int64) ([]barSpec, error) {
	if end > MaxTicks {
		return nil, fmt.Errorf("score too long: extends to tick %d, past the %d-tick limit", end, int64(MaxTicks))
	}
	var specs []barSpec
	for start := int64(0); start < end; {
		if len(specs) >= maxBars {
			return nil, fmt.Errorf("score too long: more than %d bars", maxBars)
		}
		m := meters.At(start)
		if m.Num <= 0 || m.Den <= 0 || (4*score.PPQ)%int64(m.Den) != 0 {
			return nil, fmt.Errorf("unsupported time signature %d/%d", m.Num, m.Den)
		}
		specs = append(specs, barSpec{start: start, num: m.Num, den: m.Den})
		start += int64(m.Num) * (4 * score.PPQ / int64(m.Den))
	}
	return specs, nil
}

func buildTrack(pd *partData, role score.TrackRole, specs []barSpec) *score.Track {
	tr := &score.Track{
		Name:    pd.name,
		Program: pd.program,
		Role:    role,
	}
	if pd.wind != nil {

		tr.Wind = pd.wind
		if !pd.hasProgram {
			tr.Program = pd.wind.Program
		}
	} else {
		tr.Tuning = pd.tuning
		tr.Capo = pd.capo
	}

	edges := make([][]int64, len(specs))
	for _, n := range pd.notes {
		for _, x := range [2]int64{n.start, n.end} {
			i := sort.Search(len(specs), func(i int) bool { return specs[i].start >= x }) - 1
			if i < 0 {
				continue
			}
			barEnd := specs[i].start + int64(specs[i].num)*(4*score.PPQ/int64(specs[i].den))
			if x < barEnd {
				edges[i] = append(edges[i], x)
			}
		}
	}

	cursor := 0
	var active []*rawNote
	for bi, bs := range specs {
		bar := tr.AppendBar(bs.num, bs.den)
		barEnd := bar.Start + bar.Len()
		bounds := append([]int64{bar.Start, barEnd}, edges[bi]...)
		sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
		for i := 0; i+1 < len(bounds); i++ {
			segStart, segEnd := bounds[i], bounds[i+1]
			if segEnd == segStart {
				continue
			}
			for cursor < len(pd.notes) && pd.notes[cursor].start <= segStart {
				active = append(active, pd.notes[cursor])
				cursor++
			}
			kept := active[:0]
			for _, n := range active {
				if n.end > segStart {
					kept = append(kept, n)
				}
			}
			active = kept
			var notes []score.Note
			for _, n := range active {
				nn := score.Note{
					String:   n.str,
					Fret:     n.fret,
					Tied:     n.start < segStart,
					Inferred: n.inferred,
				}

				if n.start == segStart {
					nn.Tech = n.tech
				}
				notes = append(notes, nn)
			}
			sort.Slice(notes, func(i, j int) bool { return notes[i].String > notes[j].String })
			bar.AddBeat(segEnd-segStart, notes...)
		}
	}
	return tr
}
