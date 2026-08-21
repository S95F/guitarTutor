package mxlimport

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/S95F/musicTutor/internal/fretting"
	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

func overLong(v, divisions int64) bool {
	return v > (math.MaxInt64-divisions/2)/score.PPQ ||
		(v*score.PPQ+divisions/2)/divisions > MaxTicks
}

type rawNote struct {
	start, end int64
	key        int
	str, fret  int
	hasFing    bool
	inferred   bool

	slurred bool
	tech    score.Technique
}

type openTie struct {
	key  int
	str  int
	num  int
	note *rawNote
}

const maxCapoFret = 12

const maxImportFret = 30

const maxOpenTies = 1024

func resolveTie(open []openTie, key, str, num int, start int64) int {
	best, bestRank := -1, -1
	for i := range open {
		t := &open[i]
		if t.key != key || t.note.end != start {
			continue
		}
		rank := 0
		if t.str != 0 && str != 0 {
			if t.str != str {
				continue
			}
			rank += 2
		}
		if t.num != 0 && num != 0 {
			if t.num != num {
				continue
			}
			rank++
		}
		if rank > bestRank {
			best, bestRank = i, rank
		}
	}
	return best
}

func usableFingering(e *xmlNote, tuning score.Tuning, capo int) (str, fret int, authored, usable bool) {
	s, f, ok := e.fingering()
	if !ok {
		return 0, 0, false, false
	}
	if s < 1 || s > len(tuning) || f < 0 || f > maxImportFret {
		return 0, 0, true, false
	}
	if k := tuning[s-1] + capo + f; k < 0 || k > 127 {
		return 0, 0, true, false
	}
	return s, f, true, true
}

type partData struct {
	index      int
	id         string
	name       string
	program    int
	hasProgram bool
	wind       *score.WindInstrument
	tuning     score.Tuning
	capo       int
	sawTuning  bool
	notes      []*rawNote
	end        int64
}

