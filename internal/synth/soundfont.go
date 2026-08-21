package synth

import (
	"bufio"
	"fmt"
	"math"
	"os"

	"github.com/sinshu/go-meltysynth/meltysynth"
)

// sfChunk is the scratch size a soundFontVoice renders through. meltysynth
// overwrites its output buffers, but the Voice contract is additive, so the
// voice renders into preallocated scratch this many frames at a time and
// adds the result into the mix.
const sfChunk = 1024

// Articulation constants. A SoundFont voice cannot re-excite a sounding
// note the way a modelled string can, so a slide, a hammer-on and a pull-off
// are all the same thing here: the note that is already ringing is bent to
// the new pitch and never re-struck.
const (
	// sfSlots is how many notes can be independently pitched at once: one
	// MIDI channel each, over the sixteen less channel 9, which every
	// SoundFont synthesizer reserves for percussion. Pitch bend is a
	// per-channel control, so a track that played everything on channel 0
	// could not bend one note of a chord without bending the chord.
	sfSlots = 15
	// sfBendRange is the pitch-bend range set on every melodic channel, in
	// semitones. An octave covers any slide worth notating and still
	// leaves the 14-bit bend a resolution of about a sixth of a cent.
	sfBendRange = 12
	// sfGlideChunk is the render granularity while any pitch is moving. A
	// bend is a channel control, so it can only change between rendered
	// blocks: at sfChunk a 55 ms slide would be two steps and audibly a
	// staircase, and at this size it is forty smooth ones.
	sfGlideChunk = 64
	// sfSlideSeconds matches the modelled voice's slide time — the
	// gesture is the same one, and the two voices should not disagree
	// about how long a finger takes to travel.
	sfSlideSeconds = pluckSlideSeconds
	// sfVibratoHz and sfVibratoCents likewise mirror the modelled voice.
	sfVibratoHz    = pluckVibratoHz
	sfVibratoCents = pluckVibratoCents
)

// A SoundFont is a parsed .sf2 file. It is immutable after loading, so one
// SoundFont can back the factories and voices of every track.
type SoundFont struct {
	sf *meltysynth.SoundFont
}

// LoadSoundFont reads and parses the SF2 file at path.
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

// NewSoundFontFactory returns a Factory whose voices render through sf. A
// nil or unloaded sf returns a nil Factory so callers can chain the
// built-in fallback:
//
//	factory := synth.NewSoundFontFactory(sf)
//	if factory == nil {
//		factory = synth.NewPluck
//	}
func NewSoundFontFactory(sf *SoundFont) Factory {
	if sf == nil || sf.sf == nil {
		return nil
	}
	return func(sampleRate, program int) Voice {
		return newSoundFontVoice(sf.sf, sampleRate, program)
	}
}

// An sfSlot is one MIDI channel and the note it is carrying. Notes get a
// channel each so that pitch bend, which is per channel, can move one of
// them without moving the rest.
type sfSlot struct {
	ch int32 // the MIDI channel this slot drives

	// key is the MIDI key actually sounded on the channel — for a slid or
	// hammered note, the key it started FROM, since that voice is still
	// the one ringing. logical is the key the engine names, which is where
	// the bend has taken it. Keeping them apart is what lets NoteOff find
	// a note that has moved since it was struck.
	key, logical int32
	held         bool
	age          uint64 // last note-on or note-off, for slot reuse

	bend float64 // semitones currently applied to the channel
	sent int32   // the 14-bit value last sent, so bends are not resent
	dst  float64 // glide destination, in semitones
	step float64 // semitones per frame while gliding; 0 = holding

	vib      bool
	vibPhase float64 // cycles, [0, 1)
	vibInc   float64
}

// A soundFontVoice adapts one meltysynth Synthesizer to the Voice
// interface. Each voice owns its own synthesizer (cheap next to the shared
// SoundFont sample data) with the track's General MIDI program selected on
// every melodic channel at construction.
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

// sfChannels is the melodic MIDI channel of each slot: every channel but 9,
// which is percussion.
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

// newSoundFontVoice builds a synthesizer for one track. Reverb and chorus
// are disabled: they would ring past AllNotesOff, which the engine relies
// on for clean seeks and loop boundaries. If meltysynth rejects the
// settings (it supports sample rates 16000-192000), the built-in pluck is
// returned instead so the track still sounds.
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
		ms.ProcessMidiMessage(ch, 0xC0, int32(program), 0) // program change
		// Widen the pitch-bend range through RPN 0: select the registered
		// parameter, then set it. Left at the default two semitones, any
		// slide past a whole tone would arrive at the wrong note.
		ms.ProcessMidiMessage(ch, 0xB0, 101, 0) // RPN MSB: 0
		ms.ProcessMidiMessage(ch, 0xB0, 100, 0) // RPN LSB: 0 = bend range
		ms.ProcessMidiMessage(ch, 0xB0, 6, sfBendRange)
		ms.ProcessMidiMessage(ch, 0xB0, 38, 0)
		v.slots[i].sent = sfBendCenter
	}
	return v
}

