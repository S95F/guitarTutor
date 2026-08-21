package engine

import (
	"math"
	"sync"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
)

func TestPosMatchesPosTick(t *testing.T) {
	var reg []*stubVoice
	e := New(fixtureScore(t), Options{Voices: newStubFactory(&reg)})
	l, r := make([]float32, 1024), make([]float32, 1024)
	e.Play()
	for i := 0; i < 20; i++ {
		e.RenderFrames(l, r)
		p := e.Pos()
		if got, want := int64(math.Floor(p.Tick)), e.PosTick(); got != want {
			t.Fatalf("block %d: floor(Pos().Tick) = %d, PosTick() = %d", i, got, want)
		}
	}
}

func TestPosRateIsTheSoundingTempo(t *testing.T) {
	var reg []*stubVoice
	e := New(fixtureScore(t), Options{Voices: newStubFactory(&reg)})
	l, r := make([]float32, 1024), make([]float32, 1024)

	for _, scale := range []float64{1.0, 0.5, 1.75} {
		e.SetTempoScale(scale)
		e.Play()
		e.RenderFrames(l, r)

		want := 2 * float64(score.PPQ) * scale
		if got := e.Pos().TicksPerSecond; math.Abs(got-want) > 1e-6 {
			t.Errorf("at scale %.2f the rate is %.3f ticks/s, want %.3f", scale, got, want)
		}
	}
}

func TestPosAdvancingTracksTheFrozenStates(t *testing.T) {
	l, r := make([]float32, 1024), make([]float32, 1024)

	t.Run("stopped", func(t *testing.T) {
		var reg []*stubVoice
		e := New(fixtureScore(t), Options{Voices: newStubFactory(&reg)})
		if e.Pos().Advancing {
			t.Error("a transport that has never played reports itself advancing")
		}
	})

	t.Run("playing then paused", func(t *testing.T) {
		var reg []*stubVoice
		e := New(fixtureScore(t), Options{Voices: newStubFactory(&reg)})
		e.Play()
		e.RenderFrames(l, r)
		if !e.Pos().Advancing {
			t.Error("a playing transport does not report itself advancing")
		}
		e.Pause()
		if e.Pos().Advancing {
			t.Error("a paused transport reports itself advancing")
		}
	})

	t.Run("count-in", func(t *testing.T) {
		var reg []*stubVoice
		e := New(fixtureScore(t), Options{Voices: newStubFactory(&reg), CountInBeats: 4})
		e.Play()
		e.RenderFrames(l, r)
		if p := e.Pos(); p.Advancing {
			t.Error("the position reports itself advancing during a count-in")
		}
	})

	t.Run("waiting", func(t *testing.T) {
		sc := fixtureScore(t)
		sc.Tracks[0].Role = score.RoleUser
		var reg []*stubVoice
		e := New(sc, Options{Voices: newStubFactory(&reg)})
		e.SetWaitMode(true)
		e.Play()
		e.RenderFrames(l, r)
		if !e.Waiting() {
			t.Fatal("wait mode did not engage at the first user note")
		}
		if e.Pos().Advancing {
			t.Error("the position reports itself advancing while halted at a wait point")
		}
	})
}

func TestPosSeekIsADiscontinuity(t *testing.T) {
	var reg []*stubVoice
	e := New(fixtureScore(t), Options{Voices: newStubFactory(&reg)})
	l, r := make([]float32, 1024), make([]float32, 1024)
	e.Play()
	e.RenderFrames(l, r)

	before := e.Pos().Discontinuity
	e.SeekTick(3840)
	after := e.Pos()
	if after.Discontinuity == before {
		t.Error("a seek did not show up as a discontinuity in the snapshot")
	}
	if after.Discontinuity != e.DiscontinuityFrame() {
		t.Errorf("the snapshot's discontinuity %d disagrees with DiscontinuityFrame() %d",
			after.Discontinuity, e.DiscontinuityFrame())
	}
	if int64(math.Floor(after.Tick)) != 3840 {
		t.Errorf("the snapshot's tick after the seek is %.1f, want 3840", after.Tick)
	}
}

func TestPosUnderConcurrentPublishes(t *testing.T) {
	var reg []*stubVoice
	e := New(fixtureScore(t), Options{Voices: newStubFactory(&reg)})
	e.Play()

	rates := map[float64]bool{}
	for _, s := range []float64{1.0, 0.5, 1.5} {
		rates[2*float64(score.PPQ)*s] = true
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		l, r := make([]float32, 256), make([]float32, 256)
		for {
			select {
			case <-done:
				return
			default:
			}
			e.RenderFrames(l, r)
			e.SetTempoScale(0.5)
			e.SeekTick(3840)
			e.SetTempoScale(1.5)
			e.SeekTick(0)
		}
	}()

	for i := 0; i < 200000; i++ {
		p := e.Pos()
		bad := ""
		switch {
		case !rates[p.TicksPerSecond]:
			bad = "rate no publish ever wrote"
		case math.IsNaN(p.Tick) || p.Tick < 0 || p.Tick > float64(e.scoreEnd):
			bad = "tick outside the piece"
		case p.Discontinuity < 0:
			bad = "negative discontinuity frame"
		}
		if bad != "" {
			close(done)
			wg.Wait()
			t.Fatalf("read a %s: %+v", bad, p)
		}
	}
	close(done)
	wg.Wait()
}
