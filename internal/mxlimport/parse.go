package mxlimport

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/S95F/musicTutor/internal/fretting"
	"github.com/S95F/musicTutor/internal/score"
)

// overLong reports whether v file ticks would rescale past MaxTicks
// score ticks. The first clause keeps the rescale itself from
// overflowing int64 (a hostile <duration> can be anything an int64
// holds, and v*PPQ overflows long before that).
func overLong(v, divisions int64) bool {
	return v > (math.MaxInt64-divisions/2)/score.PPQ ||
		(v*score.PPQ+divisions/2)/divisions > MaxTicks
}

// A rawNote is one sounding note in score ticks, before bar building.
// Tied note chains are merged into a single rawNote spanning the chain.
type rawNote struct {
	start, end int64
	key        int
	str, fret  int  // fingering; str 0 until assigned
	hasFing    bool // authored <technical> string+fret
	inferred   bool // fingering came from internal/fretting
	// slurred: a slur arc begun at an earlier onset was still open when
	// this note started. Recorded on every note of a chord — the arc
	// covers the onset, not one head — so whichever note survives the
	// wind chord collapse still carries it. Only finishWind consumes it;
	// mapping a fretted part's slurs to hammer-ons/pull-offs is a
	// separate decision, not taken by this importer.
	slurred bool
	tech    score.Technique // techniques for the built note's attack
}

// An openTie is a tie start still waiting for its stop.
//
// Keying ties by pitch alone collapses unisons — two notes of the SAME
// pitch sounding at once on DIFFERENT strings, as common in guitar music
// as an open B against the 7th fret of the E string. One start overwrote
// the other, so a tie never resolved and its note kept the wrong duration.
// The authored string and the tie's number attribute are therefore part of
// a tie's identity, alongside the pitch.
type openTie struct {
	key  int // sounding MIDI key
	str  int // authored <technical><string>, 0 when the note has none
	num  int // <tie number>, 0 when absent
	note *rawNote
}

// maxOpenTies bounds how many unresolved tie starts are tracked at once.
// Ties resolve by scanning that list, so leaving it unbounded would turn a
// file full of never-closed <tie type="start"/> into quadratic work — the
// same shape as audit E2. A tie chain is per string and staves are capped
// at 25 lines, so no real music comes near this.
const maxOpenTies = 1024

// resolveTie returns the index of the open tie that a note of this pitch,
// string and tie number starting at tick continues, or -1.
//
// A candidate must abut the new note and share its pitch. Where BOTH sides
// state a string (or a tie number) they must agree — that is what keeps a
// unison's two chains apart — and a candidate agreeing on more of them
// wins, so the older, laxer match still works for files that author the
// string on only one end of the tie.
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

// usableFingering returns a note's authored <technical> string and fret.
// authored reports whether the file stated one at all; usable reports
// whether it can be honored — string and fret in range for the tuning AND
// the pitch they derive inside MIDI 0-127, since Validate rejects the
// score otherwise, so an absurd authored fret falls back to inference.
func usableFingering(e *xmlNote, tuning score.Tuning, capo int) (str, fret int, authored, usable bool) {
	s, f, ok := e.fingering()
	if !ok {
		return 0, 0, false, false
	}
	if s < 1 || s > len(tuning) || f < 0 {
		return 0, 0, true, false
	}
	if k := tuning[s-1] + capo + f; k < 0 || k > 127 {
		return 0, 0, true, false
	}
	return s, f, true, true
}

// A partData is one part's extracted content.
type partData struct {
	index      int // part index in the document
	id         string
	name       string
	program    int
	hasProgram bool                  // the file declared a <midi-program>
	wind       *score.WindInstrument // non-nil: import as a monophonic wind part
	tuning     score.Tuning
	capo       int
	sawTuning  bool // an explicit <staff-tuning> set the tuning
	notes      []*rawNote
	end        int64 // end tick of the part's last measure
}

