package integration

import (
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/audio"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/live"
	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/practice"
	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/S95F/musicTutor/internal/synth"
)

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

func TestLiveLoopScoresLoopback(t *testing.T) {
	sc, err := textfmt.ParseFile(testdata(t, "fixture_riff.gtab"))
	if err != nil {
		t.Fatal(err)
	}
	const (
		sr     = 48000
		period = 480
		delay  = 4800
	)
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})

	scorer := practice.NewScorer(practice.Config{
		SampleRate:          sr,
		Track:               0,
		LatencyOffsetFrames: delay,
	})
	eng.SetEventTap(scorer.ExpectNote)

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

	delayLine := make([]float32, delay+20*sr)
	in := make([]float32, period)
	outL := make([]float32, period)
	outR := make([]float32, period)
	pos := 0
	totalFrames := 20 * sr
	for pos < totalFrames {

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

	if acc := stats.Accuracy(); acc < 0.95 {
		t.Errorf("accuracy = %.3f, want >= 0.95 (stats %+v)", acc, stats)
	}
	if stats.Hit < 15 {
		t.Errorf("hits = %d, want >= 15 (stats %+v)", stats.Hit, stats)
	}
}
