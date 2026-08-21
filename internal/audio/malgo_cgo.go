//go:build cgo

package audio

import (
	"fmt"
	"runtime"

	"github.com/gen2brain/malgo"
)

type malgoBackend struct {
	ctx *malgo.AllocatedContext

	backend string
}

var (
	_ Backend        = (*malgoBackend)(nil)
	_ Stream         = (*malgoStream)(nil)
	_ StreamObserver = (*malgoStream)(nil)
)

const malgoBackendNull = malgo.Backend(malgo.BackendNull + 1)

func newMalgoBackend() (*malgoBackend, error) {
	return newMalgoBackendFrom(preferredBackends())
}

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

		return []malgo.Backend{
			malgo.BackendWasapi, malgo.BackendDsound, malgo.BackendWinmm,
			malgo.BackendCoreaudio, malgo.BackendSndio, malgo.BackendAudio4,
			malgo.BackendOss, malgo.BackendPulseaudio, malgo.BackendAlsa,
			malgo.BackendJack, malgo.BackendAaudio, malgo.BackendOpensl,
		}
	}
}

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

func (b *malgoBackend) Name() string {
	return "miniaudio/" + b.backend
}

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

func (b *malgoBackend) close() {
	_ = b.ctx.Uninit()
	b.ctx.Free()
}
