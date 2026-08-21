package audio

import "unsafe"

const (
	DefaultSampleRate = 48000

	DefaultPeriodFrames = 480
)

func withDefaults(cfg StreamConfig) StreamConfig {
	if cfg.SampleRate == 0 {
		cfg.SampleRate = DefaultSampleRate
	}
	if cfg.PeriodFrames == 0 {
		cfg.PeriodFrames = DefaultPeriodFrames
	}
	return cfg
}

type duplexScratch struct {
	outL, outR []float32

	quiet []float32
}

func (s *duplexScratch) grow(n int) {
	if n <= len(s.outL) {
		return
	}
	s.outL = make([]float32, n)
	s.outR = make([]float32, n)
	s.quiet = make([]float32, n)
}

func (s *duplexScratch) stereo(n int) (l, r []float32) {
	s.grow(n)
	return s.outL[:n], s.outR[:n]
}

func (s *duplexScratch) silence(n int) []float32 {
	s.grow(n)
	return s.quiet[:n]
}

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

func f32View(b []byte, n int) []float32 {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), n)
}
