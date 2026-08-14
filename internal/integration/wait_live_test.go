package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/S95F/guitarTutor/internal/audio"
	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/live"
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/practice"
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/synth"
)

// waitRiff builds the wait-mode piece: four quarter notes at 120 BPM, the
// first two on the SAME key — a low E immediately followed by another low
// E, so the second wait point arms while the first note's pluck is still
// ringing (the auto-confirm bug's exact shape).
func waitRiff(t *testing.T) *score.Score {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "guitar", Tuning: score.StandardTuning}
	sc.Tracks = append(sc.Tracks, tr)
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0}) // E2, key 40
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0}) // E2 again, key 40
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 3}) // G2, key 43
	b.AddBeat(score.Quarter, score.Note{String: 5, Fret: 0}) // A2, key 45
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

// TestWaitModeLiveLoop drives the ENTIRE wait-mode practice path over the
// fake duplex backend: engine in wait mode rendered in the duplex
// callback, a ~100 ms round trip configured as the calibrated offset, and
// a simulated player whose DI'd guitar — Karplus-Strong plucks of each
// awaited key, a beat into each wait — is the capture stream. The wiring
// below is the -listen analysis callback (cmd/guitartutor) minus the
// window.
//
// Asserted end to end:
//   - every wait releases only after its pluck (the generation advances
//     pluck by pluck, never on its own);
//   - the same-key consecutive wait does NOT auto-release from the
//     previous, still-ringing note;
//   - the final stats carry zero timing-caused misses — wait-confirmed
//     notes are judged pitch-only (Hit, Matched, ErrFrames 0).
func TestWaitModeLiveLoop(t *testing.T) {
	sc := waitRiff(t)
	const (
		sr     = 48000
		period = 480
		delay  = 4800   // injected round trip: 100 ms, like a real interface
		beat   = sr / 2 // one quarter at 120 BPM
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

	// The -listen wiring (cmd/guitartutor newOnNotes): the wait is
	// tracked by the generation WaitingOn returns with its events, the
	// gate arms with a fresh-attack floor a grace before the arm, and the
	// confirming detections are recorded with WaitConfirmed BEFORE
	// ConfirmWait so the released events get a pitch-only verdict. State
	// is owned by the analysis goroutine.
	//
	// Keep this a faithful copy of newOnNotes. It was not one: it read the
	// events and the generation in two calls, the same race the real
	// wiring had, so this end-to-end test could not have caught it (bug
	// review W1). WaitingOn now returns both together, which is what makes
	// the copy correct by construction rather than by vigilance.
	var (
		armedGen    uint64
		armedMin    int64
		armedEvents []score.NoteEvent
		offerBuf    []pitch.Note
		confirmBuf  []pitch.Note
	)
	onNotes := func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {
		scorer.Detected(closed)
		scorer.Advance(consumed - 4*sr) // same lag as -listen; see advanceLagFrames

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

	// The capture side is the player's DI'd guitar — a persistent KS
	// voice — while the engine's playback goes to the output side only
	// (headphones on the interface, the setup `guitartutor devices`
	// recommends). The ~100 ms round trip is the scorer's configured
	// offset above. The DI keeps the monophonic detector honest: what
	// rings across a wait is the player's own previous note, the exact
	// signal the auto-confirm bug fed on.
	player := synth.NewPluck(sr, 0)
	in := make([]float32, period)
	outL := make([]float32, period)
	outR := make([]float32, period)
	pl := make([]float32, period)
	pr := make([]float32, period)
	pos := 0

	// drive advances the audio clock. Pacing is TIGHT (two periods, not
	// the second live_test allows): the no-auto-release assertions below
	// need the analysis clock right behind the device clock, or a wait's
	// arm point would trail reality and stale notes would look fresh.
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

	// waitFor drives audio until cond holds, then lets the asynchronous
	// analysis finish chewing what was driven before declaring failure.
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

	// pluck plays the player's note. Plucking damps the still-ringing
	// string first, as a real attack on the same string does — the ring
	// itself persists across the preceding wait's arm, which is what the
	// no-auto-release assertions feed on.
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

		// Hold for a beat before playing: nothing already in the air
		// may release the wait — for wait 2 that is the STILL-RINGING
		// first E2 pluck, an octave-exact match for this very key.
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

	// Render out the final note and let the analysis drain.
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
