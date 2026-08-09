package mxlimport

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/S95F/guitarTutor/internal/score"
)

// A rawNote is one sounding note in score ticks, before bar building.
// Tied note chains are merged into a single rawNote spanning the chain.
type rawNote struct {
	start, end int64
	key        int
	str, fret  int  // fingering; str 0 until assigned
	hasFing    bool // authored <technical> string+fret
	inferred   bool // fingering came from internal/fretting
}

// A partData is one part's extracted content.
type partData struct {
	index   int // part index in the document
	id      string
	name    string
	program int
	tuning  score.Tuning
	capo    int
	notes   []*rawNote
	end     int64 // end tick of the part's last measure
}

// parsePart walks one part's measures in document order, maintaining the
// MusicXML time cursor: <note> advances it by its duration (except
// <chord/> notes, which share the previous note's onset), <backup> moves
// it backward, <forward> moves it forward. Positions are converted from
// the file's <divisions> to score ticks as they are read, so divisions
// changes take effect exactly where they appear.
func (im *importer) parsePart(pi int, decl *xmlScorePart, xp *xmlPart) (*partData, error) {
	pd := &partData{index: pi, id: xp.ID, program: DefaultProgram, tuning: score.StandardTuning}
	if decl != nil {
		pd.name = decl.PartName
		if p := decl.midiProgram(); p >= 0 {
			pd.program = p
		}
	}
	label := fmt.Sprintf("part %d (%s)", pi+1, xp.ID)

	divisions := int64(0) // file ticks per quarter; 0 until declared
	num, den := 4, 4
	pendingNum, pendingDen := 0, 0
	var measureStart int64     // score tick of the current measure's start
	open := map[int]*rawNote{} // key -> note whose tie start awaits its stop
	// Aggregate warning counters, reported once per part.
	var rounded, mismatched, otherVoice, otherStaff, grace, badTie, badFing int

	for mi := range xp.Measures {
		meas := &xp.Measures[mi]
		mlabel := meas.Number
		if mlabel == "" {
			mlabel = strconv.Itoa(mi + 1)
		}
		var cursor, maxCursor int64 // file ticks from the measure's start
		placed := false             // any duration consumed yet in this measure
		lastBase := int64(-1)       // cursor base of the last non-chord note
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
						if pi == 0 && (n != num || d != den) {
							im.meters = append(im.meters, score.Meter{Tick: measureStart, Num: n, Den: d})
						}
						num, den = n, d
					default:
						im.warnf("%s measure %s: time signature change after notes; applied at the next measure", label, mlabel)
						pendingNum, pendingDen = n, d
					}
				}
				for i := range e.StaffDetails {
					sd := &e.StaffDetails[i]
					if tun, ok, why := sd.tuning(); ok {
						pd.tuning = tun
					} else if why != "" {
						im.warnf("%s: %s; keeping the current tuning", label, why)
					}
					if sd.Capo != nil && *sd.Capo >= 0 {
						pd.capo = *sd.Capo
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
					im.warnf("%s measure %s: note with non-positive duration skipped", label, mlabel)
					continue
				}
				placed = true
				isChord := e.Chord != nil
				base := cursor
				if isChord && lastBase < 0 {
					im.warnf("%s measure %s: <chord/> with no preceding note; treated as a normal note", label, mlabel)
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
					continue
				}
				if e.Pitch == nil {
					im.warnf("%s measure %s: note with neither <pitch> nor <rest/> skipped", label, mlabel)
					continue
				}
				key, ok := midiKey(e.Pitch.Step, e.Pitch.Alter, e.Pitch.Octave)
				if !ok {
					im.warnf("%s measure %s: unrecognized pitch step %q; note skipped", label, mlabel, e.Pitch.Step)
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
				stop, tieStart := e.tie()
				if stop {
					if prev := open[key]; prev != nil && prev.end == start {
						prev.end = end
						if !tieStart {
							delete(open, key)
						}
						continue
					}
					badTie++
				}
				n := &rawNote{start: start, end: end, key: key}
				if s, f, ok := e.fingering(); ok {
					if s >= 1 && s <= len(pd.tuning) && f >= 0 {
						n.str, n.fret, n.hasFing = s, f, true
						if pd.tuning[s-1]+pd.capo+f != key {
							mismatched++
						}
					} else {
						badFing++
					}
				}
				pd.notes = append(pd.notes, n)
				if tieStart {
					open[key] = n
				}
			}
		}

		if num <= 0 || den <= 0 || (4*score.PPQ)%int64(den) != 0 {
			return nil, fmt.Errorf("unsupported time signature %d/%d", num, den)
		}
		// Compare fill in cross-multiplied form so odd divisions that do
		// not divide the bar length exactly still compare correctly.
		if placed && divisions > 0 && maxCursor*int64(den) != int64(num)*4*divisions {
			if maxCursor*int64(den) < int64(num)*4*divisions {
				im.warnf("%s measure %s: content does not fill the %d/%d measure; padded with rest", label, mlabel, num, den)
			} else {
				im.warnf("%s measure %s: content overruns the %d/%d measure; notes spill into the next bar", label, mlabel, num, den)
			}
		}
		measureStart += int64(num) * (4 * score.PPQ / int64(den))
		if pendingNum > 0 {
			if pi == 0 && (pendingNum != num || pendingDen != den) {
				im.meters = append(im.meters, score.Meter{Tick: measureStart, Num: pendingNum, Den: pendingDen})
			}
			num, den = pendingNum, pendingDen
			pendingNum, pendingDen = 0, 0
		}
	}
	pd.end = measureStart

	if grace > 0 {
		im.warnf("%s: skipped %d grace note(s) (not in the import subset)", label, grace)
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
	if badFing > 0 {
		im.warnf("%s: %d note(s) with out-of-range <technical> string/fret; fingering inferred instead", label, badFing)
	}
	if mismatched > 0 {
		im.warnf("%s: %d note(s) whose written pitch disagrees with tuning+capo+fret; the fingering wins (pitch is derived)", label, mismatched)
	}
	if rounded > 0 {
		im.warnf("%s: %d note(s) rounded when rescaling divisions to PPQ %d", label, rounded, score.PPQ)
	}
	return pd, nil
}

