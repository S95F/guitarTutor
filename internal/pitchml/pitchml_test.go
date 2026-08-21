package pitchml

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeFS struct {
	files map[string]bool
	dirs  map[string]bool
	env   map[string]string
	exe   string
	exeEr error
	goos  string
}

func newFakeFS(goos, exe string) *fakeFS {
	return &fakeFS{
		files: map[string]bool{},
		dirs:  map[string]bool{},
		env:   map[string]string{},
		exe:   exe,
		goos:  goos,
	}
}

func (f *fakeFS) addFile(parts ...string) *fakeFS {
	p := filepath.Join(parts...)
	f.files[p] = true
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		f.dirs[d] = true
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	return f
}

func (f *fakeFS) addDir(parts ...string) *fakeFS {
	f.dirs[filepath.Join(parts...)] = true
	return f
}

func (f *fakeFS) resolver() resolver {
	return resolver{
		lookupEnv: func(k string) (string, bool) { v, ok := f.env[k]; return v, ok },
		stat: func(p string) (fs.FileInfo, error) {
			p = filepath.Clean(p)
			if f.files[p] {
				return fakeInfo{name: filepath.Base(p)}, nil
			}
			if f.dirs[p] {
				return fakeInfo{name: filepath.Base(p), dir: true}, nil
			}
			return nil, &fs.PathError{Op: "CreateFile", Path: p, Err: fs.ErrNotExist}
		},
		exeDir: func() (string, error) {
			if f.exeEr != nil {
				return "", f.exeEr
			}
			return f.exe, nil
		},
		goos: f.goos,
	}
}

type fakeInfo struct {
	name string
	dir  bool
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() fs.FileMode  { return 0 }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.dir }
func (i fakeInfo) Sys() any           { return nil }

var _ fs.FileInfo = fakeInfo{}

