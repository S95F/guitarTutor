package synth

type Voice interface {
	NoteOn(key int, velocity float64)

	NoteOff(key int)

	AllNotesOff()

	Render(left, right []float32)
}

type Factory func(sampleRate int, program int) Voice

type Attack int

const (
	AttackPluck Attack = iota

	AttackLegato

	AttackSlide
)

type NoteSpec struct {
	Key      int
	Velocity float64

	Attack Attack

	From int

	Vibrato bool
}

type Articulator interface {
	Voice

	NoteOnSpec(spec NoteSpec)
}

type ContinuationReporter interface {
	Articulator

	NoteOnSpecReport(spec NoteSpec) (continued bool)
}
