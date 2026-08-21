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
	dc.Capture.Channels = 1
	dc.Capture.ShareMode = malgo.Shared
	dc.Playback.Format = malgo.FormatF32
	dc.Playback.Channels = 2
	dc.Playback.ShareMode = malgo.Shared

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

	s.cfg.SampleRate = int(dev.SampleRate())
	s.period.Store(int64(want.PeriodFrames))
	return s, nil
}

func decodeMalgoDeviceID(s string) (malgo.DeviceID, error) {
	var id malgo.DeviceID
	raw, err := decodeDeviceID(s, len(id))
	if err != nil {
		return id, err
	}
	copy(id[:], raw)
	return id, nil
}

type malgoStream struct {
	handler DuplexHandler
	dev     *malgo.Device

	cfg StreamConfig

	period atomic.Int64

	onStop atomic.Value

	suppressStop atomic.Bool
	scratch      duplexScratch

	mu     sync.Mutex
	closed bool
}

func (s *malgoStream) data(out, in []byte, frames uint32) {
	n := int(frames)
	if n == 0 {
		return
	}
	s.period.Store(int64(n))
	if len(out) < 8*n {
		return
	}
	var mono []float32
	if len(in) >= 4*n {
		mono = f32View(in, n)
	} else {
		mono = s.scratch.silence(n)
	}
	l, r := s.scratch.stereo(n)
	s.handler(mono, l, r)
	interleaveStereo(f32View(out, 2*n), l, r)
}

func (s *malgoStream) deviceStopped() {
	if s.suppressStop.Load() {
		return
	}
	s.onStop.Load().(func())()
}

func (s *malgoStream) SetOnStop(f func()) {
	if f == nil {
		f = func() {}
	}
	s.onStop.Store(f)
}

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

func (s *malgoStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.suppressStop.Store(true)
	s.onStop.Store(func() {})
	s.dev.Uninit()
	return nil
}

func (s *malgoStream) Config() StreamConfig {
	cfg := s.cfg
	cfg.PeriodFrames = int(s.period.Load())
	return cfg
}
