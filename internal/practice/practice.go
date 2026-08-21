package practice

import (
	"math"
	"sync"

	"github.com/S95F/musicTutor/internal/pitch"
	"github.com/S95F/musicTutor/internal/score"
)

type Verdict int

const (
	VerdictHit Verdict = iota

	VerdictClose

	VerdictMiss
)

type NoteResult struct {
	Event score.NoteEvent

	OutFrame int64

	Verdict Verdict

	Matched bool

	ErrCents float64

	ErrFrames int64
}

type Stats struct{ Hit, Close, Miss int }

func (s Stats) Accuracy() float64 {
	n := s.Hit + s.Close + s.Miss
	if n == 0 {
		return 1
	}
	return (float64(s.Hit) + 0.5*float64(s.Close)) / float64(n)
}

type Config struct {
	SampleRate int

	Track int

	TimingWindowFrames int

	CentsTolerance float64

	CloseCents float64

	LatencyOffsetFrames int

	ChordPresenceRatio float64

	ChordCorrelationMin float64

	MuteEnergyRatio float64
}

const (
	defaultSampleRate     = 48000
	defaultWindowMillis   = 150
	defaultCentsTolerance = 35
	defaultCloseCents     = 70

	defaultChordPresence = 0.45

	defaultChordCorrMin = 0.30

	defaultMuteEnergy = 0.10
)

func (cfg Config) withDefaults() Config {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = defaultSampleRate
	}
	if cfg.TimingWindowFrames <= 0 {
		cfg.TimingWindowFrames = cfg.SampleRate * defaultWindowMillis / 1000
	}
	if cfg.CentsTolerance <= 0 {
		cfg.CentsTolerance = defaultCentsTolerance
	}
	if cfg.CloseCents <= 0 {
		cfg.CloseCents = defaultCloseCents
	}
	if cfg.CloseCents < cfg.CentsTolerance {
		cfg.CloseCents = cfg.CentsTolerance
	}
	if cfg.ChordPresenceRatio <= 0 {
		cfg.ChordPresenceRatio = defaultChordPresence
	}
	if cfg.ChordCorrelationMin <= 0 {
		cfg.ChordCorrelationMin = defaultChordCorrMin
	}
	if cfg.MuteEnergyRatio <= 0 {
		cfg.MuteEnergyRatio = defaultMuteEnergy
	}
	return cfg
}

type expectation struct {
	ev          score.NoteEvent
	outFrame    int64
	abandoned   bool
	onset       bool
	onsetProm   float64
	onsetFrames int64
}

type preMatch struct {
	track    int
	key      int
	str      int
	start    int64
	verdict  Verdict
	errCents float64
	born     int64
	expire   int64
}

const preMatchExpirySeconds = 5

type Scorer struct {
	mu       sync.Mutex
	cfg      Config
	pending  []expectation
	results  []NoteResult
	preMatch []preMatch
	clock    int64
	stats    Stats

	strumCand []int
}

func NewScorer(cfg Config) *Scorer {
	return &Scorer{
		cfg: cfg.withDefaults(),

		pending:   make([]expectation, 0, 256),
		results:   make([]NoteResult, 0, 256),
		preMatch:  make([]preMatch, 0, 8),
		strumCand: make([]int, 0, 16),
	}
}

func (s *Scorer) ExpectNote(ev score.NoteEvent, outFrame int64) {
	if ev.Track != s.cfg.Track {
		return
	}
	s.mu.Lock()
	if outFrame > s.clock {
		s.clock = outFrame
	}
	for i := range s.preMatch {
		p := &s.preMatch[i]
		if p.track == ev.Track && p.key == ev.Key && p.str == ev.String && p.start == ev.Start && s.clock <= p.expire {
			s.finalize(NoteResult{
				Event:    ev,
				OutFrame: outFrame,
				Verdict:  p.verdict,
				Matched:  true,
				ErrCents: p.errCents,
			})
			last := len(s.preMatch) - 1
			s.preMatch[i] = s.preMatch[last]
			s.preMatch = s.preMatch[:last]
			s.mu.Unlock()
			return
		}
	}
	s.pending = append(s.pending, expectation{ev: ev, outFrame: outFrame})
	s.mu.Unlock()
}

