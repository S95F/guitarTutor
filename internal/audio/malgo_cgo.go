//go:build cgo

package audio

import (
	"fmt"
	"runtime"

	"github.com/gen2brain/malgo"
)

// malgoBackend implements Backend over one miniaudio context
// (github.com/gen2brain/malgo), per docs/DECISIONS.md D1: all live audio
// I/O goes through miniaudio, and capture+playback share a single duplex
// device so both endpoints hang off one callback clock.
type malgoBackend struct {
	ctx *malgo.AllocatedContext
	// backend is the active malgo backend's short name ("wasapi",
	// "coreaudio", …), fixed at context init.
	backend string
}

// Compile-time checks that the concrete types satisfy the frozen
// interfaces in audio.go plus the optional observer extension.
var (
	_ Backend        = (*malgoBackend)(nil)
	_ Stream         = (*malgoStream)(nil)
	_ StreamObserver = (*malgoStream)(nil)
)

// malgoBackendNull is the C-side value of miniaudio's device-free null
// backend. malgo v0.11.25's Backend constants omit ma_backend_custom —
// the second-to-last member of miniaudio's ma_backend enum — so the one
// Go constant after it, malgo.BackendNull (13), actually names
// ma_backend_custom and initializing it fails with "No backend"; the real
// null backend is one past it (14). Every other constant matches its C
// value, so the backends production uses are unaffected. Only tests
// request the null backend (see newMalgoBackendFrom for why production
// never does), so the corrected value lives here instead of a malgo fork.
// Drop this when upstream adds BackendCustom to the enum.
const malgoBackendNull = malgo.Backend(malgo.BackendNull + 1)

// newMalgoBackend initializes a miniaudio context on the most preferred
// backend that works: WASAPI first on Windows, the platform's natural
// order elsewhere (see preferredBackends).
func newMalgoBackend() (*malgoBackend, error) {
	return newMalgoBackendFrom(preferredBackends())
}

// newMalgoBackendFrom tries each candidate backend in order and returns a
// context on the first that initializes. Candidates are tried one at a
// time — malgo does not report which backend a multi-candidate init
// chose, and Name must — and the list deliberately excludes BackendNull:
// miniaudio's null backend always initializes, and silently opening a fake
// device on a machine with no audio would mask the real failure. (Tests
// construct a null-backend instance explicitly for a device-free
// full-path run.) A machine where every candidate fails gets an error,
// and Available degrades to playback-only.
func newMalgoBackendFrom(candidates []malgo.Backend) (*malgoBackend, error) {
	var firstErr error
	for _, be := range candidates {
		ctx, err := malgo.InitContext([]malgo.Backend{be}, malgo.ContextConfig{}, nil)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("audio: init %s context: %w", malgoBackendName(be), err)
			}
			continue
		}
		return &malgoBackend{ctx: ctx, backend: malgoBackendName(be)}, nil
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("audio: no miniaudio backend candidates for %s", runtime.GOOS)
	}
	return nil, firstErr
}

// preferredBackends returns the ordered candidate list for this OS. The
// orders mirror miniaudio's own default priorities with the null backend
// removed (see newMalgoBackendFrom); on Windows that puts WASAPI first,
// with DirectSound and WinMM as fallbacks for broken WASAPI setups.
func preferredBackends() []malgo.Backend {
	switch runtime.GOOS {
	case "windows":
		return []malgo.Backend{malgo.BackendWasapi, malgo.BackendDsound, malgo.BackendWinmm}
	case "darwin", "ios":
		return []malgo.Backend{malgo.BackendCoreaudio}
	case "linux":
		return []malgo.Backend{malgo.BackendPulseaudio, malgo.BackendAlsa, malgo.BackendJack}
	case "android":
		return []malgo.Backend{malgo.BackendAaudio, malgo.BackendOpensl}
	case "openbsd":
		return []malgo.Backend{malgo.BackendSndio, malgo.BackendOss}
	case "netbsd":
		return []malgo.Backend{malgo.BackendAudio4, malgo.BackendOss}
	default:
		// Unrecognized OS: try everything miniaudio might have, in its
		// default priority order.
		return []malgo.Backend{
			malgo.BackendWasapi, malgo.BackendDsound, malgo.BackendWinmm,
			malgo.BackendCoreaudio, malgo.BackendSndio, malgo.BackendAudio4,
			malgo.BackendOss, malgo.BackendPulseaudio, malgo.BackendAlsa,
			malgo.BackendJack, malgo.BackendAaudio, malgo.BackendOpensl,
		}
	}
}

// malgoBackendName maps a malgo backend constant to its short lowercase
// name as used in Name() and log lines.
func malgoBackendName(be malgo.Backend) string {
	switch be {
	case malgo.BackendWasapi:
		return "wasapi"
	case malgo.BackendDsound:
		return "dsound"
	case malgo.BackendWinmm:
		return "winmm"
	case malgo.BackendCoreaudio:
		return "coreaudio"
	case malgo.BackendSndio:
		return "sndio"
	case malgo.BackendAudio4:
		return "audio4"
	case malgo.BackendOss:
		return "oss"
	case malgo.BackendPulseaudio:
		return "pulseaudio"
	case malgo.BackendAlsa:
		return "alsa"
	case malgo.BackendJack:
		return "jack"
	case malgo.BackendAaudio:
		return "aaudio"
	case malgo.BackendOpensl:
		return "opensl"
	case malgo.BackendWebaudio:
		return "webaudio"
	case malgoBackendNull:
		return "null"
	default:
		return fmt.Sprintf("backend-%d", uint32(be))
	}
}

// Name identifies the backend for logs and the device-picker UI, e.g.
// "miniaudio/wasapi".
func (b *malgoBackend) Name() string {
	return "miniaudio/" + b.backend
}

// Devices lists capture and playback endpoints known to the context.
func (b *malgoBackend) Devices() (capture, playback []DeviceInfo, err error) {
	capture, err = b.devices(malgo.Capture, "capture")
	if err != nil {
		return nil, nil, err
	}
	playback, err = b.devices(malgo.Playback, "playback")
	if err != nil {
		return nil, nil, err
	}
	return capture, playback, nil
}

// devices enumerates one endpoint kind and converts malgo's records to
// DeviceInfo: the fixed-size ID bytes become the stable hex form
// (encodeDeviceID) that OpenDuplex decodes back.
func (b *malgoBackend) devices(kind malgo.DeviceType, kindName string) ([]DeviceInfo, error) {
	infos, err := b.ctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("audio: enumerate %s devices: %w", kindName, err)
	}
	out := make([]DeviceInfo, 0, len(infos))
	for i := range infos {
		out = append(out, DeviceInfo{
			ID:      encodeDeviceID(infos[i].ID[:]),
			Name:    infos[i].Name(),
			Default: infos[i].IsDefault != 0,
		})
	}
	return out, nil
}

// close releases the miniaudio context. Only tests use it: the process-wide
// backend Available returns lives for the program's lifetime, and malgo
// documents that uninitializing a context with live devices is undefined.
func (b *malgoBackend) close() {
	_ = b.ctx.Uninit()
	b.ctx.Free()
}
