package synth

import (
	"bufio"
	"fmt"
	"os"

	"github.com/sinshu/go-meltysynth/meltysynth"
)

// sfChunk is the scratch size a soundFontVoice renders through. meltysynth
// overwrites its output buffers, but the Voice contract is additive, so the
// voice renders into preallocated scratch this many frames at a time and
// adds the result into the mix.
const sfChunk = 1024

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

// A soundFontVoice adapts one meltysynth Synthesizer to the Voice
// interface. Each voice owns its own synthesizer (cheap next to the shared
// SoundFont sample data), plays on MIDI channel 0, and has the track's
// General MIDI program selected at construction.
type soundFontVoice struct {
	ms       *meltysynth.Synthesizer
	scratchL []float32
	scratchR []float32
}

var _ Voice = (*soundFontVoice)(nil)

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
	ms.ProcessMidiMessage(0, 0xC0, int32(program), 0) // program change
	return &soundFontVoice{
		ms:       ms,
		scratchL: make([]float32, sfChunk),
		scratchR: make([]float32, sfChunk),
	}
}

// NoteOn attacks key, mapping velocity from [0,1] to MIDI 1-127.
// Non-positive velocities are ignored.
func (v *soundFontVoice) NoteOn(key int, velocity float64) {
	if velocity <= 0 {
		return
	}
	mv := int32(velocity*127 + 0.5)
	if mv < 1 {
		mv = 1
	}
	if mv > 127 {
		mv = 127
	}
	v.ms.NoteOn(0, int32(key), mv)
}

// NoteOff releases key into the preset's release envelope.
func (v *soundFontVoice) NoteOff(key int) {
	v.ms.NoteOff(0, int32(key))
}

// AllNotesOff kills every sounding note immediately.
func (v *soundFontVoice) AllNotesOff() {
	v.ms.NoteOffAll(true)
}

// Render adds the synthesizer's output into left and right. meltysynth's
// Render overwrites its arguments, so the voice renders through scratch in
// sfChunk-frame slices and accumulates by hand.
func (v *soundFontVoice) Render(left, right []float32) {
	for len(left) > 0 {
		n := len(left)
		if n > sfChunk {
			n = sfChunk
		}
		v.ms.Render(v.scratchL[:n], v.scratchR[:n])
		for i := 0; i < n; i++ {
			left[i] += v.scratchL[i]
			right[i] += v.scratchR[i]
		}
		left = left[n:]
		right = right[n:]
	}
}
