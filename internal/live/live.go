// Package live glues a duplex audio stream to the practice engine and the
// pitch pipeline: one audio callback pulls playback frames from the engine
// and pushes the captured instrument signal into a lock-free ring that an
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
	// Pitch parameterizes detection; zero fields take defaults for the
	// negotiated sample rate. Setting OnStrums forces Pitch.Strums on.
	Pitch pitch.Config
	// OnNotes, when set, receives closed notes and the current sounding
	// note after each analysis batch, plus the total capture frames
	// consumed so far (the input-stream clock — what Scorer.Advance
	// wants, minus a lag; see internal/practice.Advance's doc).
	// Capture lost to ring overflow is counted too — the gap is fed
	// through as silence — so consumed never drifts from the device
	// clock. Called on the analysis goroutine.
	OnNotes func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64)
	// OnStrums, when set, receives the strums (attack plus the chroma of
	// the audio that followed it) completed by each analysis batch —
	// what internal/practice.Scorer.DetectedStrum verifies chords and
	// palm mutes from (ROADMAP Phase 4, docs/DECISIONS.md D4/D5).
	// Setting it turns Pitch.Strums on, so a caller that wants chord
	// scoring only has to supply the callback.
	//
	// A parallel callback rather than more parameters on OnNotes: the
	// two carry different clocks (a Strum is stamped at its ONSET, a
	// Note only when the tracker CLOSES it, seconds later for a ringing
	// note), most call sites want only notes, and every existing one
	// keeps compiling unchanged.
	//
	// Within a batch OnStrums runs BEFORE OnNotes, and the slice is only
	// valid for the call — the detector reuses it. The order matters:
	// OnNotes carries the consumed clock that wiring drives
	// Scorer.Advance from, and Advance is what ages expectations into
	// verdicts, so a strum from the same batch must be in the scorer's
	// hands before that happens. Called on the analysis goroutine.
	OnStrums func(strums []pitch.Strum)
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
		// Only the rate is filled in from the negotiated stream;
		// pitch.Config.withDefaults supplies the rest, so a caller who
		// set (say) Strums or Estimator but left SampleRate at 0 keeps
		// those fields instead of having the whole struct replaced.
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

// analyze drains the ring through the detector/tracker until Stop.
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

// gapRec marks a stretch of capture the ring overflowed away: n samples
// were lost at accepted-stream position at (between accepted sample at-1
// and accepted sample at).
type gapRec struct{ at, n int64 }

// analyzer is the drain-loop state behind Session.analyze, split out so
// tests can drive it synchronously against a hand-fed ring.
type analyzer struct {
	s        *Session
	det      *pitch.Detector
	trk      *pitch.Tracker
	onNotes  func([]pitch.Note, pitch.Note, bool, int64)
	onStrums func([]pitch.Strum)

	chunk []float32
	zeros []float32
	// consumed is the device-clock position of the capture stream:
	// every sample fed to the detector, with overflow gaps (fed as
	// zeros) counted like any other stretch.
	consumed    int64
	lastDropped int64
	gaps        []gapRec // pending overflow gaps, in stream order
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

// drain empties the ring through the detector/tracker, splicing a zeroed
// stretch into the stream wherever the ring overflowed. Without the
// splice a drop would advance the device clock but not the detector's,
// and every detection after a stall would be stamped early by the
// cumulative loss — silently mis-scored until restart. Zeros keep the
// two clocks locked, and the tracker's unvoiced hysteresis closes
// whatever was sounding when the overflow cut in.
func (a *analyzer) drain() {
	for {
		// Check for overflow BEFORE reading. The producer publishes the
		// loss position (dropPos) before the count (Dropped), so reading
		// count then position yields the fill point of the loss just
		// observed — even when this loop notices a drop an iteration
		// late, after its own reads freed space and the write cursor
		// moved past the loss. The cursor itself was the old source of
		// the position, and it lied in exactly that case (audit D5).
		if d := a.s.ringBuf.Dropped(); d != a.lastDropped {
			a.gaps = append(a.gaps, gapRec{at: a.s.ringBuf.dropPos(), n: d - a.lastDropped})
			a.lastDropped = d
		}
		n, start := a.s.ringBuf.read(a.chunk)
		if n == 0 {
			return
		}
		buf := a.chunk[:n]
		// Reads walk the accepted stream contiguously, so a pending
		// gap always lies at or past buf's start; splice it in the
		// moment the cursor reaches it.
		for len(a.gaps) > 0 && a.gaps[0].at <= start+int64(len(buf)) {
			g := a.gaps[0]
			a.gaps = a.gaps[1:]
			cut := g.at - start
			if cut < 0 {
				// A drop noticed so late that its position precedes
				// capture already processed: splice at the earliest
				// point still possible rather than slicing negatively.
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

// process feeds one contiguous stretch to the detector and tracker and
// reports the results. synthetic marks overflow-gap filler, which never
// reaches the level meter (the meter shows what the device captured, not
// what the ring lost).
//
// Strums are drained from the detector immediately after Process (the
// slice it returns is reused on the next call) and delivered before the
// notes, per Config.OnStrums.
//
// The zeroed splice cannot manufacture a strum, because it cannot
// manufacture an ONSET: the detector fires one only when a hop's RMS
// exceeds max(smoothed level, noise floor) times the jump ratio AND rises
// above the previous hop, and a zeroed hop has an RMS of exactly 0 while
// the noise-floor clamp keeps the comparison level strictly positive. So
// filler is delivered here on the same terms as real capture and needs no
// suppression. Two consequences worth naming: a strum whose accumulation
// window was still open when the ring overflowed is completed over the
// filler and still reported — it is genuine evidence about the audio
// before the gap, only with a truncated tail — and the attack when real
// capture resumes fires an onset exactly as it would have after real
// silence, which is what the player played.
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

// feedGap runs n zeroed samples through the pipeline in chunk-sized
// slices, standing in for capture the ring overflowed away.
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

// Backlog reports capture samples buffered but not yet analyzed — a
// backpressure signal (the ring drops new samples when it fills).
func (s *Session) Backlog() int64 { return s.ringBuf.buffered() }

// Config reports the stream's negotiated parameters.
func (s *Session) Config() audio.StreamConfig { return s.stream.Config() }

// Stop stops and closes the stream and the analysis goroutine.
func (s *Session) Stop() {
	s.stream.Stop()
	close(s.stop)
	<-s.done
	s.stream.Close()
}
