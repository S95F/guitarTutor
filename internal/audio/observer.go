package audio

// A StreamObserver is optionally implemented by concrete Stream values
// that can report asynchronous device stops. The Stream interface itself
// stays minimal, so callers discover the capability with a type assertion:
//
//	if o, ok := stream.(StreamObserver); ok {
//		o.SetOnStop(onDeviceStopped)
//	}
type StreamObserver interface {
	// SetOnStop registers f to run when the device stops for any reason
	// other than the caller's own Stop or Close — hot-unplug, a
	// default-device switch the OS could not route around, or backend
	// failure (ROADMAP Phase 2: "handle hot-unplug and default-device
	// switches"). f runs on a backend-owned thread: it must return
	// quickly and must not call back into the Stream; hand off to a
	// channel or atomic instead. Suppression around an explicit
	// Stop/Close is best-effort — a notification already in flight may
	// still be delivered — so treat a call as a hint to re-check state,
	// not proof of failure. Passing nil clears the callback.
	SetOnStop(f func())
}
