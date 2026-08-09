package synth

import "math"

// Tunables for the built-in Karplus-Strong pluck voice. They are chosen by
// ear; the tests pin the contract (pitch, decay direction, peak safety),
// not these exact values.
const (
	// pluckPolyphony is the fixed voice-pool size; when the pool is
	// exhausted the oldest sounding note is stolen.
	pluckPolyphony = 16
	// pluckMinKey is the lowest renderable MIDI key (C1, 32.70 Hz).
	// Delay lines are preallocated for its period at construction;
	// lower keys clamp up to it.
	pluckMinKey = 24
	// pluckSustain is the feedback loop gain per period while a note is
	// held: long, guitar-like sustain.
	pluckSustain = 0.996
	// pluckRelease is the loop gain per period after NoteOff: the string
	// keeps ringing but dies much faster.
	pluckRelease = 0.85
	// pluckExcite scales the excitation noise burst at velocity 1.
	pluckExcite = 0.5
	// pluckDrive is the soft-clip drive. Each output channel is bounded
	// to ±1/pluckDrive, so no chord can clip, while low levels pass
	// through essentially linearly.
	pluckDrive = 1.25
	// pluckKillSeconds is the AllNotesOff ramp to silence. Long enough
	// to be click-free, short enough to read as immediate.
	pluckKillSeconds = 0.005
	// pluckWidth is the stereo spread: per-note pan stays within
	// ±pluckWidth/2 of center, so chords have width but no note is
	// hard-panned.
	pluckWidth = 0.7
	// pluckFloor is the envelope level below which a voice frees its
	// slot. It is far under audibility (-100 dBFS) and also keeps decayed
	// delay lines from churning denormals.
	pluckFloor = 1e-5
)

// A ksVoice is one Karplus-Strong string. Its delay line holds one period
// of the waveform; each output sample is read from the line and replaced by
// the decayed average of itself and its neighbor (a one-zero lowpass), so
// high harmonics die faster than the fundamental — the signature of a
// plucked string. The averaging adds half a sample of loop delay, which the
// tuning in NoteOn accounts for.
type ksVoice struct {
	delay []float32 // preallocated to the pluckMinKey period
	n     int       // active delay length in samples
	pos   int       // read/write position
	key   int       // sounding MIDI key
	age   uint64    // note-on order, for oldest-note stealing

	active  bool // sounding (still occupies its slot)
	killing bool // AllNotesOff ramp in progress

	decay     float32 // loop gain applied once per period
	perSample float32 // decay^(1/n): per-sample envelope estimate
	level     float32 // crude amplitude envelope, for slot freeing
	amp       float32 // output gain, ramped down by AllNotesOff
	killStep  float32 // amp decrement per sample while killing

	gainL, gainR float32 // equal-power pan gains
}

// tick advances the string one sample and returns its output.
func (v *ksVoice) tick() float32 {
	cur := v.delay[v.pos]
	next := v.pos + 1
	if next == v.n {
		next = 0
	}
	v.delay[v.pos] = v.decay * 0.5 * (cur + v.delay[next])
	v.pos = next
	out := cur * v.amp
	if v.killing {
		v.amp -= v.killStep
		if v.amp <= 0 {
			v.amp = 0
			v.active = false
		}
	}
	v.level *= v.perSample
	if v.level < pluckFloor {
		v.active = false
	}
	return out
}

// A pluck is a fixed pool of Karplus-Strong strings implementing Voice.
// Everything is preallocated at construction; NoteOn, NoteOff, AllNotesOff,
// and Render never allocate or lock.
type pluck struct {
	sampleRate int
	counter    uint64 // note-on order source
	rng        uint64 // xorshift64 state for excitation noise
	voices     [pluckPolyphony]ksVoice
}

// NewPluck returns the built-in Karplus-Strong plucked-string Voice, a
// guitar-like sound that needs no assets (no SoundFont download). program,
// the track's General MIDI hint, is ignored: this voice only does plucked
// strings. NewPluck is a Factory.
func NewPluck(sampleRate, program int) Voice {
	p := &pluck{sampleRate: sampleRate, rng: 0x9E3779B97F4A7C15}
	maxDelay := int(math.Ceil(float64(sampleRate)/keyFreq(pluckMinKey))) + 2
	for i := range p.voices {
		p.voices[i].delay = make([]float32, maxDelay)
	}
	return p
}

var _ Factory = NewPluck

// keyFreq returns the equal-tempered frequency of a MIDI key (A4 = 69 =
// 440 Hz).
func keyFreq(key int) float64 {
	return 440 * math.Pow(2, float64(key-69)/12)
}

