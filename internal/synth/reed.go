package synth

import "math"

// The built-in sustained wind voice. A blown note is nothing like a
// plucked one: it sits at full level for as long as the breath holds it,
// stops shortly after the breath stops, and a slurred note change re-pitches
// the standing tone without any new attack at all. None of that fits a
// decaying delay loop, so this is its own model: band-limited wavetable
// oscillators under an attack/sustain/release envelope. The pluck stays the
// voice for fretted tracks; NewBuiltin picks between the two by the track's
// General MIDI program.
//
// Honesty note: this is a placeholder reed, not a modeled saxophone — a
// harmonic-series tone with a breath-shaped envelope. It is in tune, it
// sustains, it slurs, and the practice pipeline's gates and detector hear
// it well, which is what a built-in voice is for. A SoundFont (-sf2) gives
// a real sax.
const (
	// reedPolyphony is the voice-pool size. A wind line is monophonic by
	// construction (the score model allows one note per beat), so the pool
	// only ever holds one blown note plus the release tails of its
	// predecessors — but a fretted track pointed at a wind program can
	// still send chords, and the pool absorbs them rather than asserting
	// a monophony the caller never promised.
	reedPolyphony = 4
	// reedTableSize is the single-cycle wavetable length. 2048 keeps the
	// interpolation error far below audibility at every playable pitch.
	reedTableSize = 2048
	// reedMaxHarmonics caps the table bank's richest level. 64 harmonics
	// of the soprano sax's lowest note reach past 13 kHz.
	reedMaxHarmonics = 64
	// reedRolloff is the exponent of the 1/k^reedRolloff harmonic
	// amplitude fall. Chosen by ear against GM sax patches: enough high
	// harmonics to read as a reed, not so many it reads as a string buzz.
	reedRolloff = 1.15
	// reedAttackSeconds is the rise from silence to full level — the note
	// speaking. Winds speak in tens of milliseconds; 25 ms keeps the
	// onset detector's rising edge sharp while staying click-free.
	reedAttackSeconds = 0.025
	// reedReleaseT60 is the -60 dB time after NoteOff: the breath
	// stopping. Far faster than any string's ring-out, slow enough not
	// to click.
	reedReleaseT60 = 0.09
	// reedKillSeconds is the AllNotesOff ramp to silence, as the pluck's.
	reedKillSeconds = 0.005
	// reedLevel is the output peak at velocity 1. It lands in the same
	// calibrated window as the pluck's output (tone_test pins that window
	// there), because internal/pitch's input gate and onset thresholds
	// are tuned against what the built-in voices put out.
	reedLevel = 0.30
	// reedSlideSeconds is how long a scoop or fall travels. A wind slide
	// is an embouchure-and-fingers gesture, not a finger on a string, but
	// it occupies the same few tens of milliseconds.
	reedSlideSeconds = 0.06
	// reedVibratoHz and reedVibratoCents are an unhurried jazz vibrato.
	reedVibratoHz    = 5.0
	reedVibratoCents = 25.0
	// reedFloor is the envelope level below which a released voice frees
	// its slot — far under audibility.
	reedFloor = 1e-4
)

// A reedVoice is one sounding (or releasing) wind note: a phase
// accumulator over a shared band-limited table, an envelope, and the
// glide/vibrato state a NoteSpec can arm.
type reedVoice struct {
	table []float32 // the bank level chosen for this note's pitch
	phase float64   // table position in cycles, [0, 1)
	inc   float64   // cycles per sample at the note's pitch

	// Glide state: while sliding, inc walks toward dst by step each
	// sample; step == 0 means the pitch is holding.
	dst, step float64

	// Vibrato state, as the pluck's: phase in cycles.
	vib      bool
	vibPhase float64
	vibInc   float64

	key int    // sounding MIDI key (the DESTINATION key while gliding)
	age uint64 // note-on order, for oldest-note stealing

	held    bool // breath still on (before NoteOff)
	active  bool // occupies its slot (sounding or releasing)
	killing bool // AllNotesOff ramp in progress

	env        float32 // attack/sustain/release envelope, 0..1
	attackStep float32 // env rise per sample while attacking
	releaseMul float32 // env multiplier per sample once released
	amp        float32 // velocity level times reedLevel
	kill       float32 // amp decrement per sample while killing
}

