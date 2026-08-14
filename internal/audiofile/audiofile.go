// Package audiofile decodes audio files into the project-standard
// 48 kHz stereo float32 format (ROADMAP Phase 3: backing-track import).
//
// WAV is read via internal/wavio, FLAC via github.com/mewkiz/flac, and
// MP3 — best-effort, the decoder is unmaintained (ROADMAP "Known risks")
// — via github.com/hajimehoshi/go-mp3. Mono sources are duplicated into
// both channels; sources at other sample rates are resampled to 48000 Hz
// by linear interpolation.
//
// Linear interpolation is a deliberate quality tradeoff: it attenuates
// high frequencies slightly and adds a small amount of aliasing compared
// to a windowed-sinc resampler, but it is simple, fast, and more than
// adequate for a practice backing track. A windowed-sinc upgrade is
// future work if the difference ever becomes audible in practice.
package audiofile

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	mp3 "github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac"

	"github.com/S95F/guitarTutor/internal/wavio"
)

// SampleRate is the output sample rate: the project-wide 48 kHz standard.
const SampleRate = 48000

// maxPreallocSamples caps the per-channel preallocation hint taken from
// the FLAC STREAMINFO total-sample count. That 36-bit field is
// attacker-controlled — a file of a few hundred bytes can declare 2^36
// samples and would force a ~275 GB make() if trusted — so it is honored
// only up to 4 M samples (16 MB per channel); longer streams grow by
// append as frames actually decode.
const maxPreallocSamples = 4 << 20

// Sample-rate plausibility bounds on a file's declared rate. The
// declaration is attacker-controlled bytes (a WAV fmt chunk, a FLAC
// STREAMINFO) and it DIVIDES in resampleLinear: a 167 KB WAV claiming
// 1 Hz would inflate into two ~8 GB output channels and abort the
// process out of memory (audit B3). Nothing real declares below 8 kHz
// (MPEG-2.5, telephony WAV) or above 768 kHz (the highest marketed PCM
// rate; FLAC's own spec ceiling is 655,350 Hz), so these bounds reject
// only garbage while capping the resample amplification at 48x.
// (go-mp3 is table-bound to 8-48 kHz and cannot report an absurd rate;
// the check covers it anyway as free insurance.)
const (
	minSampleRate = 1000
	maxSampleRate = 768000
)

// Load decodes the audio file at path into 48 kHz stereo float32.
// The format is chosen by file extension: .wav, .flac, or .mp3
// (case-insensitive). Mono files are duplicated into both channels, and
// other sample rates are resampled to 48000 Hz by linear interpolation
// (see the package comment for the quality tradeoff). Everything Load
// changed about the audio — resampling, channel handling, best-effort
// decoding — is reported as human-readable warnings.
func Load(path string) (left, right []float32, warnings []string, err error) {
	var rate int
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		rate, left, right, err = loadWAV(path)
	case ".flac":
		rate, left, right, warnings, err = loadFLAC(path)
	case ".mp3":
		rate, left, right, warnings, err = loadMP3(path)
	default:
		return nil, nil, nil, fmt.Errorf("audiofile: unsupported format %q (supported: .wav, .flac, .mp3)", filepath.Ext(path))
	}
	if err != nil {
		return nil, nil, nil, err
	}
	// One policy check covers every decode path above and any format
	// added later; the per-decoder checks only reject rate <= 0.
	if rate < minSampleRate || rate > maxSampleRate {
		return nil, nil, nil, fmt.Errorf(
			"audiofile: %s: declared sample rate %d Hz is outside the plausible %d-%d Hz range (corrupt or forged header)",
			filepath.Base(path), rate, minSampleRate, maxSampleRate)
	}
	if rate != SampleRate {
		left = resampleLinear(left, rate, SampleRate)
		right = resampleLinear(right, rate, SampleRate)
		warnings = append(warnings, fmt.Sprintf(
			"resampled from %d Hz to %d Hz by linear interpolation (slight high-frequency loss)", rate, SampleRate))
	}
	return left, right, warnings, nil
}

// loadWAV reads a WAV file via wavio, which already duplicates mono into
// both channels and scales samples to [-1, 1].
func loadWAV(path string) (rate int, left, right []float32, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, nil, err
	}
	defer f.Close()
	rate, left, right, err = wavio.Read(f)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("audiofile: %s: %w", path, err)
	}
	return rate, left, right, nil
}

