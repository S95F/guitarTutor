package mxlimport

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/fretting"
	"github.com/S95F/musicTutor/internal/score"
)

func findWarn(warns []string, sub string) []string {
	var out []string
	for _, w := range warns {
		if strings.Contains(w, sub) {
			out = append(out, w)
		}
	}
	return out
}

func eventsByKey(evs []score.NoteEvent) map[int]score.NoteEvent {
	out := map[int]score.NoteEvent{}
	for _, ev := range evs {
		out[ev.Key] = ev
	}
	return out
}

func TestTransposeAppliedToSoundingPitch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transpose string
		step      string
		octave    int
		wantKey   int
	}{
		{
			name:      "octave-change -1 (treble-clef guitar)",
			transpose: `<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change></transpose>`,
			step:      "E", octave: 3, wantKey: 40,
		},
		{
			name:      "chromatic -2",
			transpose: `<transpose><diatonic>-1</diatonic><chromatic>-2</chromatic></transpose>`,
			step:      "E", octave: 3, wantKey: 50,
		},
		{
			name:      "octave-change -1 plus double",
			transpose: `<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change><double/></transpose>`,
			step:      "E", octave: 4, wantKey: 40,
		},
		{
			name:      "numbered for another staff does not apply",
			transpose: `<transpose number="2"><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change></transpose>`,
			step:      "E", octave: 3, wantKey: 52,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
				tc.transpose + `</attributes>` +
				note(tc.step, tc.octave, 1920, -1, 0, "")
			s, warns, err := Import(wrap(m))
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if len(warns) != 0 {
				t.Errorf("warnings = %v, want none", warns)
			}
			evs := s.Events()
			if len(evs) != 1 {
				t.Fatalf("got %d events %v, want 1", len(evs), evs)
			}
			if evs[0].Key != tc.wantKey {
				t.Errorf("sounding key = %d, want %d (written pitch imported unshifted?)", evs[0].Key, tc.wantKey)
			}
		})
	}
}

func TestTransposedFixtureMatchesCanonical(t *testing.T) {
	data := readFixture(t, "fixture_riff.musicxml")

	up := regexp.MustCompile(`<octave>(\d)</octave>`).ReplaceAllStringFunc(string(data),
		func(m string) string {
			d := m[len("<octave>")]
			return "<octave>" + string(d+1) + "</octave>"
		})
	if up == string(data) {
		t.Fatal("raising the written octave changed nothing; fixture or regexp broken")
	}
	transposed := strings.Replace(up, "</attributes>",
		`<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change></transpose></attributes>`, 1)
	if transposed == up {
		t.Fatal("inserting <transpose> changed nothing; fixture or marker broken")
	}
	t.Run("authored fingerings", func(t *testing.T) {
		s, warns, err := Import([]byte(transposed))
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		checkCanonical(t, s, warns)
	})

	t.Run("inferred fingerings", func(t *testing.T) {
		stripped := regexp.MustCompile(`(?s)<notations>.*?</notations>`).ReplaceAllString(transposed, "")
		s, warns, err := Import([]byte(stripped))
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		if len(warns) != 0 {
			t.Errorf("warnings = %v, want none", warns)
		}
		evs := s.Events()
		if len(evs) != len(canonical) {
			t.Fatalf("got %d events, want %d", len(evs), len(canonical))
		}
		for i, want := range canonical {
			if evs[i].Start != want.start || evs[i].End != want.end || evs[i].Key != want.key {
				t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
					i, evs[i].Start, evs[i].End, evs[i].Key, want.start, want.end, want.key)
			}
		}
	})
}