// tick advances the voice one sample and returns its (mono) output.
func (v *reedVoice) tick() float32 {
	if v.step != 0 {
		v.inc += v.step
		if (v.step > 0 && v.inc >= v.dst) || (v.step < 0 && v.inc <= v.dst) {
			v.inc, v.step = v.dst, 0
		}
	}
	inc := v.inc
	if v.vib {
		v.vibPhase += v.vibInc
		if v.vibPhase >= 1 {
			v.vibPhase--
		}
		inc *= 1 + reedVibratoDepth*triangle(v.vibPhase)
	}

	p := v.phase * reedTableSize
	i0 := int(p)
	if i0 >= reedTableSize { // float rounding at the seam
		i0 = reedTableSize - 1
	}
	i1 := i0 + 1
	if i1 == reedTableSize {
		i1 = 0
	}
	frac := float32(p - math.Floor(p))
	s := v.table[i0] + frac*(v.table[i1]-v.table[i0])

	v.phase += inc
	if v.phase >= 1 {
		v.phase--
	}

	if v.held {
		if v.env < 1 {
			v.env += v.attackStep
			if v.env > 1 {
				v.env = 1
			}
		}
	} else {
		v.env *= v.releaseMul
		if v.env < reedFloor {
			v.active = false
		}
	}
	out := s * v.env * v.amp
	if v.killing {
		v.amp -= v.kill
		if v.amp <= 0 {
			v.amp = 0
			v.active = false
		}
	}
	return out
}

// reedVibratoDepth is reedVibratoCents as a relative change in phase
// increment (frequency and increment move together).
var reedVibratoDepth = math.Pow(2, reedVibratoCents/1200) - 1

// A reed is a fixed pool of wind voices over a shared band-limited table
// bank, implementing Voice and Articulator. The bank is built once at
// construction; NoteOn, NoteOnSpec, NoteOff, AllNotesOff, and Render never
// allocate or lock.
type reed struct {
	sampleRate int
	counter    uint64
	voices     [reedPolyphony]reedVoice
	// bank[l] holds harmonics 1..2^l of the cycle, each table normalized
	// to peak 1. tableFor picks the richest level that stays below
	// Nyquist at a note's pitch.
	bank [][]float32
}

// NewReed returns the built-in sustained wind voice — a reed-like
// harmonic tone that holds while blown, for the tracks NewBuiltin
// recognises as winds. program, the track's General MIDI hint, is
// ignored: one placeholder reed serves every wind until a SoundFont
// supplies the real timbres. NewReed is a Factory.
func NewReed(sampleRate, program int) Voice {
	r := &reed{sampleRate: sampleRate}
	levels := 1
	for 1<<levels <= reedMaxHarmonics {
		levels++
	}
	r.bank = make([][]float32, levels)
	for l := range r.bank {
		r.bank[l] = buildReedTable(1 << l)
	}
	return r
}

var (
	_ Factory     = NewReed
	_ Articulator = (*reed)(nil)
)

// buildReedTable renders one cycle of the reed spectrum band-limited to
// maxHarm harmonics, normalized to peak 1.
func buildReedTable(maxHarm int) []float32 {
	t := make([]float32, reedTableSize)
	peak := 0.0
	for i := range t {
		x := 2 * math.Pi * float64(i) / reedTableSize
		var s float64
		for k := 1; k <= maxHarm; k++ {
			s += math.Sin(float64(k)*x) / math.Pow(float64(k), reedRolloff)
		}
		t[i] = float32(s)
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}
	if peak > 0 {
		g := float32(1 / peak)
		for i := range t {
			t[i] *= g
		}
	}
	return t
}

// tableFor picks the richest bank level whose top harmonic stays under
// Nyquist with margin at a pitch.
func (r *reed) tableFor(freq float64) []float32 {
	limit := 0.45 * float64(r.sampleRate)
	for l := len(r.bank) - 1; l > 0; l-- {
		if float64(int(1)<<l)*freq <= limit {
			return r.bank[l]
		}
	}
	return r.bank[0]
}

// incFor is the phase increment (cycles per sample) of a MIDI key.
func (r *reed) incFor(key int) float64 {
	return keyFreq(key) / float64(r.sampleRate)
}

// alloc returns a free voice slot, stealing the oldest note when the pool
// is full.
func (r *reed) alloc() *reedVoice {
	var oldest *reedVoice
	for i := range r.voices {
		v := &r.voices[i]
		if !v.active {
			return v
		}
		if oldest == nil || v.age < oldest.age {
			oldest = v
		}
	}
	return oldest
}

// sounding returns the voice currently blowing key, or nil. A released or
// killing voice is on its way out and cannot be slurred from.
func (r *reed) sounding(key int) *reedVoice {
	for i := range r.voices {
		if v := &r.voices[i]; v.active && v.held && !v.killing && v.key == key {
			return v
		}
	}
	return nil
}

// NoteOn attacks key with an ordinary tongued attack.
func (r *reed) NoteOn(key int, velocity float64) {
	r.NoteOnSpec(NoteSpec{Key: key, Velocity: velocity})
}

