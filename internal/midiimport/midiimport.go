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
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

const Grid = score.ThirtySec

const DefaultProgram = 25

const MaxTicks = 100_000_000

const maxBars = 100_000

var smfHeaderMagic = []byte("MThd")

func Import(data []byte) (*score.Score, []string, error) {

	if len(data) >= 14 && bytes.HasPrefix(data, smfHeaderMagic) && data[12]&0x80 != 0 {
		return nil, nil, fmt.Errorf("SMPTE time format is not supported")
	}

	if err := checkSMFEventLengths(data); err != nil {
		return nil, nil, err
	}
	sm, err := readSMF(data)
	if err != nil {
		return nil, nil, err
	}
	mt, ok := sm.TimeFormat.(smf.MetricTicks)
	if !ok {
		return nil, nil, fmt.Errorf("SMPTE time format is not supported")
	}
	im := &importer{filePPQ: int64(mt.Resolution())}
	return im.run(sm)
}

func checkSMFEventLengths(data []byte) error {
	if !bytes.HasPrefix(data, smfHeaderMagic) || len(data) < 14 {
		return nil
	}
	n := len(data)

	varlen := func(pos *int) (v int, ok bool) {
		for i := 0; ; i++ {
			if *pos >= n {
				return 0, false
			}
			b := data[*pos]
			*pos++
			if v <= n {
				v = v<<7 | int(b&0x7f)
				if v > n {
					v = n + 1
				}
			}
			if b&0x80 == 0 {
				return v, true
			}
			if i >= 4 {
				return v, true
			}
		}
	}

	pos := 8 + int(be32(data[4:8]))
	for pos+8 <= n {
		typ := data[pos : pos+4]
		clen := int(be32(data[pos+4 : pos+8]))
		pos += 8
		if !bytes.Equal(typ, []byte("MTrk")) {
			pos += clen
			continue
		}
		chunkEnd := pos + clen
		if chunkEnd > n || chunkEnd < pos {
			chunkEnd = n
		}
		var status byte
		for pos < chunkEnd {
			if _, ok := varlen(&pos); !ok {
				return nil
			}
			if pos >= chunkEnd {
				break
			}
			b := data[pos]
			if b&0x80 != 0 {
				status = b
				pos++
			} else if status == 0 {
				return nil
			}
			switch {
			case status == 0xFF:
				if pos >= chunkEnd {
					return nil
				}
				pos++
				l, ok := varlen(&pos)
				if !ok {
					return nil
				}
				if l > n {
					return fmt.Errorf("reading SMF: malformed file (meta event declares %d bytes, larger than the %d-byte file)", l, n)
				}
				pos += l
			case status == 0xF0 || status == 0xF7:
				l, ok := varlen(&pos)
				if !ok {
					return nil
				}
				if l > n {
					return fmt.Errorf("reading SMF: malformed file (sysex event declares %d bytes, larger than the %d-byte file)", l, n)
				}
				pos += l
			case status >= 0x80 && status <= 0xEF:
				if status < 0xC0 || status >= 0xE0 {
					pos += 2
				} else {
					pos++
				}
			default:
				return nil
			}
		}
		pos = chunkEnd
	}
	return nil
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func readSMF(data []byte) (sm *smf.SMF, err error) {
	defer func() {
		if r := recover(); r != nil {
			sm, err = nil, fmt.Errorf("reading SMF: malformed file (%v)", r)
		}
	}()
	sm, err = smf.ReadFrom(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("reading SMF: %w", err)
	}
	return sm, nil
}

func ImportFile(path string) (*score.Score, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return Import(data)
}

type importer struct {
	filePPQ int64
	warns   []string
}

func (im *importer) warnf(format string, args ...any) {
	im.warns = append(im.warns, fmt.Sprintf(format, args...))
}

func (im *importer) scale(t int64) int64 {
	return (t*score.PPQ + im.filePPQ/2) / im.filePPQ
}

func quantize(t int64) int64 { return (t + Grid/2) / Grid * Grid }

const percussionChannel = 9

type rawNote struct {
	start, end int64
	key        int
	ch         uint8
	str, fret  int
}

type fileTrack struct {
	index    int
	name     string
	programs map[uint8]int
	notes    []*rawNote
}

type rawTrack struct {
	index   int
	channel int
	split   bool
	name    string
	program int
	wind    *score.WindInstrument
	notes   []*rawNote
}

func (rt *rawTrack) desc() string {
	if !rt.split {
		return fmt.Sprintf("track %d", rt.index)
	}
	return fmt.Sprintf("track %d channel %d", rt.index, rt.channel+1)
}

func (im *importer) run(sm *smf.SMF) (*score.Score, []string, error) {
	s := &score.Score{}
	var raws []*rawTrack
	for ti, tr := range sm.Tracks {
		ft := im.readTrack(ti, tr)
		if ti == 0 && len(ft.notes) == 0 {

			s.Title = ft.name
		}
		raws = append(raws, im.splitChannels(ft)...)
	}
	if len(raws) == 0 {
		return nil, im.warns, fmt.Errorf("no playable notes in file")
	}

	s.Tempos, s.Meters = im.readMaps(sm)

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

func (im *importer) readTrack(ti int, tr smf.Track) *fileTrack {
	ft := &fileTrack{index: ti, programs: map[uint8]int{}}
	open := map[[2]uint8][]*rawNote{}
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

				name, changed := textfmt.CleanLabel(text)
				if changed {
					im.warnf("track %d: the track name %q holds text a saved .gtab cannot (a line break or \"//\"); imported as %q", ti, text, name)
				}
				ft.name = name
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

func partName(track string, ch uint8) string {
	if track == "" {
		return fmt.Sprintf("Channel %d", ch+1)
	}
	return fmt.Sprintf("%s (channel %d)", track, ch+1)
}

func channelList(channels []uint8) string {
	parts := make([]string, len(channels))
	for i, ch := range channels {
		parts[i] = strconv.Itoa(int(ch) + 1)
	}
	return strings.Join(parts, ", ")
}

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

				if usq := score.USPerQuarter(bpm); usq > 0 {
					tempos = append(tempos, score.Tempo{Tick: im.scale(abs), USPerQuarter: usq})
				} else {
					im.warnf("skipped tempo event at tick %d: non-positive microseconds per quarter", im.scale(abs))
				}
			case ev.Message.GetMetaMeter(&num, &den):

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

func (im *importer) normalize(rt *rawTrack) {
	moved, shifted := 0, 0

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

func (im *importer) assignFrets(rt *rawTrack) {

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

type barSpec struct {
	start    int64
	num, den int
}

func (im *importer) barSpecs(meters score.MeterMap, end int64) ([]barSpec, error) {

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

	if rt.wind != nil {
		tr.Wind = rt.wind
	} else {
		tr.Tuning = score.StandardTuning
	}

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
				continue
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

					Inferred: rt.wind == nil,
				})
			}
			sort.Slice(notes, func(i, j int) bool { return notes[i].String > notes[j].String })
			bar.AddBeat(segEnd-segStart, notes...)
		}
	}
	return tr
}

func lowestKey(tuning score.Tuning, capo int) int {
	low := tuning[0] + capo
	for _, open := range tuning {
		if open+capo < low {
			low = open + capo
		}
	}
	return low
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
