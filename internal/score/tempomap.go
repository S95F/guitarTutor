package score

import (
	"fmt"
	"sort"
)

// A Tempo is a tempo change, SMF-style: microseconds per quarter note from
// Tick onward.
type Tempo struct {
	Tick         int64
	USPerQuarter int64
}

// USPerQuarter converts beats-per-minute to SMF microseconds per quarter.
func USPerQuarter(bpm float64) int64 { return int64(60e6/bpm + 0.5) }

// BPM converts microseconds per quarter back to beats per minute.
func (t Tempo) BPM() float64 { return 60e6 / float64(t.USPerQuarter) }

// A TempoMap is the piece's tempo changes, sorted by tick, with the first
// entry at tick 0.
type TempoMap []Tempo

// At returns the microseconds-per-quarter in effect at tick.
func (m TempoMap) At(tick int64) int64 {
	us := int64(500000) // SMF default: 120 BPM
	for _, t := range m {
		if t.Tick > tick {
			break
		}
		us = t.USPerQuarter
	}
	return us
}

// TimeAt returns the wall-clock time in seconds of tick, at nominal tempo
// (no practice-speed scaling — that is the engine's concern).
func (m TempoMap) TimeAt(tick int64) float64 {
	var sec float64
	prevTick := int64(0)
	prevUS := int64(500000)
	for _, t := range m {
		if t.Tick >= tick {
			break
		}
		if t.Tick > prevTick {
			sec += float64(t.Tick-prevTick) * float64(prevUS) / 1e6 / PPQ
			prevTick = t.Tick
		}
		prevUS = t.USPerQuarter
	}
	sec += float64(tick-prevTick) * float64(prevUS) / 1e6 / PPQ
	return sec
}

// TickAt returns the tick playing at wall-clock second sec (the inverse of
// TimeAt), at nominal tempo. Times past the last tempo segment extrapolate.
func (m TempoMap) TickAt(sec float64) int64 {
	var acc float64
	prevTick := int64(0)
	prevUS := int64(500000)
	for _, t := range m {
		if t.Tick > prevTick {
			segLen := float64(t.Tick-prevTick) * float64(prevUS) / 1e6 / PPQ
			if acc+segLen >= sec {
				break
			}
			acc += segLen
			prevTick = t.Tick
		}
		prevUS = t.USPerQuarter
	}
	return prevTick + int64((sec-acc)*1e6*PPQ/float64(prevUS)+0.5)
}

// A Meter is a time-signature change from Tick onward.
type Meter struct {
	Tick     int64
	Num, Den int
}

// A MeterMap is the piece's time-signature changes, sorted by tick, with
// the first entry at tick 0.
type MeterMap []Meter

// At returns the meter in effect at tick.
func (m MeterMap) At(tick int64) Meter {
	cur := Meter{Tick: 0, Num: 4, Den: 4}
	for _, ts := range m {
		if ts.Tick > tick {
			break
		}
		cur = ts
	}
	return cur
}

// BeatLen returns the length in ticks of one beat under this meter (the
// denominator note value).
func (ts Meter) BeatLen() int64 { return 4 * PPQ / int64(ts.Den) }

// Validate checks structural invariants: sorted maps starting at tick 0,
// bars whose beats exactly fill them, contiguous beat starts, and notes
// with valid string and fret numbers.
func (s *Score) Validate() error {
	if len(s.Tempos) == 0 || s.Tempos[0].Tick != 0 {
		return fmt.Errorf("tempo map must start at tick 0")
	}
	if !sort.SliceIsSorted(s.Tempos, func(i, j int) bool { return s.Tempos[i].Tick < s.Tempos[j].Tick }) {
		return fmt.Errorf("tempo map out of order")
	}
	if len(s.Meters) == 0 || s.Meters[0].Tick != 0 {
		return fmt.Errorf("meter map must start at tick 0")
	}
	if !sort.SliceIsSorted(s.Meters, func(i, j int) bool { return s.Meters[i].Tick < s.Meters[j].Tick }) {
		return fmt.Errorf("meter map out of order")
	}
	for ti, tr := range s.Tracks {
		if len(tr.Tuning) == 0 {
			return fmt.Errorf("track %d (%s): no tuning", ti, tr.Name)
		}
		wantStart := int64(0)
		for bi, bar := range tr.Bars {
			if bar.Start != wantStart {
				return fmt.Errorf("track %d bar %d: starts at tick %d, want %d", ti, bi, bar.Start, wantStart)
			}
			beatStart := bar.Start
			var filled int64
			for _, beat := range bar.Beats {
				if beat.Start != beatStart {
					return fmt.Errorf("track %d bar %d: beat at tick %d, want %d", ti, bi, beat.Start, beatStart)
				}
				if beat.Dur <= 0 {
					return fmt.Errorf("track %d bar %d: non-positive beat duration", ti, bi)
				}
				for _, n := range beat.Notes {
					if n.String < 1 || n.String > len(tr.Tuning) {
						return fmt.Errorf("track %d bar %d: string %d out of range", ti, bi, n.String)
					}
					if n.Fret < 0 {
						return fmt.Errorf("track %d bar %d: negative fret", ti, bi)
					}
				}
				beatStart += beat.Dur
				filled += beat.Dur
			}
			if filled != bar.Len() {
				return fmt.Errorf("track %d bar %d: beats fill %d ticks, meter %d/%d wants %d",
					ti, bi, filled, bar.Num, bar.Den, bar.Len())
			}
			wantStart = bar.Start + bar.Len()
		}
	}
	return nil
}
