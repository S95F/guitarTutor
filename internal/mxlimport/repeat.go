package mxlimport

import (
	"fmt"
	"sort"
	"strings"
)

// Repeat structure is read here rather than discarded, because discarding
// it is silent data loss of a kind the rest of the package refuses to
// commit: a piece whose form is "AABA via repeat signs" would import as
// "ABA" — a third of the music missing — with nothing to tell the user the
// practice session is not the whole piece. Neither README.md, ROADMAP.md
// ("MusicXML subset importer", Phase 3) nor this package's own list of
// subset boundaries ever claimed repeats were unimplemented, so there was
// no documented limitation to fall back on.
//
// Forward/backward repeats with voltas are expanded into a flat play
// order. D.C./D.S./coda/fine jumps are NOT: their semantics compose (al
// coda, al fine, second-time-only, jumps into the middle of a repeated
// section) in ways no bounded reading of the markup settles, and a wrong
// expansion is worse than none — so they warn instead.
//
// The order is computed once from the union of every part's barlines and
// then applied to all parts. Parts share barlines in MusicXML, but a file
// may carry them on only one part, and expanding parts independently would
// desynchronise them — which would be worse than not expanding at all.

// maxPlanMeasures caps the expanded play order. barSpecs caps the score
// extent afterwards, but a file claiming <repeat times="100000"> would
// otherwise build the order itself into an unbounded allocation first.
const maxPlanMeasures = maxBars

// A measureForm is the repeat and volta markup on one measure, merged
// across every part.
type measureForm struct {
	forward     bool  // a forward repeat sign opens a section here
	backward    bool  // a backward repeat sign closes one here
	times       int   // total plays of the section, from <repeat times>
	endingStart []int // the passes the volta opening here is played on
	endingOpen  bool  // a volta opens here (even if its numbers were unreadable)
	endingStop  bool  // a volta closes or discontinues here
	implicit    bool  // pickup measure
}

// scanForm merges every part's structural markup into one form per measure
// index, and collects the jump directions found anywhere in the document.
func scanForm(doc *xmlScorePartwise) (forms []measureForm, jumps []string) {
	n := 0
	for i := range doc.Parts {
		if m := len(doc.Parts[i].Measures); m > n {
			n = m
		}
	}
	forms = make([]measureForm, n)
	seen := map[string]bool{}
	for pi := range doc.Parts {
		for mi := range doc.Parts[pi].Measures {
			meas := &doc.Parts[pi].Measures[mi]
			f := &forms[mi]
			if meas.Implicit {
				f.implicit = true
			}
			for bi := range meas.Barlines {
				bl := &meas.Barlines[bi]
				if r := bl.Repeat; r != nil {
					switch r.Direction {
					case "forward":
						f.forward = true
					case "backward":
						f.backward = true
						if r.Times > f.times {
							f.times = r.Times
						}
					}
				}
				if e := bl.Ending; e != nil {
					switch e.Type {
					case "start":
						f.endingOpen = true
						if p := e.passes(); p != nil && f.endingStart == nil {
							f.endingStart = p
						}
					case "stop", "discontinue":
						f.endingStop = true
					}
				}
			}
			for _, el := range meas.Elements {
				var marks []string
				switch e := el.(type) {
				case *xmlDirection:
					marks = e.jumpMarks()
				case *xmlSound:
					marks = e.jumpMarks()
				}
				for _, m := range marks {
					if !seen[m] {
						seen[m] = true
						jumps = append(jumps, m)
					}
				}
			}
		}
	}
	sort.Strings(jumps)
	return forms, jumps
}

// hasRepeats reports whether any measure carries repeat or volta markup.
func hasRepeats(forms []measureForm) bool {
	for _, f := range forms {
		if f.forward || f.backward || f.endingOpen || f.endingStop {
			return true
		}
	}
	return false
}

// expandOrder turns the repeat structure into the flat sequence of measure
// indices the piece is actually played in, and reports how many repeat
// jumps it took. An error means the markup is malformed or unbounded; the
// caller then keeps the written order and warns, because a half-right
// expansion silently teaches the wrong music.
func expandOrder(forms []measureForm) (order []int, jumps int, err error) {
	// Volta brackets are checked up front rather than only where the walk
	// happens to need one: a file whose voltas do not balance is malformed
	// whichever pass first tries to skip one, and the decision to expand
	// must not depend on that accident.
	for i := range forms {
		if !forms[i].endingOpen {
			continue
		}
		if forms[i].endingStart == nil {
			return nil, 0, fmt.Errorf("a volta has an unreadable number attribute")
		}
		if _, ok := endOfEnding(forms, i); !ok {
			return nil, 0, fmt.Errorf("a volta opens and never closes")
		}
	}

	// A backward repeat with no forward sign returns to the start of the
	// piece. A leading pickup is excluded from that: replaying the
	// anacrusis would drop its padded bar into the middle of the piece,
	// and the near-universal reading is a return to the first full bar.
	defaultStart := 0
	if len(forms) > 0 && forms[0].implicit {
		defaultStart = 1
	}
	repeatStart := defaultStart
	pass := 1
	taken := map[int]int{} // backward-repeat measure -> jumps already made from it
	justJumped := false

	for i := 0; i < len(forms); {
		f := forms[i]
		if f.forward && !justJumped {
			repeatStart, pass = i, 1
		}
		justJumped = false

		if f.endingOpen && !containsInt(f.endingStart, pass) {
			// This volta is not played on this pass: resume after it. The
			// bracket is known to close (checked above), so end > i and
			// the walk always advances.
			end, ok := endOfEnding(forms, i)
			if !ok || end <= i {
				end = i + 1 // unreachable; keeps the walk strictly advancing
			}
			i = end
			continue
		}

		order = append(order, i)
		if len(order) > maxPlanMeasures {
			return nil, 0, fmt.Errorf("repeats expand past the %d-measure limit", maxPlanMeasures)
		}

		if f.backward {
			total := f.times
			if total < 2 {
				total = 2 // an unstated <repeat times> means play it twice
			}
			if taken[i] < total-1 {
				taken[i]++
				jumps++
				pass++
				i = repeatStart
				justJumped = true
				continue
			}
		}
		i++
	}
	return order, jumps, nil
}

// endOfEnding returns the index just past the volta opening at i, i.e. the
// measure to resume from when this volta is not played on this pass.
func endOfEnding(forms []measureForm, i int) (int, bool) {
	for k := i; k < len(forms); k++ {
		if forms[k].endingStop {
			return k + 1, true
		}
	}
	return 0, false
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// playOrder decides the measure order the whole document is imported in.
// It always returns a usable order: on malformed or unbounded repeat
// markup it falls back to the written order and warns, so the import
// degrades rather than failing or, worse, expanding wrongly.
func (im *importer) playOrder(doc *xmlScorePartwise) []int {
	forms, jumps := scanForm(doc)
	if len(jumps) > 0 {
		im.warnf("jump directions (%s) are not followed; the measures import in written order",
			strings.Join(jumps, ", "))
	}
	written := make([]int, len(forms))
	for i := range written {
		written[i] = i
	}
	if !hasRepeats(forms) {
		return written
	}
	order, taken, err := expandOrder(forms)
	if err != nil {
		im.warnf("repeat structure not expanded (%v); the measures import once through, so the import is not the whole piece", err)
		return written
	}
	if taken > 0 {
		im.warnf("expanded %d repeat(s): %d written measure(s) play as %d", taken, len(forms), len(order))
	}
	return order
}
