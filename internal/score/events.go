package score

import "sort"

type NoteEvent struct {
	Track      int
	Start, End int64
	Key        int
	Velocity   float64
	String     int
	Fret       int
	Tech       Technique
}

const DefaultVelocity = 0.8

func (s *Score) Events() []NoteEvent {
	var evs []NoteEvent
	for ti, tr := range s.Tracks {

		ringing := map[int]int{}
		for _, bar := range tr.Bars {
			for _, beat := range bar.Beats {
				sounded := map[int]bool{}
				for _, n := range beat.Notes {
					sounded[n.String] = true
					if n.Tied {
						if ei, ok := ringing[n.String]; ok && evs[ei].Fret == n.Fret && evs[ei].End == beat.Start {
							evs[ei].End = beat.Start + beat.Dur

							evs[ei].Tech |= n.Tech
							continue
						}

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
