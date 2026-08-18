package ui

// Regressions from the adversarial review of the icon toolbar. Each of
// these is a defect that was found by measurement rather than by reading,
// so each is pinned by measurement rather than by inspection.

import (
	"strings"
	"testing"
	"time"

	"github.com/S95F/guitarTutor/internal/score/textfmt"
	"github.com/hajimehoshi/ebiten/v2"
)

// TestButtonLabelBudgetIgnoresAbsentGlyphs: drawButton reserved a
// symbol's width whether or not it drew one, which cost the start
// screen's collapsed hint toggle enough room to ellipsize "show  (H)"
// into "show  …" — losing the key on the one state a dismissed strip
// leaves you in.
func TestButtonLabelBudgetIgnoresAbsentGlyphs(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	for _, hint := range []bool{true, false} {
		b.hintOpen = hint
		r := b.layout().hintBtn
		label := "hide  (H)"
		if !hint {
			label = "show  (H)"
		}
		// The same budget drawButton computes for a glyph-less button.
		if got := truncateW(label, r.w-22); got != label {
			t.Errorf("the hint toggle draws %q instead of %q in a %.0f px button", got, label, r.w)
		}
	}
}

// TestDisabledControlsStillNameThemselves: a greyed icon with no name is
// the worst thing on a toolbar — it is exactly the control the user does
// not understand — so the tooltip still answers, and says what would make
// it available.
func TestDisabledControlsStillNameThemselves(t *testing.T) {
	e := newTestEditor() // the cursor starts on a rest, so the note marks are off
	for _, id := range []string{"tie", "hammer", "bend", "undo", "redo"} {
		b := editorButton(t, e, id)
		if !b.disabled {
			t.Fatalf("the %q control is not disabled in this fixture", id)
		}
		if b.why == "" {
			t.Errorf("the %q control is greyed with no explanation", id)
		}
		tip := tipTextFor(b)
		if !strings.Contains(tip, b.name) || !strings.Contains(tip, b.why) {
			t.Errorf("the %q tooltip reads %q; want the name and the reason", id, tip)
		}
	}
	// And a live control's tooltip is just its name.
	press(t, e, ebiten.KeyDigit5)
	if got := tipTextFor(editorButton(t, e, "hammer")); got != "Hammer-on" {
		t.Errorf("a live control's tooltip reads %q", got)
	}
}

// TestPieceRowKeepsItsSettings: the row used to lay tracks out first and
// drop whatever ran off the end, which meant a piece with enough tracks
// lost the add-track control — and that control had no keyboard binding
// at all, so the piece could not gain another track by any route.
func TestPieceRowKeepsItsSettings(t *testing.T) {
	e := newTestEditor()
	for i := 0; i < 12; i++ {
		if err := e.doc.AddTrack("A track with a long name"); err != nil {
			t.Fatal(err)
		}
	}
	l := e.layoutToolbar()
	present := map[string]bool{}
	for _, b := range l.piece {
		present[b.id] = true
	}
	for _, id := range []string{"addtrack", "tuning", "meter", "tempo", "title"} {
		if !present[id] {
			t.Errorf("the %q control was dropped from a crowded piece row", id)
		}
	}
	// Nothing overlaps the file row either.
	limit := l.filesAt[0].x
	for i, r := range l.pieceAt {
		if r.x+r.w > limit {
			t.Errorf("piece control %d ends at %.0f, past the file row at %.0f", i, r.x+r.w, limit)
		}
	}
	// And the keyboard reaches it now.
	before := len(e.doc.Score().Tracks)
	pressMod(t, e, ebiten.KeyA, mods{shift: true})
	if got := len(e.doc.Score().Tracks); got != before+1 {
		t.Errorf("shift+A left %d tracks, want %d", got, before+1)
	}
}

