// Package midiimport reads Standard MIDI Files into the score model.
//
// MIDI is freeform — notes are on/off pairs at arbitrary ticks with no
// notion of bars, rests, ties, or fingering — so import is a normalization
// pass: ticks are rescaled from the file's resolution to score.PPQ, note
// starts and ends are quantized to the straight 1/32 grid (Grid ticks),
// bars are built from the meter map with rests filled in, notes crossing
// beat or bar boundaries continue as Tied notes (score.Events merges them
// back — the round-trip invariant), and every note gets a string and fret:
// a fretted part through internal/fretting's Inferred fingerings, a wind
// part by arithmetic on the instrument's single lane.
//
// Quantization is straight-grid only in v1: triplet feel is not preserved,
// and a warning is emitted when quantization moves a note edge by more
// than half a grid step (which happens when a note collapses to zero
// length and is stretched back to one grid step).
//
// A file track is not assumed to be one instrument. MIDI separates
// instruments by channel, and a type-0 (single-track) file — the common
// shape for downloaded MIDI — puts the whole band on one track that way,
// so each file track is split into one part per channel it carries. See
// splitChannels for why merging them is not a viable simplification.
// Channel 10, General MIDI percussion, is dropped rather than split off:
// its keys select kit pieces, not pitches.
//
// A part whose program change names a wind instrument (score.WindByProgram)
// imports as that instrument: one chromatic lane, one note at a time, so a
// same-tick chord keeps only its highest note — melody on top, the
// convention arrangers write by. A part with no program change keeps
// DefaultProgram and stays guitar: the default is an assumption, not
// evidence of an instrument.
//
// Deviations from the file are never silent — they are returned as
// human-readable warnings: skipped percussion tracks (channel 10), tracks
// split across channels, keys below the instrument's range shifted up an
// octave, dropped unplayable notes, chord notes a monophonic wind cannot
// sound, truncated overlapping notes, unterminated notes, and meter
// changes that fall inside a bar.
package midiimport

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/S95F/musicTutor/internal/fretting"
	"github.com/S95F/musicTutor/internal/score"
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
// One score track is produced per (file track, MIDI channel) pair holding
// notes. The RoleUser track — the part offered for practice — is the
// first of them that still sounds a note after fingering (see
// setUserTrack, which explains why "first" alone is not enough); the rest
// are RoleBacking. A part whose program change names a wind instrument
// (score.WindByProgram) becomes that wind's single-lane track; every other
// part is a standard-tuning guitar track with fingerings inferred by
// internal/fretting. The result always passes Validate.
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

// percussionChannel is the wire number of General MIDI channel 10, where
// a note's key selects a drum-kit piece instead of naming a pitch — so
// nothing on it can be spelled as a string and fret. internal/gpimport
// applies the same rule to Guitar Pro tracks.
const percussionChannel = 9

// A rawNote is one matched note-on/off pair in score ticks.
type rawNote struct {
	start, end int64
	key        int
	ch         uint8 // MIDI channel the note was authored on
	str, fret  int   // assigned fingering; str 0 until assigned
}

// A fileTrack is one SMF track exactly as the file holds it: possibly
// several instruments, told apart only by channel. A type-0 file has one
// of these carrying the entire piece.
type fileTrack struct {
	index    int
	name     string
	programs map[uint8]int // first program change per channel
	notes    []*rawNote
}

// A rawTrack is one part the import will build: one file track's notes on
// one MIDI channel.
type rawTrack struct {
	index   int  // file track index
	channel int  // MIDI channel, 0-based
	split   bool // the file track carried more than one channel
	name    string
	program int                   // program change on this channel, or -1
	wind    *score.WindInstrument // the instrument the program names, or nil for a fretted part
	notes   []*rawNote
}

