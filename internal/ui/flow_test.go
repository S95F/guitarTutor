package ui

// Cross-screen tests: the getting-started strip, the settings screen's
// mouse, and re-opening a piece in place. These are the paths that used
// to dead-end — the ones where the only way forward was to quit and
// retype a command line.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---- the getting-started strip -------------------------------------------

// TestHintShowsOnAFirstRun.
func TestHintShowsOnAFirstRun(t *testing.T) {
	sh := NewShell(Services{Prefs: &settingsFakePrefs{}, Audio: newSettingsAudio()}, nil)
	b := NewBrowser(sh)
	if !b.hintOpen {
		t.Fatal("no pieces should mean the getting-started strip is showing")
	}
	steps := b.stepList()
	if len(steps) != 3 {
		t.Fatalf("the strip has %d steps, want 3 (interface, calibration, open a piece)", len(steps))
	}
	for i, s := range steps {
		if s.title == "" || s.detail == "" {
			t.Errorf("step %d has no title or detail: %+v", i, s)
		}
	}
}

// TestChecklistTicksOffAsTheConfigurationFills is the point of the
// checklist over a static leaflet: it reports what is actually done.
func TestChecklistTicksOffAsTheConfigurationFills(t *testing.T) {
	pr := &settingsFakePrefs{}
	audio := newSettingsAudio()
	sh := NewShell(Services{Prefs: pr, Audio: audio}, nil)
	b := NewBrowser(sh)

	if b.stepList()[0].done {
		t.Error("with no capture device chosen, step 1 should be outstanding")
	}

	pr.SetDevices("cap-focus", "play-focus")
	steps := b.stepList()
	if !steps[0].done {
		t.Error("choosing a capture device should tick step 1 off")
	}
	if steps[1].done {
		t.Error("an unmeasured pair should leave the calibration step outstanding")
	}

	audio.offsets = map[string]settingsOffset{"cap-focus|play-focus": {frames: 480, ok: true}}
	if !b.stepList()[1].done {
		t.Error("a stored calibration should tick step 2 off")
	}
}

// TestChecklistWithoutAnAudioBackendStatesTheFactInstead: a step nobody
// can take is not a step. It becomes a statement, with no action.
func TestChecklistWithoutAnAudioBackendStatesTheFactInstead(t *testing.T) {
	sh := NewShell(Services{Prefs: &settingsFakePrefs{}}, nil)
	b := NewBrowser(sh)
	steps := b.stepList()
	if len(steps) != 2 {
		t.Fatalf("with no backend the checklist has %d steps, want 2", len(steps))
	}
	if steps[0].act != nil {
		t.Error("the playback-only notice should have nothing to activate")
	}
	if !strings.Contains(strings.ToLower(steps[0].title), "playback") {
		t.Errorf("first step should explain the machine is playback only, got %q", steps[0].title)
	}
}

// TestChecklistStepsAreActivatable: Enter on a configuration step opens
// settings, and Enter on the last one launches the OS file dialog.
func TestChecklistStepsAreActivatable(t *testing.T) {
	sh := NewShell(Services{Prefs: &settingsFakePrefs{}, Audio: newSettingsAudio()}, nil)
	b := NewBrowser(sh)
	opened := 0
	b.SetSettingsOpener(func() { opened++ })
	launches := 0
	b.SetOpenDialog(func(string) { launches++ })

	// The steps are their own controls now — they are clicked, not
	// selected — so they are activated by index.
	b.activateStep(0)
	if opened != 1 {
		t.Errorf("step 1 opened settings %d times, want 1", opened)
	}

	b.activateStep(len(b.stepList()) - 1)
	if launches != 1 {
		t.Errorf(`the "open a piece" step launched the file dialog %d times, want 1`, launches)
	}

	// An index off either end is a no-op rather than a panic.
	b.activateStep(-1)
	b.activateStep(99)
}

