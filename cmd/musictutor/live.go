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

	"github.com/S95F/musicTutor/internal/appconfig"
	"github.com/S95F/musicTutor/internal/audio"
	"github.com/S95F/musicTutor/internal/engine"
	"github.com/S95F/musicTutor/internal/latency"
	"github.com/S95F/musicTutor/internal/live"
	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/practice"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/ui"
)

var liveBackend = func() (audio.Backend, error) {
	b := audio.Available()
	if b == nil {
		return nil, fmt.Errorf("no live audio backend: this build has no cgo audio support or no audio system initialized (playback still works; live input does not)")
	}
	return b, nil
}

var setEventTap = func(eng *engine.Engine, fn func(ev score.NoteEvent, outFrame int64)) {
	eng.SetEventTap(fn)
}

func runDevices(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: musictutor devices")
	}
	b, err := liveBackend()
	if err != nil {
		return err
	}
	capture, playback, err := b.Devices()
	if err != nil {
		return fmt.Errorf("enumerating devices: %w", err)
	}
	fmt.Printf("backend: %s\n\ncapture (instrument in — a guitar's interface, a sax's mic):\n", b.Name())
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
		return "", fmt.Errorf("no %s device matches %q (run 'musictutor devices')", kind, query)
	default:
		names := make([]string, len(matches))
		for i, d := range matches {
			names[i] = d.Name
		}
		return "", fmt.Errorf("%q matches %d %s devices (%s) — be more specific", query, len(matches), kind, strings.Join(names, "; "))
	}
}

func defaultDeviceID(devs []audio.DeviceInfo) string {
	for _, d := range devs {
		if d.Default {
			return d.ID
		}
	}
	return ""
}

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

func fillDeviceID(devs []audio.DeviceInfo, kind, query, remembered string) (id, note string, fallback bool, err error) {
	if query == "" && remembered != "" {
		for _, d := range devs {
			if d.ID == remembered {
				return remembered, "", false, nil
			}
		}
		id, err = resolveDevice(devs, kind, "")
		if err != nil {
			return "", "", false, err
		}
		return id, fmt.Sprintf("the saved %s device is not connected; using %s", kind, deviceLabel(devs, id)), true, nil
	}
	id, err = resolveDevice(devs, kind, query)
	return id, "", false, err
}

func fillDeviceIDs(capture, playback []audio.DeviceInfo, cfg appconfig.Config, inQ, outQ string) (inID, outID string, notes []string, inFell, outFell bool, err error) {
	var note string
	if inID, note, inFell, err = fillDeviceID(capture, "capture", inQ, cfg.CaptureDeviceID); err != nil {
		return "", "", nil, false, false, err
	}
	if note != "" {
		notes = append(notes, note)
	}
	if outID, note, outFell, err = fillDeviceID(playback, "playback", outQ, cfg.PlaybackDeviceID); err != nil {
		return "", "", nil, false, false, err
	}
	if note != "" {
		notes = append(notes, note)
	}
	return inID, outID, notes, inFell, outFell, nil
}

func resolveDevices(b audio.Backend, cfg appconfig.Config, inQ, outQ string) (inID, outID string, capture, playback []audio.DeviceInfo, notes []string, inFell, outFell bool, err error) {
	capture, playback, err = b.Devices()
	if err != nil {
		return "", "", nil, nil, nil, false, false, fmt.Errorf("enumerating devices: %w", err)
	}
	inID, outID, notes, inFell, outFell, err = fillDeviceIDs(capture, playback, cfg, inQ, outQ)
	if err != nil {
		return "", "", nil, nil, nil, false, false, err
	}
	return inID, outID, capture, playback, notes, inFell, outFell, nil
}

func calibratedOffset(cfg appconfig.Config, inID, outID string) (int, bool) {
	if inID == "" && outID == "" {
		return 0, false
	}
	return cfg.OffsetFor(inID, outID)
}

const (
	calClicks  = 8
	calSpacing = sampleRate
)

const advanceLagFrames = 4 * sampleRate

const maxRecentStrums = 32

const waitArmGraceFrames = 15 * sampleRate / 100

type listenUI interface {
	OfferResults([]practice.NoteResult)
	OfferTuner(pitch.Note, bool)
}

type liveWiring struct {
	eng    *engine.Engine
	app    listenUI
	scorer *practice.Scorer
	gate   *practice.WaitGate
	pcfg   practice.Config

	advanceLag int64

	armedGen    uint64
	armedMin    int64
	armedEvents []score.NoteEvent
	offerBuf    []pitch.Note
	confirmBuf  []pitch.Note

	strumBuf    []pitch.Strum
	results     []practice.NoteResult
	lastDiscont int64
}

