package audio

import (
	"encoding/hex"
	"fmt"
)

// encodeDeviceID converts a raw backend device identifier (malgo's
// fixed-size ma_device_id byte array) into the stable string form exposed
// as DeviceInfo.ID and stored in user config: lowercase hex with the
// array's trailing zero padding trimmed. Trimming loses nothing —
// decodeDeviceID zero-pads back to the fixed size, so the round trip is
// byte-exact — and keeps IDs readable (a WASAPI ID is a UTF-16 string
// occupying a fraction of the 256-byte array). An identifier that is
// entirely zero encodes as "00" rather than "", so no real ID can collide
// with the empty string, which StreamConfig reserves for "system default".
func encodeDeviceID(raw []byte) string {
	n := len(raw)
	for n > 1 && raw[n-1] == 0 {
		n--
	}
	return hex.EncodeToString(raw[:n])
}

// decodeDeviceID parses the string form produced by encodeDeviceID back
// into a raw identifier of exactly size bytes, zero-padded at the tail. It
// rejects strings that are not valid hex or decode to more than size
// bytes — both indicate a corrupted or foreign config value.
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
