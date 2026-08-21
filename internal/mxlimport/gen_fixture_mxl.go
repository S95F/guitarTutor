//go:build ignore

package main

import (
	"archive/zip"
	"bytes"
	"log"
	"os"
)

const container = `<?xml version="1.0" encoding="UTF-8"?>
<container>
  <rootfiles>
    <rootfile full-path="fixture_riff.musicxml" media-type="application/vnd.recordare.musicxml+xml"/>
  </rootfiles>
</container>
`

func main() {
	doc, err := os.ReadFile("testdata/fixture_riff.musicxml")
	if err != nil {
		log.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"META-INF/container.xml", []byte(container)},
		{"fixture_riff.musicxml", doc},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := w.Write(f.data); err != nil {
			log.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("testdata/fixture_riff.mxl", buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote testdata/fixture_riff.mxl (%d bytes)", buf.Len())
}
