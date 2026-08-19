package ui

// Live-feedback drawing tests: the live-warning banner's interior and the
// verdict legend's spacing.
//
// Ebitengine cannot render in a unit test, so as everywhere else in this
// package the drawing is split into a layout the test can read and a draw
// that only paints it. What these check is what a screenshot would have
// caught: text drawn outside the box it belongs to, text welded to a
// border, and a row spaced by counting characters under a proportional
// face.

import (
	"strings"
	"testing"

	"github.com/S95F/guitarTutor/internal/practice"
)

// splitDeviceMsg is the real thing warnOnSplitDevices raises in
// cmd/guitartutor — two full Windows device names inside one sentence. It
// is 1383 pixels wide at scale 1 against a 1168 pixel banner interior,
// which is what made the unwrapped banner draw it from x=-51 to x=1331
// with its head and its tail clipped off the window.
const splitDeviceMsg = "capture and playback are different devices (Microphone Array (Intel Smart Sound Technology for Digital Microphones) / Speakers (Realtek(R) Audio)): their clocks drift apart over a session and timing scores wander"

// warnHintH is the dismiss hint's line height, which is also the body
// line height a wrapped message uses.
func warnHintH() float64 { return lineHeightOf(faceOf(srcBody, fontBody)) }

// TestWarningBannerWrapsALongMessage: a message too wide for the banner
// is wrapped into it, not drawn straight across the window. Every line
// has to fit the interior, and nothing may be dropped on the way.
func TestWarningBannerWrapsALongMessage(t *testing.T) {
	if textWScaled(splitDeviceMsg, 1) <= warnRect().w {
		t.Fatal("the fixture message now fits the banner; it no longer tests wrapping")
	}
	l := warnLayoutFor(splitDeviceMsg)
	if len(l.lines) < 2 {
		t.Fatalf("a %.0fpx message was laid out on %d line(s)", textW(splitDeviceMsg), len(l.lines))
	}
	for i, s := range l.lines {
		w := l.width(s)
		if w > l.inner.w {
			t.Errorf("line %d is %.0fpx wide, past the %.0fpx interior: %q", i, w, l.inner.w, s)
		}
		// And it must be drawn inside the banner, not centred over a
		// width it does not fit.
		x := centreX(s, l.inner.x, l.inner.w)
		if x < l.inner.x || x+w > l.inner.x+l.inner.w {
			t.Errorf("line %d is drawn %.0f..%.0f, outside the interior %.0f..%.0f",
				i, x, x+w, l.inner.x, l.inner.x+l.inner.w)
		}
	}
	// This message fits the lines available, so wrapping must be
	// lossless: a warning that silently lost a device name would be
	// worse than one that was clipped.
	if got := strings.Join(l.lines, " "); got != splitDeviceMsg {
		t.Errorf("wrapping changed the message:\n got %q\nwant %q", got, splitDeviceMsg)
	}
}

// TestWarningBannerTruncatesPastTheLinesThatFit: the banner is a fixed 56
// pixels between the track strip and the first string, so a message
// longer than that affords is cut with an ellipsis rather than allowed to
// grow the block over its neighbours.
func TestWarningBannerTruncatesPastTheLinesThatFit(t *testing.T) {
	msg := strings.TrimSpace(strings.Repeat("the input stream stopped and the interface was removed ", 8))
	l := warnLayoutFor(msg)
	if l.heading {
		t.Fatal("a message this long was laid out as a one-line heading")
	}
	if bottom := l.hintY + warnHintH(); bottom > l.inner.y+l.inner.h {
		t.Errorf("the block ends at %.2f, past the interior's %.2f — the banner cannot grow",
			bottom, l.inner.y+l.inner.h)
	}
	if want := int((l.inner.h - warnHintH() - warnMinGapY) / l.lineH); len(l.lines) != want {
		t.Errorf("laid out %d lines, want the %d that fit", len(l.lines), want)
	}
	if last := l.lines[len(l.lines)-1]; !strings.HasSuffix(last, "…") {
		t.Errorf("the last line %q does not show that the message was cut", last)
	}
	for i, s := range l.lines {
		if w := l.width(s); w > l.inner.w {
			t.Errorf("line %d is %.0fpx wide, past the %.0fpx interior", i, w, l.inner.w)
		}
	}
}

