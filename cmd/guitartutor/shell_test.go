package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/S95F/guitarTutor/internal/appconfig"
	"github.com/S95F/guitarTutor/internal/audio"
	"github.com/S95F/guitarTutor/internal/ui"
)

// --- test doubles -------------------------------------------------------

// stubStream is an audio.Stream that records its lifecycle and never
// delivers any audio: nothing here touches a real device, and a pass that
// waits for the capture to complete waits forever unless it is cancelled.
type stubStream struct {
	cfg audio.StreamConfig

	mu      sync.Mutex
	started int
	stopped int
	closed  int
}

func (s *stubStream) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started++
	return nil
}

func (s *stubStream) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped++
	return nil
}

func (s *stubStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

func (s *stubStream) Config() audio.StreamConfig { return s.cfg }

func (s *stubStream) counts() (started, stopped, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started, s.stopped, s.closed
}

// stubBackend hands out stubStreams and records every open attempt.
//
// openGate, when non-nil, blocks the FIRST OpenDuplex until it is closed —
// that is how a test holds a calibration pass mid-flight without waiting
// on a real device. Only the first, so a test for a guard that is missing
// fails on its assertion instead of deadlocking behind the same gate.
// openErr, when non-nil, fails the open (after the gate, so a held-open
// pass can be released into a quick failure instead of the 20 s timeout).
type stubBackend struct {
	capture  []audio.DeviceInfo
	playback []audio.DeviceInfo
	openGate chan struct{}
	openErr  error

	mu      sync.Mutex
	opens   int
	streams []*stubStream
}

func (b *stubBackend) Name() string { return "stub" }

func (b *stubBackend) Devices() ([]audio.DeviceInfo, []audio.DeviceInfo, error) {
	return b.capture, b.playback, nil
}

func (b *stubBackend) OpenDuplex(cfg audio.StreamConfig, _ audio.DuplexHandler) (audio.Stream, error) {
	b.mu.Lock()
	b.opens++
	first := b.opens == 1
	b.mu.Unlock()
	if first && b.openGate != nil {
		<-b.openGate
	}
	if b.openErr != nil {
		return nil, b.openErr
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = sampleRate
	}
	s := &stubStream{cfg: cfg}
	b.mu.Lock()
	b.streams = append(b.streams, s)
	b.mu.Unlock()
	return s, nil
}

func (b *stubBackend) openCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.opens
}

func (b *stubBackend) openStreams() []*stubStream {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*stubStream, len(b.streams))
	copy(out, b.streams)
	return out
}

// useStubBackend points the liveBackend seam at b for the test's duration,
// so nothing reaches the machine's sound card.
func useStubBackend(t *testing.T, b audio.Backend) {
	t.Helper()
	prev := liveBackend
	liveBackend = func() (audio.Backend, error) { return b, nil }
	t.Cleanup(func() { liveBackend = prev })
}

// waitFor polls cond until it holds or the test gives up.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- F1: the fatal config race -----------------------------------------

