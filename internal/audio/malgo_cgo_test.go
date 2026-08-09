//go:build cgo

package audio

import (
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
)

// These tests are the hardware-gated tier: they need a real audio backend
// (context init succeeds) and, for the duplex tests, at least one capture
// and one playback endpoint. On a deviceless CI runner they skip — except
// TestMalgoNullBackend, which runs the same full open/start/stop path on
// miniaudio's device-free null backend and should pass anywhere.

// newHardwareBackend initializes the real platform backend, skipping the
// test when none is available (headless CI).
func newHardwareBackend(t *testing.T) *malgoBackend {
	t.Helper()
	b, err := newMalgoBackend()
	if err != nil {
		t.Skipf("no real audio backend on this machine: %v", err)
	}
	t.Cleanup(b.close)
	return b
}

// duplexStats is what runDuplex measured over a stream run.
type duplexStats struct {
	frames     int64 // total frames delivered to the handler
	mismatches int64 // callbacks where len(in), len(outL), len(outR) disagreed
	stops      int64 // asynchronous stop notifications observed
}

// runDuplex opens a duplex stream on b, runs it for d, and returns the
// stats. The handler counts frames, verifies the length contract, and
// plays a quiet 220 Hz sine so a locally listening developer can tell the
// stream is really live. Open failures skip (devices can be absent or
// exclusively claimed); everything after a successful open is asserted.
func runDuplex(t *testing.T, b *malgoBackend, cfg StreamConfig, d time.Duration) duplexStats {
	t.Helper()

	var stats duplexStats
	var frames, mismatches, stops atomic.Int64
	var phase float64
	handler := func(in, outL, outR []float32) {
		if len(in) != len(outL) || len(in) != len(outR) {
			mismatches.Add(1)
		}
		frames.Add(int64(len(in)))
		const step = 2 * math.Pi * 220 / 48000
		for i := range outL {
			v := float32(0.05 * math.Sin(phase))
			phase += step
			outL[i] = v
			outR[i] = v
		}
	}

	stream, err := b.OpenDuplex(cfg, handler)
	if err != nil {
		t.Skipf("OpenDuplex on %s: %v", b.Name(), err)
	}
	defer func() {
		if err := stream.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	obs, ok := stream.(StreamObserver)
	if !ok {
		t.Fatalf("stream %T does not implement StreamObserver", stream)
	}
	obs.SetOnStop(func() { stops.Add(1) })

	got := stream.Config()
	if got.SampleRate <= 0 {
		t.Errorf("negotiated SampleRate = %d, want > 0", got.SampleRate)
	}
	if got.PeriodFrames <= 0 {
		t.Errorf("negotiated PeriodFrames = %d, want > 0", got.PeriodFrames)
	}

	if err := stream.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(d)
	if err := stream.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	after := stream.Config()
	t.Logf("%s: negotiated %d Hz, period %d frames (requested %d Hz, %d frames)",
		b.Name(), after.SampleRate, after.PeriodFrames, cfg.SampleRate, cfg.PeriodFrames)

	stats.frames = frames.Load()
	stats.mismatches = mismatches.Load()
	stats.stops = stops.Load()
	return stats
}

// checkDuplexStats asserts what every healthy run must show: real frame
// flow, the length contract intact, and no spurious async-stop reports
// from the test's own Stop/Close calls.
func checkDuplexStats(t *testing.T, stats duplexStats, d time.Duration, sampleRate int) {
	t.Helper()
	if stats.frames == 0 {
		t.Error("handler saw 0 frames; the callback never ran")
	}
	// Expect roughly d worth of frames; allow a factor-4 cushion for
	// startup latency and scheduler jitter rather than a tight bound.
	min := int64(float64(sampleRate) * d.Seconds() / 4)
	if stats.frames < min {
		t.Errorf("handler saw %d frames over %v at %d Hz, want at least %d", stats.frames, d, sampleRate, min)
	}
	if stats.mismatches != 0 {
		t.Errorf("%d callbacks broke len(in) == len(outL) == len(outR)", stats.mismatches)
	}
	if stats.stops != 0 {
		t.Errorf("explicit Stop/Close reported %d asynchronous stop notifications, want 0", stats.stops)
	}
}

func TestMalgoBackendHardware(t *testing.T) {
	b := newHardwareBackend(t)

	name := b.Name()
	if len(name) < len("miniaudio/") || name[:len("miniaudio/")] != "miniaudio/" {
		t.Errorf("Name() = %q, want a miniaudio/<backend> form", name)
	}
	t.Logf("active backend: %s", name)

	capture, playback, err := b.Devices()
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	for _, d := range capture {
		t.Logf("capture:  %q default=%v id=%s…", d.Name, d.Default, truncate(d.ID, 24))
	}
	for _, d := range playback {
		t.Logf("playback: %q default=%v id=%s…", d.Name, d.Default, truncate(d.ID, 24))
	}

	// Every enumerated ID must survive the decode the device picker will
	// eventually feed back through OpenDuplex.
	for _, d := range append(append([]DeviceInfo{}, capture...), playback...) {
		if d.Name == "" {
			t.Errorf("device %s has an empty name", d.ID)
		}
		id, err := decodeMalgoDeviceID(d.ID)
		if err != nil {
			t.Errorf("device %q: ID does not decode: %v", d.Name, err)
			continue
		}
		if again := encodeDeviceID(id[:]); again != d.ID {
			t.Errorf("device %q: ID round trip %q != %q", d.Name, again, d.ID)
		}
	}
}

func TestMalgoDuplexHardware(t *testing.T) {
	b := newHardwareBackend(t)
	stats := runDuplex(t, b, StreamConfig{}, 300*time.Millisecond)
	checkDuplexStats(t, stats, 300*time.Millisecond, DefaultSampleRate)
}

// TestMalgoDuplexExplicitDevices feeds the default devices' own enumerated
// IDs back through StreamConfig, exercising the hex decode path against
// real identifiers instead of the empty "use default" fast path.
func TestMalgoDuplexExplicitDevices(t *testing.T) {
	b := newHardwareBackend(t)
	capture, playback, err := b.Devices()
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	cfg := StreamConfig{}
	for _, d := range capture {
		if d.Default {
			cfg.CaptureDevice = d.ID
		}
	}
	for _, d := range playback {
		if d.Default {
			cfg.PlaybackDevice = d.ID
		}
	}
	if cfg.CaptureDevice == "" || cfg.PlaybackDevice == "" {
		t.Skipf("no default capture+playback pair (capture %d, playback %d devices)", len(capture), len(playback))
	}
	stats := runDuplex(t, b, cfg, 300*time.Millisecond)
	checkDuplexStats(t, stats, 300*time.Millisecond, DefaultSampleRate)
}

// TestMalgoNullBackend runs the whole context → enumerate → duplex path on
// miniaudio's null backend, which needs no audio hardware: this is the
// tier that still exercises the real malgo plumbing on a deviceless CI
// runner. The null backend is timer-driven, so frames flow in real time.
// (malgoBackendNull, not malgo.BackendNull — see its doc comment for the
// upstream enum mismatch.)
func TestMalgoNullBackend(t *testing.T) {
	b, err := newMalgoBackendFrom([]malgo.Backend{malgoBackendNull})
	if err != nil {
		// The null backend should initialize everywhere; treat failure
		// as an environment oddity, not a code bug.
		t.Skipf("null backend unavailable: %v", err)
	}
	t.Cleanup(b.close)

	if got, want := b.Name(), "miniaudio/null"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}

	capture, playback, err := b.Devices()
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	t.Logf("null backend devices: %d capture, %d playback", len(capture), len(playback))

	// Invalid stored IDs must fail fast, before any device is touched.
	if _, err := b.OpenDuplex(StreamConfig{CaptureDevice: "not-hex"}, func(in, outL, outR []float32) {}); err == nil {
		t.Error("OpenDuplex with invalid capture ID succeeded, want error")
	}

	stats := runDuplex(t, b, StreamConfig{}, 300*time.Millisecond)
	checkDuplexStats(t, stats, 300*time.Millisecond, DefaultSampleRate)
}

// TestAvailable exercises the package entry point the app uses. It cannot
// assert non-nil (deviceless CI), but when a backend comes back it must be
// the miniaudio one, and repeated calls must return the same instance.
func TestAvailable(t *testing.T) {
	b := Available()
	if b == nil {
		t.Skip("Available() = nil: no real audio backend on this machine")
	}
	if b != Available() {
		t.Error("Available() returned different instances across calls")
	}
	if _, ok := b.(*malgoBackend); !ok {
		t.Errorf("Available() = %T, want *malgoBackend", b)
	}
	t.Logf("Available backend: %s", b.Name())
}

// truncate shortens s for log lines.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
