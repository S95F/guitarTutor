// Package engine is guitarTutor's frame-counted practice sequencer: the one
// clock everything else hangs off (ROADMAP.md "Guiding principles").
//
// The engine owns a playback position in ticks and advances it by rendered
// audio frames, converting frames to ticks through the score's tempo map
// scaled by the practice tempo scale. All sounding sources — track voices
// and the metronome click — are mixed here, scheduled by frame count. Wall
// clock and time.Ticker are never consulted.
//
// Contract (the parts that are correctness features, not preferences):
//
//   - Rendering advances tick position segment-wise: a render block is split
//     at every boundary that changes the frames-per-tick conversion or the
//     event stream — tempo-map changes, the loop end point, and the score
//     end — so loops are sample-accurate and tempo changes land exactly.
//   - When the position reaches the loop end (B), the remaining frames of
//     the block continue from the loop start (A) in the same Render call;
//     ringing notes get AllNotesOff at the boundary. Gapless is the default;
//     an optional count-in plays between passes.
//   - Read (io.Reader for oto) and all control methods are safe to call
//     concurrently: controls take a short mutex; Render never blocks on
//     anything else and never allocates in steady state.
//   - UI state queries (PosTick, etc.) are cheap snapshots.
package engine

import (
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/synth"
)

// Options configures a new Engine.
type Options struct {
	// SampleRate in Hz. 0 means 48000 (the project-wide standard).
	SampleRate int
	// Voices creates one Voice per track. Nil panics: the engine does
	// not choose synthesis.
	Voices synth.Factory
	// Metronome enables the click at start.
	Metronome bool
	// CountInBeats is the number of click beats before playback starts
	// when Play is called from a stopped/paused position. 0 disables.
	CountInBeats int
	// CountInEveryPass repeats the count-in between loop passes
	// (default false: loop passes are gapless).
	CountInEveryPass bool
}

// RampConfig is the progressive speed trainer: after each completed loop
// pass, the tempo scale increases by Increment until Target.
type RampConfig struct {
	Enabled   bool
	Increment float64 // added to the tempo scale per pass, e.g. 0.05
	Target    float64 // stop raising at this scale, e.g. 1.0
}

// Engine sequences one Score. Create with New; drive audio by Read
// (io.Reader yielding interleaved stereo float32 little-endian, the format
// oto is configured for) or RenderFrames for offline use.
type Engine struct {
	// contains filtered or unexported fields
}

// New builds an engine for sc. The score must already Validate.
func New(sc *score.Score, opts Options) *Engine { panic("engine: not implemented") }

// --- Transport ---

// Play starts or resumes playback (with count-in, if configured).
func (e *Engine) Play() { panic("engine: not implemented") }

// Pause stops advancing; position is kept. Ringing notes are silenced.
func (e *Engine) Pause() { panic("engine: not implemented") }

// Playing reports whether the transport is running (count-in included).
func (e *Engine) Playing() bool { panic("engine: not implemented") }

// SeekTick moves the position. Ringing notes are silenced.
func (e *Engine) SeekTick(tick int64) { panic("engine: not implemented") }

// SetLoop sets loop points [a, b) in ticks and enables looping.
// Callers pass bar boundaries; the engine loops whatever it is given.
func (e *Engine) SetLoop(a, b int64) { panic("engine: not implemented") }

// ClearLoop disables looping.
func (e *Engine) ClearLoop() { panic("engine: not implemented") }

// Loop returns the loop points and whether looping is enabled.
func (e *Engine) Loop() (a, b int64, on bool) { panic("engine: not implemented") }

// SetTempoScale sets the practice speed multiplier (1.0 = as written,
// 0.5 = half speed). Clamped to [0.25, 2.0]. Takes effect at the next
// render block; pitch is unaffected (synthesis is re-rendered, not
// resampled).
func (e *Engine) SetTempoScale(s float64) { panic("engine: not implemented") }

// TempoScale returns the current practice speed multiplier.
func (e *Engine) TempoScale() float64 { panic("engine: not implemented") }

// SetRamp configures the progressive speed trainer.
func (e *Engine) SetRamp(r RampConfig) { panic("engine: not implemented") }

// SetMetronome toggles the click.
func (e *Engine) SetMetronome(on bool) { panic("engine: not implemented") }

// SetTrackMuted mutes or unmutes a track by index.
func (e *Engine) SetTrackMuted(track int, muted bool) { panic("engine: not implemented") }

// TrackMuted reports a track's mute state.
func (e *Engine) TrackMuted(track int) bool { panic("engine: not implemented") }

// --- State snapshots (cheap; safe from any goroutine) ---

// PosTick returns the current playback position in ticks.
func (e *Engine) PosTick() int64 { panic("engine: not implemented") }

// PassCount returns completed loop passes since the loop was set.
func (e *Engine) PassCount() int { panic("engine: not implemented") }

// EffectiveBPM returns the sounding tempo at the current position
// (score tempo × tempo scale).
func (e *Engine) EffectiveBPM() float64 { panic("engine: not implemented") }

// CountingIn reports whether a count-in is sounding, and if so how many
// click beats remain.
func (e *Engine) CountingIn() (bool, int) { panic("engine: not implemented") }

// --- Audio ---

// Read implements io.Reader for oto: interleaved stereo float32 LE.
// It never returns an error and always fills p (silence when paused).
func (e *Engine) Read(p []byte) (int, error) { panic("engine: not implemented") }

// RenderFrames renders exactly len(left) frames into the given buffers
// (zeroing them first). Offline path: used by `guitartutor render` and by
// tests. Same code path as Read.
func (e *Engine) RenderFrames(left, right []float32) { panic("engine: not implemented") }
