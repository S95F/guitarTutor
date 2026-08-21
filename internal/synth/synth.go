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

// An Attack says how a note begins. It is the acoustic question — was the
// string picked, or was it already ringing? — deliberately stated without
// reference to the notation it came from: score.Technique is the engine's
// vocabulary, and translating it here is the engine's job.
type Attack int

const (
	// AttackPluck is an ordinary picked note: the string is excited from
	// whatever it was doing before, and the note starts afresh.
	AttackPluck Attack = iota
	// AttackLegato re-frets a string that is already ringing, without
	// picking it again: a hammer-on or a pull-off. The pitch changes at
	// once and the note keeps the energy the previous one left in the
	// string.
	AttackLegato
	// AttackSlide is AttackLegato with the pitch travelling between the
	// two notes instead of jumping — a finger moving along the string.
	AttackSlide
)

// A NoteSpec describes one note attack in more detail than NoteOn can: how
// it starts, what it continues from, and what happens to its pitch while it
// sounds. A zero Attack with no From is exactly NoteOn(Key, Velocity).
type NoteSpec struct {
	Key      int
	Velocity float64
	// Attack says how the note starts.
	Attack Attack
	// From is the key the note is hammered, pulled or slid from. It is
	// meaningless for AttackPluck, and an AttackLegato or AttackSlide
	// whose From is not currently sounding falls back to a pluck — a
	// score that slides into the first note of a piece has nothing to
	// slide from, and must still be heard.
	From int
	// Vibrato oscillates the pitch around Key for as long as the note
	// sounds. It is an oscillation about the written pitch, not a
	// departure from it: what is heard is still the note in the score.
	Vibrato bool
}

// An Articulator is a Voice that can sound a NoteSpec. Implementing it is
// optional: a Voice that does not is driven through NoteOn and attacks
// every note afresh, which is what every voice did before articulation
// existed. The engine asks once, per track, and takes whichever path the
// voice offers.
type Articulator interface {
	Voice
	// NoteOnSpec attacks a note as described. For the zero Attack it must
	// behave exactly as NoteOn(spec.Key, spec.Velocity) does.
	NoteOnSpec(spec NoteSpec)
}

// A ContinuationReporter is an Articulator that can REFUSE a continuation
// while the note it was asked to continue from is still sounding: told to
// slide or hammer across an interval its mechanism cannot reach (the
// SoundFont voice past its bend range), it attacks the note afresh instead,
// and NoteOnSpec alone cannot say which happened. The caller has to know —
// an engine that suppressed the From note's release on the assumption of a
// takeover would otherwise leave a refused origin sounding forever, with no
// scheduled NoteOff left to stop it. Implementing this is optional: the
// built-in voices only ever fall back to a fresh attack when nothing is
// sounding at From any more, so for them the assumption is already true.
type ContinuationReporter interface {
	Articulator
	// NoteOnSpecReport sounds spec exactly as NoteOnSpec does, and reports
	// whether a continuation Attack took over the voice of the note at
	// From. false means the note was attacked afresh (or not at all): the
	// From note was not taken over, and if it is still sounding it still
	// owes whoever scheduled it a release.
	NoteOnSpecReport(spec NoteSpec) (continued bool)
}
