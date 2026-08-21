//go:build !onnx

package pitchml

import (
	"errors"
	"strings"
	"testing"
)

func TestAvailableIsFalseWithoutTheTag(t *testing.T) {
	if Available() {
		t.Error("Available() = true in a build without the onnx tag")
	}
}

func TestNewReturnsErrNotBuilt(t *testing.T) {
	est, err := New(Options{})
	if est != nil {
		t.Errorf("New returned an estimator %v in a build without the onnx tag", est)
	}
	if !errors.Is(err, ErrNotBuilt) {
		t.Fatalf("err = %v, want ErrNotBuilt", err)
	}

	for _, want := range []string{"built without the onnx tag", "-tags onnx", "ONNX runtime"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err.Error())
		}
	}
}

func TestNewFailsTheSameWayEvenWithValidLookingOptions(t *testing.T) {

	_, err := New(Options{
		ModelPath:   "swift_f0.onnx",
		RuntimePath: "onnxruntime.dll",
		SampleRate:  48000,
	})
	if !errors.Is(err, ErrNotBuilt) {
		t.Fatalf("err = %v, want ErrNotBuilt", err)
	}
}
