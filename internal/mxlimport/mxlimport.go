// Package mxlimport reads MusicXML files — uncompressed .musicxml and the
// zipped .mxl container — into the score model.
//
// The supported subset is MusicXML 4.0 score-partwise, with MuseScore's
// exported files as the compatibility target (real MuseScore exports are
// the validation gap until users supply them; the committed fixtures are
// hand-authored in MuseScore's flavor). The sound-versus-display dualism
// is resolved toward sound throughout: <duration> is authoritative for
// timing (so <type>, <dot/>, and <time-modification> need no separate
// handling — the duration already reflects them), <tie> (sound) rather
// than <tied> (notation) drives tie merging, and <sound tempo> wins over
// <metronome> when a direction carries both.
//
// Subset boundaries, stated plainly:
//
//   - Only voice 1 and staff 1 are imported; notes in other voices or
//     staves are skipped with a warning (their durations still move the
//     time cursor, so voice-1 timing is unaffected).
//   - Grace notes and ornaments are skipped with a warning, as are
//     <unpitched> percussion notes (counted once per part, not per note —
//     a drum staff would otherwise bury every warning that matters).
//   - <attributes><transpose> is honored: <pitch> is the WRITTEN pitch and
//     the score model stores what SOUNDS, so chromatic + octave-change
//     (and <double/>) are added to every note. A guitar part notated in
//     treble clef with octave-change -1 — what MuseScore, Guitar Pro and
//     Finale all emit — would otherwise import an octave sharp.
//   - measure implicit="yes" (a pickup) is right-aligned to the barline.
//     The score model has no short bar, so the bar stays full and gains a
//     leading rest; the pickup still ends where beat 1 of bar 1 begins.
//   - Forward/backward repeats and voltas are EXPANDED into a flat play
//     order (see repeat.go). D.C./D.S./coda/fine jumps are not followed —
//     they warn, since a wrong expansion is worse than none.
//   - <backup> and <forward> are honored exactly — mishandling them is
//     the documented silent-corruption trap in partial MusicXML
//     implementations (docs/DECISIONS.md D3), so the cursor math is
//     covered by regression tests.
//   - <staff-tuning> (line 1 = the LOWEST string on the staff) and <capo>
//     map to the track's Tuning and Capo. <technical><string>/<fret>
//     (string 1 = the HIGHEST-pitched string, the same convention as the
//     score model) are used directly when present; notes without them get
//     an inferred fingering from internal/fretting, marked Inferred.
//   - A part whose declared MIDI program — or, failing that, whose part
//     name — names a wind instrument the score model knows imports as a
//     monophonic wind track: Tuning nil, every note on the single
//     chromatic lane at sounding pitch. Chords keep only their highest
//     note, authored <technical> fingerings are ignored, and notes below
//     the instrument's lowest note (or past MIDI 127) are dropped — each
//     with a warning. A <notations><slur> arc marks every note under it
//     past the first as TechSlur — slurred into, not re-tongued; slurs on
//     fretted parts are not imported (a hammer-on/pull-off mapping is a
//     separate decision). An explicit <staff-tuning> overrides the
//     classification back to fretted: a real tab staff is stronger
//     evidence than a program number.
//   - Everything else unsupported degrades to a warning, never an error;
//     underfull measures are padded with rests. The result always passes
//     score.Validate.
//
// Ticks are rescaled from the file's <divisions> to score.PPQ. Notes that
// cross beat or bar boundaries continue as Tied notes, which score.Events
// merges back into single events — the same round-trip invariant as
// internal/midiimport.
package mxlimport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/score/textfmt"
)

// DefaultProgram is the General MIDI program assumed for parts with no
// midi-instrument declaration: 25, steel-string acoustic guitar, matching
// the text format's and MIDI importer's default.
const DefaultProgram = 25

// zipMagic is the local-file-header signature every ZIP archive (and so
// every .mxl) starts with.
var zipMagic = []byte("PK\x03\x04")

// Decompressed-size caps for .mxl archive members — the zip-bomb guard.
// The sizes claimed in the archive's headers are untrusted, so the caps
// are enforced on the bytes actually inflated.
const (
	maxContainerBytes = 1 << 20  // META-INF/container.xml
	maxRootfileBytes  = 64 << 20 // the root MusicXML document
)

// Import parses a MusicXML document into a Score, returning human-readable
// warnings for everything the import changed, skipped, or inferred. The
// container is sniffed from the bytes — a ZIP signature means .mxl,
// anything else is parsed as uncompressed XML — so the caller's file
// extension does not matter. The first part containing notes becomes the
// RoleUser track, later ones RoleBacking. The result always passes
// score.Validate.
func Import(data []byte) (*score.Score, []string, error) {
	im := &importer{}
	if bytes.HasPrefix(data, zipMagic) {
		payload, err := im.extractMXL(data)
		if err != nil {
			return nil, im.warns, err
		}
		data = payload
	}
	doc, err := parseDoc(data)
	if err != nil {
		return nil, im.warns, err
	}
	return im.run(doc)
}

// ImportFile reads path and imports it via Import.
func ImportFile(path string) (*score.Score, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return Import(data)
}

// An importer holds one import's state: accumulated warnings and the
// tempo/meter entries collected while walking the parts.
type importer struct {
	warns  []string
	tempos score.TempoMap
	meters score.MeterMap
}

// warnf records one human-readable warning.
func (im *importer) warnf(format string, args ...any) {
	im.warns = append(im.warns, fmt.Sprintf(format, args...))
}

