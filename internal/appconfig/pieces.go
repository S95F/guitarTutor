package appconfig

import (
	"os"
	"path/filepath"
	"strings"
)

const PiecesDirName = "pieces"

const MaxCreated = 12

func PiecesDir() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), PiecesDirName), nil
}

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
