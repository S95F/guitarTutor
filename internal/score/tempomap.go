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
// that fit their track's instrument — valid string and fret numbers on a
// fretted track, one note per beat on a single in-range chromatic lane on
// a wind track.
func (s *Score) Validate() error {
	if len(s.Tempos) == 0 || s.Tempos[0].Tick != 0 {
		return fmt.Errorf("tempo map must start at tick 0")
	}
	if !sort.SliceIsSorted(s.Tempos, func(i, j int) bool { return s.Tempos[i].Tick < s.Tempos[j].Tick }) {
		return fmt.Errorf("tempo map out of order")
	}
	for _, t := range s.Tempos {
		if t.USPerQuarter <= 0 {
			return fmt.Errorf("tempo at tick %d: non-positive microseconds per quarter (%d)", t.Tick, t.USPerQuarter)
		}
	}
	if len(s.Meters) == 0 || s.Meters[0].Tick != 0 {
		return fmt.Errorf("meter map must start at tick 0")
	}
	if !sort.SliceIsSorted(s.Meters, func(i, j int) bool { return s.Meters[i].Tick < s.Meters[j].Tick }) {
		return fmt.Errorf("meter map out of order")
	}
	// A meter with a zero (or negative) part would panic BeatLen with a
	// divide by zero the moment anything asks about that region of the
	// piece, and a denominator finer than the tick grid (4*PPQ, a
	// 1/3840th note) truncates BeatLen to zero ticks — reject both even
	// when no bar happens to use the meter, since importers can carry
	// meter changes past the last bar. Large numerators are legal: some
	// formats write them freely.
	for _, m := range s.Meters {
		if m.Num < 1 || m.Den < 1 || m.Den > 4*PPQ {
			return fmt.Errorf("meter at tick %d: invalid time signature %d/%d", m.Tick, m.Num, m.Den)
		}
	}
	for ti, tr := range s.Tracks {
		if tr.Wind == nil && len(tr.Tuning) == 0 {
			return fmt.Errorf("track %d (%s): no tuning", ti, tr.Name)
		}
		if tr.Wind != nil {
			// One representation per track: a wind part that also carried
			// strings or a capo would give Pitch two answers.
			if len(tr.Tuning) != 0 {
				return fmt.Errorf("track %d (%s): a %s has no strings, but the track carries a tuning", ti, tr.Name, tr.Wind.Name)
			}
			if tr.Capo != 0 {
				return fmt.Errorf("track %d (%s): a %s has no capo", ti, tr.Name, tr.Wind.Name)
			}
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
				if tr.Wind != nil && len(beat.Notes) > 1 {
					return fmt.Errorf("track %d bar %d: %d notes in one beat; a %s plays one note at a time", ti, bi, len(beat.Notes), tr.Wind.Name)
				}
				// One string sounds once per beat. The editor enforces this
				// as it types (SetFret replaces a note already on the
				// string), the parser rejects "string N played twice", and
				// textfmt.Format refuses such a beat through this very
				// check (it validates first) — so a model that let it
				// stand would be one every other layer already
				// treats as impossible: score.Events keys its tie
				// bookkeeping by string, and two notes on one string in one
				// beat give it two answers.
				var seen uint64 // bitset of strings seen; strings are 1..64 in practice
				for _, n := range beat.Notes {
					if n.String < 1 || n.String > 64 {
						continue // out-of-range strings are caught below
					}
					bit := uint64(1) << uint(n.String-1)
					if seen&bit != 0 {
						return fmt.Errorf("track %d bar %d: string %d sounds twice in one beat", ti, bi, n.String)
					}
					seen |= bit
				}
				for _, n := range beat.Notes {
					if tr.Wind != nil {
						if n.String != 1 {
							return fmt.Errorf("track %d bar %d: string %d on a %s, whose one lane is string 1", ti, bi, n.String, tr.Wind.Name)
						}
						if n.Fret < 0 {
							return fmt.Errorf("track %d bar %d: note below the %s's lowest note", ti, bi, tr.Wind.Name)
						}
						if n.Tech&(TechPull|TechDead) != 0 {
							return fmt.Errorf("track %d bar %d: pull-offs and dead notes do not exist on a %s", ti, bi, tr.Wind.Name)
						}
					} else {
						if n.String < 1 || n.String > len(tr.Tuning) {
							return fmt.Errorf("track %d bar %d: string %d out of range", ti, bi, n.String)
						}
						if n.Fret < 0 {
							return fmt.Errorf("track %d bar %d: negative fret", ti, bi)
						}
					}
					if k := tr.Pitch(n); k < 0 || k > 127 {
						return fmt.Errorf("track %d bar %d: string %d fret %d sounds MIDI key %d, outside 0-127", ti, bi, n.String, n.Fret, k)
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
