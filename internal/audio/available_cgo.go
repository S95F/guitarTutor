//go:build cgo

package audio

// Available reports the duplex backend compiled into this binary: the
// miniaudio backend via gen2brain/malgo.
//
// TODO(phase-2): returns nil until the malgo backend lands.
func Available() Backend { return nil }
