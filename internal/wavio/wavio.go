// Package wavio reads and writes RIFF/WAVE files for the offline render
// path and DSP test fixtures.
//
// Writing always produces 16-bit PCM with the canonical 44-byte header.
// Float32 samples are clamped to [-1, 1], scaled by 32768, and rounded to
// the nearest int16 — no dither. Dither trades round-trip exactness for
// nicer-sounding quantization noise; for offline renders and test
// fixtures, a bit-exact round trip is worth more than noise shaping.
// Samples that are not audio at all are still written as something safe:
// NaN becomes silence and ±Inf the matching rail (see quantize).
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
// clamped and exactly 1.0 becomes 32767). NaN becomes 0 and ±Inf the
// matching rail, so the mapping is total: every float64 the scaling can
// produce has a defined int16 answer.
//
// The saturation must happen in float space. It used to run on
// v := int(math.Round(x)), but a float-to-int conversion is undefined in
// Go when the value does not fit the destination, and on amd64 it yields
// MinInt64 — for NaN and for +Inf alike. So the `v > MaxInt16` test never
// fired and the `v < MinInt16` one did: a positive over-range sample
// saturated to full-scale NEGATIVE, and NaN became -32768 instead of
// silence. A float32 WAV carrying NaN or +Inf (a routine DAW export, and
// nothing upstream rejects non-finite samples) therefore rendered as
// full-scale noise. This function is the last thing between a decoded
// sample and a speaker, so it is written to be total rather than to
// assume its input is in range: silence is the only honest answer for
// "not a number", and an infinity is a rail.
func quantize(s float32) int16 {
	v := float64(s) * 32768
	switch {
	case math.IsNaN(v):
		return 0
	case v >= math.MaxInt16:
		return math.MaxInt16
	case v <= math.MinInt16:
		return math.MinInt16
	}
	// Now -32768 < v < 32767, so rounding half away from zero (unchanged
	// from the original, and what the bit-exact round-trip tests pin)
	// cannot leave the int16 range.
	return int16(math.Round(v))
}

// Read parses a WAV file. It accepts 16-bit PCM and 32-bit IEEE-float
// data, mono or stereo; mono files are returned duplicated into both
// channels. Unrelated RIFF chunks (LIST, INFO, cue, ...) before the data
// chunk are skipped. Samples are returned scaled to [-1, 1] (16-bit PCM
// is divided by 32768; float data is returned as stored).
//
// Declared chunk sizes are treated as untrusted: chunk bodies are read
// incrementally through a small scratch buffer and the output slices are
// preallocated to at most maxPreallocFrames, growing with the bytes
// actually read. A malformed header declaring a chunk larger than the
// remaining input therefore fails with a read error at the real end of
// input instead of forcing an allocation of the declared size, and Read
// keeps working on plain non-seekable io.Readers.
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
			// Only the first 16 bytes matter here, and the declared size
			// is untrusted (a 44-byte file can claim a 4 GiB fmt chunk),
			// so read those 16 and skip any extension bytes instead of
			// buffering size bytes.
			var body [16]byte
			if _, err := io.ReadFull(r, body[:]); err != nil {
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
			if _, err := io.CopyN(io.Discard, r, size-16); err != nil {
				return 0, nil, nil, fmt.Errorf("wavio: skipping fmt chunk extension: %w", err)
			}
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

// Allocation bounds for the attacker-controlled data chunk size. A RIFF
// header is just bytes — a 44-byte file can declare a 4 GiB data chunk —
// so the declared size must never drive an allocation directly.
const (
	// dataBufLen is the scratch buffer for incremental data-chunk reads:
	// a multiple of every possible frame length (2, 4, and 8 bytes).
	dataBufLen = 64 << 10
	// maxPreallocFrames caps the output preallocation hint taken from the
	// declared size: 4 M frames is 32 MB across both float32 channels.
	// Longer (real) data grows by append as bytes actually arrive.
	maxPreallocFrames = 4 << 20
)

// readData decodes a data chunk of size bytes into per-channel samples.
// The declared size is untrusted: bytes stream through a bounded scratch
// buffer and the outputs grow by append, so a forged size larger than
// the actual input fails with a read error instead of forcing a
// size-driven allocation.
func readData(r io.Reader, size int64, format, channels, bits int) (left, right []float32, err error) {
	sampleLen := int64(bits / 8)
	frameLen := sampleLen * int64(channels)
	if size%frameLen != 0 {
		return nil, nil, fmt.Errorf("wavio: data chunk size %d is not a multiple of the %d-byte frame", size, frameLen)
	}
	hint := size / frameLen
	if hint > maxPreallocFrames {
		hint = maxPreallocFrames
	}
	left = make([]float32, 0, hint)
	right = make([]float32, 0, hint)
	bufLen := int64(dataBufLen)
	if bufLen > size {
		bufLen = size
	}
	buf := make([]byte, bufLen)
	for remaining := size; remaining > 0; {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return nil, nil, fmt.Errorf("wavio: reading data chunk: %w", err)
		}
		for off := int64(0); off < n; off += frameLen {
			l := decodeSample(buf[off:], format)
			rr := l // mono duplicates into both channels
			if channels == 2 {
				rr = decodeSample(buf[off+sampleLen:], format)
			}
			left = append(left, l)
			right = append(right, rr)
		}
		remaining -= n
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
