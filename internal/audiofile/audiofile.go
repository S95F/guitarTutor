package audiofile

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	mp3 "github.com/hajimehoshi/go-mp3"
	"github.com/mewkiz/flac"

	"github.com/S95F/musicTutor/internal/wavio"
)

const SampleRate = 48000

const maxPreallocSamples = 4 << 20

const (
	minSampleRate = 1000
	maxSampleRate = 768000
)

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

	if rate < minSampleRate || rate > maxSampleRate {
		return nil, nil, nil, fmt.Errorf(
			"audiofile: %s: declared sample rate %d Hz is outside the plausible %d-%d Hz range (corrupt or forged header)",
			filepath.Base(path), rate, minSampleRate, maxSampleRate)
	}

	nfL, clL := scrubSamples(left)
	nfR, clR := scrubSamples(right)
	if n := nfL + nfR; n > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d sample(s) were not a finite number and were silenced (the file is damaged or was written by a faulty encoder)", n))
	}
	if n := clL + clR; n > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d sample(s) were far outside the audible range and were clamped to +/-%d (the file is damaged or was written by a faulty encoder)", n, maxSample))
	}
	if rate != SampleRate {
		left = resampleLinear(left, rate, SampleRate)
		right = resampleLinear(right, rate, SampleRate)
		warnings = append(warnings, fmt.Sprintf(
			"resampled from %d Hz to %d Hz by linear interpolation (slight high-frequency loss)", rate, SampleRate))
	}
	return left, right, warnings, nil
}

const maxSample = 16

func scrubSamples(s []float32) (nonFinite, clamped int) {
	for i, v := range s {
		f := float64(v)
		switch {
		case math.IsNaN(f) || math.IsInf(f, 0):
			s[i] = 0
			nonFinite++
		case f > maxSample:
			s[i] = maxSample
			clamped++
		case f < -maxSample:
			s[i] = -maxSample
			clamped++
		}
	}
	return nonFinite, clamped
}

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

func loadFLAC(path string) (rate int, left, right []float32, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, nil, nil, err
	}

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
		rs := ls
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
	frames := len(raw) / 4
	left = make([]float32, frames)
	right = make([]float32, frames)
	for i := 0; i < frames; i++ {
		left[i] = float32(int16(binary.LittleEndian.Uint16(raw[4*i:]))) / 32768
		right[i] = float32(int16(binary.LittleEndian.Uint16(raw[4*i+2:]))) / 32768
	}
	warnings = append(warnings, "MP3 import is best-effort (the decoder is unmaintained); prefer WAV or FLAC")
	return rate, left, right, warnings, nil
}

func mp3Err(path string, err error) error {
	return fmt.Errorf("audiofile: decoding MP3 %s (MP3 support is best-effort — the decoder is unmaintained; please convert the file to WAV or FLAC): %w", path, err)
}

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
