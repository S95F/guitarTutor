package pitch

import "math"

const (
	chromaMaxHz = 2000

	chromaTiltHz = 200

	chromaPeakFloor = 0.10

	chromaBaselineScale = 0.4

	defaultStrumWindowHops = 8

	semitonesPerLn2 = 12 / math.Ln2
)

type chromaFold struct {
	lo, hi int
	pc     []float32
	slope  []float32
	weight []float32
	mag    []float64
	prev   []float64
	base   []float64
}

func newChromaFold(sampleRate, fftLen int, minHz, maxHz float64) *chromaFold {
	nbins := fftLen/2 + 1
	binHz := float64(sampleRate) / float64(fftLen)

	c := &chromaFold{
		pc:     make([]float32, nbins),
		slope:  make([]float32, nbins),
		weight: make([]float32, nbins),
		mag:    make([]float64, nbins),
		prev:   make([]float64, nbins),
		base:   make([]float64, nbins),
	}

	c.lo = int(math.Ceil(minHz / binHz))
	if c.lo < 1 {
		c.lo = 1
	}
	c.hi = int(math.Floor(maxHz / binHz))
	if c.hi > nbins-2 {
		c.hi = nbins - 2
	}
	for k := 1; k < nbins; k++ {
		f := float64(k) * binHz

		p := 69 + 12*math.Log2(f/440)
		p = math.Mod(p, PitchClasses)
		if p < 0 {
			p += PitchClasses
		}
		c.pc[k] = float32(p)
		c.slope[k] = float32(semitonesPerLn2 / float64(k))
		c.weight[k] = 1
		if f > chromaTiltHz {
			c.weight[k] = float32(chromaTiltHz / f)
		}
	}
	return c
}

func (c *chromaFold) armBaseline() { copy(c.base, c.prev) }

func (c *chromaFold) clearBaseline() {
	for i := range c.base {
		c.base[i] = 0
	}
	for i := range c.prev {
		c.prev[i] = 0
	}
}

func (c *chromaFold) hop(coeff []complex128, acc *[PitchClasses]float64) {
	maxMag := 0.0
	for k := c.lo - 1; k <= c.hi+1; k++ {
		m := math.Sqrt(real(coeff[k]))
		c.prev[k] = m
		if acc != nil {
			if m -= chromaBaselineScale * c.base[k]; m < 0 {
				m = 0
			}
			c.mag[k] = m
			if m > maxMag {
				maxMag = m
			}
		}
	}
	if acc == nil || maxMag <= 0 {
		return
	}
	floor := chromaPeakFloor * maxMag

	for k := c.lo; k <= c.hi; k++ {
		m := c.mag[k]
		if m < floor || m <= c.mag[k-1] || m < c.mag[k+1] {
			continue
		}

		pos, val := parabolicExtremum(c.mag, k)
		if val <= 0 {
			continue
		}
		val *= float64(c.weight[k])
		p := float64(c.pc[k]) + float64(c.slope[k])*(pos-float64(k))
		p = math.Mod(p, PitchClasses)
		if p < 0 {
			p += PitchClasses
		}

		i := int(p)
		frac := p - float64(i)
		acc[i%PitchClasses] += val * (1 - frac)
		acc[(i+1)%PitchClasses] += val * frac
	}
}

func normalizeChroma(acc *[PitchClasses]float64) Chroma {
	max := 0.0
	for _, v := range acc {
		if v > max {
			max = v
		}
	}
	var out Chroma
	if max <= 0 {
		return out
	}
	for i, v := range acc {
		out[i] = float32(v / max)
	}
	return out
}
