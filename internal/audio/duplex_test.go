package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestWithDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   StreamConfig
		want StreamConfig
	}{
		{
			name: "zero config gets both defaults",
			in:   StreamConfig{},
			want: StreamConfig{SampleRate: 48000, PeriodFrames: 480},
		},
		{
			name: "explicit sample rate kept",
			in:   StreamConfig{SampleRate: 44100},
			want: StreamConfig{SampleRate: 44100, PeriodFrames: 480},
		},
		{
			name: "explicit period kept",
			in:   StreamConfig{PeriodFrames: 256},
			want: StreamConfig{SampleRate: 48000, PeriodFrames: 256},
		},
		{
			name: "device IDs pass through untouched",
			in:   StreamConfig{CaptureDevice: "aa01", PlaybackDevice: "bb02"},
			want: StreamConfig{SampleRate: 48000, PeriodFrames: 480, CaptureDevice: "aa01", PlaybackDevice: "bb02"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withDefaults(tt.in); got != tt.want {
				t.Errorf("withDefaults(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDeviceIDRoundTrip(t *testing.T) {
	const size = 256

	wasapiLike := make([]byte, size)
	copy(wasapiLike, []byte{'{', 0, '0', 0, '.', 0, '}', 0, 0xAB, 0xCD})

	full := make([]byte, size)
	for i := range full {
		full[i] = byte(i + 1)
	}

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "wasapi-like zero-padded id", raw: wasapiLike},
		{name: "id using the full array", raw: full},
		{name: "all-zero id", raw: make([]byte, size)},
		{name: "interior zeros preserved", raw: append([]byte{1, 0, 0, 2}, make([]byte, size-4)...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := encodeDeviceID(tt.raw)
			back, err := decodeDeviceID(s, size)
			if err != nil {
				t.Fatalf("decodeDeviceID(%q) error: %v", s, err)
			}
			if !bytes.Equal(back, tt.raw) {
				t.Errorf("round trip mangled ID: got %x, want %x", back, tt.raw)
			}
			if again := encodeDeviceID(back); again != s {
				t.Errorf("re-encode = %q, want %q", again, s)
			}
		})
	}
}

func TestEncodeDeviceIDNeverEmptyForRealIDs(t *testing.T) {

	if got := encodeDeviceID(make([]byte, 256)); got != "00" {
		t.Errorf("encodeDeviceID(all zeros) = %q, want %q", got, "00")
	}
}

func TestDecodeDeviceIDErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		size int
	}{
		{name: "odd length hex", in: "abc", size: 16},
		{name: "not hex", in: "zz", size: 16},
		{name: "longer than the identifier", in: "0102030405", size: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeDeviceID(tt.in, tt.size); err == nil {
				t.Errorf("decodeDeviceID(%q, %d) = nil error, want error", tt.in, tt.size)
			}
		})
	}
}

func TestInterleaveStereo(t *testing.T) {
	tests := []struct {
		name string
		l, r []float32
		want []float32
	}{
		{name: "empty is a no-op", l: nil, r: nil, want: nil},
		{name: "single frame", l: []float32{1}, r: []float32{-1}, want: []float32{1, -1}},
		{
			name: "frames stay paired",
			l:    []float32{1, 2, 3, 4},
			r:    []float32{5, 6, 7, 8},
			want: []float32{1, 5, 2, 6, 3, 7, 4, 8},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := make([]float32, 2*len(tt.l))
			interleaveStereo(dst, tt.l, tt.r)
			for i := range tt.want {
				if dst[i] != tt.want[i] {
					t.Fatalf("dst = %v, want %v", dst, tt.want)
				}
			}
		})
	}
}

func TestF32View(t *testing.T) {
	samples := []float32{0, 1, -1, 0.5, float32(math.Pi)}
	b := make([]byte, 4*len(samples))
	for i, v := range samples {
		binary.NativeEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}

	view := f32View(b, len(samples))
	for i, want := range samples {
		if view[i] != want {
			t.Errorf("view[%d] = %v, want %v", i, view[i], want)
		}
	}

	view[2] = 42.5
	if got := math.Float32frombits(binary.NativeEndian.Uint32(b[8:])); got != 42.5 {
		t.Errorf("write through view: bytes decode to %v, want 42.5", got)
	}

	if got := f32View(nil, 0); got != nil {
		t.Errorf("f32View(nil, 0) = %v, want nil", got)
	}
}

func TestDuplexScratchGrowOnly(t *testing.T) {
	var sc duplexScratch
	sc.grow(480)

	l480, r480 := sc.stereo(480)
	if len(l480) != 480 || len(r480) != 480 {
		t.Fatalf("stereo(480) lengths = %d, %d, want 480, 480", len(l480), len(r480))
	}

	l300, _ := sc.stereo(300)
	if len(l300) != 300 {
		t.Fatalf("stereo(300) length = %d, want 300", len(l300))
	}
	if &l300[0] != &l480[0] {
		t.Error("stereo(300) reallocated despite sufficient capacity")
	}
	if q := sc.silence(300); &q[0] != &sc.quiet[0] {
		t.Error("silence(300) reallocated despite sufficient capacity")
	}

	lBig, rBig := sc.stereo(512)
	if len(lBig) != 512 || len(rBig) != 512 {
		t.Fatalf("stereo(512) lengths = %d, %d, want 512, 512", len(lBig), len(rBig))
	}
	if &lBig[0] == &l480[0] {
		t.Error("stereo(512) did not grow past the old capacity")
	}

	lBig2, _ := sc.stereo(512)
	if &lBig2[0] != &lBig[0] {
		t.Error("second stereo(512) reallocated; grow must be grow-only")
	}

	for i, v := range sc.silence(512) {
		if v != 0 {
			t.Fatalf("silence[%d] = %v, want 0", i, v)
		}
	}
}
