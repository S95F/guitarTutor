package edit

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func saveable(t *testing.T, d *Doc) string {
	t.Helper()
	if err := d.Score().Validate(); err != nil {
		t.Fatalf("the piece no longer validates: %v", err)
	}
	src, err := textfmt.Format(d.Score())
	if err != nil {
		t.Fatalf("the piece can no longer be written: %v", err)
	}
	if _, err := textfmt.Parse(src, "test"); err != nil {
		t.Fatalf("what the piece writes as no longer parses: %v\n%s", err, src)
	}
	return string(src)
}

func barFull(t *testing.T, d *Doc) {
	t.Helper()
	for ti, tr := range d.Score().Tracks {
		for bi, bar := range tr.Bars {
			var sum int64
			for _, bt := range bar.Beats {
				sum += bt.Dur
			}
			if sum != bar.Len() {
				t.Fatalf("track %d bar %d holds %d ticks, its %d/%d meter wants %d",
					ti+1, bi+1, sum, bar.Num, bar.Den, bar.Len())
			}
			if len(bar.Beats) == 0 {
				t.Fatalf("track %d bar %d has no beats; the cursor would have nowhere to be", ti+1, bi+1)
			}
		}
	}
}

func TestNewIsSaveable(t *testing.T) {
	d := New(NewOptions{Title: "Test"})
	if d.BarCount() != 4 {
		t.Errorf("got %d bars, want 4", d.BarCount())
	}
	barFull(t, d)
	src := saveable(t, d)
	if !strings.Contains(src, "\\title Test") {
		t.Errorf("the title is missing:\n%s", src)
	}

	if got, want := d.Cursor().Str, 6; got != want {
		t.Errorf("cursor starts on string %d, want %d", got, want)
	}
}

func TestNewDefaultsAreTheFormatsOwn(t *testing.T) {
	d := New(NewOptions{})
	m := d.Score().Meters.At(0)
	if m.Num != 4 || m.Den != 4 {
		t.Errorf("got %d/%d, want 4/4", m.Num, m.Den)
	}
	if got := d.TempoAtCursor(); got != textfmt.DefaultBPM {
		t.Errorf("got %g BPM, want %g", got, float64(textfmt.DefaultBPM))
	}
	if got := d.Track().Program; got != textfmt.DefaultProgram {
		t.Errorf("got program %d, want %d", got, textfmt.DefaultProgram)
	}
}

func TestTypeANote(t *testing.T) {
	d := New(NewOptions{})
	if err := d.SetFret(3); err != nil {
		t.Fatalf("SetFret: %v", err)
	}
	n, ok := d.NoteAt(6)
	if !ok || n.Fret != 3 {
		t.Fatalf("got %+v ok=%v, want fret 3 on string 6", n, ok)
	}
	barFull(t, d)
	saveable(t, d)

	if err := d.SetFret(5); err != nil {
		t.Fatalf("SetFret: %v", err)
	}
	if got := len(d.Beat().Notes); got != 1 {
		t.Errorf("the beat has %d notes, want 1", got)
	}
	if n, _ := d.NoteAt(6); n.Fret != 5 {
		t.Errorf("got fret %d, want 5", n.Fret)
	}
}

func TestTypeAChord(t *testing.T) {
	d := New(NewOptions{})
	for _, f := range []struct{ str, fret int }{{6, 0}, {5, 2}, {4, 2}} {
		d.GoTo(Cursor{Bar: 0, Beat: 0, Str: f.str})
		if err := d.SetFret(f.fret); err != nil {
			t.Fatalf("SetFret(%d) on string %d: %v", f.fret, f.str, err)
		}
	}
	if got := len(d.Beat().Notes); got != 3 {
		t.Fatalf("the chord has %d notes, want 3", got)
	}

	for i := 1; i < len(d.Beat().Notes); i++ {
		if d.Beat().Notes[i].String <= d.Beat().Notes[i-1].String {
			t.Errorf("the chord is not in string order: %+v", d.Beat().Notes)
		}
	}
	src := saveable(t, d)
	if !strings.Contains(src, "(2.4 2.5 0.6)") {
		t.Errorf("the chord did not write as expected:\n%s", src)
	}
}

