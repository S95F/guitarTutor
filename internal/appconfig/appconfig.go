// Package appconfig persists user preferences — chosen audio devices,
// calibrated latency offsets, an optional SoundFont path, recently-opened
// pieces and the window geometry to restore — as a small JSON file in the
// platform config directory
// (os.UserConfigDir()/guitartutor/config.json).
//
// The zero Config is always usable: a missing file loads as one without
// error, and a corrupted file loads as one alongside the parse error so
// callers can warn and continue with defaults rather than refuse to
// start. Saves are atomic (write a temp file, then rename over the
// target), so a crash mid-save never leaves a half-written config.
//
// Files carry a schema version. Load migrates older ones forward, filling
// fields that build had never heard of with their documented defaults. A
// file from a *newer* build is refused rather than reinterpreted, and
// Save will not write over one: running an older exe for an afternoon
// must not silently strip settings the newer one stored (see
// ErrNewerVersion).
//
// The GUITARTUTOR_CONFIG_DIR environment variable overrides the config
// directory entirely; it exists as the test seam (see EnvConfigDir).
package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvConfigDir names the environment variable that, when non-empty,
// replaces the default config directory: the file lives directly at
// $GUITARTUTOR_CONFIG_DIR/config.json. This is the test seam — tests
// point it at a temp dir (t.Setenv) so they never touch the real user
// config — but it also lets users run fully portable installs.
const EnvConfigDir = "GUITARTUTOR_CONFIG_DIR"

// CurrentVersion is the config schema version this build reads and
// writes. Load migrates any file from firstVersion..CurrentVersion up to
// it, so a Config in memory is always at the current schema; Save stamps
// it into the file.
//
// Version history:
//
//	1 — devices, latency offsets and confidence, SoundFont path.
//	2 — adds recents, count-in beats, last browse directory, window size.
//	3 — adds the audio/visual sync trim.
//	4 — adds the written-pieces list and the start hint's hidden flag.
const CurrentVersion = 4

// firstVersion is the oldest schema this build can migrate forward. It is
// also what a file carrying no version at all is treated as: Save has
// always stamped one, so an unstamped file is hand-written, and the
// original shape is the only thing it can mean.
const firstVersion = 1

// ErrNewerVersion reports a config file written by a newer build of
// guitarTutor than the one reading it. Load refuses such a file (it
// cannot know what its fields mean) and Save refuses to overwrite one (it
// would downgrade settings the newer build is still using), both
// returning an error wrapping this one so callers can recognize the case
// and tell the user to upgrade rather than blaming a corrupt file.
var ErrNewerVersion = errors.New("config file is from a newer version of guitarTutor")

// dirName and fileName locate the config under os.UserConfigDir().
const (
	dirName  = "guitartutor"
	fileName = "config.json"
)