func TestTransposeWithAuthoredFingering(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change></transpose></attributes>` +
		note("E", 3, 1920, 6, 0, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := findWarn(warns, "disagrees with tuning"); len(got) != 0 {
		t.Errorf("warnings = %v, want no pitch/fingering mismatch", got)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 40 || evs[0].String != 6 || evs[0].Fret != 0 {
		t.Fatalf("events = %v, want one key-40 note on string 6 fret 0", evs)
	}
}

func TestMixedChordKeepsEveryNote(t *testing.T) {
	m := attrs44div480 +
		note("B", 2, 1920, 5, 2, "") +
		note("A", 2, 1920, -1, 0, "<chord/>")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	evs := s.Events()
	if len(evs) != 2 {
		t.Fatalf("got %d events %v, want both chord notes", len(evs), evs)
	}
	byKey := eventsByKey(evs)
	authored, ok := byKey[47]
	if !ok {
		t.Fatal("the authored chord note (key 47) is missing")
	}
	if authored.String != 5 || authored.Fret != 2 {
		t.Errorf("authored note = string %d fret %d, want string 5 fret 2", authored.String, authored.Fret)
	}
	inferred, ok := byKey[45]
	if !ok {
		t.Fatal("the unfingered chord note (key 45) was dropped")
	}
	if inferred.String == 5 {
		t.Error("the inferred note landed on string 5, which the authored note already holds")
	}
	if inferred.String != 6 || inferred.Fret != 5 {
		t.Errorf("inferred note = string %d fret %d, want the free string 6 fret 5", inferred.String, inferred.Fret)
	}
	for _, ev := range evs {
		if ev.Start != 0 || ev.End != 3840 {
			t.Errorf("event %v does not span the whole bar; the chord was split", ev)
		}
	}
}

func TestMixedChordInferredNoteJoinsAuthoredShape(t *testing.T) {
	m := attrs44div480 +
		note("E", 3, 1920, 6, 12, "") +
		note("A", 3, 1920, 5, 12, "<chord/>") +
		note("G", 4, 1920, -1, 0, "<chord/>")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	evs := s.Events()
	if len(evs) != 3 {
		t.Fatalf("got %d events %v, want all three chord notes", len(evs), evs)
	}
	byKey := eventsByKey(evs)
	inferred, ok := byKey[67]
	if !ok {
		t.Fatal("the unfingered chord note (key 67) was dropped")
	}
	if inferred.String != 3 || inferred.Fret != 12 {
		t.Errorf("inferred note = string %d fret %d, want string 3 fret 12, in the authored shape",
			inferred.String, inferred.Fret)
	}
	minF, maxF := 0, 0
	for _, ev := range evs {
		if ev.Fret == 0 {
			continue
		}
		if minF == 0 || ev.Fret < minF {
			minF = ev.Fret
		}
		if ev.Fret > maxF {
			maxF = ev.Fret
		}
	}
	if maxF-minF > fretting.MaxSpan {
		t.Errorf("chord spans frets %d-%d, past MaxSpan %d: one hand cannot hold it",
			minF, maxF, fretting.MaxSpan)
	}
}

func TestMixedChordMatrix(t *testing.T) {
	steps := []struct {
		name string
		semi int
	}{{"C", 0}, {"D", 2}, {"E", 4}, {"F", 5}, {"G", 7}, {"A", 9}, {"B", 11}}

	inReach := func(aStr, aFret, key int) bool {
		for s := 1; s <= len(score.StandardTuning); s++ {
			fret := key - score.StandardTuning[s-1]
			switch {
			case s == aStr || fret < 0 || fret > fretting.MaxFret:
			case fret == 0:
				return true
			case fret-aFret <= fretting.MaxSpan && aFret-fret <= fretting.MaxSpan:
				return true
			}
		}
		return false
	}
	for aStr := 1; aStr <= 6; aStr++ {
		for aFret := 2; aFret <= 20; aFret += 2 {
			aKey := score.StandardTuning[aStr-1] + aFret
			aStep, aOct := "", -1
			for _, s := range steps {
				if (aKey-s.semi)%12 == 0 {
					aStep, aOct = s.name, (aKey-s.semi)/12-1
				}
			}
			if aStep == "" || aOct < 0 || aOct > 9 {
				continue
			}
			for _, s := range steps {
				for oct := 2; oct <= 5; oct++ {
					key := 12*(oct+1) + s.semi
					if key == aKey {
						continue
					}
					m := attrs44div480 +
						note(aStep, aOct, 1920, aStr, aFret, "") +
						note(s.name, oct, 1920, -1, 0, "<chord/>")
					sc, warns, err := Import(wrap(m))
					if err != nil {
						t.Fatalf("Import(%s%d on %d/%d + %s%d): %v", aStep, aOct, aStr, aFret, s.name, oct, err)
					}
					where := fmt.Sprintf("authored %s%d on string %d fret %d + unfingered key %d",
						aStep, aOct, aStr, aFret, key)
					byKey := eventsByKey(sc.Events())
					if _, ok := byKey[aKey]; !ok {
						t.Fatalf("%s: the AUTHORED note vanished; warnings %v", where, warns)
					}
					ev, ok := byKey[key]
					if !ok {
						if len(findWarn(warns, fmt.Sprintf("key %d", key))) == 0 {
							t.Fatalf("%s: the note vanished with no warning naming it; warnings %v", where, warns)
						}
						continue
					}
					if ev.String == aStr {
						t.Fatalf("%s: landed on the authored string at fret %d", where, ev.Fret)
					}
					if ev.Fret == 0 || !inReach(aStr, aFret, key) {
						continue
					}
					if d := ev.Fret - aFret; d > fretting.MaxSpan || -d > fretting.MaxSpan {
						t.Errorf("%s: fingered at string %d fret %d, %d frets from the authored hand, "+
							"though a string within MaxSpan was free", where, ev.String, ev.Fret, d)
					}
				}
			}
		}
	}
}

func TestMixedChordUnplaceableNoteWarns(t *testing.T) {
	m := attrs44div480 +
		note("E", 3, 1920, 6, 12, "") +
		note("E", 2, 1920, -1, 0, "<chord/>")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(findWarn(warns, "key 40")) == 0 {
		t.Errorf("warnings = %v, want one naming the note that found no string", warns)
	}
	if evs := s.Events(); len(evs) != 1 || evs[0].Key != 52 {
		t.Fatalf("events = %v, want only the authored note", evs)
	}
}

func TestPickupMeasureRightAligned(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="0" implicit="yes">`+attrs44div480+note("E", 2, 480, 6, 0, "")+`</measure>`,
		`<measure number="1">`+note("G", 2, 1920, 6, 3, "")+`</measure>`,
	)
	s, warns, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(findWarn(warns, "pickup")) == 0 {
		t.Errorf("warnings = %v, want one naming the pickup measure", warns)
	}
	if got := findWarn(warns, "does not fill"); len(got) != 0 {
		t.Errorf("warnings = %v; a pickup is underfull by definition", got)
	}
	evs := s.Events()
	want := []struct {
		start, end int64
		key        int
	}{
		{2880, 3840, 40},
		{3840, 7680, 43},
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events %v, want %d", len(evs), evs, len(want))
	}
	for i, w := range want {
		if evs[i].Start != w.start || evs[i].End != w.end || evs[i].Key != w.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, evs[i].Start, evs[i].End, evs[i].Key, w.start, w.end, w.key)
		}
	}
	if got := s.End(); got != 7680 {
		t.Errorf("score end = %d, want 7680 (two full 4/4 bars)", got)
	}
}

