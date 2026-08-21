package textfmt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/S95F/musicTutor/internal/score"
)

func Format(sc *score.Score) ([]byte, error) {
	if sc == nil {
		return nil, fmt.Errorf("gtab: nil score")
	}
	if err := sc.Validate(); err != nil {
		return nil, fmt.Errorf("gtab: score is not valid: %w", err)
	}
	w := &writer{sc: sc}
	if err := w.run(); err != nil {
		return nil, err
	}
	return []byte(w.b.String()), nil
}

func WriteFile(path string, sc *score.Score) error {
	src, err := Format(sc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gtab-*.tmp")
	if err != nil {
		return fmt.Errorf("gtab: %w", err)
	}
	name := tmp.Name()

	defer os.Remove(name)
	if _, err := tmp.Write(src); err != nil {
		tmp.Close()
		return fmt.Errorf("gtab: writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gtab: writing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("gtab: writing %s: %w", path, err)
	}
	return nil
}

type writer struct {
	sc  *score.Score
	b   strings.Builder
	ctx string
	tr  *score.Track

	sticky int64

	tempoDone []bool
	meterDone []bool
}

func (w *writer) run() error {

	for _, m := range w.sc.Meters {
		if _, _, ok := parseTimeSig(fmt.Sprintf("%d/%d", m.Num, m.Den)); !ok {
			return fmt.Errorf("gtab: the %d/%d time signature at tick %d is not one \\time can write (n/d with n 1-64 and d one of 1 2 4 8 16 32)", m.Num, m.Den, m.Tick)
		}
	}
	if err := w.header(); err != nil {
		return err
	}
	w.tempoDone = make([]bool, len(w.sc.Tempos))
	w.meterDone = make([]bool, len(w.sc.Meters))

	w.tempoDone[0], w.meterDone[0] = true, true
	for i, tr := range w.sc.Tracks {
		if err := w.track(i, tr); err != nil {
			return err
		}
	}
	for i, t := range w.sc.Tempos {
		if !w.tempoDone[i] {
			return fmt.Errorf("gtab: the tempo change at tick %d does not fall on a barline of any track; .gtab can only change tempo between bars", t.Tick)
		}
	}
	for i, m := range w.sc.Meters {
		if !w.meterDone[i] {
			return fmt.Errorf("gtab: the %d/%d time signature at tick %d does not fall on a barline of any track; .gtab can only change the meter between bars", m.Num, m.Den, m.Tick)
		}
	}
	return nil
}

func (w *writer) header() error {
	if title := strings.TrimSpace(w.sc.Title); title != "" {
		if strings.ContainsAny(title, "\r\n") {
			return fmt.Errorf("gtab: the title contains a line break, which \\title cannot hold")
		}
		if strings.Contains(title, "//") {
			return fmt.Errorf(`gtab: the title contains "//", which \title cannot hold (it would start a comment)`)
		}
		fmt.Fprintf(&w.b, "\\title %s\n", title)
	}
	bpm, err := bpmString(w.sc.Tempos.At(0))
	if err != nil {
		return err
	}
	m := w.sc.Meters.At(0)
	fmt.Fprintf(&w.b, "\\tempo %s\n", bpm)
	fmt.Fprintf(&w.b, "\\time %d/%d\n", m.Num, m.Den)
	return nil
}

func (w *writer) track(i int, tr *score.Track) error {
	w.tr = tr
	w.b.WriteString("\n")

	headStart := w.b.Len()

	name := tr.Name
	if i > 0 && strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("Track %d", i+1)
	}
	if strings.TrimSpace(name) != "" {
		if strings.ContainsAny(name, "\r\n") {
			return fmt.Errorf("gtab: track %d: the name contains a line break, which \\track cannot hold", i+1)
		}
		if strings.Contains(name, "//") {
			return fmt.Errorf(`gtab: track %d: the name contains "//", which \track cannot hold (it would start a comment)`, i+1)
		}
		fmt.Fprintf(&w.b, "\\track %s\n", name)
	}
	if tr.Wind != nil {

		fmt.Fprintf(&w.b, "\\instrument %s\n", tr.Wind.Name)
	} else if !tr.Tuning.Equal(score.StandardTuning) {

		names := make([]string, len(tr.Tuning))
		for j, key := range tr.Tuning {
			if key < 0 || key > 127 {
				return fmt.Errorf("gtab: track %d: open string %d is MIDI key %d, outside 0-127", i+1, len(tr.Tuning)-j, key)
			}
			names[len(tr.Tuning)-1-j] = pitchName(key)
		}
		fmt.Fprintf(&w.b, "\\tuning %s\n", strings.Join(names, " "))
	}
	if tr.Capo != 0 {
		if tr.Capo < 0 || tr.Capo > MaxFret {
			return fmt.Errorf("gtab: track %d: capo %d is outside 0-%d", i+1, tr.Capo, MaxFret)
		}
		fmt.Fprintf(&w.b, "\\capo %d\n", tr.Capo)
	}

	trackDefaultProgram := DefaultProgram
	if tr.Wind != nil {
		trackDefaultProgram = tr.Wind.Program
	}
	if tr.Program != trackDefaultProgram {
		if tr.Program < 0 || tr.Program > 127 {
			return fmt.Errorf("gtab: track %d: program %d is outside 0-127", i+1, tr.Program)
		}
		fmt.Fprintf(&w.b, "\\program %d\n", tr.Program)
	}
	if tr.Role == score.RoleBacking {
		w.b.WriteString("\\backing\n")
	}

	if w.b.Len() > headStart {
		w.b.WriteString("\n")
	}

	w.sticky = score.Quarter
	for bi, bar := range tr.Bars {
		w.ctx = fmt.Sprintf("track %d bar %d", i+1, bi+1)
		if err := w.betweenBars(bar); err != nil {
			return err
		}
		if err := w.bar(bar); err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) betweenBars(bar *score.Bar) error {
	if m := w.sc.Meters.At(bar.Start); m.Num != bar.Num || m.Den != bar.Den {
		return fmt.Errorf("gtab: %s is written as %d/%d but the meter map says %d/%d at tick %d; the two have to agree before the piece can be written",
			w.ctx, bar.Num, bar.Den, m.Num, m.Den, bar.Start)
	}
	for i, m := range w.sc.Meters {
		if m.Tick != bar.Start {
			continue
		}
		if !w.meterDone[i] {
			fmt.Fprintf(&w.b, "\\time %d/%d\n", m.Num, m.Den)
			w.meterDone[i] = true
		}
	}
	for i, t := range w.sc.Tempos {
		if t.Tick != bar.Start {
			continue
		}
		if !w.tempoDone[i] {
			bpm, err := bpmString(t.USPerQuarter)
			if err != nil {
				return err
			}
			fmt.Fprintf(&w.b, "\\tempo %s\n", bpm)
			w.tempoDone[i] = true
		}
	}
	return nil
}

func (w *writer) bar(bar *score.Bar) error {
	for i, beat := range bar.Beats {
		if i > 0 {
			w.b.WriteString(" ")
		}
		tok, err := w.beat(beat)
		if err != nil {
			return err
		}
		w.b.WriteString(tok)
	}
	w.b.WriteString(" |\n")
	return nil
}

func (w *writer) beat(bt *score.Beat) (string, error) {
	dur, err := w.durSuffix(bt.Dur)
	if err != nil {
		return "", err
	}
	switch len(bt.Notes) {
	case 0:
		return "r" + dur, nil
	case 1:
		n := bt.Notes[0]
		s, err := w.noteToken(n)
		if err != nil {
			return "", err
		}
		return s + dur + techString(n.Tech, w.tr.Wind != nil), nil
	}

	allTied := true
	for _, n := range bt.Notes {
		if !n.Tied {
			allTied = false
			break
		}
	}

	parts := make([]string, 0, len(bt.Notes))
	for _, n := range bt.Notes {
		if allTied {
			n.Tied = false
		}
		s, err := w.noteToken(n)
		if err != nil {
			return "", err
		}
		parts = append(parts, s+techString(n.Tech, false))
	}
	prefix := "("
	if allTied {
		prefix = "~("
	}

	return prefix + strings.Join(parts, " ") + ")" + dur, nil
}

func (w *writer) noteToken(n score.Note) (string, error) {
	if wind := w.tr.Wind; wind != nil {
		written := wind.Written(w.tr.Pitch(n))

		if written < 12 || written > 127 {
			return "", fmt.Errorf("gtab: %s: the note's written pitch is MIDI key %d, which a beat's note name cannot hold (C0-G9)", w.ctx, written)
		}
		s := pitchName(written)
		if n.Tied {
			s = "~" + s
		}
		return s, nil
	}
	if n.Fret < 0 || n.Fret > MaxFret {
		return "", fmt.Errorf("gtab: %s: fret %d is outside 0-%d", w.ctx, n.Fret, MaxFret)
	}
	s := strconv.Itoa(n.Fret) + "." + strconv.Itoa(n.String)
	if n.Tied {
		s = "~" + s
	}
	return s, nil
}

func techString(t score.Technique, wind bool) string {
	table := []struct {
		bit  score.Technique
		char byte
	}{
		{score.TechHammer, 'h'},
		{score.TechPull, 'p'},
		{score.TechSlide, 's'},
		{score.TechBend, 'b'},
		{score.TechVibrato, 'v'},
		{score.TechDead, 'x'},
	}
	if wind {
		table = []struct {
			bit  score.Technique
			char byte
		}{
			{score.TechSlur, 'l'},
			{score.TechSlide, 's'},
			{score.TechBend, 'b'},
			{score.TechVibrato, 'v'},
		}
	}
	var b strings.Builder
	for _, e := range table {
		if t&e.bit != 0 {
			b.WriteByte(e.char)
		}
	}
	return b.String()
}

func (w *writer) durSuffix(dur int64) (string, error) {
	if dur == w.sticky {
		return "", nil
	}
	name, ok := durationName(dur)
	if !ok {
		return "", fmt.Errorf("gtab: %s: a beat of %d ticks is not a note value .gtab can write (it holds plain, dotted and triplet 1 2 4 8 16 32)", w.ctx, dur)
	}
	w.sticky = dur
	return "." + name, nil
}

var durationNames = func() map[int64]string {
	out := map[int64]string{}
	for _, d := range []struct {
		base int64
		name string
	}{
		{score.Whole, "1"},
		{score.Half, "2"},
		{score.Quarter, "4"},
		{score.Eighth, "8"},
		{score.Sixteenth, "16"},
		{score.ThirtySec, "32"},
	} {
		out[d.base] = d.name
		out[score.Dotted(d.base)] = d.name + "."
		out[score.Triplet(d.base)] = d.name + "t"
	}
	return out
}()

func durationName(dur int64) (string, bool) {
	s, ok := durationNames[dur]
	return s, ok
}

func DurationNames() []struct {
	Ticks int64
	Name  string
} {
	out := make([]struct {
		Ticks int64
		Name  string
	}, 0, len(durationNames))
	for t, n := range durationNames {
		out = append(out, struct {
			Ticks int64
			Name  string
		}{t, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ticks < out[j].Ticks })
	return out
}

func bpmString(usPerQuarter int64) (string, error) {
	if usPerQuarter <= 0 {
		return "", fmt.Errorf("gtab: tempo of %d microseconds per quarter is not positive", usPerQuarter)
	}
	bpm := 60e6 / float64(usPerQuarter)
	if !(bpm >= 1 && bpm <= 1000) {
		return "", fmt.Errorf("gtab: tempo of %.4g BPM is outside the 1-1000 the format accepts", bpm)
	}

	for dec := 0; dec <= 12; dec++ {
		s := strconv.FormatFloat(bpm, 'f', dec, 64)
		v, err := strconv.ParseFloat(s, 64)
		if err == nil && v >= 1 && v <= 1000 && score.USPerQuarter(v) == usPerQuarter {
			return s, nil
		}
	}

	return "", fmt.Errorf("gtab: tempo of %d microseconds per quarter cannot be written as a BPM that reads back exactly", usPerQuarter)
}

func CleanLabel(s string) (string, bool) {
	cleaned := strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for strings.Contains(cleaned, "//") {
		cleaned = strings.ReplaceAll(cleaned, "//", "/ /")
	}
	changed := cleaned != s
	return strings.TrimSpace(cleaned), changed
}

var pitchClassNames = [12]string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

func pitchName(key int) string {
	return pitchClassNames[key%12] + strconv.Itoa(key/12-1)
}