// TestShellPrefsRaceSaveAgainstStoreOffset is the regression test for the
// fatal config race. The game loop saves preferences (marshalling the
// config's maps) while the calibration goroutine stores a freshly measured
// offset into those same maps. Pre-fix, shellPrefs had no lock and
// Calibrate wrote through a.prefs.cfg.SetOffset directly, so the two ran
// concurrently over one map and Go aborted the process with "concurrent
// map iteration and map write" — a throw, not a panic: unrecoverable, and
// it kills practice outright.
//
// The abort cannot be caught, so the assertion is completion: this test
// passes only if both goroutines finish and the config on disk is still
// readable. Against the pre-fix code (SetOffset + Save from one goroutine,
// setCountIn + Save from the other, unlocked — exactly what StoreOffset
// and Save now do under the lock) it takes the process down.
func TestShellPrefsRaceSaveAgainstStoreOffset(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	p := &shellPrefs{}
	// A big map is what makes the window wide: encoding/json iterates
	// every entry, and the writer only has to land inside that walk.
	const seeded = 3000
	for i := 0; i < seeded; i++ {
		p.cfg.SetOffset(fmt.Sprintf("seed-cap-%04d", i), "seed-pb", i, 0.5)
	}
	for i := 0; i < appconfig.MaxRecents; i++ {
		p.cfg.AddRecent(filepath.Join("C:", "songs", fmt.Sprintf("piece-%d.gtab", i)))
	}

	const iters = 100
	errs := make(chan error, 2*iters)
	var wg sync.WaitGroup
	wg.Add(2)

	// Both loops record failures and keep going rather than bailing out:
	// the point is to run the whole racing pair, and a first-failure exit
	// would cut the window short.

	// The game loop: a settings change plus a save, over and over.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			p.SetCountIn(i % 5)
			if err := p.Save(); err != nil {
				errs <- fmt.Errorf("game-loop Save: %w", err)
			}
		}
	}()

	// The calibration goroutine: measured offsets landing in the config.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if err := p.StoreOffset(fmt.Sprintf("live-cap-%04d", i), "live-pb", i, 0.9); err != nil {
				errs <- fmt.Errorf("StoreOffset: %w", err)
			}
		}
	}()

	wg.Wait()
	close(errs)
	failed := 0
	for err := range errs {
		if failed < 3 {
			t.Errorf("racing prefs access failed: %v", err)
		}
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d of %d saves failed while the two goroutines raced", failed, 2*iters)
	}

	// Every clone that was marshalled must have been coherent, so the
	// file left behind parses and carries both goroutines' work.
	got, err := appconfig.Load()
	if err != nil {
		t.Fatalf("config written during the race does not load: %v", err)
	}
	if len(got.LatencyOffsets) < seeded {
		t.Errorf("saved config has %d offsets, want at least the %d seeded", len(got.LatencyOffsets), seeded)
	}
	if _, ok := p.offsetFor("live-cap-0099", "live-pb"); !ok {
		t.Error("the last stored offset is missing from the in-memory config")
	}
}

// TestShellPrefsSaveSnapshotIsDeep guards the other half of the fix: Save
// must marshal a deep copy, not a struct copy. appconfig.Config.Save has a
// value receiver, which copies the struct but shares the map headers and
// the recents backing array — so a shallow copy would still hand
// encoding/json the live maps.
func TestShellPrefsSaveSnapshotIsDeep(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	p := &shellPrefs{}
	p.cfg.SetOffset("cap", "pb", 100, 0.9)
	p.cfg.AddRecent(filepath.Join("C:", "songs", "a.gtab"))

	snap := p.snapshot()
	p.cfg.SetOffset("cap", "pb", 999, 0.1)
	p.cfg.SetOffset("cap2", "pb", 5, 0.1)
	p.cfg.Recents[0] = "mutated"

	if off := snap.LatencyOffsets["cap|pb"]; off != 100 {
		t.Errorf("snapshot offset changed to %d after a later write: the maps are shared", off)
	}
	if _, ok := snap.LatencyOffsets["cap2|pb"]; ok {
		t.Error("snapshot gained an entry written after it was taken: the maps are shared")
	}
	if conf := snap.LatencyConfidence["cap|pb"]; conf != 0.9 {
		t.Errorf("snapshot confidence changed to %v: the maps are shared", conf)
	}
	if snap.Recents[0] == "mutated" {
		t.Error("snapshot recents changed after a later write: the slice is shared")
	}
}

// --- F2: one calibration at a time --------------------------------------

