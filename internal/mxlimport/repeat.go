package mxlimport

import (
	"fmt"
	"sort"
	"strings"
)

const maxPlanMeasures = maxBars

type measureForm struct {
	forward     bool
	backward    bool
	times       int
	endingStart []int
	endingOpen  bool
	endingStop  bool
	implicit    bool
}

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

func hasRepeats(forms []measureForm) bool {
	for _, f := range forms {
		if f.forward || f.backward || f.endingOpen || f.endingStop {
			return true
		}
	}
	return false
}

func expandOrder(forms []measureForm) (order []int, jumps int, err error) {

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

	defaultStart := 0
	if len(forms) > 0 && forms[0].implicit {
		defaultStart = 1
	}
	repeatStart := defaultStart
	pass := 1
	taken := map[int]int{}
	justJumped := false

	for i := 0; i < len(forms); {
		f := forms[i]
		if f.forward && !justJumped {
			repeatStart, pass = i, 1
		}
		justJumped = false

		if f.endingOpen && !containsInt(f.endingStart, pass) {

			end, ok := endOfEnding(forms, i)
			if !ok || end <= i {
				end = i + 1
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
				total = 2
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
