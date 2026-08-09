package mxlimport

import (
	"fmt"
	"sort"

	"github.com/S95F/guitarTutor/internal/fretting"
	"github.com/S95F/guitarTutor/internal/score"
)

// fretAssign is internal/fretting's assignment, indirected for tests.
var fretAssign = fretting.Assign

// A barSpec is one bar of the shared bar structure.
type barSpec struct {
	start    int64
	num, den int
}

// MaxTicks caps the score extent the import will accept — about 14
// hours of 4/4 at 120 BPM. barSpecs allocates one barSpec per bar from
// tick 0 to the score's end, so a single hostile <duration> (or time
// signature) could otherwise turn the layout into an unbounded
// allocation loop.
const MaxTicks = 100_000_000

// maxBars is the belt-and-braces cap on the bar count itself, for meter
// maps whose tiny bars would slice even a legal extent into an absurd
// number of specs (it also breaks any overflow-induced cycle in the
// bar-advance arithmetic).
const maxBars = 100_000

// barSpecs lays out contiguous bars from tick 0 through end under the
// meter map. Meter entries are recorded at measure starts during parsing,
// so every entry falls on a barline by construction.
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

// buildTrack converts one finished part into a score.Track. Within each
// bar the beat boundaries are the union of the bar's edges and every note
// onset and end inside it, so each note covers a whole number of beats:
// the first carries the attack, the rest continue it as Tied notes
// (score.Events merges them back), and beats with nothing sounding become
// rests — which is how underfull measures end up padded.
func buildTrack(pd *partData, role score.TrackRole, specs []barSpec) *score.Track {
	tr := &score.Track{
		Name:    pd.name,
		Tuning:  pd.tuning,
		Capo:    pd.capo,
		Program: pd.program,
		Role:    role,
	}
	for _, bs := range specs {
		bar := tr.AppendBar(bs.num, bs.den)
		barEnd := bar.Start + bar.Len()
		bounds := []int64{bar.Start, barEnd}
		for _, n := range pd.notes {
			if n.start > bar.Start && n.start < barEnd {
				bounds = append(bounds, n.start)
			}
			if n.end > bar.Start && n.end < barEnd {
				bounds = append(bounds, n.end)
			}
		}
		sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
		for i := 0; i+1 < len(bounds); i++ {
			segStart, segEnd := bounds[i], bounds[i+1]
			if segEnd == segStart {
				continue // duplicate boundary
			}
			var notes []score.Note
			for _, n := range pd.notes {
				if n.start <= segStart && n.end > segStart {
					notes = append(notes, score.Note{
						String:   n.str,
						Fret:     n.fret,
						Tied:     n.start < segStart,
						Inferred: n.inferred,
					})
				}
			}
			sort.Slice(notes, func(i, j int) bool { return notes[i].String > notes[j].String })
			bar.AddBeat(segEnd-segStart, notes...)
		}
	}
	return tr
}
