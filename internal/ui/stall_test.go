package ui

import "testing"

func TestPlayingWithFrozenAudioRaisesTheBanner(t *testing.T) {
	a := newApp(t, 4)
	a.eng.Play()
	for i := 0; i <= audioStallAfter; i++ {
		a.checkAudioStalled()
		a.syncLive()
	}
	if !a.warningVisible() || a.warnMsg != audioStallWarning {
		t.Fatalf("after %d frozen frames: visible %v msg %q", audioStallAfter+1, a.warningVisible(), a.warnMsg)
	}

	buf := make([]byte, 4096)
	if _, err := a.eng.Read(buf); err != nil {
		t.Fatalf("engine Read: %v", err)
	}
	a.checkAudioStalled()
	a.syncLive()
	if a.warnMsg == audioStallWarning {
		t.Error("the banner did not clear once audio started moving")
	}
}

func TestPausedPlaybackIsNeverCalledStalled(t *testing.T) {
	a := newApp(t, 4)
	for i := 0; i <= 2*audioStallAfter; i++ {
		a.checkAudioStalled()
		a.syncLive()
	}
	if a.warnMsg == audioStallWarning {
		t.Error("a paused piece was reported as stalled audio")
	}
}
