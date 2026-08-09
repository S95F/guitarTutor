// Command gengp writes testdata/fixture_riff.gp: the canonical 4-bar
// fixture riff (see docs/TEXTFORMAT.md) as a Guitar Pro 7/8-style .gp
// file — a plain ZIP archive holding Content/score.gpif, the
// id-referential GPIF XML document. It is the .gp member of the
// cross-format fixture corpus (ROADMAP Phase 0/3); internal/gpimport is
// tested against its exact tick content.
//
// The archive holds only the gpif entry. Real Guitar Pro 8 exports also
// carry sidecar entries (a VERSION marker, binary stylesheets, layout
// and part configuration) that we cannot reproduce faithfully, so they
// are omitted rather than faked; the importer ignores non-gpif entries
// either way, and its tests cover archives with extra entries.
//
// Honesty note: this fixture is self-authored from the publicly
// documented structure of the format (clean-room, docs/DECISIONS.md D3),
// so the generator and the importer share one understanding of the
// format. Files exported by Guitar Pro itself remain the untested gap —
// see testdata/README-gp.txt.
//
// Bar 4 is authored as two tied half notes, exactly as the .gtab source
// writes it; score.Events() merging them into one whole-note event is
// the round-trip invariant the corpus pins down.
package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// A gnote is one authored note: string and fret in the score convention
// (string 1 = highest-pitched) plus tie flags. The generator converts to
// GP's convention (string 0 = lowest-pitched) when emitting.
type gnote struct {
	str, fret  int
	tieO, tieD bool // tie origin (into next) / destination (from previous)
}

// A gbeat is one authored beat: a rhythm name and its notes (none = rest).
type gbeat struct {
	rhythm string
	notes  []gnote
}

// bars is the canonical fixture riff, bar by bar, matching
// docs/TEXTFORMAT.md and the .gtab/.mid fixtures.
var bars = [][]gbeat{
	{ // Bar 1: eight eighth notes.
		{"Eighth", []gnote{{str: 6, fret: 0}}},
		{"Eighth", []gnote{{str: 6, fret: 3}}},
		{"Eighth", []gnote{{str: 5, fret: 5}}},
		{"Eighth", []gnote{{str: 6, fret: 0}}},
		{"Eighth", []gnote{{str: 6, fret: 3}}},
		{"Eighth", []gnote{{str: 5, fret: 5}}},
		{"Eighth", []gnote{{str: 6, fret: 3}}},
		{"Eighth", []gnote{{str: 6, fret: 0}}},
	},
	{ // Bar 2: quarter, quarter, half.
		{"Quarter", []gnote{{str: 5, fret: 2}}},
		{"Quarter", []gnote{{str: 5, fret: 0}}},
		{"Half", []gnote{{str: 5, fret: 2}}},
	},
	{ // Bar 3: E5 power chord for a half, quarter rest, quarter note.
		{"Half", []gnote{{str: 6, fret: 0}, {str: 5, fret: 2}, {str: 4, fret: 2}}},
		{"Quarter", nil},
		{"Quarter", []gnote{{str: 6, fret: 3}}},
	},
	{ // Bar 4: whole-note low E as two tied halves.
		{"Half", []gnote{{str: 6, fret: 0, tieO: true}}},
		{"Half", []gnote{{str: 6, fret: 0, tieD: true}}},
	},
}

// nStrings is the fixture's string count (standard six-string tuning).
const nStrings = 6

func main() {
	out := flag.String("o", filepath.Join("testdata", "fixture_riff.gp"), "output path")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "gengp:", err)
		os.Exit(1)
	}
}

// run writes the fixture .gp archive to path.
func run(path string) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// CreateHeader with a zero Modified time keeps the archive bytes
	// reproducible run to run.
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "Content/score.gpif", Method: zip.Deflate})
	if err != nil {
		return err
	}
	if _, err := w.Write(buildGPIF()); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

