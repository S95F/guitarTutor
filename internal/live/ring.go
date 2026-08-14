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
	// dropAt is the accepted-stream position of the most recent overflow:
	// the write cursor at the instant of loss. Published BEFORE dropped,
	// so a consumer that loads Dropped and then dropPos observes a
	// position at or after any loss the count includes — never one from
	// before it.
	dropAt atomic.Int64
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
		// Position first, then count. The consumer reads in the inverse
		// order (Dropped, then dropPos), so once it observes the new
		// count, the fill point of that loss is already visible. w+n is
		// exact: a drop means the ring is full at w+n, and w cannot
		// advance again until the consumer frees space — so this value,
		// not whatever w has grown to by the time the loss is noticed,
		// is where the zeroed splice belongs. Inferring the position
		// from the live write cursor instead attributed a late-noticed
		// drop to a cursor that had already moved past the loss,
		// splicing the gap AFTER capture that came after it (audit D5).
		g.dropAt.Store(w + n)
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

// dropPos reports the accepted-stream position of the most recent
// overflow; meaningful only once Dropped reports a loss. Load Dropped
// first — dropAt is published before dropped, so the (Dropped, dropPos)
// pair read in that order never places a loss earlier than it happened.
func (g *ring) dropPos() int64 { return g.dropAt.Load() }

// buffered reports the samples currently waiting to be read.
func (g *ring) buffered() int64 { return g.w.Load() - g.r.Load() }
