package synth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSoundFontErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
		return path
	}

	tests := []struct {
		name string
		path string
	}{
		{"missing file", filepath.Join(dir, "missing.sf2")},
		{"empty file", write("empty.sf2", nil)},
		{"garbage bytes", write("garbage.sf2", []byte("this is not a soundfont"))},
		{"truncated riff", write("truncated.sf2", []byte("RIFF\x08\x00\x00\x00sfbk"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf, err := LoadSoundFont(tt.path)
			if err == nil {
				t.Fatalf("LoadSoundFont(%q) succeeded, want error", tt.path)
			}
			if sf != nil {
				t.Errorf("LoadSoundFont(%q) = %v with error, want nil", tt.path, sf)
			}
		})
	}
}

func TestNewSoundFontFactoryNilSafety(t *testing.T) {
	if f := NewSoundFontFactory(nil); f != nil {
		t.Error("NewSoundFontFactory(nil) != nil, want nil so callers can fall back to NewPluck")
	}
	if f := NewSoundFontFactory(&SoundFont{}); f != nil {
		t.Error("NewSoundFontFactory(unloaded SoundFont) != nil, want nil")
	}
}

// TestSoundFontVoice exercises the real synthesis path. It needs an actual
// SoundFont, which the repository does not ship and tests never download:
// drop any General MIDI .sf2 at testdata/test.sf2 to run it.
func TestSoundFontVoice(t *testing.T) {
	const path = "testdata/test.sf2"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no %s (drop any GM SoundFont there to run this test)", path)
	}
	sf, err := LoadSoundFont(path)
	if err != nil {
		t.Fatalf("LoadSoundFont(%q): %v", path, err)
	}
	factory := NewSoundFontFactory(sf)
	if factory == nil {
		t.Fatal("NewSoundFontFactory returned nil for a loaded SoundFont")
	}
	v := factory(48000, 25)

	// Additive contract: with nothing sounding, Render must leave a
	// prefilled mix untouched (meltysynth itself overwrites its buffers;
	// the wrapper must not).
	left := make([]float32, 512)
	right := make([]float32, 512)
	for i := range left {
		left[i], right[i] = 0.25, -0.25
	}
	v.Render(left, right)
	for i := range left {
		if left[i] != 0.25 || right[i] != -0.25 {
			t.Fatalf("silent Render disturbed the mix at %d: %g, %g", i, left[i], right[i])
		}
	}

	// A note produces signal.
	v.NoteOn(40, 0.8)
	l2, r2 := renderFrames(v, 4800, 480)
	if peak(l2) == 0 && peak(r2) == 0 {
		t.Fatal("no signal after NoteOn")
	}

	// AllNotesOff kills voices immediately; allow the synthesizer's
	// current 64-frame block to drain, then require near-silence.
	v.AllNotesOff()
	l3, r3 := renderFrames(v, 4800, 480)
	const floor = 0.001 // -60 dBFS
	if p := peak(l3[128:]); p > floor {
		t.Errorf("left peak %g after AllNotesOff, want <= %g", p, floor)
	}
	if p := peak(r3[128:]); p > floor {
		t.Errorf("right peak %g after AllNotesOff, want <= %g", p, floor)
	}
}