func (im *importer) parsePart(pi int, decl *xmlScorePart, xp *xmlPart, order []int) (*partData, error) {
	pd := &partData{index: pi, id: xp.ID, program: DefaultProgram, tuning: score.StandardTuning}
	if decl != nil {

		name, changed := textfmt.CleanLabel(decl.PartName)
		if changed {
			im.warnf("part %d (%s): the part name holds text a saved .gtab cannot (a line break or \"//\"); imported as %q", pi+1, xp.ID, name)
		}
		pd.name = name
		if p := decl.midiProgram(); p >= 0 {
			pd.program = p
			pd.hasProgram = true
		}
	}
	label := fmt.Sprintf("part %d (%s)", pi+1, xp.ID)

	divisions := int64(0)
	num, den := 4, 4
	pendingNum, pendingDen := 0, 0
	transpose := 0
	var measureStart int64
	var open []openTie

	slurs := map[int]bool{}
	slurInto := false

	var rounded, mismatched, otherVoice, otherStaff, grace, badTie, badFing, oversized int
	var unpitched, noPitch, badDur, badStep, strayChord, pickups, untracked, authoredFing int

	emitted := [2]int{4, 4}
	setMeter := func(tick int64, n, d int) {
		if pi != 0 || (emitted[0] == n && emitted[1] == d) {
			return
		}
		im.meters = append(im.meters, score.Meter{Tick: tick, Num: n, Den: d})
		emitted = [2]int{n, d}
	}

	type carried struct {
		divisions                                   int64
		num, den, pendingNum, pendingDen, transpose int
	}
	entry := map[int]carried{}

	for _, mi := range order {
		if mi < 0 || mi >= len(xp.Measures) {
			continue
		}
		if c, ok := entry[mi]; ok {
			divisions, num, den = c.divisions, c.num, c.den
			pendingNum, pendingDen, transpose = c.pendingNum, c.pendingDen, c.transpose
		} else {
			entry[mi] = carried{divisions, num, den, pendingNum, pendingDen, transpose}
		}
		setMeter(measureStart, num, den)

		if len(open) > 0 {
			live := open[:0]
			for _, t := range open {
				if t.note.end >= measureStart {
					live = append(live, t)
				}
			}
			open = live
		}
		meas := &xp.Measures[mi]
		mlabel := meas.Number
		if mlabel == "" {
			mlabel = strconv.Itoa(mi + 1)
		}
		var cursor, maxCursor int64
		placed := false
		lastBase := int64(-1)
		noteBase := len(pd.notes)
		tempoBase := len(im.tempos)
		scale := func(v int64) int64 { return (v*score.PPQ + divisions/2) / divisions }

		for _, el := range meas.Elements {
			switch e := el.(type) {
			case *xmlAttributes:
				if e.Divisions > 0 {
					nd := int64(e.Divisions)
					if divisions > 0 && nd != divisions && cursor > 0 {
						if (cursor*nd)%divisions != 0 {
							im.warnf("%s measure %s: divisions change mid-measure rounded the time cursor", label, mlabel)
						}
						cursor = (cursor*nd + divisions/2) / divisions
						maxCursor = (maxCursor*nd + divisions/2) / divisions
						if lastBase > 0 {
							lastBase = (lastBase*nd + divisions/2) / divisions
						}
					}
					divisions = nd
				}
				if t := e.firstTime(); t != nil {
					n, d, err := t.parse()
					switch {
					case err != nil:
						im.warnf("%s measure %s: %v; keeping %d/%d", label, mlabel, err, num, den)
					case !placed:
						setMeter(measureStart, n, d)
						num, den = n, d
					default:
						im.warnf("%s measure %s: time signature change after notes; applied at the next measure", label, mlabel)
						pendingNum, pendingDen = n, d
					}
				}

				if t, ok := e.transposeForStaff(1); ok {
					transpose = t.semitones()
				}
				for i := range e.StaffDetails {
					sd := &e.StaffDetails[i]
					if tun, ok, why := sd.tuning(); ok {
						pd.tuning = tun
						pd.sawTuning = true
					} else if why != "" {
						im.warnf("%s: %s; keeping the current tuning", label, why)
					}
					if sd.Capo != nil {
						if c := *sd.Capo; c >= 0 && c <= maxCapoFret {
							pd.capo = c
						} else {

							im.warnf("%s: capo fret %d outside 0-%d; using no capo", label, c, maxCapoFret)
							pd.capo = 0
						}
					}
				}

			case *xmlDirection:
				im.recordDirectionTempo(e, measureStart, cursor, divisions, label, mlabel)

			case *xmlSound:
				if e.Tempo > 0 {
					im.recordTempo(measureStart, cursor, divisions, e.Tempo)
				}

			case *xmlBackup:
				if divisions <= 0 {
					return nil, fmt.Errorf("%s measure %s: <duration> before <divisions>", label, mlabel)
				}
				cursor -= int64(e.Duration)
				if cursor < 0 {
					im.warnf("%s measure %s: <backup> past the start of the measure; clamped to the barline", label, mlabel)
					cursor = 0
				}
				placed = true
				lastBase = -1

			case *xmlForward:
				if divisions <= 0 {
					return nil, fmt.Errorf("%s measure %s: <duration> before <divisions>", label, mlabel)
				}
				cursor += int64(e.Duration)
				if cursor > maxCursor {
					maxCursor = cursor
				}
				placed = true
				lastBase = -1

			case *xmlNote:
				if e.Grace != nil {
					grace++
					continue
				}
				if divisions <= 0 {
					return nil, fmt.Errorf("%s measure %s: <duration> before <divisions>", label, mlabel)
				}
				dur := int64(e.Duration)
				if dur <= 0 {
					badDur++
					continue
				}
				placed = true
				isChord := e.Chord != nil
				base := cursor
				if isChord && lastBase < 0 {
					strayChord++
					isChord = false
				}
				if isChord {
					base = lastBase
				} else {
					lastBase = base
					cursor = base + dur
					if cursor > maxCursor {
						maxCursor = cursor
					}
				}
				if e.Voice != "" && e.Voice != "1" {
					if e.Rest == nil {
						otherVoice++
					}
					continue
				}
				if e.Staff != 0 && e.Staff != 1 {
					if e.Rest == nil {
						otherStaff++
					}
					continue
				}
				if e.Rest != nil {

					restStops, restStarts := e.slurs()
					for _, num := range restStops {
						delete(slurs, num)
					}
					for _, num := range restStarts {
						slurs[num] = true
					}
					continue
				}
				if e.Pitch == nil {

					if e.Unpitched != nil {
						unpitched++
					} else {
						noPitch++
					}
					continue
				}

				if !isChord {
					slurInto = len(slurs) > 0
				}
				slurStops, slurStarts := e.slurs()
				for _, num := range slurStops {
					delete(slurs, num)
				}
				for _, num := range slurStarts {
					slurs[num] = true
				}
				key, ok := midiKey(e.Pitch.Step, e.Pitch.Alter, e.Pitch.Octave)
				if !ok {
					badStep++
					continue
				}

				key += transpose

				if overLong(dur, divisions) || overLong(base, divisions) || overLong(base+dur, divisions) {
					oversized++
					continue
				}
				if (base*score.PPQ)%divisions != 0 || ((base+dur)*score.PPQ)%divisions != 0 {
					rounded++
				}
				start := measureStart + scale(base)
				end := measureStart + scale(base+dur)
				if end <= start {
					end = start + 1
				}

				str, fret, authored, usable := usableFingering(e, pd.tuning, pd.capo)
				if authored {
					authoredFing++
					if !usable {
						badFing++
					}
				}
				stop, tieStart, stopNum, startNum := e.tie()
				if stop {
					if i := resolveTie(open, key, str, stopNum, start); i >= 0 {
						open[i].note.end = end
						if tieStart {

							open[i].str, open[i].num = str, startNum
						} else {
							open = append(open[:i], open[i+1:]...)
						}
						continue
					}
					badTie++
				}
				n := &rawNote{start: start, end: end, key: key, slurred: slurInto}
				if usable {
					n.str, n.fret, n.hasFing = str, fret, true
					if pd.tuning[str-1]+pd.capo+fret != key {
						mismatched++
					}
				}
				pd.notes = append(pd.notes, n)
				if tieStart {
					if len(open) >= maxOpenTies {
						untracked++
					} else {
						open = append(open, openTie{key: key, str: str, num: startNum, note: n})
					}
				}
			}
		}

		if num <= 0 || den <= 0 || (4*score.PPQ)%int64(den) != 0 {
			return nil, fmt.Errorf("unsupported time signature %d/%d", num, den)
		}
		beatTicks := 4 * score.PPQ / int64(den)

		if int64(num) > MaxTicks/beatTicks {
			return nil, fmt.Errorf("score too long: one %d/%d measure alone exceeds the %d-tick limit", num, den, int64(MaxTicks))
		}
		barTicks := int64(num) * beatTicks

		if meas.Implicit && divisions > 0 && maxCursor > 0 && !overLong(maxCursor, divisions) {
			if off := barTicks - scale(maxCursor); off > 0 {
				for _, n := range pd.notes[noteBase:] {
					n.start += off
					n.end += off
				}
				for k := tempoBase; k < len(im.tempos); k++ {
					im.tempos[k].Tick += off
				}
				pickups++
			}
		}

		if placed && divisions > 0 && !meas.Implicit && maxCursor*int64(den) != int64(num)*4*divisions {
			if maxCursor*int64(den) < int64(num)*4*divisions {
				im.warnf("%s measure %s: content does not fill the %d/%d measure; padded with rest", label, mlabel, num, den)
			} else {
				im.warnf("%s measure %s: content overruns the %d/%d measure; notes spill into the next bar", label, mlabel, num, den)
			}
		}
		measureStart += barTicks
		if pendingNum > 0 {
			setMeter(measureStart, pendingNum, pendingDen)
			num, den = pendingNum, pendingDen
			pendingNum, pendingDen = 0, 0
		}
	}
	pd.end = measureStart

	wind := score.WindByProgram(pd.program)
	if wind == nil && !pd.hasProgram {
		wind = score.WindByName(pd.name)
	}
	if wind != nil {
		if pd.sawTuning {
			im.warnf("%s: MIDI program or part name says %s, but an explicit <staff-tuning> declares a tab staff; the tab staff wins and the part imports as fretted", label, wind.Name)
		} else {
			pd.wind = wind
		}
	}

	if grace > 0 {
		im.warnf("%s: skipped %d grace note(s) (not in the import subset)", label, grace)
	}
	if unpitched > 0 {
		im.warnf("%s: skipped %d <unpitched> percussion note(s) (only pitched notes are in the import subset)", label, unpitched)
	}
	if noPitch > 0 {
		im.warnf("%s: skipped %d note(s) with neither <pitch> nor <rest/>", label, noPitch)
	}
	if badDur > 0 {
		im.warnf("%s: skipped %d note(s) with a non-positive duration", label, badDur)
	}
	if badStep > 0 {
		im.warnf("%s: skipped %d note(s) with an unrecognized pitch step", label, badStep)
	}
	if strayChord > 0 {
		im.warnf("%s: %d <chord/> note(s) had no preceding note; treated as normal notes", label, strayChord)
	}
	if pickups > 0 {
		im.warnf("%s: right-aligned %d pickup measure(s) to the barline; the bar is padded with a leading rest", label, pickups)
	}
	if otherVoice > 0 {
		im.warnf("%s: skipped %d note(s) in voices other than 1 (only voice 1 is imported)", label, otherVoice)
	}
	if otherStaff > 0 {
		im.warnf("%s: skipped %d note(s) on staves other than 1 (only staff 1 is imported)", label, otherStaff)
	}
	if badTie > 0 {
		im.warnf("%s: %d tie stop(s) had no matching tied note; treated as fresh attacks", label, badTie)
	}
	if untracked > 0 {
		im.warnf("%s: %d tie start(s) past the %d unresolved-tie limit were not tracked; their notes are separate attacks", label, untracked, maxOpenTies)
	}

	if pd.wind != nil && authoredFing > 0 {
		im.warnf("%s: ignored authored <technical> string/fret on %d note(s); a %s has no strings to check them against", label, authoredFing, pd.wind.Name)
	}
	if pd.wind == nil && badFing > 0 {
		im.warnf("%s: %d note(s) with out-of-range <technical> string/fret; fingering inferred instead", label, badFing)
	}
	if oversized > 0 {
		im.warnf("%s: skipped %d note(s) whose duration or position rescales past the %d-tick score limit", label, oversized, int64(MaxTicks))
	}
	if pd.wind == nil && mismatched > 0 {
		im.warnf("%s: %d note(s) whose written pitch disagrees with tuning+capo+fret; the fingering wins (pitch is derived)", label, mismatched)
	}
	if rounded > 0 {
		im.warnf("%s: %d note(s) rounded when rescaling divisions to PPQ %d", label, rounded, score.PPQ)
	}
	return pd, nil
}