// TestHintTicksItselfOff: the strip stays, because it can be dismissed
// on purpose — but its last step reports done once there is a piece to
// hand, so it stops asking for something that has happened.
func TestHintTicksItselfOff(t *testing.T) {
	pr := &browserFakePrefs{recents: []string{filepath.Join(t.TempDir(), "song.gp")}}
	sh := NewShell(Services{Opener: &browserFakeOpener{}, Prefs: pr}, nil)
	b := NewBrowser(sh)
	steps := b.stepList()
	if len(steps) == 0 {
		t.Fatal("the strip has no steps")
	}
	if last := steps[len(steps)-1]; !last.done {
		t.Errorf("with a piece to hand the %q step is still outstanding", last.title)
	}
}

// TestHintCanBePutAwayForGood: dismissing it writes to preferences, so a
// new screen over the same preferences comes up without it. A dismissal
// that has to be repeated every launch is not a dismissal.
func TestHintCanBePutAwayForGood(t *testing.T) {
	pr := &settingsFakePrefs{}
	sh := NewShell(Services{Prefs: pr, Audio: newSettingsAudio()}, nil)
	b := NewBrowser(sh)
	if !b.hintOpen {
		t.Fatal("the strip should start showing")
	}
	b.toggleHint()
	if b.hintOpen {
		t.Error("toggling did not hide the strip")
	}
	if !pr.hideHint {
		t.Error("hiding the strip was not persisted")
	}
	if got := NewBrowser(sh); got.hintOpen {
		t.Error("a fresh screen brought the dismissed strip back")
	}
	// And it can be brought back.
	b.toggleHint()
	if !b.hintOpen || pr.hideHint {
		t.Error("toggling again did not restore the strip")
	}
}

// ---- settings: cursor placement and mouse --------------------------------

// TestSettingsOpensOnTheFirstUnconfiguredRow: the checklist says "choose
// your audio interface" and the screen it opens should already be there.
func TestSettingsOpensOnTheFirstUnconfiguredRow(t *testing.T) {
	audio := newSettingsAudio()
	s, pr := newSettingsFixture(t, audio)
	if got, want := s.rows[s.cur], srCapture; got != want {
		t.Errorf("with no device chosen the cursor sits on row %v, want %v", got, want)
	}

	// With a device chosen but never measured, calibration is next.
	pr.SetDevices("cap-focus", "play-focus")
	s = NewSettings(s.sh)
	if got, want := s.rows[s.cur], srCalibrate; got != want {
		t.Errorf("with an unmeasured pair the cursor sits on row %v, want %v", got, want)
	}

	// Fully configured: leave the cursor at the top.
	audio.offsets = map[string]settingsOffset{"cap-focus|play-focus": {frames: 480, ok: true}}
	s = NewSettings(s.sh)
	if s.cur != 0 {
		t.Errorf("with everything configured the cursor should stay at the top, got row %d", s.cur)
	}
}

// settingsRowItem finds the display-list entry for a row kind.
func settingsRowItem(t *testing.T, s *Settings, kind settingsRow) settingsItem {
	t.Helper()
	want := s.rowIndex(kind)
	if want < 0 {
		t.Fatalf("row %v is not present on this screen", kind)
	}
	for _, it := range s.items() {
		if it.kind == siRow && it.row == want {
			return it
		}
	}
	t.Fatalf("row %v has no display-list entry", kind)
	return settingsItem{}
}

// TestSettingsClickSelectsTheWholeRow: a setting is a line, and the line
// is the target — not just its label.
func TestSettingsClickSelectsTheWholeRow(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	it := settingsRowItem(t, s, srCountIn)
	band := it.band()
	s.handleMouse(pointer{x: band.x + band.w - 200, y: band.y + band.h/2, down: true, pressed: true})
	if got := s.rows[s.cur]; got != srCountIn {
		t.Errorf("clicking the count-in row selected %v", got)
	}
}

