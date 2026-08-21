package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/audio"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/latency"
	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/practice"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/ui"
)

// Fake device lists for the resolution tests. The capture default and the
// playback default carry distinct IDs so a test that resolves the wrong
// list fails loudly.
var (
	testCapture = []audio.DeviceInfo{
		{ID: "cap-usb", Name: "USB Audio Interface"},
		{ID: "cap-mic", Name: "Laptop Microphone", Default: true},
	}
	testPlayback = []audio.DeviceInfo{
		{ID: "pb-usb", Name: "USB Audio Interface"},
		{ID: "pb-spk", Name: "Speakers", Default: true},
	}
)

// TestResolveDeviceDefault is the regression test for finding C3: an empty
// query must resolve to the CONCRETE default device's ID (it used to
// resolve to "", making offsets follow whatever the system default
// happened to be), falling back to "" only when no default is marked.
func TestResolveDeviceDefault(t *testing.T) {
	id, err := resolveDevice(testCapture, "capture", "")
	if err != nil {
		t.Fatalf("resolveDevice(default): %v", err)
	}
	if id != "cap-mic" {
		t.Errorf("empty query resolved to %q, want the concrete default cap-mic", id)
	}

	noDefault := []audio.DeviceInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	id, err = resolveDevice(noDefault, "capture", "")
	if err != nil {
		t.Fatalf("resolveDevice(no default marked): %v", err)
	}
	if id != "" {
		t.Errorf("empty query with no marked default resolved to %q, want \"\"", id)
	}
}

// TestFillDeviceIDs is the regression test for finding C2: calibrate and
// play used to resolve devices asymmetrically (play filled empty flags
// from the config, calibrate took system defaults), so the offset was
// stored under one key and looked up under another. Both paths now share
// fillDeviceIDs: flags win, the config fills gaps, and an empty result
// falls back to the concrete default.
func TestFillDeviceIDs(t *testing.T) {
	remembered := appconfig.Config{CaptureDeviceID: "cap-usb", PlaybackDeviceID: "pb-usb"}
	cases := []struct {
		name        string
		cfg         appconfig.Config
		inQ, outQ   string
		wantIn      string
		wantOut     string
		wantErrPart string
	}{
		{
			name:    "flags win over config",
			cfg:     appconfig.Config{CaptureDeviceID: "cap-mic", PlaybackDeviceID: "pb-spk"},
			inQ:     "usb",
			outQ:    "usb",
			wantIn:  "cap-usb",
			wantOut: "pb-usb",
		},
		{
			name:    "config fills empty flags",
			cfg:     remembered,
			wantIn:  "cap-usb",
			wantOut: "pb-usb",
		},
		{
			name:    "no flags, no config: concrete defaults",
			wantIn:  "cap-mic",
			wantOut: "pb-spk",
		},
		{
			name:    "mixed: flag for capture, config for playback",
			cfg:     remembered,
			inQ:     "microphone",
			wantIn:  "cap-mic",
			wantOut: "pb-usb",
		},
		{
			name:        "no match errors",
			inQ:         "scarlett",
			wantErrPart: "no capture device matches",
		},
		{
			name:        "ambiguous fragment errors",
			outQ:        "s",
			wantErrPart: "be more specific",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, out, _, _, _, err := fillDeviceIDs(testCapture, testPlayback, c.cfg, c.inQ, c.outQ)
			if c.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrPart) {
					t.Fatalf("err = %v, want one containing %q", err, c.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("fillDeviceIDs: %v", err)
			}
			if in != c.wantIn || out != c.wantOut {
				t.Errorf("resolved (%q, %q), want (%q, %q)", in, out, c.wantIn, c.wantOut)
			}
		})
	}
}

// TestCalibratedOffsetLegacyKey is the other half of finding C3: a legacy
// ""|"" config entry (calibrated back when empty queries stayed empty) is
// ambiguous about which physical devices it measured and must read as
// uncalibrated, so the warning path fires and a recalibration stores a
// concrete key.
func TestCalibratedOffsetLegacyKey(t *testing.T) {
	var cfg appconfig.Config
	cfg.SetOffset("", "", 1234, 0.95)
	if off, ok := calibratedOffset(cfg, "", ""); ok {
		t.Errorf("legacy \"\"|\"\" entry read as calibrated (offset %d), want uncalibrated", off)
	}

	cfg.SetOffset("cap-usb", "pb-usb", 777, 0.95)
	off, ok := calibratedOffset(cfg, "cap-usb", "pb-usb")
	if !ok || off != 777 {
		t.Errorf("concrete pair = (%d, %v), want (777, true)", off, ok)
	}
	if _, ok := calibratedOffset(cfg, "cap-usb", "pb-spk"); ok {
		t.Error("uncalibrated concrete pair read as calibrated")
	}
}

