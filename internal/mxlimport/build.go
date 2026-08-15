package mxlimport

import (
	"fmt"
	"sort"

	"github.com/S95F/guitarTutor/internal/fretting"
	"github.com/S95F/guitarTutor/internal/score"
)

// fretAssign is internal/fretting's assignment, indirected for tests.
var fretAssign = fretting.AssignWith

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
//
// The walk is linear in notes plus bars rather than their product: note
// edges are bucketed into their bars up front (one binary search per
// edge), and the notes sounding at each beat segment are tracked with an
// advancing cursor plus a carried active set instead of rescanning the
// whole note list per segment. After finish's same-string truncation the
// active set holds at most one note per string. A tied note can sound
// many bars past its start, which is why the active set is carried
// across bars instead of being recomputed from a per-bar window of note
// starts.
func buildTrack(pd *partData, role score.TrackRole, specs []barSpec) *score.Track {
	tr := &score.Track{
		Name:    pd.name,
		Tuning:  pd.tuning,
		Capo:    pd.capo,
		Program: pd.program,
		Role:    role,
	}
	// Bucket every note edge that falls strictly inside a bar; an edge
	// on a barline adds no boundary because the bar's own edges already
	// cover it. Bars are contiguous, so the bar owning edge x is the
	// last one starting before x, and an edge at or past that bar's end
	// sits on a barline or past the score.
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
	// pd.notes is sorted by start (finish keeps it that way) and segment
	// starts only ever advance, so a single cursor pass activates each
	// note exactly once; a note leaves the active set once it no longer
	// sounds at the current segment.
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
				continue // duplicate boundary
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
				notes = append(notes, score.Note{
					String:   n.str,
					Fret:     n.fret,
					Tied:     n.start < segStart,
					Inferred: n.inferred,
				})
			}
			sort.Slice(notes, func(i, j int) bool { return notes[i].String > notes[j].String })
			bar.AddBeat(segEnd-segStart, notes...)
		}
	}
	return tr
}
