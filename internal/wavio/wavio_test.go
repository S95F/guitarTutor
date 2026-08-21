package wavio

import (
	"bytes"
	"encoding/binary"
	"math"
	"runtime"
	"testing"
)

var le = binary.LittleEndian

func chunk(id string, body []byte) []byte {
	out := make([]byte, 8, 8+len(body)+1)
	copy(out, id)
	le.PutUint32(out[4:], uint32(len(body)))
	out = append(out, body...)
	if len(body)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

func wavFile(chunks ...[]byte) []byte {
	var body []byte
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := make([]byte, 12, 12+len(body))
	copy(out, "RIFF")
	le.PutUint32(out[4:], uint32(4+len(body)))
	copy(out[8:], "WAVE")
	return append(out, body...)
}

func fmtChunk(format, channels, sampleRate, bits int) []byte {
	b := make([]byte, 16)
	le.PutUint16(b[0:], uint16(format))
	le.PutUint16(b[2:], uint16(channels))
	le.PutUint32(b[4:], uint32(sampleRate))
	le.PutUint32(b[8:], uint32(sampleRate*channels*bits/8))
	le.PutUint16(b[12:], uint16(channels*bits/8))
	le.PutUint16(b[14:], uint16(bits))
	return b
}

func pcm16Data(samples ...int16) []byte {
	b := make([]byte, 2*len(samples))
	for i, s := range samples {
		le.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func floatData(samples ...float32) []byte {
	b := make([]byte, 4*len(samples))
	for i, s := range samples {
		le.PutUint32(b[4*i:], math.Float32bits(s))
	}
	return b
}

func ramp(n int, phase float64) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(math.Sin(phase + float64(i)*0.37))
	}
	return s
}

const step = 1.0 / 32768

func TestWriteHeader(t *testing.T) {

	var buf bytes.Buffer
	if err := Write(&buf, 48000, []float32{0, 1}, []float32{0.5, -1}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := wavFile(
		chunk("fmt ", fmtChunk(1, 2, 48000, 16)),
		chunk("data", pcm16Data(0, 16384, 32767, -32768)),
	)
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("Write output = % x, want % x", buf.Bytes(), want)
	}
	if buf.Len() != 44+8 {
		t.Errorf("file length = %d, want 52", buf.Len())
	}
}

func TestStereoRoundTrip(t *testing.T) {

	for _, n := range []int{0, 1, 3, 16, 481} {
		left := ramp(n, 0)
		right := ramp(n, 1.3)
		var buf bytes.Buffer
		if err := Write(&buf, 48000, left, right); err != nil {
			t.Fatalf("n=%d: Write: %v", n, err)
		}
		if buf.Len() != 44+4*n {
			t.Errorf("n=%d: file length = %d, want %d", n, buf.Len(), 44+4*n)
		}
		rate, l, r, err := Read(&buf)
		if err != nil {
			t.Fatalf("n=%d: Read: %v", n, err)
		}
		if rate != 48000 {
			t.Errorf("n=%d: sample rate = %d, want 48000", n, rate)
		}
		if len(l) != n || len(r) != n {
			t.Fatalf("n=%d: got %d/%d samples, want %d", n, len(l), len(r), n)
		}
		for i := 0; i < n; i++ {
			if math.Abs(float64(l[i]-left[i])) > step {
				t.Errorf("n=%d: left[%d] = %v, want %v within 1/32768", n, i, l[i], left[i])
			}
			if math.Abs(float64(r[i]-right[i])) > step {
				t.Errorf("n=%d: right[%d] = %v, want %v within 1/32768", n, i, r[i], right[i])
			}
		}
	}
}

func TestMonoRoundTrip(t *testing.T) {
	for _, n := range []int{1, 7, 480} {
		samples := ramp(n, 0.5)
		var buf bytes.Buffer
		if err := WriteMono(&buf, 44100, samples); err != nil {
			t.Fatalf("n=%d: WriteMono: %v", n, err)
		}
		if buf.Len() != 44+2*n {
			t.Errorf("n=%d: file length = %d, want %d", n, buf.Len(), 44+2*n)
		}
		rate, l, r, err := Read(&buf)
		if err != nil {
			t.Fatalf("n=%d: Read: %v", n, err)
		}
		if rate != 44100 {
			t.Errorf("n=%d: sample rate = %d, want 44100", n, rate)
		}
		if len(l) != n {
			t.Fatalf("n=%d: got %d samples, want %d", n, len(l), n)
		}
		for i := range samples {
			if math.Abs(float64(l[i]-samples[i])) > step {
				t.Errorf("n=%d: left[%d] = %v, want %v within 1/32768", n, i, l[i], samples[i])
			}
			if l[i] != r[i] {
				t.Errorf("n=%d: mono not duplicated: left[%d]=%v right[%d]=%v", n, i, l[i], i, r[i])
			}
		}
	}
}

func TestClamping(t *testing.T) {

	var buf bytes.Buffer
	if err := WriteMono(&buf, 48000, []float32{2, -2, 1, -1}); err != nil {
		t.Fatalf("WriteMono: %v", err)
	}
	_, l, _, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []float32{32767.0 / 32768, -1, 32767.0 / 32768, -1}
	for i, w := range want {
		if l[i] != w {
			t.Errorf("sample %d = %v, want %v", i, l[i], w)
		}
	}
}

func TestQuantizeNonFinite(t *testing.T) {
	tests := []struct {
		name string
		in   float32
		want int16
	}{
		{"NaN is silence", float32(math.NaN()), 0},
		{"+Inf saturates positive", float32(math.Inf(1)), math.MaxInt16},
		{"-Inf saturates negative", float32(math.Inf(-1)), math.MinInt16},
		{"huge positive saturates positive", 1e30, math.MaxInt16},
		{"huge negative saturates negative", -1e30, math.MinInt16},
		{"full scale positive", 1, math.MaxInt16},
		{"full scale negative", -1, math.MinInt16},
		{"zero", 0, 0},
		{"negative zero", float32(math.Copysign(0, -1)), 0},
		{"half scale", 0.5, 16384},
		{"rounds half away from zero", 1.5 / 32768, 2},
		{"rounds half away from zero, negative", -1.5 / 32768, -2},
	}
	for _, tt := range tests {
		if got := quantize(tt.in); got != tt.want {
			t.Errorf("%s: quantize(%v) = %d, want %d", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestWriteNonFiniteSamples(t *testing.T) {
	in := []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), 0.5, -0.5}
	var buf bytes.Buffer
	if err := WriteMono(&buf, 48000, in); err != nil {
		t.Fatalf("WriteMono: %v", err)
	}
	_, l, _, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []float32{0, 32767.0 / 32768, -1, 0.5, -0.5}
	if len(l) != len(want) {
		t.Fatalf("got %d samples, want %d", len(l), len(want))
	}
	for i, w := range want {
		if l[i] != w {
			t.Errorf("sample %d = %v, want %v", i, l[i], w)
		}
	}
}

func TestWriteArgErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, 48000, make([]float32, 3), make([]float32, 2)); err == nil {
		t.Error("Write accepted mismatched channel lengths")
	}
	if err := Write(&buf, 0, nil, nil); err == nil {
		t.Error("Write accepted zero sample rate")
	}
	if err := WriteMono(&buf, -1, nil); err == nil {
		t.Error("WriteMono accepted negative sample rate")
	}
}

func TestReadFloat32(t *testing.T) {

	want := []float32{0.25, -0.75, 1, -1, 0.001, 0.5}
	file := wavFile(
		chunk("fmt ", fmtChunk(3, 2, 96000, 32)),
		chunk("data", floatData(want[0], want[3], want[1], want[4], want[2], want[5])),
	)
	rate, l, r, err := Read(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rate != 96000 {
		t.Errorf("sample rate = %d, want 96000", rate)
	}
	if len(l) != 3 || len(r) != 3 {
		t.Fatalf("got %d/%d samples, want 3", len(l), len(r))
	}
	for i := 0; i < 3; i++ {
		if l[i] != want[i] {
			t.Errorf("left[%d] = %v, want %v", i, l[i], want[i])
		}
		if r[i] != want[3+i] {
			t.Errorf("right[%d] = %v, want %v", i, r[i], want[3+i])
		}
	}
}

func TestReadFloat32Mono(t *testing.T) {
	file := wavFile(
		chunk("fmt ", fmtChunk(3, 1, 48000, 32)),
		chunk("data", floatData(0.125, -0.5)),
	)
	_, l, r, err := Read(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(l) != 2 || l[0] != 0.125 || l[1] != -0.5 {
		t.Errorf("left = %v, want [0.125 -0.5]", l)
	}
	if len(r) != 2 || r[0] != l[0] || r[1] != l[1] {
		t.Errorf("mono not duplicated: right = %v, want %v", r, l)
	}
}

func TestReadSkipsExtraChunks(t *testing.T) {

	file := wavFile(
		chunk("fmt ", fmtChunk(1, 1, 22050, 16)),
		chunk("LIST", append([]byte("INFO"), chunk("IART", []byte("musicTutor"))...)),
		chunk("junk", []byte{1, 2, 3}),
		chunk("data", pcm16Data(-16384, 8192)),
	)
	rate, l, _, err := Read(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rate != 22050 {
		t.Errorf("sample rate = %d, want 22050", rate)
	}
	if len(l) != 2 || l[0] != -0.5 || l[1] != 0.25 {
		t.Errorf("left = %v, want [-0.5 0.25]", l)
	}
}

func allocBytes(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func forgeChunkSize(t *testing.T, file []byte, id string, size uint32) {
	t.Helper()
	off := bytes.Index(file, []byte(id))
	if off < 0 {
		t.Fatalf("no %q chunk in fixture", id)
	}
	le.PutUint32(file[off+4:], size)
}

func TestReadHugeDeclaredDataChunk(t *testing.T) {
	file := wavFile(
		chunk("fmt ", fmtChunk(1, 2, 48000, 16)),
		chunk("data", pcm16Data(1, 2, 3, 4)),
	)

	forgeChunkSize(t, file, "data", 0xFFFFF000)
	var err error
	alloc := allocBytes(func() {
		_, _, _, err = Read(bytes.NewReader(file))
	})
	if err == nil {
		t.Fatal("Read accepted a data chunk far larger than the file")
	}
	if alloc > 64<<20 {
		t.Errorf("Read allocated %d bytes for a forged ~4 GiB data chunk, want a bounded amount", alloc)
	}
}

func TestReadHugeDeclaredFmtChunk(t *testing.T) {
	file := wavFile(
		chunk("fmt ", fmtChunk(1, 1, 48000, 16)),
		chunk("data", pcm16Data(0)),
	)
	forgeChunkSize(t, file, "fmt ", 0xFFFFFF00)
	var err error
	alloc := allocBytes(func() {
		_, _, _, err = Read(bytes.NewReader(file))
	})
	if err == nil {
		t.Fatal("Read accepted a fmt chunk far larger than the file")
	}
	if alloc > 1<<20 {
		t.Errorf("Read allocated %d bytes for a forged ~4 GiB fmt chunk, want a bounded amount", alloc)
	}
}

func TestReadDataAcrossScratchBuffers(t *testing.T) {
	const n = 40000
	if 2*n <= dataBufLen || (2*n)%dataBufLen == 0 {
		t.Fatal("fixture must span multiple scratch buffers with a partial tail")
	}
	samples := ramp(n, 0.2)
	var buf bytes.Buffer
	if err := WriteMono(&buf, 48000, samples); err != nil {
		t.Fatalf("WriteMono: %v", err)
	}
	rate, l, r, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rate != 48000 {
		t.Errorf("sample rate = %d, want 48000", rate)
	}
	if len(l) != n || len(r) != n {
		t.Fatalf("got %d/%d samples, want %d", len(l), len(r), n)
	}
	for i := range samples {
		if math.Abs(float64(l[i]-samples[i])) > step {
			t.Fatalf("left[%d] = %v, want %v within 1/32768", i, l[i], samples[i])
		}
		if l[i] != r[i] {
			t.Fatalf("mono not duplicated at sample %d: left %v != right %v", i, l[i], r[i])
		}
	}
}

func TestReadErrors(t *testing.T) {
	valid16 := fmtChunk(1, 1, 48000, 16)
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"truncated riff header", []byte("RIFF")},
		{"not riff", append([]byte("JUNK\x00\x00\x00\x00WAVE"), wavFile()[12:]...)},
		{"not wave", func() []byte {
			f := wavFile(chunk("fmt ", valid16), chunk("data", nil))
			copy(f[8:12], "AVI ")
			return f
		}()},
		{"no data chunk", wavFile(chunk("fmt ", valid16))},
		{"data before fmt", wavFile(chunk("data", pcm16Data(0)), chunk("fmt ", valid16))},
		{"fmt too short", wavFile(chunk("fmt ", valid16[:8]), chunk("data", nil))},
		{"unsupported format code", wavFile(chunk("fmt ", fmtChunk(2, 1, 48000, 16)), chunk("data", nil))},
		{"unsupported pcm depth", wavFile(chunk("fmt ", fmtChunk(1, 1, 48000, 8)), chunk("data", nil))},
		{"unsupported float depth", wavFile(chunk("fmt ", fmtChunk(3, 1, 48000, 64)), chunk("data", nil))},
		{"three channels", wavFile(chunk("fmt ", fmtChunk(1, 3, 48000, 16)), chunk("data", nil))},
		{"zero channels", wavFile(chunk("fmt ", fmtChunk(1, 0, 48000, 16)), chunk("data", nil))},
		{"zero sample rate", wavFile(chunk("fmt ", fmtChunk(1, 1, 0, 16)), chunk("data", nil))},
		{"truncated data chunk", func() []byte {
			f := wavFile(chunk("fmt ", valid16), chunk("data", pcm16Data(1, 2, 3)))
			return f[:len(f)-2]
		}()},
		{"data not frame aligned", func() []byte {

			f := wavFile(chunk("fmt ", fmtChunk(1, 2, 48000, 16)), chunk("data", pcm16Data(1, 2, 3)))
			return f
		}()},
		{"truncated fmt chunk", func() []byte {
			f := wavFile(chunk("fmt ", valid16))
			return f[:20]
		}()},
	}
	for _, tt := range tests {
		if _, _, _, err := Read(bytes.NewReader(tt.data)); err == nil {
			t.Errorf("%s: Read accepted malformed input", tt.name)
		}
	}
}
