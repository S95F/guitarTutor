package audio

import "unsafe"

// Defaults a backend applies when the corresponding StreamConfig field is
// zero.
const (
	// DefaultSampleRate is the project-wide standard rate (ROADMAP:
	// "Audio pipeline standardized on 48 kHz float32 stereo").
	DefaultSampleRate = 48000
	// DefaultPeriodFrames is 10 ms at 48 kHz — the top of the 256–480
	// frame range ROADMAP Phase 2 targets, chosen because WASAPI
	// shared-mode capture rarely delivers periods below ~10 ms anyway
	// (ROADMAP "Known risks").
	DefaultPeriodFrames = 480
)

// withDefaults returns cfg with zero-valued SampleRate and PeriodFrames
// resolved to the documented defaults. Device IDs pass through unchanged
// (empty already means "system default").
func withDefaults(cfg StreamConfig) StreamConfig {
	if cfg.SampleRate == 0 {
		cfg.SampleRate = DefaultSampleRate
	}
	if cfg.PeriodFrames == 0 {
		cfg.PeriodFrames = DefaultPeriodFrames
	}
	return cfg
}

// duplexScratch holds the preallocated buffers the realtime audio callback
// uses to present miniaudio's interleaved stereo output buffer as the
// separate outL/outR slices the DuplexHandler contract promises, plus a
// silent mono buffer handed out as capture input for any period where the
// device delivers no capture data.
//
// The buffers grow but never shrink: they are sized at stream-open time to
// the negotiated period, and reallocated only if the device ever delivers a
// larger period than any seen before. miniaudio's fixed-size-callback
// default makes that rare — but a driver may resize periods after a
// default-device switch or sample-rate change, and the alternative to the
// one-time allocation would be dropping the period. Every steady-state
// callback is allocation-free.
type duplexScratch struct {
	outL, outR []float32
	// quiet stays all zeros for its lifetime: it is only ever handed to
	// the handler as the read-only `in` slice, which the DuplexHandler
	// contract forbids writing to.
	quiet []float32
}

// grow ensures the scratch buffers can serve periods of n frames,
// allocating only when n exceeds every previously seen period.
func (s *duplexScratch) grow(n int) {
	if n <= len(s.outL) {
		return
	}
	s.outL = make([]float32, n)
	s.outR = make([]float32, n)
	s.quiet = make([]float32, n) // fresh allocations are already zeroed
}

// stereo returns the left/right scratch resliced to exactly n frames.
func (s *duplexScratch) stereo(n int) (l, r []float32) {
	s.grow(n)
	return s.outL[:n], s.outR[:n]
}

// silence returns n frames of zeros, used as the capture input when the
// device delivered none.
func (s *duplexScratch) silence(n int) []float32 {
	s.grow(n)
	return s.quiet[:n]
}

// interleaveStereo packs l and r into dst as L0 R0 L1 R1 …, the frame
// layout miniaudio expects in a 2-channel device buffer. len(r) must equal
// len(l) and dst must hold at least 2*len(l) samples; the realtime callback
// guarantees both.
func interleaveStereo(dst, l, r []float32) {
	if len(l) == 0 {
		return
	}
	_ = r[len(l)-1]
	_ = dst[2*len(l)-1]
	for i, v := range l {
		dst[2*i] = v
		dst[2*i+1] = r[i]
	}
}

// f32View reinterprets the first 4*n bytes of b as n float32 samples
// without copying. miniaudio hands the data callback raw bytes that are
// float32 frames in host byte order (the backend requests FormatF32 on
// both sides of the duplex device), so a reinterpreting view is exact and
// keeps the callback allocation- and copy-free. The view aliases b and is
// only valid while b is — i.e. for the duration of the callback.
func f32View(b []byte, n int) []float32 {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), n)
}