const (
	tierClass     = 1
	tierAbandoned = 2
)

func (s *Scorer) Detected(notes []pitch.Note) {
	s.mu.Lock()
	defer s.mu.Unlock()
	win := int64(s.cfg.TimingWindowFrames)
	off := int64(s.cfg.LatencyOffsetFrames)
	for i := range notes {
		n := &notes[i]
		detOut := n.Start - off
		if detOut > s.clock {
			s.clock = detOut
		}
		pick, pickTier := -1, 0
		var pickOut int64
		for j := range s.pending {
			exp := &s.pending[j]
			dt := detOut - exp.outFrame
			if dt < -win || dt > win {
				continue
			}
			tier := 0
			switch {
			case exp.ev.Key == n.Key:
			case ((exp.ev.Key-n.Key)%12+12)%12 == 0:
				tier |= tierClass
			default:
				continue
			}
			if exp.abandoned {
				tier |= tierAbandoned
			}
			if pick < 0 || tier < pickTier || (tier == pickTier && exp.outFrame < pickOut) {
				pick, pickTier, pickOut = j, tier, exp.outFrame
			}
		}
		if pick < 0 {
			continue
		}
		exp := s.pending[pick]
		s.pending = append(s.pending[:pick], s.pending[pick+1:]...)
		v := VerdictClose
		if pickTier&tierClass == 0 && math.Abs(n.Cents) <= s.cfg.CentsTolerance {
			v = VerdictHit
		}
		s.finalize(NoteResult{
			Event:     exp.ev,
			OutFrame:  exp.outFrame,
			Verdict:   v,
			Matched:   true,
			ErrCents:  n.Cents,
			ErrFrames: detOut - exp.outFrame,
		})
	}

	for i := range notes {
		s.matchSlides(&notes[i], off, win)
	}
}

func (s *Scorer) matchSlides(n *pitch.Note, off, win int64) {
	startOut := n.Start - off

	endOut := int64(math.MaxInt64)
	if n.End > 0 {
		endOut = n.End - off
	}
	keep := s.pending[:0]
	for _, exp := range s.pending {
		v, cents, ok := s.slideVerdict(n, exp, startOut, endOut, win)
		if !ok {
			keep = append(keep, exp)
			continue
		}
		s.finalize(NoteResult{
			Event:    exp.ev,
			OutFrame: exp.outFrame,
			Verdict:  v,
			Matched:  true,
			ErrCents: cents,
		})
	}
	s.pending = keep
}

func (s *Scorer) slideVerdict(n *pitch.Note, exp expectation, startOut, endOut, win int64) (Verdict, float64, bool) {
	if exp.ev.Tech&score.TechSlide == 0 {
		return 0, 0, false
	}

	if exp.ev.Key == n.Key {
		return 0, 0, false
	}
	if startOut > exp.outFrame+win || endOut < exp.outFrame-win {
		return 0, 0, false
	}

	want := float64(exp.ev.Key-n.Key) * 100
	if err := n.EndCents - want; math.Abs(err) <= s.cfg.CentsTolerance {
		return VerdictHit, err, true
	}

	if want >= n.MinCents-s.cfg.CentsTolerance && want <= n.MaxCents+s.cfg.CentsTolerance {
		return VerdictClose, 0, true
	}
	return 0, 0, false
}

func (s *Scorer) DetectedStrum(st pitch.Strum) {
	s.mu.Lock()
	defer s.mu.Unlock()
	win := int64(s.cfg.TimingWindowFrames)
	off := int64(s.cfg.LatencyOffsetFrames)
	stOut := st.Frame - off
	if stOut > s.clock {
		s.clock = stOut
	}

	cand := s.strumCand[:0]
	for j := range s.pending {
		if dt := stOut - s.pending[j].outFrame; dt >= -win && dt <= win {
			cand = append(cand, j)
		}
	}
	s.strumCand = cand
	if len(cand) == 0 {
		return
	}

	stats := chromaStatsOf(st.Chroma)
	for _, j := range cand {
		exp := &s.pending[j]
		p := stats.prominence(float64(st.Chroma[pitch.ChromaOf(exp.ev.Key)]))
		if !exp.onset || p > exp.onsetProm {
			exp.onsetProm = p
			exp.onsetFrames = stOut - exp.outFrame
		}
		exp.onset = true
	}

	if group := s.chordGroup(cand, stOut); group >= 0 {
		s.verifyChord(st, stOut, s.pending[group].ev.Start, s.pending[group].outFrame, stats)
	}
}

