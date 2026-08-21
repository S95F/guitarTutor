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
		return "", fmt.Errorf("no %s device matches %q (run 'musictutor devices')", kind, query)
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
//
// A remembered device that is no longer enumerated — unplugged since it
// was saved — falls back to the default instead of being handed to the
// backend verbatim, and the fallback is reported in note so no caller
// stays silent about it. The settings screen already fell back this way
// for display while this path passed the stale ID straight to the device
// open: the two disagreed about which device would be used, and the one
// that was wrong was the one that opened the stream (audit C3).
//
// fallback distinguishes that stand-in from a device the user actually
// chose, because a caller that PERSISTS the result must not write it: the
// saved preference is the whole record of which interface the player
// owns, and replacing it with whatever was plugged in the day their
// interface was not would lose it silently (bug review C5).
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

// fillDeviceIDs applies fillDeviceID to both endpoints. Both the listen and
// calibrate paths resolve through here, so an offset is stored and looked
// up under the same key. Pure — the seam the tests drive with fake device
// lists and configs. notes carries any silent-fallback explanations for
// the caller to surface, and inFell/outFell mark the endpoints that are
// standing in for a device that is not connected (see fillDeviceID).
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

// resolveDevices enumerates the backend's devices and resolves the -in/-out
// flag values with the config's remembered devices filling the gaps (flags
// win). The device lists are returned for labeling, notes carries any
// fallback the caller must tell the user about, and inFell/outFell mark
// the endpoints a caller must not persist as a preference.
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
// any note a plucked string holds — the string decays and the tracker
// closes the note whatever the player does; the cost is that a miss shows
// up on the tab ~4 s after the fact. A wind player holds a note for as
// long as the score says, so the wind lag is computed per piece instead
// (advanceLagFor).
const advanceLagFrames = 4 * sampleRate

// maxRecentStrums bounds the buffer of attacks held for a gate that has
// not armed yet. Strums are pruned by age every batch, so this is only a
// backstop against a pathological burst of onsets.
const maxRecentStrums = 32

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