func TestPickupMeasureTempoShifted(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="0" implicit="yes">`+attrs44div480+
			`<direction><sound tempo="90"/></direction>`+note("E", 2, 480, 6, 0, "")+`</measure>`,
		`<measure number="1">`+note("G", 2, 1920, 6, 3, "")+`</measure>`,
	)
	s, _, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	found := false
	for _, tp := range s.Tempos {
		if tp.Tick == 2880 && tp.USPerQuarter == score.USPerQuarter(90) {
			found = true
		}
	}
	if !found {
		t.Errorf("Tempos = %v, want the pickup's 90 BPM mark at tick 2880", s.Tempos)
	}
}

func TestTiedUnisonKeepsBothChains(t *testing.T) {
	m1 := attrs44div480 +
		note("B", 3, 1920, 2, 0, `<tie type="start"/>`) +
		note("B", 3, 1920, 3, 4, `<chord/><tie type="start"/>`)
	m2 := note("B", 3, 1920, 2, 0, `<tie type="stop"/>`) +
		note("B", 3, 1920, 3, 4, `<chord/><tie type="stop"/>`)
	s, warns, err := Import(wrap(m1, m2))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none (both ties resolve)", warns)
	}
	evs := s.Events()
	if len(evs) != 2 {
		t.Fatalf("got %d events %v, want 2 merged unison chains", len(evs), evs)
	}
	strs := map[int]bool{}
	for _, ev := range evs {
		if ev.Key != 59 {
			t.Errorf("event %v: want key 59", ev)
		}
		if ev.Start != 0 || ev.End != 7680 {
			t.Errorf("event on string %d = (%d,%d), want (0,7680): its tie never resolved",
				ev.String, ev.Start, ev.End)
		}
		strs[ev.String] = true
	}
	if !strs[2] || !strs[3] {
		t.Errorf("events sit on strings %v, want one on string 2 and one on string 3", strs)
	}
}

func TestTieNumberDistinguishesChains(t *testing.T) {
	m1 := attrs44div480 +
		note("B", 3, 1920, -1, 0, `<tie type="start" number="1"/>`) +
		note("B", 3, 1920, -1, 0, `<chord/><tie type="start" number="2"/>`)
	m2 := note("B", 3, 1920, -1, 0, `<tie type="stop" number="2"/>`) +
		note("B", 3, 1920, -1, 0, `<chord/><tie type="stop" number="1"/>`)
	s, warns, err := Import(wrap(m1, m2))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := findWarn(warns, "tie"); len(got) != 0 {
		t.Errorf("warnings = %v, want both numbered ties to resolve", got)
	}
	evs := s.Events()
	if len(evs) != 2 {
		t.Fatalf("got %d events %v, want 2", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.Start != 0 || ev.End != 7680 {
			t.Errorf("event %v = (%d,%d), want (0,7680)", ev, ev.Start, ev.End)
		}
	}
}

func TestUnpitchedNotesWarnOnce(t *testing.T) {
	un := `<note><unpitched><display-step>C</display-step><display-octave>5</display-octave></unpitched>` +
		`<duration>480</duration><voice>1</voice></note>`
	m := attrs44div480 + un + un + un + note("E", 2, 480, 6, 0, "")
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	got := findWarn(warns, "unpitched")
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want exactly one coalesced <unpitched> warning", warns)
	}
	if !strings.Contains(got[0], "3") {
		t.Errorf("warning = %q, want the count of skipped notes", got[0])
	}
	if evs := s.Events(); len(evs) != 1 || evs[0].Key != 40 {
		t.Fatalf("events = %v, want only the pitched note", evs)
	}
}

func TestBackwardRepeatExpanded(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1">`+attrs44div480+note("E", 2, 1920, 6, 0, "")+
			`<barline location="right"><repeat direction="backward"/></barline></measure>`,
		`<measure number="2">`+note("G", 2, 1920, 6, 3, "")+`</measure>`,
	)
	s, warns, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(findWarn(warns, "expanded")) == 0 {
		t.Errorf("warnings = %v, want one reporting the expansion", warns)
	}
	want := []struct {
		start, end int64
		key        int
	}{{0, 3840, 40}, {3840, 7680, 40}, {7680, 11520, 43}}
	evs := s.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events %v, want %d (measure 1 played twice)", len(evs), evs, len(want))
	}
	for i, w := range want {
		if evs[i].Start != w.start || evs[i].End != w.end || evs[i].Key != w.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, evs[i].Start, evs[i].End, evs[i].Key, w.start, w.end, w.key)
		}
	}
}