func (im *importer) recordDirectionTempo(d *xmlDirection, measureStart, cursor, divisions int64, label, mlabel string) {
	if d.Sound != nil && d.Sound.Tempo > 0 {
		im.recordTempo(measureStart, cursor, divisions, d.Sound.Tempo)
		return
	}
	for i := range d.Metronomes {
		bpm, ok, why := d.Metronomes[i].quarterBPM()
		if !ok {
			im.warnf("%s measure %s: %s", label, mlabel, why)
			continue
		}
		im.recordTempo(measureStart, cursor, divisions, bpm)
		return
	}
}

const (
	minBPM float64 = 1
	maxBPM float64 = 4000
)

func (im *importer) recordTempo(measureStart, cursor, divisions int64, bpm float64) {
	tick := measureStart
	if divisions > 0 {
		tick += (cursor*score.PPQ + divisions/2) / divisions
	}
	if usq := score.USPerQuarter(bpm); usq <= 0 || bpm < minBPM || bpm > maxBPM {
		im.warnf("skipped tempo of %g BPM at tick %d (outside the supported %g-%g BPM range)", bpm, tick, minBPM, maxBPM)
		return
	}
	if tick < 0 || tick > MaxTicks {
		im.warnf("skipped tempo change at tick %d (past the %d-tick score limit)", tick, int64(MaxTicks))
		return
	}
	im.tempos = append(im.tempos, score.Tempo{Tick: tick, USPerQuarter: score.USPerQuarter(bpm)})
}