// TestCalibrationSpacingHeadroom is the regression test for finding C4:
// the old half-second click spacing had zero headroom over the 500 ms
// search window, so large-but-sane round trips sat at the alias point. A
// realistic worst case (450 ms) must be recovered exactly with the cmd
// constants, and the spacing must keep the full search window clear of
// the next click.
func TestCalibrationSpacingHeadroom(t *testing.T) {
	if calSpacing < sampleRate {
		t.Fatalf("calSpacing = %d frames, want >= 1 s (%d) of alias headroom", calSpacing, sampleRate)
	}
	const delay = 45 * sampleRate / 100 // 450 ms
	train := latency.ClickTrain(sampleRate, calClicks, calSpacing)
	captured := make([]float32, len(train)+sampleRate)
	copy(captured[delay:], train)
	off, conf, err := latency.Estimate(sampleRate, train, captured, calSpacing, calClicks)
	if err != nil {
		t.Fatalf("Estimate(450 ms delay): %v", err)
	}
	if off < delay-2 || off > delay+2 {
		t.Errorf("estimated offset %d, want %d +/- 2", off, delay)
	}
	if conf < 0.9 {
		t.Errorf("confidence %.2f on a clean loopback, want >= 0.9", conf)
	}
}

// resultSink is the test's listenUI: it records offered results.
type resultSink struct {
	results []practice.NoteResult
}

func (s *resultSink) OfferResults(rs []practice.NoteResult) {
	s.results = append(s.results, rs...)
}
func (s *resultSink) OfferTuner(pitch.Note, bool) {}

// TestOnNotesSeekAbandonsPending drives the real -listen callback through
// a seek: notes that sounded before it must not surface as misses.
//
// Scorer.Reset() was never wired to anything, so expectations queued by
// the tap outlived the seek and Advance aged them into VerdictMiss four
// seconds later — a false "you missed" for notes whose answering window a
// keypress cut short (ROADMAP's guiding principles; D5).
func TestOnNotesSeekAbandonsPending(t *testing.T) {
	sc := waitFixtureScore(t)
	eng := newEngine(sc, engine.Options{})

	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	sink := &resultSink{}
	onNotes := newLiveWiring(eng, sink, scorer, gate, pcfg).onNotes

	// Play far enough that the first notes have sounded and been tapped.
	eng.Play()
	l := make([]float32, 480)
	r := make([]float32, 480)
	for i := 0; i < 100; i++ { // 48000 frames = 1 s = two quarter notes
		eng.RenderFrames(l, r)
	}
	consumed := int64(sampleRate)
	onNotes(nil, pitch.Note{}, false, consumed)
	if len(sink.results) != 0 {
		t.Fatalf("results before anything expired: %+v", sink.results)
	}

	// The user jumps back to the top, answering nothing.
	eng.SeekTick(0)

	// Well past every pending deadline: the callback must abandon them
	// rather than let Advance score them.
	consumed += 10 * sampleRate
	onNotes(nil, pitch.Note{}, false, consumed)

	for _, r := range sink.results {
		if r.Verdict == practice.VerdictMiss {
			t.Errorf("seek produced a spurious miss: %+v", r)
		}
	}
	if st := scorer.Stats(); st.Miss != 0 {
		t.Errorf("stats after a seek = %+v, want no invented misses", st)
	}
}

// TestMergeNote: a still-sounding note re-offered each batch must replace
// its earlier snapshot, not pile up duplicates, so WaitConfirmed sees each
// attack once with its latest cents.
func TestMergeNote(t *testing.T) {
	var buf []pitch.Note
	buf = mergeNote(buf, pitch.Note{Start: 100, Key: 40, Cents: 10})
	buf = mergeNote(buf, pitch.Note{Start: 100, Key: 40, Cents: 4}) // same attack, refined
	buf = mergeNote(buf, pitch.Note{Start: 900, Key: 40, Cents: 1}) // new attack, same key
	if len(buf) != 2 {
		t.Fatalf("buffer holds %d notes, want 2 (snapshot not merged)", len(buf))
	}
	if buf[0].Cents != 4 {
		t.Errorf("first attack cents = %v, want the refined 4", buf[0].Cents)
	}
}

// waitFixtureScore builds the wait-mode fixture: two consecutive quarter
// notes on the SAME key (low E — the auto-confirm finding's exact shape)
// followed by a half note on A.
func waitFixtureScore(t *testing.T) *score.Score {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "guitar", Tuning: score.StandardTuning}
	sc.Tracks = append(sc.Tracks, tr)
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0}) // E2, key 40
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0}) // E2 again
	b.AddBeat(score.Half, score.Note{String: 5, Fret: 0})    // A2, key 45
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

