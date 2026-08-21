// Package pitchml is musicTutor's optional ONNX pitch backend: SwiftF0
// (MIT, https://github.com/lars76/swift-f0) wrapped as a
// [pitch.F0Estimator], per docs/DECISIONS.md D4 and ROADMAP Phase 4.
//
// The default engine is the in-house MPM/YIN-FFT detector in
// [github.com/S95F/musicTutor/internal/pitch]; it is pure Go and ships in
// a single binary with no external runtime. This package exists for the
// case that engine handles worst — a noisy, distorted, or heavily
// compressed signal, where autocorrelation octave-flips — and it buys that
// robustness at the price of a shared library and a model file on disk.
// It is therefore behind the "onnx" build tag: a build without the tag
// does not link, load, or even compile the ONNX binding, and [New] returns
// [ErrNotBuilt].
//
// # Enabling it end to end
//
// 1. Get the ONNX Runtime shared library. Download the release for your
// platform from https://github.com/microsoft/onnxruntime/releases and copy
// the library out of its lib/ directory: onnxruntime.dll on Windows,
// libonnxruntime.so on Linux, libonnxruntime.dylib on macOS. Nothing is
// installed system-wide and no headers are needed — the binding loads the
// library by path at runtime (that dynamic load is exactly why D4 picked
// yalue/onnxruntime_go over a statically linked binding).
//
// 2. Get the model. SwiftF0 publishes an exported swift_f0.onnx; see the
// project page above. It is a few megabytes and is deliberately NOT
// committed to this repository.
//
// 3. Build with the tag:
//
//	go build -tags onnx ./cmd/musictutor
//
// 4. Point the app at the two files. Easiest is to drop both beside the
// executable, where they are found with no configuration:
//
//	musictutor.exe
//	onnxruntime.dll        (or onnxruntime/lib/onnxruntime.dll)
//	swift_f0.onnx          (or models/swift_f0.onnx)
//
// Otherwise set [EnvRuntime] and [EnvModel], or fill in
// [Options].RuntimePath and [Options].ModelPath. Both accept either the
// file itself or a directory to search, so pointing at an unpacked ONNX
// Runtime release directory works. Resolution order is explicit option,
// then environment variable, then the locations beside the executable;
// every failure names what was tried and what to do about it.
//
// Then hand the estimator to the detector:
//
//	est, err := pitchml.New(pitchml.Options{SampleRate: 48000})
//	if err != nil {
//		// Fall back to the built-in detector: this is an optional
//		// backend and a missing DLL must never be fatal.
//		log.Println(err)
//	} else {
//		cfg.Estimator = est
//	}
//
// The returned value also satisfies [Estimator], so a caller that owns the
// lifetime should close it:
//
//	if c, ok := est.(io.Closer); ok {
//		defer c.Close()
//	}
//
// # What happens to the audio
//
// SwiftF0 consumes 16 kHz mono; the detector hands whole analysis windows
// at the input rate (48 kHz by default). Each window is low-pass filtered
// and decimated to 16 kHz by a whole-number ratio — the filter is
// specified in decimate.go — then centered in a reusable input tensor, and the
// model's per-frame pitch and confidence outputs are reduced to the one
// (Hz, clarity) pair the [pitch.F0Estimator] contract wants. Windows are
// filtered independently, with zeros outside them: the detector's windows
// overlap heavily, so nothing is lost, and EstimateF0 stays a pure
// function of the window it is given.
//
// Only input rates that are whole multiples of 16 kHz are supported
// (16000, 32000, 48000); 44.1 kHz returns [ErrSampleRate] rather than
// quietly resampling badly. After the first call the estimator allocates
// nothing per window.
//
// # Status: unvalidated
//
// Honest disclosure: this package was written without the ONNX runtime or
// the SwiftF0 model present, so nothing here has ever executed a real
// inference. Everything that can be tested without those files is tested —
// path and environment resolution, error messages, the anti-alias filter's
// frequency response, input-length and centering behavior against a fake
// runner, output aggregation, and the steady-state no-allocation
// property. The tensor plumbing in swiftf0_onnx.go — the model's input and
// output names, its tensor rank, and the pitch/confidence output mapping —
// is derived from the model's own metadata at load time rather than
// hard-coded, but the first person to run it with real files should expect
// to confirm (or fix) that layer. TestRealInference in swiftf0_onnx_test.go
// is the check to run; it skips until the files exist.
package pitchml
