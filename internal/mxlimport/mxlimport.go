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

const DefaultProgram = 25

var zipMagic = []byte("PK\x03\x04")

const (
	maxContainerBytes = 1 << 20
	maxRootfileBytes  = 64 << 20
)

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

func ImportFile(path string) (*score.Score, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return Import(data)
}

type importer struct {
	warns  []string
	tempos score.TempoMap
	meters score.MeterMap
}

func (im *importer) warnf(format string, args ...any) {
	im.warns = append(im.warns, fmt.Sprintf(format, args...))
}

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

func zipEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

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

func (im *importer) run(doc *xmlScorePartwise) (*score.Score, []string, error) {

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