// desc names a part in warnings. A file track that carried a single
// channel keeps the plain "track N" wording; a part carved out of a
// multi-channel track names its channel too, so a warning points at one
// instrument rather than at the whole merged track.
func (rt *rawTrack) desc() string {
	if !rt.split {
		return fmt.Sprintf("track %d", rt.index)
	}
	return fmt.Sprintf("track %d channel %d", rt.index, rt.channel+1)
}

// run drives the import of a parsed SMF.
func (im *importer) run(sm *smf.SMF) (*score.Score, []string, error) {
	s := &score.Score{}
	var raws []*rawTrack
	for ti, tr := range sm.Tracks {
		ft := im.readTrack(ti, tr)
		if ti == 0 && len(ft.notes) == 0 {
			// SMF-1 convention: a noteless first track is the
			// conductor track and its name is the piece title.
			s.Title = ft.name
		}
		raws = append(raws, im.splitChannels(ft)...)
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

	for _, rt := range raws {
		s.Tracks = append(s.Tracks, im.buildTrack(rt, score.RoleBacking, specs))
	}
	setUserTrack(s)
	if err := s.Validate(); err != nil {
		return nil, im.warns, fmt.Errorf("imported score failed validation: %w", err)
	}
	return s, im.warns, nil
}

// setUserTrack marks the practice part, and does it once the tracks are
// built rather than from the raw part order.
//
// Position alone stopped being safe when tracks began splitting per
// channel. normalize can empty a part completely — every note out of the
// instrument's range is dropped as unfrettable — and a whole channel can
// be exactly that: a piccolo, bells, a synth lead two octaves up. Chosen
// by position, such a channel becomes the part that opens for practice,
// and the player is handed a blank tab with nothing to play. Choosing the
// first track that actually sounds costs nothing when the first part is
// fine, which is the ordinary case.
//
// run has already established that at least one note survived
// normalization, so the loop always finds a track; the trailing
// assignment is the belt for a future caller that has not.
func setUserTrack(s *score.Score) {
	for _, tr := range s.Tracks {
		if trackHasNotes(tr) {
			tr.Role = score.RoleUser
			return
		}
	}
	if len(s.Tracks) > 0 {
		s.Tracks[0].Role = score.RoleUser
	}
}

// trackHasNotes reports whether a built track sounds anything at all, as
// opposed to being a run of rests.
func trackHasNotes(t *score.Track) bool {
	for _, bar := range t.Bars {
		for _, beat := range bar.Beats {
			if len(beat.Notes) > 0 {
				return true
			}
		}
	}
	return false
}

// readTrack extracts one file track's name, its first program change per
// channel, and its notes, matching note-ons to note-offs and filtering
// percussion (channel 10, wire channel 9). Ticks are converted to score
// resolution; quantization happens later in normalize.
//
// Programs are kept per channel, not per track: on a multi-channel track
// the first program change belongs to whichever channel sent it, and
// applying it to the whole track would label the guitar part with the
// bass's instrument.
func (im *importer) readTrack(ti int, tr smf.Track) *fileTrack {
	ft := &fileTrack{index: ti, programs: map[uint8]int{}}
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
			if ch == percussionChannel {
				perc++
				continue
			}
			n := &rawNote{start: im.scale(abs), key: int(key), ch: ch}
			ft.notes = append(ft.notes, n)
			open[[2]uint8{ch, key}] = append(open[[2]uint8{ch, key}], n)
		case msg.GetNoteEnd(&ch, &key):
			if ch == percussionChannel {
				continue
			}
			k := [2]uint8{ch, key}
			if q := open[k]; len(q) > 0 {
				q[0].end = im.scale(abs)
				open[k] = q[1:]
			}
		case msg.GetProgramChange(&ch, &prog):
			if ch == percussionChannel {
				continue
			}
			if _, ok := ft.programs[ch]; !ok {
				ft.programs[ch] = int(prog)
			}
		case msg.GetMetaTrackName(&text):
			if ft.name == "" {
				ft.name = text
			}
		}
	}
	if perc > 0 {
		if len(ft.notes) == 0 {
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
	return ft
}

// splitChannels divides a file track into one part per MIDI channel,
// lowest channel first, and returns nothing for a track without notes.
//
// A type-0 (single-track) MIDI file — the usual shape for downloaded
// MIDI — puts every instrument on one track, separated only by channel.
// Merging them hands the fretting heuristic a bass line, a guitar part
// and a piano voicing sounding at once; it crams what fits onto six
// strings, truncates the same-string overlaps and drops the rest, so the
// practice part is not any of the three instruments. Splitting by channel
// gives back one selectable part per instrument, which is the shape the
// rest of the app expects of a score.
//
// The split is driven by the channels actually used rather than by the
// file's format byte: type-1 tracks are nearly always single-channel, so
// this is a no-op for them, and where one is not, it is carrying several
// instruments for the same reason and wants the same treatment.
func (im *importer) splitChannels(ft *fileTrack) []*rawTrack {
	if len(ft.notes) == 0 {
		return nil
	}
	var channels []uint8
	seen := map[uint8]bool{}
	for _, n := range ft.notes {
		if !seen[n.ch] {
			seen[n.ch] = true
			channels = append(channels, n.ch)
		}
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i] < channels[j] })
	multi := len(channels) > 1
	if multi {
		im.warnf("track %d: notes on %d MIDI channels (%s); split into one part per channel, since a MIDI track separates instruments by channel",
			ft.index, len(channels), channelList(channels))
	}
	out := make([]*rawTrack, 0, len(channels))
	for _, ch := range channels {
		rt := &rawTrack{index: ft.index, channel: int(ch), split: multi, name: ft.name, program: -1}
		if p, ok := ft.programs[ch]; ok {
			rt.program = p
			// A program change naming a wind instrument makes the part
			// that instrument. Classification wants explicit evidence: a
			// part with no program change keeps the guitar default,
			// because DefaultProgram is an assumption of ours, not
			// something the file said.
			rt.wind = score.WindByProgram(p)
		}
		if multi {
			rt.name = partName(ft.name, ch)
		}
		for _, n := range ft.notes {
			if n.ch == ch {
				rt.notes = append(rt.notes, n)
			}
		}
		out = append(out, rt)
	}
	return out
}

