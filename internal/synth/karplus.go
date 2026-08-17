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

// Articulation tunables (see NoteSpec). A slide and a hammer-on are the
// same gesture to this voice — a string that is already ringing takes a new
// pitch — and differ only in whether the pitch travels and how much energy
// the fretting hand adds.
const (
	// pluckSlideSeconds is how long a slide takes to travel, whatever the
	// interval and whatever the practice tempo. A finger moving along a
	// string takes about as long at any speed, so this is wall-clock and
	// not a musical duration; it is the one number that decides whether a
	// slide reads as a slide or as a fast portamento.
	pluckSlideSeconds = 0.055
	// pluckLegatoExcite is the energy a hammer-on or a pull-off adds,
	// relative to a full pick. The fretting hand does put some energy
	// into the string, but far less than the pick, and a legato note that
	// added none at all disappeared under whatever the string was already
	// doing.
	pluckLegatoExcite = 0.3
	// pluckVibratoHz and pluckVibratoCents are the vibrato rate and its
	// peak deviation either side of the written pitch — a fifth of a
	// semitone at five and a half hertz, which is an unhurried hand.
	pluckVibratoHz    = 5.5
	pluckVibratoCents = 22.0
	// pluckHeadroom is the slack on the preallocated delay line, over the
	// longest period any note can need. Vibrato widens the loop past the
	// note's own period, and the interpolated read looks one sample
	// further back again.
	pluckHeadroom = 1.05
)

// A ksVoice is one Karplus-Strong string. Its delay line holds one period
// of the waveform; each output sample is read from the line, averaged with
// the previous one (a one-zero lowpass), decayed and written back, so high
// harmonics die faster than the fundamental — the signature of a plucked
// string. The averaging filter contributes exactly half a sample of loop
// delay, which the tuning in delayFor accounts for.
//
// The loop length is fractional and the read is interpolated between the
// two samples it falls between. That is what lets the pitch MOVE: a slide
// is this delay walking from one length to another, and vibrato is it
// wobbling. A note at rest still sits on a whole number of samples, where
// the interpolation reads one sample and changes nothing — see delayFor for
// why that rounding is deliberate.
type ksVoice struct {
	buf []float32 // circular delay line; only the last `d` samples are in the loop
	w   int       // write index
	d   float64   // loop delay in samples, fractional
	lp  float32   // one-zero lowpass state: the previous sample read

	// Glide state. While gliding d walks toward dst by dStep each sample;
	// dStep == 0 means the pitch is holding.
	dst   float64
	dStep float64

	// Vibrato state. vibPhase runs in cycles, [0, 1).
	vib      bool
	vibPhase float64
	vibInc   float64

	key int    // sounding MIDI key (the DESTINATION key while gliding)
	age uint64 // note-on order, for oldest-note stealing

	active  bool // sounding (still occupies its slot)
	killing bool // AllNotesOff ramp in progress

	decay     float32 // loop gain applied once per period
	perSample float32 // decay^(1/d): per-sample envelope estimate
	level     float32 // crude amplitude envelope, for slot freeing
	amp       float32 // output gain, ramped down by AllNotesOff
	killStep  float32 // amp decrement per sample while killing

	gainL, gainR float32 // equal-power pan gains
}

