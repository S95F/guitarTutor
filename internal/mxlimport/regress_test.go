package mxlimport

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/fretting"
	"github.com/S95F/musicTutor/internal/score"
)

// findWarn returns the warnings containing sub.
func findWarn(warns []string, sub string) []string {
	var out []string
	for _, w := range warns {
		if strings.Contains(w, sub) {
			out = append(out, w)
		}
	}
	return out
}

// eventsByKey indexes events by derived MIDI key.
func eventsByKey(evs []score.NoteEvent) map[int]score.NoteEvent {
	out := map[int]score.NoteEvent{}
	for _, ev := range evs {
		out[ev.Key] = ev
	}
	return out
}

// TestTransposeAppliedToSoundingPitch: <pitch> is the WRITTEN pitch;
// <attributes><transpose> gives the shift to the pitch that SOUNDS. The
// octave-change -1 case is the one that matters — it is what MuseScore,
// Guitar Pro and Finale all emit for a guitar part notated in treble clef,
// so ignoring the element imports every note of an ordinary guitar file an
// octave sharp, with nothing to say so.
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
			step:      "E", octave: 3, wantKey: 40, // written E3 (52) sounds E2 (40)
		},
		{
			name:      "chromatic -2",
			transpose: `<transpose><diatonic>-1</diatonic><chromatic>-2</chromatic></transpose>`,
			step:      "E", octave: 3, wantKey: 50, // written E3 (52) sounds D3 (50)
		},
		{
			name:      "octave-change -1 plus double",
			transpose: `<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change><double/></transpose>`,
			step:      "E", octave: 4, wantKey: 40, // written E4 (64) sounds E2 (40)
		},
		{
			name:      "numbered for another staff does not apply",
			transpose: `<transpose number="2"><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change></transpose>`,
			step:      "E", octave: 3, wantKey: 52, // staff 2's shift must not leak onto staff 1
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

// TestTransposedFixtureMatchesCanonical is the end-to-end form of the
// same bug, on the shared cross-format fixture: the canonical riff
// re-notated in treble clef — every written octave one higher, with
// <transpose><octave-change>-1</octave-change> — is the SAME PIECE, and
// must import to the identical event table, fingerings and metadata.
// Before <transpose> was read it imported an octave sharp throughout.
func TestTransposedFixtureMatchesCanonical(t *testing.T) {
	data := readFixture(t, "fixture_riff.musicxml")
	// <tuning-octave> is sounding pitch and must not move; the literal
	// "<octave>" cannot match inside it.
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

	// With the <technical> fingerings stripped, pitch is all there is: the
	// written key drives the fretting heuristic and lands in the score
	// unfiltered, so an unread <transpose> puts every event an octave out.
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

// TestTransposeWithAuthoredFingering: a transposed part whose notes also
// carry <technical> must not report a written-vs-fingering mismatch — the
// fingering derives the SOUNDING pitch, which is what transpose produces.
func TestTransposeWithAuthoredFingering(t *testing.T) {
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>-1</octave-change></transpose></attributes>` +
		note("E", 3, 1920, 6, 0, "") // written E3, sounds E2 = string 6 fret 0
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

// TestMixedChordKeepsEveryNote: a chord mixing an authored <technical>
// note with an unfingered one must keep both. The unfingered A2's cheapest
// position is the open 5th string, which the authored B2 already holds; the
// merge used to truncate that collision to zero length and drop the note,
// so the player saw an incomplete chord and was then scored on it.
func TestMixedChordKeepsEveryNote(t *testing.T) {
	m := attrs44div480 +
		note("B", 2, 1920, 5, 2, "") + // authored: string 5 fret 2, key 47
		note("A", 2, 1920, -1, 0, "<chord/>") // unfingered, key 45
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

// TestMixedChordInferredNoteJoinsAuthoredShape: the unfingered note of a
// mixed chord must be fingered where the authored notes put the hand. The
// chord here is authored at the 12th fret; key 67's cheapest position on
// its own is string 1 fret 3, and the heuristic — handed only the strings
// the authored notes left free, and nothing about the frets they hold —
// chose exactly that: a chord spanning nine frets, never dropped but not
// something anyone could play as written. With the authored positions
// passed through as context it lands on string 3 fret 12, inside the shape.
func TestMixedChordInferredNoteJoinsAuthoredShape(t *testing.T) {
	m := attrs44div480 +
		note("E", 3, 1920, 6, 12, "") + // authored: string 6 fret 12, key 52
		note("A", 3, 1920, 5, 12, "<chord/>") + // authored: string 5 fret 12, key 57
		note("G", 4, 1920, -1, 0, "<chord/>") // unfingered, key 67
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
			continue // open strings need no hand and do not count
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

// TestMixedChordMatrix sweeps a mixed two-note chord over every authored
// string, a range of authored frets, and a range of unfingered pitches,
// and asserts the properties the mixed-chord path exists to hold:
//
//   - both notes survive the import, or a warning names the one that did
//     not — no note is ever dropped in silence;
//   - the inferred note never lands on the authored note's string, which
//     is what the same-string truncation would turn into a lost note;
//   - when some free string offers a fret within reach of the authored
//     hand, the inferred note is fingered there rather than wherever the
//     fretboard is cheapest.
//
// The last one is the placement quality the authored positions buy: over
// this sweep's 811 chords it moves 225 into reach of the authored hand and
// none out of it.
func TestMixedChordMatrix(t *testing.T) {
	steps := []struct {
		name string
		semi int
	}{{"C", 0}, {"D", 2}, {"E", 4}, {"F", 5}, {"G", 7}, {"A", 9}, {"B", 11}}
	// inReach reports whether any string other than aStr can sound key
	// within MaxSpan of an authored note at aFret (an open string always
	// can: it needs no hand).
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
				continue // the authored pitch needs an accidental; skip it
			}
			for _, s := range steps {
				for oct := 2; oct <= 5; oct++ {
					key := 12*(oct+1) + s.semi
					if key == aKey {
						continue // unison: the same-string rules own that case
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

// TestMixedChordUnplaceableNoteWarns: when the authored notes leave the
// unfingered one genuinely nowhere to go, it is reported — never dropped
// in silence.
func TestMixedChordUnplaceableNoteWarns(t *testing.T) {
	m := attrs44div480 +
		note("E", 3, 1920, 6, 12, "") + // authored: string 6 fret 12
		note("E", 2, 1920, -1, 0, "<chord/>") // key 40: only ever string 6 fret 0
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

// TestPickupMeasureRightAligned: measure implicit="yes" is a pickup bar,
// which ENDS on the barline instead of starting on it. Laying it out from
// the start of a full bar puts every note in the piece on the wrong beat
// relative to the barlines, and the count-in then disagrees with the music.
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
		{2880, 3840, 40}, // the pickup quarter ends exactly on the barline
		{3840, 7680, 43}, // bar 1 beat 1 is where the notation says
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

// TestPickupMeasureTempoShifted: a tempo mark inside the pickup rides
// along with the notes it belongs to.
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

// TestTiedUnisonKeepsBothChains: two notes of the SAME pitch sounding at
// once on DIFFERENT strings — an open B against the 7th fret of the E
// string is the everyday example — are separate tie chains. A tie table
// keyed by pitch alone let one start overwrite the other, so one chain
// never resolved and its note kept the wrong duration.
func TestTiedUnisonKeepsBothChains(t *testing.T) {
	m1 := attrs44div480 +
		note("B", 3, 1920, 2, 0, `<tie type="start"/>`) + // open 2nd string, key 59
		note("B", 3, 1920, 3, 4, `<chord/><tie type="start"/>`) // 3rd string fret 4, key 59
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

// TestTieNumberDistinguishesChains: an unfingered unison has no authored
// string to tell its two simultaneous chains apart, so <tie number> has to
// — including when the stops arrive in the opposite order to the starts.
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

// TestUnpitchedNotesWarnOnce: a percussion staff is hundreds of
// <unpitched> notes. One warning each buries every warning that matters,
// so they are counted and reported once per part.
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

// TestBackwardRepeatExpanded: a repeated section is played twice. Parsing
// the repeat and discarding it imports a piece the score says is AAB as
// AB — a third of the music missing, with nothing to say so.
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

// TestVoltaEndingsExpanded: with 1st/2nd endings the second pass skips the
// first ending and plays the second.
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
	// Written 1,2,3 plays as 1,2,1,3: E, G, E, A.
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

// TestRepeatTimesHonoured: <repeat times="3"> plays the section 3 times.
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

// TestRepeatRestoresMeterAcrossPasses: a meter change INSIDE a repeated
// section must not leak into the section's second pass. Nothing in the
// file restates the earlier signature at the repeat sign — a performer
// simply returns to it — so the state at each measure has to be restored
// rather than carried forward, and the meter map has to agree with it or
// the bar layout and the notes part company.
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
	// Written 1,2,3 plays as 1,2,3,2,3 under 4/4, 4/4, 2/4, 4/4, 2/4.
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

// TestJumpDirectionsWarn: D.C./D.S./coda jumps are not followed — their
// semantics compose in ways no bounded reading of the markup settles — so
// the user is told the import is not the whole piece.
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

// TestUnbalancedVoltaNotExpanded: malformed repeat markup falls back to
// the written order WITH a warning rather than guessing — a half-right
// expansion would silently teach the wrong music.
func TestUnbalancedVoltaNotExpanded(t *testing.T) {
	doc := wrapMeasures(
		`<measure number="1"><barline location="left"><repeat direction="forward"/></barline>`+
			attrs44div480+note("E", 2, 1920, 6, 0, "")+`</measure>`,
		`<measure number="2"><barline location="left"><ending number="1" type="start"/></barline>`+
			note("G", 2, 1920, 6, 3, "")+`</measure>`, // the volta never closes
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

// TestNoRepeatMarkupIsSilent: a plain final barline is not repeat markup,
// so an ordinary file gains no warning from any of this.
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

// TestInferredFingeringPastMIDIRangeDropped: fretting places by pitch
// arithmetic with no MIDI ceiling of its own, so an extreme (but
// well-formed) capo can hand an inferred note a fingering sounding past
// MIDI 127. Validate rejects such a score outright, so before the fix one
// hostile note failed the whole import ("imported score failed
// validation"); it must instead be dropped with a warning, like every
// other unplayable note, and the sane notes must survive.
func TestInferredFingeringPastMIDIRangeDropped(t *testing.T) {
	// Round 2 note: the original vehicle was <capo>40</capo>, which the
	// importer now clamps to "no capo" — so the extreme here is a LEGAL
	// one-string tuning at G9 (127) under a +1 octave transposition. The
	// heuristic happily frets the written G9 twelve frets up (sounding
	// 139), and without the ceiling that one note fails the final
	// Validate and kills the whole import.
	m := `<attributes><divisions>480</divisions><time><beats>4</beats><beat-type>4</beat-type></time>` +
		`<transpose><diatonic>0</diatonic><chromatic>0</chromatic><octave-change>1</octave-change></transpose>` +
		`<staff-details><staff-lines>1</staff-lines>` +
		`<staff-tuning line="1"><tuning-step>G</tuning-step><tuning-octave>9</tuning-octave></staff-tuning>` +
		`</staff-details></attributes>` +
		// Written G8 (115) sounds 127: fret 0, the one playable pitch.
		`<note><pitch><step>G</step><octave>8</octave></pitch><duration>960</duration><voice>1</voice></note>` +
		// Written G9 (127) sounds 139: fret 12 on the G9 string — past MIDI 127.
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

// TestStaffTuningOutsideMIDIFallsBack: a <staff-tuning> whose open string
// sits outside MIDI 0-127 (a negative octave, or octave 9 past G9) used
// to be installed verbatim into Track.Tuning — round 1 made the notes
// derived from it drop, but the stored tuning itself stayed nonsense for
// the UI and synth to use, and \tuning refuses to write it, so the piece
// could never save. The staff is now rejected wholesale like gpimport's
// parseTuning does, warned, and the current (standard) tuning kept.
func TestStaffTuningOutsideMIDIFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		octave     string
	}{
		{name: "below MIDI 0", step: "C", octave: "-3"},  // key -24
		{name: "above MIDI 127", step: "B", octave: "9"}, // key 131
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

// TestOverflowingMeterNumeratorRejected: a <beats> value large enough
// that numerator*beat-ticks wraps int64 used to slide the measure start
// to a negative tick and slip past the MaxTicks extent check — the import
// "succeeded" with a track of zero bars and zero events and no
// explanation. It must error like any other measure past the score
// limit (TestHostileMeterScoreTooLong is the non-overflowing cousin).
func TestOverflowingMeterNumeratorRejected(t *testing.T) {
	m1 := `<attributes><divisions>480</divisions><time><beats>18014398509481984</beats><beat-type>4</beat-type></time></attributes>`
	m2 := `<note><pitch><step>E</step><octave>2</octave></pitch><duration>1920</duration><voice>1</voice></note>`
	_, _, err := Import(wrap(m1, m2))
	if err == nil || !strings.Contains(err.Error(), "score too long") {
		t.Fatalf("err = %v, want a score-too-long error", err)
	}
}