func newLiveWiring(eng *engine.Engine, app listenUI, scorer *practice.Scorer, gate *practice.WaitGate, pcfg practice.Config) *liveWiring {
	return &liveWiring{
		eng:         eng,
		app:         app,
		scorer:      scorer,
		gate:        gate,
		pcfg:        pcfg,
		advanceLag:  advanceLagFrames,
		offerBuf:    make([]pitch.Note, 0, 16),
		confirmBuf:  make([]pitch.Note, 0, 16),
		strumBuf:    make([]pitch.Strum, 0, 8),
		lastDiscont: eng.DiscontinuityFrame(),
	}
}

func (w *liveWiring) onStrums(sts []pitch.Strum) {
	for _, st := range sts {

		w.scorer.DetectedStrum(st)
		if w.armedLive() && w.gate.OfferStrum(st) {
			w.confirmWait()
			continue
		}

		w.strumBuf = append(w.strumBuf, st)
	}
	if n := len(w.strumBuf); n > maxRecentStrums {
		w.strumBuf = append(w.strumBuf[:0], w.strumBuf[n-maxRecentStrums:]...)
	}
}

func (w *liveWiring) armedLive() bool {
	if len(w.armedEvents) == 0 {
		return false
	}
	_, gen, waiting := w.eng.WaitingOn()
	return waiting && gen == w.armedGen
}

func (w *liveWiring) onNotes(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {

	if d := w.eng.DiscontinuityFrame(); d != w.lastDiscont {
		w.lastDiscont = d
		w.scorer.AbandonBefore(d)
	}
	w.scorer.Detected(closed)
	w.scorer.Advance(consumed - w.advanceLag)
	w.results = w.scorer.Results(w.results[:0])
	if len(w.results) > 0 {
		w.app.OfferResults(w.results)
	}
	w.app.OfferTuner(current, sounding)

	floor := consumed - waitArmGraceFrames
	keep := w.strumBuf[:0]
	for _, st := range w.strumBuf {
		if st.Frame >= floor {
			keep = append(keep, st)
		}
	}
	w.strumBuf = keep

	evs, gen, waiting := w.eng.WaitingOn()
	if !waiting {
		return
	}
	if gen != w.armedGen {
		w.armedGen = gen
		w.armedMin = consumed - waitArmGraceFrames
		w.armedEvents = append(w.armedEvents[:0], evs...)
		w.confirmBuf = w.confirmBuf[:0]
		w.gate.Arm(evs, w.armedMin)

		for _, st := range w.strumBuf {
			if st.Frame >= w.armedMin && w.gate.OfferStrum(st) {
				w.confirmWait()
				return
			}
		}
	}
	w.offerBuf = append(w.offerBuf[:0], closed...)
	if sounding {
		w.offerBuf = append(w.offerBuf, current)
	}

	for _, n := range w.offerBuf {
		if n.Start >= w.armedMin || w.slideEvidence(n) {
			w.confirmBuf = mergeNote(w.confirmBuf, n)
		}
	}
	if len(w.offerBuf) > 0 && w.gate.Offer(w.offerBuf) {
		w.confirmWait()
	}
}

func (w *liveWiring) slideEvidence(n pitch.Note) bool {
	for i := range w.armedEvents {
		if practice.ConfirmsSlide(n, w.armedEvents[i], w.pcfg) {
			return true
		}
	}
	return false
}

func (w *liveWiring) confirmWait() {
	w.scorer.WaitConfirmed(w.armedEvents, w.confirmBuf)
	w.eng.ConfirmWait()

	w.strumBuf = w.strumBuf[:0]
}

func mergeNote(buf []pitch.Note, n pitch.Note) []pitch.Note {
	for i := range buf {
		if buf[i].Start == n.Start && buf[i].Key == n.Key {
			buf[i] = n
			return buf
		}
	}
	return append(buf, n)
}

type liveConditions struct {
	notes []string

	uncalibrated bool
}

func setupListen(eng *engine.Engine, app *ui.App, sc *score.Score, inQ, outQ string, cfg appconfig.Config) (session *live.Session, cond liveConditions, err error) {
	b, err := liveBackend()
	if err != nil {
		return nil, cond, err
	}
	inID, outID, _, _, notes, _, _, err := resolveDevices(b, cfg, inQ, outQ)
	if err != nil {
		return nil, cond, err
	}
	cond.notes = notes
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "warning:", n)
	}
	offset, calibrated := calibratedOffset(cfg, inID, outID)
	cond.uncalibrated = !calibrated
	if !calibrated {
		fmt.Fprintln(os.Stderr, "warning: no latency calibration for these devices — run 'musictutor calibrate'.")
		fmt.Fprintln(os.Stderr, "Scoring works, but timing verdicts are skewed by the unmeasured round trip.")
	}

	tr := sc.Tracks[app.Track()]
	pcfg := practice.Config{
		SampleRate:          sampleRate,
		Track:               app.Track(),
		LatencyOffsetFrames: offset,
	}
	scorer := practice.NewScorer(pcfg)
	gate := practice.NewWaitGate(pcfg)
	wiring := newLiveWiring(eng, app, scorer, gate, pcfg)
	wiring.advanceLag = advanceLagFor(sc, app.Track())

	setEventTap(eng, scorer.ExpectNote)
	wired := false
	defer func() {
		if !wired {
			setEventTap(eng, nil)
		}
	}()

	lcfg := live.Config{
		Backend: b,
		Engine:  eng,
		Stream: audio.StreamConfig{
			SampleRate:     sampleRate,
			CaptureDevice:  inID,
			PlaybackDevice: outID,
		},

		Pitch:   pitchConfigFor(tr),
		OnNotes: wiring.onNotes,
	}
	if tr.Wind == nil {

		lcfg.OnStrums = wiring.onStrums
	}
	session, err = live.Start(lcfg)
	if err != nil {
		return nil, cond, err
	}
	app.SetLiveStatus(func() (float64, int64) {
		return session.InputLevel(), session.DroppedSamples()
	})
	app.SetWaitControl(true)
	fmt.Printf("listening on %s (offset %d frames, calibrated: %v)\n", b.Name(), offset, calibrated)
	wired = true
	return session, cond, nil
}

