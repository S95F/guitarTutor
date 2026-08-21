package pitchml

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EstimatorName is the name the returned pitch.F0Estimator reports, for
// logs and the settings UI.
const EstimatorName = "swiftf0"

// Environment variables that locate the two files this backend needs.
// Both accept either the file itself or a directory to search.
const (
	// EnvRuntime locates the ONNX Runtime shared library
	// (onnxruntime.dll / libonnxruntime.so / libonnxruntime.dylib).
	EnvRuntime = "MUSICTUTOR_ONNXRUNTIME"
	// EnvModel locates the exported SwiftF0 model file.
	EnvModel = "MUSICTUTOR_SWIFTF0_MODEL"
)

// DefaultModelName is the file name looked for beside the executable when
// neither Options.ModelPath nor EnvModel is set. AltModelName is also
// accepted because the upstream export script has used both spellings.
const (
	DefaultModelName = "swift_f0.onnx"
	AltModelName     = "swiftf0.onnx"
)

// DefaultSampleRate is assumed when Options.SampleRate is 0; it matches
// pitch.DefaultConfig's expectation of a 48 kHz capture stream.
const DefaultSampleRate = 48000

// Options configures the backend. The zero value is valid: it takes the
// default sample rate and discovers both files from the environment or
// from the directory holding the executable.
type Options struct {
	// ModelPath is the SwiftF0 .onnx file, or a directory containing it.
	// Empty consults EnvModel, then the locations beside the executable.
	ModelPath string
	// RuntimePath is the ONNX Runtime shared library, or a directory
	// containing it (an unpacked release directory works: its lib/
	// subdirectory is searched too). Empty consults EnvRuntime, then the
	// locations beside the executable. The library is loaded by path at
	// run time; nothing has to be installed system-wide.
	RuntimePath string
	// SampleRate of the analysis windows handed to EstimateF0, in Hz.
	// 0 takes DefaultSampleRate. Must be a whole multiple of
	// ModelSampleRate.
	SampleRate int
}

// Errors returned by New. Every one of them is also wrapped with the
// concrete paths that were tried and the action that fixes it, so callers
// should print the error, not just match it.
var (
	// ErrNotBuilt is returned by New in a build without the onnx tag.
	ErrNotBuilt = errors.New("pitchml: built without the onnx tag; rebuild with -tags onnx and install the ONNX runtime")
	// ErrRuntimeNotFound means the ONNX Runtime shared library could not
	// be located (or is not a file).
	ErrRuntimeNotFound = errors.New("pitchml: ONNX runtime shared library not found")
	// ErrModelNotFound means the SwiftF0 model file could not be located.
	ErrModelNotFound = errors.New("pitchml: SwiftF0 model file not found")
	// ErrSampleRate means Options.SampleRate is not a whole multiple of
	// ModelSampleRate, which is all this backend's decimator can do.
	ErrSampleRate = errors.New("pitchml: unsupported sample rate")
	// ErrModelLayout means the .onnx file loaded but does not look like
	// SwiftF0: wrong number of inputs or outputs, wrong element type, or
	// no output that can be identified as pitch and confidence.
	ErrModelLayout = errors.New("pitchml: unexpected model layout")
)

// runtimeLibName is the ONNX Runtime shared-library file name on goos.
func runtimeLibName(goos string) string {
	switch goos {
	case "windows":
		return "onnxruntime.dll"
	case "darwin":
		return "libonnxruntime.dylib"
	default:
		return "libonnxruntime.so"
	}
}

// A resolver turns Options into concrete file paths. Its three hooks are
// the whole reason it exists: tests drive resolution with a fake
// environment, a fake filesystem, and a fake executable directory, on
// every GOOS, without the onnx tag or either real file.
type resolver struct {
	lookupEnv func(string) (string, bool)
	stat      func(string) (fs.FileInfo, error)
	exeDir    func() (string, error)
	goos      string
}

// osResolver is the production resolver.
func osResolver() resolver {
	return resolver{
		lookupEnv: os.LookupEnv,
		stat:      os.Stat,
		exeDir: func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.Dir(exe), nil
		},
		goos: runtime.GOOS,
	}
}

// resolved is everything New needs after discovery: two existing paths and
// the decimation ratio implied by the sample rate.
type resolved struct {
	RuntimePath string
	ModelPath   string
	SampleRate  int
	Ratio       int
}

// resolve validates the sample rate and locates both files. The sample
// rate is checked first because it is a programming error rather than an
// installation problem, and the runtime before the model because a user
// missing both has to fetch the runtime either way.
func (r resolver) resolve(opts Options) (resolved, error) {
	var out resolved

	rate := opts.SampleRate
	if rate == 0 {
		rate = DefaultSampleRate
	}
	if rate < ModelSampleRate || rate%ModelSampleRate != 0 {
		return out, fmt.Errorf("%w: %d Hz. SwiftF0 consumes %d Hz and this backend only decimates by a whole-number ratio, so the input must be %d, %d or %d Hz; resample upstream for anything else (44100 Hz included)",
			ErrSampleRate, rate, ModelSampleRate, ModelSampleRate, 2*ModelSampleRate, 3*ModelSampleRate)
	}
	out.SampleRate = rate
	out.Ratio = rate / ModelSampleRate

	rt, err := r.runtimePath(opts.RuntimePath)
	if err != nil {
		return out, err
	}
	out.RuntimePath = rt

	model, err := r.modelPath(opts.ModelPath)
	if err != nil {
		return out, err
	}
	out.ModelPath = model
	return out, nil
}

