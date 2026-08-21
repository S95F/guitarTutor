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

func TestCalibrationSpacingHeadroom(t *testing.T) {
	if calSpacing < sampleRate {
		t.Fatalf("calSpacing = %d frames, want >= 1 s (%d) of alias headroom", calSpacing, sampleRate)
	}
	const delay = 45 * sampleRate / 100
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

type resultSink struct {
	results []practice.NoteResult
}

func (s *resultSink) OfferResults(rs []practice.NoteResult) {
	s.results = append(s.results, rs...)
}
func (s *resultSink) OfferTuner(pitch.Note, bool) {}

func TestOnNotesSeekAbandonsPending(t *testing.T) {
	sc := waitFixtureScore(t)
	eng := newEngine(sc, engine.Options{})

	pcfg := practice.Config{SampleRate: sampleRate, Track: 0}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)
	sink := &resultSink{}
	onNotes := newLiveWiring(eng, sink, scorer, gate, pcfg).onNotes

	eng.Play()
	l := make([]float32, 480)
	r := make([]float32, 480)
	for i := 0; i < 100; i++ {
		eng.RenderFrames(l, r)
	}
	consumed := int64(sampleRate)
	onNotes(nil, pitch.Note{}, false, consumed)
	if len(sink.results) != 0 {
		t.Fatalf("results before anything expired: %+v", sink.results)
	}

	eng.SeekTick(0)

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

func TestMergeNote(t *testing.T) {
	var buf []pitch.Note
	buf = mergeNote(buf, pitch.Note{Start: 100, Key: 40, Cents: 10})
	buf = mergeNote(buf, pitch.Note{Start: 100, Key: 40, Cents: 4})
	buf = mergeNote(buf, pitch.Note{Start: 900, Key: 40, Cents: 1})
	if len(buf) != 2 {
		t.Fatalf("buffer holds %d notes, want 2 (snapshot not merged)", len(buf))
	}
	if buf[0].Cents != 4 {
		t.Errorf("first attack cents = %v, want the refined 4", buf[0].Cents)
	}
}

func waitFixtureScore(t *testing.T) *score.Score {
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
	b.AddBeat(score.Half, score.Note{String: 5, Fret: 0})
	if err := sc.Validate(); err != nil {
		t.Fatalf("fixture score does not validate: %v", err)
	}
	return sc
}

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
	renderUntilWait(t, eng, 1)

	consumed := int64(sampleRate)
	ringing := pitch.Note{Start: 0, Key: 40, Cents: 2, Clarity: 0.9}
	onNotes(nil, ringing, true, consumed)
	if !eng.Waiting() {
		t.Fatal("wait 1 auto-confirmed by a note ringing from before the wait")
	}

	fresh := pitch.Note{Start: consumed - 1000, End: consumed + 400, Key: 40, Cents: 3, Clarity: 0.9}
	onNotes([]pitch.Note{fresh}, pitch.Note{}, false, consumed+480)
	if eng.Waiting() {
		t.Fatal("fresh attack did not confirm wait 1")
	}

	renderUntilWait(t, eng, 2)

	consumed = 2 * sampleRate
	onNotes(nil, pitch.Note{}, false, consumed)
	if len(sink.results) != 1 {
		t.Fatalf("after wait 1 released: %d results, want 1", len(sink.results))
	}
	r := sink.results[0]
	if r.Event.Key != 40 || r.Verdict != practice.VerdictHit || !r.Matched || r.ErrFrames != 0 {
		t.Fatalf("wait-confirmed note scored %+v, want pitch-only Hit (Matched, ErrFrames 0)", r)
	}

	stillRinging := pitch.Note{Start: consumed - sampleRate, Key: 40, Cents: 2, Clarity: 0.9}
	onNotes(nil, stillRinging, true, consumed+480)
	if !eng.Waiting() {
		t.Fatal("wait 2 auto-released from the previous note still ringing (same key)")
	}

	staleStart := consumed + 480
	eng.SeekTick(score.PPQ)
	renderUntilWait(t, eng, 3)
	consumed = 4 * sampleRate
	onNotes(nil, pitch.Note{Start: staleStart, Key: 40, Cents: 1, Clarity: 0.9}, true, consumed)
	if !eng.Waiting() {
		t.Fatal("re-wait after seek confirmed by a stale attack (gate not re-armed)")
	}

	onNotes([]pitch.Note{{Start: consumed + 100, End: consumed + 600, Key: 40, Cents: -4, Clarity: 0.9}},
		pitch.Note{}, false, consumed+960)
	if eng.Waiting() {
		t.Fatal("fresh attack did not confirm the re-armed wait")
	}

	renderUntilWait(t, eng, 4)

	consumed = 6 * sampleRate
	onNotes([]pitch.Note{{Start: consumed + 100, End: consumed + 600, Key: 45, Cents: 6, Clarity: 0.9}},
		pitch.Note{}, false, consumed+960)
	if eng.Waiting() {
		t.Fatal("fresh attack did not confirm the final wait")
	}

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
		cfg.SetOffset("cap-mic", "pb-spk", 500, 0.9)
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

func (s *silentStream) Stop() error {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
	return nil
}
func (s *silentStream) Close() error               { return s.Stop() }
func (s *silentStream) Config() audio.StreamConfig { return audio.StreamConfig{SampleRate: sampleRate} }

type silentBackend struct{}

func (silentBackend) Name() string { return "silent" }
func (silentBackend) Devices() ([]audio.DeviceInfo, []audio.DeviceInfo, error) {
	return testCapture, testPlayback, nil
}
func (silentBackend) OpenDuplex(_ audio.StreamConfig, h audio.DuplexHandler) (audio.Stream, error) {
	return &silentStream{handler: h, stop: make(chan struct{})}, nil
}

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

func TestSetupListenRollsBackTapOnFailure(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	backend := &stubBackend{
		capture:  testCapture,
		playback: testPlayback,
		openErr:  errors.New("stub device refused"),
	}
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

	cfg = appconfig.Config{CaptureDeviceID: "cap-usb", PlaybackDeviceID: "pb-usb"}
	_, _, notes, _, _, err = fillDeviceIDs(testCapture, testPlayback, cfg, "", "")
	if err != nil || len(notes) != 0 {
		t.Errorf("present devices produced notes %v (err %v), want none", notes, err)
	}
}

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

func TestCalibrateRefusesTheAmbiguousDefaultPair(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())
	backend := &stubBackend{
		capture:  []audio.DeviceInfo{{ID: "a", Name: "A"}},
		playback: []audio.DeviceInfo{{ID: "b", Name: "B"}},
	}
	useStubBackend(t, backend)

	err := runCalibrate(nil)
	if err == nil {
		t.Fatal("runCalibrate with no resolvable devices returned nil, want a refusal")
	}
	for _, want := range []string{"-in", "-out", "musictutor devices"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	if n := backend.openCount(); n != 0 {
		t.Errorf("the refused calibration opened the device %d times, want 0", n)
	}
}

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

	consumed := int64(sampleRate)
	w.onNotes(nil, pitch.Note{}, false, consumed)
	if !eng.Waiting() {
		t.Fatal("the wait released with no player input at all")
	}

	w.onStrums([]pitch.Strum{{Frame: consumed}})
	w.onNotes(nil, pitch.Note{}, false, consumed+4096)

	if eng.Waiting() {
		t.Fatal("a strum did not release the dead-note wait point — playback is stuck forever")
	}

	for _, r := range sink.results {
		if r.Verdict == practice.VerdictMiss {
			t.Errorf("the confirmed dead note scored a miss: %+v", r)
		}
	}
}

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

	consumed := int64(sampleRate)
	w.onStrums([]pitch.Strum{{Frame: consumed - 100}})
	w.onNotes(nil, pitch.Note{}, false, consumed)

	if eng.Waiting() {
		t.Fatal("a strum in the arming batch was dropped; the anticipating player's attack was lost")
	}
}

func TestStaleGateCannotReleaseTheNextWait(t *testing.T) {
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
	if eng.Waiting() {
		t.Fatal("the fresh attack did not confirm wait 1")
	}

	renderUntilWait(t, eng, 2)

	w.onStrums([]pitch.Strum{{Frame: consumed + 20000}})

	if !eng.Waiting() {
		t.Fatal("a bare onset released wait 2 through the previous wait's satisfied gate")
	}
}

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

	w.onStrums([]pitch.Strum{{Frame: consumed + 50000}})
}

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

	consumed := int64(sampleRate)
	w.onStrums([]pitch.Strum{{Frame: consumed - 1000}})
	w.onNotes(nil, pitch.Note{}, false, consumed)

	renderUntilWait(t, eng, 1)
	w.onNotes(nil, pitch.Note{}, false, consumed+2000)

	if eng.Waiting() {
		t.Fatal("the anticipating strum was dropped by the not-waiting batch; the dead-note wait is still halted")
	}
}

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