// TestEditorHeaderTitleYieldsToTheStatus: the status line is drawn
// right-aligned and never truncated, so a title measured against half the
// window ran straight into it.
func TestEditorHeaderTitleYieldsToTheStatus(t *testing.T) {
	e := newTestEditor()
	if err := e.doc.SetTitle(strings.Repeat("a very long piece title ", 8)); err != nil {
		t.Fatal(err)
	}
	title, status := e.headerTitle(), e.statusLine()
	// drawHeader draws the title from uiPadX at uiTitleScl and the status
	// ending at screenW-uiPadX.
	titleEnd := uiPadX + textWScaled(title, uiTitleScl)
	statusStart := screenW - uiPadX - textW(status)
	if titleEnd > statusStart {
		t.Errorf("the title ends at %.0f and the status starts at %.0f: they overlap by %.0f px",
			titleEnd, statusStart, titleEnd-statusStart)
	}
}

// TestBackingTrackIsVisibleOnTheRow: which part is accompaniment and
// which is the one you practise is a fact about the piece. Moving it into
// the tooltip meant you had to hover to learn it, which is the same as
// most people never learning it.
func TestBackingTrackIsVisibleOnTheRow(t *testing.T) {
	e := newTestEditor()
	if err := e.doc.AddTrack("Rhythm"); err != nil {
		t.Fatal(err)
	}
	if err := e.doc.SetRole(1); err != nil { // score.RoleBacking
		t.Fatal(err)
	}
	var found bool
	for _, b := range e.pieceButtons() {
		if strings.Contains(b.label, "Rhythm") {
			found = true
			if !strings.Contains(b.label, "backing") {
				t.Errorf("the backing track's control reads %q, with no sign of its role", b.label)
			}
		}
	}
	if !found {
		t.Error("the second track has no control on the piece row")
	}
}

// TestParserCountsInBeats: the parser's bar-length complaints reach the
// user twice — under the text view and in the library's own listing — and
// they used to be measured in PPQ ticks, a unit defined nowhere a user
// can see.
func TestParserCountsInBeats(t *testing.T) {
	for _, src := range []string{
		"\\time 4/4\n0.6.4 |\n",       // underfull
		"\\time 4/4\n0.6.1 0.6.4 |\n", // overfull
	} {
		_, err := textfmt.Parse([]byte(src), "piece")
		if err == nil {
			t.Fatalf("%q should not parse", src)
		}
		msg := gtabProblem(err)
		if strings.Contains(msg, "tick") {
			t.Errorf("the complaint counts in ticks: %q", msg)
		}
		if !strings.Contains(msg, "beat") {
			t.Errorf("the complaint never mentions beats: %q", msg)
		}
	}
}

// TestFrameClockIsMeasuredNotAssumed: the step used to be 1/TPS, which is
// the UPDATE rate — but it is read from Draw, which Ebitengine calls once
// per display refresh. On a 144 Hz monitor every animation ran two and a
// half times too fast and the tooltip's dwell shrank with it.
func TestFrameClockIsMeasuredNotAssumed(t *testing.T) {
	uiFrameLast, uiFrameDT = time.Time{}, 1.0/playheadFallbackTPS
	measureFrame() // the first call only starts the clock
	time.Sleep(12 * time.Millisecond)
	measureFrame()
	if got := uiFrameSeconds(); got < 0.008 || got > 0.05 {
		t.Errorf("a ~12 ms frame measured as %.4f s", got)
	}
	// A window that was suspended hands back seconds; clamped, animations
	// resume where they were instead of snapping.
	uiFrameLast = time.Now().Add(-30 * time.Second)
	measureFrame()
	if got := uiFrameSeconds(); got > 1.0/20+1e-9 {
		t.Errorf("a 30 second gap measured as %.4f s; want it clamped", got)
	}
	// And the same value comes back however many times a frame asks.
	first := uiFrameSeconds()
	time.Sleep(5 * time.Millisecond)
	if second := uiFrameSeconds(); second != first {
		t.Errorf("uiFrameSeconds moved within a frame: %v then %v", first, second)
	}
}