// noise returns the next excitation sample in [-1, 1) from an xorshift64
// generator (allocation- and lock-free, unlike math/rand's global source).
func (p *pluck) noise() float32 {
	p.rng ^= p.rng << 13
	p.rng ^= p.rng >> 7
	p.rng ^= p.rng << 17
	return float32(int32(uint32(p.rng>>32))) / (1 << 31)
}

// alloc returns a free voice slot, stealing the oldest note when the pool
// is full.
func (p *pluck) alloc() *ksVoice {
	var oldest *ksVoice
	for i := range p.voices {
		v := &p.voices[i]
		if !v.active {
			return v
		}
		if oldest == nil || v.age < oldest.age {
			oldest = v
		}
	}
	return oldest
}

// NoteOn attacks key: it fills a string's delay line with a lowpass-
// filtered, zero-mean noise burst scaled by velocity. Keys below
// pluckMinKey clamp up to it; non-positive velocities are ignored.
func (p *pluck) NoteOn(key int, velocity float64) {
	if velocity <= 0 {
		return
	}
	if velocity > 1 {
		velocity = 1
	}
	if key < pluckMinKey {
		key = pluckMinKey
	}

	// The loop's averaging filter adds half a sample of delay, so a line
	// of n samples resonates at sampleRate/(n+0.5): subtract the half
	// sample before rounding.
	f := keyFreq(key)
	n := int(math.Round(float64(p.sampleRate)/f - 0.5))
	v := p.alloc()
	if n < 2 {
		n = 2
	}
	if n > len(v.delay) {
		n = len(v.delay)
	}

	amp := float32(velocity) * pluckExcite
	var lp, sum float32
	for i := 0; i < n; i++ {
		lp += 0.5 * (p.noise() - lp) // soften the burst: less initial fizz
		v.delay[i] = lp
		sum += lp
	}
	mean := sum / float32(n)
	for i := 0; i < n; i++ {
		v.delay[i] = amp * (v.delay[i] - mean) // zero-mean: no DC in the loop
	}

	// Slight deterministic per-key pan so chords have width. key*7 walks
	// pitch classes far apart in pan, so adjacent chord tones spread.
	pan := (float64((key*7)%16)/15 - 0.5) * pluckWidth
	ang := (pan + 1) * math.Pi / 4
	v.gainL = float32(math.Cos(ang))
	v.gainR = float32(math.Sin(ang))

	v.n = n
	v.pos = 0
	v.key = key
	v.age = p.counter
	p.counter++
	v.decay = pluckSustain
	v.perSample = float32(math.Pow(pluckSustain, 1/float64(n)))
	v.level = amp
	v.amp = 1
	v.killing = false
	v.active = true
}

// NoteOff switches every string sounding key to the faster release decay;
// the note rings out instead of stopping dead.
func (p *pluck) NoteOff(key int) {
	for i := range p.voices {
		v := &p.voices[i]
		if v.active && !v.killing && v.key == key {
			v.decay = pluckRelease
			v.perSample = float32(math.Pow(pluckRelease, 1/float64(v.n)))
		}
	}
}

// AllNotesOff ramps every sounding string to silence over pluckKillSeconds.
// The ramp is why it never clicks: output is never hard-zeroed mid-buffer.
func (p *pluck) AllNotesOff() {
	step := float32(1 / (pluckKillSeconds * float64(p.sampleRate)))
	for i := range p.voices {
		v := &p.voices[i]
		if v.active && !v.killing {
			v.killing = true
			v.killStep = step
		}
	}
}

// softClip applies gentle tanh saturation with unity gain at low levels,
// bounding the result to ±1/pluckDrive: soft headroom instead of hard
// clipping when many strings ring at once.
func softClip(x float32) float32 {
	return float32(math.Tanh(float64(pluckDrive*x))) / pluckDrive
}

// Render adds the pool's output into left and right (the Voice contract is
// additive). It allocates nothing.
func (p *pluck) Render(left, right []float32) {
	any := false
	for i := range p.voices {
		if p.voices[i].active {
			any = true
			break
		}
	}
	if !any {
		return
	}
	for i := range left {
		var l, r float32
		for vi := range p.voices {
			v := &p.voices[vi]
			if !v.active {
				continue
			}
			s := v.tick()
			l += s * v.gainL
			r += s * v.gainR
		}
		left[i] += softClip(l)
		right[i] += softClip(r)
	}
}