const chordProximityMillis = 50

func (s *Scorer) chordGroup(cand []int, stOut int64) int {
	nearest := int64(-1)
	for _, j := range cand {
		if s.chordSize(cand, j) < 2 {
			continue
		}
		if d := absInt64(stOut - s.pending[j].outFrame); nearest < 0 || d < nearest {
			nearest = d
		}
	}
	if nearest < 0 {
		return -1
	}
	slop := int64(s.cfg.SampleRate) * chordProximityMillis / 1000
	group, groupN, groupD := -1, 0, int64(0)
	for _, j := range cand {
		n := s.chordSize(cand, j)
		d := absInt64(stOut - s.pending[j].outFrame)
		if n < 2 || d > nearest+slop {
			continue
		}
		if group < 0 || n > groupN || (n == groupN && d < groupD) {
			group, groupN, groupD = j, n, d
		}
	}
	return group
}

func (s *Scorer) chordSize(cand []int, j int) int {
	n := 0
	for _, k := range cand {
		if s.pending[k].ev.Start == s.pending[j].ev.Start && s.pending[k].outFrame == s.pending[j].outFrame {
			n++
		}
	}
	return n
}

func (s *Scorer) verifyChord(st pitch.Strum, stOut, start, outFrame int64, stats chromaStats) {
	if stats.peak <= 0 {
		return
	}
	var tmpl [pitch.PitchClasses]bool
	for j := range s.pending {
		if s.pending[j].ev.Start == start && s.pending[j].outFrame == outFrame {
			tmpl[pitch.ChromaOf(s.pending[j].ev.Key)] = true
		}
	}
	if chordCorrelation(st.Chroma, tmpl) < s.cfg.ChordCorrelationMin {
		return
	}
	hitThresh := s.cfg.ChordPresenceRatio * stats.peak
	rival := rivalBin(st.Chroma, tmpl)
	keep := s.pending[:0]
	for _, exp := range s.pending {
		if exp.ev.Start != start || exp.outFrame != outFrame {
			keep = append(keep, exp)
			continue
		}
		v := float64(st.Chroma[pitch.ChromaOf(exp.ev.Key)])
		r := NoteResult{Event: exp.ev, OutFrame: exp.outFrame, Verdict: VerdictMiss}
		switch {
		case v >= hitThresh && v >= rival:
			r.Verdict = VerdictHit
		case stats.prominence(v) >= s.cfg.MuteEnergyRatio:
			r.Verdict = VerdictClose
		}
		if r.Verdict != VerdictMiss {

			r.Matched = true
			r.ErrFrames = stOut - exp.outFrame
		}
		s.finalize(r)
	}
	s.pending = keep
}

type chromaStats struct {
	peak float64
	bg   float64
}

func chromaStatsOf(ch pitch.Chroma) chromaStats {
	var sorted [pitch.PitchClasses]float64
	for i, v := range ch {
		sorted[i] = float64(v)
	}

	for i := 1; i < len(sorted); i++ {
		v := sorted[i]
		j := i - 1
		for ; j >= 0 && sorted[j] > v; j-- {
			sorted[j+1] = sorted[j]
		}
		sorted[j+1] = v
	}
	return chromaStats{peak: sorted[len(sorted)-1], bg: sorted[len(sorted)/4]}
}

func (s chromaStats) prominence(v float64) float64 {
	if s.peak <= s.bg {
		return 0
	}
	return (v - s.bg) / (s.peak - s.bg)
}

func rivalBin(ch pitch.Chroma, tmpl [pitch.PitchClasses]bool) float64 {
	rival := 0.0
	for i, v := range ch {
		if !tmpl[i] && float64(v) > rival {
			rival = float64(v)
		}
	}
	return rival
}

