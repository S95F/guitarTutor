//go:build !cgo

package audio

// Available reports the duplex backend compiled into this binary. Without
// cgo there is none: the app is playback-only (oto), and live-input
// features are disabled with a clear message.
func Available() Backend { return nil }
