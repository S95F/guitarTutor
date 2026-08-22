package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const EnvConfigDir = "MUSICTUTOR_CONFIG_DIR"

const CurrentVersion = 4

var ErrNewerVersion = errors.New("config file is from a newer version of musicTutor")

const (
	dirName    = "musictutor"
	oldDirName = "guitartutor"
	fileName   = "config.json"
)

type Config struct {
	Version int `json:"version"`

	CaptureDeviceID  string `json:"captureDeviceID,omitempty"`
	PlaybackDeviceID string `json:"playbackDeviceID,omitempty"`

	LatencyOffsets map[string]int `json:"latencyOffsets,omitempty"`

	LatencyConfidence map[string]float64 `json:"latencyConfidence,omitempty"`

	SoundFontPath string `json:"soundFontPath,omitempty"`

	Recents []string `json:"recents,omitempty"`

	CountInBeats int `json:"countInBeats,omitempty"`

	LastBrowseDir string `json:"lastBrowseDir,omitempty"`

	WindowWidth  int `json:"windowWidth,omitempty"`
	WindowHeight int `json:"windowHeight,omitempty"`

	Created []string `json:"created,omitempty"`

	HideStartHint bool `json:"hideStartHint,omitempty"`

	SyncTrimMS int `json:"syncTrimMS,omitempty"`
}

const MaxSyncTrimMS = 250

const (
	MaxCountInBeats     = 8
	DefaultCountInBeats = 2
)

func Path() (string, error) {
	if dir := os.Getenv(EnvConfigDir); dir != "" {
		return filepath.Join(dir, fileName), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("appconfig: locating user config dir: %w", err)
	}
	migrateOldDir(base)
	return filepath.Join(base, dirName, fileName), nil
}

func migrateOldDir(base string) {
	newDir := filepath.Join(base, dirName)
	if _, err := os.Stat(newDir); err == nil || !os.IsNotExist(err) {
		return
	}
	oldDir := filepath.Join(base, oldDirName)
	if _, err := os.Stat(oldDir); err != nil {
		return
	}
	_ = os.Rename(oldDir, newDir)
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{CountInBeats: DefaultCountInBeats}, nil
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
	c.rehomePaths()
	return c, nil
}

func (c *Config) rehomePaths() {
	if os.Getenv(EnvConfigDir) != "" {
		return
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return
	}
	oldDir := filepath.Join(base, oldDirName)
	newDir := filepath.Join(base, dirName)
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		return
	}
	if _, err := os.Stat(newDir); err != nil {
		return
	}
	prefix := oldDir + string(filepath.Separator)
	rehome := func(p string) string {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return filepath.Join(newDir, rest)
		}
		return p
	}
	for i, p := range c.Recents {
		c.Recents[i] = rehome(p)
	}
	for i, p := range c.Created {
		c.Created[i] = rehome(p)
	}
	c.LastBrowseDir = rehome(c.LastBrowseDir)
	c.SoundFontPath = rehome(c.SoundFontPath)
}

func (c *Config) migrate() {
	c.Version = CurrentVersion

	c.Recents = sanitizeRecents(c.Recents)
	c.Created = sanitizeCreated(c.Created)
	if c.CountInBeats < 0 {
		c.CountInBeats = 0
	}
	if c.CountInBeats > MaxCountInBeats {
		c.CountInBeats = MaxCountInBeats
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

func pairKey(captureID, playbackID string) string {
	return captureID + "|" + playbackID
}

func (c Config) OffsetFor(captureID, playbackID string) (int, bool) {
	off, ok := c.LatencyOffsets[pairKey(captureID, playbackID)]
	return off, ok
}

func (c Config) ConfidenceFor(captureID, playbackID string) (float64, bool) {
	conf, ok := c.LatencyConfidence[pairKey(captureID, playbackID)]
	return conf, ok
}

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