// TestWarningBannerInteriorIsBalanced: the interior is laid out from the
// faces' real ascent and descent, so the message block and the hint sit
// evenly inside the box and the hint's descenders clear the bottom
// border.
//
// The fixed offsets this replaces put the message at y+12 — 16 pixels of
// dead air above it — and the hint at y+40, whose 'p' reached y+56.1,
// straight through the 2px border stroked across y+55..y+57.
func TestWarningBannerInteriorIsBalanced(t *testing.T) {
	for _, msg := range []string{
		"input stream stopped",
		"could not re-open the piece: truncated archive",
		splitDeviceMsg,
	} {
		l := warnLayoutFor(msg)
		blockH := float64(len(l.lines)) * l.lineH
		above := l.msgY - l.inner.y
		between := l.hintY - (l.msgY + blockH)
		below := (l.inner.y + l.inner.h) - (l.hintY + warnHintH())

		const eps = 0.01
		if above < -eps || between < -eps || below < -eps {
			t.Errorf("%.20q: interior spacing above=%.2f between=%.2f below=%.2f, one of them is negative",
				msg, above, between, below)
		}
		if diff := above - below; diff > eps || diff < -eps {
			t.Errorf("%.20q: %.2f above the message but %.2f under the hint — the block is not centred",
				msg, above, below)
		}
		if diff := above - between; diff > eps || diff < -eps {
			t.Errorf("%.20q: %.2f above the message but %.2f between it and the hint — the slack is not shared",
				msg, above, between)
		}
		// The border is stroked 2px wide centred on the banner's edges,
		// so its ink covers ptWarnY+ptWarnH-1 .. +1. Nothing may reach it.
		if bottom := l.hintY + warnHintH(); bottom > ptWarnY+ptWarnH-warnBorderW {
			t.Errorf("%.20q: the hint's descenders reach %.2f, on the border at %.0f",
				msg, bottom, float64(ptWarnY+ptWarnH)-warnBorderW/2)
		}
		if l.msgY < ptWarnY+warnBorderW {
			t.Errorf("%.20q: the message starts at %.2f, on the top border", msg, l.msgY)
		}
	}
}

// TestLegendItemsAreEvenlySpaced: the row advances by the shaped width of
// each label plus one gap, so the space between items is the same
// everywhere.
//
// It used to advance 7*(len(label)+2) — the bitmap font's cell width
// times a character count — which under the proportional face left the
// four items 15.7, 19.8 and 15.7 pixels apart, and would drift further
// for any label that is wider or not ASCII.
func TestLegendItemsAreEvenlySpaced(t *testing.T) {
	xs := legendXs()
	if len(xs) != len(legendItems) {
		t.Fatalf("%d positions for %d items", len(xs), len(legendItems))
	}
	if xs[0] != uiPadX {
		t.Errorf("the legend starts at %.2f, not the page margin %.0f", xs[0], uiPadX)
	}
	const eps = 0.001
	for i := 1; i < len(xs); i++ {
		want := xs[i-1] + textW(legendItems[i-1].s) + legendGap
		if diff := xs[i] - want; diff > eps || diff < -eps {
			t.Errorf("item %d (%q) starts at %.2f, want %.2f (the previous label's width plus the gap)",
				i, legendItems[i].s, xs[i], want)
		}
		if gap := xs[i] - (xs[i-1] + textW(legendItems[i-1].s)); gap-legendGap > eps || legendGap-gap > eps {
			t.Errorf("the gap after %q is %.2f, not the uniform %.0f", legendItems[i-1].s, gap, legendGap)
		}
	}
	last := len(xs) - 1
	if right := xs[last] + textW(legendItems[last].s); right > screenW-uiPadX {
		t.Errorf("the legend ends at %.0f, past the %.0f margin", right, screenW-uiPadX)
	}
}