// liveWiring is the analysis state of one live session: detections and
// strums feed the scorer and tuner, and while the engine waits the gate
// decides when to release.
//
// The two callbacks share one struct because they answer the same wait
// point by different evidence. A pitched wait point is released by a
// detection reaching its key; a wait point on a DEAD (muted) note can
// only be released by a strum, because a damped string produces no
// trackable f0 — leaving strums out of the gate halted such a piece
// forever (bug review P3). Both run on the single analysis goroutine
// (live.Config: OnStrums then OnNotes within a batch), so the state below
// needs no lock, only a common owner.
//
// The wait is tracked by the generation WaitingOn returns with its
// events: it increments per engaged wait, so unlike the wait tick it
// distinguishes a seek's re-wait or a loop wrap at the same tick, with no
// confirm-time reset to get wrong. Both come from the one call because
// the render thread can end one wait and begin the next between two
// calls, and arming the previous wait's events under the new generation
// deadlocks the gate: it can never be satisfied and never re-arms (bug
// review W1).
//
// The gate arms with minStart = consumed - waitArmGraceFrames so only a
// fresh attack confirms. On satisfaction the confirming detections are
// recorded with Scorer.WaitConfirmed BEFORE ConfirmWait, so the released
// events get a pitch-only verdict instead of being timing-judged against
// the machinery's own latency.
type liveWiring struct {
	eng    *engine.Engine
	app    listenUI
	scorer *practice.Scorer
	gate   *practice.WaitGate
	pcfg   practice.Config
	// advanceLag is how far miss finalization trails the capture clock:
	// advanceLagFrames for fretted tracks, advanceLagFor's per-piece
	// figure for winds.
	advanceLag int64

	armedGen    uint64
	armedMin    int64
	armedEvents []score.NoteEvent
	offerBuf    []pitch.Note
	confirmBuf  []pitch.Note
	// strumBuf holds this batch's strums. OnStrums runs first, so a
	// strum in the very batch that arms the gate would otherwise be
	// offered to an unarmed gate and never seen again — and a player
	// who anticipates the wait point by a hair would have to strike a
	// muted note twice. They are re-offered once, immediately after
	// arming.
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

// onStrums feeds chord verification and palm-mute credit, and offers each
// fresh strum to the gate so a dead-note wait point can be released.
func (w *liveWiring) onStrums(sts []pitch.Strum) {
	for _, st := range sts {
		// Chord verification wants every strum, wait or no wait.
		w.scorer.DetectedStrum(st)
		if w.armedLive() && w.gate.OfferStrum(st) {
			w.confirmWait()
			continue
		}
		// Not usable yet — remember it in case the gate arms shortly.
		// The slice the detector hands us is only valid for this call.
		w.strumBuf = append(w.strumBuf, st)
	}
	if n := len(w.strumBuf); n > maxRecentStrums {
		w.strumBuf = append(w.strumBuf[:0], w.strumBuf[n-maxRecentStrums:]...)
	}
}

// armedLive reports whether the gate is armed for the wait the engine is
// halted on right now.
//
// The gate stays satisfied from the moment it is filled until something
// re-arms it, and only onNotes arms. Without this check every strum after
// a confirmation found a satisfied gate and released whatever wait came
// next — the player would strum once and the piece would run on through
// wait points they never answered, which is worse than the halt this
// callback was added to fix. onNotes needs no equivalent guard: it
// returns early when the engine is not waiting, and ConfirmWait clears
// that flag under the engine's own lock.
//
// One WaitingOn call rather than Waiting() plus WaitGeneration(), for the
// same reason the arming path uses one: read separately they can describe
// different waits (W1).
func (w *liveWiring) armedLive() bool {
	if len(w.armedEvents) == 0 {
		return false
	}
	_, gen, waiting := w.eng.WaitingOn()
	return waiting && gen == w.armedGen
}

// onNotes is the note half of the analysis callback.
func (w *liveWiring) onNotes(closed []pitch.Note, current pitch.Note, sounding bool, consumed int64) {
	// Before Advance, so a seek or loop edit abandons what it
	// truncated instead of letting the deadline score it a Miss.
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

	// Forget strums too old to confirm anything. The arming floor only
	// ever moves forward, so a strum below this batch's floor is already
	// past every future arm — which bounds the buffer to the handful of
	// attacks inside one grace window.
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
		// Re-offer the strums that arrived before the arm (see strumBuf).
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
	// Collect every fresh attack heard during this wait: a chord
	// confirms across batches, and WaitConfirmed wants all of the
	// confirming detections, not just the batch that completed
	// the set.
	for _, n := range w.offerBuf {
		if n.Start >= w.armedMin || w.slideEvidence(n) {
			w.confirmBuf = mergeNote(w.confirmBuf, n)
		}
	}
	if len(w.offerBuf) > 0 && w.gate.Offer(w.offerBuf) {
		w.confirmWait()
	}
}

// slideEvidence reports whether a detection older than the arming floor
// is nevertheless what proves an armed slide destination was played. A
// legato slide sounds on a string struck a beat earlier, so it never
// passes the freshness test every other note has to.
func (w *liveWiring) slideEvidence(n pitch.Note) bool {
	for i := range w.armedEvents {
		if practice.ConfirmsSlide(n, w.armedEvents[i], w.pcfg) {
			return true
		}
	}
	return false
}

// confirmWait releases the engine's wait, recording the confirming
// detections first so the released events get a pitch-only verdict.
func (w *liveWiring) confirmWait() {
	w.scorer.WaitConfirmed(w.armedEvents, w.confirmBuf)
	w.eng.ConfirmWait()
	// Everything buffered has now been used or superseded. Dropping it
	// stops one attack from also releasing the NEXT wait point, the way
	// the gate's own attack identity stops a ringing note doing it.
	w.strumBuf = w.strumBuf[:0]
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

// liveConditions is what a successfully opened live session is operating
// under that the player deserves to know about: which endpoints are
// stand-ins for devices that were not there, and whether the round trip
// the timing verdicts depend on has ever been measured. setupListen
// prints these to stderr for the terminal-launched path, but stderr is
// invisible to a double-clicked windowed binary — so they are also
// RETURNED, and each caller folds them into the practice view's warning
// banner with wording that names the remedy that caller can offer.
type liveConditions struct {
	// notes are the device-fallback explanations from resolveDevices: the
	// saved device was not connected and a stand-in opened instead.
	notes []string
	// uncalibrated is true when no latency offset is stored for the pair
	// of devices the stream actually opened on.
	uncalibrated bool
}

// setupListen wires the live practice loop: duplex stream -> engine
// playback + pitch analysis -> scorer and wait gate -> UI feeds.
//
// cfg is the caller's authoritative configuration, passed in rather than
// loaded from disk here. The shell keeps its config in memory and writes
// it through on save; when a save failed, loading from disk in this
// function silently opened the stream on whatever the file still said —
// the wrong device, with offset 0 — while every other part of the app
// (the settings display, the split-device warning, the decision to go
// live at all) read the in-memory state (audit C1). The CLI path loads
// the file itself and passes it in, so both callers choose their source
// explicitly.
//
// The conditions the session opened under come back alongside it rather
// than being shown here: this function cannot know whether "press S for
// settings" or "run 'musictutor calibrate'" is the remedy that is true
// for its caller, and a banner set here was overwritten by whichever
// banner the caller set next (App.SetLiveWarning holds one message).
//
// Every failure path leaves the engine exactly as it was found: in
// particular the event tap is rolled back, so a caller that falls back to
// plain playback is not left with an engine feeding expectations into a
// scorer nobody drains (see the rollback below).
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

	// The scored track comes from the view rather than a parameter of its
	// own: it must be the track being drawn and waited on, and a second
	// way to say so is a second way to say something different (W3). The
	// score is only here so the wiring can read that one track's
	// instrument.
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

	lcfg := live.Config{
		Backend: b,
		Engine:  eng,
		Stream: audio.StreamConfig{
			SampleRate:     sampleRate,
			CaptureDevice:  inID,
			PlaybackDevice: outID,
		},
		// The detector's search range follows the scored instrument; the
		// zero config is the guitar-tuned default.
		Pitch:   pitchConfigFor(tr),
		OnNotes: wiring.onNotes,
	}
	if tr.Wind == nil {
		// Chord verification and palm-mute credit (Phase 4) both hang
		// off strums; supplying this callback is what enables them.
		// Without it the session silently scores like Phase 3: one hit
		// and N-1 misses per strummed chord — and a wait point on a
		// dead note never releases at all. A wind track gets neither
		// callback on purpose: it cannot hold a chord or a dead note, so
		// the only thing strums could do there is let breath noise claim
		// a palm-mute's deadline credit.
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

// advanceLagFor is the miss-finalization lag for a piece and its scored
// track. The fretted default stands on string physics: a pluck decays, so
// the tracker closes every note within a few seconds whatever the player
// does. A wind player holds a note exactly as long as the (possibly
// slowed) score says, and a note still open when its deadline passes is
// falsely scored a miss — the #1 rage-quit failure (D5) — so the wind lag
// covers the scored track's longest note at the slowest speed the
// transport reaches, plus a second for the tracker to settle. The cost is
// honest and accepted: on a wind track a genuine miss surfaces later.
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

// pitchConfigFor fits the detector to the scored track's instrument: the
// zero config takes the guitar-tuned defaults at the stream's negotiated
// rate, and a wind track gets its search range fitted to the horn's
// sounding compass (see pitch.ConfigForKeys — the guitar ceiling sits
// only three semitones over a soprano sax's top note).
func pitchConfigFor(tr *score.Track) pitch.Config {
	if w := tr.Wind; w != nil {
		return pitch.ConfigForKeys(0, w.LowSounding, w.LowSounding+w.Span)
	}
	return pitch.Config{}
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
		return 0, 0, fmt.Errorf("calibration timed out — no audio flowed (check the devices with 'musictutor devices')")
	}
	stream.Stop()
	stream.Close()

	// The estimate's error passes through unchanged. The latency package
	// already writes its diagnostics for a person — what went wrong and
	// what to try — and the settings screen renders that message directly,
	// trimming the package's own prefix; a second "could not measure"
	// wrapper stacked on top read as part of the message there. A caller
	// with a print site of its own can add its framing where it prints.
	return latency.Estimate(sampleRate, train, captured, calSpacing, calClicks)
}

// rememberDevices adopts a calibrated pair as the saved device
// preference, skipping either endpoint that is only standing in for a
// device that is not connected (see fillDeviceID).
//
// The measured OFFSET is always stored, under whichever pair was really
// used — that measurement is true of those devices. The preference is
// different: it is the whole record of which interface the player owns.
// Calibrating with the interface unplugged measured the laptop speakers
// and then adopted them, so reconnecting the interface no longer selected
// it and the offset already measured for it was never looked up again.
// The player's only clue was that timing verdicts had quietly gone wrong
// (bug review C5).
func rememberDevices(cfg *appconfig.Config, inID, outID string, inFell, outFell bool) {
	if !inFell {
		cfg.CaptureDeviceID = inID
	}
	if !outFell {
		cfg.PlaybackDeviceID = outID
	}
}

// runCalibrate measures the round-trip latency offset and stores it.
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
	// Same resolution as -listen (flags win, remembered devices fill the
	// gaps), so the offset is stored under the key playback will look up.
	inID, outID, capture, playback, notes, inFell, outFell, err := resolveDevices(b, cfg, *inQ, *outQ)
	if err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, "warning:", n)
	}
	// With no flags, no remembered devices and a backend that marks no
	// system default, both IDs resolve empty — the very ""|"" key
	// calibratedOffset refuses as ambiguous. Measuring would work and the
	// save would claim "live scoring will use this offset" about an offset
	// no lookup will ever return, so refuse up front with the fix.
	if inID == "" && outID == "" {
		return fmt.Errorf("this backend marks no default devices, so the offset would be stored under no device at all; pick them with -in and -out (run 'musictutor devices')")
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
