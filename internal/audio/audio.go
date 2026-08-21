package audio

type DeviceInfo struct {
	ID      string
	Name    string
	Default bool
}

type StreamConfig struct {
	SampleRate int

	PeriodFrames int

	CaptureDevice  string
	PlaybackDevice string
}

type DuplexHandler func(in, outL, outR []float32)

type Stream interface {
	Start() error
	Stop() error

	Close() error

	Config() StreamConfig
}

type Backend interface {
	Name() string

	Devices() (capture, playback []DeviceInfo, err error)

	OpenDuplex(cfg StreamConfig, h DuplexHandler) (Stream, error)
}
