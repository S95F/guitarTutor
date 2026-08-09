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
//   - Wait mode (SetWaitMode; see wait.go) halts the position exactly at
//     the frame a user-track NoteOn would fire and holds those events
//     unfired until ConfirmWait releases them on the release frame; frames
//     keep flowing while waiting (voice tails ring, metronome silent).
package engine

import (
	"encoding/binary"
	"math"
	"sync"
	"sync/atomic"

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

// Tempo scale bounds and the default sample rate.
const (
	defaultSampleRate = 48000
	minTempoScale     = 0.25
	maxTempoScale     = 2.0
)

// readBlockFrames is the internal block size Read renders through.
const readBlockFrames = 2048

// Engine sequences one Score. Create with New; drive audio by Read
// (io.Reader yielding interleaved stereo float32 little-endian, the format
// oto is configured for) or RenderFrames for offline use.
type Engine struct {
	mu sync.Mutex // guards everything below except the atomic snapshots

	sc               *score.Score
	sampleRate       int
	countInBeats     int
	countInEveryPass bool

	events   []score.NoteEvent // flattened score, sorted by Start
	scoreEnd int64             // tick just past the last bar

	voices    []synth.Voice // one per track
	muted     []bool        // one per track
	userTrack []bool        // one per track: Role == score.RoleUser

	// Transport and control state.
	playing bool
	pos     float64 // position in ticks; fractional between frames
	scale   float64 // practice tempo scale
	ramp    RampConfig

	loopA, loopB int64
	loopOn       bool
	passes       int // completed loop passes since SetLoop

	metronome bool

	// Wait-mode state (see wait.go). While waiting the position is frozen
	// at the wait point's frame with its events unfired; waitReleased marks
	// a confirmed wait whose held events fire at the next action frame.
	waitMode     bool
	waiting      bool
	waitReleased bool
	waitTick     int64 // tick of the wait point; valid while waiting

	// Scheduling state.
	nextEvent int          // index into events of the next unfired event
	active    []activeNote // sounding notes awaiting their NoteOff
	absFrame  int64        // output frames produced since New (the stream clock)
	tap       func(ev score.NoteEvent, outFrame int64)

	// Segment state: one span of constant frames-per-tick, anchored at an
	// exact tick so event frames are independent of block partitioning.
	// See render.go.
	segValid  bool
	anchor    float64 // tick at segment start
	fpt       float64 // frames per tick within the segment
	segFrame  int     // frames rendered since anchor
	segEnd    int     // segment length in frames
	boundary  int64   // tick the segment ends at
	bKind     boundaryKind
	nextBeat  int64 // tick of the next metronome beat
	beatLen   int64 // ticks per beat under the segment's meter
	barLen    int64 // ticks per bar under the segment's meter
	meterBase int64 // tick the segment's meter took effect

	// Count-in state. Active while ciBeatsLeft > 0; position frozen.
	ciBeatsLeft int
	ciFPB       int // frames per count-in beat
	ciFrameIn   int // frames into the current count-in beat

	// Prerendered metronome bursts and the currently sounding clicks.
	accentBuf, beatBuf []float32
	clicks             [4]clickState

	// Read conversion buffers (single reader, as io.Reader implies).
	readL, readR   []float32
	rem            [8]byte // partially consumed frame
	remOff, remLen int

	// Published snapshots, refreshed once per render block and on every
	// control change; read lock-free by the state query methods.
	aPos     atomic.Int64
	aPlaying atomic.Bool
	aPasses  atomic.Int64
	aBPM     atomic.Uint64
	aCiOn    atomic.Bool
	aCiLeft  atomic.Int64
	aFrames  atomic.Int64
	aWaiting atomic.Bool
	aWaitGen atomic.Uint64
	aDiscont atomic.Int64
}

// An activeNote is a sounding note awaiting its NoteOff at end.
type activeNote struct {
	track int
	key   int
	end   int64 // tick, exclusive
}

// New builds an engine for sc. The score must already Validate.
func New(sc *score.Score, opts Options) *Engine {
	if opts.Voices == nil {
		panic("engine: Options.Voices is nil")
	}
	sr := opts.SampleRate
	if sr == 0 {
		sr = defaultSampleRate
	}
	e := &Engine{
		sc:               sc,
		sampleRate:       sr,
		countInBeats:     opts.CountInBeats,
		countInEveryPass: opts.CountInEveryPass,
		events:           sc.Events(),
		scoreEnd:         sc.End(),
		metronome:        opts.Metronome,
		scale:            1.0,
	}
	e.voices = make([]synth.Voice, len(sc.Tracks))
	e.muted = make([]bool, len(sc.Tracks))
	e.userTrack = make([]bool, len(sc.Tracks))
	for i, tr := range sc.Tracks {
		e.voices[i] = opts.Voices(sr, tr.Program)
		e.userTrack[i] = tr.Role == score.RoleUser
	}
	e.active = make([]activeNote, 0, len(e.events)+1)
	e.accentBuf = renderClickBurst(sr, clickAccentHz)
	e.beatBuf = renderClickBurst(sr, clickBeatHz)
	e.readL = make([]float32, readBlockFrames)
	e.readR = make([]float32, readBlockFrames)
	e.publish()
	return e
}

// --- Transport ---

// Play starts or resumes playback (with count-in, if configured).
func (e *Engine) Play() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.playing {
		return
	}
	e.playing = true
	if e.countInBeats > 0 {
		e.startCountIn()
	}
	e.publish()
}

