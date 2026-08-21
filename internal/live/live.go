package live

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/S95F/musicTutor/internal/audio"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/pitch"
)

type Config struct {
	Backend audio.Backend

	Engine *engine.Engine

	Stream audio.StreamConfig

	Pitch pitch.Config

	OnNotes func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64)

	OnStrums func(strums []pitch.Strum)
}

type Session struct {
	stream  audio.Stream
	eng     *engine.Engine
	ringBuf *ring
	stop    chan struct{}
	done    chan struct{}

	level atomic.Uint64
}

func Start(cfg Config) (*Session, error) {
	if cfg.Backend == nil {
		return nil, fmt.Errorf("live: no audio backend (built without cgo?)")
	}
	if cfg.Engine == nil {
		return nil, fmt.Errorf("live: nil engine")
	}

	s := &Session{
		eng:     cfg.Engine,
		ringBuf: newRing(4 * 48000),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	kick := make(chan struct{}, 1)

	handler := func(in, outL, outR []float32) {

		s.eng.RenderFrames(outL, outR)

		s.ringBuf.write(in)
		select {
		case kick <- struct{}{}:
		default:
		}
	}

	stream, err := cfg.Backend.OpenDuplex(cfg.Stream, handler)
	if err != nil {
		return nil, fmt.Errorf("live: opening duplex stream: %w", err)
	}
	s.stream = stream

	neg := stream.Config()
	pcfg := cfg.Pitch
	if pcfg.SampleRate == 0 {

		pcfg.SampleRate = neg.SampleRate
	}
	if cfg.OnStrums != nil {
		pcfg.Strums = true
	}

	go s.analyze(pcfg, kick, cfg.OnNotes, cfg.OnStrums)

	if err := stream.Start(); err != nil {
		close(s.stop)
		<-s.done
		stream.Close()
		return nil, fmt.Errorf("live: starting stream: %w", err)
	}
	return s, nil
}

func (s *Session) analyze(cfg pitch.Config, kick <-chan struct{}, onNotes func([]pitch.Note, pitch.Note, bool, int64), onStrums func([]pitch.Strum)) {
	defer close(s.done)
	a := newAnalyzer(s, cfg, onNotes, onStrums)
	for {
		select {
		case <-s.stop:
			return
		case <-kick:
		}
		a.drain()
	}
}

type gapRec struct{ at, n int64 }

type analyzer struct {
	s        *Session
	det      *pitch.Detector
	trk      *pitch.Tracker
	onNotes  func([]pitch.Note, pitch.Note, bool, int64)
	onStrums func([]pitch.Strum)

	chunk []float32
	zeros []float32

	consumed    int64
	lastDropped int64
	gaps        []gapRec
}

func newAnalyzer(s *Session, cfg pitch.Config, onNotes func([]pitch.Note, pitch.Note, bool, int64), onStrums func([]pitch.Strum)) *analyzer {
	return &analyzer{
		s:        s,
		det:      pitch.NewDetector(cfg),
		trk:      pitch.NewTracker(cfg),
		onNotes:  onNotes,
		onStrums: onStrums,
		chunk:    make([]float32, 4096),
		zeros:    make([]float32, 4096),
	}
}

func (a *analyzer) drain() {
	for {

		if d := a.s.ringBuf.Dropped(); d != a.lastDropped {
			a.gaps = append(a.gaps, gapRec{at: a.s.ringBuf.dropPos(), n: d - a.lastDropped})
			a.lastDropped = d
		}
		n, start := a.s.ringBuf.read(a.chunk)
		if n == 0 {
			return
		}
		buf := a.chunk[:n]

		for len(a.gaps) > 0 && a.gaps[0].at <= start+int64(len(buf)) {
			g := a.gaps[0]
			a.gaps = a.gaps[1:]
			cut := g.at - start
			if cut < 0 {

				cut = 0
			}
			a.process(buf[:cut], false)
			buf = buf[cut:]
			start += cut
			a.feedGap(g.n)
		}
		a.process(buf, false)
	}
}

func (a *analyzer) process(buf []float32, synthetic bool) {
	if len(buf) == 0 {
		return
	}
	a.consumed += int64(len(buf))
	if !synthetic {
		a.s.publishLevel(buf)
	}
	frames := a.det.Process(buf)
	strums := a.det.Strums()
	closed := a.trk.Feed(frames)
	if a.onStrums != nil && len(strums) > 0 {
		a.onStrums(strums)
	}
	if a.onNotes != nil {
		cur, ok := a.trk.Current()
		a.onNotes(closed, cur, ok, a.consumed)
	}
}

func (a *analyzer) feedGap(n int64) {
	for n > 0 {
		m := int64(len(a.zeros))
		if m > n {
			m = n
		}
		a.process(a.zeros[:m], true)
		n -= m
	}
}

func (s *Session) publishLevel(samples []float32) {
	var sum float64
	for _, v := range samples {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	db := -120.0
	if rms > 0 {
		db = 20 * math.Log10(rms)
	}
	prev := math.Float64frombits(s.level.Load())
	if prev == 0 {
		prev = -120
	}
	s.level.Store(math.Float64bits(prev*0.7 + db*0.3))
}

func (s *Session) InputLevel() float64 {
	v := math.Float64frombits(s.level.Load())
	if v == 0 {
		return -120
	}
	return v
}

func (s *Session) DroppedSamples() int64 { return s.ringBuf.Dropped() }

func (s *Session) Backlog() int64 { return s.ringBuf.buffered() }

func (s *Session) Config() audio.StreamConfig { return s.stream.Config() }

func (s *Session) Stop() {
	s.stream.Stop()
	close(s.stop)
	<-s.done
	s.stream.Close()
}