// renderUntilWait renders until the engine is waiting at generation gen.
func renderUntilWait(t *testing.T, eng *engine.Engine, gen uint64) {
	t.Helper()
	l := make([]float32, 480)
	r := make([]float32, 480)
	for i := 0; i < 2000; i++ {
		if eng.Waiting() && eng.WaitGeneration() == gen {
			return
		}
		eng.RenderFrames(l, r)
	}
	t.Fatalf("engine never reached wait generation %d (waiting=%v gen=%d)",
		gen, eng.Waiting(), eng.WaitGeneration())
}

// TestOnNotesWaitWiring is the regression test for finding C1, driving the
// exact -listen analysis callback against a real waiting engine:
//
//	(b) a note still ringing from before a wait (same key!) must not
//	    auto-confirm it — only a fresh attack releases;
//	(a) a seek that re-waits at the SAME tick re-arms (generation
//	    tracking): a stale attack from the abandoned wait cannot confirm
//	    the new one;
//	(c) wait-confirmed notes score pitch-only — Hit, Matched, ErrFrames
//	    0 — never timing-judged against the machinery's release latency.
func TestOnNotesWaitWiring(t *testing.T) {
	sc := waitFixtureScore(t)
	eng := newEngine(sc, engine.Options{})
	eng.SetWaitMode(true)

	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	sink := &resultSink{}
	onNotes := newLiveWiring(eng, sink, scorer, gate, pcfg).onNotes

	eng.Play()
	renderUntilWait(t, eng, 1) // wait 1: first E2, tick 0

	// (b) A ringing note from long before the wait must not confirm it.
	consumed := int64(sampleRate)
	ringing := pitch.Note{Start: 0, Key: 40, Cents: 2, Clarity: 0.9}
	onNotes(nil, ringing, true, consumed)
	if !eng.Waiting() {
		t.Fatal("wait 1 auto-confirmed by a note ringing from before the wait")
	}

	// A fresh attack — allowed to start a hair before the arm (grace) —
	// confirms.
	fresh := pitch.Note{Start: consumed - 1000, End: consumed + 400, Key: 40, Cents: 3, Clarity: 0.9}
	onNotes([]pitch.Note{fresh}, pitch.Note{}, false, consumed+480)
	if eng.Waiting() {
		t.Fatal("fresh attack did not confirm wait 1")
	}

	renderUntilWait(t, eng, 2) // wait 2: second E2, same key, tick PPQ

	// (c) The released first note must have scored pitch-only.
	consumed = 2 * sampleRate
	onNotes(nil, pitch.Note{}, false, consumed) // drains results, arms wait 2
	if len(sink.results) != 1 {
		t.Fatalf("after wait 1 released: %d results, want 1", len(sink.results))
	}
	r := sink.results[0]
	if r.Event.Key != 40 || r.Verdict != practice.VerdictHit || !r.Matched || r.ErrFrames != 0 {
		t.Fatalf("wait-confirmed note scored %+v, want pitch-only Hit (Matched, ErrFrames 0)", r)
	}

	// (b) THE finding: the first E2 is still ringing while the engine
	// waits on the second E2. Same key, old attack — must not release.
	stillRinging := pitch.Note{Start: consumed - sampleRate, Key: 40, Cents: 2, Clarity: 0.9}
	onNotes(nil, stillRinging, true, consumed+480)
	if !eng.Waiting() {
		t.Fatal("wait 2 auto-released from the previous note still ringing (same key)")
	}

	// (a) Seek to the same tick: the wait clears and re-engages at the
	// SAME tick with a new generation. An attack from the abandoned wait
	// is stale for the new one and must not confirm it (the old
	// tick-keyed wiring would not have re-armed).
	staleStart := consumed + 480 // fresh for wait 2, stale after the seek
	eng.SeekTick(score.PPQ)
	renderUntilWait(t, eng, 3)
	consumed = 4 * sampleRate
	onNotes(nil, pitch.Note{Start: staleStart, Key: 40, Cents: 1, Clarity: 0.9}, true, consumed)
	if !eng.Waiting() {
		t.Fatal("re-wait after seek confirmed by a stale attack (gate not re-armed)")
	}

	// A genuinely fresh attack releases the re-armed wait.
	onNotes([]pitch.Note{{Start: consumed + 100, End: consumed + 600, Key: 40, Cents: -4, Clarity: 0.9}},
		pitch.Note{}, false, consumed+960)
	if eng.Waiting() {
		t.Fatal("fresh attack did not confirm the re-armed wait")
	}

	renderUntilWait(t, eng, 4) // wait 4: the A2 half note

	consumed = 6 * sampleRate
	onNotes([]pitch.Note{{Start: consumed + 100, End: consumed + 600, Key: 45, Cents: 6, Clarity: 0.9}},
		pitch.Note{}, false, consumed+960)
	if eng.Waiting() {
		t.Fatal("fresh attack did not confirm the final wait")
	}

	// Render out the rest of the piece and drain.
	l := make([]float32, 4800)
	r2 := make([]float32, 4800)
	for i := 0; i < 40 && eng.Playing(); i++ {
		eng.RenderFrames(l, r2)
	}
	onNotes(nil, pitch.Note{}, false, 8*sampleRate)

	stats := scorer.Stats()
	if stats.Miss != 0 {
		t.Errorf("stats %+v: wait-confirmed practice produced misses, want 0", stats)
	}
	if stats.Hit != 3 {
		t.Errorf("stats %+v: want 3 pitch-only hits (one per wait-confirmed note)", stats)
	}
	for _, res := range sink.results {
		if !res.Matched || res.ErrFrames != 0 {
			t.Errorf("result %+v: wait-confirmed note carries a timing error", res)
		}
	}
}

