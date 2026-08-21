package pitchml

import (
	"fmt"
	"math"
	"slices"

	"github.com/S95F/musicTutor/internal/pitch"
)

type Estimator interface {
	pitch.F0Estimator

	Err() error

	Close() error
}

type modelRunner interface {
	name() string

	resize(samples int) (int, error)

	input() []float32

	run() (pitchHz, confidence []float32, err error)

	close() error
}

const (
	minModelSamples = 256

	selectConfidenceRatio = 0.8
)

type estimator struct {
	r   modelRunner
	dec *decimator

	winLen int
	dsLen  int

	srcOff, dstOff, copyLen int

	ds      []float32
	scratch []float64
	err     error
	closed  bool
}

func newEstimator(r modelRunner, sampleRate int) (*estimator, error) {
	dec, err := newDecimator(sampleRate)
	if err != nil {
		return nil, err
	}
	return &estimator{
		r:       r,
		dec:     dec,
		scratch: make([]float64, 0, 128),
	}, nil
}

func (e *estimator) Name() string { return e.r.name() }

func (e *estimator) Err() error { return e.err }

func (e *estimator) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	return e.r.close()
}

func (e *estimator) EstimateF0(window []float32) (float64, float64) {
	if e.closed || len(window) == 0 {
		return 0, 0
	}
	if len(window) != e.winLen {
		if err := e.reshape(len(window)); err != nil {
			e.err = err
			return 0, 0
		}
	}
	if e.dsLen == 0 {
		return 0, 0
	}

	ds := e.dec.decimate(window, e.ds)
	in := e.r.input()
	if len(in) < e.dstOff+e.copyLen || len(ds) < e.srcOff+e.copyLen {

		e.err = fmt.Errorf("pitchml: model input buffer is %d samples, too short for the %d it accepted", len(in), e.dstOff+e.copyLen)
		e.dsLen = 0
		return 0, 0
	}
	copy(in[e.dstOff:e.dstOff+e.copyLen], ds[e.srcOff:e.srcOff+e.copyLen])

	pitchHz, confidence, err := e.r.run()
	if err != nil {
		e.err = err
		return 0, 0
	}
	return e.reduce(pitchHz, confidence)
}

func (e *estimator) reshape(n int) error {
	e.winLen = n
	e.dsLen = e.dec.outLen(n)
	if e.dsLen <= 0 {
		return fmt.Errorf("pitchml: analysis window of %d samples is shorter than the %d:1 decimation ratio", n, e.dec.ratio)
	}

	want := e.dsLen
	if want < minModelSamples {
		want = minModelSamples
	}
	got, err := e.r.resize(want)
	if err != nil {
		e.dsLen = 0
		return err
	}
	if got <= 0 {
		e.dsLen = 0
		return fmt.Errorf("pitchml: model adopted an input length of %d samples", got)
	}

	if cap(e.ds) < e.dsLen {
		e.ds = make([]float32, e.dsLen)
	}
	e.ds = e.ds[:e.dsLen]

	e.copyLen = e.dsLen
	e.srcOff = 0
	if e.copyLen > got {
		e.srcOff = (e.copyLen - got) / 2
		e.copyLen = got
	}
	e.dstOff = (got - e.copyLen) / 2

	in := e.r.input()
	for i := range in {
		in[i] = 0
	}
	return nil
}

func (e *estimator) reduce(pitchHz, confidence []float32) (float64, float64) {
	n := min(len(pitchHz), len(confidence))
	if n == 0 {
		return 0, 0
	}

	best := math.Inf(-1)
	for i := range n {
		if c := float64(confidence[i]); c > best {
			best = c
		}
	}
	if !(best > 0) {
		return 0, 0
	}
	clarity := min(best, 1)

	cut := selectConfidenceRatio * best
	sel := e.scratch[:0]
	for i := range n {
		if c := float64(confidence[i]); !(c >= cut) {
			continue
		}
		f := float64(pitchHz[i])
		if !(f > 0) || math.IsInf(f, 0) {
			continue
		}
		sel = append(sel, f)
	}
	e.scratch = sel
	if len(sel) == 0 {
		return 0, clarity
	}
	slices.Sort(sel)
	return median(sel), clarity
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return 0.5 * (sorted[n/2-1] + sorted[n/2])
}
