package synth

import (
	"bufio"
	"fmt"
	"math"
	"os"

	"github.com/sinshu/go-meltysynth/meltysynth"
)

const sfChunk = 1024

const (
	sfSlots = 15

	sfBendRange = 12

	sfGlideChunk = 64

	sfSlideSeconds = pluckSlideSeconds

	sfVibratoHz    = pluckVibratoHz
	sfVibratoCents = pluckVibratoCents
)

type SoundFont struct {
	sf *meltysynth.SoundFont
}

func LoadSoundFont(path string) (*SoundFont, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load soundfont: %w", err)
	}
	defer f.Close()
	sf, err := meltysynth.NewSoundFont(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("load soundfont %s: %w", path, err)
	}
	return &SoundFont{sf: sf}, nil
}

func NewSoundFontFactory(sf *SoundFont) Factory {
	if sf == nil || sf.sf == nil {
		return nil
	}
	return func(sampleRate, program int) Voice {
		return newSoundFontVoice(sf.sf, sampleRate, program)
	}
}

type sfSlot struct {
	ch int32

	key, logical int32
	held         bool
	age          uint64

	bend float64
	sent int32
	dst  float64
	step float64

	vib      bool
	vibPhase float64
	vibInc   float64
}

type soundFontVoice struct {
	ms         *meltysynth.Synthesizer
	sampleRate int
	scratchL   []float32
	scratchR   []float32
	slots      [sfSlots]sfSlot
	counter    uint64
}

var (
	_ Voice                = (*soundFontVoice)(nil)
	_ Articulator          = (*soundFontVoice)(nil)
	_ ContinuationReporter = (*soundFontVoice)(nil)
)

var sfChannels = func() [sfSlots]int32 {
	var out [sfSlots]int32
	n := 0
	for ch := int32(0); ch < 16; ch++ {
		if ch == 9 {
			continue
		}
		out[n] = ch
		n++
	}
	return out
}()

func newSoundFontVoice(sf *meltysynth.SoundFont, sampleRate, program int) Voice {
	settings := meltysynth.NewSynthesizerSettings(int32(sampleRate))
	settings.EnableReverbAndChorus = false
	ms, err := meltysynth.NewSynthesizer(sf, settings)
	if err != nil {
		return NewPluck(sampleRate, program)
	}
	if program < 0 {
		program = 0
	}
	if program > 127 {
		program = 127
	}
	v := &soundFontVoice{
		ms:         ms,
		sampleRate: sampleRate,
		scratchL:   make([]float32, sfChunk),
		scratchR:   make([]float32, sfChunk),
	}
	for i := range v.slots {
		ch := sfChannels[i]
		v.slots[i] = sfSlot{ch: ch, key: -1, logical: -1}
		ms.ProcessMidiMessage(ch, 0xC0, int32(program), 0)

		ms.ProcessMidiMessage(ch, 0xB0, 101, 0)
		ms.ProcessMidiMessage(ch, 0xB0, 100, 0)
		ms.ProcessMidiMessage(ch, 0xB0, 6, sfBendRange)
		ms.ProcessMidiMessage(ch, 0xB0, 38, 0)
		v.slots[i].sent = sfBendCenter
	}
	return v
}

const sfBendCenter = 8192

func (v *soundFontVoice) NoteOn(key int, velocity float64) {
	v.NoteOnSpec(NoteSpec{Key: key, Velocity: velocity})
}

func (v *soundFontVoice) NoteOnSpec(spec NoteSpec) {
	v.NoteOnSpecReport(spec)
}

func (v *soundFontVoice) NoteOnSpecReport(spec NoteSpec) bool {
	if spec.Velocity <= 0 {
		return false
	}
	if s := v.continuation(spec); s != nil {

		semis := float64(int32(spec.Key) - s.key)
		if spec.Attack == AttackSlide {
			s.dst = semis
			s.step = (semis - s.bend) / (sfSlideSeconds * float64(v.sampleRate))
		} else {
			s.bend, s.dst, s.step = semis, semis, 0
		}
		s.logical = int32(spec.Key)
		s.age = v.counter
		v.counter++
		v.setSlotVibrato(s, spec.Vibrato)
		v.pushBend(s)
		return true
	}

	mv := int32(spec.Velocity*127 + 0.5)
	if mv < 1 {
		mv = 1
	}
	if mv > 127 {
		mv = 127
	}
	s := v.alloc()

	s.bend, s.dst, s.step = 0, 0, 0
	v.setSlotVibrato(s, spec.Vibrato)
	v.pushBend(s)
	s.key, s.logical = int32(spec.Key), int32(spec.Key)
	s.held = true
	s.age = v.counter
	v.counter++
	v.ms.NoteOn(s.ch, s.key, mv)
	return false
}

