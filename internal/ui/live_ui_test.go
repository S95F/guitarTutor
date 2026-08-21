package ui

import (
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/practice"
)

const splitDeviceMsg = "capture and playback are different devices (Microphone Array (Intel Smart Sound Technology for Digital Microphones) / Speakers (Realtek(R) Audio)): their clocks drift apart over a session and timing scores wander"

func warnHintH() float64 { return lineHeightOf(faceOf(srcBody, fontBody)) }

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

		x := centreX(s, l.inner.x, l.inner.w)
		if x < l.inner.x || x+w > l.inner.x+l.inner.w {
			t.Errorf("line %d is drawn %.0f..%.0f, outside the interior %.0f..%.0f",
				i, x, x+w, l.inner.x, l.inner.x+l.inner.w)
		}
	}

	if got := strings.Join(l.lines, " "); got != splitDeviceMsg {
		t.Errorf("wrapping changed the message:\n got %q\nwant %q", got, splitDeviceMsg)
	}
}

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

		if bottom := l.hintY + warnHintH(); bottom > ptWarnY+ptWarnH-warnBorderW {
			t.Errorf("%.20q: the hint's descenders reach %.2f, on the border at %.0f",
				msg, bottom, float64(ptWarnY+ptWarnH)-warnBorderW/2)
		}
		if l.msgY < ptWarnY+warnBorderW {
			t.Errorf("%.20q: the message starts at %.2f, on the top border", msg, l.msgY)
		}
	}
}

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

func TestTunerIdleLineIsHonest(t *testing.T) {
	a := newApp(t, 1)

	s, scale := a.tunerIdleLine()
	if strings.Contains(s, "settings") || !strings.Contains(s, "-listen") {
		t.Errorf("with no live input and no settings screen the tuner says %q, want the command-line remedy", s)
	}
	if w := textWScaled(s, scale); w > screenW {
		t.Errorf("the idle line is %.0f px wide at scale %v, past the %d px window", w, scale, screenW)
	}
	a.SetSettingsOpener(func() {})
	if s, _ := a.tunerIdleLine(); !strings.Contains(s, "settings") {
		t.Errorf("with settings wired the tuner says %q, want it to point at settings", s)
	}

	a.SetLiveStatus(func() (float64, int64) { return -20, 0 })
	a.syncLive()
	if s, _ := a.tunerIdleLine(); s != "listening..." {
		t.Errorf("a live session's idle tuner says %q, want %q", s, "listening...")
	}
}

func TestDismissedWarningReturnsForANewOccurrence(t *testing.T) {
	a := newApp(t, 1)
	const msg = "could not re-open the piece: truncated archive"

	a.SetLiveWarning(msg)
	a.syncLive()
	if !a.warningVisible() {
		t.Fatal("the first failure raised no banner")
	}
	a.dismissWarning()

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

	a.SetLiveWarning("")
	a.syncLive()
	a.SetLiveWarning(msg)
	a.syncLive()
	if !a.warningVisible() {
		t.Error("the condition returning after being cleared raised no banner")
	}
}
