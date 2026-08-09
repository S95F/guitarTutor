package integration

import (
	"testing"
	"time"

	"github.com/S95F/guitarTutor/internal/audio"
	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/live"
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/practice"
	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/S95F/guitarTutor/internal/synth"
)

// fakeBackend is an in-process audio.Backend whose stream the test drives
// by hand: no hardware, no clock — every period is a direct handler call.
type fakeBackend struct {
	handler audio.DuplexHandler
	cfg     audio.StreamConfig
}

func (b *fakeBackend) Name() string { return "fake" }
func (b *fakeBackend) Devices() (capture, playback []audio.DeviceInfo, err error) {
	return nil, nil, nil
}
func (b *fakeBackend) OpenDuplex(cfg audio.StreamConfig, h audio.DuplexHandler) (audio.Stream, error) {
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 48000
	}
	if cfg.PeriodFrames == 0 {
		cfg.PeriodFrames = 480
	}
	b.handler = h
	b.cfg = cfg
	return (*fakeStream)(b), nil
}

type fakeStream fakeBackend

func (s *fakeStream) Start() error               { return nil }
func (s *fakeStream) Stop() error                { return nil }
func (s *fakeStream) Close() error               { return nil }
func (s *fakeStream) Config() audio.StreamConfig { return s.cfg }

// TestLiveLoopScoresLoopback drives the ENTIRE live practice path with the
// engine's own output looped back as the "guitar": fixture riff -> engine +
// KS synth rendered in the duplex callback -> delayed mono loopback into
// the capture side -> live.Session's detector/tracker -> scorer with the
// delay as its calibrated offset. This is the -listen wiring minus the
// window, and it must score high: the player is literally the synth.
func TestLiveLoopScoresLoopback(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		sr     = 48000
		period = 480
		delay  = 4800 // injected round-trip: 100 ms, like a real interface
	)
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})

	scorer := practice.NewScorer(practice.Config{
		SampleRate:          sr,
		Track:               0,
		LatencyOffsetFrames: delay,
	})
	eng.SetEventTap(scorer.ExpectNote)

	// The advance lag must exceed the longest sustained note plus its
	// decay: the tracker reports a note only when it closes, so the
	// fixture's 2 s whole note arrives ~2 s "late" (same constant as the
	// -listen wiring; see cmd/guitartutor advanceLagFrames).
	onNotes := func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {
		scorer.Detected(closed)
		scorer.Advance(consumed - 4*sr)
	}

	backend := &fakeBackend{}
	session, err := live.Start(live.Config{
		Backend: backend,
		Engine:  eng,
		Stream:  audio.StreamConfig{SampleRate: sr, PeriodFrames: period},
		OnNotes: onNotes,
		// Phase 4: strums carry the chroma that verifies chords. Wired
		// exactly as cmd/guitartutor does, so this test fails if that
		// wiring is ever dropped — without it the bar-3 chord silently
		// reverts to one hit and two misses.
		OnStrums: func(sts []pitch.Strum) {
			for _, st := range sts {
				scorer.DetectedStrum(st)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Stop()

	eng.Play()

	// Drive the callback: capture = the mixed output of `delay` frames
	// ago (a software loopback). Pace against the analysis goroutine so
	// the ring never overflows — dropped samples would corrupt scoring.
	delayLine := make([]float32, delay+20*sr) // delay + whole piece + slack
	in := make([]float32, period)
	outL := make([]float32, period)
	outR := make([]float32, period)
	pos := 0
	totalFrames := 20 * sr // 8 s piece + decay + advance-lag headroom
	for pos < totalFrames {
		// Real backpressure, not blind sleeps: under full-suite CPU
		// load the analysis goroutine can lag far behind a driver
		// running flat out, and ring overflow would corrupt scoring.
		for session.Backlog() > sr {
			time.Sleep(time.Millisecond)
		}
		copy(in, delayLine[pos:pos+period])
		backend.handler(in, outL, outR)
		for i := 0; i < period; i++ {
			delayLine[pos+delay+i] = 0.5 * (outL[i] + outR[i])
		}
		pos += period
	}

	// The analysis goroutine drains asynchronously; wait for all 16
	// expected notes to be judged.
	deadline := time.Now().Add(10 * time.Second)
	var stats practice.Stats
	for {
		stats = scorer.Stats()
		if stats.Hit+stats.Close+stats.Miss >= 16 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if dropped := session.DroppedSamples(); dropped != 0 {
		t.Errorf("ring dropped %d samples; pacing failed and scoring is unreliable", dropped)
	}
	total := stats.Hit + stats.Close + stats.Miss
	if total != 16 {
		t.Fatalf("judged %d notes, want 16 (stats %+v)", total, stats)
	}
	// Phase 4 floor. Before chord verification the strummed chord cost
	// three misses and capped this at 0.8125, so anything at or above
	// 0.95 is unreachable without strums actually reaching the scorer
	// through the live session — which is the point of asserting it here
	// rather than only in the scorer's own round trip.
	if acc := stats.Accuracy(); acc < 0.95 {
		t.Errorf("accuracy = %.3f, want >= 0.95 (stats %+v)", acc, stats)
	}
	if stats.Hit < 15 {
		t.Errorf("hits = %d, want >= 12 (stats %+v)", stats.Hit, stats)
	}
}
