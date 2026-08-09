package main

import (
	"context"
	"errors"
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
	"github.com/S95F/guitarTutor/internal/score"
	"github.com/S95F/guitarTutor/internal/ui"
)

// liveBackend returns the duplex backend or a friendly explanation.
//
// It is a variable rather than a plain function so tests can substitute a
// fake backend: every path that reaches a real device funnels through
// here, and a unit test must not open the machine's sound card.
var liveBackend = func() (audio.Backend, error) {
	b := audio.Available()
	if b == nil {
		return nil, fmt.Errorf("no live audio backend: this build has no cgo audio support or no audio system initialized (playback still works; live input does not)")
	}
	return b, nil
}

// setEventTap installs (or, with nil, removes) the engine's expected-note
// tap. It is a variable for the same reason liveBackend is: the engine
// offers no way to read the tap back, so nothing else could tell an
// installed tap from a cleared one, and the rollback in setupListen is
// precisely what needs testing.
var setEventTap = func(eng *engine.Engine, fn func(ev score.NoteEvent, outFrame int64)) {
	eng.SetEventTap(fn)
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

// resolveDevice turns a user-supplied device query (empty = the system
// default, else a case-insensitive name fragment, or a full backend ID)
// into a device ID. An empty query resolves to the CONCRETE default
// device's ID when the backend marks one: offsets stored under the
// ambiguous ""|"" key silently followed whatever the system default
// happened to be on the day.
func resolveDevice(devs []audio.DeviceInfo, kind, query string) (string, error) {
	if query == "" {
		return defaultDeviceID(devs), nil
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

// defaultDeviceID returns the ID of the device the backend marks as the
// system default, or "" when none is marked.
func defaultDeviceID(devs []audio.DeviceInfo) string {
	for _, d := range devs {
		if d.Default {
			return d.ID
		}
	}
	return ""
}

// deviceLabel names a device ID for humans: the enumerated device's name,
// "system default" for an empty ID, or the raw ID for a device that is not
// currently enumerated (unplugged since it was remembered).
func deviceLabel(devs []audio.DeviceInfo, id string) string {
	if id == "" {
		return "system default"
	}
	for _, d := range devs {
		if d.ID == id {
			return d.Name
		}
	}
	return id
}

// fillDeviceID resolves one endpoint: an explicit flag query wins, the
// config's remembered device fills an empty flag, and with neither the
// concrete system default is chosen.
func fillDeviceID(devs []audio.DeviceInfo, kind, query, remembered string) (string, error) {
	if query == "" && remembered != "" {
		return remembered, nil
	}
	return resolveDevice(devs, kind, query)
}

// fillDeviceIDs applies fillDeviceID to both endpoints. Both the listen and
// calibrate paths resolve through here, so an offset is stored and looked
// up under the same key. Pure — the seam the tests drive with fake device
// lists and configs.
func fillDeviceIDs(capture, playback []audio.DeviceInfo, cfg appconfig.Config, inQ, outQ string) (inID, outID string, err error) {
	if inID, err = fillDeviceID(capture, "capture", inQ, cfg.CaptureDeviceID); err != nil {
		return "", "", err
	}
	if outID, err = fillDeviceID(playback, "playback", outQ, cfg.PlaybackDeviceID); err != nil {
		return "", "", err
	}
	return inID, outID, nil
}

// resolveDevices enumerates the backend's devices and resolves the -in/-out
// flag values with the config's remembered devices filling the gaps (flags
// win). The device lists are returned for labeling.
func resolveDevices(b audio.Backend, cfg appconfig.Config, inQ, outQ string) (inID, outID string, capture, playback []audio.DeviceInfo, err error) {
	capture, playback, err = b.Devices()
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("enumerating devices: %w", err)
	}
	inID, outID, err = fillDeviceIDs(capture, playback, cfg, inQ, outQ)
	if err != nil {
		return "", "", nil, nil, err
	}
	return inID, outID, capture, playback, nil
}

// calibratedOffset looks up the stored latency offset for a device pair.
// A legacy ""|"" entry (both IDs empty) predates concrete default-device
// resolution and followed whatever the system default happened to be when
// it was measured, so it is treated as uncalibrated: the warning fires and
// the next calibration stores the offset under real device IDs.
func calibratedOffset(cfg appconfig.Config, inID, outID string) (int, bool) {
	if inID == "" && outID == "" {
		return 0, false
	}
	return cfg.OffsetFor(inID, outID)
}

// Calibration parameters: 8 clicks one second apart. Estimate searches up
// to 500 ms of delay per click, and a delay approaching the spacing is
// indistinguishable from the next click arriving — a full second of
// spacing keeps every sane round trip far below the alias point
// (internal/latency refuses the alias signature outright).
const (
	calClicks  = 8
	calSpacing = sampleRate
)

// advanceLagFrames delays miss finalization behind the capture clock: the
// tracker only reports a note when it CLOSES, so a sustained note's
// detection arrives roughly its own duration late and must not be
// pre-judged as a miss (see practice.Scorer.Advance). Four seconds covers
// any note a practice piece holds; the cost is that a miss shows up on
// the tab ~4 s after the fact.
const advanceLagFrames = 4 * sampleRate

// waitArmGraceFrames is how far before the gate arms a confirming attack
// may have started (~150 ms): a player anticipating the wait point by a
// hair still confirms, but a note ringing from before the wait cannot
// auto-release a same-key wait point.
const waitArmGraceFrames = 15 * sampleRate / 100

// listenUI is the slice of the UI the analysis callback feeds; *ui.App
// implements it, tests substitute a recorder.
type listenUI interface {
	OfferResults([]practice.NoteResult)
	OfferTuner(pitch.Note, bool)
}

// newOnNotes builds the analysis callback: detections feed the scorer and
// tuner, and while the engine waits the gate decides when to release.
//
// The wait is tracked by Engine.WaitGeneration, which increments per
// engaged wait: unlike the wait tick it distinguishes a seek's re-wait or
// a loop wrap at the same tick, with no confirm-time reset to get wrong.
// The gate arms with minStart = consumed - waitArmGraceFrames so only a
// fresh attack confirms. On satisfaction the confirming detections are
// recorded with Scorer.WaitConfirmed BEFORE ConfirmWait, so the released
// events get a pitch-only verdict instead of being timing-judged against
// the machinery's own latency.
func newOnNotes(eng *engine.Engine, app listenUI, scorer *practice.Scorer, gate *practice.WaitGate) func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {
	// State below is owned by the analysis goroutine (OnNotes is
	// single-threaded).
	var (
		armedGen    uint64
		armedMin    int64
		armedEvents []score.NoteEvent
		offerBuf    = make([]pitch.Note, 0, 16)
		confirmBuf  = make([]pitch.Note, 0, 16)
		results     []practice.NoteResult
		lastDiscont = eng.DiscontinuityFrame()
	)
	return func(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {
		// Before Advance, so a seek or loop edit abandons what it
		// truncated instead of letting the deadline score it a Miss.
		if d := eng.DiscontinuityFrame(); d != lastDiscont {
			lastDiscont = d
			scorer.AbandonBefore(d)
		}
		scorer.Detected(closed)
		scorer.Advance(consumed - advanceLagFrames)
		results = scorer.Results(results[:0])
		if len(results) > 0 {
			app.OfferResults(results)
		}
		app.OfferTuner(current, sounding)

		evs, waiting := eng.WaitingOn()
		if !waiting {
			return
		}
		if gen := eng.WaitGeneration(); gen != armedGen {
			armedGen = gen
			armedMin = consumed - waitArmGraceFrames
			armedEvents = append(armedEvents[:0], evs...)
			confirmBuf = confirmBuf[:0]
			gate.Arm(evs, armedMin)
		}
		offerBuf = append(offerBuf[:0], closed...)
		if sounding {
			offerBuf = append(offerBuf, current)
		}
		// Collect every fresh attack heard during this wait: a chord
		// confirms across batches, and WaitConfirmed wants all of the
		// confirming detections, not just the batch that completed
		// the set.
		for _, n := range offerBuf {
			if n.Start >= armedMin {
				confirmBuf = mergeNote(confirmBuf, n)
			}
		}
		if len(offerBuf) > 0 && gate.Offer(offerBuf) {
			scorer.WaitConfirmed(armedEvents, confirmBuf)
			eng.ConfirmWait()
		}
	}
}

// mergeNote appends n to buf, replacing an earlier snapshot of the same
// attack (same Start and Key): a still-sounding note is re-offered every
// batch, and its closed form supersedes the running snapshots.
func mergeNote(buf []pitch.Note, n pitch.Note) []pitch.Note {
	for i := range buf {
		if buf[i].Start == n.Start && buf[i].Key == n.Key {
			buf[i] = n
			return buf
		}
	}
	return append(buf, n)
}

// setupListen wires the live practice loop: duplex stream -> engine
// playback + pitch analysis -> scorer and wait gate -> UI feeds.
//
// Every failure path leaves the engine exactly as it was found: in
// particular the event tap is rolled back, so a caller that falls back to
// plain playback is not left with an engine feeding expectations into a
// scorer nobody drains (see the rollback below).
func setupListen(eng *engine.Engine, app *ui.App, track int, inQ, outQ string) (session *live.Session, err error) {
	b, err := liveBackend()
	if err != nil {
		return nil, err
	}
	cfg, cfgErr := appconfig.Load()
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "warning: existing config unreadable, ignoring it:", cfgErr)
	}
	inID, outID, _, _, err := resolveDevices(b, cfg, inQ, outQ)
	if err != nil {
		return nil, err
	}
	offset, calibrated := calibratedOffset(cfg, inID, outID)
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
	// From here on the engine feeds this scorer. If the wiring below
	// fails, the session that would have drained the scorer never exists,
	// and an engine left tapped keeps appending expectations to a list
	// nothing ever reads: unbounded growth, reallocating inside the render
	// callback. Roll the tap back on every failure path — here, at the
	// point that installed it, so no caller has to remember.
	setEventTap(eng, scorer.ExpectNote)
	wired := false
	defer func() {
		if !wired {
			setEventTap(eng, nil)
		}
	}()

	session, err = live.Start(live.Config{
		Backend: b,
		Engine:  eng,
		Stream: audio.StreamConfig{
			SampleRate:     sampleRate,
			CaptureDevice:  inID,
			PlaybackDevice: outID,
		},
		OnNotes: newOnNotes(eng, app, scorer, gate),
		// Chord verification and palm-mute credit (Phase 4) both hang
		// off strums; supplying this callback is what enables them.
		// Without it the session silently scores like Phase 3: one hit
		// and N-1 misses per strummed chord.
		OnStrums: func(sts []pitch.Strum) {
			for _, st := range sts {
				scorer.DetectedStrum(st)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	app.SetLiveStatus(func() (float64, int64) {
		return session.InputLevel(), session.DroppedSamples()
	})
	app.SetWaitControl(true)
	fmt.Printf("listening on %s (offset %d frames, calibrated: %v)\n", b.Name(), offset, calibrated)
	wired = true
	return session, nil
}

// errCalibrationCanceled reports a pass abandoned through its context —
// the caller asked for it (the settings screen was left), so it is not a
// device fault and callers can tell the two apart.
var errCalibrationCanceled = errors.New("calibration canceled")

// calibrationPass plays the click train over a duplex stream, records the
// input, and recovers the round-trip offset. progress, when non-nil, is
// called from the audio thread with the capture's completion in [0, 1] —
// so it must not block; the settings screen posts to a mailbox.
//
// The pass holds the audio device for as long as it runs, so it is
// cancellable: cancelling ctx takes the same Stop/Close exit as the
// timeout and returns errCalibrationCanceled promptly, instead of keeping
// the device for the rest of the 20 s deadline after whoever wanted the
// measurement has gone away.
//
// Shared by the `calibrate` subcommand and the in-app settings screen so
// the two can never measure differently.
func calibrationPass(ctx context.Context, b audio.Backend, inID, outID string, progress func(float64)) (int, float64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Already abandoned: do not touch the device at all.
	if err := ctx.Err(); err != nil {
		return 0, 0, fmt.Errorf("%w: %w", errCalibrationCanceled, err)
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
		if progress != nil {
			progress(float64(len(captured)) / float64(capLen))
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
		return 0, 0, fmt.Errorf("opening duplex stream: %w", err)
	}
	if err := stream.Start(); err != nil {
		stream.Close()
		return 0, 0, fmt.Errorf("starting stream: %w", err)
	}
	// The run captures ~9 s of audio (8 clicks a second apart plus slack);
	// well past that with no completion, no audio is flowing.
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		stream.Stop()
		stream.Close()
		return 0, 0, fmt.Errorf("%w: %w", errCalibrationCanceled, ctx.Err())
	case <-deadline.C:
		stream.Stop()
		stream.Close()
		return 0, 0, fmt.Errorf("calibration timed out — no audio flowed (check the devices with 'guitartutor devices')")
	}
	stream.Stop()
	stream.Close()

	off, conf, err := latency.Estimate(sampleRate, train, captured, calSpacing, calClicks)
	if err != nil {
		return off, conf, fmt.Errorf("could not measure the round trip: %w", err)
	}
	return off, conf, nil
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
	cfg, cfgErr := appconfig.Load()
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "warning: existing config unreadable, starting fresh:", cfgErr)
	}
	// Same resolution as -listen (flags win, remembered devices fill the
	// gaps), so the offset is stored under the key playback will look up.
	inID, outID, capture, playback, err := resolveDevices(b, cfg, *inQ, *outQ)
	if err != nil {
		return err
	}
	fmt.Printf("measuring playback [%s] -> capture [%s]\n",
		deviceLabel(playback, outID), deviceLabel(capture, inID))

	fmt.Println("playing calibration clicks — the input must be able to hear the")
	fmt.Println("output (mic near the speakers, or a loopback cable)...")
	// The subcommand has no UI to abandon it from; Ctrl-C ends the
	// process, so the timeout is the only bound it needs.
	off, conf, err := calibrationPass(context.Background(), b, inID, outID, nil)
	if err != nil {
		return err
	}
	fmt.Printf("round-trip latency: %d frames (%.1f ms), confidence %.2f\n",
		off, float64(off)/sampleRate*1000, conf)

	cfg.SetOffset(inID, outID, off, conf)
	cfg.CaptureDeviceID, cfg.PlaybackDeviceID = inID, outID
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Println("saved — live scoring will use this offset for these devices.")
	return nil
}
