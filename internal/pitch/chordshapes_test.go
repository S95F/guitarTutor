package pitch

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

type chordShape struct {
	name string
	keys []int
}

var chordCorpus = []chordShape{

	{"open E", []int{40, 47, 52, 56, 59, 64}},

	{"Em", []int{40, 47, 52, 55, 59, 64}},

	{"open G", []int{43, 47, 50, 55, 59, 67}},

	{"open A", []int{45, 52, 57, 61, 64}},

	{"Am", []int{45, 52, 57, 60, 64}},

	{"open C", []int{48, 52, 55, 60, 64}},

	{"open D", []int{50, 57, 62, 66}},

	{"E5", []int{40, 47, 52}},
}

var strumSpreads = []struct {
	name   string
	spread float64
}{
	{"simultaneous", 0},
	{"5ms", 0.005},
	{"12ms", 0.012},
	{"20ms", 0.020},
}

func (s chordShape) soundedClasses() map[int]bool {
	m := make(map[int]bool, len(s.keys))
	for _, k := range s.keys {
		m[ChromaOf(k)] = true
	}
	return m
}

func ksStrum(seconds, spread float64, keys ...int) []float32 {
	v := ksVoice()
	total := int(seconds * testSR)
	step := int(math.Round(spread * testSR))
	out := make([]float32, 0, total)
	next := 0
	for pos := 0; pos < total; {
		for next < len(keys) && next*step <= pos {
			v.NoteOn(keys[next], 0.8)
			next++
		}

		end := total
		if next < len(keys) {
			if at := next * step; at < end {
				end = at
			}
		}
		if end == pos {
			end = pos + 1
		}
		out = ksRender(v, float64(end-pos)/testSR, out)
		pos = end
	}
	return out
}

type chordMargin struct {
	minSounded float64
	maxOther   float64
	worstClass int
	loudOther  int
	chroma     Chroma
}

func (m chordMargin) ok() bool { return m.minSounded > m.maxOther }

func (m chordMargin) String() string {
	return fmt.Sprintf("weakest sounded %s=%.2f vs strongest unsounded %s=%.2f (margin %+.2f)",
		pitchClassNames[m.worstClass], m.minSounded,
		pitchClassNames[m.loudOther], m.maxOther, m.minSounded-m.maxOther)
}

func measureShape(t *testing.T, cfg Config, s chordShape, spread float64) chordMargin {
	t.Helper()
	x := append(silence(0.2), ksStrum(1.0, spread, s.keys...)...)
	d := NewDetector(cfg)
	_, strums := feedStrums(d, x, 480)
	if len(strums) != 1 {
		t.Fatalf("%s: got %d strums, want exactly 1", s.name, len(strums))
	}
	return marginOf(strums[0].Chroma, s.soundedClasses())
}

func marginOf(c Chroma, sounded map[int]bool) chordMargin {
	m := chordMargin{minSounded: math.Inf(1), maxOther: math.Inf(-1), chroma: c}
	for i, v := range c {
		if sounded[i] {
			if float64(v) < m.minSounded {
				m.minSounded, m.worstClass = float64(v), i
			}
			continue
		}
		if float64(v) > m.maxOther {
			m.maxOther, m.loudOther = float64(v), i
		}
	}
	return m
}

func TestChordCorpusSoundedClassesRankFirst(t *testing.T) {
	cfg := strumConfig()
	for _, shape := range chordCorpus {
		for _, sp := range strumSpreads {
			t.Run(shape.name+"/"+sp.name, func(t *testing.T) {
				m := measureShape(t, cfg, shape, sp.spread)
				if !m.ok() {
					t.Errorf("%s;%s", m, formatChroma(m.chroma))
				}
			})
		}
	}
}

func TestChordCorpusMarginTable(t *testing.T) {
	cfg := strumConfig()
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-10s", "shape")
	for _, sp := range strumSpreads {
		fmt.Fprintf(&b, " %14s", sp.name)
	}
	b.WriteByte('\n')
	for _, shape := range chordCorpus {
		fmt.Fprintf(&b, "%-10s", shape.name)
		for _, sp := range strumSpreads {
			m := measureShape(t, cfg, shape, sp.spread)
			fmt.Fprintf(&b, " %+14.3f", m.minSounded-m.maxOther)
		}
		b.WriteByte('\n')
	}
	t.Log(b.String())
}

