package audio

import (
	"encoding/hex"
	"fmt"
)

func encodeDeviceID(raw []byte) string {
	n := len(raw)
	for n > 1 && raw[n-1] == 0 {
		n--
	}
	return hex.EncodeToString(raw[:n])
}

func decodeDeviceID(s string, size int) ([]byte, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("device ID %q: %w", s, err)
	}
	if len(raw) > size {
		return nil, fmt.Errorf("device ID %q: %d bytes exceeds the %d-byte identifier size", s, len(raw), size)
	}
	out := make([]byte, size)
	copy(out, raw)
	return out, nil
}