// TestSetupListenReportsConditions pins setupListen's other return: the
// conditions a session opened under used to be stderr lines only, which a
// double-clicked windowed binary has nowhere to show. They now come back
// to the caller, so each caller can put them on the practice view's
// banner with its own remedy wording.
func TestSetupListenReportsConditions(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())
	backend := &stubBackend{capture: testCapture, playback: testPlayback}
	useStubBackend(t, backend)

	sc := oneBarScore(t)
	open := func(t *testing.T, cfg appconfig.Config) liveConditions {
		t.Helper()
		eng := newEngine(sc, engine.Options{})
		app := ui.New(eng, sc, 0)
		session, cond, err := setupListen(eng, app, sc, "", "", cfg)
		if err != nil {
			t.Fatalf("setupListen: %v", err)
		}
		session.Stop()
		return cond
	}

	t.Run("no calibration stored", func(t *testing.T) {
		cond := open(t, appconfig.Config{})
		if !cond.uncalibrated {
			t.Error("an empty config reported the session as calibrated")
		}
		if len(cond.notes) != 0 {
			t.Errorf("notes = %v, want none when every device resolves", cond.notes)
		}
	})

	t.Run("calibration stored for the resolved pair", func(t *testing.T) {
		var cfg appconfig.Config
		cfg.SetOffset("cap-mic", "pb-spk", 500, 0.9) // the concrete defaults
		cond := open(t, cfg)
		if cond.uncalibrated {
			t.Error("a stored offset for the resolved pair reported as uncalibrated")
		}
	})

	t.Run("stale remembered device comes back as a note", func(t *testing.T) {
		cfg := appconfig.Config{CaptureDeviceID: "cap-unplugged", PlaybackDeviceID: "pb-usb"}
		cond := open(t, cfg)
		if len(cond.notes) != 1 || !strings.Contains(cond.notes[0], "capture device is not connected") {
			t.Errorf("notes = %v, want exactly one explaining the capture fallback", cond.notes)
		}
	})
}

// silentStream drives the calibration handler from its own goroutine with
// silence on the input: the pass completes its capture, and the estimate
// then fails — which is exactly the path the pass-through test needs.
type silentStream struct {
	handler audio.DuplexHandler
	stop    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

func (s *silentStream) Start() error {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		in := make([]float32, 4800)
		outL := make([]float32, 4800)
		outR := make([]float32, 4800)
		for {
			select {
			case <-s.stop:
				return
			default:
				s.handler(in, outL, outR)
			}
		}
	}()
	return nil
}

// Stop returns only after the handler goroutine has exited, because the
// caller reads the captured buffer that goroutine writes next.
func (s *silentStream) Stop() error {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
	return nil
}
func (s *silentStream) Close() error               { return s.Stop() }
func (s *silentStream) Config() audio.StreamConfig { return audio.StreamConfig{SampleRate: sampleRate} }

// silentBackend opens silentStreams.
type silentBackend struct{}

func (silentBackend) Name() string { return "silent" }
func (silentBackend) Devices() ([]audio.DeviceInfo, []audio.DeviceInfo, error) {
	return testCapture, testPlayback, nil
}
func (silentBackend) OpenDuplex(_ audio.StreamConfig, h audio.DuplexHandler) (audio.Stream, error) {
	return &silentStream{handler: h, stop: make(chan struct{})}, nil
}

// TestCalibrationPassReturnsTheLatencyErrorUnchanged pins the contract
// the settings screen renders by: the latency package writes its
// diagnostics for a person, and calibrationPass used to stack "could not
// measure the round trip:" on top of the package's own prefix — the
// settings screen then showed both, reading as one garbled sentence. The
// package's error must come through unchanged.
func TestCalibrationPassReturnsTheLatencyErrorUnchanged(t *testing.T) {
	_, _, err := calibrationPass(context.Background(), silentBackend{}, "cap-usb", "pb-usb", nil)
	if err == nil {
		t.Fatal("a silent capture produced a nil error, want the latency package's diagnosis")
	}
	if !strings.HasPrefix(err.Error(), "latency:") {
		t.Errorf("error = %q, want the latency package's own message, unwrapped", err)
	}
	if strings.Contains(err.Error(), "could not measure the round trip") {
		t.Errorf("error = %q still carries the cmd-layer wrapper", err)
	}
}