// TestShellAudioCalibrateSingleOwner is the regression test for the
// single-owner guard. The "one at a time" flag used to live on the
// Settings screen, which the shell rebuilds on every visit — so a second
// visit (or a second screen) happily started a second pass on the same
// device pair. The guard now lives on the object that owns the device.
func TestShellAudioCalibrateSingleOwner(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	gate := make(chan struct{})
	backend := &stubBackend{
		capture:  testCapture,
		playback: testPlayback,
		openGate: gate,
		openErr:  errors.New("stub device refused"),
	}
	a := &shellAudio{backend: backend, prefs: &shellPrefs{}}

	first := make(chan error, 1)
	go func() {
		_, _, err := a.Calibrate("cap-usb", "pb-usb", nil)
		first <- err
	}()
	// The first pass is now inside OpenDuplex, holding the device.
	waitFor(t, "the first calibration to claim the device", func() bool {
		return backend.openCount() == 1
	})

	if _, _, err := a.Calibrate("cap-usb", "pb-usb", nil); !errors.Is(err, errCalibrationBusy) {
		t.Errorf("second concurrent Calibrate = %v, want errCalibrationBusy", err)
	}
	if n := backend.openCount(); n != 1 {
		t.Errorf("backend saw %d opens, want 1: the refused call touched the device", n)
	}

	close(gate)
	if err := <-first; err == nil {
		t.Fatal("first Calibrate returned nil, want the stub's open error")
	}

	// The guard is released when the pass returns, so the next visit to
	// settings can calibrate: this attempt reaches the device again.
	if _, _, err := a.Calibrate("cap-usb", "pb-usb", nil); errors.Is(err, errCalibrationBusy) {
		t.Error("Calibrate still refused after the previous pass finished: the guard leaked")
	}
	if n := backend.openCount(); n != 2 {
		t.Errorf("backend saw %d opens, want 2 (the retry reached the device)", n)
	}
}

// --- F4: opening a second piece ------------------------------------------

// oneBarGtab writes a minimal playable piece and returns its path.
func oneBarGtab(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "piece.gtab")
	const src = "\\tempo 120\n\\time 4/4\n2.5.4 0.5.4 2.5.2 |\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

// TestOpenClosesPreviousSession is the regression test for the orphaned
// stream. Nothing stops a second Open — the start screen can hand the
// shell another piece — and Open used to overwrite o.session (and
// o.player) with the new one, leaving the previous stream running with its
// device held and no reference left to close it. Open now closes what it
// finds before it starts anything.
func TestOpenClosesPreviousSession(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	backend := &stubBackend{
		// Both endpoints on one interface, so the split-device warning
		// stays out of the way.
		capture:  []audio.DeviceInfo{{ID: "cap-1", Name: "Stub Audio (Stub Interface)", Default: true}},
		playback: []audio.DeviceInfo{{ID: "pb-1", Name: "Stub Out (Stub Interface)", Default: true}},
	}
	useStubBackend(t, backend)

	prefs := &shellPrefs{}
	prefs.SetDevices("cap-1", "pb-1")
	o := &shellOpener{prefs: prefs}
	t.Cleanup(o.CloseCurrent)

	path := oneBarGtab(t)
	if _, _, err := o.Open(path); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if o.session == nil {
		t.Fatal("first Open did not take the live path; the rest of this test proves nothing")
	}
	firstSession := o.session

	if _, _, err := o.Open(path); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if o.session == nil {
		t.Fatal("second Open did not take the live path")
	}
	if o.session == firstSession {
		t.Fatal("second Open reused the first session")
	}

	streams := backend.openStreams()
	if len(streams) != 2 {
		t.Fatalf("backend opened %d streams, want 2", len(streams))
	}
	if _, _, closed := streams[0].counts(); closed != 1 {
		t.Errorf("the first piece's stream was closed %d times, want exactly 1 (it was orphaned)", closed)
	}
	if _, _, closed := streams[1].counts(); closed != 0 {
		t.Errorf("the current piece's stream was closed %d times, want 0", closed)
	}
}

