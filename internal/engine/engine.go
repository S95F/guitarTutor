package engine

import (
	"encoding/binary"
	"math"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

type Options struct {
	SampleRate int

	Voices synth.Factory

	Metronome bool

	CountInBeats int

	CountInEveryPass bool
}

type RampConfig struct {
	Enabled   bool
	Increment float64
	Target    float64
}

const (
	defaultSampleRate = 48000
	minTempoScale     = 0.25
	maxTempoScale     = 2.0
)

const readBlockFrames = 2048

type Engine struct {
	mu sync.Mutex

	sc               *score.Score
	sampleRate       int
	countInBeats     int
	countInEveryPass bool

	events   []score.NoteEvent
	scoreEnd int64

	voices []synth.Voice

	artic []synth.Articulator

	articRep  []synth.ContinuationReporter
	muted     []bool
	userTrack []bool

	playing bool
	pos     float64
	scale   float64
	ramp    RampConfig

	loopA, loopB int64
	loopOn       bool
	passes       int

	metronome bool

	waitMode     bool
	waiting      bool
	waitReleased bool
	waitTick     int64
	waitTrack    int
	waitRelTrack int

	nextEvent int
	active    []activeNote
	absFrame  int64
	tap       func(ev score.NoteEvent, outFrame int64)

	segValid  bool
	anchor    float64
	fpt       float64
	segFrame  int
	segEnd    int
	boundary  int64
	bKind     boundaryKind
	nextBeat  int64
	beatLen   int64
	barLen    int64
	meterBase int64

	backL, backR []float32
	backOffset   float64
	backGain     float64
	backBase     float64

	ciBeatsLeft int
	ciFPB       int
	ciFrameIn   int

	accentBuf, beatBuf []float32
	clicks             [4]clickState

	readL, readR   []float32
	rem            [8]byte
	remOff, remLen int

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

	aPosSeq  atomic.Uint64
	aPosTick atomic.Uint64
	aPosRate atomic.Uint64
	aPosAdv  atomic.Bool
	aPosDisc atomic.Int64
}

type activeNote struct {
	track int
	key   int
	str   int
	end   int64
}

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
		backGain:         1.0,
		waitTrack:        -1,
		waitRelTrack:     -1,
	}
	e.voices = make([]synth.Voice, len(sc.Tracks))
	e.artic = make([]synth.Articulator, len(sc.Tracks))
	e.articRep = make([]synth.ContinuationReporter, len(sc.Tracks))
	e.muted = make([]bool, len(sc.Tracks))
	e.userTrack = make([]bool, len(sc.Tracks))
	for i, tr := range sc.Tracks {
		e.voices[i] = opts.Voices(sr, tr.Program)
		e.artic[i], _ = e.voices[i].(synth.Articulator)
		e.articRep[i], _ = e.voices[i].(synth.ContinuationReporter)
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

func (e *Engine) Playing() bool { return e.aPlaying.Load() }

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

func (e *Engine) ClearLoop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loopOn = false
	e.segValid = false
	e.clearWait()
	e.markDiscontinuity()
	e.publish()
}

func (e *Engine) markDiscontinuity() { e.aDiscont.Store(e.absFrame) }

func (e *Engine) DiscontinuityFrame() int64 { return e.aDiscont.Load() }

func (e *Engine) Loop() (a, b int64, on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loopA, e.loopB, e.loopOn
}

func (e *Engine) SetTempoScale(s float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setScale(s)
	e.publish()
}

func (e *Engine) TempoScale() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scale
}

func (e *Engine) SetRamp(r RampConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ramp = r
}

func (e *Engine) SetMetronome(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if on == e.metronome {
		return
	}
	e.metronome = on

	e.segValid = false
}

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

func (e *Engine) SetEventTap(fn func(ev score.NoteEvent, outFrame int64)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tap = fn
}

func (e *Engine) TotalFrames() int64 { return e.aFrames.Load() }

func (e *Engine) TrackMuted(track int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 0 || track >= len(e.muted) {
		return false
	}
	return e.muted[track]
}

func (e *Engine) PosTick() int64 { return e.aPos.Load() }

type PlayPos struct {
	Tick float64

	TicksPerSecond float64

	Advancing bool

	Discontinuity int64
}

func (e *Engine) Pos() PlayPos {
	for {
		seq := e.aPosSeq.Load()
		if seq&1 != 0 {

			runtime.Gosched()
			continue
		}
		p := PlayPos{
			Tick:           math.Float64frombits(e.aPosTick.Load()),
			TicksPerSecond: math.Float64frombits(e.aPosRate.Load()),
			Advancing:      e.aPosAdv.Load(),
			Discontinuity:  e.aPosDisc.Load(),
		}
		if e.aPosSeq.Load() == seq {
			return p
		}

		runtime.Gosched()
	}
}

func (e *Engine) PassCount() int { return int(e.aPasses.Load()) }

func (e *Engine) EffectiveBPM() float64 { return math.Float64frombits(e.aBPM.Load()) }

func (e *Engine) CountingIn() (bool, int) {
	return e.aCiOn.Load(), int(e.aCiLeft.Load())
}

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

func (e *Engine) RenderFrames(left, right []float32) {
	clear(left)
	clear(right)
	e.mu.Lock()
	e.render(left, right)
	e.publish()
	e.mu.Unlock()
}
