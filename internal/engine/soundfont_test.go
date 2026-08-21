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

// The engine driving the real SoundFont voice across the articulation seam.
// The stub-voice tests pin WHAT the engine asks for; these pin the one case
// where the voice may not do it — a continuation it refuses because the
// interval is past its bend range — and the engine must find out and release
// the origin note itself, or nothing ever will.

// buildAudibleTinySF2 assembles the smallest SoundFont meltysynth will parse
// — the pattern of internal/synth's buildTinySF2, duplicated here because
// test helpers do not cross packages — with one difference: the sample is a
// looped square wave instead of silence, so a note that is held keeps
// SOUNDING and a leak is measurable in rendered audio rather than in slot
// state this package cannot see.
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

	// INFO list: version and a bank name.
	info := &bytes.Buffer{}
	info.WriteString("INFO")
	chunk(info, "ifil", []byte{2, 0, 1, 0})
	chunk(info, "INAM", []byte("tiny\x00\x00"))

	// sdta list: 64 samples of a period-8 square wave, so the loop below
	// holds a sustained tone.
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

	// pdta list: one preset -> one instrument -> one sample, each list
	// closed by its terminator record.
	phdr := &bytes.Buffer{}
	name20(phdr, "P")
	u16(phdr, 0) // patch
	u16(phdr, 0) // bank
	u16(phdr, 0) // zone start
	u32(phdr, 0)
	u32(phdr, 0)
	u32(phdr, 0)
	name20(phdr, "EOP")
	u16(phdr, 0)
	u16(phdr, 0)
	u16(phdr, 1) // terminator zone start = 1: preset 0 has one zone
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
	u32(shdr, 0)     // start
	u32(shdr, 48)    // end
	u32(shdr, 8)     // loop start
	u32(shdr, 40)    // loop end
	u32(shdr, 48000) // sample rate
	shdr.WriteByte(60)
	shdr.WriteByte(0)
	u16(shdr, 0)
	u16(shdr, 1)                 // mono
	shdr.Write(make([]byte, 46)) // terminator record

	pgen := &bytes.Buffer{}
	u16(pgen, 41) // instrument generator -> instrument 0
	u16(pgen, 0)
	u16(pgen, 0) // terminator
	u16(pgen, 0)

	igen := &bytes.Buffer{}
	u16(igen, 54) // sampleModes -> continuous loop: a held note sustains
	u16(igen, 1)
	u16(igen, 53) // sampleID generator -> sample 0
	u16(igen, 0)
	u16(igen, 0) // terminator
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

// audibleSoundFontFactory loads buildAudibleTinySF2 through the public
// SoundFont path and returns its Factory.
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

// peak32 is the largest absolute sample in b.
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

// TestRefusedContinuationReleasesTheOrigin: a slide wider than the SoundFont
// voice's bend range is REFUSED by the voice — it attacks the destination
// afresh, the documented fallback — but the engine has already suppressed
// the origin note's release on the assumption of a takeover. Unless the
// voice reports the refusal and the engine releases the origin itself, that
// note is held forever: no scheduled NoteOff names it any more, and even the
// score-end releaseAll no longer knows it exists. The leak is audible — a
// tone still sounding through what the score writes as a rest — which is
// exactly how this test measures it.
func TestRefusedContinuationReleasesTheOrigin(t *testing.T) {
	// One 4/4 bar at the default 120 BPM: E2 (string 6 fret 0, key 40) for
	// a quarter, a slide up 20 semitones to fret 20 (key 60) — past the
	// voice's 12-semitone bend range — for a quarter, then a half rest.
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
	// At 120 BPM and 48 kHz a quarter is 24000 frames: the second note ends
	// at frame 48000. Render through it into the rest.
	l, r := make([]float32, 480), make([]float32, 480)
	for f := 0; f < 60000; f += len(l) {
		e.RenderFrames(l, r)
	}
	// 0.25 s into the written rest every release tail is long gone (the
	// tiny SoundFont's release is near-instant): anything still sounding is
	// a note that was never released.
	e.RenderFrames(l, r)
	const floor = 0.002
	if p := peak32(l); p > floor {
		t.Errorf("audio still sounding %g (> %g) in the rest after a refused takeover: the origin note was never released", p, floor)
	}
}