// TestFailedOpenLeavesPreviousSessionPlaying is the regression test for
// audit A2. Open used to release the running piece's audio as its very
// first statement — but ReopenPiece deliberately keeps the current
// practice screen when a load fails, so a failed F5 reload left a
// live-looking screen with no audio behind it: frozen playhead, silent
// transport, a header still claiming "playing". A failed Open must leave
// the previous session exactly as it was; the teardown happens only once
// the open is past the failure points, immediately before the new audio
// is installed. (The browser flow needs no early teardown: the practice
// screen it came from was popped, and its audio closed, before the
// browser could open anything.)
func TestFailedOpenLeavesPreviousSessionPlaying(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	backend := &stubBackend{
		capture:  []audio.DeviceInfo{{ID: "cap-1", Name: "Stub Audio (Stub Interface)", Default: true}},
		playback: []audio.DeviceInfo{{ID: "pb-1", Name: "Stub Out (Stub Interface)", Default: true}},
	}
	useStubBackend(t, backend)

	prefs := &shellPrefs{}
	prefs.SetDevices("cap-1", "pb-1")
	o := &shellOpener{prefs: prefs}
	t.Cleanup(o.CloseCurrent)

	if _, _, err := o.Open(oneBarGtab(t)); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if o.session == nil {
		t.Fatal("first Open did not take the live path")
	}
	firstSession := o.session

	if _, _, err := o.Open(filepath.Join(t.TempDir(), "missing.gtab")); err == nil {
		t.Fatal("Open of a missing piece returned nil error")
	}
	if o.session != firstSession {
		t.Error("a failed Open must leave the previous session installed and playing")
	}
	streams := backend.openStreams()
	if len(streams) != 1 {
		t.Fatalf("backend opened %d streams, want only the first piece's", len(streams))
	}
	if _, _, closed := streams[0].counts(); closed != 0 {
		t.Errorf("the previous stream was closed %d times by a failed open, want 0", closed)
	}

	// And a following successful open still swaps cleanly: the old
	// session is closed exactly once, before the new one is installed.
	if _, _, err := o.Open(oneBarGtab(t)); err != nil {
		t.Fatalf("recovery Open: %v", err)
	}
	if o.session == firstSession {
		t.Error("the recovery open reused the failed-over session")
	}
	streams = backend.openStreams()
	if len(streams) != 2 {
		t.Fatalf("backend opened %d streams in total, want 2", len(streams))
	}
	if _, _, closed := streams[0].counts(); closed != 1 {
		t.Errorf("the first stream was closed %d times after the recovery open, want exactly 1", closed)
	}
}

// TestShellAudioStoredConfidence checks the optional extension the
// settings screen probes for to rate a stored measurement (the contract
// method is StoredConfidence, asserted at compile time in shell.go): the
// confidence stored beside an offset must read back, and a pair that has
// never been measured must answer false rather than a made-up figure.
func TestShellAudioStoredConfidence(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	p := &shellPrefs{}
	a := &shellAudio{prefs: p}
	if conf, ok := a.StoredConfidence("cap", "pb"); ok {
		t.Fatalf("an unmeasured pair reported confidence %v, want none stored", conf)
	}
	if err := p.StoreOffset("cap", "pb", 1234, 0.87); err != nil {
		t.Fatalf("StoreOffset: %v", err)
	}
	conf, ok := a.StoredConfidence("cap", "pb")
	if !ok || conf != 0.87 {
		t.Errorf("StoredConfidence = (%v, %v), want (0.87, true)", conf, ok)
	}
	if _, ok := a.StoredConfidence("cap", "other"); ok {
		t.Error("a different pair read back the stored confidence")
	}
}

// TestSplitDeviceWarning: the clock-drift check returns its text instead
// of setting the banner itself, so an open can compose it with the other
// conditions — and the text now carries its remedy, because a warning
// that names a problem without naming the fix is homework.
func TestSplitDeviceWarning(t *testing.T) {
	backend := &stubBackend{
		capture: []audio.DeviceInfo{
			{ID: "cap-usb", Name: "Line In (Scarlett 2i2 USB)", Default: true},
			{ID: "cap-rt", Name: "Microphone (Realtek(R) Audio)"},
		},
		playback: []audio.DeviceInfo{
			{ID: "pb-usb", Name: "Headphones (Scarlett 2i2 USB)", Default: true},
			{ID: "pb-rt", Name: "Speakers (Realtek(R) Audio)"},
		},
	}
	useStubBackend(t, backend)
	prefs := &shellPrefs{}
	o := &shellOpener{prefs: prefs}

	if got := o.splitDeviceWarning(); got != "" {
		t.Errorf("same-interface defaults warned: %q", got)
	}

	prefs.SetDevices("cap-usb", "pb-rt")
	got := o.splitDeviceWarning()
	for _, want := range []string{
		"different devices",
		"Line In (Scarlett 2i2 USB)",
		"Speakers (Realtek(R) Audio)",
		"pick the same interface for capture and playback in settings (S)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("split warning %q does not contain %q", got, want)
		}
	}
}