func TestVoltaEndingsExpanded(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1"><barline location="left"><repeat direction="forward"/></barline>`+
			attrs44div480+note("E", 2, 1920, 6, 0, "")+`</measure>`,
		`<measure number="2"><barline location="left"><ending number="1" type="start"/></barline>`+
			note("G", 2, 1920, 6, 3, "")+
			`<barline location="right"><ending number="1" type="stop"/><repeat direction="backward"/></barline></measure>`,
		`<measure number="3"><barline location="left"><ending number="2" type="start"/></barline>`+
			note("A", 2, 1920, 5, 0, "")+
			`<barline location="right"><ending number="2" type="discontinue"/></barline></measure>`,
	)
	s, warns, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(findWarn(warns, "expanded")) == 0 {
		t.Errorf("warnings = %v, want one reporting the expansion", warns)
	}

	wantKeys := []int{40, 43, 40, 45}
	evs := s.Events()
	if len(evs) != len(wantKeys) {
		t.Fatalf("got %d events %v, want %d", len(evs), evs, len(wantKeys))
	}
	for i, k := range wantKeys {
		if evs[i].Key != k {
			t.Errorf("event %d key = %d, want %d (volta order wrong)", i, evs[i].Key, k)
		}
		if want := int64(i) * 3840; evs[i].Start != want {
			t.Errorf("event %d start = %d, want %d", i, evs[i].Start, want)
		}
	}
}

