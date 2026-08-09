// Package appconfig persists user preferences — chosen audio devices,
// calibrated latency offsets, an optional SoundFont path — as a small
// JSON file in the platform config directory
// (os.UserConfigDir()/guitartutor/config.json).
//
// The zero Config is always usable: a missing file loads as one without
// error, and a corrupted file loads as one alongside the parse error so
// callers can warn and continue with defaults rather than refuse to
// start. Saves are atomic (write a temp file, then rename over the
// target), so a crash mid-save never leaves a half-written config.
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

// CurrentVersion is the config schema version Save stamps into files
// written by this build. Load returns whatever version the file carries;
// future releases use it to migrate old files.
const CurrentVersion = 1

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
}

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
	return c, nil
}

// Save writes the config atomically: it creates the config directory if
// needed, writes a temp file beside the target, and renames it into
// place, so a crash mid-write can never leave a truncated config.json.
// A zero Version is stamped with CurrentVersion on the way out.
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