// tick advances the string one sample and returns its output.
func (v *ksVoice) tick() float32 {
	if v.dStep != 0 {
		v.d += v.dStep
		if (v.dStep > 0 && v.d >= v.dst) || (v.dStep < 0 && v.d <= v.dst) {
			v.d, v.dStep = v.dst, 0
		}
	}
	if v.vib {
		v.vibPhase += v.vibInc
		if v.vibPhase >= 1 {
			v.vibPhase--
		}
	}
	d := v.effectiveDelay()

	// Read d samples behind the write head, interpolating between the two
	// samples it falls between.
	rp := float64(v.w) - d
	if rp < 0 {
		rp += float64(len(v.buf))
	}
	i0 := int(rp)
	if i0 >= len(v.buf) { // only reachable through float rounding at the seam
		i0 -= len(v.buf)
	}
	i1 := i0 + 1
	if i1 == len(v.buf) {
		i1 = 0
	}
	frac := float32(rp - math.Floor(rp))
	cur := v.buf[i0] + frac*(v.buf[i1]-v.buf[i0])

	v.buf[v.w] = v.decay * 0.5 * (cur + v.lp)
	v.lp = cur
	v.w++
	if v.w == len(v.buf) {
		v.w = 0
	}

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

// effectiveDelay is the loop delay this sample: the note's own, wobbled by
// vibrato while it is armed. The wobble is applied here rather than to d
// itself so that the note's actual pitch — where a glide is heading, and
// where it comes to rest — is never disturbed by the oscillation around it.
func (v *ksVoice) effectiveDelay() float64 {
	d := v.d
	if v.vib {
		d *= 1 + vibratoDepth*triangle(v.vibPhase)
	}
	// This is the number that indexes the delay line, so it is also where
	// the read is made provably in bounds. delayFor already keeps the
	// resting delay well inside the line and pluckHeadroom leaves room for
	// the vibrato on top, so neither bound binds today — but they are two
	// separate pieces of arithmetic in two files, and if a future tuning
	// change makes them disagree the symptom is an out-of-range index on
	// the audio thread rather than a note that sounds wrong.
	if d < 1 {
		return 1
	}
	if max := float64(len(v.buf) - 2); d > max {
		return max
	}
	return d
}

// vibratoDepth is pluckVibratoCents as a relative change in loop delay.
// Delay and frequency are reciprocal, so a longer loop is a flatter note;
// the sign does not matter for an oscillation about the pitch.
var vibratoDepth = math.Pow(2, pluckVibratoCents/1200) - 1

// triangle is one cycle of a triangle wave in [-1, 1] for a phase in
// [0, 1). Vibrato does not need a sine — at five hertz and a fifth of a
// semitone the difference is inaudible — and this costs no trig call on
// every sample of every sounding voice.
func triangle(phase float64) float64 { return 4*math.Abs(phase-0.5) - 1 }

// A pluck is a fixed pool of Karplus-Strong strings implementing Voice and
// Articulator. Everything is preallocated at construction; NoteOn,
// NoteOnSpec, NoteOff, AllNotesOff, and Render never allocate or lock.
type pluck struct {
	sampleRate int
	counter    uint64 // note-on order source
	rng        uint64 // xorshift64 state for excitation noise
	voices     [pluckPolyphony]ksVoice
	// scratch is where an excitation burst is built before it reaches a
	// delay line; sized for the longest one at construction. Note-ons are
	// serialized with Render on the one render goroutine, so a single
	// shared buffer is enough.
	scratch []float32
}

// NewPluck returns the built-in Karplus-Strong plucked-string Voice, a
// guitar-like sound that needs no assets (no SoundFont download). program,
// the track's General MIDI hint, is ignored: this voice only does plucked
// strings. NewPluck is a Factory.
func NewPluck(sampleRate, program int) Voice {
	p := &pluck{sampleRate: sampleRate, rng: 0x9E3779B97F4A7C15}
	maxDelay := int(math.Ceil(float64(sampleRate)/keyFreq(pluckMinKey)*pluckHeadroom)) + 4
	for i := range p.voices {
		p.voices[i].buf = make([]float32, maxDelay)
	}
	p.scratch = make([]float32, maxDelay)
	return p
}

var (
	_ Factory     = NewPluck
	_ Articulator = (*pluck)(nil)
)

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

// sounding returns the voice currently playing key, or nil. A voice already
// ramping to silence does not count: it is on its way out and cannot be
// slid or hammered from.
func (p *pluck) sounding(key int) *ksVoice {
	for i := range p.voices {
		if v := &p.voices[i]; v.active && !v.killing && v.key == key {
			return v
		}
	}
	return nil
}

// clampKey brings a key up into the range the delay lines were sized for.
func clampKey(key int) int {
	if key < pluckMinKey {
		return pluckMinKey
	}
	return key
}

// delayFor is the loop delay in samples that makes the line resonate at
// key's frequency. The loop averages each sample with the previous one, so
// it contributes half a sample of delay on top of the line's own, and the
// tuning subtracts it back out.
//
// A note at REST is tuned to a whole number of samples, even though the
// delay line no longer needs it to be. That is deliberate, and it is not
// the obvious choice: an interpolated read can hold any pitch exactly, and
// rounding leaves the top of the neck measurably sharp (a sixth of a
// semitone at the 22nd fret, 48 kHz — the loop period is only forty samples
// up there, so one sample is a lot of pitch).
//
// The reason is what exact tuning does to a chord. Six modelled strings
// tuned to the sample are six perfectly harmonic series, and the ones an
// open chord shares then coincide EXACTLY: partials that a real instrument
// keeps slightly apart — no guitar is in tune to a cent, and real strings
// are stiff enough that their upper partials stretch sharp — instead sit on
// top of each other and interfere in slow swells. That is audible as a
// phasiness on sustained chords, and internal/pitch measured it: the
// spectral-flux onset detector read the swell as a second attack on a
// ringing chord, and the margin between "a new chord was strummed" and
// "the old one is still ringing" closed to nothing.
//
// Rounding here restores the resting pitches — and the resting TIMBRE, an
// integer delay reads one sample and interpolates nothing — to exactly what
// they were before the delay line went fractional. Motion is what the
// fractional read is for: a glide travels between two of these, and vibrato
// oscillates around one. Playing the top of the neck in tune needs the
// model to earn it the way a string does, by stretching its partials, and
// that is a change to make on its own with the detector re-measured against
// it, not a rider on articulation.
func (p *pluck) delayFor(key int) float64 {
	d := math.Round(float64(p.sampleRate)/keyFreq(clampKey(key)) - 0.5)
	if d < 2 {
		d = 2
	}
	// One sample of margin for the interpolated read, which looks at d and
	// d+1 samples back and must not reach the sample about to be written.
	if max := float64(len(p.voices[0].buf)) - 2; d > max {
		d = max
	}
	return d
}

// NoteOn attacks key with an ordinary pick. Non-positive velocities are
// ignored.
func (p *pluck) NoteOn(key int, velocity float64) {
	p.NoteOnSpec(NoteSpec{Key: key, Velocity: velocity})
}

// NoteOnSpec attacks a note as described. A plucked note takes a fresh
// voice and a full excitation burst; a slid or hammered note takes over the
// voice its From is ringing on, keeping that string's energy and pan, and
// either walks its pitch across or changes it at once.
func (p *pluck) NoteOnSpec(spec NoteSpec) {
	if spec.Velocity <= 0 {
		return
	}
	velocity := spec.Velocity
	if velocity > 1 {
		velocity = 1
	}
	key := clampKey(spec.Key)
	target := p.delayFor(key)

	// A continuation needs something to continue from. Without it — a
	// score that slides into its first note, or a predecessor that has
	// already decayed away — the note is picked instead, which is the one
	// outcome that is never silent.
	var v *ksVoice
	if spec.Attack != AttackPluck {
		v = p.sounding(clampKey(spec.From))
	}
	if v == nil {
		p.pluckNote(key, velocity, target, spec)
		return
	}

	if spec.Attack == AttackSlide {
		// Travel: d walks from where it is to the new pitch. The step is
		// signed, and tick stops exactly on the target.
		frames := pluckSlideSeconds * float64(p.sampleRate)
		v.dst = target
		v.dStep = (target - v.d) / frames
		if v.dStep == 0 {
			v.d = target // same pitch either side: nothing to travel
		}
	} else {
		v.d, v.dst, v.dStep = target, target, 0
		// The fretting hand puts a little energy in; without it a
		// hammer-on onto a quiet string is inaudible.
		p.excite(v, float32(velocity)*pluckExcite*pluckLegatoExcite, false)
	}

	// The note is held again from here: whatever the predecessor's NoteOff
	// had done to the decay is undone, and the slot is kept alive.
	v.key = key
	v.age = p.counter
	p.counter++
	v.decay = pluckSustain
	v.perSample = float32(math.Pow(pluckSustain, 1/target))
	if lvl := float32(velocity) * pluckExcite; v.level < lvl {
		v.level = lvl
	}
	v.setVibrato(spec.Vibrato, p.sampleRate)
	// The pan is deliberately NOT recomputed. It stands for where the
	// string sits in the stereo field, and a note that slid two frets is
	// the same string: moving it would pan the sound across the image
	// mid-glide.
}

// pluckNote starts key on a fresh voice with a full excitation burst.
func (p *pluck) pluckNote(key int, velocity, target float64, spec NoteSpec) {
	v := p.alloc()
	v.d, v.dst, v.dStep = target, target, 0
	v.w, v.lp = 0, 0
	// Clear the line rather than only the excited part: a glide reads
	// further back than the note has yet written, and a stolen slot's
	// leftovers are the wrong string.
	clear(v.buf)
	p.excite(v, float32(velocity)*pluckExcite, true)

	// Slight deterministic per-key pan so chords have width. key*7 walks
	// pitch classes far apart in pan, so adjacent chord tones spread.
	pan := (float64((key*7)%16)/15 - 0.5) * pluckWidth
	ang := (pan + 1) * math.Pi / 4
	v.gainL = float32(math.Cos(ang))
	v.gainR = float32(math.Sin(ang))

	v.key = key
	v.age = p.counter
	p.counter++
	v.decay = pluckSustain
	v.perSample = float32(math.Pow(pluckSustain, 1/target))
	v.level = float32(velocity) * pluckExcite
	v.amp = 1
	v.killing = false
	v.active = true
	v.setVibrato(spec.Vibrato, p.sampleRate)
}

// excite puts a lowpass-filtered, zero-mean noise burst into the samples
// the loop is about to read back: the n samples ENDING at the write head,
// which is left where it is, so the first sample read back is the start of
// the burst. replace fills them (a pick); otherwise the burst is added to
// whatever the string is already carrying, which is the smaller nudge a
// fretting finger gives a string that is already ringing.
//
// The burst is built in scratch first because its mean is only known once
// all of it exists, and the additive path cannot use the delay line as its
// own workspace without destroying what it is adding to.
func (p *pluck) excite(v *ksVoice, amp float32, replace bool) {
	n := int(math.Ceil(v.d)) + 1
	if n > len(v.buf) {
		n = len(v.buf)
	}
	burst := p.scratch[:n]
	var lp, sum float32
	for k := range burst {
		lp += 0.5 * (p.noise() - lp) // soften the burst: less initial fizz
		burst[k] = lp
		sum += lp
	}
	// Zero-mean: DC in the burst is a step the loop circulates forever,
	// because the decay scales it but never removes it.
	mean := sum / float32(n)

	i := v.w - n
	if i < 0 {
		i += len(v.buf)
	}
	for k := range burst {
		s := amp * (burst[k] - mean)
		if replace {
			v.buf[i] = s
		} else {
			v.buf[i] += s
		}
		i++
		if i == len(v.buf) {
			i = 0
		}
	}
}

// setVibrato arms or disarms the pitch oscillation. It starts at a zero
// crossing of the triangle (phase 1/4), so an armed note begins at its
// written pitch and moves off it, rather than starting a fifth of a
// semitone sharp.
func (v *ksVoice) setVibrato(on bool, sampleRate int) {
	v.vib = on
	if on {
		v.vibPhase = 0.25
		v.vibInc = pluckVibratoHz / float64(sampleRate)
	}
}

// NoteOff switches every string sounding key to the faster release decay;
// the note rings out instead of stopping dead.
func (p *pluck) NoteOff(key int) {
	key = clampKey(key)
	for i := range p.voices {
		v := &p.voices[i]
		if v.active && !v.killing && v.key == key {
			v.decay = pluckRelease
			v.perSample = float32(math.Pow(pluckRelease, 1/v.d))
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
