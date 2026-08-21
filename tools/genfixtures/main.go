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

type note struct {
	start, dur int64
	key        uint8
}

var riff = []note{

	{0, 480, 40}, {480, 480, 43}, {960, 480, 50}, {1440, 480, 40},
	{1920, 480, 43}, {2400, 480, 50}, {2880, 480, 43}, {3360, 480, 40},

	{3840, 960, 47}, {4800, 960, 45}, {5760, 1920, 47},

	{7680, 1920, 40}, {7680, 1920, 47}, {7680, 1920, 52},
	{10560, 960, 43},

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

func run(path string) error {
	s := smf.NewSMF1()
	s.TimeFormat = smf.MetricTicks(960)

	var t0 smf.Track
	t0.Add(0, smf.MetaTrackSequenceName("Fixture Riff"))
	t0.Add(0, smf.MetaTempo(120))
	t0.Add(0, smf.MetaMeter(4, 4))
	t0.Close(0)
	if err := s.Add(t0); err != nil {
		return err
	}

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
	t1.Add(0, midi.ProgramChange(0, 25))
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
