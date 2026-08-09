package appconfig

import (
	"path/filepath"
	"runtime"
	"strings"
)

// MaxRecents is the most recently-opened pieces a Config keeps. Twelve is
// long enough to cover a rotation of pieces someone is actually working
// on, and short enough that the start screen's recents list stays a
// shortcut rather than turning into a second file browser. AddRecent
// drops the oldest entry past this many.
const MaxRecents = 12

// normalizeRecent puts a piece path into the canonical form recents are
// stored and compared in: cleaned — so "dir/../dir/song.gp" and, on
// Windows, "dir/song.gp" and `dir\song.gp` collapse to one spelling — and
// absolute where the working directory can be resolved, so opening a
// piece by relative path and later by full path is one entry rather than
// two. A path that cannot be made absolute (Getwd failing on a deleted
// working directory) is kept merely cleaned: a slightly weaker key is
// better than losing the entry. The empty path normalizes to empty and is
// never recorded.
func normalizeRecent(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	// filepath.Abs cleans as part of its job, so this covers both.
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// sameRecentPath reports whether two normalized recents name the same
// file. Windows file systems are case-insensitive, so `C:\Songs\A.gp` and
// `c:\songs\a.gp` are one piece there and must not both appear in the
// list. Elsewhere paths are compared exactly: Linux is case-sensitive,
// and macOS case-insensitivity is a per-volume choice, so folding there
// would risk merging two genuinely different files.
func sameRecentPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// AddRecent records a piece as the most recently opened. An entry already
// in the list moves to the front instead of being duplicated (matched on
// the normalized path, case-insensitively on Windows), and the list is
// trimmed to MaxRecents from the back. Blank paths are ignored, so a
// caller need not guard the empty case.
func (c *Config) AddRecent(path string) {
	p := normalizeRecent(path)
	if p == "" {
		return
	}
	out := make([]string, 0, len(c.Recents)+1)
	out = append(out, p)
	for _, r := range c.Recents {
		if r == "" || sameRecentPath(r, p) {
			continue
		}
		out = append(out, r)
	}
	if len(out) > MaxRecents {
		out = out[:MaxRecents]
	}
	c.Recents = out
}

// ForgetRecent removes a piece from the recents list, matching the same
// way AddRecent de-duplicates. It is how a screen drops an entry whose
// file has been moved or deleted. Removing a path that is not listed does
// nothing.
func (c *Config) ForgetRecent(path string) {
	p := normalizeRecent(path)
	if p == "" || len(c.Recents) == 0 {
		return
	}
	out := make([]string, 0, len(c.Recents))
	for _, r := range c.Recents {
		if r == "" || sameRecentPath(r, p) {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		// A drained list is nil, not an empty slice, so it stays out of
		// the JSON (omitempty) and matches the zero Config.
		c.Recents = nil
		return
	}
	c.Recents = out
}

// sanitizeRecents repairs a recents list read off disk: it drops blank
// entries, collapses duplicates, and enforces MaxRecents (a file written
// by a build with a larger cap, or edited by hand, must not grow the list
// forever). It deliberately does not re-normalize the entries it keeps —
// rewriting spellings on every load would churn files written by a build
// whose normalization differs. Returns nil for an empty result so the
// field round-trips as absent.
func sanitizeRecents(in []string) []string {
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
		if len(out) == MaxRecents {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