// TestSettingsButtonsAdjustTheirOwnRow, wherever the cursor happened to
// be: pressing a row's button should not need the row selected first.
func TestSettingsButtonsAdjustTheirOwnRow(t *testing.T) {
	s, pr := newSettingsFixture(t, newSettingsAudio())
	s.cur = 0

	it := settingsRowItem(t, s, srCountIn)
	if len(it.buttons) != 2 {
		t.Fatalf("the count-in row has %d buttons, want 2", len(it.buttons))
	}
	plus := it.buttons[1]
	s.handleMouse(pointer{x: plus.r.x + plus.r.w/2, y: plus.r.y + plus.r.h/2, down: true, pressed: true})
	if s.countIn != 1 {
		t.Errorf("the + button set the count-in to %d, want 1", s.countIn)
	}
	if pr.countIn != 1 {
		t.Errorf("the change did not reach preferences (%d)", pr.countIn)
	}
	if got := s.rows[s.cur]; got != srCountIn {
		t.Errorf("pressing a row's button left the cursor on %v", got)
	}
}

// TestSettingsDeviceButtonsCycle covers the two pickers.
func TestSettingsDeviceButtonsCycle(t *testing.T) {
	s, pr := newSettingsFixture(t, newSettingsAudio())
	it := settingsRowItem(t, s, srCapture)
	before := s.capIdx
	next := it.buttons[1]
	s.handleMouse(pointer{x: next.r.x + 2, y: next.r.y + 2, down: true, pressed: true})
	if s.capIdx == before {
		t.Error("the capture > button did not move the selection")
	}
	if capID, _ := pr.Devices(); capID != s.capture[s.capIdx].ID {
		t.Errorf("preferences hold %q, screen shows %q", capID, s.capture[s.capIdx].ID)
	}
}

// TestSettingsSoundFontRowOffersBrowseOnlyWithAPicker: the row used to
// be able to clear a SoundFont and never choose one.
func TestSettingsSoundFontRowOffersBrowseOnlyWithAPicker(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	if got := len(settingsRowItem(t, s, srSoundFont).buttons); got != 1 {
		t.Errorf("with no picker wired the soundfont row has %d buttons, want just clear", got)
	}

	asked := 0
	s.SetFilePicker(func([]string, func(string)) { asked++ })
	it := settingsRowItem(t, s, srSoundFont)
	if len(it.buttons) != 2 {
		t.Fatalf("with a picker wired the row has %d buttons, want browse and clear", len(it.buttons))
	}
	browse := it.buttons[0]
	s.handleMouse(pointer{x: browse.r.x + 2, y: browse.r.y + 2, down: true, pressed: true})
	if asked != 1 {
		t.Errorf("the browse button asked for a file %d times, want 1", asked)
	}
}

// TestSettingsOnCloseRunsOnce: it is how the practice view learns that
// what it was built from has changed.
func TestSettingsOnCloseRunsOnce(t *testing.T) {
	s, _ := newSettingsFixture(t, nil)
	closes := 0
	s.SetOnClose(func() { closes++ })
	s.Close()
	s.Close()
	if closes != 1 {
		t.Errorf("the close hook ran %d times, want exactly 1", closes)
	}
}

// ---- reopening a piece ---------------------------------------------------

// flowOpener hands back a distinct screen from every Open, so a test can
// tell a replaced screen from the one it replaced. browserFakeOpener
// returns an empty struct value, and two of those compare equal.
type flowOpener struct {
	opened []string
	closed int
	err    error
}

func (o *flowOpener) Open(path string) (Screen, []string, error) {
	o.opened = append(o.opened, path)
	if o.err != nil {
		return nil, nil, o.err
	}
	return &shellPlainScreen{}, nil, nil
}

func (o *flowOpener) CloseCurrent() { o.closed++ }

