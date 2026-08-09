package appconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPathHonorsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	p, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join(dir, "config.json"); p != want {
		t.Errorf("Path = %q, want %q", p, want)
	}
}

func TestPathDefaultUsesUserConfigDir(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	p, err := Path()
	if err != nil {
		t.Skipf("os.UserConfigDir unavailable: %v", err)
	}
	if want := filepath.Join("guitartutor", "config.json"); !strings.HasSuffix(p, want) {
		t.Errorf("Path = %q, want suffix %q", p, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v, want nil", err)
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Errorf("Load = %+v, want zero Config", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())
	c := Config{
		CaptureDeviceID:  "wasapi-{cap-guid}",
		PlaybackDeviceID: "wasapi-{play-guid}",
		SoundFontPath:    `C:\soundfonts\gm.sf2`,
	}
	c.SetOffset("wasapi-{cap-guid}", "wasapi-{play-guid}", 517, 0.93)
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := c
	want.Version = CurrentVersion // Save stamps a zero Version
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "nested")
	t.Setenv(EnvConfigDir, dir)
	if err := (Config{SoundFontPath: "x.sf2"}).Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestLoadCorruptedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{this is not json"), 0o644); err != nil {
		t.Fatalf("writing corrupted file: %v", err)
	}
	c, err := Load()
	if err == nil {
		t.Error("Load on corrupted file: nil error, want non-nil")
	}
	// Callers warn and continue with defaults, so the config itself must
	// be the usable zero value.
	if !reflect.DeepEqual(c, Config{}) {
		t.Errorf("Load = %+v, want zero Config", c)
	}
}

func TestSaveLeavesNoTempLitter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	var c Config
	for i := 0; i < 3; i++ {
		c.SetOffset("cap", "play", 100+i, 0.9)
		if err := c.Save(); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("config dir contains %v, want exactly [config.json]", names)
	}
}

func TestOffsetHelpers(t *testing.T) {
	var c Config
	if _, ok := c.OffsetFor("cap", "play"); ok {
		t.Fatal("zero Config reports a stored offset")
	}
	if _, ok := c.ConfidenceFor("cap", "play"); ok {
		t.Fatal("zero Config reports a stored confidence")
	}
	c.SetOffset("cap", "play", 480, 0.9)
	c.SetOffset("cap2", "play", -12, 0.7)

	tests := []struct {
		name              string
		capture, playback string
		wantOff           int
		wantConf          float64
		wantOK            bool
	}{
		{"stored pair", "cap", "play", 480, 0.9, true},
		{"second pair", "cap2", "play", -12, 0.7, true},
		{"unknown pair", "cap", "play2", 0, 0, false},
		{"swapped IDs are a different pair", "play", "cap", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			off, ok := c.OffsetFor(tt.capture, tt.playback)
			if off != tt.wantOff || ok != tt.wantOK {
				t.Errorf("OffsetFor = %d, %v, want %d, %v", off, ok, tt.wantOff, tt.wantOK)
			}
			conf, ok := c.ConfidenceFor(tt.capture, tt.playback)
			if conf != tt.wantConf || ok != tt.wantOK {
				t.Errorf("ConfidenceFor = %g, %v, want %g, %v", conf, ok, tt.wantConf, tt.wantOK)
			}
		})
	}

	// Recalibration replaces the stored pair.
	c.SetOffset("cap", "play", 501, 0.95)
	if off, _ := c.OffsetFor("cap", "play"); off != 501 {
		t.Errorf("after recalibration OffsetFor = %d, want 501", off)
	}
	if conf, _ := c.ConfidenceFor("cap", "play"); conf != 0.95 {
		t.Errorf("after recalibration ConfidenceFor = %g, want 0.95", conf)
	}
}
