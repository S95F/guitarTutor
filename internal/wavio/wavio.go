// Package wavio reads and writes RIFF/WAVE files for the offline render
// path and DSP test fixtures.
//
// Writing always produces 16-bit PCM with the canonical 44-byte header.
// Float32 samples are clamped to [-1, 1], scaled by 32768, and rounded to
// the nearest int16 — no dither. Dither trades round-trip exactness for
// nicer-sounding quantization noise; for offline renders and test
// fixtures, a bit-exact round trip is worth more than noise shaping.
//
// Reading accepts 16-bit PCM and 32-bit IEEE-float data, mono or stereo,
// and skips over unrelated RIFF chunks (LIST, INFO, cue, ...).
package wavio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// WAVE format codes from the fmt chunk.
const (
	formatPCM   = 1 // integer PCM
	formatFloat = 3 // IEEE float
)

// headerLen is the size of the canonical WAV header: RIFF descriptor,
// 16-byte fmt chunk, and the data chunk header.
const headerLen = 44

// Write writes left and right as a 16-bit PCM stereo WAV file. The two
// slices must be the same length. Samples outside [-1, 1] are clamped.
func Write(w io.Writer, sampleRate int, left, right []float32) error {
	if len(left) != len(right) {
		return fmt.Errorf("wavio: left has %d samples, right has %d", len(left), len(right))
	}
	interleaved := make([]float32, 0, 2*len(left))
	for i := range left {
		interleaved = append(interleaved, left[i], right[i])
	}
	return writePCM16(w, sampleRate, 2, interleaved)
}

// WriteMono writes samples as a 16-bit PCM mono WAV file. Samples outside
// [-1, 1] are clamped.
func WriteMono(w io.Writer, sampleRate int, samples []float32) error {
	return writePCM16(w, sampleRate, 1, samples)
}

// writePCM16 writes the canonical 44-byte header followed by the
// interleaved samples quantized to int16.
func writePCM16(w io.Writer, sampleRate, channels int, interleaved []float32) error {
	if sampleRate <= 0 {
		return fmt.Errorf("wavio: sample rate must be positive, got %d", sampleRate)
	}
	dataLen := 2 * len(interleaved)
	if uint64(dataLen) > math.MaxUint32-(headerLen-8) {
		return fmt.Errorf("wavio: %d samples exceed the 4 GiB WAV size limit", len(interleaved))
	}
	le := binary.LittleEndian
	buf := make([]byte, headerLen+dataLen)
	copy(buf[0:], "RIFF")
	le.PutUint32(buf[4:], uint32(headerLen-8+dataLen))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	le.PutUint32(buf[16:], 16) // fmt chunk size
	le.PutUint16(buf[20:], formatPCM)
	le.PutUint16(buf[22:], uint16(channels))
	le.PutUint32(buf[24:], uint32(sampleRate))
	le.PutUint32(buf[28:], uint32(sampleRate*channels*2)) // byte rate
	le.PutUint16(buf[32:], uint16(channels*2))            // block align
	le.PutUint16(buf[34:], 16)                            // bits per sample
	copy(buf[36:], "data")
	le.PutUint32(buf[40:], uint32(dataLen))
	for i, s := range interleaved {
		le.PutUint16(buf[headerLen+2*i:], uint16(quantize(s)))
	}
	_, err := w.Write(buf)
	return err
}

// quantize converts a float32 sample to int16: scale by 32768, round to
// nearest, and saturate at the int16 limits (so out-of-range input is
// clamped and exactly 1.0 becomes 32767).
func quantize(s float32) int16 {
	v := int(math.Round(float64(s) * 32768))
	if v > math.MaxInt16 {
		v = math.MaxInt16
	}
	if v < math.MinInt16 {
		v = math.MinInt16
	}
	return int16(v)
}

