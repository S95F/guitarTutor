// Package pitch is guitarTutor's monophonic pitch engine: an MPM (McLeod
// pitch method) detector with a YIN-FFT cross-check, an onset/energy gate,
// and a note tracker that turns per-hop f0 estimates into discrete note
// events. Pure Go on gonum's FFT; designed for a live electric-guitar DI
// signal per docs/DECISIONS.md D4.
//
// The physics budget (ROADMAP Phase 2): low E is 82.4 Hz, detectors want
// 2–4 cycles, so expect ~25–50 ms from onset to a stable estimate. Frames
// and notes are stamped in input-stream sample frames so callers can align
// them against the playback clock after latency calibration.
package pitch

// Config parameterizes the detector and tracker.
type Config struct {
	// SampleRate in Hz of the input stream.
	SampleRate int
	// Window is the analysis window in samples (power of two). Sized
	// for the lowest supported note: 2048 at 48 kHz reaches ~70 Hz;
	// use 4096 for tunings below that (drop C and lower).
	Window int
	// Hop is the analysis stride in samples.
	Hop int
	// MinHz and MaxHz bound the f0 search range.
	MinHz, MaxHz float64
	// ClarityThreshold is the minimum MPM clarity (0–1) for a frame's
	// f0 to be trusted.
	ClarityThreshold float64
}

// DefaultConfig returns the guitar-tuned defaults for a sample rate.
func DefaultConfig(sampleRate int) Config {
	return Config{
		SampleRate:       sampleRate,
		Window:           2048,
		Hop:              480,
		MinHz:            60,
		MaxHz:            1500,
		ClarityThreshold: 0.80,
	}
}

// A Frame is one analysis hop's estimate. F0 is 0 when no pitch cleared
// the clarity threshold (silence, noise, or the gate closed).
type Frame struct {
	// Frame is the input-stream sample frame of the window's center.
	Frame int64
	// F0 in Hz; 0 means unvoiced.
	F0 float64
	// Clarity is the MPM peak clarity in [0, 1].
	Clarity float64
	// RMS of the analysis window.
	RMS float64
	// Onset marks an attack transient detected at (or near) this hop.
	Onset bool
}

// A Detector turns a live sample stream into per-hop Frames. Feed it
// arbitrary-length chunks; it buffers internally and emits one Frame per
// hop. Not safe for concurrent use; feed it from one goroutine.
type Detector struct {
	// contains filtered or unexported fields
}

// NewDetector builds a detector. Config values of 0 take defaults.
func NewDetector(cfg Config) *Detector { panic("pitch: not implemented") }

// Process consumes samples and returns the Frames completed by them. The
// returned slice is reused across calls — copy anything you keep.
func (d *Detector) Process(samples []float32) []Frame { panic("pitch: not implemented") }

// A Note is a discrete note event assembled from consecutive voiced
// frames: quantized to the nearest MIDI key with a cents deviation.
type Note struct {
	// Start and End are input-stream sample frames. End is 0 while the
	// note is still sounding (only from Tracker.Current).
	Start, End int64
	// Key is the nearest MIDI note.
	Key int
	// Cents is the median deviation from Key, negative = flat.
	Cents float64
	// Clarity is the median frame clarity over the note.
	Clarity float64
}

// A Tracker assembles Frames into Notes with hysteresis: a note opens
// after enough consecutive voiced frames agree on a key, follows bends via
// the cents trajectory, and closes on silence, an onset, or a sustained
// key change. Not safe for concurrent use.
type Tracker struct {
	// contains filtered or unexported fields
}

// NewTracker builds a tracker sharing the detector's config.
func NewTracker(cfg Config) *Tracker { panic("pitch: not implemented") }

// Feed consumes detector frames and returns the notes that CLOSED during
// them. The returned slice is reused across calls — copy anything you keep.
func (t *Tracker) Feed(frames []Frame) []Note { panic("pitch: not implemented") }

// Current reports the still-sounding note, if any — the tuner view and
// wait mode read this.
func (t *Tracker) Current() (Note, bool) { panic("pitch: not implemented") }

// Flush closes and returns any in-progress note (end of stream).
func (t *Tracker) Flush() []Note { panic("pitch: not implemented") }
