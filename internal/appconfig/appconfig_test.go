package appconfig

import (
	"errors"
	"fmt"
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
	if want := filepath.Join("musictutor", "config.json"); !strings.HasSuffix(p, want) {
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
	want.Version = CurrentVersion
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveLoadRoundTripShellFields(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())
	browse := t.TempDir()
	c := Config{
		CountInBeats:  4,
		LastBrowseDir: browse,
		WindowWidth:   1600,
		WindowHeight:  900,
	}
	c.AddRecent(filepath.Join(browse, "second.gp"))
	c.AddRecent(filepath.Join(browse, "first.gp"))
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := c
	want.Version = CurrentVersion
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}

	if len(got.Recents) != 2 || got.Recents[0] != filepath.Join(browse, "first.gp") {
		t.Errorf("Recents = %v, want most-recent-first", got.Recents)
	}
}

const v1Config = `{
  "version": 1,
  "captureDeviceID": "wasapi-{cap-guid}",
  "playbackDeviceID": "wasapi-{play-guid}",
  "latencyOffsets": {
    "wasapi-{cap-guid}|wasapi-{play-guid}": 517
  },
  "latencyConfidence": {
    "wasapi-{cap-guid}|wasapi-{play-guid}": 0.93
  },
  "soundFontPath": "C:\\soundfonts\\gm.sf2"
}
`

func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestLoadMigratesPreviousSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	writeConfigFile(t, dir, v1Config)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load of a version 1 file: %v", err)
	}

	want := Config{
		Version:          CurrentVersion,
		CaptureDeviceID:  "wasapi-{cap-guid}",
		PlaybackDeviceID: "wasapi-{play-guid}",
		LatencyOffsets:   map[string]int{"wasapi-{cap-guid}|wasapi-{play-guid}": 517},
		LatencyConfidence: map[string]float64{
			"wasapi-{cap-guid}|wasapi-{play-guid}": 0.93,
		},
		SoundFontPath: `C:\soundfonts\gm.sf2`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated config:\n got %+v\nwant %+v", got, want)
	}

	if got.Recents != nil || got.CountInBeats != 0 || got.LastBrowseDir != "" ||
		got.WindowWidth != 0 || got.WindowHeight != 0 {
		t.Errorf("new fields not defaulted: %+v", got)
	}
	if off, ok := got.OffsetFor("wasapi-{cap-guid}", "wasapi-{play-guid}"); !ok || off != 517 {
		t.Errorf("OffsetFor after migration = %d, %v, want 517, true", off, ok)
	}

	got.AddRecent(filepath.Join(dir, "new.gp"))
	if err := got.Save(); err != nil {
		t.Fatalf("Save after migration: %v", err)
	}
	back, err := Load()
	if err != nil {
		t.Fatalf("Load after migration save: %v", err)
	}
	if back.Version != CurrentVersion {
		t.Errorf("saved Version = %d, want %d", back.Version, CurrentVersion)
	}
	if back.SoundFontPath != want.SoundFontPath || len(back.Recents) != 1 {
		t.Errorf("after migration save got %+v", back)
	}
}

func TestLoadTreatsUnstampedFileAsFirstVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	writeConfigFile(t, dir, `{"soundFontPath": "hand-written.sf2"}`)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", got.Version, CurrentVersion)
	}
	if got.SoundFontPath != "hand-written.sf2" {
		t.Errorf("SoundFontPath = %q, want it preserved", got.SoundFontPath)
	}
}

func TestLoadRepairsRecentsFromDisk(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	entries := make([]string, 0, MaxRecents+4)
	entries = append(entries, `""`, `"/songs/a.gp"`, `"/songs/a.gp"`)
	for i := 0; i < MaxRecents+1; i++ {
		entries = append(entries, fmt.Sprintf(`"/songs/s%02d.gp"`, i))
	}
	writeConfigFile(t, dir, fmt.Sprintf(`{"version": %d, "recents": [%s]}`,
		CurrentVersion, strings.Join(entries, ", ")))

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Recents) != MaxRecents {
		t.Fatalf("len(Recents) = %d, want the cap %d", len(got.Recents), MaxRecents)
	}
	if got.Recents[0] != "/songs/a.gp" || got.Recents[1] != "/songs/s00.gp" {
		t.Errorf("Recents starts %v, want the blank dropped and the duplicate collapsed", got.Recents[:2])
	}
}

