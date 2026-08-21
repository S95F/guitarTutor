package textfmt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/S95F/musicTutor/internal/score"
)

type ParseError struct {
	Name string
	Line int
	Col  int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.Name, e.Line, e.Col, e.Msg)
}

func ProblemLine(err error) string {
	var pe *ParseError
	if !errors.As(err, &pe) {
		return err.Error()
	}
	return fmt.Sprintf("line %d, column %d — %s", pe.Line, pe.Col, pe.Msg)
}

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
