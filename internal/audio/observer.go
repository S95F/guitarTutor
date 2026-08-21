package audio

type StreamObserver interface {
	SetOnStop(f func())
}
