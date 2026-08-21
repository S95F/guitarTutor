package textfmt

import (
	"strings"
	"unicode/utf8"
)

// A pos is a 1-based line and column in .gtab source. Columns count runes.
type pos struct {
	line, col int
}

// An argument is one whitespace-separated directive argument and its
// source position.
type argument struct {
	text string
	pos  pos
}

// A scanner walks .gtab source, tracking line and column. Every
// structural character is ASCII, so it dispatches on bytes and advances
// runes.
type scanner struct {
	src string
	i   int // byte offset of the next unread character
	ln  int // current line, 1-based
	cl  int // current column, 1-based, counting runes
}

// newScanner returns a scanner positioned at the start of src.
func newScanner(src string) *scanner {
	return &scanner{src: src, ln: 1, cl: 1}
}

// eof reports whether the input is exhausted.
func (s *scanner) eof() bool { return s.i >= len(s.src) }

// pos returns the current position.
func (s *scanner) pos() pos { return pos{line: s.ln, col: s.cl} }

// peek returns the next byte without consuming it, or 0 at end of input.
func (s *scanner) peek() byte {
	if s.eof() {
		return 0
	}
	return s.src[s.i]
}

// peekAt returns the byte n past the cursor, or 0 past end of input.
func (s *scanner) peekAt(n int) byte {
	if s.i+n >= len(s.src) {
		return 0
	}
	return s.src[s.i+n]
}

// next consumes and returns one rune, updating the line and column.
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

// atComment reports whether the cursor is at a "//" comment.
func (s *scanner) atComment() bool {
	return s.peek() == '/' && s.peekAt(1) == '/'
}

// skipSpace advances past whitespace (including newlines) and comments.
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

// skipInline advances past spaces and tabs without crossing a line end.
// A bare "\r" counts as a line end here, not inline space: classic Mac
// files terminate lines with "\r" alone, and consuming it inline would
// run a directive's argument list into the next line.
func (s *scanner) skipInline() {
	for !s.eof() {
		if c := s.peek(); c == ' ' || c == '\t' {
			s.next()
			continue
		}
		return
	}
}

// word consumes and returns a token: a maximal run of characters up to
// whitespace, a bar line, a parenthesis, or a comment. The returned
// position is the token's first character (the current position when the
// token is empty).
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

// args consumes the remaining whitespace-separated arguments on the
// current line, stopping at a line end ("\n" or a bare "\r"), a comment,
// or end of input.
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

// restOfLine consumes to the end of the current line (or the start of a
// comment) and returns the text with surrounding whitespace trimmed. A
// bare "\r" ends the line like "\n" does: it did in every other position
// (word and args break on it), and letting one through here put a
// carriage return INSIDE a title or track name — text Format refuses,
// so the piece parsed and could never be saved.
func (s *scanner) restOfLine() string {
	start := s.i
	for !s.eof() && s.peek() != '\n' && s.peek() != '\r' && !s.atComment() {
		s.next()
	}
	return strings.TrimSpace(s.src[start:s.i])
}
