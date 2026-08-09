// Package live glues a duplex audio stream to the practice engine and the
// pitch pipeline: one audio callback pulls playback frames from the engine
// and pushes the captured guitar signal into a lock-free ring that an
// analysis goroutine drains through the detector and tracker.
//
// Clock domains (docs/DECISIONS.md D1): input and output share the duplex
// stream's frame counter, so a note detected at input frame N aligns with
// the engine's output clock at N minus the calibrated round-trip offset.
// The engine's SetEventTap reports expected notes in output frames; the
// scorer subtracts the offset to compare.
package live

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/S95F/guitarTutor/internal/audio"
	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/pitch"
)

// Config assembles a live session.
type Config struct {
	// Backend is the duplex backend (audio.Available()); required.
	Backend audio.Backend
	// Engine renders playback; required.
	Engine *engine.Engine
	// Stream requests device/latency parameters (zero values default).
	Stream audio.StreamConfig
	// Pitch parameterizes detection; zero Window/Hop take defaults for
	// the negotiated sample rate.
	Pitch pitch.Config
	// OnNotes, when set, receives closed notes and the current sounding
	// note after each analysis batch. Called on the analysis goroutine.
	OnNotes func(closed []pitch.Note, current pitch.Note, sounding bool)
}

// A Session is a running duplex practice session.
type Session struct {
	stream  audio.Stream
	eng     *engine.Engine
	ringBuf *ring
	stop    chan struct{}
	done    chan struct{}

	// level is the smoothed input RMS in dBFS, published for UI meters.
	level atomic.Uint64
}

// Start opens the duplex stream and begins analysis. The engine's output
// is rendered inside the audio callback; capture flows through the ring
// into the detector on a separate goroutine.
func Start(cfg Config) (*Session, error) {
	if cfg.Backend == nil {
		return nil, fmt.Errorf("live: no audio backend (built without cgo?)")
	}
	if cfg.Engine == nil {
		return nil, fmt.Errorf("live: nil engine")
	}

	s := &Session{
		eng:     cfg.Engine,
		ringBuf: newRing(4 * 48000), // ~4 s of mono capture headroom
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	kick := make(chan struct{}, 1)

	handler := func(in, outL, outR []float32) {
		// Playback: the engine renders directly into the device buffer.
		s.eng.RenderFrames(outL, outR)
		// Capture: memcpy into the ring, then a non-blocking nudge.
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
		pcfg = pitch.DefaultConfig(neg.SampleRate)
	}

	go s.analyze(pcfg, kick, cfg.OnNotes)

	if err := stream.Start(); err != nil {
		close(s.stop)
		<-s.done
		stream.Close()
		return nil, fmt.Errorf("live: starting stream: %w", err)
	}
	return s, nil
}

// analyze drains the ring through the detector/tracker until Stop.
func (s *Session) analyze(cfg pitch.Config, kick <-chan struct{}, onNotes func([]pitch.Note, pitch.Note, bool)) {
	defer close(s.done)
	det := pitch.NewDetector(cfg)
	trk := pitch.NewTracker(cfg)
	chunk := make([]float32, 4096)
	for {
		select {
		case <-s.stop:
			return
		case <-kick:
		}
		for {
			n, _ := s.ringBuf.read(chunk)
			if n == 0 {
				break
			}
			s.publishLevel(chunk[:n])
			frames := det.Process(chunk[:n])
			closed := trk.Feed(frames)
			if onNotes != nil {
				cur, ok := trk.Current()
				onNotes(closed, cur, ok)
			}
		}
	}
}

// publishLevel updates the smoothed input level for UI meters.
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

// InputLevel reports the smoothed capture level in dBFS (~-120 quiet).
func (s *Session) InputLevel() float64 {
	v := math.Float64frombits(s.level.Load())
	if v == 0 {
		return -120
	}
	return v
}

// DroppedSamples reports capture samples lost to analysis backpressure.
func (s *Session) DroppedSamples() int64 { return s.ringBuf.Dropped() }

// Config reports the stream's negotiated parameters.
func (s *Session) Config() audio.StreamConfig { return s.stream.Config() }

// Stop stops and closes the stream and the analysis goroutine.
func (s *Session) Stop() {
	s.stream.Stop()
	close(s.stop)
	<-s.done
	s.stream.Close()
}
