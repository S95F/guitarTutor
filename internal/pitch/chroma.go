package pitch

import "math"

// Chroma/strum tunables. Phase 4's expected-chord verification (ROADMAP,
// docs/DECISIONS.md D4) needs pitch-class evidence, not a transcription:
// these values shape the fold, and the tests pin the contract (a sounded
// class ends up clearly above an unsounded one), not the exact numbers.
const (
	// chromaMaxHz is the top of the folded range. The guitar's useful
	// partials for pitch-class evidence die out well before this, and
	// folding higher only adds fret/pick noise; folding from MinHz up
	// means a low E's harmonics still land in the right classes even
	// where its fundamental bin is coarse.
	//
	// It was 2500 through Phase 4. Measured over the whole chord corpus
	// (every shape in chordCorpus at every strum spread), dropping it to
	// 2000 raises the WORST sounded-vs-unsounded margin from +0.038 to
	// +0.084 and the mean from +0.272 to +0.287: above ~2 kHz an open
	// chord's partials are dense enough that every class collects some,
	// so the band was contributing more bleed than evidence. The flux
	// onset trigger keeps its own, wider band (onsetFluxMaxHz) because
	// its job — noticing that the spectrum changed — is helped by the
	// extra bins that hurt the fold.
	chromaMaxHz = 2000
	// chromaTiltHz is the knee of the partial weighting: peaks above it
	// are weighted down by chromaTiltHz/f, peaks below it not at all.
	//
	// A plucked string's energy is spread over dozens of partials, and
	// above the first few they stop being pitch-class evidence: by the
	// 16th, a low E has deposited energy in EVERY class, so an unweighted
	// fold of a chord's upper spectrum scores the notes nobody played
	// about as high as the ones somebody did (measured, before this: a
	// strummed E2/B2/E3 put F# above E). Rolling off 1/f above the knee
	// approximates the plucked-string envelope, keeps the fundamental
	// register flat so a bass note does not outvote a treble one, and
	// incidentally demotes the broadband pick transient. The cost is
	// honest: sub-fundamental rumble is weighted as heavily as a high
	// string's fundamental, which is why the peak floor below is set
	// where it is.
	chromaTiltHz = 200
	// chromaPeakFloor drops spectral peaks below this fraction of the
	// hop's strongest peak — window sidelobes, noise, hum — measured on
	// the raw magnitudes, before the tilt.
	chromaPeakFloor = 0.10
	// chromaBaselineScale is how much of the pre-onset spectrum is
	// subtracted from every hop of a strum span; see armBaseline.
	//
	// Under 1 for two reasons: the ringing chord keeps decaying under the
	// new one, so its partials are already smaller than the snapshot by
	// the time the span is folded, and a partial the two chords SHARE
	// must survive the subtraction. Measured over 18 chord-change pairs
	// (a chord struck over another still ringing at ~78% amplitude), 0.4
	// cuts false misses from 12 to 7; 0.8 and above start erasing shared
	// strings and the count climbs back.
	chromaBaselineScale = 0.4
	// defaultStrumWindowHops is Config.StrumWindowHops' default.
	defaultStrumWindowHops = 8
	// semitonesPerLn2 is 12/ln(2): d(12*log2(f))/df * f, the constant in
	// the per-bin semitones-per-bin slope.
	semitonesPerLn2 = 12 / math.Ln2
)

// A chromaFold turns the detector's existing power spectrum into
// octave-folded pitch-class energy. Everything frequency-dependent is
// precomputed at construction, so a hop costs one sqrt per in-range bin
// plus a few multiplies per spectral peak — no logs, no allocations.
//
// Why peaks rather than every bin: the detector's FFT is 2*Window points
// (zero-padded), so at 48 kHz with Window 2048 the bins are 11.7 Hz apart
// while a semitone at A2 is 6 Hz. Assigning raw bins to their nearest
// pitch class therefore misfiles the whole bottom octave — a 110 Hz sine
// has no bin nearer to A2 than to G#2 or A#2. Interpolating each peak
// instead recovers the true partial frequency to a few cents (the 2x
// zero-padding is what makes parabolic interpolation that accurate), and
// leaves the window's leakage skirt out of the fold entirely.
type chromaFold struct {
	lo, hi int       // inclusive bin range folded (neighbors stay in bounds)
	pc     []float32 // fractional pitch class of each bin center, in [0, 12)
	slope  []float32 // semitones per bin at that bin
	weight []float32 // partial weighting at that bin (see chromaTiltHz)
	mag    []float64 // per-hop magnitude scratch, indexed by bin
	prev   []float64 // the last hop's RAW magnitudes (baseline candidate)
	base   []float64 // pre-onset magnitudes subtracted from the span
}

// newChromaFold precomputes the bin tables for an fftLen-point transform of
// a sampleRate stream, folding bins from minHz up to maxHz.
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
	// Bin 0 (DC) is never folded, and the range keeps one bin of headroom
	// on each side so peak picking can always read both neighbors.
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
		// Continuous MIDI pitch of the bin center, folded to a pitch
		// class in [0, 12) with C at 0 (MIDI 0 is a C, so the fold is
		// just mod 12).
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

// armBaseline makes the LAST observed hop the span's baseline: the
// spectrum that was sounding immediately before the attack, i.e. whatever
// chord is still ringing. Subsequent folds subtract it bin by bin.
//
// Bin by bin, not class by class: the previous chord and the new one
// usually share pitch classes (that is what a chord progression is), so
// removing a class wholesale would erase evidence for a string the player
// really did strike. At the bin level a partial that the old chord alone
// produced is cancelled where it sits, while a string that was just struck
// rises far above its old magnitude and survives.
func (c *chromaFold) armBaseline() { copy(c.base, c.prev) }

// clearBaseline forgets the baseline (nothing was sounding).
func (c *chromaFold) clearBaseline() {
	for i := range c.base {
		c.base[i] = 0
	}
	for i := range c.prev {
		c.prev[i] = 0
	}
}

// hop processes one hop's power spectrum (coeff holds |X(k)|^2 in the real
// part, the transform the detector already computed). It always records
// the hop's raw magnitudes as the next baseline candidate; when acc is
// non-nil it also folds the baseline-subtracted magnitudes into it.
//
// acc is left unnormalized: a Strum sums several hops and normalizes once,
// so louder hops of the span weigh more.
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
			continue // not a local maximum worth folding
		}
		// Sub-bin peak position and height; the offset converts to
		// semitones through the bin's precomputed local slope, whose
		// linearization costs at most ~0.03 semitone at the lowest
		// folded bin and less everywhere above it.
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
		// Split between the two adjacent classes so a partial that sits
		// between semitones (the 7th and 11th harmonics do, by 31 and 51
		// cents) is not filed under one of them wholesale.
		i := int(p)
		frac := p - float64(i)
		acc[i%PitchClasses] += val * (1 - frac)
		acc[(i+1)%PitchClasses] += val * frac
	}
}

// normalizeChroma scales acc so its largest bin is 1; an all-zero
// accumulator (no energy to fold) yields an all-zero Chroma.
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
