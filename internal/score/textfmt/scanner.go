package textfmt

import (
	"strings"
	"unicode/utf8"
)

type pos struct {
	line, col int
}

type argument struct {
	text string
	pos  pos
}

type scanner struct {
	src string
	i   int
	ln  int
	cl  int
}

func newScanner(src string) *scanner {
	return &scanner{src: src, ln: 1, cl: 1}
}

func (s *scanner) eof() bool { return s.i >= len(s.src) }

func (s *scanner) pos() pos { return pos{line: s.ln, col: s.cl} }

func (s *scanner) peek() byte {
	if s.eof() {
		return 0
	}
	return s.src[s.i]
}

func (s *scanner) next() rune {
	r, size := utf8.DecodeRuneInString(s.src[s.i:])
	s.i += size
	if r == '\n' {
		s.ln++
		s.cl = 1
	} else {
		s.cl++
	}
	return r
}

func (s *scanner) atComment() bool {
	return s.peek() == '/' && s.i+1 < len(s.src) && s.src[s.i+1] == '/'
}

func (s *scanner) skipSpace() {
	for !s.eof() {
		switch c := s.peek(); {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			s.next()
		case s.atComment():
			for !s.eof() && s.peek() != '\n' {
				s.next()
			}
		default:
			return
		}
	}
}

func (s *scanner) skipInline() {
	for !s.eof() {
		if c := s.peek(); c == ' ' || c == '\t' {
			s.next()
			continue
		}
		return
	}
}

func (s *scanner) word() (string, pos) {
	p := s.pos()
	start := s.i
	for !s.eof() {
		c := s.peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '|' || c == '(' || c == ')' || s.atComment() {
			break
		}
		s.next()
	}
	return s.src[start:s.i], p
}

func (s *scanner) args() []argument {
	var out []argument
	for {
		s.skipInline()
		if s.eof() || s.peek() == '\n' || s.peek() == '\r' || s.atComment() {
			return out
		}
		p := s.pos()
		start := s.i
		for !s.eof() {
			c := s.peek()
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' || s.atComment() {
				break
			}
			s.next()
		}
		out = append(out, argument{text: s.src[start:s.i], pos: p})
	}
}

func (s *scanner) restOfLine() string {
	start := s.i
	for !s.eof() && s.peek() != '\n' && s.peek() != '\r' && !s.atComment() {
		s.next()
	}
	return strings.TrimSpace(s.src[start:s.i])
}