// runtimePath locates the ONNX Runtime shared library.
func (r resolver) runtimePath(explicit string) (string, error) {
	lib := runtimeLibName(r.goos)
	inDir := []string{lib, filepath.Join("lib", lib)}

	if explicit != "" {
		p, err := r.locate(explicit, inDir)
		if err != nil {
			return "", fmt.Errorf("%w: Options.RuntimePath %q: %s. %s", ErrRuntimeNotFound, explicit, cleanStatError(err), runtimeHelp(lib))
		}
		return p, nil
	}
	if v, ok := r.lookupEnv(EnvRuntime); ok && v != "" {
		p, err := r.locate(v, inDir)
		if err != nil {
			return "", fmt.Errorf("%w: %s=%q: %s. %s", ErrRuntimeNotFound, EnvRuntime, v, cleanStatError(err), runtimeHelp(lib))
		}
		return p, nil
	}

	dir, dirErr := r.exeDir()
	if dirErr != nil {
		return "", fmt.Errorf("%w: %s is unset and the executable's directory could not be determined (%s). %s", ErrRuntimeNotFound, EnvRuntime, dirErr, runtimeHelp(lib))
	}
	tried := []string{
		filepath.Join(dir, lib),
		filepath.Join(dir, "onnxruntime", lib),
		filepath.Join(dir, "onnxruntime", "lib", lib),
	}
	for _, c := range tried {
		if r.isFile(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w: tried %s. %s", ErrRuntimeNotFound, strings.Join(tried, ", "), runtimeHelp(lib))
}

// modelPath locates the SwiftF0 model file.
func (r resolver) modelPath(explicit string) (string, error) {
	inDir := []string{DefaultModelName, AltModelName, filepath.Join("models", DefaultModelName)}

	if explicit != "" {
		p, err := r.locate(explicit, inDir)
		if err != nil {
			return "", fmt.Errorf("%w: Options.ModelPath %q: %s. %s", ErrModelNotFound, explicit, cleanStatError(err), modelHelp())
		}
		return p, nil
	}
	if v, ok := r.lookupEnv(EnvModel); ok && v != "" {
		p, err := r.locate(v, inDir)
		if err != nil {
			return "", fmt.Errorf("%w: %s=%q: %s. %s", ErrModelNotFound, EnvModel, v, cleanStatError(err), modelHelp())
		}
		return p, nil
	}

	dir, dirErr := r.exeDir()
	if dirErr != nil {
		return "", fmt.Errorf("%w: %s is unset and the executable's directory could not be determined (%s). %s", ErrModelNotFound, EnvModel, dirErr, modelHelp())
	}
	tried := []string{
		filepath.Join(dir, DefaultModelName),
		filepath.Join(dir, AltModelName),
		filepath.Join(dir, "models", DefaultModelName),
	}
	for _, c := range tried {
		if r.isFile(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w: tried %s. %s", ErrModelNotFound, strings.Join(tried, ", "), modelHelp())
}

// locate accepts either the file itself or a directory holding it, trying
// each of inDir (relative paths) inside a directory. Callers render the
// returned error through cleanStatError.
func (r resolver) locate(path string, inDir []string) (string, error) {
	info, err := r.stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	for _, rel := range inDir {
		if c := filepath.Join(path, rel); r.isFile(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("is a directory and contains none of %s", strings.Join(inDir, ", "))
}

// isFile reports whether path exists and is not a directory.
func (r resolver) isFile(path string) bool {
	info, err := r.stat(path)
	return err == nil && !info.IsDir()
}

// cleanStatError trims the *fs.PathError wrapper so the message does not
// repeat a path the caller is about to print anyway.
func cleanStatError(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// runtimeHelp is the actionable half of every runtime error: a missing DLL
// must say what to download and where to put it.
func runtimeHelp(lib string) string {
	return "Download the ONNX Runtime release for your platform from https://github.com/microsoft/onnxruntime/releases, copy " +
		lib + " out of its lib/ directory to the directory holding the executable, or set " + EnvRuntime +
		" (or Options.RuntimePath) to the file or to the unpacked release directory."
}

// modelHelp is the same for the model file.
func modelHelp() string {
	return "Download or export " + DefaultModelName + " from https://github.com/lars76/swift-f0 (MIT) to the directory holding the executable, or set " +
		EnvModel + " (or Options.ModelPath) to the file. It is intentionally not bundled with musicTutor."
}
