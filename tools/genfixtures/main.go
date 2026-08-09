// Command genfixtures writes testdata/fixture_riff.mid: the canonical
// 4-bar fixture riff (see docs/TEXTFORMAT.md) as a format-1 Standard MIDI
// File at PPQ 960, 120 BPM, 4/4, with one guitar track. It is the MIDI
// member of the cross-format fixture corpus required by ROADMAP Phase 0;
// internal/midiimport is tested against its exact tick content.
//
// Bar 4 is written as a single 3840-tick note — the .gtab source authors
// it as two tied halves, and score.Events() merging them back to one event
// is exactly the round-trip invariant the corpus exists to pin down.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"
)

// A note is one canonical riff note: start and duration in PPQ-960 ticks
// plus its MIDI key.
type note struct {
	start, dur int64
	key        uint8
}

// riff is the canonical fixture riff, matching docs/TEXTFORMAT.md bar by
// bar. Ticks are already at PPQ 960, so they are written verbatim.
var riff = []note{
	// Bar 1: eight eighth notes.
	{0, 480, 40}, {480, 480, 43}, {960, 480, 50}, {1440, 480, 40},
	{1920, 480, 43}, {2400, 480, 50}, {2880, 480, 43}, {3360, 480, 40},
	// Bar 2: quarter, quarter, half.
	{3840, 960, 47}, {4800, 960, 45}, {5760, 1920, 47},
	// Bar 3: E5 power chord for a half, quarter rest, quarter note.
	{7680, 1920, 40}, {7680, 1920, 47}, {7680, 1920, 52},
	{10560, 960, 43},
	// Bar 4: whole-note low E as one note (ties are a notation artifact).
	{11520, 3840, 40},
}

func main() {
	out := flag.String("o", filepath.Join("testdata", "fixture_riff.mid"), "output path")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "genfixtures:", err)
		os.Exit(1)
	}
}

// run writes the fixture SMF to path.
func run(path string) error {
	s := smf.NewSMF1()
	s.TimeFormat = smf.MetricTicks(960)

	// Track 0: conductor — title, tempo, and meter.
	var t0 smf.Track
	t0.Add(0, smf.MetaTrackSequenceName("Fixture Riff"))
	t0.Add(0, smf.MetaTempo(120))
	t0.Add(0, smf.MetaMeter(4, 4))
	t0.Close(0)
	if err := s.Add(t0); err != nil {
		return err
	}

	// Track 1: the guitar. Flatten the riff into note-on/off edges sorted
	// by tick, offs before ons at the same tick.
	type edge struct {
		tick int64
		on   bool
		key  uint8
	}
	var edges []edge
	for _, n := range riff {
		edges = append(edges, edge{n.start, true, n.key}, edge{n.start + n.dur, false, n.key})
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].tick != edges[j].tick {
			return edges[i].tick < edges[j].tick
		}
		return !edges[i].on && edges[j].on
	})

	var t1 smf.Track
	t1.Add(0, smf.MetaTrackSequenceName("Guitar"))
	t1.Add(0, midi.ProgramChange(0, 25)) // GM 25: steel-string acoustic
	prev := int64(0)
	for _, e := range edges {
		delta := uint32(e.tick - prev)
		prev = e.tick
		if e.on {
			t1.Add(delta, midi.NoteOn(0, e.key, 96))
		} else {
			t1.Add(delta, midi.NoteOff(0, e.key))
		}
	}
	t1.Close(0)
	if err := s.Add(t1); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := s.WriteFile(path); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}
