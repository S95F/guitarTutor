package appconfig

// The pieces the application manages itself, as opposed to the ones it
// merely opened once from wherever they happened to live.
//
// Those are two different things and the start screen shows them as two
// different things. A recent is a path: you opened a .gp file out of a
// download folder, and the entry is a shortcut back to it. A piece in the
// library is a piece the application OWNS — written in its editor, saved
// in its own folder, in the one format it can write — and it stays there
// whether or not it was opened lately.

import (
	"os"
	"path/filepath"
	"strings"
)

// PiecesDirName is the folder, under the config directory, that the
// application keeps its own pieces in. It sits beside config.json rather
// than in Documents because it is the app's storage, not the user's
// filing: the editor's save dialog opens here, and anyone who would
// rather keep a piece somewhere else can simply save it somewhere else.
const PiecesDirName = "pieces"

// MaxCreated is how many recently-written pieces a Config remembers. The
// library pane lists everything in the pieces folder, so this list is
// only the "what was I just working on" shortcut and does not need to be
// long.
const MaxCreated = 12

// PiecesDir returns the directory the application keeps its own pieces
// in. It does not create it — see EnsurePiecesDir.
func PiecesDir() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), PiecesDirName), nil
}

// EnsurePiecesDir returns the pieces directory, creating it if it is not
// there. A save dialog that opens on a folder which does not exist lands
// somewhere arbitrary instead, which is exactly the confusion the managed
// folder exists to avoid.
func EnsurePiecesDir() (string, error) {
	dir, err := PiecesDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// InPiecesDir reports whether a path is a piece the application manages —
// that is, a file sitting directly in the pieces directory. It is how a
// screen tells "I wrote this" from "I opened this once", and it compares
// the same way recents do, so Windows spelling and case do not split one
// piece into two.
func InPiecesDir(path string) bool {
	dir, err := PiecesDir()
	if err != nil {
		return false
	}
	p := normalizeRecent(path)
	if p == "" {
		return false
	}
	return sameRecentPath(filepath.Dir(p), normalizeRecent(dir))
}

// AddCreated records a piece as the most recently written, exactly as
// AddRecent records one as the most recently opened: an entry already in
// the list moves to the front rather than being duplicated, and the list
// is trimmed from the back.
func (c *Config) AddCreated(path string) {
	p := normalizeRecent(path)
	if p == "" {
		return
	}
	out := make([]string, 0, len(c.Created)+1)
	out = append(out, p)
	for _, r := range c.Created {
		if r == "" || sameRecentPath(r, p) {
			continue
		}
		out = append(out, r)
	}
	if len(out) > MaxCreated {
		out = out[:MaxCreated]
	}
	c.Created = out
}

// ForgetCreated removes a piece from the written list. The file itself is
// untouched: forgetting is about the shortcut, and the library pane still
// lists anything that is actually in the folder.
func (c *Config) ForgetCreated(path string) {
	p := normalizeRecent(path)
	if p == "" || len(c.Created) == 0 {
		return
	}
	out := make([]string, 0, len(c.Created))
	for _, r := range c.Created {
		if r == "" || sameRecentPath(r, p) {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		c.Created = nil
		return
	}
	c.Created = out
}

// sanitizeCreated repairs a written-pieces list read off disk, the same
// way sanitizeRecents does for the opened one.
func sanitizeCreated(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, r := range in {
		if strings.TrimSpace(r) == "" {
			continue
		}
		dup := false
		for _, kept := range out {
			if sameRecentPath(kept, r) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		out = append(out, r)
		if len(out) == MaxCreated {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
