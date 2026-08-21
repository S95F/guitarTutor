package synth

import "math"

const (
	pluckPolyphony = 16

	pluckMinKey = 24

	pluckExcite = 0.5

	pluckDrive = 1.25

	pluckKillSeconds = 0.005

	pluckWidth = 0.7

	pluckFloor = 1e-5
)

const (
	pluckT60Bass   = 5.0
	pluckT60Treble = 2.0

	pluckT60BassKey   = 40
	pluckT60TrebleKey = 76

	pluckT60Release = 0.45

	pluckLoopMaxGain = 0.99999
)

const (
	pluckPickPos = 0.16

	pluckPickDepth = 0.85

	pluckExciteLPSoft = 0.32
	pluckExciteLPHard = 0.72
)

const (
	pluckSlideSeconds = 0.055

	pluckLegatoExcite = 0.3

	pluckVibratoHz    = 5.5
	pluckVibratoCents = 22.0

	pluckHeadroom = 1.05
)

type ksVoice struct {
	buf []float32
	w   int
	d   float64
	lp  float32

	dst   float64
	dStep float64

	vib      bool
	vibPhase float64
	vibInc   float64

	key int
	age uint64

	active  bool
	killing bool

	decay     float32
	perSample float32
	level     float32
	amp       float32
	killStep  float32

	gainL, gainR float32
}

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

	rp := float64(v.w) - d
	if rp < 0 {
		rp += float64(len(v.buf))
	}
	i0 := int(rp)
	if i0 >= len(v.buf) {
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

func (v *ksVoice) effectiveDelay() float64 {
	d := v.d
	if v.vib {
		d *= 1 + vibratoDepth*triangle(v.vibPhase)
	}

	if d < 1 {
		return 1
	}
	if max := float64(len(v.buf) - 2); d > max {
		return max
	}
	return d
}

var vibratoDepth = math.Pow(2, pluckVibratoCents/1200) - 1

func triangle(phase float64) float64 { return 4*math.Abs(phase-0.5) - 1 }

type pluck struct {
	sampleRate int
	counter    uint64
	rng        uint64
	voices     [pluckPolyphony]ksVoice

	scratch []float32
	pickBuf []float32
}

func NewPluck(sampleRate, program int) Voice {
	p := &pluck{sampleRate: sampleRate, rng: 0x9E3779B97F4A7C15}
	maxDelay := int(math.Ceil(float64(sampleRate)/keyFreq(pluckMinKey)*pluckHeadroom)) + 4
	for i := range p.voices {
		p.voices[i].buf = make([]float32, maxDelay)
	}
	p.scratch = make([]float32, maxDelay)
	p.pickBuf = make([]float32, maxDelay)
	return p
}

var (
	_ Factory     = NewPluck
	_ Articulator = (*pluck)(nil)
)

func keyFreq(key int) float64 {
	return 440 * math.Pow(2, float64(key-69)/12)
}

func t60For(key int) float64 {
	t := float64(key-pluckT60BassKey) / float64(pluckT60TrebleKey-pluckT60BassKey)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return pluckT60Bass + t*(pluckT60Treble-pluckT60Bass)
}

func (p *pluck) setDecay(v *ksVoice, t60 float64) {
	if t60 <= 0 {
		t60 = pluckT60Release
	}
	g := math.Pow(10, -3*v.d/(float64(p.sampleRate)*t60))
	if g > pluckLoopMaxGain {
		g = pluckLoopMaxGain
	}
	v.decay = float32(g)
	v.perSample = float32(math.Pow(10, -3/(float64(p.sampleRate)*t60)))
}

func (p *pluck) noise() float32 {
	p.rng ^= p.rng << 13
	p.rng ^= p.rng >> 7
	p.rng ^= p.rng << 17
	return float32(int32(uint32(p.rng>>32))) / (1 << 31)
}

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

func (p *pluck) sounding(key int) *ksVoice {
	for i := range p.voices {
		if v := &p.voices[i]; v.active && !v.killing && v.key == key {
			return v
		}
	}
	return nil
}

func clampKey(key int) int {
	if key < pluckMinKey {
		return pluckMinKey
	}
	return key
}

func (p *pluck) delayFor(key int) float64 {
	d := math.Round(float64(p.sampleRate)/keyFreq(clampKey(key)) - 0.5)
	if d < 2 {
		d = 2
	}

	if max := float64(len(p.voices[0].buf)) - 2; d > max {
		d = max
	}
	return d
}

func (p *pluck) NoteOn(key int, velocity float64) {
	p.NoteOnSpec(NoteSpec{Key: key, Velocity: velocity})
}

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

	var v *ksVoice
	if spec.Attack != AttackPluck {
		v = p.sounding(clampKey(spec.From))
	}
	if v == nil {
		p.pluckNote(key, velocity, target, spec)
		return
	}

	if spec.Attack == AttackSlide {

		frames := pluckSlideSeconds * float64(p.sampleRate)
		v.dst = target
		v.dStep = (target - v.d) / frames
		if v.dStep == 0 {
			v.d = target
		}
	} else {
		v.d, v.dst, v.dStep = target, target, 0

		p.excite(v, float32(velocity)*pluckExcite*pluckLegatoExcite, 0, false)
	}

	v.key = key
	v.age = p.counter
	p.counter++
	p.setDecay(v, t60For(key))
	if lvl := float32(velocity) * pluckExcite; v.level < lvl {
		v.level = lvl
	}
	v.setVibrato(spec.Vibrato, p.sampleRate)

}