// Pause stops advancing; position is kept. Ringing notes are silenced.
// An active wait is cleared without firing its held notes; Play re-arms
// wait mode at the next user note (the same one, if the position has not
// moved).
func (e *Engine) Pause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.playing {
		return
	}
	e.playing = false
	e.allNotesOff()
	e.stopClicks()
	e.ciBeatsLeft, e.ciFrameIn = 0, 0
	e.clearWait()
	e.publish()
}

// Playing reports whether the transport is running (count-in included).
func (e *Engine) Playing() bool { return e.aPlaying.Load() }

// SeekTick moves the position. Ringing notes are silenced. An active wait
// is cleared without firing its held notes; wait mode re-arms at the next
// user note at or after the seek target.
func (e *Engine) SeekTick(tick int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tick < 0 {
		tick = 0
	}
	if tick > e.scoreEnd {
		tick = e.scoreEnd
	}
	e.allNotesOff()
	e.pos = float64(tick)
	e.segValid = false
	e.reindexFrom(tick)
	e.clearWait()
	e.markDiscontinuity()
	e.publish()
}

// SetLoop sets loop points [a, b) in ticks and enables looping.
// Callers pass bar boundaries; the engine loops whatever it is given,
// clamped to the score (a at least 0, b at most the score end). If the
// clamped span is empty, looping is disabled. An active wait is cleared
// without firing its held notes (the loop change moves the goalposts);
// wait mode re-arms at the next user note.
func (e *Engine) SetLoop(a, b int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a < 0 {
		a = 0
	}
	if b > e.scoreEnd {
		b = e.scoreEnd
	}
	e.clearWait()
	e.markDiscontinuity()
	if b <= a {
		e.loopOn = false
		e.segValid = false
		e.publish()
		return
	}
	e.loopA, e.loopB = a, b
	e.loopOn = true
	e.passes = 0
	e.segValid = false
	e.publish()
}

// ClearLoop disables looping. Like SetLoop, an active wait is cleared.
func (e *Engine) ClearLoop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loopOn = false
	e.segValid = false
	e.clearWait()
	e.markDiscontinuity()
	e.publish()
}

// markDiscontinuity stamps the stream clock as the moment the position or
// the loop bounds jumped under the player. Caller holds mu.
func (e *Engine) markDiscontinuity() { e.aDiscont.Store(e.absFrame) }

// DiscontinuityFrame returns the output-clock frame of the most recent
// position discontinuity: a seek, or a loop change. It is 0 until one
// happens.
//
// A loop WRAP is deliberately not one. Wrapping is the practice loop
// working as intended — the notes near the loop end were heard and are
// still being answered — whereas a seek or a loop edit truncates the
// player's chance to answer whatever just sounded. Wiring that judges
// timing uses this to tell those apart: the practice Scorer abandons
// expectations stamped before this frame, so a UI action cannot age them
// into misses (see Scorer.AbandonBefore). Comparing frames rather than
// counting events makes the split exact — everything the tap stamped
// before the jump is stale, everything after it is live — which a bare
// counter cannot express, since the tap keeps running between the seek
// and the analysis goroutine noticing it.
//
// Lock-free snapshot, safe from any goroutine.
func (e *Engine) DiscontinuityFrame() int64 { return e.aDiscont.Load() }

// Loop returns the loop points and whether looping is enabled.
func (e *Engine) Loop() (a, b int64, on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loopA, e.loopB, e.loopOn
}

// SetTempoScale sets the practice speed multiplier (1.0 = as written,
// 0.5 = half speed). Clamped to [0.25, 2.0]. Takes effect at the next
// render block; pitch is unaffected (synthesis is re-rendered, not
// resampled).
func (e *Engine) SetTempoScale(s float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setScale(s)
	e.publish()
}

