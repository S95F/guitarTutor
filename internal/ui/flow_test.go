package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

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

func TestChecklistStepsAreActivatable(t *testing.T) {
	sh := NewShell(Services{Prefs: &settingsFakePrefs{}, Audio: newSettingsAudio()}, nil)
	b := NewBrowser(sh)
	opened := 0
	b.SetSettingsOpener(func() { opened++ })
	launches := 0
	b.SetOpenDialog(func(string) { launches++ })

	b.activateStep(0)
	if opened != 1 {
		t.Errorf("step 1 opened settings %d times, want 1", opened)
	}

	b.activateStep(len(b.stepList()) - 1)
	if launches != 1 {
		t.Errorf(`the "open a piece" step launched the file dialog %d times, want 1`, launches)
	}

	b.activateStep(-1)
	b.activateStep(99)
}

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

	b.toggleHint()
	if !b.hintOpen || pr.hideHint {
		t.Error("toggling again did not restore the strip")
	}
}

func TestSettingsOpensOnTheFirstUnconfiguredRow(t *testing.T) {
	audio := newSettingsAudio()
	s, pr := newSettingsFixture(t, audio)
	if got, want := s.rows[s.cur], srCapture; got != want {
		t.Errorf("with no device chosen the cursor sits on row %v, want %v", got, want)
	}

	pr.SetDevices("cap-focus", "play-focus")
	s = NewSettings(s.sh)
	if got, want := s.rows[s.cur], srCalibrate; got != want {
		t.Errorf("with an unmeasured pair the cursor sits on row %v, want %v", got, want)
	}

	audio.offsets = map[string]settingsOffset{"cap-focus|play-focus": {frames: 480, ok: true}}
	s = NewSettings(s.sh)
	if s.cur != 0 {
		t.Errorf("with everything configured the cursor should stay at the top, got row %d", s.cur)
	}
}

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

func TestSettingsClickSelectsTheWholeRow(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	it := settingsRowItem(t, s, srCountIn)
	band := it.band()
	s.handleMouse(pointer{x: band.x + band.w - 200, y: band.y + band.h/2, down: true, pressed: true})
	if got := s.rows[s.cur]; got != srCountIn {
		t.Errorf("clicking the count-in row selected %v", got)
	}
}

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

func TestReopenPieceReplacesInPlace(t *testing.T) {
	op := &flowOpener{}
	sh := NewShell(Services{Opener: op, Prefs: &browserFakePrefs{}}, &shellPlainScreen{})
	if _, err := sh.OpenPiece("song.gp"); err != nil {
		t.Fatalf("OpenPiece: %v", err)
	}
	_ = sh.Update()
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

func TestHintIsClickable(t *testing.T) {
	sh := NewShell(Services{Prefs: &settingsFakePrefs{}, Audio: newSettingsAudio()}, nil)
	b := NewBrowser(sh)
	opened := 0
	b.SetSettingsOpener(func() { opened++ })

	l := b.layout()
	if len(l.steps) < 2 {
		t.Fatalf("the strip laid out %d steps, want at least 2", len(l.steps))
	}

	r := l.steps[1]
	b.handleMouse(pointer{x: r.x + 4, y: r.y + 4, pressed: true, down: true})
	if opened != 1 {
		t.Errorf("clicking the calibration step opened settings %d times, want 1", opened)
	}

	b.handleMouse(pointer{x: l.hintBtn.x + 4, y: l.hintBtn.y + 4, pressed: true, down: true})
	if b.hintOpen {
		t.Error("clicking hide left the strip showing")
	}
}

func TestTextMetricsMeasureWhatIsDrawn(t *testing.T) {
	if w := textW("étude"); w <= 0 {
		t.Errorf("textW(étude) = %v, want a positive advance", w)
	}
	if textW("wide text here") <= textW("a") {
		t.Error("a longer string measured narrower than a single glyph")
	}

	if textWScaled("Title", 2) <= textW("Title") {
		t.Error("a heading at scale 2 should measure wider than body text")
	}

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

func TestQuitBeatsSameFrameStackEdits(t *testing.T) {
	root := &shellPlainScreen{}
	sh := NewShell(Services{}, root)
	sh.Quit()
	sh.Show(&shellPlainScreen{})
	if err := sh.Update(); err != errQuit {
		t.Fatalf("Update = %v, want errQuit — the later Show must not undo the quit", err)
	}
	if sh.Depth() != 0 {
		t.Errorf("depth = %d after quit, want 0", sh.Depth())
	}

	sh.Replace(&shellPlainScreen{})
	if err := sh.Update(); err != errQuit {
		t.Errorf("Update after a post-quit Replace = %v, want errQuit", err)
	}
}

func TestCountInRoundTripWithdrawsThePrompt(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(4)
	a.SetReloader(func() {})
	a.SetCountInApplier(func(int) bool { return false })

	a.toggleCountIn()
	if a.reloadPrompt() == "" {
		t.Fatal("a pending count-in change should offer F5")
	}
	a.toggleCountIn()
	if got := a.reloadPrompt(); got != "" {
		t.Errorf("after a round trip the offer is still up: %q", got)
	}

	a.MarkSettingsChanged()
	a.toggleCountIn()
	a.toggleCountIn()
	if a.reloadPrompt() == "" {
		t.Error("a count-in round trip must not clear the settings screen's change")
	}
}

func TestSettingsFallbackNoteClearsOnSelection(t *testing.T) {
	audio := newSettingsAudio()
	s, pr := newSettingsFixture(t, audio)
	pr.SetDevices("cap-unplugged", "play-focus")
	s = NewSettings(s.sh)
	if !s.capMissing {
		t.Fatal("a stale saved capture ID should raise the fallback note")
	}

	s.cycleCapture(+1)
	if s.capMissing {
		t.Error("selecting a connected device left the not-connected note up")
	}
	if capID, _ := pr.Devices(); capID != s.capture[s.capIdx].ID {
		t.Errorf("prefs hold %q, screen selected %q — the commit did not save", capID, s.capture[s.capIdx].ID)
	}
}

func TestSoundFontBrowseBusyGuard(t *testing.T) {
	s, _ := newSettingsFixture(t, newSettingsAudio())
	asked := 0
	var done func(string)
	s.SetFilePicker(func(_ []string, chosen func(string)) { asked++; done = chosen })

	s.chooseSoundFont()
	s.chooseSoundFont()
	if asked != 1 {
		t.Fatalf("two activations opened %d dialogs, want 1", asked)
	}

	done("")
	s.syncSettings()
	if s.sfBusy {
		t.Error("a cancel outcome should re-arm the browse")
	}
	if s.soundFont != "" {
		t.Errorf("a cancel changed the soundfont to %q", s.soundFont)
	}

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

func TestCountInSyncsFromSettings(t *testing.T) {
	a := newApp(t, 1)
	a.SetCountIn(4)
	a.SetReloader(func() {})
	var saved []int
	a.SetCountInApplier(func(b int) bool { saved = append(saved, b); return false })

	a.SyncCountIn(2)
	if got := a.CountInBeats(); got != 2 {
		t.Fatalf("after the settings change the view offers %d, want 2", got)
	}
	if !a.countInStale || a.reloadPrompt() == "" {
		t.Error("2 differs from the engine's 4, so the reload should still be offered")
	}

	a.toggleCountIn()
	a.toggleCountIn()
	if len(saved) != 2 || saved[0] != 0 || saved[1] != 2 {
		t.Errorf("applier saw %v, want [0 2] — the settings value, not the open-time one", saved)
	}
}
