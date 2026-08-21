//go:build !onnx

package pitchml

import "github.com/S95F/musicTutor/internal/pitch"

func Available() bool { return false }

func New(opts Options) (pitch.F0Estimator, error) {
	_ = opts
	return nil, ErrNotBuilt
}