// parsePart walks one part's measures in play order, maintaining the
// MusicXML time cursor: <note> advances it by its duration (except
// <chord/> notes, which share the previous note's onset), <backup> moves
// it backward, <forward> moves it forward. Positions are converted from
// the file's <divisions> to score ticks as they are read, so divisions
// changes take effect exactly where they appear.
//
// order lists the measure indices to walk, so a measure inside a repeated
// section is walked once per pass (see repeat.go). Indices past this
// part's last measure are skipped: the order spans the longest part.
func (im *importer) parsePart(pi int, decl *xmlScorePart, xp *xmlPart, order []int) (*partData, error) {
	pd := &partData{index: pi, id: xp.ID, program: DefaultProgram, tuning: score.StandardTuning}
	if decl != nil {
		pd.name = decl.PartName
		if p := decl.midiProgram(); p >= 0 {
			pd.program = p
			pd.hasProgram = true
		}
	}
	label := fmt.Sprintf("part %d (%s)", pi+1, xp.ID)

	divisions := int64(0) // file ticks per quarter; 0 until declared
	num, den := 4, 4
	pendingNum, pendingDen := 0, 0
	transpose := 0         // written-to-sounding shift for staff 1
	var measureStart int64 // score tick of the current measure's start
	var open []openTie     // tie starts awaiting their stops
	// Slur arcs, keyed by their number attribute so overlapping arcs stay
	// apart. An arc spans measures freely, so nothing prunes this at
	// barlines; an arc never stopped simply stays open to the part's end,
	// which is how a phrase mark reads, and an unmatched stop deletes
	// nothing — neither is worth a warning, since nothing is lost.
	slurs := map[int]bool{}
	slurInto := false // an arc from an earlier onset covers the current onset
	// Aggregate warning counters, reported once per part.
	var rounded, mismatched, otherVoice, otherStaff, grace, badTie, badFing, oversized int
	var unpitched, noPitch, badDur, badStep, strayChord, pickups, untracked, authoredFing int

	// setMeter records the shared bar structure's meter, and only where it
	// actually changes. barSpecs lays the bars out from this map, so it has
	// to agree with the num/den this walk uses at every measure start —
	// including when a repeat returns to a section under a different meter,
	// where nothing in the file restates the earlier signature.
	emitted := [2]int{4, 4} // what the map asserts; run's default entry is 4/4 at tick 0
	setMeter := func(tick int64, n, d int) {
		if pi != 0 || (emitted[0] == n && emitted[1] == d) {
			return
		}
		im.meters = append(im.meters, score.Meter{Tick: tick, Num: n, Den: d})
		emitted = [2]int{n, d}
	}

	// Divisions, meter and transposition persist until an <attributes>
	// restates them, so re-walking a measure would otherwise inherit
	// whatever the END of the repeated section left behind — a meter
	// change inside the section would leak into its second pass and shift
	// every bar after it. A repeat returns to the section with the state
	// that was in effect there the first time through, so that state is
	// recorded per measure and restored whenever the order comes back.
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
		// A tie is only ever continued by a note that abuts it, and a note
		// never starts before its own measure, so a tie ending before this
		// measure is dead and can leave the scan.
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
		var cursor, maxCursor int64 // file ticks from the measure's start
		placed := false             // any duration consumed yet in this measure
		lastBase := int64(-1)       // cursor base of the last non-chord note
		noteBase := len(pd.notes)   // first note this measure contributes
		tempoBase := len(im.tempos) // first tempo entry this measure contributes
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
				// <transpose> is the shift from written to sounding
				// pitch. It persists until another <attributes> restates
				// it, so an attributes block without one changes nothing.
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
					continue
				}
				if e.Pitch == nil {
					// Both causes are counted, not warned per note: a drum
					// staff is hundreds of <unpitched> notes, and one line
					// each would bury every warning that matters.
					if e.Unpitched != nil {
						unpitched++
					} else {
						noPitch++
					}
					continue
				}
				// Coverage is decided per ONSET, before this note's own
				// endpoints apply: an arc starting here covers only LATER
				// notes, and the note an arc stops on is the arc's last
				// covered note. Chord members reuse the head's answer
				// rather than re-reading the state their head's start
				// already changed. Grace notes left the walk above, so an
				// arc touching one is simply an arc missing an endpoint.
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
				// <pitch> is what is WRITTEN; the score model stores what
				// SOUNDS. Everything downstream — fret positions, scoring
				// expectations — is derived from this key.
				key += transpose
				// A hostile <duration> here or on an earlier <forward>
				// would blow the score extent past anything the bar
				// layout can hold — or overflow the rescale outright.
				// Skip the note; barSpecs backstops the global extent.
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
				// The fingering is resolved before the tie so the authored
				// string can take part in identifying the tie chain.
				str, fret, authored, usable := usableFingering(e, pd.tuning, pd.capo)
				if authored {
					authoredFing++
					if !usable {
						badFing++
					}
				}
				stop, tieStart, tieNum := e.tie()
				if stop {
					if i := resolveTie(open, key, str, tieNum, start); i >= 0 {
						open[i].note.end = end
						if tieStart {
							// The chain continues: the next link matches
							// against this note's identity, not the first's.
							open[i].str, open[i].num = str, tieNum
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
						open = append(open, openTie{key: key, str: str, num: tieNum, note: n})
					}
				}
			}
		}

		if num <= 0 || den <= 0 || (4*score.PPQ)%int64(den) != 0 {
			return nil, fmt.Errorf("unsupported time signature %d/%d", num, den)
		}
		barTicks := int64(num) * (4 * score.PPQ / int64(den))
		// A pickup (implicit) measure holds fewer beats than the time
		// signature and ENDS on the barline rather than starting on it.
		// The score model has no short bar — Bar.Len() is fixed by the
		// meter — so the bar is kept full and the content slid to its end,
		// which puts every later note on the beat the notation says and
		// keeps the count-in and metronome agreeing with the music. The
		// shift is applied after the walk, when the measure's full extent
		// is known, and covers the tempo marks inside it too.
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
		// Compare fill in cross-multiplied form so odd divisions that do
		// not divide the bar length exactly still compare correctly. A
		// pickup is underfull by definition, so it is not reported as such.
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

	// Wind classification: the declared General MIDI program is the
	// primary signal, the part name the fallback for files that carry no
	// MIDI block at all — never for one that declared a program the
	// registry does not know as a wind: a part labelled "Flute" whose
	// file explicitly says guitar is a guitar, and the file's word wins.
	// An explicit <staff-tuning> overrides both — a real tab staff, with
	// authored string lines, is stronger evidence of a fretted instrument
	// than a program number or a label — so the part stays fretted and
	// the conflict is reported, never silently resolved.
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
	// A wind part has no strings to check an authored fingering against,
	// so <technical> is not honored there at all — the pitch alone decides
	// the lane position. One warning covers both the in-range and the
	// out-of-range kind; the fretted-only warnings below would mislead
	// (nothing is inferred, and the fingering never wins).
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

