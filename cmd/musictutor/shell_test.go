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

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/audio"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/ui"
)

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

func useStubBackend(t *testing.T, b audio.Backend) {
	t.Helper()
	prev := liveBackend
	liveBackend = func() (audio.Backend, error) { return b, nil }
	t.Cleanup(func() { liveBackend = prev })
}

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

func TestShellPrefsRaceSaveAgainstStoreOffset(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())

	p := &shellPrefs{}

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

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			p.SetCountIn(i % 5)
			if err := p.Save(); err != nil {
				errs <- fmt.Errorf("game-loop Save: %w", err)
			}
		}
	}()

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

	if _, _, err := a.Calibrate("cap-usb", "pb-usb", nil); errors.Is(err, errCalibrationBusy) {
		t.Error("Calibrate still refused after the previous pass finished: the guard leaked")
	}
	if n := backend.openCount(); n != 2 {
		t.Errorf("backend saw %d opens, want 2 (the retry reached the device)", n)
	}
}

func oneBarGtab(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "piece.gtab")
	const src = "\\tempo 120\n\\time 4/4\n2.5.4 0.5.4 2.5.2 |\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestOpenClosesPreviousSession(t *testing.T) {
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

func TestSyncTrimBoundsAgree(t *testing.T) {
	if ui.MaxSyncTrimMS != appconfig.MaxSyncTrimMS {
		t.Errorf("the settings row allows +/-%d ms and the config stores +/-%d ms",
			ui.MaxSyncTrimMS, appconfig.MaxSyncTrimMS)
	}
}

func TestCountInBoundsAgree(t *testing.T) {
	if ui.MaxCountIn != appconfig.MaxCountInBeats {
		t.Errorf("the settings row offers 0-%d beats and the config stores 0-%d",
			ui.MaxCountIn, appconfig.MaxCountInBeats)
	}
}

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

func TestBytesPerFrameMatchesTheStreamFormat(t *testing.T) {
	if bytesPerFrame != 8 {
		t.Errorf("bytesPerFrame is %d; oto is opened as 2 channels of float32, which is 8", bytesPerFrame)
	}

	if got := framesToDuration(playerBufferBytes / bytesPerFrame); got != playerReadAhead {
		t.Errorf("the configured read-ahead reads back as %v, want %v", got, playerReadAhead)
	}
}