func chordCorrelation(ch pitch.Chroma, tmpl [pitch.PitchClasses]bool) float64 {
	var sumC, sumT float64
	for i, v := range ch {
		sumC += float64(v)
		if tmpl[i] {
			sumT++
		}
	}
	meanC := sumC / pitch.PitchClasses
	meanT := sumT / pitch.PitchClasses
	var dot, normC, normT float64
	for i, v := range ch {
		a := float64(v) - meanC
		b := -meanT
		if tmpl[i] {
			b = 1 - meanT
		}
		dot += a * b
		normC += a * a
		normT += b * b
	}
	if normC <= 0 || normT <= 0 {
		return 0
	}
	return dot / math.Sqrt(normC*normT)
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (s *Scorer) Advance(inFrame int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	win := int64(s.cfg.TimingWindowFrames)
	off := int64(s.cfg.LatencyOffsetFrames)
	if out := inFrame - off; out > s.clock {
		s.clock = out
	}
	keepPM := s.preMatch[:0]
	for _, p := range s.preMatch {
		if s.clock <= p.expire {
			keepPM = append(keepPM, p)
		}
	}
	s.preMatch = keepPM
	keep := s.pending[:0]
	for _, exp := range s.pending {
		if exp.outFrame+off+win < inFrame {

			if !exp.abandoned {
				s.finalize(s.deadlineResult(exp))
			}
			continue
		}
		keep = append(keep, exp)
	}
	s.pending = keep
}

func (s *Scorer) deadlineResult(exp expectation) NoteResult {
	r := NoteResult{Event: exp.ev, OutFrame: exp.outFrame, Verdict: VerdictMiss}
	if exp.onset && exp.onsetProm >= s.cfg.MuteEnergyRatio {
		r.Verdict = VerdictClose
		r.Matched = true
		r.ErrFrames = exp.onsetFrames
	}
	return r
}

func (s *Scorer) AbandonBefore(outFrame int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pending {
		if s.pending[i].outFrame < outFrame {
			s.pending[i].abandoned = true
		}
	}

	keep := s.preMatch[:0]
	for _, p := range s.preMatch {
		if p.born >= outFrame {
			keep = append(keep, p)
		}
	}
	s.preMatch = keep
}

func (s *Scorer) WaitConfirmed(evs []score.NoteEvent, notes []pitch.Note) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range evs {
		if ev.Track != s.cfg.Track {
			continue
		}
		best, bestExact := -1, false
		var bestAbs float64
		for i := range notes {
			n := &notes[i]
			exact := n.Key == ev.Key
			if !exact && ((ev.Key-n.Key)%12+12)%12 != 0 {
				continue
			}
			abs := math.Abs(n.Cents)
			switch {
			case best < 0, exact && !bestExact:
				best, bestExact, bestAbs = i, exact, abs
			case exact == bestExact && abs < bestAbs:
				best, bestAbs = i, abs
			}
		}
		slideIdx := -1
		if ev.Tech&score.TechSlide != 0 {
			slideIdx = slideConfirmer(notes, ev.Key, s.cfg.CentsTolerance)
		}
		v, cents := VerdictClose, 0.0
		switch {
		case best >= 0:
			if bestExact && bestAbs <= s.cfg.CentsTolerance {
				v = VerdictHit
			}
			cents = notes[best].Cents
		case slideIdx >= 0:

			n := &notes[slideIdx]
			if err := n.EndCents - float64(ev.Key-n.Key)*100; math.Abs(err) <= s.cfg.CentsTolerance {
				v, cents = VerdictHit, err
			}
		case ev.Tech&score.TechDead != 0:

		default:
			continue
		}
		pm := preMatch{
			track:    ev.Track,
			key:      ev.Key,
			str:      ev.String,
			start:    ev.Start,
			verdict:  v,
			errCents: cents,
			born:     s.clock,
			expire:   s.clock + int64(preMatchExpirySeconds*s.cfg.SampleRate),
		}
		replaced := false
		for i := range s.preMatch {
			p := &s.preMatch[i]
			if p.track == pm.track && p.key == pm.key && p.str == pm.str && p.start == pm.start {
				*p = pm
				replaced = true
				break
			}
		}
		if !replaced {
			s.preMatch = append(s.preMatch, pm)
		}
	}
}

