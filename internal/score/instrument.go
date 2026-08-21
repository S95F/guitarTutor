package score

import "strings"

type WindInstrument struct {
	Name string

	LowSounding int

	Span int

	Transpose int

	Program int
}

func (w *WindInstrument) Written(sounding int) int { return sounding + w.Transpose }

func (w *WindInstrument) Sounding(written int) int { return written - w.Transpose }

var WindInstruments = []WindInstrument{
	{Name: "soprano sax", LowSounding: 56, Span: 32, Transpose: 2, Program: 64},
	{Name: "alto sax", LowSounding: 49, Span: 32, Transpose: 9, Program: 65},
	{Name: "tenor sax", LowSounding: 44, Span: 32, Transpose: 14, Program: 66},
	{Name: "baritone sax", LowSounding: 37, Span: 32, Transpose: 21, Program: 67},
	{Name: "flute", LowSounding: 60, Span: 36, Transpose: 0, Program: 73},
	{Name: "clarinet", LowSounding: 50, Span: 44, Transpose: 2, Program: 71},
	{Name: "trumpet", LowSounding: 52, Span: 30, Transpose: 2, Program: 56},
}

func WindByName(name string) *WindInstrument {
	name = strings.TrimSpace(name)
	for i := range WindInstruments {
		if strings.EqualFold(WindInstruments[i].Name, name) {
			return &WindInstruments[i]
		}
	}
	return nil
}

func (w *WindInstrument) NoteFor(key int) Note {
	return Note{String: 1, Fret: key - w.LowSounding}
}

func WindByProgram(program int) *WindInstrument {
	for i := range WindInstruments {
		if WindInstruments[i].Program == program {
			return &WindInstruments[i]
		}
	}
	return nil
}

func WindNames() []string {
	names := make([]string, len(WindInstruments))
	for i := range WindInstruments {
		names[i] = WindInstruments[i].Name
	}
	return names
}
