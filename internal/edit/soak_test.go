package edit

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func TestSoak(t *testing.T) {
	const (
		seeds = 12
		steps = 300
	)
	for seed := int64(0); seed < seeds; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			d := New(NewOptions{Title: "Soak", Bars: 1 + rng.Intn(3)})
			durations := textfmt.DurationNames()

			var last string
			for step := 0; step < steps; step++ {
				op, err := applyRandomOp(d, rng, durations)

				_ = err
				if e := d.Score().Validate(); e != nil {
					t.Fatalf("step %d (%s) left an invalid piece: %v\n--- last good ---\n%s", step, op, e, last)
				}
				src, e := textfmt.Format(d.Score())
				if e != nil {
					t.Fatalf("step %d (%s) left an unwritable piece: %v\n--- last good ---\n%s", step, op, e, last)
				}
				back, e := textfmt.Parse(src, "soak")
				if e != nil {
					t.Fatalf("step %d (%s) wrote a piece that will not parse: %v\n--- output ---\n%s", step, op, e, src)
				}
				again, e := textfmt.Format(back)
				if e != nil {
					t.Fatalf("step %d (%s): the re-parsed piece will not write: %v", step, op, e)
				}
				if string(again) != string(src) {
					t.Fatalf("step %d (%s) produced a piece that does not survive a round trip\n--- first ---\n%s\n--- second ---\n%s",
						step, op, src, again)
				}
				for ti, tr := range d.Score().Tracks {
					for bi, bar := range tr.Bars {
						var sum int64
						for _, bt := range bar.Beats {
							sum += bt.Dur
						}
						if sum != bar.Len() {
							t.Fatalf("step %d (%s): track %d bar %d holds %d of %d ticks",
								step, op, ti+1, bi+1, sum, bar.Len())
						}
						if len(bar.Beats) == 0 {
							t.Fatalf("step %d (%s): track %d bar %d has no beats", step, op, ti+1, bi+1)
						}
					}
					if len(tr.Bars) != d.BarCount() {
						t.Fatalf("step %d (%s): track %d has %d bars, the grid has %d",
							step, op, ti+1, len(tr.Bars), d.BarCount())
					}
				}
				last = string(src)
			}
		})
	}
}

func applyRandomOp(d *Doc, rng *rand.Rand, durations []struct {
	Ticks int64
	Name  string
}) (string, error) {
	switch rng.Intn(20) {
	case 0, 1, 2, 3, 4:
		fret := rng.Intn(textfmt.MaxFret + 1)
		return fmt.Sprintf("SetFret(%d) at %+v", fret, d.Cursor()), d.SetFret(fret)
	case 5, 6:
		dur := durations[rng.Intn(len(durations))]
		return fmt.Sprintf("SetDuration(%s) at %+v", dur.Name, d.Cursor()), d.SetDuration(dur.Ticks)
	case 7:
		return fmt.Sprintf("ClearNote at %+v", d.Cursor()), d.ClearNote()
	case 8:
		return fmt.Sprintf("ClearBeat at %+v", d.Cursor()), d.ClearBeat()
	case 9:
		return fmt.Sprintf("InsertBeat at %+v", d.Cursor()), d.InsertBeat()
	case 10:
		return fmt.Sprintf("DeleteBeat at %+v", d.Cursor()), d.DeleteBeat()
	case 11:
		return fmt.Sprintf("ToggleTie at %+v", d.Cursor()), d.ToggleTie()
	case 12:
		techs := []score.Technique{
			score.TechHammer, score.TechPull, score.TechSlide,
			score.TechBend, score.TechVibrato, score.TechDead,
		}
		tech := techs[rng.Intn(len(techs))]
		return fmt.Sprintf("ToggleTech(%d) at %+v", tech, d.Cursor()), d.ToggleTech(tech)
	case 13:
		return "AppendBar", d.AppendBar()
	case 14:
		return fmt.Sprintf("InsertBar at bar %d", d.Cursor().Bar), d.InsertBar()
	case 15:
		return fmt.Sprintf("DeleteBar at bar %d", d.Cursor().Bar), d.DeleteBar()
	case 16:
		num := 1 + rng.Intn(7)
		den := []int{1, 2, 4, 8, 16, 32}[rng.Intn(6)]
		return fmt.Sprintf("SetMeter(%d/%d) at bar %d", num, den, d.Cursor().Bar), d.SetMeter(num, den)
	case 17:
		bpm := float64(40 + rng.Intn(200))
		return fmt.Sprintf("SetTempo(%g) at bar %d", bpm, d.Cursor().Bar), d.SetTempo(bpm)
	case 18:

		switch rng.Intn(4) {
		case 0:
			return "Undo", nilIf(d.Undo())
		case 1:
			return "Redo", nilIf(d.Redo())
		case 2:
			return "AddTrack", d.AddTrack(fmt.Sprintf("Track %d", rng.Intn(9)), nil)
		default:
			return "DeleteTrack", d.DeleteTrack()
		}
	default:
		switch rng.Intn(6) {
		case 0:
			d.MoveBeat(1 - 2*rng.Intn(2))
			return "MoveBeat", nil
		case 1:
			d.MoveString(1 - 2*rng.Intn(2))
			return "MoveString", nil
		case 2:
			d.MoveBar(1 - 2*rng.Intn(2))
			return "MoveBar", nil
		case 3:
			d.GoTo(Cursor{Bar: rng.Intn(8), Beat: rng.Intn(8), Str: 1 + rng.Intn(8)})
			return "GoTo", nil
		case 4:
			d.SelectTrack(rng.Intn(3))
			return "SelectTrack", nil
		default:
			d.GoToEnd()
			return "GoToEnd", nil
		}
	}
}

func nilIf(ok bool) error {
	if ok {
		return nil
	}
	return fmt.Errorf("nothing to do")
}