// extractMXL opens a .mxl ZIP container and returns the root MusicXML
// document named by META-INF/container.xml. A missing container.xml
// degrades to picking the first XML file in the archive, with a warning.
func (im *importer) extractMXL(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("opening .mxl container: %w", err)
	}
	rootPath := ""
	if f := zipEntry(zr, "META-INF/container.xml"); f != nil {
		b, err := readZipEntry(f, maxContainerBytes)
		if err != nil {
			return nil, fmt.Errorf("reading META-INF/container.xml: %w", err)
		}
		var c xmlContainer
		if err := xml.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("parsing META-INF/container.xml: %w", err)
		}
		for _, rf := range c.RootFiles {
			if rf.FullPath != "" {
				rootPath = rf.FullPath
				break
			}
		}
		if rootPath == "" {
			return nil, fmt.Errorf(".mxl META-INF/container.xml names no rootfile")
		}
	} else {
		im.warnf(".mxl has no META-INF/container.xml; using the first XML file in the archive")
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "META-INF/") {
				continue
			}
			switch strings.ToLower(path.Ext(f.Name)) {
			case ".xml", ".musicxml":
				rootPath = f.Name
			}
			if rootPath != "" {
				break
			}
		}
		if rootPath == "" {
			return nil, fmt.Errorf(".mxl contains no MusicXML document")
		}
	}
	f := zipEntry(zr, rootPath)
	if f == nil {
		return nil, fmt.Errorf(".mxl rootfile %q not found in the archive", rootPath)
	}
	return readZipEntry(f, maxRootfileBytes)
}

// zipEntry finds one archive member by exact name.
func zipEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// readZipEntry reads one archive member, erroring as soon as the
// decompressed data exceeds limit bytes.
func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf(".mxl member %q decompresses past the %d MiB limit", f.Name, limit>>20)
	}
	return b, nil
}

// parseDoc unmarshals a score-partwise document, with a clearer error for
// the score-timewise variant.
func parseDoc(data []byte) (*xmlScorePartwise, error) {
	var doc xmlScorePartwise
	if err := xml.Unmarshal(data, &doc); err != nil {
		if rootName(data) == "score-timewise" {
			return nil, fmt.Errorf("score-timewise MusicXML is not supported (only score-partwise)")
		}
		return nil, fmt.Errorf("parsing MusicXML: %w", err)
	}
	return &doc, nil
}

// rootName returns the document's root element name, or "".
func rootName(data []byte) string {
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

// run drives the import of a parsed document.
func (im *importer) run(doc *xmlScorePartwise) (*score.Score, []string, error) {
	// A title holding a line break or the "//" comment marker would import
	// a piece the .gtab writer refuses to save; clean it like every other
	// unrepresentable detail — degrade, and say so.
	title, changed := textfmt.CleanLabel(doc.title())
	if changed {
		im.warnf("title %q holds text a saved .gtab cannot (a line break or \"//\"); imported as %q", doc.title(), title)
	}
	s := &score.Score{Title: title}
	decls := map[string]*xmlScorePart{}
	for i := range doc.PartList.ScoreParts {
		sp := &doc.PartList.ScoreParts[i]
		decls[sp.ID] = sp
	}

	// One play order for the whole document: parts share barlines, so
	// expanding each part's repeats on its own could desynchronise them.
	order := im.playOrder(doc)

	var parts []*partData
	for pi := range doc.Parts {
		xp := &doc.Parts[pi]
		pd, err := im.parsePart(pi, decls[xp.ID], xp, order)
		if err != nil {
			return nil, im.warns, err
		}
		if len(pd.notes) == 0 {
			im.warnf("part %d (%s): no notes; skipped", pi+1, xp.ID)
			continue
		}
		parts = append(parts, pd)
	}

	sort.SliceStable(im.tempos, func(i, j int) bool { return im.tempos[i].Tick < im.tempos[j].Tick })
	s.Tempos = dedupe(im.tempos, func(t score.Tempo) int64 { return t.Tick })
	if len(s.Tempos) == 0 || s.Tempos[0].Tick != 0 {
		s.Tempos = append(score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}}, s.Tempos...)
	}
	sort.SliceStable(im.meters, func(i, j int) bool { return im.meters[i].Tick < im.meters[j].Tick })
	s.Meters = dedupe(im.meters, func(m score.Meter) int64 { return m.Tick })
	if len(s.Meters) == 0 || s.Meters[0].Tick != 0 {
		s.Meters = append(score.MeterMap{{Tick: 0, Num: 4, Den: 4}}, s.Meters...)
	}

	// Fingerings, overlap cleanup, and the global extent — a part's notes
	// may spill past its last measure (overfull measures warn above).
	var end int64
	kept := parts[:0]
	for _, pd := range parts {
		im.finish(pd)
		if len(pd.notes) == 0 {
			im.warnf("part %d (%s): every note was dropped; skipped", pd.index+1, pd.id)
			continue
		}
		kept = append(kept, pd)
		if pd.end > end {
			end = pd.end
		}
		for _, n := range pd.notes {
			if n.end > end {
				end = n.end
			}
		}
	}
	if len(kept) == 0 {
		return nil, im.warns, fmt.Errorf("no playable notes in file")
	}

	specs, err := barSpecs(s.Meters, end)
	if err != nil {
		return nil, im.warns, err
	}
	for i, pd := range kept {
		role := score.RoleBacking
		if i == 0 {
			role = score.RoleUser
		}
		s.Tracks = append(s.Tracks, buildTrack(pd, role, specs))
	}
	if err := s.Validate(); err != nil {
		return nil, im.warns, fmt.Errorf("imported score failed validation: %w", err)
	}
	return s, im.warns, nil
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
