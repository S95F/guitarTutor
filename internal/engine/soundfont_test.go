package engine

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/S95F/musicTutor/internal/score"
	"github.com/S95F/musicTutor/internal/synth"
)

func buildAudibleTinySF2() []byte {
	le := binary.LittleEndian
	u16 := func(b *bytes.Buffer, v uint16) { binary.Write(b, le, v) }
	u32 := func(b *bytes.Buffer, v uint32) { binary.Write(b, le, v) }
	i16 := func(b *bytes.Buffer, v int16) { binary.Write(b, le, v) }
	name20 := func(b *bytes.Buffer, s string) {
		var n [20]byte
		copy(n[:], s)
		b.Write(n[:])
	}
	chunk := func(b *bytes.Buffer, id string, body []byte) {
		b.WriteString(id)
		u32(b, uint32(len(body)))
		b.Write(body)
	}

	info := &bytes.Buffer{}
	info.WriteString("INFO")
	chunk(info, "ifil", []byte{2, 0, 1, 0})
	chunk(info, "INAM", []byte("tiny\x00\x00"))

	smpl := &bytes.Buffer{}
	for i := 0; i < 64; i++ {
		if (i/4)%2 == 0 {
			i16(smpl, 12000)
		} else {
			i16(smpl, -12000)
		}
	}
	sdta := &bytes.Buffer{}
	sdta.WriteString("sdta")
	chunk(sdta, "smpl", smpl.Bytes())

	phdr := &bytes.Buffer{}
	name20(phdr, "P")
	u16(phdr, 0)
	u16(phdr, 0)
	u16(phdr, 0)
	u32(phdr, 0)
	u32(phdr, 0)
	u32(phdr, 0)
	name20(phdr, "EOP")
	u16(phdr, 0)
	u16(phdr, 0)
	u16(phdr, 1)
	u32(phdr, 0)
	u32(phdr, 0)
	u32(phdr, 0)

	bag := func(gen0, gen1 uint16) []byte {
		b := &bytes.Buffer{}
		u16(b, gen0)
		u16(b, 0)
		u16(b, gen1)
		u16(b, 0)
		return b.Bytes()
	}

	inst := &bytes.Buffer{}
	name20(inst, "I")
	u16(inst, 0)
	name20(inst, "EOI")
	u16(inst, 1)

	shdr := &bytes.Buffer{}
	name20(shdr, "S")
	u32(shdr, 0)
	u32(shdr, 48)
	u32(shdr, 8)
	u32(shdr, 40)
	u32(shdr, 48000)
	shdr.WriteByte(60)
	shdr.WriteByte(0)
	u16(shdr, 0)
	u16(shdr, 1)
	shdr.Write(make([]byte, 46))

	pgen := &bytes.Buffer{}
	u16(pgen, 41)
	u16(pgen, 0)
	u16(pgen, 0)
	u16(pgen, 0)

	igen := &bytes.Buffer{}
	u16(igen, 54)
	u16(igen, 1)
	u16(igen, 53)
	u16(igen, 0)
	u16(igen, 0)
	u16(igen, 0)

	pdta := &bytes.Buffer{}
	pdta.WriteString("pdta")
	chunk(pdta, "phdr", phdr.Bytes())
	chunk(pdta, "pbag", bag(0, 1))
	chunk(pdta, "pmod", make([]byte, 10))
	chunk(pdta, "pgen", pgen.Bytes())
	chunk(pdta, "inst", inst.Bytes())
	chunk(pdta, "ibag", bag(0, 2))
	chunk(pdta, "imod", make([]byte, 10))
	chunk(pdta, "igen", igen.Bytes())
	chunk(pdta, "shdr", shdr.Bytes())

	body := &bytes.Buffer{}
	body.WriteString("sfbk")
	chunk(body, "LIST", info.Bytes())
	chunk(body, "LIST", sdta.Bytes())
	chunk(body, "LIST", pdta.Bytes())

	out := &bytes.Buffer{}
	chunk(out, "RIFF", body.Bytes())
	return out.Bytes()
}

func audibleSoundFontFactory(t *testing.T) synth.Factory {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tiny.sf2")
	if err := os.WriteFile(path, buildAudibleTinySF2(), 0o644); err != nil {
		t.Fatalf("writing the tiny SoundFont: %v", err)
	}
	sf, err := synth.LoadSoundFont(path)
	if err != nil {
		t.Fatalf("loading the tiny SoundFont: %v", err)
	}
	f := synth.NewSoundFontFactory(sf)
	if f == nil {
		t.Fatal("NewSoundFontFactory returned nil for a loaded SoundFont")
	}
	return f
}

func peak32(b []float32) float32 {
	var p float32
	for _, v := range b {
		if v < 0 {
			v = -v
		}
		if v > p {
			p = v
		}
	}
	return p
}

func TestRefusedContinuationReleasesTheOrigin(t *testing.T) {

	s := &score.Score{
		Tempos: score.TempoMap{{Tick: 0, USPerQuarter: score.USPerQuarter(120)}},
		Meters: score.MeterMap{{Tick: 0, Num: 4, Den: 4}},
	}
	tr := &score.Track{Name: "Guitar", Tuning: score.StandardTuning}
	s.Tracks = []*score.Track{tr}
	b := tr.AppendBar(4, 4)
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 0})
	b.AddBeat(score.Quarter, score.Note{String: 6, Fret: 20, Tech: score.TechSlide})
	b.AddBeat(score.Half)
	if err := s.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}

	e := New(s, Options{Voices: audibleSoundFontFactory(t)})
	e.Play()

	l, r := make([]float32, 480), make([]float32, 480)
	for f := 0; f < 60000; f += len(l) {
		e.RenderFrames(l, r)
	}

	e.RenderFrames(l, r)
	const floor = 0.002
	if p := peak32(l); p > floor {
		t.Errorf("audio still sounding %g (> %g) in the rest after a refused takeover: the origin note was never released", p, floor)
	}
}
