package pitch

import (
	"math"
	"slices"
)

const (
	trackerMedianHops = 5

	trackerOpenHops = 3

	trackerCloseUnvoicedHops = 3

	trackerKeyChangeHops = 4

	trackerKeyCentsTol = 60

	trackerJumpCents = 80

	trackerEndHops = 5
)

type Tracker struct {
	cfg Config

	fifo    [trackerMedianHops]float64
	fifoLen int
	fifoPos int
	sort5   [trackerMedianHops]float64

	open       bool
	key        int
	start      int64
	lastVoiced int64
	prevDev    float64
	cents      []float64
	clar       []float64

	candKey   int
	candCount int
	candStart int64
	candLast  int64
	candCents []float64
	candClar  []float64

	jumpKey   int
	jumpCount int
	jumpStart int64
	jumpLast  int64
	jumpCents []float64
	jumpClar  []float64

	unvoicedRun int

	medScratch []float64
	notes      []Note
}

func NewTracker(cfg Config) *Tracker {
	return &Tracker{cfg: cfg.withDefaults()}
}

func (t *Tracker) Feed(frames []Frame) []Note {
	t.notes = t.notes[:0]
	for i := range frames {
		t.feedOne(&frames[i])
	}
	return t.notes
}

func (t *Tracker) feedOne(f *Frame) {
	if f.Onset {

		if t.open {
			t.closeNote(t.lastVoiced)
		}
		t.resetCand()
		t.resetJump()
		t.fifoLen, t.fifoPos = 0, 0
	}

	if f.F0 <= 0 {
		t.unvoicedRun++

		t.resetCand()

		if t.open && t.unvoicedRun >= trackerCloseUnvoicedHops {
			t.closeNote(t.lastVoiced)
			t.resetJump()
		}
		return
	}
	t.unvoicedRun = 0

	f0 := t.medianF0(f.F0)
	m := midiPitch(f0)

	if !t.open {
		if t.candCount > 0 && math.Abs((m-float64(t.candKey))*100) <= trackerKeyCentsTol {
			t.candCount++
		} else {
			t.candKey = int(math.Round(m))
			t.candCount = 1
			t.candStart = f.Frame
			t.candCents = t.candCents[:0]
			t.candClar = t.candClar[:0]
		}
		t.candCents = append(t.candCents, (m-float64(t.candKey))*100)
		t.candClar = append(t.candClar, f.Clarity)
		t.candLast = f.Frame
		if t.candCount >= trackerOpenHops {
			t.openNote(t.candKey, t.candStart, t.candLast, t.candCents, t.candClar)
			t.resetCand()
		}
		return
	}

	dev := (m - float64(t.key)) * 100
	k := int(math.Round(m))

	if t.jumpCount > 0 {
		if math.Abs((m-float64(t.jumpKey))*100) <= trackerKeyCentsTol {
			t.jumpCount++
			t.jumpCents = append(t.jumpCents, (m-float64(t.jumpKey))*100)
			t.jumpClar = append(t.jumpClar, f.Clarity)
			t.jumpLast = f.Frame
			if t.jumpCount >= trackerKeyChangeHops {

				t.closeNote(t.lastVoiced)
				t.openNote(t.jumpKey, t.jumpStart, t.jumpLast, t.jumpCents, t.jumpClar)
				t.resetJump()
			}
			return
		}

		t.resetJump()
		if math.Abs(dev-t.prevDev) > trackerJumpCents && k != t.key {
			t.startJump(k, m, f)
			return
		}
	} else if math.Abs(dev-t.prevDev) > trackerJumpCents && k != t.key {
		t.startJump(k, m, f)
		return
	}

	t.cents = append(t.cents, dev)
	t.clar = append(t.clar, f.Clarity)
	t.lastVoiced = f.Frame
	t.prevDev = dev
}

func (t *Tracker) Current() (Note, bool) {
	if !t.open {
		return Note{}, false
	}
	lo, hi, end := t.trajectory()
	return Note{
		Start:    t.start,
		Key:      t.key,
		Cents:    median(&t.medScratch, t.cents),
		MinCents: lo,
		MaxCents: hi,
		EndCents: end,
		Clarity:  median(&t.medScratch, t.clar),
	}, true
}

func (t *Tracker) Flush() []Note {
	t.notes = t.notes[:0]
	if t.open {
		t.closeNote(t.lastVoiced)
	}
	t.resetCand()
	t.resetJump()
	t.fifoLen, t.fifoPos = 0, 0
	t.unvoicedRun = 0
	return t.notes
}

func (t *Tracker) openNote(key int, start, last int64, cents, clar []float64) {
	t.open = true
	t.key = key
	t.start = start
	t.lastVoiced = last
	t.cents = append(t.cents[:0], cents...)
	t.clar = append(t.clar[:0], clar...)
	t.prevDev = cents[len(cents)-1]
}

func (t *Tracker) closeNote(end int64) {
	lo, hi, last := t.trajectory()
	t.notes = append(t.notes, Note{
		Start:    t.start,
		End:      end,
		Key:      t.key,
		Cents:    median(&t.medScratch, t.cents),
		MinCents: lo,
		MaxCents: hi,
		EndCents: last,
		Clarity:  median(&t.medScratch, t.clar),
	})
	t.open = false
	t.cents = t.cents[:0]
	t.clar = t.clar[:0]
	t.prevDev = 0
}

func (t *Tracker) trajectory() (lo, hi, end float64) {
	if len(t.cents) == 0 {
		return 0, 0, 0
	}
	lo, hi = t.cents[0], t.cents[0]
	for _, c := range t.cents {
		if c < lo {
			lo = c
		}
		if c > hi {
			hi = c
		}
	}
	tail := t.cents
	if len(tail) > trackerEndHops {
		tail = tail[len(tail)-trackerEndHops:]
	}
	return lo, hi, median(&t.medScratch, tail)
}

func (t *Tracker) startJump(k int, m float64, f *Frame) {
	t.jumpKey = k
	t.jumpCount = 1
	t.jumpStart = f.Frame
	t.jumpLast = f.Frame
	t.jumpCents = append(t.jumpCents[:0], (m-float64(k))*100)
	t.jumpClar = append(t.jumpClar[:0], f.Clarity)
}

func (t *Tracker) resetCand() { t.candCount = 0 }
func (t *Tracker) resetJump() { t.jumpCount = 0 }

func (t *Tracker) medianF0(f0 float64) float64 {
	t.fifo[t.fifoPos] = f0
	t.fifoPos = (t.fifoPos + 1) % trackerMedianHops
	if t.fifoLen < trackerMedianHops {
		t.fifoLen++
	}
	s := t.sort5[:t.fifoLen]
	copy(s, t.fifo[:t.fifoLen])
	slices.Sort(s)
	if t.fifoLen%2 == 1 {
		return s[t.fifoLen/2]
	}
	return 0.5 * (s[t.fifoLen/2-1] + s[t.fifoLen/2])
}

func midiPitch(f0 float64) float64 {
	return 69 + 12*math.Log2(f0/440)
}

func median(scratch *[]float64, xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append((*scratch)[:0], xs...)
	*scratch = s
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return 0.5 * (s[n/2-1] + s[n/2])
}