// TestReopenPieceReplacesInPlace: reloading a piece must swap the
// practice screen, not stack a second one on top of it — otherwise
// leaving the reloaded piece would land on the stale one underneath.
func TestReopenPieceReplacesInPlace(t *testing.T) {
	op := &flowOpener{}
	sh := NewShell(Services{Opener: op, Prefs: &browserFakePrefs{}}, &shellPlainScreen{})
	if _, err := sh.OpenPiece("song.gp"); err != nil {
		t.Fatalf("OpenPiece: %v", err)
	}
	_ = sh.Update() // apply the queued push
	if sh.Depth() != 2 {
		t.Fatalf("depth %d after opening a piece, want 2", sh.Depth())
	}
	first := sh.stack[1]

	if _, err := sh.ReopenPiece("song.gp"); err != nil {
		t.Fatalf("ReopenPiece: %v", err)
	}
	_ = sh.Update()
	if sh.Depth() != 2 {
		t.Errorf("depth %d after reopening, want it unchanged at 2", sh.Depth())
	}
	if sh.stack[1] == first {
		t.Error("reopening did not put a freshly built screen on the stack")
	}
	if len(op.opened) != 2 {
		t.Errorf("the opener was asked %d times, want 2", len(op.opened))
	}
}

// TestReopenPieceLeavesTheStackAloneOnFailure: a load that fails must
// not take the screen the user is looking at away with it.
func TestReopenPieceLeavesTheStackAloneOnFailure(t *testing.T) {
	op := &flowOpener{}
	sh := NewShell(Services{Opener: op, Prefs: &browserFakePrefs{}}, &shellPlainScreen{})
	if _, err := sh.OpenPiece("song.gp"); err != nil {
		t.Fatalf("OpenPiece: %v", err)
	}
	_ = sh.Update()
	current := sh.stack[len(sh.stack)-1]

	op.err = os.ErrNotExist
	if _, err := sh.ReopenPiece("song.gp"); err == nil {
		t.Fatal("ReopenPiece should report the load failure")
	}
	_ = sh.Update()
	if sh.Depth() != 2 || sh.stack[len(sh.stack)-1] != current {
		t.Error("a failed reload changed the screen stack")
	}
}

// ---- help on every screen ------------------------------------------------

// TestEveryScreenHasACompleteControlTable: pressing ? used to do nothing
// on two screens out of three. Every table now has to be complete enough
// to render, and to produce a footer hint that fits the window.
func TestEveryScreenHasACompleteControlTable(t *testing.T) {
	app := newApp(t, 4)
	brw := NewBrowser(NewShell(Services{Prefs: &settingsFakePrefs{}}, nil))
	set, _ := newSettingsFixture(t, newSettingsAudio())

	for _, c := range []struct {
		name string
		rows []helpBinding
		hint string
	}{
		{"practice", app.helpRows(), app.hintLine()},
		{"start screen", brw.browserBindings(), brw.hintLine()},
		{"settings", set.settingsBindings(), set.hintLine()},
	} {
		if len(c.rows) == 0 {
			t.Errorf("%s has no control table", c.name)
			continue
		}
		for i, b := range c.rows {
			if b.Group == "" || b.Keys == "" || b.Desc == "" {
				t.Errorf("%s row %d is incomplete: %+v", c.name, i, b)
			}
		}
		if c.hint == "" {
			t.Errorf("%s has an empty footer hint", c.name)
		}
		if w := uiPadX + textW(c.hint); w > screenW {
			t.Errorf("%s footer hint is %.0f px wide, past the %d px window", c.name, w, screenW)
		}
		if len(helpSections(c.rows)) == 0 {
			t.Errorf("%s help overlay has no sections", c.name)
		}
	}
}

