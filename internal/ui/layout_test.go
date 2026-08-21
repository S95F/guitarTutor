package ui

import "testing"

type band struct {
	name   string
	top    float64
	bottom float64
}

func practiceBands(a *App) []band {
	l := a.layout()
	tab := l.tab
	return []band{
		{"header", uiHeaderY, uiHeaderH - 10},
		{"transport row", l.transport[0].y, l.transport[0].y + l.transport[0].h},
		{"track strip", ptTracksY, ptTracksY + chipH + uiLineH},
		{"tab", tab.y, tab.y + tab.h},
		{"state chip", a.stateChipY(), a.stateChipY() + uiTextH*1.5},
		{"position caption", a.captionY(), a.captionY() + uiTextH},
		{"timeline", a.timelineY() - 4, a.timelineY() + ptTimelineH + 8},
		{"legend", a.legendY(), a.legendY() + uiTextH},
		{"messages", a.msgY(), a.msgY() + 20 + uiTextH},
		{"footer", uiFooterY, uiFooterY + uiTextH},
	}
}

func TestWarningBannerCoversOnlyTheNotation(t *testing.T) {
	for _, strings := range []int{4, 6, 8} {
		trackStripBottom := ptTracksY + chipH + uiLineH
		if ptWarnY < trackStripBottom {
			t.Errorf("%d strings: the banner starts at %.0f, over the track strip ending at %.0f",
				strings, float64(ptWarnY), trackStripBottom)
		}
		if bottom := float64(ptWarnY + ptWarnH); bottom > tabTop {
			t.Errorf("%d strings: the banner ends at %.0f, over the first string at %d",
				strings, bottom, tabTop)
		}
	}
}

func TestPracticeBandsDoNotOverlap(t *testing.T) {
	a := newApp(t, 8)
	bands := practiceBands(a)
	for i := 1; i < len(bands); i++ {
		prev, cur := bands[i-1], bands[i]
		if cur.top < prev.bottom {
			t.Errorf("%q (ends %.0f) overlaps %q (starts %.0f)",
				prev.name, prev.bottom, cur.name, cur.top)
		}
	}
	last := bands[len(bands)-1]
	if last.bottom > screenH {
		t.Errorf("%q ends at %.0f, past the %d px window", last.name, last.bottom, screenH)
	}
	if bands[0].top < 0 {
		t.Errorf("%q starts above the window at %.0f", bands[0].name, bands[0].top)
	}
}

func TestTransportRowFitsTheWindow(t *testing.T) {
	a := newApp(t, 4)
	l := a.layout()
	last := l.chips[len(l.chips)-1]
	if right := last.x + last.w; right > screenW-uiPadX {
		t.Errorf("the transport row ends at %.0f, past the %.0f margin", right, screenW-uiPadX)
	}

	prev := l.transport[0]
	for i, r := range append(l.transport[1:], l.chips...) {
		if r.x < prev.x+prev.w {
			t.Errorf("transport item %d starts at %.0f, inside the one ending at %.0f", i, r.x, prev.x+prev.w)
		}
		prev = r
	}
}

func TestTransportControlsShareOneRow(t *testing.T) {
	a := newApp(t, 4)
	l := a.layout()
	for i, r := range l.transport {
		if r.y != ptTransportY {
			t.Errorf("transport button %d sits at y %.0f, want %.0f", i, r.y, ptTransportY)
		}
	}
	for i, r := range l.chips {
		if r.y != ptTransportY {
			t.Errorf("chip %d sits at y %.0f, want %.0f", i, r.y, ptTransportY)
		}
	}
}

func TestTabStaysInsideTheWindowForAWideNeck(t *testing.T) {
	for _, strings := range []int{4, 6, 7, 8} {
		a := newAppStrings(t, strings)
		bands := practiceBands(a)
		for i := 1; i < len(bands); i++ {
			if bands[i].top < bands[i-1].bottom {
				t.Errorf("%d strings: %q overlaps %q", strings, bands[i-1].name, bands[i].name)
			}
		}
		tab := a.layout().tab
		if tab.y+tab.h > a.captionY() {
			t.Errorf("%d strings: the tab ends at %.0f, under the timeline caption at %.0f",
				strings, tab.y+tab.h, a.captionY())
		}
	}
}

func TestBPMEntryBoxSitsOverTheTab(t *testing.T) {
	a := newApp(t, 4)
	r := bpmEntryRect()
	tab := a.layout().tab
	if r.x < 0 || r.x+r.w > screenW || r.y < 0 || r.y+r.h > screenH {
		t.Errorf("the tempo box %+v is not fully inside the window", r)
	}
	if r.y < tab.y || r.y > tab.y+tab.h {
		t.Errorf("the tempo box starts at y %.0f, outside the tab band %.0f..%.0f", r.y, tab.y, tab.y+tab.h)
	}
}

func TestTimelineSpansThePageMargins(t *testing.T) {
	a := newApp(t, 4)
	tl := a.layout().timeline
	if tl.x != uiPadX || tl.x+tl.w != screenW-uiPadX {
		t.Errorf("timeline spans %.0f..%.0f, want %.0f..%.0f", tl.x, tl.x+tl.w, uiPadX, screenW-uiPadX)
	}
}

func newAppStrings(t *testing.T, n int) *App {
	t.Helper()
	a := newApp(t, 4)
	tuning := make([]int, n)
	for i := range tuning {
		tuning[i] = 40 + 5*i
	}
	tr := a.displayed()
	tr.Tuning = tuning

	for _, bar := range tr.Bars {
		for _, beat := range bar.Beats {
			for i := range beat.Notes {
				if beat.Notes[i].String > n {
					beat.Notes[i].String = n
				}
			}
		}
	}
	return a
}

func TestPracticeBandsAreReportedInOrder(t *testing.T) {
	bands := practiceBands(newApp(t, 4))
	for i, b := range bands {
		if b.bottom < b.top {
			t.Errorf("band %d (%q) is inverted: %.0f..%.0f", i, b.name, b.top, b.bottom)
		}
	}
	if len(bands) < 10 {
		t.Fatalf("only %d bands listed; the check is meant to cover the whole view", len(bands))
	}
}