func advanceLagFor(sc *score.Score, track int) int64 {
	if sc.Tracks[track].Wind == nil {
		return advanceLagFrames
	}
	var longest float64
	for _, ev := range sc.Events() {
		if ev.Track != track {
			continue
		}
		if d := sc.Tempos.TimeAt(ev.End) - sc.Tempos.TimeAt(ev.Start); d > longest {
			longest = d
		}
	}
	lag := int64((longest/minScale + 1) * sampleRate)
	if lag < advanceLagFrames {
		lag = advanceLagFrames
	}
	return lag
}

func pitchConfigFor(tr *score.Track) pitch.Config {
	if w := tr.Wind; w != nil {
		return pitch.ConfigForKeys(0, w.LowSounding, w.LowSounding+w.Span)
	}
	return pitch.Config{}
}

var errCalibrationCanceled = errors.New("calibration canceled")

func calibrationPass(ctx context.Context, b audio.Backend, inID, outID string, progress func(float64)) (int, float64, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return 0, 0, fmt.Errorf("%w: %w", errCalibrationCanceled, err)
	}
	train := latency.ClickTrain(sampleRate, calClicks, calSpacing)

	capLen := len(train) + sampleRate
	captured := make([]float32, 0, capLen)
	pos := 0
	done := make(chan struct{})
	var once sync.Once

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
		return 0, 0, fmt.Errorf("calibration timed out — no audio flowed (check the devices with 'musictutor devices')")
	}
	stream.Stop()
	stream.Close()

	return latency.Estimate(sampleRate, train, captured, calSpacing, calClicks)
}

func rememberDevices(cfg *appconfig.Config, inID, outID string, inFell, outFell bool) {
	if !inFell {
		cfg.CaptureDeviceID = inID
	}
	if !outFell {
		cfg.PlaybackDeviceID = outID
	}
}

func runCalibrate(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	inQ := fs.String("in", "", inFlagHelp)
	outQ := fs.String("out", "", outFlagHelp)
	setUsage(fs, "musictutor calibrate [-in device] [-out device]",
		"calibrate measures the round-trip latency offset used to align scoring;",
		"the output must be audible to the input (mic near the speakers, or a loopback).")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: musictutor calibrate [-in device] [-out device]")
	}
	b, err := liveBackend()
	if err != nil {
		return err
	}
	cfg, cfgErr := appconfig.Load()
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "warning: existing config unreadable, starting fresh:", cfgErr)
	}

	inID, outID, capture, playback, notes, inFell, outFell, err := resolveDevices(b, cfg, *inQ, *outQ)
	if err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "warning:", n)
	}

	if inID == "" && outID == "" {
		return fmt.Errorf("this backend marks no default devices, so the offset would be stored under no device at all; pick them with -in and -out (run 'musictutor devices')")
	}
	fmt.Printf("measuring playback [%s] -> capture [%s]\n",
		deviceLabel(playback, outID), deviceLabel(capture, inID))

	fmt.Println("playing calibration clicks — the input must be able to hear the")
	fmt.Println("output (mic near the speakers, or a loopback cable)...")

	off, conf, err := calibrationPass(context.Background(), b, inID, outID, nil)
	if err != nil {
		return err
	}
	fmt.Printf("round-trip latency: %d frames (%.1f ms), confidence %.2f\n",
		off, float64(off)/sampleRate*1000, conf)

	cfg.SetOffset(inID, outID, off, conf)
	rememberDevices(&cfg, inID, outID, inFell, outFell)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Println("saved — live scoring will use this offset for these devices.")
	if inFell || outFell {
		fmt.Println("your saved device preference was kept; reconnect it and calibrate again.")
	}
	return nil
}
