package pitch

import (
	"math"
	"slices"
)

// Tracker tunables: the hysteresis counts and tolerances that turn noisy
// per-hop estimates into stable note events.
const (
	// trackerMedianHops is the causal median filter length over voiced
	// f0 estimates.
	trackerMedianHops = 5
	// trackerOpenHops opens a note after this many consecutive voiced
	// hops within trackerKeyCentsTol of the same key.
	trackerOpenHops = 3
	// trackerCloseUnvoicedHops closes the open note after this many
	// consecutive unvoiced hops.
	trackerCloseUnvoicedHops = 3
	// trackerKeyChangeHops closes the open note (and opens the new one)
	// after this many voiced hops settled on a different key reached by
	// a discontinuous jump. Unvoiced hops in between neither count nor
	// cancel: they carry no pitch evidence, so only a voiced hop back on
	// the old trajectory (or on yet another key) resets the candidacy.
	trackerKeyChangeHops = 4
	// trackerKeyCentsTol is how far (in cents) a hop may sit from a
	// candidate key and still count toward opening it.
	trackerKeyCentsTol = 60
	// trackerJumpCents is the per-hop pitch discontinuity, in cents,
	// that distinguishes a re-fretted note from a bend. A bend moves a
	// few cents per hop; a hammer-on moves at least a semitone in one.
	trackerJumpCents = 80
)

// A Tracker assembles Frames into Notes with hysteresis: a note opens
// after enough consecutive voiced frames agree on a key, follows bends via
// the cents trajectory, and closes on silence, an onset, or a sustained
// key change. Not safe for concurrent use.
//
// Bend semantics: while a note is open, its cents deviation is measured
// against the note's opening key without limit, so a bend that reaches the
// next key is still ONE note whose Cents trajectory rose. Only an onset
// (re-pick) or a discontinuous jump (trackerJumpCents in one hop, settling
// on a different key for trackerKeyChangeHops) splits into a new note.
type Tracker struct {
	cfg Config

	// Causal median filter over the last trackerMedianHops voiced f0s.
	fifo    [trackerMedianHops]float64
	fifoLen int
	fifoPos int
	sort5   [trackerMedianHops]float64

	// The open note.
	open       bool
	key        int
	start      int64
	lastVoiced int64 // stamp of the last voiced hop credited to the note
	prevDev    float64
	cents      []float64
	clar       []float64

	// Candidacy for opening a note (no note open).
	candKey   int
	candCount int
	candStart int64
	candLast  int64
	candCents []float64
	candClar  []float64

	// Candidacy for a discontinuous key change (note open).
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

// NewTracker builds a tracker sharing the detector's config.
func NewTracker(cfg Config) *Tracker {
	return &Tracker{cfg: cfg.withDefaults()}
}

// Feed consumes detector frames and returns the notes that CLOSED during
// them. The returned slice is reused across calls — copy anything you keep.
func (t *Tracker) Feed(frames []Frame) []Note {
	t.notes = t.notes[:0]
	for i := range frames {
		t.feedOne(&frames[i])
	}
	return t.notes
}

// feedOne advances the hysteresis state machine by one frame.
func (t *Tracker) feedOne(f *Frame) {
	if f.Onset {
		// A new attack always splits: close whatever is sounding and
		// restart candidacy (and the median filter) from scratch.
		if t.open {
			t.closeNote(t.lastVoiced)
		}
		t.resetCand()
		t.resetJump()
		t.fifoLen, t.fifoPos = 0, 0
	}

	if f.F0 <= 0 {
		t.unvoicedRun++
		// Opening still requires consecutive voiced hops, so a quiet
		// note flickering around the voicing threshold restarts its
		// opening candidacy on every dropout — the analogous limitation
		// to the jump-candidacy fix below, accepted for now because no
		// note is being held hostage while nothing is open.
		t.resetCand()
		// An unvoiced hop carries no contradicting pitch evidence, so it
		// must NOT destroy an in-progress key-change candidacy; only a
		// voiced hop back on the old note's trajectory cancels the jump
		// (handled in the voiced path). Resetting the jump here used to
		// deadlock alternating new-key-voiced/unvoiced streams: the
		// voiced hops kept resetting unvoicedRun, the unvoiced hops kept
		// resetting the jump, and the old note stayed open forever.
		if t.open && t.unvoicedRun >= trackerCloseUnvoicedHops {
			t.closeNote(t.lastVoiced)
			t.resetJump() // the note the jump was leaving is gone
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
				// Settled: the jump was a real key change.
				t.closeNote(t.lastVoiced)
				t.openNote(t.jumpKey, t.jumpStart, t.jumpLast, t.jumpCents, t.jumpClar)
				t.resetJump()
			}
			return
		}
		// The jump didn't settle. Either the pitch snapped back to the
		// old note's trajectory (spurious hops: drop them) or it leapt
		// somewhere new again (restart the jump candidacy there).
		t.resetJump()
		if math.Abs(dev-t.prevDev) > trackerJumpCents && k != t.key {
			t.startJump(k, m, f)
			return
		}
	} else if math.Abs(dev-t.prevDev) > trackerJumpCents && k != t.key {
		t.startJump(k, m, f)
		return
	}

	// Continuous with the open note (bends included): follow it.
	t.cents = append(t.cents, dev)
	t.clar = append(t.clar, f.Clarity)
	t.lastVoiced = f.Frame
	t.prevDev = dev
}

// Current reports the still-sounding note, if any — the tuner view and
// wait mode read this.
func (t *Tracker) Current() (Note, bool) {
	if !t.open {
		return Note{}, false
	}
	return Note{
		Start:   t.start,
		Key:     t.key,
		Cents:   median(&t.medScratch, t.cents),
		Clarity: median(&t.medScratch, t.clar),
	}, true
}

// Flush closes and returns any in-progress note (end of stream).
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

// openNote opens a note at key with the candidacy's accumulated history
// (cents are relative to key).
func (t *Tracker) openNote(key int, start, last int64, cents, clar []float64) {
	t.open = true
	t.key = key
	t.start = start
	t.lastVoiced = last
	t.cents = append(t.cents[:0], cents...)
	t.clar = append(t.clar[:0], clar...)
	t.prevDev = cents[len(cents)-1]
}

// closeNote closes the open note at end and appends it to t.notes.
func (t *Tracker) closeNote(end int64) {
	t.notes = append(t.notes, Note{
		Start:   t.start,
		End:     end,
		Key:     t.key,
		Cents:   median(&t.medScratch, t.cents),
		Clarity: median(&t.medScratch, t.clar),
	})
	t.open = false
	t.cents = t.cents[:0]
	t.clar = t.clar[:0]
	t.prevDev = 0
}

// startJump begins a key-change candidacy at key k for a hop at MIDI
// pitch m.
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

// medianF0 pushes f0 into the causal median filter and returns the median
// of the last trackerMedianHops voiced estimates.
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

// midiPitch converts a frequency to a fractional MIDI key (A4 = 69 =
// 440 Hz).
func midiPitch(f0 float64) float64 {
	return 69 + 12*math.Log2(f0/440)
}

// median returns the median of xs, sorting a copy kept in *scratch so
// callers' slices are left in order.
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