// sfBendCenter is the 14-bit pitch-bend value meaning no bend.
const sfBendCenter = 8192

// NoteOn attacks key with an ordinary attack, mapping velocity from [0,1]
// to MIDI 1-127. Non-positive velocities are ignored.
func (v *soundFontVoice) NoteOn(key int, velocity float64) {
	v.NoteOnSpec(NoteSpec{Key: key, Velocity: velocity})
}

// NoteOnSpec attacks a note as described. A slid or hammered note takes
// over the still-ringing voice of the note it comes from and bends it to
// the new pitch — instantly for a hammer-on or pull-off, over the slide
// time for a slide — so the sample is never re-struck. Everything else
// takes a fresh channel and a fresh attack.
func (v *soundFontVoice) NoteOnSpec(spec NoteSpec) {
	v.NoteOnSpecReport(spec)
}

// NoteOnSpecReport is NoteOnSpec with the ContinuationReporter answer: true
// only when the note took over its From note's channel. A refused takeover
// (continuation's bend-range fallback) and a plain attack both report
// false, telling the engine the From note, if any, was not released here.
func (v *soundFontVoice) NoteOnSpecReport(spec NoteSpec) bool {
	if spec.Velocity <= 0 {
		return false
	}
	if s := v.continuation(spec); s != nil {
		// The bend rides on the key the channel was STRUCK at, which on a
		// chained slur or legato run (every note continued from the one
		// before) is not the note it comes From: measuring the offset from
		// spec.From instead left the second link of every chain sounding
		// spec.From's offset short of its written pitch.
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
	// A reused channel may still be carrying the bend of the note that had
	// it; a fresh attack starts in tune.
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

// continuation returns the slot a slid or hammered note should take over,
// or nil when there is nothing to take over and the note must be attacked
// afresh: no Attack asking for it, no sounding note at From, or an interval
// wider than the channel's bend range can reach. Falling back to a fresh
// attack is always better than sounding the wrong pitch.
func (v *soundFontVoice) continuation(spec NoteSpec) *sfSlot {
	if spec.Attack == AttackPluck {
		return nil
	}
	for i := range v.slots {
		if s := &v.slots[i]; s.held && s.logical == int32(spec.From) {
			// The reach that matters is from the key the channel was
			// STRUCK at — the pitch the bend is measured from — not from
			// the note continued From: a chain of in-reach links can
			// still drift a slot beyond what its bend can express.
			if d := int32(spec.Key) - s.key; d > sfBendRange || d < -sfBendRange {
				return nil
			}
			return s
		}
	}
	return nil
}

// alloc returns the slot for a new note: the one released longest ago, or —
// when every channel is still holding a note — the oldest of those, whose
// note is stopped to make room.
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

// NoteOff releases key into the preset's release envelope. The key looked
// for is the one the engine named, which for a note that has been slid or
// hammered is not the key the channel is actually sounding — see sfSlot.
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
		// The bend is deliberately left in place: the note is still
		// ringing out its release, and returning the channel to centre
		// would slide the tail back to a pitch the player never heard.
		// Reusing the channel resets it.
	}
}

// AllNotesOff kills every sounding note immediately and returns every
// channel to concert pitch, so the next note on a channel that was carrying
// a slide does not inherit it.
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

// setSlotVibrato arms or disarms a slot's pitch oscillation, starting it at
// a zero crossing so an armed note begins at its written pitch.
func (v *soundFontVoice) setSlotVibrato(s *sfSlot, on bool) {
	s.vib = on
	if on {
		s.vibPhase = 0.25
		s.vibInc = sfVibratoHz / float64(v.sampleRate)
	}
}

// pushBend sends a slot's current pitch offset to its channel, if it has
// changed since the last one.
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

// advance moves every slot's glide and vibrato on by n frames and pushes
// the resulting bends. Called between rendered blocks, which is the only
// granularity a channel control has.
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

// modulating reports whether any slot's pitch is on the move, which is what
// decides how finely Render has to slice its blocks.
func (v *soundFontVoice) modulating() bool {
	for i := range v.slots {
		if s := &v.slots[i]; s.step != 0 || s.vib {
			return true
		}
	}
	return false
}

// Render adds the synthesizer's output into left and right. meltysynth's
// Render overwrites its arguments, so the voice renders through scratch in
// slices and accumulates by hand. The slices shrink while a pitch is
// moving, because pitch bend can only change between them.
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