// Read parses a WAV file. It accepts 16-bit PCM and 32-bit IEEE-float
// data, mono or stereo; mono files are returned duplicated into both
// channels. Unrelated RIFF chunks (LIST, INFO, cue, ...) before the data
// chunk are skipped. Samples are returned scaled to [-1, 1] (16-bit PCM
// is divided by 32768; float data is returned as stored).
func Read(r io.Reader) (sampleRate int, left, right []float32, err error) {
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return 0, nil, nil, fmt.Errorf("wavio: reading RIFF header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" {
		return 0, nil, nil, fmt.Errorf("wavio: not a RIFF file (got %q)", riff[0:4])
	}
	if string(riff[8:12]) != "WAVE" {
		return 0, nil, nil, fmt.Errorf("wavio: not a WAVE file (got %q)", riff[8:12])
	}
	le := binary.LittleEndian
	var (
		haveFmt  bool
		format   int
		channels int
		bits     int
	)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return 0, nil, nil, fmt.Errorf("wavio: no data chunk")
			}
			return 0, nil, nil, fmt.Errorf("wavio: reading chunk header: %w", err)
		}
		id := string(hdr[0:4])
		size := int64(le.Uint32(hdr[4:8]))
		switch id {
		case "fmt ":
			if size < 16 {
				return 0, nil, nil, fmt.Errorf("wavio: fmt chunk is %d bytes, want at least 16", size)
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(r, body); err != nil {
				return 0, nil, nil, fmt.Errorf("wavio: reading fmt chunk: %w", err)
			}
			format = int(le.Uint16(body[0:2]))
			channels = int(le.Uint16(body[2:4]))
			sampleRate = int(le.Uint32(body[4:8]))
			bits = int(le.Uint16(body[14:16]))
			switch {
			case format == formatPCM && bits == 16:
			case format == formatFloat && bits == 32:
			default:
				return 0, nil, nil, fmt.Errorf("wavio: unsupported format %d at %d bits (want 16-bit PCM or 32-bit float)", format, bits)
			}
			if channels != 1 && channels != 2 {
				return 0, nil, nil, fmt.Errorf("wavio: %d channels, want mono or stereo", channels)
			}
			if sampleRate <= 0 {
				return 0, nil, nil, fmt.Errorf("wavio: sample rate must be positive, got %d", sampleRate)
			}
			haveFmt = true
			if err := skipPad(r, size); err != nil {
				return 0, nil, nil, err
			}
		case "data":
			if !haveFmt {
				return 0, nil, nil, fmt.Errorf("wavio: data chunk before fmt chunk")
			}
			left, right, err = readData(r, size, format, channels, bits)
			if err != nil {
				return 0, nil, nil, err
			}
			return sampleRate, left, right, nil
		default:
			// An unrelated chunk (LIST, INFO, cue, ...): skip it.
			if _, err := io.CopyN(io.Discard, r, size); err != nil {
				return 0, nil, nil, fmt.Errorf("wavio: skipping %q chunk: %w", id, err)
			}
			if err := skipPad(r, size); err != nil {
				return 0, nil, nil, err
			}
		}
	}
}

// skipPad consumes the pad byte after an odd-sized chunk: RIFF chunks are
// word-aligned, so a chunk with an odd size is followed by one pad byte.
func skipPad(r io.Reader, size int64) error {
	if size%2 == 0 {
		return nil
	}
	if _, err := io.CopyN(io.Discard, r, 1); err != nil {
		return fmt.Errorf("wavio: skipping chunk pad byte: %w", err)
	}
	return nil
}

// readData decodes a data chunk of size bytes into per-channel samples.
func readData(r io.Reader, size int64, format, channels, bits int) (left, right []float32, err error) {
	sampleLen := int64(bits / 8)
	frameLen := sampleLen * int64(channels)
	if size%frameLen != 0 {
		return nil, nil, fmt.Errorf("wavio: data chunk size %d is not a multiple of the %d-byte frame", size, frameLen)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, nil, fmt.Errorf("wavio: reading data chunk: %w", err)
	}
	frames := size / frameLen
	left = make([]float32, frames)
	right = make([]float32, frames)
	for f := int64(0); f < frames; f++ {
		off := f * frameLen
		l := decodeSample(raw[off:], format)
		rr := l // mono duplicates into both channels
		if channels == 2 {
			rr = decodeSample(raw[off+sampleLen:], format)
		}
		left[f] = l
		right[f] = rr
	}
	return left, right, nil
}

// decodeSample decodes the sample at the start of raw. The format has
// already been validated, so PCM implies 16 bits and float implies 32.
func decodeSample(raw []byte, format int) float32 {
	le := binary.LittleEndian
	if format == formatPCM {
		return float32(int16(le.Uint16(raw))) / 32768
	}
	return math.Float32frombits(le.Uint32(raw))
}