func TestEditPieceOpensBrokenGtabInTheEditor(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.gtab")
	if err := os.WriteFile(broken, []byte("\tempo nope\n0.6.1 |\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prefs := &shellPrefs{}
	o := &shellOpener{prefs: prefs}
	sh, browser := ui.NewBrowserShell(ui.Services{Opener: o, Prefs: prefs, Library: pieceLibrary{}})
	o.shell, o.browser = sh, browser
	t.Cleanup(o.CloseCurrent)

	if got := sh.Depth(); got != 1 {
		t.Fatalf("the fresh shell is %d deep, want 1 (the browser)", got)
	}
	o.editPiece(broken)
	if err := sh.Update(); err != nil {
		t.Fatalf("draining the shell: %v", err)
	}
	if got := sh.Depth(); got != 2 {
		t.Fatalf("editPiece on a broken .gtab left the shell %d deep, want 2 — the editor never opened", got)
	}

	o.editPiece(filepath.Join(dir, "gone.gtab"))
	if err := sh.Update(); err != nil {
		t.Fatalf("draining the shell: %v", err)
	}
	if got := sh.Depth(); got != 2 {
		t.Errorf("editPiece on a missing file changed the stack to %d deep, want it untouched at 2", got)
	}
}

const uneditableMusicXML = `<?xml version="1.0" encoding="UTF-8"?>
<score-partwise version="3.1">
  <part-list><score-part id="P1"><part-name>Guitar</part-name></score-part></part-list>
  <part id="P1">
    <measure number="1">
      <attributes>
        <divisions>7</divisions>
        <time><beats>4</beats><beat-type>4</beat-type></time>
      </attributes>
      <note><pitch><step>E</step><octave>2</octave></pitch><duration>3</duration></note>
      <note><rest/><duration>25</duration></note>
    </measure>
  </part>
</score-partwise>
`

func TestEditPieceUneditableImportStaysOnTheBrowser(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())
	path := filepath.Join(t.TempDir(), "odd.musicxml")
	if err := os.WriteFile(path, []byte(uneditableMusicXML), 0o644); err != nil {
		t.Fatal(err)
	}

	sc, _, err := load(path)
	if err != nil {
		t.Fatalf("the fixture stopped loading: %v", err)
	}
	if _, err := ui.NewEditorFor(nil, sc, path); err == nil {
		t.Skip("the editor now opens this piece; the refusal path needs a new fixture")
	}

	prefs := &shellPrefs{}
	o := &shellOpener{prefs: prefs}
	sh, browser := ui.NewBrowserShell(ui.Services{Opener: o, Prefs: prefs, Library: pieceLibrary{}})
	o.shell, o.browser = sh, browser
	t.Cleanup(o.CloseCurrent)

	o.editPiece(path)
	if err := sh.Update(); err != nil {
		t.Fatalf("draining the shell: %v", err)
	}
	if got := sh.Depth(); got != 1 {
		t.Fatalf("editPiece on an uneditable import left the shell %d deep, want 1 (no dead editor)", got)
	}
}

func TestPractiseFromEditorFailureStaysOnTheEditor(t *testing.T) {
	t.Setenv(appconfig.EnvConfigDir, t.TempDir())
	prefs := &shellPrefs{}
	o := &shellOpener{prefs: prefs}
	sh, browser := ui.NewBrowserShell(ui.Services{Opener: o, Prefs: prefs, Library: pieceLibrary{}})
	o.shell, o.browser = sh, browser
	t.Cleanup(o.CloseCurrent)

	ed := ui.NewEditor(sh)
	o.installEditor(ed)
	if err := sh.Update(); err != nil {
		t.Fatalf("draining the shell: %v", err)
	}
	if got := sh.Depth(); got != 2 {
		t.Fatalf("installEditor left the shell %d deep, want 2 (browser + editor)", got)
	}

	o.practiseFromEditor(ed, filepath.Join(t.TempDir(), "gone.gtab"))
	if err := sh.Update(); err != nil {
		t.Fatalf("draining the shell: %v", err)
	}
	if got := sh.Depth(); got != 2 {
		t.Errorf("a failed practice open changed the stack to %d deep, want the editor untouched at 2", got)
	}
}

func TestSoundFontAdoptionMarksOnTheGameLoop(t *testing.T) {
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

	o.adoptSoundFont("picked.sf2")
	if got := prefs.SoundFont(); got != "picked.sf2" {
		t.Errorf("adopted SoundFont = %q, want picked.sf2", got)
	}
	if cfg, err := appconfig.Load(); err != nil || cfg.SoundFontPath != "picked.sf2" {
		t.Errorf("persisted SoundFont = %q (err %v), want picked.sf2 saved", cfg.SoundFontPath, err)
	}
	if !o.sfPicked.Load() {
		t.Fatal("adoptSoundFont did not arm the settings-changed mark")
	}

	sc := oneBarScore(t)
	app := ui.New(newEngine(sc, engine.Options{}), sc, 0)
	app.SetReloader(func() {})
	o.drainSettingsMark(app)
	if o.sfPicked.Load() {
		t.Error("drainSettingsMark left the mark armed")
	}

	prefs.SetSoundFont("")
	o.sfPicked.Store(true)
	if _, _, err := o.Open(oneBarGtab(t)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if o.sfPicked.Load() {
		t.Error("Open left a pre-open pick armed; the fresh piece would offer a pointless reload")
	}
}
