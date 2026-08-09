//go:build cgo

package audio

import "sync"

var (
	availableOnce    sync.Once
	availableBackend *malgoBackend
)

// Available reports the duplex backend compiled into this binary: the
// miniaudio backend via gen2brain/malgo. The context is initialized once,
// on first call, preferring WASAPI on Windows and each platform's natural
// backend order elsewhere; every later call returns the same instance,
// which lives for the rest of the process (see malgoBackend.close for why
// it is never torn down).
//
// Available returns nil when no real audio backend initializes — e.g. a
// headless CI runner — which downgrades the app to the same playback-only
// behavior as a non-cgo build. The null backend is deliberately not used
// as a fallback: a fake device would mask the failure instead of letting
// the app explain it.
func Available() Backend {
	availableOnce.Do(func() {
		b, err := newMalgoBackend()
		if err != nil {
			return
		}
		availableBackend = b
	})
	if availableBackend == nil {
		return nil // typed-nil guard: keep the interface value itself nil
	}
	return availableBackend
}