// TestCalibrationPassCancel is the regression test for the uncancellable
// calibration. A pass holds the audio device until the capture completes
// or the 20 s deadline expires; before the context was threaded through
// there was no third way out, so a calibration started from a settings
// screen the user then left kept the device (and kept writing to the
// config) for the rest of that timeout. Cancelling must take the same
// Stop/Close exit, promptly.
//
// The stub stream never delivers audio, so completion is impossible: what
// this measures is purely whether cancellation is honoured.
func TestCalibrationPassCancel(t *testing.T) {
	backend := &stubBackend{capture: testCapture, playback: testPlayback}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, _, err := calibrationPass(ctx, backend, "cap-usb", "pb-usb", nil)
		done <- result{err}
	}()

	waitFor(t, "the pass to open and start the stream", func() bool {
		streams := backend.openStreams()
		if len(streams) != 1 {
			return false
		}
		started, _, _ := streams[0].counts()
		return started == 1
	})

	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, errCalibrationCanceled) {
			t.Fatalf("cancelled pass returned %v, want an error wrapping errCalibrationCanceled", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("calibrationPass did not return within 5 s of cancellation (it is waiting out the 20 s deadline)")
	}

	streams := backend.openStreams()
	_, stopped, closed := streams[0].counts()
	if stopped != 1 || closed != 1 {
		t.Errorf("after cancellation the stream was stopped %d and closed %d times, want 1 and 1: the device is still held",
			stopped, closed)
	}
}

// TestCalibrationPassAlreadyCanceled: a context cancelled before the call
// must not open the device at all.
func TestCalibrationPassAlreadyCanceled(t *testing.T) {
	backend := &stubBackend{capture: testCapture, playback: testPlayback}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := calibrationPass(ctx, backend, "cap-usb", "pb-usb", nil)
	if !errors.Is(err, errCalibrationCanceled) {
		t.Fatalf("err = %v, want an error wrapping errCalibrationCanceled", err)
	}
	if n := backend.openCount(); n != 0 {
		t.Errorf("backend saw %d opens for an already-cancelled pass, want 0", n)
	}
}

// TestSetupListenRollsBackTapOnFailure is the regression test for the
// leaked event tap. setupListen installs the tap before starting the
// session; when the session fails to start, its caller falls back to plain
// playback — and used to leave the engine feeding expectations into a
// Scorer that nothing would ever drain, so its pending list grew without
// bound and reallocated inside the render callback. The failure path must
// clear the tap, at setupListen itself, so every caller is covered.
//
// The engine exposes no way to read the tap back, so the assertion goes
// through the setEventTap seam: the last thing setupListen does to the
// engine on a failed wiring must be to clear it.
func TestSetupListenRollsBackTapOnFailure(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	backend := &stubBackend{
		capture:  testCapture,
		playback: testPlayback,
		openErr:  errors.New("stub device refused"),
	}
	useStubBackend(t, backend)

	var installed []bool // one entry per SetEventTap call: was it non-nil?
	prev := setEventTap
	setEventTap = func(eng *engine.Engine, fn func(ev score.NoteEvent, outFrame int64)) {
		installed = append(installed, fn != nil)
		prev(eng, fn)
	}
	t.Cleanup(func() { setEventTap = prev })

	sc := oneBarScore(t)
	eng := newEngine(sc, engine.Options{})
	app := ui.New(eng, sc, 0)

	session, _, err := setupListen(eng, app, sc, "", "", appconfig.Config{})
	if err == nil {
		session.Stop()
		t.Fatal("setupListen with a refusing backend returned nil error")
	}
	if len(installed) == 0 {
		t.Fatal("setupListen never touched the event tap; the test is not exercising the leak")
	}
	if installed[len(installed)-1] {
		t.Error("a failed setupListen left the event tap installed: the engine keeps feeding an orphaned scorer")
	}
}

// TestSetupListenKeepsTapOnSuccess: the rollback must not fire on the
// happy path — the live session depends on the tap being installed.
func TestSetupListenKeepsTapOnSuccess(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	backend := &stubBackend{capture: testCapture, playback: testPlayback}
	useStubBackend(t, backend)

	var installed []bool
	prev := setEventTap
	setEventTap = func(eng *engine.Engine, fn func(ev score.NoteEvent, outFrame int64)) {
		installed = append(installed, fn != nil)
		prev(eng, fn)
	}
	t.Cleanup(func() { setEventTap = prev })

	sc := oneBarScore(t)
	eng := newEngine(sc, engine.Options{})
	app := ui.New(eng, sc, 0)

	session, _, err := setupListen(eng, app, sc, "", "", appconfig.Config{})
	if err != nil {
		t.Fatalf("setupListen: %v", err)
	}
	defer session.Stop()
	if len(installed) != 1 || !installed[0] {
		t.Errorf("event tap calls = %v, want exactly one install and no rollback", installed)
	}
}