func TestClearNoteLeavesARest(t *testing.T) {
	d := New(NewOptions{})
	if err := d.SetFret(3); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearNote(); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.NoteAt(6); ok {
		t.Error("the note survived being cleared")
	}
	barFull(t, d)
	saveable(t, d)
}

func TestClearNoteOnEmptyStringIsNotAnEdit(t *testing.T) {
	d := New(NewOptions{})
	if err := d.ClearNote(); err != nil {
		t.Fatalf("ClearNote on an empty beat: %v", err)
	}
	if d.Dirty() {
		t.Error("clearing nothing marked the piece dirty")
	}
	if d.CanUndo() {
		t.Error("clearing nothing pushed an undo snapshot")
	}

	if err := d.SetFret(3); err != nil {
		t.Fatal(err)
	}
	if !d.Undo() {
		t.Fatal("nothing to undo after SetFret")
	}
	if err := d.ClearNote(); err != nil {
		t.Fatal(err)
	}
	if !d.Redo() {
		t.Error("a no-op clear threw away the redo stack")
	}
	if n, ok := d.NoteAt(6); !ok || n.Fret != 3 {
		t.Errorf("redo after the no-op clear gave %+v ok=%v, want fret 3 back", n, ok)
	}
	barFull(t, d)
	saveable(t, d)
}

func TestSetDurationEatsThePadding(t *testing.T) {
	d := New(NewOptions{})
	if err := d.SetFret(0); err != nil {
		t.Fatal(err)
	}
	if err := d.SetDuration(score.Half); err != nil {
		t.Fatalf("SetDuration: %v", err)
	}
	if got := d.Beat().Dur; got != score.Half {
		t.Errorf("got %d ticks, want %d", got, score.Half)
	}
	barFull(t, d)

	if got := len(d.Bar().Beats); got != 2 {
		t.Errorf("the bar has %d beats, want 2 (the note and one rest)", got)
	}
	saveable(t, d)
}