// NoteOnSpec sounds a note as described. A tongued note takes a fresh
// voice and speaks through the attack ramp; a slurred or slid note takes
// over the voice its From is sounding on — the standing tone re-pitched,
// no new attack, which is exactly what a slur is.
func (r *reed) NoteOnSpec(spec NoteSpec) {
	if spec.Velocity <= 0 {
		return
	}
	velocity := spec.Velocity
	if velocity > 1 {
		velocity = 1
	}
	freq := keyFreq(spec.Key)
	target := r.incFor(spec.Key)

	var v *reedVoice
	if spec.Attack != AttackPluck {
		v = r.sounding(spec.From)
	}
	if v == nil {
		v = r.alloc()
		v.table = r.tableFor(freq)
		v.phase = 0
		v.inc, v.dst, v.step = target, target, 0
		v.key = spec.Key
		v.age = r.counter
		r.counter++
		v.held = true
		v.active = true
		v.killing = false
		v.env = 0
		v.attackStep = float32(1 / (reedAttackSeconds * float64(r.sampleRate)))
		v.releaseMul = releaseMulFor(reedReleaseT60, r.sampleRate)
		v.amp = float32(velocity) * reedLevel
		v.setVibrato(spec.Vibrato, r.sampleRate)
		return
	}

	// The band limit follows the higher of the two pitches, so a scoop
	// upward cannot alias on the way. The tables are phase-aligned sums
	// of the same sines, so the swap is a step in brightness, not a
	// discontinuity the size of the waveform.
	v.table = r.tableFor(keyFreq(max(spec.Key, v.key)))
	if spec.Attack == AttackSlide {
		frames := reedSlideSeconds * float64(r.sampleRate)
		v.dst = target
		v.step = (target - v.inc) / frames
		if v.step == 0 {
			v.inc = target
		}
	} else {
		v.inc, v.dst, v.step = target, target, 0
	}
	v.key = spec.Key
	v.age = r.counter
	r.counter++
	// The breath carries on: level may change with the new note's
	// velocity, but the envelope does not restart.
	v.amp = float32(velocity) * reedLevel
	v.setVibrato(spec.Vibrato, r.sampleRate)
}

// setVibrato arms or disarms the oscillation, starting at a zero crossing
// so an armed note begins at its written pitch.
func (v *reedVoice) setVibrato(on bool, sampleRate int) {
	v.vib = on
	if on {
		v.vibPhase = 0.25
		v.vibInc = reedVibratoHz / float64(sampleRate)
	}
}

// releaseMulFor is the per-sample envelope multiplier that loses 60 dB in
// t60 seconds.
func releaseMulFor(t60 float64, sampleRate int) float32 {
	return float32(math.Pow(10, -3/(t60*float64(sampleRate))))
}

// NoteOff stops the breath on every voice blowing key: a fast release,
// not a ring-out.
func (r *reed) NoteOff(key int) {
	for i := range r.voices {
		if v := &r.voices[i]; v.active && v.held && v.key == key {
			v.held = false
		}
	}
}

// AllNotesOff ramps every sounding voice to silence over reedKillSeconds,
// click-free, as the pluck does.
func (r *reed) AllNotesOff() {
	step := float32(1 / (reedKillSeconds * float64(r.sampleRate)))
	for i := range r.voices {
		if v := &r.voices[i]; v.active && !v.killing {
			v.killing = true
			v.kill = step
			v.held = false
		}
	}
}

// Render adds the pool's output into left and right — centered, one
// player at one spot, unlike the pluck's per-string spread. It allocates
// nothing.
func (r *reed) Render(left, right []float32) {
	any := false
	for i := range r.voices {
		if r.voices[i].active {
			any = true
			break
		}
	}
	if !any {
		return
	}
	const center = float32(0.70710678) // equal-power middle
	for i := range left {
		var m float32
		for vi := range r.voices {
			v := &r.voices[vi]
			if !v.active {
				continue
			}
			m += v.tick()
		}
		s := softClip(m) * center
		left[i] += s
		right[i] += s
	}
}

// NewBuiltin is the built-in Factory: it reads the track's General MIDI
// program the way GM does — programs 56-79 (brass, reeds, pipes) get the
// sustained reed voice, everything else the Karplus-Strong pluck. A wind
// track defaults to its instrument's program (score.WindInstruments), so
// a soprano sax part sustains out of the box; a fretted track that names
// a wind program gets the wind timbre it asked for.
func NewBuiltin(sampleRate, program int) Voice {
	if program >= 56 && program <= 79 {
		return NewReed(sampleRate, program)
	}
	return NewPluck(sampleRate, program)
}

var _ Factory = NewBuiltin