func (im *importer) finish(pd *partData) {
	label := fmt.Sprintf("part %d (%s)", pd.index+1, pd.id)
	sort.SliceStable(pd.notes, func(i, j int) bool {
		if pd.notes[i].start != pd.notes[j].start {
			return pd.notes[i].start < pd.notes[j].start
		}
		return pd.notes[i].key < pd.notes[j].key
	})

	if pd.wind != nil {
		im.finishWind(pd, label)
	} else {
		im.finishFretted(pd, label)
	}

	truncated := 0
	last := map[int]*rawNote{}
	for _, n := range pd.notes {
		if p := last[n.str]; p != nil && p.end > n.start {
			p.end = n.start
			truncated++
		}
		last[n.str] = n
	}
	if truncated > 0 {
		im.warnf("%s: truncated %d note(s) overlapping a later note on the same string", label, truncated)
	}

	collapsed := 0
	kept := pd.notes[:0]
	for _, n := range pd.notes {
		if n.end > n.start {
			kept = append(kept, n)
			continue
		}
		collapsed++
	}
	pd.notes = kept
	if collapsed > 0 {
		im.warnf("%s: dropped %d note(s) left zero-length by another attack on the same string at the same tick", label, collapsed)
	}
}

func (im *importer) finishWind(pd *partData, label string) {
	w := pd.wind

	kept := pd.notes[:0]
	for _, n := range pd.notes {
		if n.key < w.LowSounding {
			im.warnf("%s: dropped note (key %d) at tick %d: below the %s's lowest note (key %d)", label, n.key, n.start, w.Name, w.LowSounding)
			continue
		}
		if n.key > 127-w.Transpose {
			im.warnf("%s: dropped note (key %d) at tick %d: its written pitch on a %s is past MIDI 127", label, n.key, n.start, w.Name)
			continue
		}
		kept = append(kept, n)
	}
	pd.notes = kept
	chords := 0
	kept = pd.notes[:0]
	for i := 0; i < len(pd.notes); {
		j := i
		for j+1 < len(pd.notes) && pd.notes[j+1].start == pd.notes[i].start {
			j++
		}
		if j > i {
			chords++
		}

		kept = append(kept, pd.notes[j])
		i = j + 1
	}
	pd.notes = kept
	if chords > 0 {
		im.warnf("%s: kept only the highest note of %d chord(s); a %s plays one note at a time", label, chords, w.Name)
	}
	for _, n := range pd.notes {
		p := w.NoteFor(n.key)
		n.str, n.fret = p.String, p.Fret
		if n.slurred {
			n.tech = score.TechSlur
		}
	}
}