// TestHelpSectionsKeepTableOrderAndGroupContiguously.
func TestHelpSectionsKeepTableOrderAndGroupContiguously(t *testing.T) {
	rows := []helpBinding{
		{Group: "a", Keys: "1"}, {Group: "a", Keys: "2"},
		{Group: "b", Keys: "3"}, {Group: "a", Keys: "4"},
	}
	got := helpSections(rows)
	if len(got) != 3 {
		t.Fatalf("got %d sections, want 3 — a group that reappears starts a new one", len(got))
	}
	if got[0].Name != "a" || len(got[0].Rows) != 2 || got[2].Name != "a" {
		t.Errorf("sections came out as %+v", got)
	}
}

// TestHintLineDropsUnavailableBindings: the footer has no room to
// explain itself, so a key that does nothing right now is left out —
// while the overlay still lists it, greyed.
func TestHintLineDropsUnavailableBindings(t *testing.T) {
	rows := []helpBinding{
		{Keys: "A", Hint: "A here"},
		{Keys: "B", Hint: "B gone", Off: true},
		{Keys: "C", Desc: "no hint at all"},
	}
	if got, want := hintLineOf(rows), "A here"; got != want {
		t.Errorf("hintLineOf = %q, want %q", got, want)
	}
}

// TestHintIsClickable (audit A1's successor): the strip was mouse-dead
// for exactly the users it exists for once before. Its steps are laid out
// by the same layout() the mouse reads, so a click on one runs it.
func TestHintIsClickable(t *testing.T) {
	sh := NewShell(Services{Prefs: &settingsFakePrefs{}, Audio: newSettingsAudio()}, nil)
	b := NewBrowser(sh)
	opened := 0
	b.SetSettingsOpener(func() { opened++ })

	l := b.layout()
	if len(l.steps) < 2 {
		t.Fatalf("the strip laid out %d steps, want at least 2", len(l.steps))
	}
	// The second step is the calibration one, and it opens settings.
	r := l.steps[1]
	b.handleMouse(pointer{x: r.x + 4, y: r.y + 4, pressed: true, down: true})
	if opened != 1 {
		t.Errorf("clicking the calibration step opened settings %d times, want 1", opened)
	}

	// And the hide button is reachable the same way.
	b.handleMouse(pointer{x: l.hintBtn.x + 4, y: l.hintBtn.y + 4, pressed: true, down: true})
	if b.hintOpen {
		t.Error("clicking hide left the strip showing")
	}
}

// TestTextMetricsMeasureWhatIsDrawn (audit A9's successor): with real
// typefaces the measurement is the shaper's advance, so widths must be
// positive and monotone-ish, non-ASCII must measure like any other text,
// and a cut must never split a rune or blow its pixel budget.
func TestTextMetricsMeasureWhatIsDrawn(t *testing.T) {
	if w := textW("étude"); w <= 0 {
		t.Errorf("textW(étude) = %v, want a positive advance", w)
	}
	if textW("wide text here") <= textW("a") {
		t.Error("a longer string measured narrower than a single glyph")
	}
	// The scaled measurement must track the scaled face drawTextScaled
	// actually uses, or centred headings drift off centre.
	if textWScaled("Title", 2) <= textW("Title") {
		t.Error("a heading at scale 2 should measure wider than body text")
	}
	// A cut must never split a rune and never exceed its budget.
	for _, s := range []string{"ααααααα", "日本語のタブ譜", "naïve—song.gp", "héllo wörld"} {
		for _, px := range []float64{0, 10, 30, 60, 200} {
			for name, f := range map[string]func(string, float64) string{"truncateW": truncateW, "ellipsizeW": ellipsizeW} {
				out := f(s, px)
				if !utf8.ValidString(out) {
					t.Fatalf("%s(%q, %v) produced invalid UTF-8 %q", name, s, px, out)
				}
				if px > 0 && out != s && textW(out) > px {
					t.Fatalf("%s(%q, %v) = %q measures %.1fpx, past its budget", name, s, px, out, textW(out))
				}
			}
		}
	}
}

// ---- verification follow-ups ----------------------------------------------

