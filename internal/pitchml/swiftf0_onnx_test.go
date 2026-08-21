//go:build onnx

package pitchml

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAvailableIsTrueWithTheTag(t *testing.T) {
	if !Available() {
		t.Error("Available() = false in a build with the onnx tag")
	}
}

func TestNewRejectsBadSampleRateBeforeTouchingTheRuntime(t *testing.T) {
	// Cheap validation must come first: a caller passing 44.1 kHz gets
	// the useful error, not "onnxruntime.dll not found".
	if _, err := New(Options{SampleRate: 44100}); !errors.Is(err, ErrSampleRate) {
		t.Fatalf("err = %v, want ErrSampleRate", err)
	}
}

func TestNewReportsAMissingRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvRuntime, filepath.Join(dir, "onnxruntime-that-is-not-there.dll"))
	t.Setenv(EnvModel, filepath.Join(dir, "swift_f0.onnx"))

	_, err := New(Options{SampleRate: 48000})
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("err = %v, want ErrRuntimeNotFound", err)
	}
}

func TestNewReportsAMissingModel(t *testing.T) {
	dir := t.TempDir()
	// A file that exists but is not a real ONNX Runtime is enough to get
	// past discovery; the model is checked next.
	lib := filepath.Join(dir, runtimeLibName(runtime.GOOS))
	if err := os.WriteFile(lib, []byte("not a library"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRuntime, lib)
	t.Setenv(EnvModel, filepath.Join(dir, "absent.onnx"))

	_, err := New(Options{SampleRate: 48000})
	if !errors.Is(err, ErrModelNotFound) && !errors.Is(err, ErrRuntimeNotFound) {
		// Loading the bogus library fails on some platforms before the
		// model is ever consulted; either error is a correct, named
		// outcome. What must not happen is a panic or a nil error.
		t.Fatalf("err = %v, want ErrModelNotFound or ErrRuntimeNotFound", err)
	}
	if err == nil {
		t.Fatal("err = nil, want a named failure")
	}
}

// TestRealInference is the only test here that exercises actual ONNX
// inference, and it is the check to run first on a machine that has the
// files. It needs BOTH of these present (see doc.go):
//
//   - the ONNX Runtime shared library — onnxruntime.dll on Windows,
//     libonnxruntime.so on Linux, libonnxruntime.dylib on macOS — beside
//     the test binary or at $MUSICTUTOR_ONNXRUNTIME;
//   - swift_f0.onnx, beside the test binary or at
//     $MUSICTUTOR_SWIFTF0_MODEL.
//
// Neither is committed to this repository (a shared library and a model
// are not source), so the test skips until someone installs them:
//
//	MUSICTUTOR_ONNXRUNTIME=/path/to/libonnxruntime.so \
//	MUSICTUTOR_SWIFTF0_MODEL=/path/to/swift_f0.onnx \
//	go test -tags onnx ./internal/pitchml/...
func TestRealInference(t *testing.T) {
	res, err := osResolver().resolve(Options{SampleRate: 48000})
	if err != nil {
		t.Skipf("skipping real inference: %v", err)
	}
	for _, p := range []string{res.RuntimePath, res.ModelPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("skipping real inference: %v", err)
		}
	}

	est, err := New(Options{
		RuntimePath: res.RuntimePath,
		ModelPath:   res.ModelPath,
		SampleRate:  48000,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c, ok := est.(io.Closer); ok {
		defer c.Close()
	}
	if est.Name() != EstimatorName {
		t.Errorf("Name = %q, want %q", est.Name(), EstimatorName)
	}

	// A clean A3 (220 Hz) through a 2048-sample window at 48 kHz, the
	// detector's default geometry. A pitch model that cannot hit this is
	// misconfigured, not merely inaccurate.
	f0, clarity := est.EstimateF0(sine(2048, 220, 48000))
	if e, ok := est.(Estimator); ok && e.Err() != nil {
		t.Fatalf("inference error: %v", e.Err())
	}
	cents := 1200 * math.Log2(f0/220)
	if f0 <= 0 || math.Abs(cents) > 50 {
		t.Errorf("EstimateF0(220 Hz sine) = %.2f Hz (%.0f cents off), want within 50 cents of 220", f0, cents)
	}
	if clarity < 0.5 || clarity > 1 {
		t.Errorf("clarity = %.3f on a clean tone, want a confident value in [0.5, 1]", clarity)
	}

	// Silence must be unvoiced, not a confident guess.
	if f0, clarity := est.EstimateF0(make([]float32, 2048)); f0 != 0 && clarity > 0.5 {
		t.Errorf("silence reported %.2f Hz at clarity %.3f, want unvoiced", f0, clarity)
	}

	// And the steady-state promise the front end makes must survive the
	// real runner: after warm-up, no allocation per window.
	win := sine(2048, 220, 48000)
	est.EstimateF0(win)
	if n := testing.AllocsPerRun(20, func() { est.EstimateF0(win) }); n != 0 {
		t.Errorf("EstimateF0 allocated %v times per call with the real session, want 0", n)
	}
}
