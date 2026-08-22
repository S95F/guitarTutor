package main

import (
	"os"
	"path/filepath"

	"github.com/S95F/musicTutor/internal/appconfig"
)

var demoPieces = map[string]string{
	"First steps on guitar.gtab": `\title First steps on guitar
\tempo 90
0.6.4 3.6 5.6 0.5 | 3.5 5.5 3.5.2 | 0.6.4 3.6 5.6 3.6 | 0.6.1 |
`,
	"First steps on soprano sax.gtab": `\title First steps on soprano sax
\tempo 90
\instrument soprano sax
D5.4 E5 F5 G5 | A5.2 G5.2 | F5.4 E5l D5.2 | D5.1 |
`,
}

func seedLibrary() {
	dir, err := appconfig.PiecesDir()
	if err != nil {
		return
	}
	if _, err := os.Stat(dir); err == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	for name, src := range demoPieces {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644)
	}
}
