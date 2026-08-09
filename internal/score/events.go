package score

import "sort"

// A NoteEvent is a flattened, playable note: the unit the engine schedules
// and the scorer (Phase 2) matches against. Tied beats are merged into a
// single event spanning their combined duration.
type NoteEvent struct {
	Track      int   // index into Score.Tracks
	Start, End int64 // ticks; End is exclusive
	Key        int   // derived MIDI note
	Velocity   float64
	String     int
	Fret       int
	Tech       Technique
}

// DefaultVelocity is used until dynamics exist in the model.
const DefaultVelocity = 0.8

// Events flattens the score into note events sorted by start tick (ties
// broken by track). Dead notes (TechDead) keep their events — they are
// attacked but pitchless; the synth decides how to voice them.
func (s *Score) Events() []NoteEvent {
	var evs []NoteEvent
	for ti, tr := range s.Tracks {
		// ringing[stringNo] is the index in evs of the event a tied
		// note would extend.
		ringing := map[int]int{}
		for _, bar := range tr.Bars {
			for _, beat := range bar.Beats {
				sounded := map[int]bool{}
				for _, n := range beat.Notes {
					sounded[n.String] = true
					if n.Tied {
						if ei, ok := ringing[n.String]; ok && evs[ei].Fret == n.Fret && evs[ei].End == beat.Start {
							evs[ei].End = beat.Start + beat.Dur
							// Sustain-phase techniques (vibrato, bend)
							// written on the tied half belong to the
							// merged note.
							evs[ei].Tech |= n.Tech
							continue
						}
						// A tie with nothing to tie to (or a fret
						// mismatch) degrades to a fresh attack.
					}
					evs = append(evs, NoteEvent{
						Track:    ti,
						Start:    beat.Start,
						End:      beat.Start + beat.Dur,
						Key:      tr.Pitch(n),
						Velocity: DefaultVelocity,
						String:   n.String,
						Fret:     n.Fret,
						Tech:     n.Tech,
					})
					ringing[n.String] = len(evs) - 1
				}
				// A string not sounded in this beat stops ringing for
				// tie purposes (its event ended at its own End).
				for str, ei := range ringing {
					if !sounded[str] && evs[ei].End <= beat.Start {
						delete(ringing, str)
					}
				}
			}
		}
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].Start != evs[j].Start {
			return evs[i].Start < evs[j].Start
		}
		return evs[i].Track < evs[j].Track
	})
	return evs
}
