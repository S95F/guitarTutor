// Package textfmt parses the musicTutor text tab format (.gtab), the
// small alphaTex-inspired authoring language specified in
// docs/TEXTFORMAT.md.
//
// Parsing produces the internal/score model directly: PPQ 960 ticks,
// tempo and meter maps from the directives, and Note{String, Fret, Tied,
// Tech} per beat. Everything Parse accepts passes score.Validate;
// everything it rejects is reported as a *ParseError carrying a line and
// column. There is no layout or display information in the format —
// rendering decides that.
package textfmt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/S95F/musicTutor/internal/score"
)

// A ParseError reports a syntax or semantic error at a position in .gtab
// source. Line and Col are 1-based; Col counts runes.
type ParseError struct {
	Name string // source name, as given to Parse
	Line int
	Col  int
	Msg  string
}

// Error formats the error as "name:line:col: message".
func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.Name, e.Line, e.Col, e.Msg)
}

// ProblemLine renders a parse failure as a line somebody can act on where
// the source is already on screen: the compiler-shaped "name:line:col:"
// prefix becomes words, and the name is dropped — it is the row above, or
// the pane itself. Every screen that shows a parse error renders it
// through this one function, so the library row and the editor pane
// cannot describe one problem two ways. A non-ParseError passes through
// unchanged.
func ProblemLine(err error) string {
	var pe *ParseError
	if !errors.As(err, &pe) {
		return err.Error()
	}
	return fmt.Sprintf("line %d, column %d — %s", pe.Line, pe.Col, pe.Msg)
}

// Parse parses .gtab source into a score. name identifies the source in
// error messages and seeds the default title, used when the source has no
// \title directive.
func Parse(src []byte, name string) (*score.Score, error) {
	p := &parser{
		sc:   newScanner(string(src)),
		name: name,
		score: &score.Score{
			Title:  name,
			Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(DefaultBPM)}},
			Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
		},
		sticky: score.Quarter,
	}
	if err := p.run(); err != nil {
		return nil, err
	}
	return p.score, nil
}

// ParseFile reads and parses the .gtab file at path. The file's base name
// without its extension seeds the default title; errors carry the full
// path.
func ParseFile(path string) (*score.Score, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(path)
	s, err := Parse(src, strings.TrimSuffix(base, filepath.Ext(base)))
	if err != nil {
		var pe *ParseError
		if errors.As(err, &pe) {
			pe.Name = path
		}
		return nil, err
	}
	return s, nil
}