// minBPM and maxBPM bound the tempos accepted from a file. The range is
// deliberately generous — real scores live in roughly 20-400 — because
// past it USPerQuarter degenerates (rounds to 0 or overflows) and
// Validate would hard-fail the whole import over one absurd marking.
const (
	minBPM float64 = 1
	maxBPM float64 = 4000
)

// recordTempo appends a tempo entry at the cursor's score tick. Absurd
// tempos degrade to a warning and are skipped, mirroring the MIDI
// importer; the tick-0 120 BPM default covers any gap.
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

// finish completes a parsed part. Notes are sorted, then placed: on a
// fretted part, notes without authored fingering get one from
// internal/fretting (marked Inferred; unplayable keys are dropped with a
// warning) placed around the fingerings the authored notes at the same
// onset already hold; on a wind part, finishWind computes the single-lane
// placement instead — no heuristic runs. Overlapping notes on the same
// string are then truncated (strings, and the wind lane, are monophonic).
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

	// A new attack on a string still ringing cuts the ringing note off.
	// On a wind part every note sits on lane 1, so this same rule is what
	// makes overlapping notes sequential — the one-note-per-beat shape
	// Validate demands of a monophonic instrument.
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
	// Truncating to zero length is a dropped note, not a shortened one —
	// two attacks on one string at the same tick, which only a file
	// authoring the same string twice in a chord can produce. Say so.
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