// A Config holds the user preferences guitarTutor persists between runs.
// The zero value is a valid "no preferences yet" config; all lookups on
// it simply report nothing stored.
type Config struct {
	// Version is the schema version of the file this config came from.
	// Save stamps CurrentVersion when it is zero.
	Version int `json:"version"`
	// CaptureDeviceID and PlaybackDeviceID are audio.DeviceInfo IDs of
	// the preferred endpoints; empty means the system default.
	CaptureDeviceID  string `json:"captureDeviceID,omitempty"`
	PlaybackDeviceID string `json:"playbackDeviceID,omitempty"`
	// LatencyOffsets holds the calibrated round-trip offset in frames
	// for each device pair the wizard has measured, keyed by
	// captureID+"|"+playbackID (device IDs never contain '|'; WASAPI and
	// friends use GUID-style strings). Use OffsetFor/SetOffset rather
	// than touching the map directly.
	LatencyOffsets map[string]int `json:"latencyOffsets,omitempty"`
	// LatencyConfidence holds the confidence latency.Estimate reported
	// for each stored offset, same keys as LatencyOffsets, so the UI can
	// suggest recalibration for weak measurements.
	LatencyConfidence map[string]float64 `json:"latencyConfidence,omitempty"`
	// SoundFontPath is the SF2 file for the meltysynth voice; empty
	// selects the built-in Karplus-Strong voice.
	SoundFontPath string `json:"soundFontPath,omitempty"`
	// Recents lists recently-opened piece paths, most recent first, at
	// most MaxRecents of them. Entries are normalized absolute paths and
	// unique (case-insensitively on Windows). Use AddRecent/ForgetRecent
	// rather than editing the slice, so ordering and de-duplication hold.
	// A listed file may since have been moved or deleted — the list is a
	// record of what was opened, not a promise that it still exists, so
	// callers stat before offering an entry.
	Recents []string `json:"recents,omitempty"`
	// CountInBeats is the number of click beats before playback starts,
	// as engine.Options.CountInBeats. Zero — the default — means no
	// count-in, which is also what the -countin flag defaults to.
	CountInBeats int `json:"countInBeats,omitempty"`
	// LastBrowseDir is the directory the file browser was last showing,
	// so opening a second piece starts where the first was found rather
	// than at the home directory. Empty means "no preference yet"; the
	// directory may since have been removed, so callers fall back.
	LastBrowseDir string `json:"lastBrowseDir,omitempty"`
	// WindowWidth and WindowHeight are the window size to restore, in
	// physical pixels. Zero — for either — means use the built-in default
	// size, so a config that has never seen a resize imposes nothing.
	WindowWidth  int `json:"windowWidth,omitempty"`
	WindowHeight int `json:"windowHeight,omitempty"`
	// Created lists recently WRITTEN pieces, most recent first, at most
	// MaxCreated of them — the counterpart to Recents, which lists the
	// ones that were opened. A piece appears in both when it is written
	// and then practised, which is correct: it is recent in both senses.
	// Use AddCreated/ForgetCreated rather than editing the slice.
	Created []string `json:"created,omitempty"`
	// HideStartHint suppresses the start screen's getting-started strip.
	// Everything it points at is reachable from settings, so a user who
	// has read it once should be able to put it away for good.
	HideStartHint bool `json:"hideStartHint,omitempty"`
	// SyncTrimMS nudges the playhead against the sound, in milliseconds.
	// The application already subtracts the output buffering it can
	// measure, so this is for what it cannot: the few milliseconds the
	// hardware and its driver hold onto after the software has handed the
	// audio over. Positive moves the notation LATER — use it when the
	// playhead still runs ahead of what you hear. Zero, the default, means
	// no trim, and the measured compensation stands on its own.
	SyncTrimMS int `json:"syncTrimMS,omitempty"`
}

// MaxSyncTrimMS bounds the manual sync trim either way. A quarter of a
// second is already far more than any driver holds; past that the trim
// stops correcting an error and starts being one.
const MaxSyncTrimMS = 250

// Path returns the config file path: $GUITARTUTOR_CONFIG_DIR/config.json
// when the override is set, else os.UserConfigDir()/guitartutor/config.json.
func Path() (string, error) {
	if dir := os.Getenv(EnvConfigDir); dir != "" {
		return filepath.Join(dir, fileName), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("appconfig: locating user config dir: %w", err)
	}
	return filepath.Join(base, dirName, fileName), nil
}

// Load reads the config file. A missing file is not an error — a first
// run gets the zero Config with a nil error. A file that exists but does
// not parse returns the zero Config together with the error, so callers
// warn and continue with defaults (the broken file is left in place for
// the user to inspect; the next Save replaces it).
//
// A file from an older schema is migrated forward: its stored fields keep
// their meaning and anything this version added takes its documented
// default, so upgrading never loses a device choice or a calibration. The
// returned Config always reports CurrentVersion.
//
// A file from a newer schema is refused: the zero Config comes back with
// an error wrapping ErrNewerVersion, and Save will likewise decline to
// overwrite it, so a downgraded exe runs on defaults instead of quietly
// discarding the newer build's settings.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("appconfig: reading %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("appconfig: parsing %s: %w", path, err)
	}
	if c.Version > CurrentVersion {
		return Config{}, fmt.Errorf("appconfig: %s is schema version %d, this build reads %d: %w",
			path, c.Version, CurrentVersion, ErrNewerVersion)
	}
	c.migrate()
	return c, nil
}

