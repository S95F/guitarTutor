package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/S95F/guitarTutor/internal/appconfig"
	"github.com/S95F/guitarTutor/internal/audio"
	"github.com/S95F/guitarTutor/internal/engine"
	"github.com/S95F/guitarTutor/internal/latency"
	"github.com/S95F/guitarTutor/internal/live"
	"github.com/S95F/guitarTutor/internal/pitch"
	"github.com/S95F/guitarTutor/internal/practice"
	"github.com/S95F/guitarTutor/internal/ui"
)

// liveBackend returns the duplex backend or a friendly explanation.
func liveBackend() (audio.Backend, error) {
	b := audio.Available()
	if b == nil {
		return nil, fmt.Errorf("no live audio backend: this build has no cgo audio support or no audio system initialized (playback still works; live input does not)")
	}
	return b, nil
}

// runDevices lists capture and playback endpoints by name; -in/-out take a
// unique, case-insensitive fragment of these names (raw backend IDs are
// hundreds of characters and stay internal).
func runDevices(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: guitartutor devices")
	}
	b, err := liveBackend()
	if err != nil {
		return err
	}
	capture, playback, err := b.Devices()
	if err != nil {
		return fmt.Errorf("enumerating devices: %w", err)
	}
	fmt.Printf("backend: %s\n\ncapture (guitar in):\n", b.Name())
	for _, d := range capture {
		mark := " "
		if d.Default {
			mark = "*"
		}
		fmt.Printf(" %s %s\n", mark, d.Name)
	}
	fmt.Println("\nplayback:")
	for _, d := range playback {
		mark := " "
		if d.Default {
			mark = "*"
		}
		fmt.Printf(" %s %s\n", mark, d.Name)
	}
	fmt.Println(`
* = system default. Select devices with a unique part of the name, e.g.
-in scarlett -out scarlett. For reliable scoring, put capture and playback
on the SAME physical interface (plug headphones into it) — separate
devices run on separate clocks that drift apart over a session.`)
	return nil
}

// resolveDevice turns a user-supplied device query (empty = default, else a
// case-insensitive name fragment, or a full backend ID) into a device ID.
func resolveDevice(devs []audio.DeviceInfo, kind, query string) (string, error) {
	if query == "" {
		return "", nil
	}
	var matches []audio.DeviceInfo
	q := strings.ToLower(query)
	for _, d := range devs {
		if d.ID == query {
			return d.ID, nil
		}
		if strings.Contains(strings.ToLower(d.Name), q) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", fmt.Errorf("no %s device matches %q (run 'guitartutor devices')", kind, query)
	default:
		names := make([]string, len(matches))
		for i, d := range matches {
			names[i] = d.Name
		}
		return "", fmt.Errorf("%q matches %d %s devices (%s) — be more specific", query, len(matches), kind, strings.Join(names, "; "))
	}
}

// resolveDevices resolves the -in/-out flag values against the backend.
func resolveDevices(b audio.Backend, inQ, outQ string) (inID, outID string, err error) {
	capture, playback, err := b.Devices()
	if err != nil {
		return "", "", fmt.Errorf("enumerating devices: %w", err)
	}
	if inID, err = resolveDevice(capture, "capture", inQ); err != nil {
		return "", "", err
	}
	if outID, err = resolveDevice(playback, "playback", outQ); err != nil {
		return "", "", err
	}
	return inID, outID, nil
}

// Calibration parameters: 8 clicks half a second apart bounds the
// detectable round trip at ~500 ms, far above any sane setup.
const (
	calClicks  = 8
	calSpacing = sampleRate / 2
)

// advanceLagFrames delays miss finalization behind the capture clock: the
// tracker only reports a note when it CLOSES, so a sustained note's
// detection arrives roughly its own duration late and must not be
// pre-judged as a miss (see practice.Scorer.Advance). Four seconds covers
// any note a practice piece holds; the cost is that a miss shows up on
// the tab ~4 s after the fact.
const advanceLagFrames = 4 * sampleRate

