//go:build cgo

package audio

import "sync"

var (
	availableOnce    sync.Once
	availableBackend *malgoBackend
)

func Available() Backend {
	availableOnce.Do(func() {
		b, err := newMalgoBackend()
		if err != nil {
			return
		}
		availableBackend = b
	})
	if availableBackend == nil {
		return nil
	}
	return availableBackend
}
