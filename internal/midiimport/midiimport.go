// Package midiimport reads Standard MIDI Files into the score model.
//
// MIDI is freeform — notes are on/off pairs at arbitrary ticks with no
// notion of bars, rests, ties, or fingering — so import is a normalization
// pass: ticks are rescaled from the file's resolution to score.PPQ, note
// starts and ends are quantized to the straight 1/32 grid (Grid ticks),
// bars are built from the meter map with rests filled in, notes crossing
// beat or bar boundaries continue as Tied notes (score.Events merges them
// back — the round-trip invariant), and every note gets an Inferred
// string/fret fingering from internal/fretting.
//
// Quantization is straight-grid only in v1: triplet feel is not preserved,
// and a warning is emitted when quantization moves a note edge by more
// than half a grid step (which happens when a note collapses to zero
// length and is stretched back to one grid step).
//
// Deviations from the file are never silent — they are returned as
// human-readable warnings: skipped percussion tracks (channel 10), keys
// below the instrument's range shifted up an octave, dropped unplayable
// notes, truncated same-string overlaps, unterminated notes, and meter
// changes that fall inside a bar.
package midiimport

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/S95F/guitarTutor/internal/fretting"
	"github.com/S95F/guitarTutor/internal/score"
)

// Grid is the quantization grid in score ticks: a straight 1/32 note.
const Grid = score.ThirtySec

// DefaultProgram is the General MIDI program assumed for tracks with no
// program change: 25, steel-string acoustic guitar, matching the text
// format's default (docs/TEXTFORMAT.md).
const DefaultProgram = 25

// MaxTicks caps the score extent the import will accept — about 14
// hours of 4/4 at 120 BPM. barSpecs allocates one barSpec per bar from
// tick 0 to the score's end, so a single hostile delta (a few bytes in
// the file) could otherwise turn the layout into an unbounded
// allocation loop. Score ticks are always PPQ 960 after rescaling, so
// the limit is resolution-independent: a 3-hour piece is roughly 21
// million ticks / 5,400 bars, comfortably inside.
const MaxTicks = 100_000_000

// maxBars is the belt-and-braces cap on the bar count itself, for meter
// maps whose tiny bars (a 1/128 meter makes 30-tick bars) would slice
// even a legal extent into an absurd number of specs.
const maxBars = 100_000

// Import parses a Standard MIDI File into a Score, returning
// human-readable warnings for everything the import changed or dropped.
// The first file track containing notes becomes the RoleUser track, later
// ones RoleBacking; all tracks are standard tuning with fingerings
// inferred by internal/fretting. The result always passes Validate.
func Import(data []byte) (*score.Score, []string, error) {
	sm, err := smf.ReadFrom(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("reading SMF: %w", err)
	}
	mt, ok := sm.TimeFormat.(smf.MetricTicks)
	if !ok {
		return nil, nil, fmt.Errorf("SMPTE time format is not supported")
	}
	im := &importer{filePPQ: int64(mt.Resolution())}
	return im.run(sm)
}

// ImportFile reads path and imports it via Import.
func ImportFile(path string) (*score.Score, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return Import(data)
}

// An importer holds one import's state: the file's tick resolution and the
// warnings accumulated so far.
type importer struct {
	filePPQ int64
	warns   []string
}

// warnf records one human-readable warning.
func (im *importer) warnf(format string, args ...any) {
	im.warns = append(im.warns, fmt.Sprintf(format, args...))
}

// scale converts a tick from the file's resolution to score.PPQ, rounding
// to nearest.
func (im *importer) scale(t int64) int64 {
	return (t*score.PPQ + im.filePPQ/2) / im.filePPQ
}

// quantize snaps a score tick to the nearest Grid multiple.
func quantize(t int64) int64 { return (t + Grid/2) / Grid * Grid }

// A rawNote is one matched note-on/off pair in score ticks.
type rawNote struct {
	start, end int64
	key        int
	str, fret  int // assigned fingering; str 0 until assigned
}