// loadFLAC decodes a FLAC file. Samples are scaled by the frame's bit
// depth to [-1, 1]. Files with more than two channels keep the first two
// (front left/right in every FLAC channel assignment), with a warning.
func loadFLAC(path string) (rate int, left, right []float32, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, nil, nil, err
	}
	// Close the file directly: flac.New wraps it in a bufio.Reader, so
	// Stream.Close cannot reach (and never closes) the *os.File.
	defer f.Close()
	stream, err := flac.New(f)
	if err != nil {
		return 0, nil, nil, nil, fmt.Errorf("audiofile: opening FLAC %s: %w", path, err)
	}

	info := stream.Info
	rate = int(info.SampleRate)
	if rate <= 0 {
		return 0, nil, nil, nil, fmt.Errorf("audiofile: FLAC %s: invalid sample rate %d", path, rate)
	}
	nch := int(info.NChannels)
	if nch > 2 {
		warnings = append(warnings, fmt.Sprintf("FLAC has %d channels; using the first two (front left/right)", nch))
	}
	if n := info.NSamples; n > 0 {
		if n > maxPreallocSamples {
			n = maxPreallocSamples
		}
		left = make([]float32, 0, n)
		right = make([]float32, 0, n)
	}
	for {
		fr, err := stream.ParseNext()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, nil, nil, nil, fmt.Errorf("audiofile: decoding FLAC %s: %w", path, err)
		}
		bps := fr.BitsPerSample
		if bps == 0 {
			bps = info.BitsPerSample
		}
		scale := 1 / float32(int64(1)<<(bps-1))
		if len(fr.Subframes) == 0 {
			continue
		}
		ls := fr.Subframes[0].Samples
		rs := ls // mono duplicates into both channels
		if len(fr.Subframes) > 1 {
			rs = fr.Subframes[1].Samples
		}
		n := len(ls)
		if len(rs) < n {
			n = len(rs)
		}
		for i := 0; i < n; i++ {
			left = append(left, float32(ls[i])*scale)
			right = append(right, float32(rs[i])*scale)
		}
	}
	return rate, left, right, warnings, nil
}

// loadMP3 decodes an MP3 file. go-mp3 always yields 16-bit little-endian
// stereo at the file's reported sample rate. Decoding is best-effort: the
// decoder is unmaintained, so failures suggest converting to WAV or FLAC.
func loadMP3(path string) (rate int, left, right []float32, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, nil, nil, err
	}
	defer f.Close()
	d, err := mp3.NewDecoder(f)
	if err != nil {
		return 0, nil, nil, nil, mp3Err(path, err)
	}
	rate = d.SampleRate()
	if rate <= 0 {
		return 0, nil, nil, nil, mp3Err(path, fmt.Errorf("invalid sample rate %d", rate))
	}
	var raw []byte
	if n := d.Length(); n > 0 {
		raw = make([]byte, 0, n)
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := d.Read(buf)
		raw = append(raw, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, nil, nil, nil, mp3Err(path, err)
		}
	}
	frames := len(raw) / 4 // 2 channels x 2 bytes
	left = make([]float32, frames)
	right = make([]float32, frames)
	for i := 0; i < frames; i++ {
		left[i] = float32(int16(binary.LittleEndian.Uint16(raw[4*i:]))) / 32768
		right[i] = float32(int16(binary.LittleEndian.Uint16(raw[4*i+2:]))) / 32768
	}
	warnings = append(warnings, "MP3 import is best-effort (the decoder is unmaintained); prefer WAV or FLAC")
	return rate, left, right, warnings, nil
}

// mp3Err wraps an MP3 decoding failure with the best-effort caveat and
// the recommended way out.
func mp3Err(path string, err error) error {
	return fmt.Errorf("audiofile: decoding MP3 %s (MP3 support is best-effort — the decoder is unmaintained; please convert the file to WAV or FLAC): %w", path, err)
}

// resampleLinear resamples src from srcRate to dstRate by linear
// interpolation between neighboring input samples. Output sample i sits
// at input position i*srcRate/dstRate; positions past the last input
// sample hold it (at most one output sample at the tail).
func resampleLinear(src []float32, srcRate, dstRate int) []float32 {
	if srcRate == dstRate || len(src) == 0 {
		return src
	}
	n := int(int64(len(src)) * int64(dstRate) / int64(srcRate))
	if n < 1 {
		n = 1
	}
	out := make([]float32, n)
	ratio := float64(srcRate) / float64(dstRate)
	last := len(src) - 1
	for i := range out {
		p := float64(i) * ratio
		j := int(p)
		if j >= last {
			out[i] = src[last]
			continue
		}
		f := float32(p - float64(j))
		out[i] = src[j] + f*(src[j+1]-src[j])
	}
	return out
}