func TestLoadClampsHandEditedCountIn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	writeConfigFile(t, dir, fmt.Sprintf(`{"version": %d, "countInBeats": 999}`, CurrentVersion))
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CountInBeats != MaxCountInBeats {
		t.Errorf("CountInBeats = %d, want the cap %d", got.CountInBeats, MaxCountInBeats)
	}

	writeConfigFile(t, dir, fmt.Sprintf(`{"version": %d, "countInBeats": -3}`, CurrentVersion))
	if got, err = Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CountInBeats != 0 {
		t.Errorf("a negative count-in loaded as %d, want 0", got.CountInBeats)
	}
}

func TestLoadRefusesNewerVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	writeConfigFile(t, dir, fmt.Sprintf(`{"version": %d, "soundFontPath": "future.sf2"}`, CurrentVersion+1))

	c, err := Load()
	if err == nil {
		t.Fatal("Load of a newer-version file: nil error, want ErrNewerVersion")
	}
	if !errors.Is(err, ErrNewerVersion) {
		t.Errorf("Load error = %v, want one wrapping ErrNewerVersion", err)
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Errorf("Load = %+v, want the zero Config so the caller runs on defaults", c)
	}
}

func TestSaveRefusesToDowngradeNewerFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	newer := fmt.Sprintf("{\n  \"version\": %d,\n  \"soundFontPath\": \"future.sf2\"\n}\n", CurrentVersion+1)
	path := writeConfigFile(t, dir, newer)

	err := (Config{SoundFontPath: "old.sf2"}).Save()
	if err == nil {
		t.Fatal("Save over a newer-version file: nil error, want ErrNewerVersion")
	}
	if !errors.Is(err, ErrNewerVersion) {
		t.Errorf("Save error = %v, want one wrapping ErrNewerVersion", err)
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if string(data) != newer {
		t.Errorf("config file was rewritten:\n%s", data)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	if len(entries) != 1 {
		t.Errorf("config dir has %d entries after a refused save, want 1", len(entries))
	}
}

func TestSaveOverwritesSameOrOlderVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	writeConfigFile(t, dir, v1Config)

	if err := (Config{SoundFontPath: "new.sf2"}).Save(); err != nil {
		t.Fatalf("Save over an older file: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SoundFontPath != "new.sf2" || got.Version != CurrentVersion {
		t.Errorf("Load = %+v, want the new config at version %d", got, CurrentVersion)
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

	c.SetOffset("cap", "play", 501, 0.95)
	if off, _ := c.OffsetFor("cap", "play"); off != 501 {
		t.Errorf("after recalibration OffsetFor = %d, want 501", off)
	}
	if conf, _ := c.ConfidenceFor("cap", "play"); conf != 0.95 {
		t.Errorf("after recalibration ConfidenceFor = %g, want 0.95", conf)
	}
}

func TestAddCreatedMovesToTheFrontAndCaps(t *testing.T) {
	var c Config
	for i := 0; i < MaxCreated+5; i++ {
		c.AddCreated(filepath.Join("dir", fmt.Sprintf("p%02d.gtab", i)))
	}
	if len(c.Created) != MaxCreated {
		t.Fatalf("got %d written pieces, want the %d cap", len(c.Created), MaxCreated)
	}

	if !strings.HasSuffix(c.Created[0], fmt.Sprintf("p%02d.gtab", MaxCreated+4)) {
		t.Errorf("the front of the list is %q, want the last one written", c.Created[0])
	}

	again := c.Created[3]
	c.AddCreated(again)
	if c.Created[0] != again {
		t.Errorf("re-writing a piece left it at %q, want it at the front", c.Created[0])
	}
	seen := map[string]bool{}
	for _, p := range c.Created {
		if seen[p] {
			t.Errorf("%q is listed twice", p)
		}
		seen[p] = true
	}
}

func TestForgetCreated(t *testing.T) {
	var c Config
	c.AddCreated("a.gtab")
	c.AddCreated("b.gtab")
	c.ForgetCreated("a.gtab")
	if len(c.Created) != 1 || !strings.HasSuffix(c.Created[0], "b.gtab") {
		t.Errorf("got %v, want only b.gtab", c.Created)
	}
	c.ForgetCreated("b.gtab")
	if c.Created != nil {
		t.Errorf("a drained list is %v, want nil so it stays out of the JSON", c.Created)
	}
	c.ForgetCreated("never-there.gtab")
}

func TestCreatedSurvivesASaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	var c Config
	c.AddCreated(filepath.Join(dir, "mine.gtab"))
	c.HideStartHint = true
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Created) != 1 {
		t.Errorf("got %d written pieces back, want 1", len(got.Created))
	}
	if !got.HideStartHint {
		t.Error("the dismissed start hint did not survive the round trip")
	}
	if got.Version != CurrentVersion {
		t.Errorf("loaded version %d, want %d", got.Version, CurrentVersion)
	}
}