// TestFillDeviceIDStaleRemembered is the regression test for audit C3: a
// remembered device that is no longer enumerated must fall back to the
// concrete default WITH a note, not be handed to the backend verbatim.
// The settings screen already displayed the fallback; the open path
// used the stale ID, so what the user saw and what the stream opened on
// disagreed.
func TestFillDeviceIDStaleRemembered(t *testing.T) {
	cfg := appconfig.Config{CaptureDeviceID: "cap-unplugged", PlaybackDeviceID: "pb-usb"}
	in, out, notes, _, _, err := fillDeviceIDs(testCapture, testPlayback, cfg, "", "")
	if err != nil {
		t.Fatalf("fillDeviceIDs: %v", err)
	}
	if in != "cap-mic" {
		t.Errorf("stale capture ID resolved to %q, want the default cap-mic", in)
	}
	if out != "pb-usb" {
		t.Errorf("still-present playback ID resolved to %q, want pb-usb kept", out)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "capture device is not connected") {
		t.Errorf("notes = %v, want exactly one explaining the capture fallback", notes)
	}

	// A present remembered device keeps its silence: no note.
	cfg = appconfig.Config{CaptureDeviceID: "cap-usb", PlaybackDeviceID: "pb-usb"}
	_, _, notes, _, _, err = fillDeviceIDs(testCapture, testPlayback, cfg, "", "")
	if err != nil || len(notes) != 0 {
		t.Errorf("present devices produced notes %v (err %v), want none", notes, err)
	}
}

// TestResolvedDeviceName is the regression test for audit C4: an unset or
// stale ID must resolve to the name of the device that will actually be
// used (the system default), never to a placeholder string that matches
// no real interface — the old code compared the literal words "system
// default" and raised a permanent unfounded split-device banner.
func TestResolvedDeviceName(t *testing.T) {
	if got := resolvedDeviceName(testPlayback, ""); got != "Speakers" {
		t.Errorf("unset ID resolved to %q, want the default's name Speakers", got)
	}
	if got := resolvedDeviceName(testPlayback, "pb-gone"); got != "Speakers" {
		t.Errorf("stale ID resolved to %q, want the default's name Speakers", got)
	}
	if got := resolvedDeviceName(testPlayback, "pb-usb"); got != "USB Audio Interface" {
		t.Errorf("present ID resolved to %q, want its own name", got)
	}
	noDefault := []audio.DeviceInfo{{ID: "a", Name: "A"}}
	if got := resolvedDeviceName(noDefault, ""); got != "" {
		t.Errorf("no default marked resolved to %q, want the unknown \"\"", got)
	}
}

// TestCalibrateKeepsThePreferenceWhenTheDeviceIsAway is the C5 regression.
// Calibrating while the saved interface is unplugged measures whatever is
// there instead — correctly, and the offset for that pair is worth
// keeping. What must NOT happen is adopting the stand-in as the player's
// device: the saved preference is the only record of which interface they
// own, and losing it means reconnecting it no longer selects it and the
// offset already measured for it is never looked up again.
func TestCalibrateKeepsThePreferenceWhenTheDeviceIsAway(t *testing.T) {
	cfg := appconfig.Config{CaptureDeviceID: "cap-unplugged", PlaybackDeviceID: "pb-usb"}
	in, out, _, inFell, outFell, err := fillDeviceIDs(testCapture, testPlayback, cfg, "", "")
	if err != nil {
		t.Fatalf("fillDeviceIDs: %v", err)
	}
	if !inFell {
		t.Fatal("the unplugged capture device was not reported as a fallback")
	}
	if outFell {
		t.Fatal("the still-present playback device was reported as a fallback")
	}

	rememberDevices(&cfg, in, out, inFell, outFell)
	if cfg.CaptureDeviceID != "cap-unplugged" {
		t.Errorf("saved capture device = %q, want the player's own cap-unplugged kept", cfg.CaptureDeviceID)
	}
	if cfg.PlaybackDeviceID != "pb-usb" {
		t.Errorf("saved playback device = %q, want pb-usb", cfg.PlaybackDeviceID)
	}
}