func (s *Scorer) finalize(r NoteResult) {
	s.results = append(s.results, r)
	switch r.Verdict {
	case VerdictHit:
		s.stats.Hit++
	case VerdictClose:
		s.stats.Close++
	case VerdictMiss:
		s.stats.Miss++
	}
}

func (s *Scorer) Results(dst []NoteResult) []NoteResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst = append(dst, s.results...)
	s.results = s.results[:0]
	return dst
}

func (s *Scorer) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Scorer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = s.pending[:0]
	s.results = s.results[:0]
	s.preMatch = s.preMatch[:0]
	s.stats = Stats{}
}

type WaitGate struct {
	mu         sync.Mutex
	closeCents float64

	slideCents float64
	events     []score.NoteEvent
	done       []bool
	nDone      int

	fired    bool
	minStart int64

	credited []attack
	spent    []attack
}

type attack struct {
	start int64
	key   int
}

func NewWaitGate(cfg Config) *WaitGate {
	c := cfg.withDefaults()
	return &WaitGate{closeCents: c.CloseCents, slideCents: c.CentsTolerance}
}

func (g *WaitGate) Arm(events []score.NoteEvent, minStart int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.spent = append(g.spent, g.credited...)
	g.credited = g.credited[:0]
	keep := g.spent[:0]
	for _, a := range g.spent {
		if a.start >= minStart {
			keep = append(keep, a)
		}
	}
	g.spent = keep
	g.events = append(g.events[:0], events...)
	g.done = append(g.done[:0], make([]bool, len(events))...)
	g.nDone = 0
	g.fired = false
	g.minStart = minStart
}

func (g *WaitGate) Offer(notes []pitch.Note) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) == 0 {
		return false
	}
	for i := range notes {
		n := &notes[i]

		fresh := n.Start >= g.minStart && !g.isSpent(n)
		inTune := math.Abs(n.Cents) <= g.closeCents
		for j := range g.events {
			if g.done[j] {
				continue
			}
			ev := &g.events[j]
			switch {

			case ev.Tech&score.TechSlide != 0 && settledAt(n, ev.Key, g.slideCents):

			case fresh && inTune && ev.Key == n.Key:
			default:
				continue
			}
			g.done[j] = true
			g.nDone++
			g.credited = append(g.credited, attack{start: n.Start, key: n.Key})
			break
		}
	}
	return g.satisfied()
}

func (g *WaitGate) OfferStrum(st pitch.Strum) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.events) == 0 {
		return false
	}
	if st.Frame >= g.minStart {
		for j := range g.events {
			if !g.done[j] && g.events[j].Tech&score.TechDead != 0 {
				g.done[j] = true
				g.nDone++
			}
		}
	}
	return g.satisfied()
}

func (g *WaitGate) satisfied() bool {
	if g.fired || len(g.events) == 0 || g.nDone != len(g.events) {
		return false
	}
	g.fired = true
	return true
}

func (g *WaitGate) isSpent(n *pitch.Note) bool {
	for _, a := range g.spent {
		if a.start == n.Start && a.key == n.Key {
			return true
		}
	}
	return false
}

func settledAt(n *pitch.Note, key int, slop float64) bool {
	if key == n.Key {
		return false
	}
	return math.Abs(n.EndCents-float64(key-n.Key)*100) <= slop
}

func reachedKey(n *pitch.Note, key int, slop float64) bool {

	if key == n.Key {
		return false
	}
	want := float64(key-n.Key) * 100
	return want >= n.MinCents-slop && want <= n.MaxCents+slop
}

func slideConfirmer(notes []pitch.Note, key int, slop float64) int {
	for i := range notes {
		if reachedKey(&notes[i], key, slop) {
			return i
		}
	}
	return -1
}

func ConfirmsSlide(n pitch.Note, ev score.NoteEvent, cfg Config) bool {
	if ev.Tech&score.TechSlide == 0 {
		return false
	}
	return settledAt(&n, ev.Key, cfg.withDefaults().CentsTolerance)
}
