package live

import "sync/atomic"

type ring struct {
	buf  []float32
	mask int64

	w atomic.Int64
	r atomic.Int64

	dropped atomic.Int64

	dropAt atomic.Int64
}

func newRing(capacity int) *ring {
	n := 1
	for n < capacity {
		n <<= 1
	}
	return &ring{buf: make([]float32, n), mask: int64(n - 1)}
}

func (g *ring) write(samples []float32) {
	w := g.w.Load()
	r := g.r.Load()
	n := int64(len(g.buf)) - (w - r)
	drop := int64(0)
	if need := int64(len(samples)); need <= n {
		n = need
	} else {
		drop = need - n
	}
	for i := int64(0); i < n; i++ {
		g.buf[(w+i)&g.mask] = samples[i]
	}
	g.w.Store(w + n)
	if drop > 0 {

		g.dropAt.Store(w + n)
		g.dropped.Add(drop)
	}
}

func (g *ring) read(dst []float32) (n int, start int64) {
	r := g.r.Load()
	w := g.w.Load()
	avail := w - r
	if avail == 0 {
		return 0, r
	}
	if avail > int64(len(dst)) {
		avail = int64(len(dst))
	}
	for i := int64(0); i < avail; i++ {
		dst[i] = g.buf[(r+i)&g.mask]
	}
	g.r.Store(r + avail)
	return int(avail), r
}

func (g *ring) Dropped() int64 { return g.dropped.Load() }

func (g *ring) dropPos() int64 { return g.dropAt.Load() }

func (g *ring) buffered() int64 { return g.w.Load() - g.r.Load() }
