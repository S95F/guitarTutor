package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/audio"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/live"
	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/practice"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

func waitRiff(t *testing.T) *score.Score {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "guitar", Tuning: score.StandardTuning}
	sc.Tracks = append(sc.Tracks, tr)
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3})
	b.AddBeat(score.Quarter, score.Note{String: 5, Fret: 0})
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

func TestWaitModeLiveLoop(t *testing.T) {
	sc := waitRiff(t)
	const (
		sr     = 48000
		period = 480
		delay  = 4800
		beat   = sr / 2
		grace  = 15 * sr / 100
	)
	eng := engine.New(sc, engine.Options{SampleRate: sr, Voices: synth.NewPluck})
	eng.SetWaitMode(true)

	pcfg := practice.Config{
		SampleRate:          sr,
		Track:               0,
		LatencyOffsetFrames: delay,
	}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)

	var (
		armedGen    uint64
		armedMin    int64
		armedEvents []score.NoteEvent
		offerBuf    []pitch.Note
		confirmBuf  []pitch.Note
	)
	onNotes := func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {
		scorer.Detected(closed)
		scorer.Advance(consumed - 4*sr)

		evs, gen, waiting := eng.WaitingOn()
		if !waiting {
			return
		}
		if gen != armedGen {
			armedGen = gen
			armedMin = consumed - grace
			armedEvents = append(armedEvents[:0], evs...)
			confirmBuf = confirmBuf[:0]
			gate.Arm(evs, armedMin)
		}
		offerBuf = append(offerBuf[:0], closed...)
		if sounding {
			offerBuf = append(offerBuf, current)
		}
		for _, n := range offerBuf {
			if n.Start < armedMin {
				continue
			}
			merged := false
			for i := range confirmBuf {
				if confirmBuf[i].Start == n.Start && confirmBuf[i].Key == n.Key {
					confirmBuf[i] = n
					merged = true
					break
				}
			}
			if !merged {
				confirmBuf = append(confirmBuf, n)
			}
		}
		if len(offerBuf) > 0 && gate.Offer(offerBuf) {
			scorer.WaitConfirmed(armedEvents, confirmBuf)
			eng.ConfirmWait()
		}
	}

	backend := &fakeBackend{}
	session, err := live.Start(live.Config{
		Backend: backend,
		Engine:  eng,
		Stream:  audio.StreamConfig{SampleRate: sr, PeriodFrames: period},
		OnNotes: onNotes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Stop()

	eng.Play()

	player := synth.NewPluck(sr, 0)
	in := make([]float32, period)
	outL := make([]float32, period)
	outR := make([]float32, period)
	pl := make([]float32, period)
	pr := make([]float32, period)
	pos := 0

	drive := func(frames int) {
		t.Helper()
		for f := 0; f < frames; f += period {
			for session.Backlog() > 2*period {
				time.Sleep(time.Millisecond)
			}
			for i := range pl {
				pl[i], pr[i] = 0, 0
			}
			player.Render(pl, pr)
			for i := 0; i < period; i++ {
				in[i] = 0.5 * (pl[i] + pr[i])
			}
			backend.handler(in, outL, outR)
			pos += period
		}
	}

	waitFor := func(what string, budgetFrames int, cond func() bool) {
		t.Helper()
		for f := 0; f < budgetFrames; f += period {
			if cond() {
				return
			}
			drive(period)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s (pos %d, waiting %v, generation %d)",
			what, pos, eng.Waiting(), eng.WaitGeneration())
	}

	pluck := func(key int) {
		player.AllNotesOff()
		player.NoteOn(key, 0.9)
	}

	keys := []int{40, 40, 43, 45}
	for i, key := range keys {
		gen := uint64(i + 1)
		waitFor(fmt.Sprintf("wait %d (key %d) to engage", i+1, key), 6*sr, func() bool {
			return eng.Waiting() && eng.WaitGeneration() == gen
		})

		drive(beat)
		if !eng.Waiting() || eng.WaitGeneration() != gen {
			t.Fatalf("wait %d (key %d) released before its pluck (waiting %v, generation %d)",
				i+1, key, eng.Waiting(), eng.WaitGeneration())
		}

		pluck(key)
		waitFor(fmt.Sprintf("wait %d (key %d) to release after its pluck", i+1, key), 6*sr, func() bool {
			return !eng.Waiting() || eng.WaitGeneration() > gen
		})
	}

	drive(2 * sr)

	if dropped := session.DroppedSamples(); dropped != 0 {
		t.Errorf("ring dropped %d samples; pacing failed and scoring is unreliable", dropped)
	}
	stats := scorer.Stats()
	if total := stats.Hit + stats.Close + stats.Miss; total != 4 {
		t.Fatalf("judged %d notes, want 4 (stats %+v)", total, stats)
	}
	if stats.Miss != 0 {
		t.Errorf("stats %+v: wait-confirmed practice produced misses, want 0", stats)
	}
	if stats.Hit != 4 {
		t.Errorf("stats %+v: want 4 hits — wait-confirmed notes are pitch-only hits", stats)
	}
	for _, r := range scorer.Results(nil) {
		if !r.Matched || r.ErrFrames != 0 || r.Verdict != practice.VerdictHit {
			t.Errorf("result %+v: wait-confirmed note must be a pitch-only hit with no timing error", r)
		}
	}
}