func (v *soundFontVoice) continuation(spec NoteSpec) *sfSlot {
	if spec.Attack == AttackPluck {
		return nil
	}
	for i := range v.slots {
		if s := &v.slots[i]; s.held && s.logical == int32(spec.From) {

			if d := int32(spec.Key) - s.key; d > sfBendRange || d < -sfBendRange {
				return nil
			}
			return s
		}
	}
	return nil
}

func (v *soundFontVoice) alloc() *sfSlot {
	var free, oldest *sfSlot
	for i := range v.slots {
		s := &v.slots[i]
		if !s.held {
			if free == nil || s.age < free.age {
				free = s
			}
			continue
		}
		if oldest == nil || s.age < oldest.age {
			oldest = s
		}
	}
	if free != nil {
		return free
	}
	v.ms.NoteOff(oldest.ch, oldest.key)
	oldest.held = false
	return oldest
}

func (v *soundFontVoice) NoteOff(key int) {
	for i := range v.slots {
		s := &v.slots[i]
		if !s.held || s.logical != int32(key) {
			continue
		}
		v.ms.NoteOff(s.ch, s.key)
		s.held = false
		s.age = v.counter
		v.counter++

	}
}

func (v *soundFontVoice) AllNotesOff() {
	v.ms.NoteOffAll(true)
	for i := range v.slots {
		s := &v.slots[i]
		s.held = false
		s.key, s.logical = -1, -1
		s.bend, s.dst, s.step = 0, 0, 0
		s.vib = false
		v.pushBend(s)
	}
}

func (v *soundFontVoice) setSlotVibrato(s *sfSlot, on bool) {
	s.vib = on
	if on {
		s.vibPhase = 0.25
		s.vibInc = sfVibratoHz / float64(v.sampleRate)
	}
}

func (v *soundFontVoice) pushBend(s *sfSlot) {
	semis := s.bend
	if s.vib {
		semis += sfVibratoCents / 100 * triangle(s.vibPhase)
	}
	val := int32(math.Round(sfBendCenter + semis/sfBendRange*sfBendCenter))
	if val < 0 {
		val = 0
	}
	if val > 16383 {
		val = 16383
	}
	if val == s.sent {
		return
	}
	s.sent = val
	v.ms.ProcessMidiMessage(s.ch, 0xE0, val&0x7F, val>>7)
}

func (v *soundFontVoice) advance(n int) {
	for i := range v.slots {
		s := &v.slots[i]
		if s.step != 0 {
			s.bend += s.step * float64(n)
			if (s.step > 0 && s.bend >= s.dst) || (s.step < 0 && s.bend <= s.dst) {
				s.bend, s.step = s.dst, 0
			}
		}
		if s.vib {
			s.vibPhase += s.vibInc * float64(n)
			s.vibPhase -= math.Floor(s.vibPhase)
		}
		if s.step != 0 || s.vib {
			v.pushBend(s)
		}
	}
}

func (v *soundFontVoice) modulating() bool {
	for i := range v.slots {
		if s := &v.slots[i]; s.step != 0 || s.vib {
			return true
		}
	}
	return false
}

func (v *soundFontVoice) Render(left, right []float32) {
	for len(left) > 0 {
		limit := sfChunk
		if v.modulating() {
			limit = sfGlideChunk
		}
		n := len(left)
		if n > limit {
			n = limit
		}
		v.ms.Render(v.scratchL[:n], v.scratchR[:n])
		for i := 0; i < n; i++ {
			left[i] += v.scratchL[i]
			right[i] += v.scratchR[i]
		}
		v.advance(n)
		left = left[n:]
		right = right[n:]
	}
}
