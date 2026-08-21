package synth

import "math"

const (
	reedPolyphony = 8

	reedTableSize = 2048

	reedMaxHarmonics = 64

	reedRolloff = 1.15

	reedAttackSeconds = 0.025

	reedReleaseT60 = 0.09

	reedKillSeconds = 0.005

	reedLevel = 0.30

	reedSlideSeconds = 0.06

	reedVibratoHz    = 5.0
	reedVibratoCents = 25.0

	reedFloor = 1e-4
)

type reedVoice struct {
	table []float32
	phase float64
	inc   float64

	dst, step float64

	vib      bool
	vibPhase float64
	vibInc   float64

	key int
	age uint64

	held    bool
	active  bool
	killing bool

	env        float32
	attackStep float32
	releaseMul float32
	amp        float32
	kill       float32
}

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
	if i0 >= reedTableSize {
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

var reedVibratoDepth = math.Pow(2, reedVibratoCents/1200) - 1

type reed struct {
	sampleRate int
	counter    uint64
	voices     [reedPolyphony]reedVoice

	bank [][]float32
}

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

func (r *reed) tableFor(freq float64) []float32 {
	limit := 0.45 * float64(r.sampleRate)
	for l := len(r.bank) - 1; l > 0; l-- {
		if float64(int(1)<<l)*freq <= limit {
			return r.bank[l]
		}
	}
	return r.bank[0]
}

func (r *reed) incFor(key int) float64 {
	return keyFreq(key) / float64(r.sampleRate)
}

func (r *reed) alloc() *reedVoice {
	var oldestReleased, oldestHeld *reedVoice
	for i := range r.voices {
		v := &r.voices[i]
		if !v.active {
			return v
		}
		if !v.held {
			if oldestReleased == nil || v.age < oldestReleased.age {
				oldestReleased = v
			}
		} else if oldestHeld == nil || v.age < oldestHeld.age {
			oldestHeld = v
		}
	}
	if oldestReleased != nil {
		return oldestReleased
	}
	return oldestHeld
}

func (r *reed) sounding(key int) *reedVoice {
	for i := range r.voices {
		if v := &r.voices[i]; v.active && v.held && !v.killing && v.key == key {
			return v
		}
	}
	return nil
}

func (r *reed) NoteOn(key int, velocity float64) {
	r.NoteOnSpec(NoteSpec{Key: key, Velocity: velocity})
}

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

	v.amp = float32(velocity) * reedLevel
	v.setVibrato(spec.Vibrato, r.sampleRate)
}

func (v *reedVoice) setVibrato(on bool, sampleRate int) {
	v.vib = on
	if on {
		v.vibPhase = 0.25
		v.vibInc = reedVibratoHz / float64(sampleRate)
	}
}

func releaseMulFor(t60 float64, sampleRate int) float32 {
	return float32(math.Pow(10, -3/(t60*float64(sampleRate))))
}

func (r *reed) NoteOff(key int) {
	for i := range r.voices {
		if v := &r.voices[i]; v.active && v.held && v.key == key {
			v.held = false
		}
	}
}

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
	const center = float32(0.70710678)
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

func NewBuiltin(sampleRate, program int) Voice {
	if program >= 56 && program <= 79 {
		return NewReed(sampleRate, program)
	}
	return NewPluck(sampleRate, program)
}

var _ Factory = NewBuiltin