func TestOverfullIsRefusedWhole(t *testing.T) {
	d := New(NewOptions{Num: 2, Den: 4, Bars: 1})

	if err := d.SetDuration(score.Quarter); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFret(0); err != nil {
		t.Fatal(err)
	}
	d.MoveBeat(1)
	if err := d.SetFret(2); err != nil {
		t.Fatal(err)
	}
	if got := len(d.Bar().Beats); got != 2 {
		t.Fatalf("the 2/4 bar has %d beats, want the 2 quarters this test is about", got)
	}
	before := saveable(t, d)
	d.MarkSaved()

	d.GoTo(Cursor{Bar: 0, Beat: 0, Str: 6})
	err := d.SetDuration(score.Whole)
	if err == nil {
		t.Fatal("a whole note was accepted into a 2/4 bar that already had two quarters")
	}
	if !strings.Contains(err.Error(), "does not fit") {
		t.Errorf("the refusal does not say what went wrong: %v", err)
	}
	if got := saveable(t, d); got != before {
		t.Errorf("the refused edit changed the piece\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}

	if d.Dirty() {
		t.Error("the refused edit marked the piece dirty")
	}

	d.Undo()
	d.GoTo(Cursor{Bar: 0, Beat: 1, Str: 6})
	if _, ok := d.NoteAt(6); ok {
		t.Error("undo after a refusal did not take back the last real edit")
	}
}

func TestUndoRedo(t *testing.T) {
	d := New(NewOptions{})
	empty := saveable(t, d)
	if d.CanUndo() {
		t.Error("a fresh piece has something to undo")
	}
	if err := d.SetFret(7); err != nil {
		t.Fatal(err)
	}
	typed := saveable(t, d)
	if typed == empty {
		t.Fatal("typing a note changed nothing")
	}
	if !d.Undo() {
		t.Fatal("Undo found nothing to take back")
	}
	if got := saveable(t, d); got != empty {
		t.Errorf("undo did not restore the piece\n--- want ---\n%s\n--- got ---\n%s", empty, got)
	}
	if !d.Redo() {
		t.Fatal("Redo found nothing to re-apply")
	}
	if got := saveable(t, d); got != typed {
		t.Errorf("redo did not re-apply the edit\n--- want ---\n%s\n--- got ---\n%s", typed, got)
	}

	d.Undo()
	if err := d.SetFret(9); err != nil {
		t.Fatal(err)
	}
	if d.CanRedo() {
		t.Error("a new edit left the abandoned redo branch in place")
	}
}

func TestUndoIsNotAliased(t *testing.T) {

	d := New(NewOptions{})
	if err := d.SetFret(1); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFret(2); err != nil {
		t.Fatal(err)
	}
	d.Undo()
	if n, _ := d.NoteAt(6); n.Fret != 1 {
		t.Fatalf("after one undo the fret is %d, want 1", n.Fret)
	}
	d.Undo()
	if _, ok := d.NoteAt(6); ok {
		t.Error("after undoing both edits there is still a note")
	}
}

func TestBarsMoveTogetherAcrossTracks(t *testing.T) {
	d := New(NewOptions{})
	if err := d.AddTrack("Rhythm", nil); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if got := len(d.Score().Tracks); got != 2 {
		t.Fatalf("got %d tracks, want 2", got)
	}
	before := d.BarCount()
	if err := d.AppendBar(); err != nil {
		t.Fatalf("AppendBar: %v", err)
	}
	for i, tr := range d.Score().Tracks {
		if got := len(tr.Bars); got != before+1 {
			t.Errorf("track %d has %d bars, want %d — the grid is not shared", i+1, got, before+1)
		}
	}
	barFull(t, d)
	saveable(t, d)
}

func TestMeterChangeFollowsItsBar(t *testing.T) {
	d := New(NewOptions{Bars: 4})
	d.GoTo(Cursor{Bar: 2, Str: 6})
	if err := d.SetMeter(3, 4); err != nil {
		t.Fatalf("SetMeter: %v", err)
	}
	bars := d.Score().Tracks[0].Bars
	for i, want := range []int{4, 4, 3, 3} {
		if bars[i].Num != want {
			t.Errorf("bar %d is %d/%d, want %d/4", i+1, bars[i].Num, bars[i].Den, want)
		}
	}
	barFull(t, d)
	saveable(t, d)

	d.GoTo(Cursor{Bar: 0, Str: 6})
	if err := d.InsertBar(); err != nil {
		t.Fatalf("InsertBar: %v", err)
	}
	bars = d.Score().Tracks[0].Bars
	for i, want := range []int{4, 4, 4, 3, 3} {
		if bars[i].Num != want {
			t.Errorf("after inserting, bar %d is %d/%d, want %d/4", i+1, bars[i].Num, bars[i].Den, want)
		}
	}
	saveable(t, d)
}

func TestTempoChangeFollowsItsBar(t *testing.T) {
	d := New(NewOptions{BPM: 100, Bars: 4})
	d.GoTo(Cursor{Bar: 2, Str: 6})
	if err := d.SetTempo(140); err != nil {
		t.Fatalf("SetTempo: %v", err)
	}

	tempoAt := func(bar int) int64 {
		return d.Score().Tempos.At(d.Score().Tracks[0].Bars[bar].Start)
	}
	bpm := func(v float64) int64 { return score.USPerQuarter(v) }
	for i, want := range []float64{100, 100, 140, 140} {
		if got := tempoAt(i); got != bpm(want) {
			t.Errorf("bar %d is at %g BPM, want %g", i+1, 60e6/float64(got), want)
		}
	}
	d.GoTo(Cursor{Bar: 0, Str: 6})
	if err := d.InsertBar(); err != nil {
		t.Fatalf("InsertBar: %v", err)
	}
	for i, want := range []float64{100, 100, 100, 140, 140} {
		if got := tempoAt(i); got != bpm(want) {
			t.Errorf("after inserting, bar %d is at %g BPM, want %g", i+1, 60e6/float64(got), want)
		}
	}
	saveable(t, d)

	d.GoTo(Cursor{Bar: 3, Str: 6})
	if err := d.DeleteBar(); err != nil {
		t.Fatalf("DeleteBar: %v", err)
	}
	for i, want := range []float64{100, 100, 100, 140} {
		if got := tempoAt(i); got != bpm(want) {
			t.Errorf("after deleting, bar %d is at %g BPM, want %g", i+1, 60e6/float64(got), want)
		}
	}
	saveable(t, d)
}

func TestDeleteLastBarIsRefused(t *testing.T) {
	d := New(NewOptions{Bars: 1})
	if err := d.DeleteBar(); err == nil {
		t.Fatal("the only bar of the piece was deleted")
	}
	if d.BarCount() != 1 {
		t.Errorf("got %d bars, want 1", d.BarCount())
	}
}

func TestShorteningAMeterRefusesRatherThanLosingNotes(t *testing.T) {
	d := New(NewOptions{Bars: 1})

	for i := 0; i < 4; i++ {
		d.GoTo(Cursor{Bar: 0, Beat: i, Str: 6})
		if err := d.SetFret(i); err != nil {
			t.Fatalf("SetFret: %v", err)
		}
	}
	before := saveable(t, d)
	if err := d.SetMeter(2, 4); err == nil {
		t.Fatal("a 4/4 bar of four quarter notes was squeezed into 2/4")
	}
	if got := saveable(t, d); got != before {
		t.Errorf("the refused meter change altered the piece\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

func TestRetuneRefusesToStrandNotes(t *testing.T) {
	d := New(NewOptions{})
	d.GoTo(Cursor{Bar: 0, Beat: 0, Str: 6})
	if err := d.SetFret(0); err != nil {
		t.Fatal(err)
	}

	err := d.SetTuning(score.Tuning{64, 59, 55, 50})
	if err == nil {
		t.Fatal("re-tuning away a string that had a note on it was accepted")
	}
	if got := len(d.Track().Tuning); got != 6 {
		t.Errorf("the refused re-tune left %d strings, want 6", got)
	}
	saveable(t, d)
}

func TestCapoRefusesToPushNotesOffTheTop(t *testing.T) {
	d := New(NewOptions{})
	d.GoTo(Cursor{Bar: 0, Beat: 0, Str: 1})
	if err := d.SetFret(textfmt.MaxFret); err != nil {
		t.Fatal(err)
	}

	if err := d.SetTuning(score.Tuning{90, 59, 55, 50, 45, 40}); err != nil {
		t.Fatalf("SetTuning: %v", err)
	}
	if err := d.SetCapo(20); err == nil {
		t.Fatal("a capo that pushes a note past MIDI 127 was accepted")
	}
	if got := d.Track().Capo; got != 0 {
		t.Errorf("the refused capo change left it at %d, want 0", got)
	}
	saveable(t, d)
}

func TestOpenSquaresUpARaggedPiece(t *testing.T) {

	sc := &score.Score{
		Title:  "Ragged",
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	for i, bars := range []int{3, 1} {
		tr := &score.Track{
			Name:    []string{"Lead", "Rhythm"}[i],
			Tuning:  append(score.Tuning(nil), score.StandardTuning...),
			Program: textfmt.DefaultProgram,
		}
		for b := 0; b < bars; b++ {
			bar := tr.AppendBar(4, 4)
			bar.AddBeat(score.Whole, score.Note{String: 6, Fret: b})
		}
		sc.Tracks = append(sc.Tracks, tr)
	}
	d, err := Open(sc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i, tr := range d.Score().Tracks {
		if got := len(tr.Bars); got != 3 {
			t.Errorf("track %d has %d bars, want 3", i+1, got)
		}
	}
	barFull(t, d)
	saveable(t, d)

	if got := len(sc.Tracks[1].Bars); got != 1 {
		t.Errorf("Open modified the caller's score (track 2 now has %d bars, was 1)", got)
	}
}

func TestOpenAcceptsWindPieces(t *testing.T) {
	sc, err := textfmt.Parse([]byte("\\instrument soprano sax\nD5.4 E5.4 G5.2"), "sax")
	if err != nil {
		t.Fatalf("parsing the wind fixture: %v", err)
	}
	d, err := Open(sc)
	if err != nil {
		t.Fatalf("Open refused a valid wind piece: %v", err)
	}
	if w := d.Wind(); w == nil || w.Name != "soprano sax" {
		t.Errorf("Wind() = %v, want the soprano sax", w)
	}
	barFull(t, d)
	saveable(t, d)
}

func TestWindPitchOps(t *testing.T) {
	w := score.WindByName("soprano sax")
	d := New(NewOptions{Wind: w})

	if got, want := d.WindReference(), w.LowSounding+w.Span/2; got != want {
		t.Errorf("empty WindReference = %d, want the mid-range %d", got, want)
	}

	if err := d.SetWindPitch(74); err != nil {
		t.Fatalf("SetWindPitch(74): %v", err)
	}
	n, ok := d.NoteAt(1)
	if !ok || n.String != 1 || n.Fret != 16 {
		t.Fatalf("SetWindPitch left %+v ok=%v, want String 1 Fret 16", n, ok)
	}
	if got := d.WindReference(); got != w.LowSounding+16 {
		t.Errorf("WindReference = %d, want the note under the cursor (%d)", got, w.LowSounding+16)
	}

	if err := d.ToggleTech(score.TechSlur); err != nil {
		t.Fatal(err)
	}
	if err := d.SetWindPitch(76); err != nil {
		t.Fatal(err)
	}
	if n, _ = d.NoteAt(1); n.Fret != 18 || n.Tech&score.TechSlur == 0 {
		t.Errorf("retyping the pitch gave %+v, want Fret 18 with its slur kept", n)
	}

	if err := d.NudgePitch(-12); err != nil {
		t.Fatal(err)
	}
	if n, _ = d.NoteAt(1); n.Fret != 6 {
		t.Errorf("NudgePitch(-12) left Fret %d, want 6", n.Fret)
	}
	if err := d.NudgePitch(-7); err == nil {
		t.Error("NudgePitch below the horn's lowest note was not refused")
	}

	if err := d.SetWindPitch(127); err != nil {
		t.Fatalf("SetWindPitch(127): %v — G9 is the top note name, not past it", err)
	}
	if err := d.SetWindPitch(128); err == nil {
		t.Error("SetWindPitch(128) was not refused")
	} else {
		if !strings.Contains(err.Error(), "G#9") {
			t.Errorf("the refusal %q does not name G#9, the note that was asked for", err)
		}
		if !strings.Contains(err.Error(), "G9") {
			t.Errorf("the refusal %q does not name G9, the actual top", err)
		}
	}
	if err := d.SetWindPitch(58); err != nil {
		t.Fatalf("SetWindPitch at the horn's bottom: %v", err)
	}
	if err := d.SetWindPitch(57); err == nil {
		t.Error("SetWindPitch below the horn's lowest note was not refused")
	}

	g := New(NewOptions{})
	if err := g.SetWindPitch(74); err == nil {
		t.Error("SetWindPitch on a guitar track was not refused")
	}
	if err := g.NudgePitch(1); err == nil {
		t.Error("NudgePitch on a guitar track was not refused")
	}
}

func TestWindRefusesFrettedVerbs(t *testing.T) {
	d := New(NewOptions{Wind: score.WindByName("soprano sax")})
	if err := d.SetWindPitch(74); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFret(0); err == nil {
		t.Error("SetFret on a wind track was not refused")
	}
	if err := d.SetCapo(2); err == nil {
		t.Error("SetCapo on a wind track was not refused")
	}
	if err := d.SetTuning(score.StandardTuning); err == nil {
		t.Error("SetTuning on a wind track was not refused")
	}
	if err := d.ToggleTech(score.TechPull); err == nil {
		t.Error("a pull-off on a wind track was not refused")
	}
	if err := d.ToggleTech(score.TechDead); err == nil {
		t.Error("a dead note on a wind track was not refused")
	}

	if err := d.ToggleTech(score.TechSlur); err != nil {
		t.Errorf("ToggleTech(TechSlur): %v", err)
	}
	if err := d.ToggleTech(score.TechVibrato); err != nil {
		t.Errorf("ToggleTech(TechVibrato): %v", err)
	}
	barFull(t, d)
	saveable(t, d)
}

func TestNewClampsNaNTempo(t *testing.T) {
	d := New(NewOptions{BPM: math.NaN()})
	if got := d.TempoAtCursor(); got != textfmt.DefaultBPM {
		t.Errorf("got %g BPM, want the default %g", got, float64(textfmt.DefaultBPM))
	}
	barFull(t, d)
	saveable(t, d)
}

func TestTitleAndTrackNameRefuseCommentStarter(t *testing.T) {
	d := New(NewOptions{Title: "Test"})
	if err := d.SetTitle("AC//DC"); err == nil {
		t.Error(`a title containing "//" was accepted`)
	}
	if got := d.Score().Title; got != "Test" {
		t.Errorf("the refused title stuck: %q", got)
	}
	if err := d.SetTrackName("Riff // solo intro"); err == nil {
		t.Error(`a track name containing "//" was accepted`)
	}
	if err := d.AddTrack("B // C", nil); err == nil {
		t.Error(`AddTrack with "//" in the name was accepted`)
	}
	if got := len(d.Score().Tracks); got != 1 {
		t.Errorf("the refused AddTrack left %d tracks, want 1", got)
	}
	saveable(t, d)
}

func TestExoticMeterRefusesInsteadOfPanicking(t *testing.T) {
	fifths := func(trackBars ...int) *score.Score {
		sc := &score.Score{
			Title:  "Fifths",
			Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
			Meters: score.MeterMap{{Tick: 0, Num: 1, Den: 5}},
		}
		for _, bars := range trackBars {
			tr := &score.Track{Tuning: append(score.Tuning(nil), score.StandardTuning...), Program: textfmt.DefaultProgram}
			for b := 0; b < bars; b++ {
				tr.AppendBar(1, 5).AddBeat(768, score.Note{String: 6, Fret: 0})
			}
			sc.Tracks = append(sc.Tracks, tr)
		}
		return sc
	}

	ragged := fifths(2, 1)
	if err := ragged.Validate(); err != nil {
		t.Fatalf("the fixture should be model-valid: %v", err)
	}
	if _, err := Open(ragged); err == nil {
		t.Error("Open squared up a piece whose meter no rests can fill")
	}

	d, err := Open(fifths(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.AppendBar(); err == nil {
		t.Error("AppendBar invented a bar of rests no note length fills")
	}
	if err := d.AddTrack("Second", nil); err == nil {
		t.Error("AddTrack invented a bar of rests no note length fills")
	}
	if got := d.BarCount(); got != 1 {
		t.Errorf("the refusals changed the piece: %d bars, want 1", got)
	}
	if got := len(d.Score().Tracks); got != 1 {
		t.Errorf("the refusals changed the piece: %d tracks, want 1", got)
	}
	if err := d.Score().Validate(); err != nil {
		t.Errorf("the refusals left an invalid piece: %v", err)
	}
}

func TestOpenTheRichFixture(t *testing.T) {
	src, err := os.ReadFile("../../testdata/fixture_rich.gtab")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	sc, err := textfmt.Parse(src, "rich")
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	d, err := Open(sc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	barFull(t, d)
	saveable(t, d)

	if got := d.BarCount(); got != 4 {
		t.Errorf("got %d bars, want 4", got)
	}
	if got := d.Score().Tracks[0].Capo; got != 2 {
		t.Errorf("the lead track's capo came through as %d, want 2", got)
	}
}

func TestInsertAndDeleteBeat(t *testing.T) {
	d := New(NewOptions{Bars: 1})
	if err := d.SetFret(0); err != nil {
		t.Fatal(err)
	}
	beats := len(d.Bar().Beats)
	if err := d.InsertBeat(); err != nil {
		t.Fatalf("InsertBeat: %v", err)
	}
	if got := d.Cursor().Beat; got != 1 {
		t.Errorf("the cursor is on beat %d, want 1 (the new one)", got)
	}
	if err := d.SetFret(3); err != nil {
		t.Fatalf("SetFret into the inserted beat: %v", err)
	}
	barFull(t, d)
	saveable(t, d)

	if err := d.DeleteBeat(); err != nil {
		t.Fatalf("DeleteBeat: %v", err)
	}
	if got := len(d.Bar().Beats); got != beats {
		t.Errorf("after deleting there are %d beats, want %d", got, beats)
	}
	barFull(t, d)
	saveable(t, d)
}

func TestTiesAndTechniques(t *testing.T) {
	d := New(NewOptions{Bars: 1})
	if err := d.SetFret(5); err != nil {
		t.Fatal(err)
	}
	if err := d.ToggleTech(score.TechHammer); err != nil {
		t.Fatalf("ToggleTech: %v", err)
	}
	if n, _ := d.NoteAt(6); n.Tech&score.TechHammer == 0 {
		t.Error("the hammer-on did not stick")
	}
	if err := d.ToggleTech(score.TechHammer); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.NoteAt(6); n.Tech != 0 {
		t.Error("toggling the technique again did not clear it")
	}
	d.MoveBeat(1)
	if err := d.SetFret(5); err != nil {
		t.Fatal(err)
	}
	if err := d.ToggleTie(); err != nil {
		t.Fatalf("ToggleTie: %v", err)
	}
	if n, _ := d.NoteAt(6); !n.Tied {
		t.Error("the tie did not stick")
	}
	src := saveable(t, d)
	if !strings.Contains(src, "~5.6") {
		t.Errorf("the tie did not write:\n%s", src)
	}
}

func TestTieWithoutANoteSaysSo(t *testing.T) {
	d := New(NewOptions{})
	if err := d.ToggleTie(); err == nil {
		t.Fatal("a rest was tied")
	}
	if d.CanUndo() {
		t.Error("the refused tie took an undo slot")
	}
}

func TestCursorStaysInsideThePiece(t *testing.T) {
	d := New(NewOptions{Bars: 2})
	d.GoTo(Cursor{Bar: 99, Beat: 99, Str: 99})
	c := d.Cursor()
	if c.Bar != 1 {
		t.Errorf("bar clamped to %d, want 1", c.Bar)
	}
	if c.Beat >= len(d.Bar().Beats) {
		t.Errorf("beat %d is past the %d in the bar", c.Beat, len(d.Bar().Beats))
	}
	if c.Str != 6 {
		t.Errorf("string clamped to %d, want 6", c.Str)
	}
	d.GoTo(Cursor{Bar: -5, Beat: -5, Str: -5})
	c = d.Cursor()
	if c.Bar != 0 || c.Beat != 0 || c.Str != 1 {
		t.Errorf("got %+v, want the first beat of the first bar on string 1", c)
	}
}

func TestMoveBeatCrossesBarlines(t *testing.T) {
	d := New(NewOptions{Bars: 2})
	d.GoToStart()

	for i := 0; i < 100; i++ {
		d.MoveBeat(1)
	}
	if got := d.Cursor().Bar; got != 1 {
		t.Errorf("walking forward landed in bar %d, want the last one (1)", got)
	}
	for i := 0; i < 100; i++ {
		d.MoveBeat(-1)
	}
	if c := d.Cursor(); c.Bar != 0 || c.Beat != 0 {
		t.Errorf("walking back landed at %+v, want the first beat of bar 1", c)
	}
}

func TestDeletingTheLastTrackIsRefused(t *testing.T) {
	d := New(NewOptions{})
	if err := d.DeleteTrack(); err == nil {
		t.Fatal("the only track was deleted")
	}
	if err := d.AddTrack("Second", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteTrack(); err != nil {
		t.Fatalf("DeleteTrack: %v", err)
	}
	if got := len(d.Score().Tracks); got != 1 {
		t.Errorf("got %d tracks, want 1", got)
	}
	saveable(t, d)
}

func TestRestsForMatchesItsContract(t *testing.T) {
	for r := int64(0); r <= 4*score.PPQ; r += tickGrid {
		want := r == 0 ||
			(r%40 == 0 && r >= 80) ||
			r == 180 ||
			(r%40 == 20 && r >= 260)
		rests, ok := restsFor(r)
		if ok != want {
			t.Fatalf("restsFor(%d) fillable = %v, want %v", r, ok, want)
		}
		if !ok {
			continue
		}
		var sum int64
		for _, d := range rests {
			sum += d
			if _, writable := writableDuration(d); !writable {
				t.Fatalf("restsFor(%d) produced a %d-tick rest, which cannot be written", r, d)
			}
		}
		if sum != r {
			t.Fatalf("restsFor(%d) sums to %d", r, sum)
		}
	}
}

func TestRestsForRefusesTheImpossible(t *testing.T) {

	for _, r := range []int64{-20, 10, 30, maxBarTicks + tickGrid, 20, 40, 60, 100, 140, 220} {
		if _, ok := restsFor(r); ok {
			t.Errorf("restsFor(%d) claimed to be fillable", r)
		}
	}
}

func TestRestsForBeatsGreedy(t *testing.T) {
	const dotted32nd = 180
	rests, ok := restsFor(4*score.PPQ - dotted32nd)
	if !ok {
		t.Fatal("the remainder after a dotted 32nd could not be filled")
	}
	var sum int64
	for _, d := range rests {
		sum += d
	}
	if want := int64(4*score.PPQ - dotted32nd); sum != want {
		t.Errorf("the rests sum to %d, want %d", sum, want)
	}
}