// TestQuitBeatsSameFrameStackEdits: pending edits run in queue order, so
// a Show queued after a Quit in the same frame — Q and a chip click can
// both fire in one Update — must not resurrect the emptied stack and
// leave the application running with its screens gone.
func TestQuitBeatsSameFrameStackEdits(t *testing.T) {
	root := &shellPlainScreen{}
	sh := NewShell(Services{}, root)
	sh.Quit()
	sh.Show(&shellPlainScreen{}) // queued after the quit, same frame
	if err := sh.Update(); err != errQuit {
		t.Fatalf("Update = %v, want errQuit — the later Show must not undo the quit", err)
	}
	if sh.Depth() != 0 {
		t.Errorf("depth = %d after quit, want 0", sh.Depth())
	}

	// And edits queued on LATER frames stay inert too: quitting is
	// terminal, not a one-frame race winner.
	sh.Replace(&shellPlainScreen{})
	if err := sh.Update(); err != errQuit {
		t.Errorf("Update after a post-quit Replace = %v, want errQuit", err)
	}
}

// TestCountInRoundTripWithdrawsThePrompt: pending is a comparison against
// the engine's constructed count-in, not a latch — toggling C twice is no
// change, and the F5 offer it raised must go away with it.
func TestCountInRoundTripWithdrawsThePrompt(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(4)
	a.SetReloader(func() {})
	a.SetCountInApplier(func(int) bool { return false }) // the shell's shape

	a.toggleCountIn() // 4 -> off: genuinely pending
	if a.reloadPrompt() == "" {
		t.Fatal("a pending count-in change should offer F5")
	}
	a.toggleCountIn() // off -> 4: back to what the engine was built with
	if got := a.reloadPrompt(); got != "" {
		t.Errorf("after a round trip the offer is still up: %q", got)
	}

	// A change the settings screen reported is NOT withdrawn by count-in
	// arithmetic: the two conditions are independent.
	a.MarkSettingsChanged()
	a.toggleCountIn()
	a.toggleCountIn()
	if a.reloadPrompt() == "" {
		t.Error("a count-in round trip must not clear the settings screen's change")
	}
}

// TestSettingsFallbackNoteClearsOnSelection: the "saved device is not
// connected" note describes the current saved ID; explicitly selecting a
// connected device makes that device the saved one, and the note must go
// with the staleness it described.
func TestSettingsFallbackNoteClearsOnSelection(t *testing.T) {
	audio := newSettingsAudio()
	s, pr := newSettingsFixture(t, audio)
	pr.SetDevices("cap-unplugged", "play-focus")
	s = NewSettings(s.sh)
	if !s.capMissing {
		t.Fatal("a stale saved capture ID should raise the fallback note")
	}

	s.cycleCapture(+1) // an explicit selection commits and saves
	if s.capMissing {
		t.Error("selecting a connected device left the not-connected note up")
	}
	if capID, _ := pr.Devices(); capID != s.capture[s.capIdx].ID {
		t.Errorf("prefs hold %q, screen selected %q — the commit did not save", capID, s.capture[s.capIdx].ID)
	}
}

// TestSoundFontBrowseBusyGuard (verification follow-up): repeated
// activation of the SoundFont browse must not stack OS dialogs — the
// picker contract is one dialog at a time, re-armed by the outcome
// callback, which fires with "" on cancel.
func TestSoundFontBrowseBusyGuard(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	asked := 0
	var done func(string)
	s.SetFilePicker(func(_ []string, chosen func(string)) { asked++; done = chosen })

	s.chooseSoundFont()
	s.chooseSoundFont() // a second Enter while the dialog floats
	if asked != 1 {
		t.Fatalf("two activations opened %d dialogs, want 1", asked)
	}

	// A cancel re-arms without changing the setting.
	done("")
	s.syncSettings()
	if s.sfBusy {
		t.Error("a cancel outcome should re-arm the browse")
	}
	if s.soundFont != "" {
		t.Errorf("a cancel changed the soundfont to %q", s.soundFont)
	}

	// And the re-armed browse works: a real pick applies.
	s.chooseSoundFont()
	if asked != 2 {
		t.Fatalf("the re-armed browse did not open a dialog (%d)", asked)
	}
	done(`C:\sounds\grand.sf2`)
	s.syncSettings()
	if s.soundFont != `C:\sounds\grand.sf2` {
		t.Errorf("the picked soundfont did not apply, got %q", s.soundFont)
	}
	if s.sfBusy {
		t.Error("an applied outcome should also re-arm the browse")
	}
}

