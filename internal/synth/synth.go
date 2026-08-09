// Package synth defines the voice interface the engine renders through,
// plus its implementations: a built-in Karplus-Strong plucked-string voice
// (no assets required) and a SoundFont voice backed by go-meltysynth (used
// when the user supplies an .sf2 file).
package synth

// A Voice renders one track's notes into audio. The engine calls it from a
// single render goroutine; calls are never concurrent, and Render is on the
// realtime path: implementations must not allocate or lock in steady state.
type Voice interface {
	// NoteOn attacks a MIDI key at velocity in [0,1].
	NoteOn(key int, velocity float64)
	// NoteOff releases a key (the note may keep ringing through its
	// natural decay/release).
	NoteOff(key int)
	// AllNotesOff silences everything immediately (used at seeks and
	// loop boundaries).
	AllNotesOff()
	// Render mixes (adds) len(left) frames into left and right.
	// len(left) == len(right) always. The engine owns zeroing.
	Render(left, right []float32)
}

// A Factory creates a Voice for one track. program is the track's General
// MIDI program hint (0-127); implementations may ignore it.
type Factory func(sampleRate int, program int) Voice