func (im *importer) finishFretted(pd *partData, label string) {

	claimed := map[int64][]fretting.Position{}
	for _, n := range pd.notes {
		if !n.hasFing {
			continue
		}
		claimed[n.start] = append(claimed[n.start], fretting.Position{String: n.str, Fret: n.fret})
	}

	var plain, mixed [][]*rawNote
	for _, n := range pd.notes {
		if n.hasFing {
			continue
		}
		dst := &plain
		if claimed[n.start] != nil {
			dst = &mixed
		}
		if g := *dst; len(g) > 0 && g[len(g)-1][0].start == n.start {
			g[len(g)-1] = append(g[len(g)-1], n)
			continue
		}
		*dst = append(*dst, []*rawNote{n})
	}
	if len(plain) > 0 {
		im.assignFingerings(pd, label, plain, nil)
	}
	for _, g := range mixed {
		im.assignFingerings(pd, label, [][]*rawNote{g}, [][]fretting.Position{claimed[g[0].start]})
	}
	if len(plain) > 0 || len(mixed) > 0 {
		kept := pd.notes[:0]
		for _, n := range pd.notes {
			if n.str != 0 {
				kept = append(kept, n)
			}
		}
		pd.notes = kept
	}
}

func (im *importer) assignFingerings(pd *partData, label string, groups [][]*rawNote, fixed [][]fretting.Position) {
	beats := make([][]int, len(groups))
	for i, g := range groups {
		beats[i] = make([]int, len(g))
		for j, n := range g {
			beats[i][j] = n.key
		}
	}
	positions, unplayable := fretting.AssignWith(beats, fixed, pd.tuning, pd.capo)
	for i, g := range groups {
		for j, n := range g {
			p := positions[i][j]
			if p.String < 1 {
				continue
			}

			if k := pd.tuning[p.String-1] + pd.capo + p.Fret; k < 0 || k > 127 {
				im.warnf("%s: dropped note (key %d) at tick %d: string %d fret %d sounds MIDI key %d, outside 0-127",
					label, n.key, n.start, p.String, p.Fret, k)
				continue
			}
			n.str, n.fret = p.String, p.Fret
			n.inferred = true
		}
	}
	for _, u := range unplayable {
		im.warnf("%s: dropped unplayable note (key %d) at tick %d: %s",
			label, u.Key, groups[u.Beat][0].start, u.Reason)
	}
}