// TestImportSummary: the banner gets one line, so a pile of importer
// warnings is coalesced into the first one plus a count.
func TestImportSummary(t *testing.T) {
	cases := []struct {
		name  string
		warns []string
		want  string
	}{
		{name: "none", warns: nil, want: ""},
		{name: "one", warns: []string{"percussion track dropped"},
			want: "imported with 1 warning: percussion track dropped"},
		{name: "several", warns: []string{"percussion track dropped", "tempo map truncated", "lyrics ignored"},
			want: "imported with 3 warnings: percussion track dropped (and 2 more)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := importSummary(c.warns); got != c.want {
				t.Errorf("importSummary(%v) = %q, want %q", c.warns, got, c.want)
			}
		})
	}
}

// TestComposeBanner: the practice view's warning slot holds ONE message
// (last writer wins), so the conditions of an open must arrive joined —
// the device fallback used to vanish under the split-device warning, and
// the missing calibration never reached the screen at all.
func TestComposeBanner(t *testing.T) {
	if got := composeBanner(nil); got != "" {
		t.Errorf("composeBanner(nil) = %q, want empty", got)
	}
	got := composeBanner([]string{"a fallback note", uncalibratedShellWarning, "a split-device warning"})
	for _, want := range []string{"a fallback note", "press S for settings", "a split-device warning"} {
		if !strings.Contains(got, want) {
			t.Errorf("composed banner %q lost %q", got, want)
		}
	}
}

// TestSyncTrimBoundsAgree pins the one thing the two packages either side
// of the audio/visual trim cannot check for themselves. internal/ui states
// the bound the settings row enforces and deliberately knows nothing about
// the config file; internal/appconfig clamps what it stores and knows
// nothing about the UI. This file is the only place that imports both, so
// it is the only place the two numbers can be compared — and if they ever
// drift, the symptom is a setting the user can reach and the config
// silently refuses to keep.
func TestSyncTrimBoundsAgree(t *testing.T) {
	if ui.MaxSyncTrimMS != appconfig.MaxSyncTrimMS {
		t.Errorf("the settings row allows +/-%d ms and the config stores +/-%d ms",
			ui.MaxSyncTrimMS, appconfig.MaxSyncTrimMS)
	}
}

// TestShellPrefsSyncTrimRoundTrips checks the optional Prefs extension the
// settings row probes for: without it the row does not appear at all.
func TestShellPrefsSyncTrimRoundTrips(t *testing.T) {
	p := &shellPrefs{}
	var _ interface {
		SyncTrim() int
		SetSyncTrim(int)
	} = p
	if got := p.SyncTrim(); got != 0 {
		t.Errorf("a fresh config reports a %d ms trim, want 0", got)
	}
	p.SetSyncTrim(-35)
	if got := p.SyncTrim(); got != -35 {
		t.Errorf("got %d ms, want -35", got)
	}
}

// TestFramesToDuration checks the conversion the latency hook reports
// through, at the sample rate the whole pipeline runs at.
func TestFramesToDuration(t *testing.T) {
	for _, tt := range []struct {
		frames int
		want   time.Duration
	}{
		{0, 0},
		{sampleRate, time.Second},
		{sampleRate / 100, 10 * time.Millisecond},
		{480, 10 * time.Millisecond},
	} {
		if got := framesToDuration(tt.frames); got != tt.want {
			t.Errorf("framesToDuration(%d) = %v, want %v", tt.frames, got, tt.want)
		}
	}
}

// TestBytesPerFrameMatchesTheStreamFormat: BufferedSize counts bytes, and
// the latency hook divides by this to get frames. oto is opened as stereo
// float32, and the two have to agree or the compensation is out by a
// factor.
func TestBytesPerFrameMatchesTheStreamFormat(t *testing.T) {
	if bytesPerFrame != 8 {
		t.Errorf("bytesPerFrame is %d; oto is opened as 2 channels of float32, which is 8", bytesPerFrame)
	}
	// playerBufferBytes is built from the same unit, so the read-ahead it
	// asks for has to come back out as the duration it was written as.
	if got := framesToDuration(playerBufferBytes() / bytesPerFrame); got != playerReadAhead {
		t.Errorf("the configured read-ahead reads back as %v, want %v", got, playerReadAhead)
	}
}