// TempoScale returns the current practice speed multiplier.
func (e *Engine) TempoScale() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scale
}

// SetRamp configures the progressive speed trainer.
func (e *Engine) SetRamp(r RampConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ramp = r
}

// SetMetronome toggles the click. Enabling it mid-play takes effect at the
// next beat boundary at or after the current position.
func (e *Engine) SetMetronome(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if on == e.metronome {
		return
	}
	e.metronome = on
	// nextBeat only advances while the click is on, so the current
	// segment's beat schedule may be stale; rebuild it so every beat
	// since the segment anchor is not fired at once.
	e.segValid = false
}

// SetTrackMuted mutes or unmutes a track by index.
func (e *Engine) SetTrackMuted(track int, muted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 0 || track >= len(e.muted) {
		return
	}
	if muted && !e.muted[track] {
		e.voices[track].AllNotesOff()
		for i := 0; i < len(e.active); {
			if e.active[i].track == track {
				last := len(e.active) - 1
				e.active[i] = e.active[last]
				e.active = e.active[:last]
				continue
			}
			i++
		}
	}
	e.muted[track] = muted
}

// SetEventTap registers fn to be called for every scheduled NoteOn as it
// fires, with the exact output frame it sounds on. The tap is the seam the
// Phase 2 scorer hangs off: expected notes arrive frame-stamped on the
// same clock the duplex stream runs on, ready to match against detected
// input notes (plus the calibrated latency offset).
//
// fn runs on the render goroutine with the engine lock held: it must not
// call back into the engine, block, or allocate — copy the event into a
// preallocated ring and get out. Events fire for muted tracks too (the tap
// reports the musical schedule, not what is audible). Pass nil to remove.
func (e *Engine) SetEventTap(fn func(ev score.NoteEvent, outFrame int64)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tap = fn
}

// TotalFrames returns the output frames produced since New — the stream
// clock, advancing even while paused (silence still fills the stream).
func (e *Engine) TotalFrames() int64 { return e.aFrames.Load() }

// TrackMuted reports a track's mute state.
func (e *Engine) TrackMuted(track int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 0 || track >= len(e.muted) {
		return false
	}
	return e.muted[track]
}

// --- State snapshots (cheap; safe from any goroutine) ---

// PosTick returns the current playback position in ticks.
func (e *Engine) PosTick() int64 { return e.aPos.Load() }

// PassCount returns completed loop passes since the loop was set.
func (e *Engine) PassCount() int { return int(e.aPasses.Load()) }

// EffectiveBPM returns the sounding tempo at the current position
// (score tempo × tempo scale).
func (e *Engine) EffectiveBPM() float64 { return math.Float64frombits(e.aBPM.Load()) }

// CountingIn reports whether a count-in is sounding, and if so how many
// click beats remain.
func (e *Engine) CountingIn() (bool, int) {
	return e.aCiOn.Load(), int(e.aCiLeft.Load())
}

// --- Audio ---

// Read implements io.Reader for oto: interleaved stereo float32 LE.
// It never returns an error and always fills p (silence when paused).
func (e *Engine) Read(p []byte) (int, error) {
	w := 0
	for w < len(p) {
		if e.remLen > e.remOff {
			n := copy(p[w:], e.rem[e.remOff:e.remLen])
			e.remOff += n
			w += n
			continue
		}
		e.remOff, e.remLen = 0, 0
		frames := (len(p) - w) / 8
		if frames == 0 {
			// Less than one frame of space left: render one frame
			// into the remainder buffer and copy what fits.
			e.RenderFrames(e.readL[:1], e.readR[:1])
			binary.LittleEndian.PutUint32(e.rem[0:], math.Float32bits(e.readL[0]))
			binary.LittleEndian.PutUint32(e.rem[4:], math.Float32bits(e.readR[0]))
			e.remLen = 8
			continue
		}
		if frames > len(e.readL) {
			frames = len(e.readL)
		}
		e.RenderFrames(e.readL[:frames], e.readR[:frames])
		for i := 0; i < frames; i++ {
			binary.LittleEndian.PutUint32(p[w:], math.Float32bits(e.readL[i]))
			binary.LittleEndian.PutUint32(p[w+4:], math.Float32bits(e.readR[i]))
			w += 8
		}
	}
	return len(p), nil
}

// RenderFrames renders exactly len(left) frames into the given buffers
// (zeroing them first). Offline path: used by `guitartutor render` and by
// tests. Same code path as Read.
func (e *Engine) RenderFrames(left, right []float32) {
	clear(left)
	clear(right)
	e.mu.Lock()
	e.render(left, right)
	e.publish()
	e.mu.Unlock()
}