// migrate brings a config parsed off disk up to CurrentVersion in place.
// Each step upgrades one version, so the chain still works for someone
// skipping several releases.
//
// The 1 -> 2 step adds recents, count-in, the last browse directory and
// the window size, and the 2 -> 3 step adds the sync trim; every one of
// those is documented as "zero means the default", so neither migration
// has a field to rewrite, only the version to advance. The steps are
// spelled out anyway, because one of them will eventually not be so lucky
// and this is where it goes.
func (c *Config) migrate() {
	if c.Version < firstVersion {
		// No stamp (or a nonsense one): treat it as the original shape.
		c.Version = firstVersion
	}
	if c.Version < 2 {
		c.Version = 2
	}
	if c.Version < 3 {
		c.Version = 3
	}
	if c.Version < 4 {
		c.Version = 4
	}
	// Repair rather than trust: the file may have been hand-edited, or
	// written by a build with a different cap.
	c.Recents = sanitizeRecents(c.Recents)
	c.Created = sanitizeCreated(c.Created)
	if c.CountInBeats < 0 {
		c.CountInBeats = 0
	}
	if c.WindowWidth < 0 {
		c.WindowWidth = 0
	}
	if c.SyncTrimMS < -MaxSyncTrimMS {
		c.SyncTrimMS = -MaxSyncTrimMS
	}
	if c.SyncTrimMS > MaxSyncTrimMS {
		c.SyncTrimMS = MaxSyncTrimMS
	}
	if c.WindowHeight < 0 {
		c.WindowHeight = 0
	}
}

// Save writes the config atomically: it creates the config directory if
// needed, writes a temp file beside the target, and renames it into
// place, so a crash mid-write can never leave a truncated config.json.
// A zero Version is stamped with CurrentVersion on the way out.
//
// Save refuses, with an error wrapping ErrNewerVersion, when the file
// already there carries a newer schema version than the config being
// written: an older exe must not downgrade a newer build's settings. A
// missing or unreadable existing file has nothing to protect, so it is
// simply replaced, as the corrupt-file rule promises.
func (c Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("appconfig: creating %s: %w", dir, err)
	}
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if on := onDiskVersion(path); on > c.Version {
		return fmt.Errorf("appconfig: %s is schema version %d, refusing to overwrite it with version %d: %w",
			path, on, c.Version, ErrNewerVersion)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("appconfig: encoding config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, fileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("appconfig: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("appconfig: writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("appconfig: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("appconfig: replacing %s: %w", path, err)
	}
	return nil
}

// onDiskVersion reports the schema version of the config file at path,
// reading nothing else from it. Zero means "no version to respect": the
// file is missing, unreadable, or not JSON, all cases the existing
// durability rules already answer with "the next save replaces it".
func onDiskVersion(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return 0
	}
	return head.Version
}

// pairKey builds the LatencyOffsets/LatencyConfidence key for a device
// pair. The offset belongs to the pair, not to either device alone: the
// capture and playback paths each contribute to the round trip.
func pairKey(captureID, playbackID string) string {
	return captureID + "|" + playbackID
}

// OffsetFor reports the calibrated latency offset in frames for a device
// pair, and whether one has been stored. Safe on the zero Config.
func (c Config) OffsetFor(captureID, playbackID string) (int, bool) {
	off, ok := c.LatencyOffsets[pairKey(captureID, playbackID)]
	return off, ok
}

// ConfidenceFor reports the stored calibration confidence for a device
// pair, and whether one has been stored. Safe on the zero Config.
func (c Config) ConfidenceFor(captureID, playbackID string) (float64, bool) {
	conf, ok := c.LatencyConfidence[pairKey(captureID, playbackID)]
	return conf, ok
}

// SetOffset records a calibration result for a device pair, replacing
// any previous one, and allocates the maps on first use.
func (c *Config) SetOffset(captureID, playbackID string, offsetFrames int, confidence float64) {
	if c.LatencyOffsets == nil {
		c.LatencyOffsets = make(map[string]int)
	}
	if c.LatencyConfidence == nil {
		c.LatencyConfidence = make(map[string]float64)
	}
	key := pairKey(captureID, playbackID)
	c.LatencyOffsets[key] = offsetFrames
	c.LatencyConfidence[key] = confidence
}
