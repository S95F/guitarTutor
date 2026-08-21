package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/score/textfmt"
	"github.com/hajimehoshi/ebiten/v2"
)

func TestButtonLabelBudgetIgnoresAbsentGlyphs(t *testing.T) {
	b := browserFixture(t, &browserFakePrefs{}, &browserFakeOpener{})
	for _, hint := range []bool{true, false} {
		b.hintOpen = hint
		r := b.layout().hintBtn
		label := "hide  (H)"
		if !hint {
			label = "show  (H)"
		}

		if got := truncateW(label, r.w-22); got != label {
			t.Errorf("the hint toggle draws %q instead of %q in a %.0f px button", got, label, r.w)
		}
	}
}

func TestDisabledControlsStillNameThemselves(t *testing.T) {
	e := newTestEditor()
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

	press(t, e, ebiten.KeyDigit5)
	if got := tipTextFor(editorButton(t, e, "hammer")); got != "Hammer-on" {
		t.Errorf("a live control's tooltip reads %q", got)
	}
}

func TestPieceRowKeepsItsSettings(t *testing.T) {
	e := newTestEditor()
	for i := 0; i < 12; i++ {
		if err := e.doc.AddTrack("A track with a long name", nil); err != nil {
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

	limit := l.filesAt[0].x
	for i, r := range l.pieceAt {
		if r.x+r.w > limit {
			t.Errorf("piece control %d ends at %.0f, past the file row at %.0f", i, r.x+r.w, limit)
		}
	}

	before := len(e.doc.Score().Tracks)
	pressMod(t, e, ebiten.KeyA, mods{shift: true})
	if e.picker == nil || e.picker.purpose != pickAddTrack {
		t.Fatal("shift+A did not open the instrument picker for the new track")
	}
	e.applyPick(0)
	if got := len(e.doc.Score().Tracks); got != before+1 {
		t.Errorf("shift+A then guitar left %d tracks, want %d", got, before+1)
	}
}

func TestEditorHeaderTitleYieldsToTheStatus(t *testing.T) {
	e := newTestEditor()
	if err := e.doc.SetTitle(strings.Repeat("a very long piece title ", 8)); err != nil {
		t.Fatal(err)
	}
	title, status := e.headerTitle(), e.statusLine()

	titleEnd := uiPadX + textWScaled(title, uiTitleScl)
	statusStart := screenW - uiPadX - textW(status)
	if titleEnd > statusStart {
		t.Errorf("the title ends at %.0f and the status starts at %.0f: they overlap by %.0f px",
			titleEnd, statusStart, titleEnd-statusStart)
	}
}

func TestBackingTrackIsVisibleOnTheRow(t *testing.T) {
	e := newTestEditor()
	if err := e.doc.AddTrack("Rhythm", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.doc.SetRole(1); err != nil {
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

func TestParserCountsInBeats(t *testing.T) {
	for _, src := range []string{
		"\\time 4/4\n0.6.4 |\n",
		"\\time 4/4\n0.6.1 0.6.4 |\n",
	} {
		_, err := textfmt.Parse([]byte(src), "piece")
		if err == nil {
			t.Fatalf("%q should not parse", src)
		}
		msg := textfmt.ProblemLine(err)
		if strings.Contains(msg, "tick") {
			t.Errorf("the complaint counts in ticks: %q", msg)
		}
		if !strings.Contains(msg, "beat") {
			t.Errorf("the complaint never mentions beats: %q", msg)
		}
	}
}

func TestFrameClockIsMeasuredNotAssumed(t *testing.T) {
	uiFrameLast, uiFrameDT = time.Time{}, 1.0/playheadFallbackTPS
	measureFrame()
	time.Sleep(12 * time.Millisecond)
	measureFrame()
	if got := uiFrameSeconds(); got < 0.008 || got > 0.05 {
		t.Errorf("a ~12 ms frame measured as %.4f s", got)
	}

	uiFrameLast = time.Now().Add(-30 * time.Second)
	measureFrame()
	if got := uiFrameSeconds(); got > 1.0/20+1e-9 {
		t.Errorf("a 30 second gap measured as %.4f s; want it clamped", got)
	}

	first := uiFrameSeconds()
	time.Sleep(5 * time.Millisecond)
	if second := uiFrameSeconds(); second != first {
		t.Errorf("uiFrameSeconds moved within a frame: %v then %v", first, second)
	}
}
