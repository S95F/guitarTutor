package appconfig

import (
	"path/filepath"
	"runtime"
	"strings"
)

const MaxRecents = 12

func normalizeRecent(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}

	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

func sameRecentPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

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

		c.Recents = nil
		return
	}
	c.Recents = out
}

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
