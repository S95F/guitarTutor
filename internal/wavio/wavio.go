package wavio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const (
	formatPCM   = 1
	formatFloat = 3
)

const headerLen = 44

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

func WriteMono(w io.Writer, sampleRate int, samples []float32) error {
	return writePCM16(w, sampleRate, 1, samples)
}

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
	le.PutUint32(buf[16:], 16)
	le.PutUint16(buf[20:], formatPCM)
	le.PutUint16(buf[22:], uint16(channels))
	le.PutUint32(buf[24:], uint32(sampleRate))
	le.PutUint32(buf[28:], uint32(sampleRate*channels*2))
	le.PutUint16(buf[32:], uint16(channels*2))
	le.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	le.PutUint32(buf[40:], uint32(dataLen))
	for i, s := range interleaved {
		le.PutUint16(buf[headerLen+2*i:], uint16(quantize(s)))
	}
	_, err := w.Write(buf)
	return err
}

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

	return int16(math.Round(v))
}

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

			if _, err := io.CopyN(io.Discard, r, size); err != nil {
				return 0, nil, nil, fmt.Errorf("wavio: skipping %q chunk: %w", id, err)
			}
			if err := skipPad(r, size); err != nil {
				return 0, nil, nil, err
			}
		}
	}
}

func skipPad(r io.Reader, size int64) error {
	if size%2 == 0 {
		return nil
	}
	if _, err := io.CopyN(io.Discard, r, 1); err != nil {
		return fmt.Errorf("wavio: skipping chunk pad byte: %w", err)
	}
	return nil
}

const (
	dataBufLen = 64 << 10

	maxPreallocFrames = 4 << 20
)

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
			rr := l
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

func decodeSample(raw []byte, format int) float32 {
	le := binary.LittleEndian
	if format == formatPCM {
		return float32(int16(le.Uint16(raw))) / 32768
	}
	return math.Float32frombits(le.Uint32(raw))
}