func (p *pluck) pluckNote(key int, velocity, target float64, spec NoteSpec) {
	v := p.alloc()
	v.d, v.dst, v.dStep = target, target, 0
	v.w, v.lp = 0, 0

	clear(v.buf)
	p.excite(v, float32(velocity)*pluckExcite, velocity, true)

	pan := (float64((key*7)%16)/15 - 0.5) * pluckWidth
	ang := (pan + 1) * math.Pi / 4
	v.gainL = float32(math.Cos(ang))
	v.gainR = float32(math.Sin(ang))

	v.key = key
	v.age = p.counter
	p.counter++
	p.setDecay(v, t60For(key))
	v.level = float32(velocity) * pluckExcite
	v.amp = 1
	v.killing = false
	v.active = true
	v.setVibrato(spec.Vibrato, p.sampleRate)
}

func (p *pluck) excite(v *ksVoice, amp float32, velocity float64, replace bool) {
	n := int(math.Ceil(v.d)) + 1
	if n > len(v.buf) {
		n = len(v.buf)
	}
	burst := p.scratch[:n]

	if velocity < 0 {
		velocity = 0
	}
	if velocity > 1 {
		velocity = 1
	}
	a := float32(pluckExciteLPSoft + velocity*(pluckExciteLPHard-pluckExciteLPSoft))
	var lp, sum float32
	for k := range burst {
		lp += a * (p.noise() - lp)
		burst[k] = lp
		sum += lp
	}

	mean := sum / float32(n)
	before := 0.0
	for k := range burst {
		burst[k] -= mean
		if a := math.Abs(float64(burst[k])); a > before {
			before = a
		}
	}
	p.pickFilter(burst, int(math.Round(pluckPickPos*v.d)))
	rescale(burst, before)

	i := v.w - n
	if i < 0 {
		i += len(v.buf)
	}
	for k := range burst {
		s := amp * burst[k]
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

func (p *pluck) pickFilter(burst []float32, m int) {
	n := len(burst)
	if m <= 0 || m >= n {
		return
	}
	prev := p.pickBuf[:n]
	copy(prev, burst)
	const g = float32(pluckPickDepth)
	for k := range burst {
		j := k - m
		if j < 0 {
			j += n
		}
		burst[k] = prev[k] - g*prev[j]
	}
}

func rescale(burst []float32, want float64) {
	if want <= 0 {
		return
	}
	have := 0.0
	for _, s := range burst {
		if a := math.Abs(float64(s)); a > have {
			have = a
		}
	}
	if have <= 0 {
		return
	}
	g := float32(want / have)
	for k := range burst {
		burst[k] *= g
	}
}

func (v *ksVoice) setVibrato(on bool, sampleRate int) {
	v.vib = on
	if on {
		v.vibPhase = 0.25
		v.vibInc = pluckVibratoHz / float64(sampleRate)
	}
}

func (p *pluck) NoteOff(key int) {
	key = clampKey(key)
	for i := range p.voices {
		v := &p.voices[i]
		if v.active && !v.killing && v.key == key {
			p.setDecay(v, pluckT60Release)
		}
	}
}

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

func softClip(x float32) float32 {
	return float32(math.Tanh(float64(pluckDrive*x))) / pluckDrive
}

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
