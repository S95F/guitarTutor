package audiofile

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"

	"github.com/S95F/musicTutor/internal/wavio"
)

func sine(n int, freq float64, rate int, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

func writeWAVStereo(t *testing.T, rate int, left, right []float32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := wavio.Write(f, rate, left, right); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeWAVMono(t *testing.T, rate int, samples []float32) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mono.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := wavio.WriteMono(f, rate, samples); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWAVRoundTrip48k(t *testing.T) {
	const n = 4800
	l := sine(n, 440, SampleRate, 0.8)
	r := sine(n, 330, SampleRate, 0.5)
	path := writeWAVStereo(t, SampleRate, l, r)

	gl, gr, warns, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings for a 48 kHz stereo WAV: %v", warns)
	}
	if len(gl) != n || len(gr) != n {
		t.Fatalf("got %d/%d samples, want %d", len(gl), len(gr), n)
	}
	const tol = 1.0 / 32768
	for i := 0; i < n; i++ {
		if math.Abs(float64(gl[i]-l[i])) > tol || math.Abs(float64(gr[i]-r[i])) > tol {
			t.Fatalf("sample %d = (%v, %v), want (%v, %v) within %v", i, gl[i], gr[i], l[i], r[i], tol)
		}
	}
}

func TestWAVMonoDuplication(t *testing.T) {
	m := sine(4800, 440, SampleRate, 0.7)
	path := writeWAVMono(t, SampleRate, m)
	gl, gr, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(gl) != len(gr) || len(gl) != len(m) {
		t.Fatalf("got %d/%d samples, want %d", len(gl), len(gr), len(m))
	}
	for i := range gl {
		if gl[i] != gr[i] {
			t.Fatalf("sample %d: left %v != right %v (mono must duplicate)", i, gl[i], gr[i])
		}
	}
}

func estimateFreq(s []float32, rate int) float64 {
	var first, last float64
	crossings := 0
	for i := 1; i < len(s); i++ {
		if s[i-1] < 0 && s[i] >= 0 {

			x := float64(i-1) + float64(-s[i-1])/float64(s[i]-s[i-1])
			if crossings == 0 {
				first = x
			}
			last = x
			crossings++
		}
	}
	if crossings < 2 {
		return 0
	}
	return float64(crossings-1) * float64(rate) / (last - first)
}

func TestResampleKeepsFrequency(t *testing.T) {
	const srcRate = 44100
	src := sine(srcRate, 440, srcRate, 0.8)
	path := writeWAVMono(t, srcRate, src)

	gl, _, warns, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantLen := len(src) * SampleRate / srcRate
	if abs := len(gl) - wantLen; abs < -1 || abs > 1 {
		t.Errorf("resampled length = %d, want %d +-1", len(gl), wantLen)
	}
	if f := estimateFreq(gl, SampleRate); math.Abs(f-440) > 1 {
		t.Errorf("resampled frequency = %.3f Hz, want 440 +-1", f)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "resampled") {
			found = true
		}
	}
	if !found {
		t.Errorf("no resample warning in %v", warns)
	}
}

func writeFLAC(t *testing.T, rate int, channels [][]int32) string {
	t.Helper()
	n := len(channels[0])
	info := &meta.StreamInfo{
		BlockSizeMin:  16,
		BlockSizeMax:  65535,
		SampleRate:    uint32(rate),
		NChannels:     uint8(len(channels)),
		BitsPerSample: 16,
		NSamples:      uint64(n),
	}
	path := filepath.Join(t.TempDir(), "test.flac")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := flac.NewEncoder(f, info)
	if err != nil {
		t.Fatalf("flac.NewEncoder: %v", err)
	}
	ch := frame.ChannelsMono
	if len(channels) == 2 {
		ch = frame.ChannelsLR
	}
	const block = 1024
	for off := 0; off < n; off += block {
		end := off + block
		if end > n {
			end = n
		}
		fr := &frame.Frame{Header: frame.Header{
			HasFixedBlockSize: false,
			BlockSize:         uint16(end - off),
			SampleRate:        uint32(rate),
			Channels:          ch,
			BitsPerSample:     16,
		}}
		for _, c := range channels {
			fr.Subframes = append(fr.Subframes, &frame.Subframe{
				SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
				Samples:   c[off:end],
				NSamples:  end - off,
			})
		}
		if err := enc.WriteFrame(fr); err != nil {
			t.Fatalf("flac WriteFrame: %v", err)
		}
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("flac Close: %v", err)
	}
	return path
}