// partName labels one channel's part. MIDI names tracks, never channels,
// so a split track's name is qualified by the channel rather than
// repeated verbatim on parts that are different instruments.
func partName(track string, ch uint8) string {
	if track == "" {
		return fmt.Sprintf("Channel %d", ch+1)
	}
	return fmt.Sprintf("%s (channel %d)", track, ch+1)
}

// channelList renders channels as the 1-based numbers a musician sees.
func channelList(channels []uint8) string {
	parts := make([]string, len(channels))
	for i, ch := range channels {
		parts[i] = strconv.Itoa(int(ch) + 1)
	}
	return strings.Join(parts, ", ")
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

// normalize quantizes a track's notes to the Grid, shifts keys below the
// part's instrument up an octave, gives every note its string and fret —
// heuristic fingerings for a fretted part (assignFrets), lane arithmetic
// for a wind part (assignWind), either of which drops what the instrument
// cannot sound — and truncates overlapping notes on the same lane.
func (im *importer) normalize(rt *rawTrack) {
	moved, shifted := 0, 0
	// The range floor is the lowest note the part's instrument sounds; the
	// one-octave rescue below it is the same policy for both families.
	low := lowestKey(score.StandardTuning, 0)
	if rt.wind != nil {
		low = rt.wind.LowSounding
	}
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
		im.warnf("%s: quantization to the 1/32 grid moved %d note(s) by more than half a grid step (triplet timing is not preserved)", rt.desc(), moved)
	}
	if shifted > 0 {
		im.warnf("%s: shifted %d note(s) below the instrument's range up an octave", rt.desc(), shifted)
	}

	sort.SliceStable(rt.notes, func(i, j int) bool {
		if rt.notes[i].start != rt.notes[j].start {
			return rt.notes[i].start < rt.notes[j].start
		}
		return rt.notes[i].key < rt.notes[j].key
	})

	if rt.wind != nil {
		im.assignWind(rt)
	} else {
		im.assignFrets(rt)
	}
	kept := rt.notes[:0]
	for _, n := range rt.notes {
		if n.str != 0 {
			kept = append(kept, n)
		}
	}
	rt.notes = kept

	// A new attack on a string still ringing cuts the ringing note off:
	// strings are monophonic, and a wind part's whole lane is.
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
		if rt.wind != nil {
			im.warnf("%s: truncated %d note(s) still sounding at the next attack (a %s plays one note at a time)", rt.desc(), truncated, rt.wind.Name)
		} else {
			im.warnf("%s: truncated %d note(s) overlapping a later note on the same string", rt.desc(), truncated)
		}
	}
}

