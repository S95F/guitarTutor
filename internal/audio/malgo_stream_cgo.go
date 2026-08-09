//go:build cgo

package audio

// #include <stdlib.h>
import "C"

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// OpenDuplex opens (but does not start) one full-duplex miniaudio device:
// mono float32 capture (the guitar) and stereo float32 playback in a
// single callback, shared-mode, low-latency profile — the D1 shape.
// miniaudio performs any channel/format/rate conversion to and from the
// device's native format, so the handler always sees the contract's mono
// in / stereo out at the negotiated rate.
func (b *malgoBackend) OpenDuplex(cfg StreamConfig, h DuplexHandler) (Stream, error) {
	if h == nil {
		return nil, fmt.Errorf("audio: OpenDuplex: nil handler")
	}
	if cfg.SampleRate < 0 || cfg.PeriodFrames < 0 {
		return nil, fmt.Errorf("audio: OpenDuplex: negative sample rate or period (%d, %d)", cfg.SampleRate, cfg.PeriodFrames)
	}
	want := withDefaults(cfg)

	dc := malgo.DefaultDeviceConfig(malgo.Duplex)
	dc.SampleRate = uint32(want.SampleRate)
	dc.PeriodSizeInFrames = uint32(want.PeriodFrames)
	dc.PerformanceProfile = malgo.LowLatency
	dc.Capture.Format = malgo.FormatF32
	dc.Capture.Channels = 1 // guitar is mono; miniaudio downmixes multi-channel interfaces
	dc.Capture.ShareMode = malgo.Shared
	dc.Playback.Format = malgo.FormatF32
	dc.Playback.Channels = 2
	dc.Playback.ShareMode = malgo.Shared

	// Non-default device IDs: decode the stable hex form back into
	// malgo's fixed-size array. DeviceID.Pointer copies the bytes to the
	// C heap; ma_device_init copies the ID into the device (ma_device
	// keeps its own ma_device_id), so the C copies are freed as soon as
	// InitDevice returns — the deferred free covers every return path
	// below because all of them are after InitDevice.
	var idPtrs []unsafe.Pointer
	defer func() {
		for _, p := range idPtrs {
			C.free(p)
		}
	}()
	if want.CaptureDevice != "" {
		id, err := decodeMalgoDeviceID(want.CaptureDevice)
		if err != nil {
			return nil, fmt.Errorf("audio: capture %w", err)
		}
		p := id.Pointer()
		idPtrs = append(idPtrs, p)
		dc.Capture.DeviceID = p
	}
	if want.PlaybackDevice != "" {
		id, err := decodeMalgoDeviceID(want.PlaybackDevice)
		if err != nil {
			return nil, fmt.Errorf("audio: playback %w", err)
		}
		p := id.Pointer()
		idPtrs = append(idPtrs, p)
		dc.Playback.DeviceID = p
	}

	s := &malgoStream{handler: h, cfg: want}
	// Preallocate the callback scratch to the requested period so the
	// steady state allocates nothing from the very first callback.
	s.scratch.grow(want.PeriodFrames)
	s.onStop.Store(func() {})

	dev, err := malgo.InitDevice(b.ctx.Context, dc, malgo.DeviceCallbacks{
		Data: s.data,
		Stop: s.deviceStopped,
	})
	if err != nil {
		return nil, fmt.Errorf("audio: open duplex on %s: %w", b.Name(), err)
	}
	s.dev = dev
	// Negotiated parameters: the device reports the granted sample rate.
	// malgo exposes no accessor for the granted period, but miniaudio's
	// fixed-size-callback default delivers exactly PeriodSizeInFrames per
	// callback; period additionally tracks the frame count each callback
	// actually delivers, so Config stays truthful even if a driver
	// deviates.
	s.cfg.SampleRate = int(dev.SampleRate())
	s.period.Store(int64(want.PeriodFrames))
	return s, nil
}

// decodeMalgoDeviceID parses a DeviceInfo.ID string into malgo's
// fixed-size device identifier.
func decodeMalgoDeviceID(s string) (malgo.DeviceID, error) {
	var id malgo.DeviceID
	raw, err := decodeDeviceID(s, len(id))
	if err != nil {
		return id, err
	}
	copy(id[:], raw)
	return id, nil
}

// malgoStream is an open duplex device. The zero value is not usable;
// OpenDuplex constructs it.
type malgoStream struct {
	handler DuplexHandler
	dev     *malgo.Device
	// cfg holds the negotiated parameters except PeriodFrames, which
	// lives in period (the callback updates it concurrently).
	cfg StreamConfig
	// period is the frames-per-callback most recently observed (seeded
	// with the requested period before the first callback).
	period atomic.Int64
	// onStop always holds a non-nil func(); SetOnStop swaps it.
	onStop atomic.Value
	// suppressStop is set for the duration of an explicit Stop/Close so
	// the resulting miniaudio stop notification is not reported as an
	// asynchronous device loss. miniaudio fires the notification on the
	// worker thread before Stop returns, so the window is exact in the
	// normal path; see StreamObserver for the best-effort caveat.
	suppressStop atomic.Bool
	scratch      duplexScratch

	mu     sync.Mutex
	closed bool
}

// data is the malgo DataProc — the realtime audio callback. It only
// reslices views, runs the handler, and copies the handler's outL/outR
// into the interleaved device buffer. It must not allocate: the single
// exception is duplexScratch's documented grow-only reallocation the first
// time the device delivers a period larger than any seen before.
func (s *malgoStream) data(out, in []byte, frames uint32) {
	n := int(frames)
	if n == 0 {
		return
	}
	s.period.Store(int64(n))
	if len(out) < 8*n { // 2 channels × 4 bytes; no playback buffer means nothing to fill
		return
	}
	var mono []float32
	if len(in) >= 4*n {
		mono = f32View(in, n)
	} else {
		mono = s.scratch.silence(n) // capture starved this period
	}
	l, r := s.scratch.stereo(n)
	s.handler(mono, l, r)
	interleaveStereo(f32View(out, 2*n), l, r)
}

// deviceStopped is the malgo StopProc, invoked on a backend thread
// whenever the device transitions to stopped.
func (s *malgoStream) deviceStopped() {
	if s.suppressStop.Load() {
		return
	}
	s.onStop.Load().(func())()
}

// SetOnStop implements StreamObserver.
func (s *malgoStream) SetOnStop(f func()) {
	if f == nil {
		f = func() {}
	}
	s.onStop.Store(f)
}

// Start begins callback delivery.
func (s *malgoStream) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("audio: Start on closed stream")
	}
	if err := s.dev.Start(); err != nil {
		return fmt.Errorf("audio: start duplex device: %w", err)
	}
	return nil
}

// Stop pauses the device; Start may be called again.
func (s *malgoStream) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("audio: Stop on closed stream")
	}
	s.suppressStop.Store(true)
	err := s.dev.Stop()
	s.suppressStop.Store(false)
	if err != nil {
		return fmt.Errorf("audio: stop duplex device: %w", err)
	}
	return nil
}

// Close releases the device. Closing twice is a no-op; the stream cannot
// be restarted.
func (s *malgoStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.suppressStop.Store(true)
	s.onStop.Store(func() {})
	s.dev.Uninit() // stops the device if started, then frees it
	return nil
}

// Config reports the negotiated parameters: the sample rate granted by the
// device at init and the period the callbacks actually deliver (the
// requested period until the first callback lands).
func (s *malgoStream) Config() StreamConfig {
	cfg := s.cfg
	cfg.PeriodFrames = int(s.period.Load())
	return cfg
}
