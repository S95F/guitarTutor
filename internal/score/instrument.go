package score

import "strings"

// A WindInstrument describes a monophonic wind instrument the app knows by
// name. A wind track keeps the {String, Fret} note shape with String always
// 1 and Fret counting semitones up from LowSounding — one chromatic lane
// instead of six strings — so pitch stays derived, never stored, and
// everything downstream of Track.Pitch (events, the engine, the scorer) is
// none the wiser.
//
// The registry speaks two pitch languages at once, deliberately. Sounding
// (concert) pitch is what the model, the synth, and the microphone agree
// on. Written pitch is what the player reads: most winds transpose (a
// B-flat soprano sax reads a major second above what comes out), and a
// practice tool that showed a sax player concert pitch would be teaching
// them to misread every chart ever printed for their instrument. The
// conversion lives here and is applied only at the edges — the text format
// and the screen — never inside the model.
type WindInstrument struct {
	// Name is how the \instrument directive, the library, and the UI say
	// the instrument.
	Name string
	// LowSounding is the sounding (concert) MIDI key of the instrument's
	// lowest note: the pitch a note with Fret 0 makes.
	LowSounding int
	// Span is the size of the standard range in semitones: ordinary
	// fingerings run from Fret 0 to Fret Span. Notes above it are
	// altissimo — real, hard-won, and drawn as such — so Span caps what
	// the editor offers, not what a piece may hold.
	Span int
	// Transpose is the written transposition: the written key the player
	// reads is the sounding key plus Transpose.
	Transpose int
	// Program is the General MIDI program the instrument defaults to.
	Program int
}

// Written returns the written key a player reads for a sounding key.
func (w *WindInstrument) Written(sounding int) int { return sounding + w.Transpose }

// Sounding returns the sounding key of a written one.
func (w *WindInstrument) Sounding(written int) int { return written - w.Transpose }

// WindInstruments are the wind instruments the app calls by name. Ranges
// are the standard (pre-altissimo) ranges from published fingering charts;
// the whole saxophone family reads the same written range, B♭3 to F♯6,
// which is why the four Spans agree and only the transpositions differ.
var WindInstruments = []WindInstrument{
	{Name: "soprano sax", LowSounding: 56, Span: 32, Transpose: 2, Program: 64},
	{Name: "alto sax", LowSounding: 49, Span: 32, Transpose: 9, Program: 65},
	{Name: "tenor sax", LowSounding: 44, Span: 32, Transpose: 14, Program: 66},
	{Name: "baritone sax", LowSounding: 37, Span: 32, Transpose: 21, Program: 67},
	{Name: "flute", LowSounding: 60, Span: 36, Transpose: 0, Program: 73},
	{Name: "clarinet", LowSounding: 50, Span: 44, Transpose: 2, Program: 71},
	{Name: "trumpet", LowSounding: 52, Span: 30, Transpose: 2, Program: 56},
}

// WindByName finds a wind instrument by name, case-insensitively, or nil.
// The returned pointer is into WindInstruments and must not be written to.
func WindByName(name string) *WindInstrument {
	name = strings.TrimSpace(name)
	for i := range WindInstruments {
		if strings.EqualFold(WindInstruments[i].Name, name) {
			return &WindInstruments[i]
		}
	}
	return nil
}

// WindByProgram finds the wind instrument a General MIDI program names, or
// nil when the program is nobody's. Importers use it to recognise a wind
// part in a file that carries programs but no instrument names.
func WindByProgram(program int) *WindInstrument {
	for i := range WindInstruments {
		if WindInstruments[i].Program == program {
			return &WindInstruments[i]
		}
	}
	return nil
}

// WindNames lists every wind instrument name, registry order, for error
// messages and pickers.
func WindNames() []string {
	names := make([]string, len(WindInstruments))
	for i := range WindInstruments {
		names[i] = WindInstruments[i].Name
	}
	return names
}