// A rawTrack is one file track's extracted content.
type rawTrack struct {
	index   int // file track index
	name    string
	program int // first program change, or -1
	notes   []*rawNote
}

// run drives the import of a parsed SMF.
func (im *importer) run(sm *smf.SMF) (*score.Score, []string, error) {
	s := &score.Score{}
	var raws []*rawTrack
	for ti, tr := range sm.Tracks {
		rt := im.readTrack(ti, tr)
		if ti == 0 && len(rt.notes) == 0 {
			// SMF-1 convention: a noteless first track is the
			// conductor track and its name is the piece title.
			s.Title = rt.name
		}
		if len(rt.notes) > 0 {
			raws = append(raws, rt)
		}
	}
	if len(raws) == 0 {
		return nil, im.warns, fmt.Errorf("no playable notes in file")
	}

	s.Tempos, s.Meters = im.readMaps(sm)

	// Normalize every track's notes, then size the shared bar structure
	// to the latest note end across all tracks.
	var end int64
	kept := 0
	for _, rt := range raws {
		im.normalize(rt)
		kept += len(rt.notes)
		for _, n := range rt.notes {
			if n.end > end {
				end = n.end
			}
		}
	}
	if kept == 0 {
		// Every note was dropped during normalization; the warnings
		// explain why.
		return nil, im.warns, fmt.Errorf("no playable notes in file")
	}
	specs, err := im.barSpecs(s.Meters, end)
	if err != nil {
		return nil, im.warns, err
	}
	s.Meters = rebaseMeters(s.Meters, specs)

	for i, rt := range raws {
		role := score.RoleBacking
		if i == 0 {
			role = score.RoleUser
		}
		s.Tracks = append(s.Tracks, im.buildTrack(rt, role, specs))
	}
	if err := s.Validate(); err != nil {
		return nil, im.warns, fmt.Errorf("imported score failed validation: %w", err)
	}
	return s, im.warns, nil
}

// readTrack extracts one file track's name, program, and notes, matching
// note-ons to note-offs and filtering percussion (channel 10, wire
// channel 9). Ticks are converted to score resolution; quantization
// happens later in normalize.
func (im *importer) readTrack(ti int, tr smf.Track) *rawTrack {
	rt := &rawTrack{index: ti, program: -1}
	open := map[[2]uint8][]*rawNote{} // (channel, key) -> unclosed notes, oldest first
	perc := 0
	var abs int64
	for _, ev := range tr {
		abs += int64(ev.Delta)
		msg := ev.Message
		var ch, key, vel, prog uint8
		var text string
		switch {
		case msg.GetNoteStart(&ch, &key, &vel):
			if ch == 9 {
				perc++
				continue
			}
			n := &rawNote{start: im.scale(abs), key: int(key)}
			rt.notes = append(rt.notes, n)
			open[[2]uint8{ch, key}] = append(open[[2]uint8{ch, key}], n)
		case msg.GetNoteEnd(&ch, &key):
			if ch == 9 {
				continue
			}
			k := [2]uint8{ch, key}
			if q := open[k]; len(q) > 0 {
				q[0].end = im.scale(abs)
				open[k] = q[1:]
			}
		case msg.GetProgramChange(&ch, &prog):
			if ch == 9 {
				continue
			}
			if rt.program < 0 {
				rt.program = int(prog)
			}
		case msg.GetMetaTrackName(&text):
			if rt.name == "" {
				rt.name = text
			}
		}
	}
	if perc > 0 {
		if len(rt.notes) == 0 {
			im.warnf("track %d: percussion track (channel 10) skipped", ti)
		} else {
			im.warnf("track %d: skipped %d percussion notes (channel 10)", ti, perc)
		}
	}
	unterminated := 0
	for _, q := range open {
		for _, n := range q {
			n.end = im.scale(abs)
			unterminated++
		}
	}
	if unterminated > 0 {
		im.warnf("track %d: closed %d unterminated note(s) at end of track", ti, unterminated)
	}
	return rt
}