// TestLiveStatsLineEarnsItsPercentage: Accuracy() answers 1.0 with
// nothing judged — right for the scorer, but printed as "100%" the moment
// a session opens it is a grade nobody earned. The percentage waits for
// the first judged note.
func TestLiveStatsLineEarnsItsPercentage(t *testing.T) {
	if got := liveStatsLine(practice.Stats{}); strings.Contains(got, "%") {
		t.Errorf("with nothing judged the stats line %q still grades the player", got)
	}
	if got := liveStatsLine(practice.Stats{}); !strings.Contains(got, "hit 0") {
		t.Errorf("the zero counts must still show, got %q", got)
	}
	if got := liveStatsLine(practice.Stats{Hit: 1, Miss: 1}); !strings.Contains(got, "50%") {
		t.Errorf("one hit and one miss should read 50%%, got %q", got)
	}
}

// TestTunerIdleLineIsHonest: T works in every session, but "listening..."
// is only true in a live one — without a capture device the honest line
// says what to change, and it has to fit the window at the scale it is
// drawn at.
func TestTunerIdleLineIsHonest(t *testing.T) {
	a := newApp(t, 1)
	s, scale := a.tunerIdleLine()
	if !strings.Contains(s, "settings") {
		t.Errorf("with no live input the tuner says %q, want it to point at settings", s)
	}
	if w := textWScaled(s, scale); w > screenW {
		t.Errorf("the idle line is %.0f px wide at scale %v, past the %d px window", w, scale, screenW)
	}

	a.SetLiveStatus(func() (float64, int64) { return -20, 0 })
	a.syncLive()
	if s, _ := a.tunerIdleLine(); s != "listening..." {
		t.Errorf("a live session's idle tuner says %q, want %q", s, "listening...")
	}
}

// TestDismissedWarningReturnsForANewOccurrence: press F5, the reload
// fails, dismiss the banner, press F5 again — the identical error message
// has to come back, or the key the reload prompt is telling the user to
// press does nothing they can see.
//
// The app layer only speaks on failure, so the frames between the two
// presses post nothing; that lapse is what marks the second failure as a
// new event rather than the first one still being reported.
func TestDismissedWarningReturnsForANewOccurrence(t *testing.T) {
	a := newApp(t, 1)
	const msg = "could not re-open the piece: truncated archive"

	a.SetLiveWarning(msg)
	a.syncLive()
	if !a.warningVisible() {
		t.Fatal("the first failure raised no banner")
	}
	a.dismissWarning()

	// Frames in which nothing happens: the user is reading, then presses
	// F5 again.
	for i := 0; i < 3; i++ {
		a.syncLive()
		if a.warningVisible() {
			t.Fatal("the dismissed banner came back on its own")
		}
		if a.warnMsg != msg {
			t.Fatalf("the message was lost while dismissed: %q", a.warnMsg)
		}
	}

	a.SetLiveWarning(msg)
	a.syncLive()
	if !a.warningVisible() {
		t.Error("the second identical failure raised no banner")
	}
}

// TestRepeatedWarningStaysDismissed is the other half: a polled condition
// re-posts the same text on every frame, and dismissing it has to mean
// dismissed — otherwise the banner reappears the frame after the D key.
func TestRepeatedWarningStaysDismissed(t *testing.T) {
	a := newApp(t, 1)
	const msg = "input stream stopped"

	for i := 0; i < 5; i++ {
		a.SetLiveWarning(msg)
		a.syncLive()
	}
	if !a.warningVisible() {
		t.Fatal("the polled condition raised no banner")
	}
	a.dismissWarning()
	for i := 0; i < 60; i++ {
		a.SetLiveWarning(msg)
		a.syncLive()
		if a.warningVisible() {
			t.Fatalf("the banner came back %d frames after being dismissed", i+1)
		}
	}
	// Clearing and re-raising the same condition is a new occurrence
	// even without a gap: the app layer said it was over.
	a.SetLiveWarning("")
	a.syncLive()
	a.SetLiveWarning(msg)
	a.syncLive()
	if !a.warningVisible() {
		t.Error("the condition returning after being cleared raised no banner")
	}
}