func int16Sine(n int, freq float64, rate int, amp float64) []int32 {
	out := make([]int32, n)
	for i := range out {
		out[i] = int32(math.Round(amp * 32767 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))))
	}
	return out
}

func TestFLACStereoDecode(t *testing.T) {
	const n = 3000
	l := int16Sine(n, 440, SampleRate, 0.8)
	r := int16Sine(n, 330, SampleRate, 0.5)
	path := writeFLAC(t, SampleRate, [][]int32{l, r})

	gl, gr, warns, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings for a 48 kHz stereo FLAC: %v", warns)
	}
	if len(gl) != n || len(gr) != n {
		t.Fatalf("got %d/%d samples, want %d", len(gl), len(gr), n)
	}
	for i := 0; i < n; i++ {
		wl := float32(l[i]) / 32768
		wr := float32(r[i]) / 32768
		if gl[i] != wl || gr[i] != wr {
			t.Fatalf("sample %d = (%v, %v), want exactly (%v, %v)", i, gl[i], gr[i], wl, wr)
		}
	}
}

func TestFLACMonoResample(t *testing.T) {
	const srcRate = 44100
	m := int16Sine(srcRate/2, 440, srcRate, 0.8)
	path := writeFLAC(t, srcRate, [][]int32{m})

	gl, gr, warns, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(gl) != len(gr) {
		t.Fatalf("channel lengths differ: %d vs %d", len(gl), len(gr))
	}
	for i := range gl {
		if gl[i] != gr[i] {
			t.Fatalf("sample %d: left %v != right %v (mono must duplicate)", i, gl[i], gr[i])
		}
	}
	if f := estimateFreq(gl, SampleRate); math.Abs(f-440) > 1 {
		t.Errorf("resampled frequency = %.3f Hz, want 440 +-1", f)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "resampled") {
			found = true
		}
	}
	if !found {
		t.Errorf("no resample warning in %v", warns)
	}
}

func TestFLACForgedNSamplesBoundedAlloc(t *testing.T) {
	const n = 256
	m := int16Sine(n, 440, SampleRate, 0.5)
	path := writeFLAC(t, SampleRate, [][]int32{m})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	data[8+13] |= 0x0F
	for i := 14; i <= 17; i++ {
		data[8+i] = 0xFF
	}
	forged := filepath.Join(t.TempDir(), "forged.flac")
	if err := os.WriteFile(forged, data, 0o644); err != nil {
		t.Fatal(err)
	}

	fh, err := os.Open(forged)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := flac.New(fh)
	if err != nil {
		fh.Close()
		t.Fatalf("opening forged FLAC: %v", err)
	}
	if got := stream.Info.NSamples; got != 1<<36-1 {
		fh.Close()
		t.Fatalf("forged NSamples = %d, want 2^36-1 (the STREAMINFO patch missed its target)", got)
	}
	fh.Close()

	gl, gr, _, err := Load(forged)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(gl) != n || len(gr) != n {
		t.Fatalf("got %d/%d samples, want %d", len(gl), len(gr), n)
	}
	for i := range gl {
		if want := float32(m[i]) / 32768; gl[i] != want {
			t.Fatalf("sample %d = %v, want %v", i, gl[i], want)
		}
	}
	if cap(gl) > maxPreallocSamples || cap(gr) > maxPreallocSamples {
		t.Errorf("channel capacities = %d/%d samples, want at most %d (forged NSamples must be clamped as an allocation hint)",
			cap(gl), cap(gr), maxPreallocSamples)
	}
}

func silentMP3(nframes int) []byte {

	const frameLen = 417
	var out []byte
	for i := 0; i < nframes; i++ {
		f := make([]byte, frameLen)
		f[0] = 0xFF
		f[1] = 0xFB
		f[2] = 0x90
		f[3] = 0x00
		out = append(out, f...)
	}
	return out
}

func TestMP3SilentDecode(t *testing.T) {
	const nframes = 8
	path := filepath.Join(t.TempDir(), "silence.mp3")
	if err := os.WriteFile(path, silentMP3(nframes), 0o644); err != nil {
		t.Fatal(err)
	}
	gl, gr, warns, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	srcSamples := nframes * 1152
	wantLen := srcSamples * SampleRate / 44100
	if abs := len(gl) - wantLen; abs < -2 || abs > 2 {
		t.Errorf("got %d samples, want about %d (8 frames resampled 44.1->48 kHz)", len(gl), wantLen)
	}
	for i := range gl {
		if gl[i] != 0 || gr[i] != 0 {
			t.Fatalf("sample %d = (%v, %v), want silence", i, gl[i], gr[i])
		}
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "best-effort") {
			found = true
		}
	}
	if !found {
		t.Errorf("no best-effort warning in %v", warns)
	}
}