// readMaps collects tempo and time-signature meta events from every track
// into sorted maps, inserting the SMF defaults (120 BPM, 4/4) when a file
// omits them at tick 0.
func (im *importer) readMaps(sm *smf.SMF) (score.TempoMap, score.MeterMap) {
	var tempos score.TempoMap
	var meters score.MeterMap
	for _, tr := range sm.Tracks {
		var abs int64
		for _, ev := range tr {
			abs += int64(ev.Delta)
			var bpm float64
			var num, den uint8
			switch {
			case ev.Message.GetMetaTempo(&bpm):
				// A zero tempo payload (0 microseconds per quarter)
				// decodes as an infinite BPM and would produce a
				// degenerate tempo the score model rejects: skip it
				// and let the tick-0 default supply 120 BPM.
				if usq := score.USPerQuarter(bpm); usq > 0 {
					tempos = append(tempos, score.Tempo{Tick: im.scale(abs), USPerQuarter: usq})
				} else {
					im.warnf("skipped tempo event at tick %d: non-positive microseconds per quarter", im.scale(abs))
				}
			case ev.Message.GetMetaMeter(&num, &den):
				// gomidi decodes the denominator byte as a power of
				// two, and a byte of 8 or more overflows uint8 to 0;
				// the numerator byte is not validated at all. A
				// zero-valued meter placed past the last note escapes
				// barSpecs' check (which only inspects meters that
				// govern bars) and later panics BeatLen with a divide
				// by zero when the user seeks to the end of the piece
				// — so reject invalid meters here, with a warning.
				if num >= 1 && den >= 1 {
					meters = append(meters, score.Meter{Tick: im.scale(abs), Num: int(num), Den: int(den)})
				} else {
					im.warnf("skipped time signature event at tick %d: %d/%d is not a valid meter", im.scale(abs), num, den)
				}
			}
		}
	}
	sort.SliceStable(tempos, func(i, j int) bool { return tempos[i].Tick < tempos[j].Tick })
	sort.SliceStable(meters, func(i, j int) bool { return meters[i].Tick < meters[j].Tick })
	tempos = dedupe(tempos, func(t score.Tempo) int64 { return t.Tick })
	meters = dedupe(meters, func(m score.Meter) int64 { return m.Tick })
	if len(tempos) == 0 || tempos[0].Tick != 0 {
		tempos = append(score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}}, tempos...)
	}
	if len(meters) == 0 || meters[0].Tick != 0 {
		meters = append(score.MeterMap{{Tick: 0, Num: 4, Den: 4}}, meters...)
	}
	return tempos, meters
}

