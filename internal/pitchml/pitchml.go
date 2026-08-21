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

const EstimatorName = "swiftf0"

const (
	EnvRuntime = "MUSICTUTOR_ONNXRUNTIME"

	EnvModel = "MUSICTUTOR_SWIFTF0_MODEL"
)

const (
	DefaultModelName = "swift_f0.onnx"
	AltModelName     = "swiftf0.onnx"
)

const DefaultSampleRate = 48000

type Options struct {
	ModelPath string

	RuntimePath string

	SampleRate int
}

var (
	ErrNotBuilt = errors.New("pitchml: built without the onnx tag; rebuild with -tags onnx and install the ONNX runtime")

	ErrRuntimeNotFound = errors.New("pitchml: ONNX runtime shared library not found")

	ErrModelNotFound = errors.New("pitchml: SwiftF0 model file not found")

	ErrSampleRate = errors.New("pitchml: unsupported sample rate")

	ErrModelLayout = errors.New("pitchml: unexpected model layout")
)

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

type resolver struct {
	lookupEnv func(string) (string, bool)
	stat      func(string) (fs.FileInfo, error)
	exeDir    func() (string, error)
	goos      string
}

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

type resolved struct {
	RuntimePath string
	ModelPath   string
	SampleRate  int
	Ratio       int
}

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

func (r resolver) isFile(path string) bool {
	info, err := r.stat(path)
	return err == nil && !info.IsDir()
}

func cleanStatError(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func runtimeHelp(lib string) string {
	return "Download the ONNX Runtime release for your platform from https://github.com/microsoft/onnxruntime/releases, copy " +
		lib + " out of its lib/ directory to the directory holding the executable, or set " + EnvRuntime +
		" (or Options.RuntimePath) to the file or to the unpacked release directory."
}

func modelHelp() string {
	return "Download or export " + DefaultModelName + " from https://github.com/lars76/swift-f0 (MIT) to the directory holding the executable, or set " +
		EnvModel + " (or Options.ModelPath) to the file. It is intentionally not bundled with musicTutor."
}