// ---- UI re-review follow-ups ----------------------------------------------

// TestBindingsDescribeTheFocusedPane: Delete forgets a shortcut, and the
// library has none to forget — it lists what is in a folder. A footer
// teaching a key that cannot work on the pane the user is looking at is
// the same fault the first-run checklist had.
func TestBindingsDescribeTheFocusedPane(t *testing.T) {
	sh := NewShell(Services{Prefs: &settingsFakePrefs{}, Audio: newSettingsAudio(),
		Library: stubLibrary{}}, nil)
	b := NewBrowser(sh)
	if !strings.Contains(b.hintLine(), "del forget") {
		t.Errorf("the recent pane's footer omits Delete: %q", b.hintLine())
	}
	b.focusPane(paneLibrary)
	if strings.Contains(b.hintLine(), "del forget") {
		t.Errorf("the library footer advertises Delete: %q", b.hintLine())
	}
}

// TestDialogOpenStopsTheRestOfTheFrame: a piece opened by the file
// dialog's result has to halt the frame exactly like one opened by
// Enter. The one-action-per-frame baseline used to be sampled AFTER the
// mailbox drained, folding the dialog's own push into the baseline — so
// an Escape in the same frame reached the Shell with a push already
// queued, and it built a practice screen it discarded without releasing
// the audio.
func TestDialogOpenStopsTheRestOfTheFrame(t *testing.T) {
	op := &flowOpener{}
	sh := NewShell(Services{Opener: op, Prefs: &browserFakePrefs{}}, nil)
	b := NewBrowser(sh)
	b.SetOpenDialog(func(string) {})

	piece := filepath.Join(t.TempDir(), "picked.gtab")
	if err := os.WriteFile(piece, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.launchOpenDialog("")
	b.OfferDialogResult(piece, "")

	// The frame the result lands on: Update must apply the open and stop,
	// reporting no quit even though a key would otherwise be read.
	if err := b.Update(); err != nil {
		t.Fatalf("Update on the drain frame = %v, want nil", err)
	}
	if len(op.opened) != 1 {
		t.Fatalf("the dialog result opened %d pieces, want 1", len(op.opened))
	}
	if got := len(sh.pending); got != 1 {
		t.Errorf("shell holds %d queued edits after the drain frame, want exactly the one push", got)
	}
}

// TestCountInSyncsFromSettings: the view keeps its own mirror of the
// count-in, and that mirror is what the next press of C writes back — so
// a count-in changed in settings has to reach it, or C restores the
// open-time value over the user's new choice.
func TestCountInSyncsFromSettings(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(4) // the engine was built with 4
	a.SetReloader(func() {})
	var saved []int
	a.SetCountInApplier(func(b int) bool { saved = append(saved, b); return false })

	a.SyncCountIn(2) // settings changed it to 2 while the piece runs
	if got := a.CountInBeats(); got != 2 {
		t.Fatalf("after the settings change the view offers %d, want 2", got)
	}
	if !a.countInStale || a.reloadPrompt() == "" {
		t.Error("2 differs from the engine's 4, so the reload should still be offered")
	}

	a.toggleCountIn() // off
	a.toggleCountIn() // back on: must restore 2, not the open-time 4
	if len(saved) != 2 || saved[0] != 0 || saved[1] != 2 {
		t.Errorf("applier saw %v, want [0 2] — the settings value, not the open-time one", saved)
	}
}