// dedupe keeps the last of consecutive entries sharing a tick (a later
// event at the same tick overrides an earlier one).
func dedupe[T any](in []T, tick func(T) int64) []T {
	var out []T
	for i, v := range in {
		if i+1 < len(in) && tick(in[i+1]) == tick(v) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// normalize quantizes a track's notes to the Grid, shifts
// below-instrument keys up an octave, infers fingerings via
// internal/fretting (dropping notes fretting reports unplayable), and
// truncates overlapping notes on the same string.
func (im *importer) normalize(rt *rawTrack) {
	moved, shifted := 0, 0
	low := lowestKey(score.StandardTuning, 0)
	for _, n := range rt.notes {
		qs, qe := quantize(n.start), quantize(n.end)
		if qe <= qs {
			qe = qs + Grid
		}
		if abs64(qs-n.start) > Grid/2 || abs64(qe-n.end) > Grid/2 {
			moved++
		}
		n.start, n.end = qs, qe
		if n.key < low {
			n.key += 12
			shifted++
		}
	}
	if moved > 0 {
		im.warnf("track %d: quantization to the 1/32 grid moved %d note(s) by more than half a grid step (triplet timing is not preserved)", rt.index, moved)
	}
	if shifted > 0 {
		im.warnf("track %d: shifted %d note(s) below the instrument's range up an octave", rt.index, shifted)
	}

	sort.SliceStable(rt.notes, func(i, j int) bool {
		if rt.notes[i].start != rt.notes[j].start {
			return rt.notes[i].start < rt.notes[j].start
		}
		return rt.notes[i].key < rt.notes[j].key
	})

	// Group simultaneous attacks into chords and hand the onset sequence
	// to the fretting heuristic.
	var onsets [][]*rawNote
	for _, n := range rt.notes {
		if len(onsets) > 0 && onsets[len(onsets)-1][0].start == n.start {
			onsets[len(onsets)-1] = append(onsets[len(onsets)-1], n)
			continue
		}
		onsets = append(onsets, []*rawNote{n})
	}
	beats := make([][]int, len(onsets))
	for i, group := range onsets {
		beats[i] = make([]int, len(group))
		for j, n := range group {
			beats[i][j] = n.key
		}
	}
	positions, unplayable := fretting.Assign(beats, score.StandardTuning, 0)
	for i, group := range onsets {
		for j, n := range group {
			n.str, n.fret = positions[i][j].String, positions[i][j].Fret
		}
	}
	for _, u := range unplayable {
		im.warnf("track %d: dropped unplayable note (key %d) at tick %d: %s",
			rt.index, u.Key, onsets[u.Beat][0].start, u.Reason)
	}
	kept := rt.notes[:0]
	for _, n := range rt.notes {
		if n.str != 0 {
			kept = append(kept, n)
		}
	}
	rt.notes = kept

	// A new attack on a string still ringing cuts the ringing note off
	// (strings are monophonic).
	truncated := 0
	last := map[int]*rawNote{}
	for _, n := range rt.notes {
		if p := last[n.str]; p != nil && p.end > n.start {
			p.end = n.start
			truncated++
		}
		last[n.str] = n
	}
	if truncated > 0 {
		im.warnf("track %d: truncated %d note(s) overlapping a later note on the same string", rt.index, truncated)
	}
}

// A barSpec is one bar of the shared bar structure.
type barSpec struct {
	start    int64
	num, den int
}

// barSpecs lays out contiguous bars from tick 0 through end under the
// meter map. A meter change that falls inside a bar takes effect at the
// next barline, with a warning.
func (im *importer) barSpecs(meters score.MeterMap, end int64) ([]barSpec, error) {
	// A negative end means an absurd file delta overflowed int64 in
	// scale(); both that and a merely enormous end would otherwise drive
	// the layout loop below into an unbounded (or never-terminating)
	// allocation.
	if end < 0 || end > MaxTicks {
		return nil, fmt.Errorf("score too long: extends to tick %d, past the %d-tick limit", end, int64(MaxTicks))
	}
	var specs []barSpec
	starts := map[int64]bool{}
	for start := int64(0); start < end; {
		if len(specs) >= maxBars {
			return nil, fmt.Errorf("score too long: more than %d bars", maxBars)
		}
		m := meters.At(start)
		if m.Num <= 0 || m.Den <= 0 || (4*score.PPQ)%int64(m.Den) != 0 {
			return nil, fmt.Errorf("unsupported time signature %d/%d", m.Num, m.Den)
		}
		specs = append(specs, barSpec{start: start, num: m.Num, den: m.Den})
		starts[start] = true
		start += int64(m.Num) * (4 * score.PPQ / int64(m.Den))
	}
	for _, m := range meters {
		if m.Tick < end && !starts[m.Tick] {
			im.warnf("meter change to %d/%d at tick %d is not on a barline; applied at the next bar", m.Num, m.Den, m.Tick)
		}
	}
	return specs, nil
}

// rebaseMeters rewrites the meter map onto the ticks the bar structure
// actually used: a change that fell inside a bar (which barSpecs applied
// at the next barline) moves to that barline, and when two changes land
// on the same tick the later one wins — so the stored map and the bars
// always agree.
func rebaseMeters(meters score.MeterMap, specs []barSpec) score.MeterMap {
	if len(specs) == 0 {
		return meters
	}
	starts := make(map[int64]bool, len(specs))
	for _, bs := range specs {
		starts[bs.start] = true
	}
	last := specs[len(specs)-1]
	scoreEnd := last.start + int64(last.num)*(4*score.PPQ/int64(last.den))
	out := make(score.MeterMap, len(meters))
	copy(out, meters)
	for i := range out {
		if out[i].Tick >= scoreEnd || starts[out[i].Tick] {
			continue
		}
		next := scoreEnd
		for _, bs := range specs {
			if bs.start > out[i].Tick {
				next = bs.start
				break
			}
		}
		out[i].Tick = next
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Tick < out[j].Tick })
	return dedupe(out, func(m score.Meter) int64 { return m.Tick })
}

// buildTrack converts one normalized raw track into a score.Track. Within
// each bar the beat boundaries are the union of the bar's edges and every
// note onset and end inside it, so each note covers a whole number of
// beats: the first carries the attack, the rest continue it as Tied
// notes, and beats with nothing sounding become rests.
//
// The walk is linear in notes plus bars rather than their product: note
// edges are bucketed into their bars up front (one binary search per
// edge), and the notes sounding at each beat segment are tracked with an
// advancing cursor plus a carried active set instead of rescanning the
// whole note list per segment. After normalize's same-string truncation
// the active set holds at most one note per string. A tied note can
// sound many bars past its start, which is why the active set is carried
// across bars instead of being recomputed from a per-bar window of note
// starts.
func (im *importer) buildTrack(rt *rawTrack, role score.TrackRole, specs []barSpec) *score.Track {
	program := rt.program
	if program < 0 {
		program = DefaultProgram
	}
	tr := &score.Track{
		Name:    rt.name,
		Tuning:  score.StandardTuning,
		Program: program,
		Role:    role,
	}
	// Bucket every note edge that falls strictly inside a bar; an edge
	// on a barline adds no boundary because the bar's own edges already
	// cover it. Bars are contiguous, so the bar owning edge x is the
	// last one starting before x, and an edge at or past that bar's end
	// sits on a barline or past the score.
	edges := make([][]int64, len(specs))
	for _, n := range rt.notes {
		for _, x := range [2]int64{n.start, n.end} {
			i := sort.Search(len(specs), func(i int) bool { return specs[i].start >= x }) - 1
			if i < 0 {
				continue
			}
			barEnd := specs[i].start + int64(specs[i].num)*(4*score.PPQ/int64(specs[i].den))
			if x < barEnd {
				edges[i] = append(edges[i], x)
			}
		}
	}
	// rt.notes is sorted by start (normalize keeps it that way) and
	// segment starts only ever advance, so a single cursor pass
	// activates each note exactly once; a note leaves the active set
	// once it no longer sounds at the current segment.
	cursor := 0
	var active []*rawNote
	for bi, bs := range specs {
		bar := tr.AppendBar(bs.num, bs.den)
		barEnd := bar.Start + bar.Len()
		bounds := append([]int64{bar.Start, barEnd}, edges[bi]...)
		sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
		for i := 0; i+1 < len(bounds); i++ {
			segStart, segEnd := bounds[i], bounds[i+1]
			if segEnd == segStart {
				continue // duplicate boundary
			}
			for cursor < len(rt.notes) && rt.notes[cursor].start <= segStart {
				active = append(active, rt.notes[cursor])
				cursor++
			}
			kept := active[:0]
			for _, n := range active {
				if n.end > segStart {
					kept = append(kept, n)
				}
			}
			active = kept
			var notes []score.Note
			for _, n := range active {
				notes = append(notes, score.Note{
					String:   n.str,
					Fret:     n.fret,
					Tied:     n.start < segStart,
					Inferred: true,
				})
			}
			sort.Slice(notes, func(i, j int) bool { return notes[i].String > notes[j].String })
			bar.AddBeat(segEnd-segStart, notes...)
		}
	}
	return tr
}

// lowestKey returns the lowest sounding pitch of a tuning with a capo.
func lowestKey(tuning score.Tuning, capo int) int {
	low := tuning[0] + capo
	for _, open := range tuning {
		if open+capo < low {
			low = open + capo
		}
	}
	return low
}

// abs64 returns the absolute value of a tick delta.
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