// TestCalibrateAdoptsADeviceTheUserChose pins the other side: a device
// resolved from a flag or filling an empty preference is a real choice and
// must be remembered, or -in/-out would never stick.
func TestCalibrateAdoptsADeviceTheUserChose(t *testing.T) {
	cfg := appconfig.Config{}
	in, out, _, inFell, outFell, err := fillDeviceIDs(testCapture, testPlayback, cfg, "usb", "usb")
	if err != nil {
		t.Fatalf("fillDeviceIDs: %v", err)
	}
	if inFell || outFell {
		t.Fatalf("an explicitly chosen device was reported as a fallback (in %v, out %v)", inFell, outFell)
	}
	rememberDevices(&cfg, in, out, inFell, outFell)
	if cfg.CaptureDeviceID != "cap-usb" || cfg.PlaybackDeviceID != "pb-usb" {
		t.Errorf("saved devices = %q/%q, want cap-usb/pb-usb", cfg.CaptureDeviceID, cfg.PlaybackDeviceID)
	}
}

// deadNoteWaitScore: one dead (muted) note, then a normal one. A damped
// string produces no trackable f0, so the only evidence the player struck
// it is the attack itself.
func deadNoteWaitScore(t *testing.T) *score.Score {
	t.Helper()
	sc := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: 500000}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "guitar", Tuning: score.StandardTuning}
	sc.Tracks = append(sc.Tracks, tr)
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 5, Tech: score.TechDead})
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	b.AddBeat(score.Half, score.Note{String: 5, Fret: 0})
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

// TestDeadNoteWaitReleasedByStrum is the P3 regression driven through the
// real -listen wiring, which is where it actually bit: a wait point on a
// dead note can never be satisfied by a detection, because a muted string
// has no pitch to detect. Before the fix the strum callback fed only the
// scorer, so playback halted at the first `x` in a piece and never
// resumed no matter what the player did.
func TestDeadNoteWaitReleasedByStrum(t *testing.T) {
	sc := deadNoteWaitScore(t)
	eng := newEngine(sc, engine.Options{})
	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	sink := &resultSink{}
	w := newLiveWiring(eng, sink, scorer, gate, pcfg)

	eng.SetWaitMode(true)
	eng.Play()
	renderUntilWait(t, eng, 1)

	evs, _, ok := eng.WaitingOn()
	if !ok || len(evs) != 1 || evs[0].Tech != score.TechDead {
		t.Fatalf("expected to be halted on the dead note, got %+v (waiting=%v)", evs, ok)
	}

	// Arm the gate the way a live batch would.
	consumed := int64(sampleRate)
	w.onNotes(nil, pitch.Note{}, false, consumed)
	if !eng.Waiting() {
		t.Fatal("the wait released with no player input at all")
	}

	// The player strikes the muted string: an attack, no pitch.
	w.onStrums([]pitch.Strum{{Frame: consumed}})
	w.onNotes(nil, pitch.Note{}, false, consumed+4096)

	if eng.Waiting() {
		t.Fatal("a strum did not release the dead-note wait point — playback is stuck forever")
	}
	// And it must be credited, not left to age into a false miss.
	for _, r := range sink.results {
		if r.Verdict == practice.VerdictMiss {
			t.Errorf("the confirmed dead note scored a miss: %+v", r)
		}
	}
}

// TestDeadNoteWaitStrumInTheArmingBatch: OnStrums runs before OnNotes, so
// a player who anticipates the wait point by a hair strums while the gate
// is still unarmed. That attack must not be dropped — otherwise they have
// to strike a muted note twice with no indication why.
func TestDeadNoteWaitStrumInTheArmingBatch(t *testing.T) {
	sc := deadNoteWaitScore(t)
	eng := newEngine(sc, engine.Options{})
	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	w := newLiveWiring(eng, &resultSink{}, scorer, gate, pcfg)

	eng.SetWaitMode(true)
	eng.Play()
	renderUntilWait(t, eng, 1)

	// Strum arrives first, in the same batch that will arm the gate.
	consumed := int64(sampleRate)
	w.onStrums([]pitch.Strum{{Frame: consumed - 100}})
	w.onNotes(nil, pitch.Note{}, false, consumed)

	if eng.Waiting() {
		t.Fatal("a strum in the arming batch was dropped; the anticipating player's attack was lost")
	}
}

