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
	// NoiseFloorDB is the energy gate in dBFS (negative): a window whose
	// RMS is below it is unvoiced without any pitch analysis, and onsets
	// never fire below it. 0 takes the default (-55 dBFS).
	NoiseFloorDB float64
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
		NoiseFloorDB:     -55,
	}
}

// withDefaults fills zero fields with DefaultConfig values and clamps the
// rest into a usable range. In particular Window is rounded UP (rather
// than erroring) to the minimum the MaxHz search bound needs — one lag
// past tauMin ~= SampleRate/MaxHz — so the detector's analysis buffers can
// always hold the search range.
func (cfg Config) withDefaults() Config {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 48000
	}
	def := DefaultConfig(cfg.SampleRate)
	if cfg.Window <= 0 {
		cfg.Window = def.Window
	}
	if cfg.Hop <= 0 {
		cfg.Hop = def.Hop
	}
	if cfg.Hop > cfg.Window {
		cfg.Hop = cfg.Window
	}
	if cfg.MinHz <= 0 {
		cfg.MinHz = def.MinHz
	}
	if cfg.MaxHz <= 0 {
		cfg.MaxHz = def.MaxHz
	}
	if cfg.MaxHz < cfg.MinHz {
		cfg.MaxHz = cfg.MinHz
	}
	// Minimum usable window: the shortest search lag (tauMin, with the
	// detector's >= 2 floor) plus the one extra lag the analysis computes
	// for interpolation. Anything smaller cannot represent even the top
	// of the search range.
	minTau := int(float64(cfg.SampleRate) / cfg.MaxHz)
	if minTau < 2 {
		minTau = 2
	}
	if cfg.Window < minTau+2 {
		cfg.Window = minTau + 2
	}
	if cfg.ClarityThreshold <= 0 {
		cfg.ClarityThreshold = def.ClarityThreshold
	}
	if cfg.NoiseFloorDB == 0 {
		cfg.NoiseFloorDB = def.NoiseFloorDB
	}
	return cfg
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