func TestOlderConfigMigratesForward(t *testing.T) {

	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	old := `{"version":2,"recents":["a.gp"],"countInBeats":4}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Version != CurrentVersion {
		t.Errorf("migrated to version %d, want %d", got.Version, CurrentVersion)
	}
	if got.CountInBeats != 4 || len(got.Recents) != 1 {
		t.Errorf("migration lost stored settings: %+v", got)
	}
	if got.SyncTrimMS != 0 || got.Created != nil || got.HideStartHint {
		t.Errorf("migration invented values for fields the old file had none of: %+v", got)
	}
}

func TestPiecesDirIsUnderTheConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	got, err := PiecesDir()
	if err != nil {
		t.Fatalf("PiecesDir: %v", err)
	}
	if want := filepath.Join(dir, PiecesDirName); got != want {
		t.Errorf("PiecesDir() = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Error("PiecesDir created the folder; only EnsurePiecesDir should")
	}
	made, err := EnsurePiecesDir()
	if err != nil {
		t.Fatalf("EnsurePiecesDir: %v", err)
	}
	if fi, err := os.Stat(made); err != nil || !fi.IsDir() {
		t.Errorf("EnsurePiecesDir did not make a directory: %v", err)
	}

	if _, err := EnsurePiecesDir(); err != nil {
		t.Errorf("EnsurePiecesDir on an existing folder: %v", err)
	}
}

func TestMigrateOldDirAdoptsGuitartutor(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, oldDirName)
	if err := os.MkdirAll(filepath.Join(old, "pieces"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		fileName:                           `{"version": 4}`,
		filepath.Join("pieces", "my.gtab"): "\\tempo 120\n0.6.1",
	} {
		if err := os.WriteFile(filepath.Join(old, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	migrateOldDir(base)

	for _, name := range []string{fileName, filepath.Join("pieces", "my.gtab")} {
		if _, err := os.Stat(filepath.Join(base, dirName, name)); err != nil {
			t.Errorf("after migration, %s: %v", name, err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old directory is still there; the move must not fork the config")
	}
}

func TestMigrateOldDirNeverOverwrites(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{dirName, oldDirName} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, oldDirName, fileName), []byte(`{"version": 4}`), 0o644); err != nil {
		t.Fatal(err)
	}

	migrateOldDir(base)

	if _, err := os.Stat(filepath.Join(base, oldDirName, fileName)); err != nil {
		t.Errorf("the old directory was disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, dirName, fileName)); !os.IsNotExist(err) {
		t.Error("migration wrote into an existing musictutor directory")
	}
}

func TestRehomePathsFollowsTheMigration(t *testing.T) {
	base := t.TempDir()
	t.Setenv(EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("AppData", base)
	if err := os.MkdirAll(filepath.Join(base, dirName), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPieces := filepath.Join(base, oldDirName, "pieces", "riff.gtab")
	elsewhere := filepath.Join(base, "elsewhere", "song.gtab")
	c := Config{
		Recents:       []string{oldPieces, elsewhere},
		Created:       []string{oldPieces},
		LastBrowseDir: filepath.Join(base, oldDirName, "pieces"),
	}
	c.rehomePaths()

	want := filepath.Join(base, dirName, "pieces", "riff.gtab")
	if c.Recents[0] != want {
		t.Errorf("recent = %q, want %q", c.Recents[0], want)
	}
	if c.Recents[1] != elsewhere {
		t.Errorf("a path outside the old directory moved: %q", c.Recents[1])
	}
	if c.Created[0] != want {
		t.Errorf("created = %q, want %q", c.Created[0], want)
	}
	if wantDir := filepath.Join(base, dirName, "pieces"); c.LastBrowseDir != wantDir {
		t.Errorf("browse dir = %q, want %q", c.LastBrowseDir, wantDir)
	}

	if err := os.MkdirAll(filepath.Join(base, oldDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	c2 := Config{Recents: []string{oldPieces}}
	c2.rehomePaths()
	if c2.Recents[0] != oldPieces {
		t.Errorf("rehome fired with the old directory present: %q", c2.Recents[0])
	}
}
