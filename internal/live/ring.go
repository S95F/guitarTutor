package live

import "sync/atomic"

// ring is a single-producer single-consumer lock-free ring buffer of
// float32 samples: the audio callback writes, the analysis goroutine
// reads. Capacity is fixed at construction; writes that would overflow
// drop their own excess (newest data). Dropping newest — not oldest —
// is what keeps the ring race-free: only the consumer ever advances the
// read cursor, so an in-flight read can never be torn by the producer.
// The analysis side is best-effort; losing samples under load beats
// blocking the audio thread.
type ring struct {
	buf  []float32
	mask int64
	// w and r are monotonically increasing sample counts; the cursor
	// into buf is count & mask. w is written only by the producer, r
	// only by the consumer.
	w atomic.Int64
	r atomic.Int64
	// dropped counts samples lost to overflow (producer-side).
	dropped atomic.Int64
}

// newRing rounds capacity up to a power of two.
func newRing(capacity int) *ring {
	n := 1
	for n < capacity {
		n <<= 1
	}
	return &ring{buf: make([]float32, n), mask: int64(n - 1)}
}

// write copies samples in; called from the audio thread. Never blocks and
// never allocates. When the ring is full the excess (newest) samples are
// dropped and counted; the consumer can detect loss via Dropped.
func (g *ring) write(samples []float32) {
	w := g.w.Load()
	r := g.r.Load()
	n := int64(len(g.buf)) - (w - r) // free space
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
		// Counted only after publishing w: a consumer that observes
		// the new dropped count therefore also sees the w the ring
		// filled up at, which is exactly where the lost stretch
		// belongs in the accepted stream (Session's analyzer splices
		// a zeroed gap there to keep the detector on the device
		// clock).
		g.dropped.Add(drop)
	}
}

// read copies up to len(dst) available samples into dst and returns the
// count along with the stream index of dst[0] (total samples produced
// before it). Called from the consumer goroutine.
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

// Dropped reports samples lost to overflow since construction.
func (g *ring) Dropped() int64 { return g.dropped.Load() }

// accepted reports the total samples ever accepted into the ring (the
// write cursor). accepted() + Dropped() is the device-clock position of
// the capture stream.
func (g *ring) accepted() int64 { return g.w.Load() }

// buffered reports the samples currently waiting to be read.
func (g *ring) buffered() int64 { return g.w.Load() - g.r.Load() }