// assignFrets gives a fretted part's notes their string and fret via the
// internal/fretting heuristic, leaving str 0 — dropped — on the notes it
// reports unplayable.
func (im *importer) assignFrets(rt *rawTrack) {
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
		im.warnf("%s: dropped unplayable note (key %d) at tick %d: %s",
			rt.desc(), u.Key, onsets[u.Beat][0].start, u.Reason)
	}
}

// assignWind puts a wind part's notes on the instrument's single lane:
// String 1, Fret counting semitones above the lowest note — arithmetic,
// not a heuristic, which is why buildTrack does not mark the result
// Inferred. A key still below the instrument after normalize's octave
// shift is dropped (str stays 0), mirroring the fretted policy. The
// ceiling is the WRITTEN pitch: a transposing instrument reads above
// what it sounds, and a sounding key past 127 - Transpose has no written
// note name, so the text format could never save the piece — such a note
// is dropped with a warning rather than imported as something unwritable.
// Span caps only what the editor offers — altissimo imports as written.
//
// The instrument is monophonic, so simultaneous attacks cannot all sound:
// each same-tick chord keeps its highest note — melody on top, the
// convention arrangers write by — and the drops are counted into one
// warning per part rather than reported note by note.
func (im *importer) assignWind(rt *rawTrack) {
	w := rt.wind
	for _, n := range rt.notes {
		if n.key < w.LowSounding {
			im.warnf("%s: dropped unplayable note (key %d) at tick %d: below the %s's lowest note",
				rt.desc(), n.key, n.start, w.Name)
			continue
		}
		if n.key > 127-w.Transpose {
			im.warnf("%s: dropped note (key %d) at tick %d: its written pitch on a %s is past MIDI 127",
				rt.desc(), n.key, n.start, w.Name)
			continue
		}
		note := w.NoteFor(n.key)
		n.str, n.fret = note.String, note.Fret
	}
	// rt.notes is sorted by (start, key), so within a same-tick chord the
	// last playable note is the highest: each collision drops the earlier.
	dropped := 0
	var prev *rawNote
	for _, n := range rt.notes {
		if n.str == 0 {
			continue
		}
		if prev != nil && prev.start == n.start {
			prev.str = 0
			dropped++
		}
		prev = n
	}
	if dropped > 0 {
		im.warnf("%s: dropped %d chord note(s), keeping the highest; a %s plays one note at a time",
			rt.desc(), dropped, w.Name)
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
		Program: program,
		Role:    role,
	}
	// One representation per family: a wind track carries its instrument
	// and no strings (Tuning nil, Capo 0), a fretted track its tuning —
	// Validate rejects a mix.
	if rt.wind != nil {
		tr.Wind = rt.wind
	} else {
		tr.Tuning = score.StandardTuning
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
					String: n.str,
					Fret:   n.fret,
					Tied:   n.start < segStart,
					// Inferred marks heuristic fingerings the UI renders
					// distinctly; a wind lane is arithmetic, not a guess.
					Inferred: rt.wind == nil,
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
