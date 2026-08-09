package live

import (
	"sync"
	"testing"
)

func TestRingBasicReadWrite(t *testing.T) {
	g := newRing(8)
	g.write([]float32{1, 2, 3})
	dst := make([]float32, 8)
	n, start := g.read(dst)
	if n != 3 || start != 0 {
		t.Fatalf("read = (%d, %d), want (3, 0)", n, start)
	}
	for i, want := range []float32{1, 2, 3} {
		if dst[i] != want {
			t.Errorf("dst[%d] = %v, want %v", i, dst[i], want)
		}
	}
	// Stream index continues across writes.
	g.write([]float32{4, 5})
	n, start = g.read(dst)
	if n != 2 || start != 3 {
		t.Fatalf("read = (%d, %d), want (2, 3)", n, start)
	}
}

func TestRingWrap(t *testing.T) {
	g := newRing(4) // capacity 4
	for round := 0; round < 10; round++ {
		g.write([]float32{float32(round), float32(round) + 0.5})
		dst := make([]float32, 4)
		n, _ := g.read(dst)
		if n != 2 || dst[0] != float32(round) || dst[1] != float32(round)+0.5 {
			t.Fatalf("round %d: read %d samples %v", round, n, dst[:n])
		}
	}
}

func TestRingOverflowDropsNewest(t *testing.T) {
	g := newRing(4)
	g.write([]float32{1, 2, 3, 4})
	g.write([]float32{5, 6}) // full: the new samples are dropped
	if d := g.Dropped(); d != 2 {
		t.Fatalf("Dropped = %d, want 2", d)
	}
	dst := make([]float32, 8)
	n, start := g.read(dst)
	if n != 4 || start != 0 {
		t.Fatalf("read = (%d, %d), want (4, 0)", n, start)
	}
	for i, want := range []float32{1, 2, 3, 4} {
		if dst[i] != want {
			t.Errorf("dst[%d] = %v, want %v", i, dst[i], want)
		}
	}
	// Space freed by the read accepts new data again.
	g.write([]float32{7})
	if n, start = g.read(dst); n != 1 || start != 4 || dst[0] != 7 {
		t.Fatalf("post-drop write: read = (%d, %d, %v)", n, start, dst[0])
	}
}

func TestRingConcurrentStream(t *testing.T) {
	g := newRing(1 << 12)
	const total = 1 << 18
	producerDone := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(producerDone)
		buf := make([]float32, 512) // divides total exactly
		v := float32(0)
		for sent := 0; sent < total; sent += len(buf) {
			for i := range buf {
				buf[i] = v
				v++
			}
			g.write(buf)
		}
	}()

	// The consumer verifies every sample it sees is strictly increasing
	// (values are sequential at the producer, so any tear, duplication,
	// or reorder shows as a non-increase) and accounts for every value
	// it does NOT see: skipped values are legal only as overflow drops,
	// which the ring counts. A skip can surface anywhere, including
	// inside a single read — the producer estimates free space against
	// a possibly stale read cursor, so an overflowing write may drop
	// without leaving the ring exactly full, unaligning drop boundaries
	// from read boundaries.
	dst := make([]float32, 1024)
	next := float32(0) // the value expected were nothing dropped
	var missing, readCount int64
	for {
		n, _ := g.read(dst)
		if n == 0 {
			select {
			case <-producerDone:
				if n, _ = g.read(dst); n == 0 {
					goto donereading
				}
			default:
				continue
			}
		}
		if dst[0] < next {
			t.Fatalf("stream went backwards: %v after %v", dst[0], next-1)
		}
		missing += int64(dst[0] - next)
		for i := 1; i < n; i++ {
			if dst[i] <= dst[i-1] {
				t.Fatalf("non-increase inside a read at %d: %v -> %v", i, dst[i-1], dst[i])
			}
			missing += int64(dst[i] - dst[i-1] - 1)
		}
		next = dst[n-1] + 1
		readCount += int64(n)
	}
donereading:
	wg.Wait()
	missing += total - int64(next) // values after the last one seen
	// Every produced sample was either delivered in order or counted as
	// dropped — none duplicated, none torn.
	if got := readCount + g.Dropped(); got != total {
		t.Errorf("read %d + dropped %d = %d, want %d", readCount, g.Dropped(), got, int64(total))
	}
	if missing != g.Dropped() {
		t.Errorf("values missing from the stream = %d, want the dropped count %d", missing, g.Dropped())
	}
}