func TestRuntimeLibNamePerGOOS(t *testing.T) {
	for goos, want := range map[string]string{
		"windows": "onnxruntime.dll",
		"darwin":  "libonnxruntime.dylib",
		"linux":   "libonnxruntime.so",
		"freebsd": "libonnxruntime.so",
	} {
		if got := runtimeLibName(goos); got != want {
			t.Errorf("runtimeLibName(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestResolveFindsFilesBesideExecutable(t *testing.T) {
	f := newFakeFS("windows", `C:\app`)
	f.addFile(`C:\app`, "onnxruntime.dll")
	f.addFile(`C:\app`, DefaultModelName)

	got, err := f.resolver().resolve(Options{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(`C:\app`, "onnxruntime.dll"); got.RuntimePath != want {
		t.Errorf("RuntimePath = %q, want %q", got.RuntimePath, want)
	}
	if want := filepath.Join(`C:\app`, DefaultModelName); got.ModelPath != want {
		t.Errorf("ModelPath = %q, want %q", got.ModelPath, want)
	}
	if got.SampleRate != DefaultSampleRate {
		t.Errorf("SampleRate = %d, want %d", got.SampleRate, DefaultSampleRate)
	}
	if got.Ratio != 3 {
		t.Errorf("Ratio = %d, want 3", got.Ratio)
	}
}

func TestResolveSearchesDocumentedSubdirectories(t *testing.T) {
	f := newFakeFS("linux", "/opt/gt")
	f.addFile("/opt/gt", "onnxruntime", "lib", "libonnxruntime.so")
	f.addFile("/opt/gt", "models", DefaultModelName)

	got, err := f.resolver().resolve(Options{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join("/opt/gt", "onnxruntime", "lib", "libonnxruntime.so"); got.RuntimePath != want {
		t.Errorf("RuntimePath = %q, want %q", got.RuntimePath, want)
	}
	if want := filepath.Join("/opt/gt", "models", DefaultModelName); got.ModelPath != want {
		t.Errorf("ModelPath = %q, want %q", got.ModelPath, want)
	}
}

func TestResolveAcceptsAltModelName(t *testing.T) {
	f := newFakeFS("linux", "/opt/gt")
	f.addFile("/opt/gt", "libonnxruntime.so")
	f.addFile("/opt/gt", AltModelName)

	got, err := f.resolver().resolve(Options{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join("/opt/gt", AltModelName); got.ModelPath != want {
		t.Errorf("ModelPath = %q, want %q", got.ModelPath, want)
	}
}

func TestResolvePrecedenceOptionsBeatsEnvBeatsExeDir(t *testing.T) {
	f := newFakeFS("linux", "/opt/gt")
	f.addFile("/opt/gt", "libonnxruntime.so")
	f.addFile("/opt/gt", DefaultModelName)
	f.addFile("/env/libonnxruntime.so")
	f.addFile("/env/model.onnx")
	f.addFile("/explicit/libonnxruntime.so")
	f.addFile("/explicit/model.onnx")
	f.env[EnvRuntime] = "/env/libonnxruntime.so"
	f.env[EnvModel] = "/env/model.onnx"

	got, err := f.resolver().resolve(Options{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.RuntimePath != "/env/libonnxruntime.so" || got.ModelPath != "/env/model.onnx" {
		t.Errorf("env did not win: %+v", got)
	}

	got, err = f.resolver().resolve(Options{
		RuntimePath: "/explicit/libonnxruntime.so",
		ModelPath:   "/explicit/model.onnx",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.RuntimePath != "/explicit/libonnxruntime.so" || got.ModelPath != "/explicit/model.onnx" {
		t.Errorf("options did not win: %+v", got)
	}
}

func TestResolveAcceptsDirectories(t *testing.T) {

	f := newFakeFS("windows", `C:\app`)
	f.addFile(`C:\dl\onnxruntime-win-x64-1.20.1`, "lib", "onnxruntime.dll")
	f.addFile(`C:\dl\models`, DefaultModelName)

	got, err := f.resolver().resolve(Options{
		RuntimePath: `C:\dl\onnxruntime-win-x64-1.20.1`,
		ModelPath:   `C:\dl\models`,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(`C:\dl\onnxruntime-win-x64-1.20.1`, "lib", "onnxruntime.dll"); got.RuntimePath != want {
		t.Errorf("RuntimePath = %q, want %q", got.RuntimePath, want)
	}
	if want := filepath.Join(`C:\dl\models`, DefaultModelName); got.ModelPath != want {
		t.Errorf("ModelPath = %q, want %q", got.ModelPath, want)
	}
}

func TestResolveRejectsDirectoryWithoutTheLibrary(t *testing.T) {
	f := newFakeFS("linux", "/opt/gt")
	f.addDir("/somewhere")

	_, err := f.resolver().resolve(Options{RuntimePath: "/somewhere"})
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("err = %v, want ErrRuntimeNotFound", err)
	}
	assertMentions(t, err, "/somewhere", "libonnxruntime.so", "github.com/microsoft/onnxruntime/releases")
}

func TestRuntimeMissingErrorIsActionable(t *testing.T) {
	f := newFakeFS("windows", `C:\app`)

	_, err := f.resolver().resolve(Options{})
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("err = %v, want ErrRuntimeNotFound", err)
	}

	assertMentions(t, err,
		filepath.Join(`C:\app`, "onnxruntime.dll"),
		filepath.Join(`C:\app`, "onnxruntime", "lib", "onnxruntime.dll"),
		"https://github.com/microsoft/onnxruntime/releases",
		EnvRuntime,
		"Options.RuntimePath",
	)
}

func TestModelMissingErrorIsActionable(t *testing.T) {
	f := newFakeFS("linux", "/opt/gt")
	f.addFile("/opt/gt", "libonnxruntime.so")

	_, err := f.resolver().resolve(Options{})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err = %v, want ErrModelNotFound", err)
	}
	assertMentions(t, err,
		filepath.Join("/opt/gt", DefaultModelName),
		filepath.Join("/opt/gt", "models", DefaultModelName),
		"https://github.com/lars76/swift-f0",
		EnvModel,
		"Options.ModelPath",
	)
}

func TestExplicitMissingPathIsNamedWithItsSource(t *testing.T) {
	f := newFakeFS("linux", "/opt/gt")

	_, err := f.resolver().resolve(Options{RuntimePath: "/nope/libonnxruntime.so"})
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("err = %v, want ErrRuntimeNotFound", err)
	}
	assertMentions(t, err, "Options.RuntimePath", "/nope/libonnxruntime.so")

	f.addFile("/opt/gt", "libonnxruntime.so")
	f.env[EnvModel] = "/nope/model.onnx"
	_, err = f.resolver().resolve(Options{})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err = %v, want ErrModelNotFound", err)
	}
	assertMentions(t, err, EnvModel, "/nope/model.onnx")
}

func TestUnreadableExecutableDirectoryIsReported(t *testing.T) {
	f := newFakeFS("linux", "")
	f.exeEr = errors.New("no /proc/self/exe")

	_, err := f.resolver().resolve(Options{})
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("err = %v, want ErrRuntimeNotFound", err)
	}
	assertMentions(t, err, "no /proc/self/exe", EnvRuntime)

	f.addFile("/pinned/libonnxruntime.so")
	_, err = f.resolver().resolve(Options{RuntimePath: "/pinned/libonnxruntime.so"})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err = %v, want ErrModelNotFound", err)
	}
	assertMentions(t, err, "no /proc/self/exe", EnvModel)
}

func TestOSResolverIsWiredToTheRealEnvironment(t *testing.T) {

	r := osResolver()
	if r.goos != runtime.GOOS {
		t.Errorf("goos = %q, want %q", r.goos, runtime.GOOS)
	}
	t.Setenv(EnvModel, "sentinel-value")
	if v, ok := r.lookupEnv(EnvModel); !ok || v != "sentinel-value" {
		t.Errorf("lookupEnv = (%q, %v), want the value just set", v, ok)
	}
	dir, err := r.exeDir()
	if err != nil || dir == "" {
		t.Errorf("exeDir = (%q, %v), want the test binary's directory", dir, err)
	}
	self := filepath.Join(dir, filepath.Base(os.Args[0]))
	if !r.isFile(self) {
		t.Errorf("isFile(%q) = false, want true for the running test binary", self)
	}
	if r.isFile(dir) {
		t.Errorf("isFile(%q) = true for a directory, want false", dir)
	}
}

func TestResolveSampleRates(t *testing.T) {
	f := newFakeFS("linux", "/opt/gt")
	f.addFile("/opt/gt", "libonnxruntime.so")
	f.addFile("/opt/gt", DefaultModelName)

	for _, tc := range []struct {
		rate      int
		wantRatio int
	}{
		{0, 3},
		{16000, 1},
		{32000, 2},
		{48000, 3},
		{96000, 6},
	} {
		got, err := f.resolver().resolve(Options{SampleRate: tc.rate})
		if err != nil {
			t.Errorf("resolve(%d): %v", tc.rate, err)
			continue
		}
		if got.Ratio != tc.wantRatio {
			t.Errorf("resolve(%d).Ratio = %d, want %d", tc.rate, got.Ratio, tc.wantRatio)
		}
	}

	for _, rate := range []int{-1, 1, 8000, 22050, 44100, 88200} {
		_, err := f.resolver().resolve(Options{SampleRate: rate})
		if !errors.Is(err, ErrSampleRate) {
			t.Errorf("resolve(%d) err = %v, want ErrSampleRate", rate, err)
		}
	}

	_, err := f.resolver().resolve(Options{SampleRate: 44100})
	assertMentions(t, err, "44100", "16000", "48000")
}

func assertMentions(t *testing.T, err error, needles ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	msg := err.Error()
	for _, n := range needles {
		if !strings.Contains(msg, n) {
			t.Errorf("error does not mention %q:\n%s", n, msg)
		}
	}
}
