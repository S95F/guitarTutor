//go:build !onnx

package pitchml

import "github.com/S95F/guitarTutor/internal/pitch"

// This file is the whole package in a default build. It exposes the same
// exported API as swiftf0_onnx.go and imports nothing beyond the pitch
// contracts, so `go build ./...` never reaches github.com/yalue/
// onnxruntime_go, never invokes cgo, and the shipped binary needs no ONNX
// runtime on disk. See doc.go for how to turn the real backend on.

// Available reports whether this build includes the ONNX backend. False
// here; callers should use it to hide or grey out the setting rather than
// calling New and showing an error nobody can act on without a rebuild.
func Available() bool { return false }

// New always fails in a build without the onnx tag. The error is
// ErrNotBuilt and says what to do about it. Options are not inspected: a
// path problem is not the caller's problem until the backend is compiled
// in at all.
func New(opts Options) (pitch.F0Estimator, error) {
	_ = opts
	return nil, ErrNotBuilt
}