// buildGPIF emits the id-referential GPIF document for the riff. Ids are
// assigned in emission order: rhythms by first use, notes and beats
// sequentially, one voice and one bar per master bar.
func buildGPIF() []byte {
	var b bytes.Buffer
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	// Pre-assign ids so referencing sections can be emitted in the
	// conventional order (masters first, leaf tables last).
	rhythmID := map[string]int{}
	var rhythmOrder []string
	type beatRec struct {
		id, rhythm int
		noteIDs    []int
	}
	type noteRec struct {
		id int
		n  gnote
	}
	var beatRecs []beatRec
	var noteRecs []noteRec
	voiceBeats := make([][]int, len(bars)) // beat ids per bar's voice
	for bi, bar := range bars {
		for _, bt := range bar {
			rid, ok := rhythmID[bt.rhythm]
			if !ok {
				rid = len(rhythmOrder)
				rhythmID[bt.rhythm] = rid
				rhythmOrder = append(rhythmOrder, bt.rhythm)
			}
			br := beatRec{id: len(beatRecs), rhythm: rid}
			for _, n := range bt.notes {
				nr := noteRec{id: len(noteRecs), n: n}
				noteRecs = append(noteRecs, nr)
				br.noteIDs = append(br.noteIDs, nr.id)
			}
			beatRecs = append(beatRecs, br)
			voiceBeats[bi] = append(voiceBeats[bi], br.id)
		}
	}

	w(`<?xml version="1.0" encoding="UTF-8"?>`)
	w(`<!-- Generated by tools/gengp: the canonical fixture riff as a GP7/8-style`)
	w(`     score.gpif payload. Self-authored fixture; see testdata/README-gp.txt. -->`)
	w(`<GPIF>`)
	w(`  <GPRevision>7.0.0</GPRevision>`)
	w(`  <Score>`)
	w(`    <Title>Fixture Riff</Title>`)
	w(`  </Score>`)
	w(`  <MasterTrack>`)
	w(`    <Tracks>0</Tracks>`)
	w(`    <Automations>`)
	w(`      <Automation>`)
	w(`        <Type>Tempo</Type>`)
	w(`        <Linear>false</Linear>`)
	w(`        <Bar>0</Bar>`)
	w(`        <Position>0</Position>`)
	w(`        <Visible>true</Visible>`)
	w(`        <Value>120 2</Value>`)
	w(`      </Automation>`)
	w(`    </Automations>`)
	w(`  </MasterTrack>`)
	w(`  <Tracks>`)
	w(`    <Track id="0">`)
	w(`      <Name>Guitar</Name>`)
	w(`      <Staves>`)
	w(`        <Staff>`)
	w(`          <Properties>`)
	w(`            <Property name="Tuning">`)
	// Open-string MIDI notes, low to high: standard EADGBE.
	w(`              <Pitches>40 45 50 55 59 64</Pitches>`)
	w(`            </Property>`)
	w(`            <Property name="CapoFret">`)
	w(`              <Fret>0</Fret>`)
	w(`            </Property>`)
	w(`          </Properties>`)
	w(`        </Staff>`)
	w(`      </Staves>`)
	w(`    </Track>`)
	w(`  </Tracks>`)

	w(`  <MasterBars>`)
	for bi := range bars {
		w(`    <MasterBar>`)
		w(`      <Time>4/4</Time>`)
		w(`      <Bars>%d</Bars>`, bi)
		w(`    </MasterBar>`)
	}
	w(`  </MasterBars>`)

	w(`  <Bars>`)
	for bi := range bars {
		w(`    <Bar id="%d">`, bi)
		w(`      <Voices>%d -1 -1 -1</Voices>`, bi)
		w(`    </Bar>`)
	}
	w(`  </Bars>`)

	w(`  <Voices>`)
	for bi, ids := range voiceBeats {
		w(`    <Voice id="%d">`, bi)
		w(`      <Beats>%s</Beats>`, joinInts(ids))
		w(`    </Voice>`)
	}
	w(`  </Voices>`)

	w(`  <Beats>`)
	for _, br := range beatRecs {
		w(`    <Beat id="%d">`, br.id)
		w(`      <Rhythm ref="%d" />`, br.rhythm)
		if len(br.noteIDs) > 0 {
			w(`      <Notes>%s</Notes>`, joinInts(br.noteIDs))
		}
		w(`    </Beat>`)
	}
	w(`  </Beats>`)

	w(`  <Notes>`)
	for _, nr := range noteRecs {
		w(`    <Note id="%d">`, nr.id)
		w(`      <Properties>`)
		w(`        <Property name="String">`)
		// GP counts string 0 at the lowest-pitched string; the score
		// model counts string 1 at the highest.
		w(`          <String>%d</String>`, nStrings-nr.n.str)
		w(`        </Property>`)
		w(`        <Property name="Fret">`)
		w(`          <Fret>%d</Fret>`, nr.n.fret)
		w(`        </Property>`)
		w(`      </Properties>`)
		if nr.n.tieO || nr.n.tieD {
			w(`      <Tie origin="%t" destination="%t" />`, nr.n.tieO, nr.n.tieD)
		}
		w(`    </Note>`)
	}
	w(`  </Notes>`)

	w(`  <Rhythms>`)
	for rid, name := range rhythmOrder {
		w(`    <Rhythm id="%d">`, rid)
		w(`      <NoteValue>%s</NoteValue>`, name)
		w(`    </Rhythm>`)
	}
	w(`  </Rhythms>`)
	w(`</GPIF>`)
	return b.Bytes()
}

// joinInts renders ids as the space-separated list GPIF uses.
func joinInts(ids []int) string {
	var b bytes.Buffer
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d", id)
	}
	return b.String()
}