func TestRepeatTimesHonoured(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1">` + attrs44div480 + note("E", 2, 1920, 6, 0, "") +
			`<barline location="right"><repeat direction="backward" times="3"/></barline></measure>`,
	)
	s, _, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if evs := s.Events(); len(evs) != 3 {
		t.Fatalf("got %d events %v, want 3 passes", len(evs), evs)
	}
}

func TestRepeatRestoresMeterAcrossPasses(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1">`+attrs44div480+note("E", 2, 1920, 6, 0, "")+`</measure>`,
		`<measure number="2"><barline location="left"><repeat direction="forward"/></barline>`+
			note("G", 2, 1920, 6, 3, "")+`</measure>`,
		`<measure number="3"><attributes><time><beats>2</beats><beat-type>4</beat-type></time></attributes>`+
			note("A", 2, 960, 5, 0, "")+
			`<barline location="right"><repeat direction="backward"/></barline></measure>`,
	)
	s, _, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	want := []struct {
		start, end int64
		key        int
	}{
		{0, 3840, 40}, {3840, 7680, 43}, {7680, 9600, 45},
		{9600, 13440, 43}, {13440, 15360, 45},
	}
	evs := s.Events()
	if len(evs) != len(want) {
		t.Fatalf("got %d events %v, want %d", len(evs), evs, len(want))
	}
	for i, w := range want {
		if evs[i].Start != w.start || evs[i].End != w.end || evs[i].Key != w.key {
			t.Errorf("event %d = (%d,%d,key %d), want (%d,%d,key %d)",
				i, evs[i].Start, evs[i].End, evs[i].Key, w.start, w.end, w.key)
		}
	}
	wantMeters := score.MeterMap{
		{Tick: 0, Num: 4, Den: 4}, {Tick: 7680, Num: 2, Den: 4},
		{Tick: 9600, Num: 4, Den: 4}, {Tick: 13440, Num: 2, Den: 4},
	}
	if len(s.Meters) != len(wantMeters) {
		t.Fatalf("Meters = %v, want %v", s.Meters, wantMeters)
	}
	for i, w := range wantMeters {
		if s.Meters[i] != w {
			t.Errorf("meter %d = %v, want %v", i, s.Meters[i], w)
		}
	}
	if got := s.End(); got != 15360 {
		t.Errorf("score end = %d, want 15360", got)
	}
}

func TestJumpDirectionsWarn(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1">`+attrs44div480+note("E", 2, 1920, 6, 0, "")+`</measure>`,
		`<measure number="2">`+note("G", 2, 1920, 6, 3, "")+`<sound dacapo="yes"/></measure>`,
	)
	_, warns, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	got := findWarn(warns, "D.C.")
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want exactly one jump-direction warning", warns)
	}
	if !strings.Contains(got[0], "not followed") {
		t.Errorf("warning = %q, want it to say the jump is not followed", got[0])
	}
}