// recordDirectionTempo records a tempo change at the direction's position.
// <sound tempo> is authoritative when present; otherwise the metronome
// marking is converted to quarter-note BPM.
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

// recordTempo appends a tempo entry at the cursor's score tick.
func (im *importer) recordTempo(measureStart, cursor, divisions int64, bpm float64) {
	tick := measureStart
	if divisions > 0 {
		tick += (cursor*score.PPQ + divisions/2) / divisions
	}
	im.tempos = append(im.tempos, score.Tempo{Tick: tick, USPerQuarter: score.USPerQuarter(bpm)})
}

// finish completes a parsed part: notes are sorted, notes without authored
// fingering get one from internal/fretting (marked Inferred; unplayable
// keys are dropped with a warning), and overlapping notes on the same
// string are truncated (strings are monophonic).
func (im *importer) finish(pd *partData) {
	label := fmt.Sprintf("part %d (%s)", pd.index+1, pd.id)
	sort.SliceStable(pd.notes, func(i, j int) bool {
		if pd.notes[i].start != pd.notes[j].start {
			return pd.notes[i].start < pd.notes[j].start
		}
		return pd.notes[i].key < pd.notes[j].key
	})

	// Group the unfingered notes by onset and hand the sequence to the
	// fretting heuristic, so chords get joint fingerings and consecutive
	// beats a playable hand path.
	var groups [][]*rawNote
	for _, n := range pd.notes {
		if n.hasFing {
			continue
		}
		if len(groups) > 0 && groups[len(groups)-1][0].start == n.start {
			groups[len(groups)-1] = append(groups[len(groups)-1], n)
			continue
		}
		groups = append(groups, []*rawNote{n})
	}
	if len(groups) > 0 {
		beats := make([][]int, len(groups))
		for i, g := range groups {
			beats[i] = make([]int, len(g))
			for j, n := range g {
				beats[i][j] = n.key
			}
		}
		positions, unplayable := fretAssign(beats, pd.tuning, pd.capo)
		for i, g := range groups {
			for j, n := range g {
				n.str, n.fret = positions[i][j].String, positions[i][j].Fret
				n.inferred = true
			}
		}
		for _, u := range unplayable {
			im.warnf("%s: dropped unplayable note (key %d) at tick %d: %s",
				label, u.Key, groups[u.Beat][0].start, u.Reason)
		}
		kept := pd.notes[:0]
		for _, n := range pd.notes {
			if n.str != 0 {
				kept = append(kept, n)
			}
		}
		pd.notes = kept
	}

	// A new attack on a string still ringing cuts the ringing note off.
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
	kept := pd.notes[:0]
	for _, n := range pd.notes {
		if n.end > n.start {
			kept = append(kept, n)
		}
	}
	pd.notes = kept
}