func TestMP3GarbageError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.mp3")
	if err := os.WriteFile(path, []byte("this is not an mp3 file at all, not even close"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := Load(path)
	if err == nil {
		t.Fatal("Load of garbage .mp3 succeeded, want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "WAV") || !strings.Contains(msg, "FLAC") {
		t.Errorf("MP3 error %q does not suggest WAV/FLAC", msg)
	}
}

func TestMP3RealFile(t *testing.T) {
	path := filepath.Join("testdata", "real.mp3")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no real MP3 fixture at %s: %v", path, err)
	}
	gl, gr, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(gl) == 0 || len(gl) != len(gr) {
		t.Fatalf("got %d/%d samples, want equal nonzero lengths", len(gl), len(gr))
	}
}

func TestUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.ogg")
	if err := os.WriteFile(path, []byte("OggS"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Load(.ogg) = %v, want an unsupported-format error", err)
	}
}

func TestResampleLinearRatio(t *testing.T) {
	src := []float32{0, 2, 4, 6, 8, 10, 12, 14}
	got := resampleLinear(src, 24000, 48000)
	if len(got) != 16 {
		t.Fatalf("got %d samples, want 16", len(got))
	}
	for i := range got {
		want := float32(i)
		if i >= 15 {
			want = 14
		}
		if got[i] != want {
			t.Errorf("sample %d = %v, want %v", i, got[i], want)
		}
	}
}

func TestImplausibleSampleRateRejected(t *testing.T) {
	samples := sine(2000, 220, 8000, 0.5)
	for _, rate := range []int{1, 999, 768001, 4_000_000} {
		path := writeWAVMono(t, rate, samples)
		if _, _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "sample rate") {
			t.Errorf("rate %d: err = %v, want a sample-rate rejection", rate, err)
		}
	}

	path := writeWAVMono(t, 8000, samples)
	_, _, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("8 kHz should load, got %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "resampled") {
			found = true
		}
	}
	if !found {
		t.Errorf("8 kHz load produced warnings %v, want a resample note", warnings)
	}
}

func writeFloatWAV(t *testing.T, rate int, samples []float32) string {
	t.Helper()
	var buf bytes.Buffer
	dataLen := 4 * len(samples)
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataLen))
	buf.WriteString("WAVEfmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(3))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint32(rate))
	binary.Write(&buf, binary.LittleEndian, uint32(rate*4))
	binary.Write(&buf, binary.LittleEndian, uint16(4))
	binary.Write(&buf, binary.LittleEndian, uint16(32))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataLen))
	for _, s := range samples {
		binary.Write(&buf, binary.LittleEndian, math.Float32bits(s))
	}
	path := filepath.Join(t.TempDir(), "float.wav")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSilencesNonFiniteSamples(t *testing.T) {
	samples := []float32{
		0.5,
		float32(math.NaN()),
		-0.25,
		float32(math.Inf(1)),
		float32(math.Inf(-1)),
		0.125,
	}
	path := writeFloatWAV(t, SampleRate, samples)
	left, right, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(left) != len(samples) {
		t.Fatalf("decoded %d samples, want %d", len(left), len(samples))
	}
	for i := range left {
		for _, ch := range [][]float32{left, right} {
			if f := float64(ch[i]); math.IsNaN(f) || math.IsInf(f, 0) {
				t.Errorf("sample %d survived as %v, want it silenced", i, ch[i])
			}
		}
	}
	want := []float32{0.5, 0, -0.25, 0, 0, 0.125}
	for i, w := range want {
		if left[i] != w {
			t.Errorf("left[%d] = %v, want %v (good samples must be untouched)", i, left[i], w)
		}
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "finite") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming the silenced samples — a silent repair is the thing this project does not do", warnings)
	}
}

func TestResampleCannotManufactureNonFiniteSamples(t *testing.T) {

	samples := make([]float32, 64)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 3e38
		} else {
			samples[i] = -3e38
		}
	}
	path := writeFloatWAV(t, 44100, samples)
	left, right, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(left) == 0 {
		t.Fatal("decoded nothing")
	}
	for i := range left {
		for name, ch := range map[string][]float32{"left": left, "right": right} {
			if f := float64(ch[i]); math.IsNaN(f) || math.IsInf(f, 0) {
				t.Fatalf("%s[%d] = %v after resampling an all-finite file", name, i, ch[i])
			}
		}
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "clamped") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming the clamped samples", warnings)
	}
}