func TestUnbalancedVoltaNotExpanded(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1"><barline location="left"><repeat direction="forward"/></barline>`+
			attrs44div480+note("E", 2, 1920, 6, 0, "")+`</measure>`,
		`<measure number="2"><barline location="left"><ending number="1" type="start"/></barline>`+
			note("G", 2, 1920, 6, 3, "")+`</measure>`,
	)
	s, warns, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(findWarn(warns, "not expanded")) == 0 {
		t.Errorf("warnings = %v, want one saying the repeats were not expanded", warns)
	}
	if evs := s.Events(); len(evs) != 2 {
		t.Fatalf("got %d events %v, want the 2 written measures", len(evs), evs)
	}
}

func TestNoRepeatMarkupIsSilent(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1">` + attrs44div480 + note("E", 2, 1920, 6, 0, "") +
			`<barline location="right"><bar-style>light-heavy</bar-style></barline></measure>`,
	)
	_, warns, err := Import(doc)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
}

func TestInferredFingeringPastMIDIRangeDropped(t *testing.T) {

	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>1</octave-change></transpose>` +
		`<staff-details><staff-lines>1</staff-lines>` +
		`<staff-tuning line="1"><tuning-step>G</tuning-step><tuning-octave>9</tuning-octave></staff-tuning>` +
		`</staff-details></attributes>` +

		`<note><pitch><step>G</step><octave>8</octave></pitch><duration>960</duration><voice>1</voice></note>` +

		`<note><pitch><step>G</step><octave>9</octave></pitch><duration>960</duration><voice>1</voice></note>`
	s, warns, err := Import(wrap(m))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(findWarn(warns, "outside 0-127")) != 1 {
		t.Errorf("warnings = %v, want exactly one about the out-of-MIDI note", warns)
	}
	evs := s.Events()
	if len(evs) != 1 || evs[0].Key != 127 {
		t.Fatalf("events = %+v, want only the playable sounding-127 note", evs)
	}
}

func TestStaffTuningOutsideMIDIFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		octave     string
	}{
		{name: "below MIDI 0", step: "C", octave: "-3"},
		{name: "above MIDI 127", step: "B", octave: "9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
				`<staff-details><staff-lines>1</staff-lines>` +
				`<staff-tuning line="1"><tuning-step>` + tc.step + `</tuning-step><tuning-octave>` + tc.octave + `</tuning-octave></staff-tuning>` +
				`</staff-details></attributes>` +
				note("E", 2, 1920, -1, 0, "")
			s, warns, err := Import(wrap(m))
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if len(findWarn(warns, "outside 0-127")) != 1 || len(findWarn(warns, "keeping the current tuning")) != 1 {
				t.Errorf("warnings = %v, want the staff tuning rejected and the current tuning kept", warns)
			}
			tr := s.Tracks[0]
			if !tr.Tuning.Equal(score.StandardTuning) {
				t.Fatalf("Tuning = %v, want the standard fallback", tr.Tuning)
			}
			evs := s.Events()
			if len(evs) != 1 || evs[0].Key != 40 {
				t.Fatalf("events = %+v, want the E2 fingered on the fallback tuning", evs)
			}
		})
	}
}

func TestOverflowingMeterNumeratorRejected(t *testing.T) {
	m1 := `<attributes><divisions>480</divisions><time><beats>18014398509481984</beats><beat-type>4</beat-type></time></attributes>`
	m2 := `<note><pitch><step>E</step><octave>2</octave></pitch><duration>1920</duration><voice>1</voice></note>`
	_, _, err := Import(wrap(m1, m2))
	if err == nil || !strings.Contains(err.Error(), "score too long") {
		t.Fatalf("err = %v, want a score-too-long error", err)
	}
}