type strumEvent struct {
	at   float64
	keys []int
	sp   float64
}

func renderStrumSeq(total float64, events ...strumEvent) []float32 {
	v := ksVoice()
	type strike struct{ at, key int }
	var strikes []strike
	for _, e := range events {
		for i, k := range e.keys {
			strikes = append(strikes, strike{int((e.at + float64(i)*e.sp) * testSR), k})
		}
	}
	n := int(total * testSR)
	out := make([]float32, 0, n)
	pos, next := 0, 0
	for pos < n {
		for next < len(strikes) && strikes[next].at <= pos {
			v.NoteOn(strikes[next].key, 0.8)
			next++
		}
		end := n
		if next < len(strikes) && strikes[next].at < end {
			end = strikes[next].at
		}
		if end == pos {
			end++
		}
		out = ksRender(v, float64(end-pos)/testSR, out)
		pos = end
	}
	return out
}

var changePairs = [][2]int{{2, 5}, {5, 2}, {0, 3}, {4, 6}, {6, 0}, {3, 1}}

func strumNear(strums []Strum, at, tol float64) *Strum {
	for i := range strums {
		d := strums[i].Frame - int64(at*testSR)
		if d < 0 {
			d = -d
		}
		if d < int64(tol*testSR) {
			return &strums[i]
		}
	}
	return nil
}

func TestChordChangeOverRingingChordEmitsStrum(t *testing.T) {
	cfg := strumConfig()
	for _, gap := range []float64{0.3, 0.45, 0.6} {
		for _, sp := range []float64{0, 0.012, 0.020} {
			for _, pr := range changePairs {
				a, b := chordCorpus[pr[0]], chordCorpus[pr[1]]
				name := fmt.Sprintf("%s->%s/%.0fms/gap%.0fms", a.name, b.name, sp*1000, gap*1000)
				t.Run(name, func(t *testing.T) {
					x := renderStrumSeq(gap+1.2,
						strumEvent{0.2, a.keys, sp},
						strumEvent{0.2 + gap, b.keys, sp})
					d := NewDetector(cfg)
					_, strums := feedStrums(d, x, 480)
					if strumNear(strums, 0.2+gap, 0.12) == nil {
						t.Errorf("no Strum within 120 ms of the second chord (%d strums total)", len(strums))
					}
				})
			}
		}
	}
}

func TestChordChangeMarginTable(t *testing.T) {
	cfg := strumConfig()
	var b strings.Builder
	b.WriteString("\nchord change over a still-ringing chord, gap 600 ms\n")
	negative, total := 0, 0
	for _, sp := range []float64{0, 0.012, 0.020} {
		for _, pr := range changePairs {
			a, c := chordCorpus[pr[0]], chordCorpus[pr[1]]
			x := renderStrumSeq(2.0, strumEvent{0.2, a.keys, sp}, strumEvent{0.8, c.keys, sp})
			d := NewDetector(cfg)
			_, strums := feedStrums(d, x, 480)
			s := strumNear(strums, 0.8, 0.12)
			if s == nil {
				t.Errorf("%s -> %s at %.0f ms spread: no Strum", a.name, c.name, sp*1000)
				continue
			}
			m := marginOf(s.Chroma, c.soundedClasses())
			total++
			if !m.ok() {
				negative++
			}
			fmt.Fprintf(&b, "%4.0fms %-9s -> %-9s %+.3f  (%s)\n",
				sp*1000, a.name, c.name, m.minSounded-m.maxOther, m)
		}
	}
	fmt.Fprintf(&b, "%d of %d transitions still rank an unsounded class first\n", negative, total)
	t.Log(b.String())

	if negative > 6 {
		t.Errorf("%d of %d transitions rank an unsounded class first, want <= 6", negative, total)
	}
}