// setupListen wires the live practice loop: duplex stream -> engine
// playback + pitch analysis -> scorer and wait gate -> UI feeds.
func setupListen(eng *engine.Engine, app *ui.App, track int, inQ, outQ string) (*live.Session, error) {
	b, err := liveBackend()
	if err != nil {
		return nil, err
	}
	inID, outID, err := resolveDevices(b, inQ, outQ)
	if err != nil {
		return nil, err
	}
	cfg, cfgErr := appconfig.Load()
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "warning: existing config unreadable, ignoring it:", cfgErr)
	}
	// Flags win; the config's remembered devices fill the gaps.
	if inQ == "" && cfg.CaptureDeviceID != "" {
		inID = cfg.CaptureDeviceID
	}
	if outQ == "" && cfg.PlaybackDeviceID != "" {
		outID = cfg.PlaybackDeviceID
	}
	offset, calibrated := cfg.OffsetFor(inID, outID)
	if !calibrated {
		fmt.Fprintln(os.Stderr, "warning: no latency calibration for these devices — run 'guitartutor calibrate'.")
		fmt.Fprintln(os.Stderr, "Scoring works, but timing verdicts are skewed by the unmeasured round trip.")
	}

	pcfg := practice.Config{
		SampleRate:          sampleRate,
		Track:               track,
		LatencyOffsetFrames: offset,
	}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	eng.SetEventTap(scorer.ExpectNote)

	// State owned by the analysis goroutine (OnNotes is single-threaded).
	armedTick := int64(-1)
	offerBuf := make([]pitch.Note, 0, 16)
	var results []practice.NoteResult

	onNotes := func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {
		scorer.Detected(closed)
		scorer.Advance(consumed - advanceLagFrames)
		results = scorer.Results(results[:0])
		if len(results) > 0 {
			app.OfferResults(results)
		}
		app.OfferTuner(current, sounding)

		if evs, waiting := eng.WaitingOn(); waiting {
			// Re-arm on each new wait point. ConfirmWait resets
			// armedTick, so a loop wrap waiting on the same tick
			// re-arms too.
			if len(evs) > 0 && evs[0].Start != armedTick {
				gate.Arm(evs)
				armedTick = evs[0].Start
			}
			offerBuf = append(offerBuf[:0], closed...)
			if sounding {
				offerBuf = append(offerBuf, current)
			}
			if len(offerBuf) > 0 && gate.Offer(offerBuf) {
				eng.ConfirmWait()
				armedTick = -1
			}
		} else {
			armedTick = -1
		}
	}

	session, err := live.Start(live.Config{
		Backend: b,
		Engine:  eng,
		Stream: audio.StreamConfig{
			SampleRate:     sampleRate,
			CaptureDevice:  inID,
			PlaybackDevice: outID,
		},
		OnNotes: onNotes,
	})
	if err != nil {
		return nil, err
	}
	app.SetLiveStatus(func() (float64, int64) {
		return session.InputLevel(), session.DroppedSamples()
	})
	app.SetWaitControl(true)
	fmt.Printf("listening on %s (offset %d frames, calibrated: %v)\n", b.Name(), offset, calibrated)
	return session, nil
}

// runCalibrate measures the round-trip latency offset and stores it.
func runCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	inQ := fs.String("in", "", "capture device (name fragment)")
	outQ := fs.String("out", "", "playback device (name fragment)")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: guitartutor calibrate [-in device] [-out device]")
	}
	b, err := liveBackend()
	if err != nil {
		return err
	}
	inID, outID, err := resolveDevices(b, *inQ, *outQ)
	if err != nil {
		return err
	}

	train := latency.ClickTrain(sampleRate, calClicks, calSpacing)
	// Record for the train plus a second of slack so a large delay still
	// lands inside the capture.
	capLen := len(train) + sampleRate
	captured := make([]float32, 0, capLen)
	pos := 0
	done := make(chan struct{})
	var once sync.Once

	// The handler owns pos/captured exclusively (single audio thread);
	// the append never reallocates (capacity fixed above).
	handler := func(in, outL, outR []float32) {
		for i := range outL {
			var s float32
			if pos < len(train) {
				s = train[pos]
			}
			outL[i], outR[i] = s, s
			pos++
		}
		if room := capLen - len(captured); room > 0 {
			n := len(in)
			if n > room {
				n = room
			}
			captured = append(captured, in[:n]...)
		}
		if pos >= capLen && len(captured) >= capLen {
			once.Do(func() { close(done) })
		}
	}

	stream, err := b.OpenDuplex(audio.StreamConfig{
		SampleRate:     sampleRate,
		CaptureDevice:  inID,
		PlaybackDevice: outID,
	}, handler)
	if err != nil {
		return fmt.Errorf("opening duplex stream: %w", err)
	}
	fmt.Println("playing calibration clicks — the input must be able to hear the")
	fmt.Println("output (mic near the speakers, or a loopback cable)...")
	if err := stream.Start(); err != nil {
		stream.Close()
		return fmt.Errorf("starting stream: %w", err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		stream.Stop()
		stream.Close()
		return fmt.Errorf("calibration timed out — no audio flowed (check the devices with 'guitartutor devices')")
	}
	stream.Stop()
	stream.Close()

	off, conf, err := latency.Estimate(sampleRate, train, captured, calSpacing, calClicks)
	if err != nil {
		return fmt.Errorf("could not measure the round trip: %w", err)
	}
	fmt.Printf("round-trip latency: %d frames (%.1f ms), confidence %.2f\n",
		off, float64(off)/sampleRate*1000, conf)

	cfg, cfgErr := appconfig.Load()
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "warning: existing config unreadable, starting fresh:", cfgErr)
	}
	cfg.SetOffset(inID, outID, off, conf)
	cfg.CaptureDeviceID, cfg.PlaybackDeviceID = inID, outID
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Println("saved — live scoring will use this offset for these devices.")
	return nil
}