// finishWind places a wind part's notes on the instrument's single
// chromatic lane: every note gets str 1 and fret key−LowSounding. The
// lane is arithmetic, not a heuristic, so fretAssign never runs and
// nothing is marked Inferred. Any authored <technical> fingering was
// already discounted during parsing (a wind part has no strings to check
// it against).
//
// A <chord> resolves to its HIGHEST key — the melody rides on top of a
// voicing far more often than under it — and the rest of the chord is
// dropped, counted once per part. Out-of-range notes are dropped, never
// octave-rewritten: MusicXML pitch (written plus <transpose>) is
// authoritative, and rewriting it would silently change the music. Below,
// the floor is the instrument's lowest note; above, MIDI 127 is the only
// ceiling — altissimo is real playing and imports fine.
//
// A note a slur arc was open across plays without a fresh tongue —
// TechSlur. The flag was recorded per onset on every chord member, so it
// survives both filters here no matter which chord note carried the slur
// elements.
func (im *importer) finishWind(pd *partData, label string) {
	w := pd.wind
	// Range first, chords second: a chord whose TOP note is outside the
	// instrument must fall back to its highest playable note, not lose
	// the whole chord to the unplayable one. The ceiling is the written
	// pitch — a sounding key past 127 - Transpose has no written note
	// name, so the text format could never save it.
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
		// The sort is by start then key, so the group's last note is its
		// highest.
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

// finishFretted assigns a fretted part's missing fingerings with
// internal/fretting and drops what stays unplayable.
func (im *importer) finishFretted(pd *partData, label string) {
	// The fingerings an authored note already holds, per onset. A chord
	// that mixes authored and unfingered notes used to lose a note here:
	// the heuristic, told nothing about the authored notes, could put an
	// inferred one on a string an authored note already held, and the
	// same-string truncation below then shortened the earlier note to
	// nothing and dropped it. The player was shown an incomplete chord and
	// scored on it.
	claimed := map[int64][]fretting.Position{}
	for _, n := range pd.notes {
		if !n.hasFing {
			continue
		}
		claimed[n.start] = append(claimed[n.start], fretting.Position{String: n.str, Fret: n.fret})
	}

	// Group the unfingered notes by onset and hand the sequence to the
	// fretting heuristic, so chords get joint fingerings and consecutive
	// beats a playable hand path. Onsets that also carry an authored note
	// are pulled out of that sequence and fingered against those notes'
	// own positions; the authored notes fix the hand position there
	// anyway, so little is lost by breaking the chain at them.
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

// assignFingerings fingers a sequence of unfingered onsets with
// internal/fretting.
//
// fixed[i] holds the fingerings an authored note already occupies at
// groups[i]'s onset. The heuristic places around them: it keeps off their
// strings, and it fingers what is left within reach of the hand they
// imply, so an inferred note joins the authored shape instead of landing
// at whatever fret is cheapest on its own. A mixed chord is therefore
// fingered by the same machinery as a fully unfingered one rather than by
// a second, divergent placement rule. A note that still finds no position
// is warned about, never dropped in silence.
func (im *importer) assignFingerings(pd *partData, label string, groups [][]*rawNote, fixed [][]fretting.Position) {
	beats := make([][]int, len(groups))
	for i, g := range groups {
		beats[i] = make([]int, len(g))
		for j, n := range g {
			beats[i][j] = n.key
		}
	}
	positions, unplayable := fretAssign(beats, fixed, pd.tuning, pd.capo)
	for i, g := range groups {
		for j, n := range g {
			p := positions[i][j]
			if p.String < 1 {
				continue // unplayable; reported below and filtered out
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
