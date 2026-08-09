// Package audio abstracts live audio I/O behind the Backend interface, per
// ROADMAP Phase 2: one full-duplex stream so guitar capture and playback
// share a callback, with the concrete implementation (miniaudio via
// gen2brain/malgo) fenced behind cgo. Builds without cgo get playback-only
// behavior (Available returns nil) — the pure-Go fallback the README
// promises contributors.
package audio

// A DeviceInfo describes one capture or playback endpoint.
type DeviceInfo struct {
	// ID is a backend-specific opaque identifier, stable enough to store
	// in config. Empty means the system default.
	ID      string
	Name    string
	Default bool
}

// A StreamConfig requests stream parameters; Open may negotiate different
// ones (see Stream.Config for what was actually granted).
type StreamConfig struct {
	// SampleRate in Hz. 0 means 48000, the project-wide standard.
	SampleRate int
	// PeriodFrames is the requested callback period. 0 lets the backend
	// choose a low-latency default (256–480 at 48 kHz on WASAPI shared).
	PeriodFrames int
	// CaptureDevice and PlaybackDevice are DeviceInfo.IDs; empty means
	// the system default. Putting both on the same physical interface is
	// what makes input and output share a clock (ROADMAP "Known risks");
	// callers should steer users there.
	CaptureDevice  string
	PlaybackDevice string
}

// A DuplexHandler is called on the audio thread for every period: in holds
// the captured guitar signal (mono — channel 0 of the capture device), and
// outL/outR must be filled completely with playback audio. All three share
// a length (the negotiated period may vary call to call; always use
// len(in)). The handler must be allocation-free and must never block —
// hand data off through preallocated ring buffers.
type DuplexHandler func(in, outL, outR []float32)

// A Stream is an open duplex stream. Frames delivered to the handler are
// consecutive; the total delivered count is the shared clock both capture
// timestamps and playback positions hang off.
type Stream interface {
	Start() error
	Stop() error
	// Close releases the device. The stream cannot be restarted.
	Close() error
	// Config reports the negotiated parameters (actual sample rate and
	// period), not the requested ones.
	Config() StreamConfig
}

// A Backend enumerates devices and opens duplex streams.
type Backend interface {
	// Name identifies the backend (e.g. "miniaudio/wasapi") for logs
	// and the device-picker UI.
	Name() string
	// Devices lists capture and playback endpoints.
	Devices() (capture, playback []DeviceInfo, err error)
	// OpenDuplex opens (but does not start) a duplex stream.
	OpenDuplex(cfg StreamConfig, h DuplexHandler) (Stream, error)
}