// TestStaleGateCannotReleaseTheNextWait is the regression for the defect
// the fix-verification pass found in the P3 wiring itself.
//
// The wait gate stays satisfied from the moment it is filled until
// something re-arms it, and only onNotes arms. When onStrums was first
// given the gate it consulted nothing else, so every strum AFTER a
// confirmation found a satisfied gate and released whatever wait came
// next: the player strummed once and the piece ran on through wait points
// they never answered. That is worse than the halt the strum path was
// added to cure — wait mode silently stopped waiting.
func TestStaleGateCannotReleaseTheNextWait(t *testing.T) {
	sc := waitFixtureScore(t) // three pitched notes; no dead notes at all
	eng := newEngine(sc, engine.Options{})
	eng.SetWaitMode(true)

	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	w := newLiveWiring(eng, &resultSink{}, scorer, gate, pcfg)

	eng.Play()
	renderUntilWait(t, eng, 1)

	// Wait 1 released honestly, by a fresh E2 attack.
	consumed := int64(sampleRate)
	w.onNotes(nil, pitch.Note{}, false, consumed) // arm
	fresh := pitch.Note{Start: consumed - 1000, End: consumed + 400, Key: 40, Cents: 3, Clarity: 0.9}
	w.onNotes([]pitch.Note{fresh}, pitch.Note{}, false, consumed+480)
	if eng.Waiting() {
		t.Fatal("the fresh attack did not confirm wait 1")
	}

	renderUntilWait(t, eng, 2) // wait 2 engages: the second E2

	// The player has played nothing new. A bare onset — a chair creak, a
	// hand landing on the strings — arrives before onNotes re-arms
	// (OnStrums runs first within a batch, per live.Config).
	w.onStrums([]pitch.Strum{{Frame: consumed + 20000}})

	if !eng.Waiting() {
		t.Fatal("a bare onset released wait 2 through the previous wait's satisfied gate")
	}
}

// TestStrumsAreInertWhileNothingIsWaiting: with wait mode off entirely,
// strums must still reach the scorer for chord verification but must
// never reach the gate — otherwise every strum re-records WaitConfirmed
// pre-matches for a wait that is long gone.
func TestStrumsAreInertWhileNothingIsWaiting(t *testing.T) {
	sc := waitFixtureScore(t)
	eng := newEngine(sc, engine.Options{})
	eng.SetWaitMode(true)

	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	w := newLiveWiring(eng, &resultSink{}, scorer, gate, pcfg)

	eng.Play()
	renderUntilWait(t, eng, 1)
	consumed := int64(sampleRate)
	w.onNotes(nil, pitch.Note{}, false, consumed)
	fresh := pitch.Note{Start: consumed - 1000, End: consumed + 400, Key: 40, Cents: 3, Clarity: 0.9}
	w.onNotes([]pitch.Note{fresh}, pitch.Note{}, false, consumed+480)

	// Wait mode off: nothing is waiting and nothing can be.
	eng.SetWaitMode(false)
	l := make([]float32, 480)
	r := make([]float32, 480)
	for i := 0; i < 50; i++ {
		eng.RenderFrames(l, r)
	}
	if eng.Waiting() {
		t.Fatal("the engine is still waiting after SetWaitMode(false)")
	}
	if w.armedLive() {
		t.Error("the wiring believes it is armed for a live wait when none exists")
	}
	// The strum must still be scored, just not routed to the gate.
	w.onStrums([]pitch.Strum{{Frame: consumed + 50000}})
}

// TestDeadNoteWaitStrumBeforeTheWaitEngages: a player anticipating the
// wait point strums while the engine has not reached it yet, so the batch
// carrying that strum takes the "not waiting" path. The attack must
// survive to the batch that arms the gate — it is inside the arming grace
// by definition, and dropping it makes the player strike a muted string
// twice with no indication why.
func TestDeadNoteWaitStrumBeforeTheWaitEngages(t *testing.T) {
	sc := deadNoteWaitScore(t)
	eng := newEngine(sc, engine.Options{})
	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	w := newLiveWiring(eng, &resultSink{}, scorer, gate, pcfg)

	eng.SetWaitMode(true)
	eng.Play()

	// A batch BEFORE the engine has reached the wait point: the strum
	// arrives, and onNotes takes its not-waiting early return.
	consumed := int64(sampleRate)
	w.onStrums([]pitch.Strum{{Frame: consumed - 1000}})
	w.onNotes(nil, pitch.Note{}, false, consumed)

	// Now the engine reaches the dead note and the gate arms.
	renderUntilWait(t, eng, 1)
	w.onNotes(nil, pitch.Note{}, false, consumed+2000)

	if eng.Waiting() {
		t.Fatal("the anticipating strum was dropped by the not-waiting batch; the dead-note wait is still halted")
	}
}

// TestStrumBufferIsBounded: strums are held only until they are too old to
// confirm anything, so a long noodling session cannot grow the buffer.
func TestStrumBufferIsBounded(t *testing.T) {
	sc := deadNoteWaitScore(t)
	eng := newEngine(sc, engine.Options{})
	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	w := newLiveWiring(eng, &resultSink{}, scorer, practice.NewWaitGate(pcfg), pcfg)

	for i := int64(0); i < 500; i++ {
		consumed := i * 4096
		w.onStrums([]pitch.Strum{{Frame: consumed}})
		w.onNotes(nil, pitch.Note{}, false, consumed)
	}
	if n := len(w.strumBuf); n > maxRecentStrums {
		t.Errorf("strum buffer holds %d after 500 batches, want at most %d", n, maxRecentStrums)
	}
}
