package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func TestDemoPiecesParseAndValidate(t *testing.T) {
	var winds, guitars int
	for name, src := range demoPieces {
		sc, err := textfmt.Parse([]byte(src), name)
		if err != nil {
			t.Errorf("%s does not parse: %v", name, err)
			continue
		}
		if err := sc.Validate(); err != nil {
			t.Errorf("%s does not validate: %v", name, err)
		}
		if sc.Tracks[0].Wind != nil {
			winds++
		} else {
			guitars++
		}
	}
	if winds == 0 || guitars == 0 {
		t.Errorf("the demo library has %d wind and %d guitar pieces; want at least one of each", winds, guitars)
	}
}

func TestSeedLibraryRunsOnceAndOnlyOnce(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())
	seedLibrary()
	dir, err := appconfig.PiecesDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != len(demoPieces) {
		t.Fatalf("first run seeded %d pieces (err %v), want %d", len(entries), err, len(demoPieces))
	}
	for name := range demoPieces {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		break
	}
	seedLibrary()
	entries, _ = os.ReadDir(dir)
	if len(entries) != len(demoPieces)-1 {
		t.Errorf("a second run re-seeded a library the user had already curated (%d pieces)", len(entries))
	}
}

func TestRenderAuditionMakesSound(t *testing.T) {
	for _, program := range []int{25, 64} {
		buf := renderAudition(program, 72)
		if want := int((auditionHold+auditionTail)*sampleRate) * 8; len(buf) != want {
			t.Fatalf("program %d: buffer is %d bytes, want %d", program, len(buf), want)
		}
		loud := 0
		for i := 0; i+3 < len(buf); i += 4 {
			if buf[i] != 0 || buf[i+1] != 0 || buf[i+2] != 0 || buf[i+3] != 0 {
				loud++
			}
		}
		if loud < 1000 {
			t.Errorf("program %d: only %d non-zero samples; the audition is silent", program, loud)
		}
	}
}
