package synth

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/sinshu/go-meltysynth/meltysynth"
)

func TestLoadSoundFontErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
		return path
	}

	tests := []struct {
		name string
		path string
	}{
		{"missing file", filepath.Join(dir, "missing.sf2")},
		{"empty file", write("empty.sf2", nil)},
		{"garbage bytes", write("garbage.sf2", []byte("this is not a soundfont"))},
		{"truncated riff", write("truncated.sf2", []byte("RIFF\x08\x00\x00\x00sfbk"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf, err := LoadSoundFont(tt.path)
			if err == nil {
				t.Fatalf("LoadSoundFont(%q) succeeded, want error", tt.path)
			}
			if sf != nil {
				t.Errorf("LoadSoundFont(%q) = %v with error, want nil", tt.path, sf)
			}
		})
	}
}

func TestNewSoundFontFactoryNilSafety(t *testing.T) {
	if f := NewSoundFontFactory(nil); f != nil {
		t.Error("NewSoundFontFactory(nil) != nil, want nil so callers can fall back to NewPluck")
	}
	if f := NewSoundFontFactory(&SoundFont{}); f != nil {
		t.Error("NewSoundFontFactory(unloaded SoundFont) != nil, want nil")
	}
}

func TestSoundFontVoice(t *testing.T) {
	const path = "testdata/test.sf2"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no %s (drop any GM SoundFont there to run this test)", path)
	}
	sf, err := LoadSoundFont(path)
	if err != nil {
		t.Fatalf("LoadSoundFont(%q): %v", path, err)
	}
	factory := NewSoundFontFactory(sf)
	if factory == nil {
		t.Fatal("NewSoundFontFactory returned nil for a loaded SoundFont")
	}
	v := factory(48000, 25)

	left := make([]float32, 512)
	right := make([]float32, 512)
	for i := range left {
		left[i], right[i] = 0.25, -0.25
	}
	v.Render(left, right)
	for i := range left {
		if left[i] != 0.25 || right[i] != -0.25 {
			t.Fatalf("silent Render disturbed the mix at %d: %g, %g", i, left[i], right[i])
		}
	}

	v.NoteOn(40, 0.8)
	l2, r2 := renderFrames(v, 4800, 480)
	if peak(l2) == 0 && peak(r2) == 0 {
		t.Fatal("no signal after NoteOn")
	}

	v.AllNotesOff()
	l3, r3 := renderFrames(v, 4800, 480)
	const floor = 0.001
	if p := peak(l3[128:]); p > floor {
		t.Errorf("left peak %g after AllNotesOff, want <= %g", p, floor)
	}
	if p := peak(r3[128:]); p > floor {
		t.Errorf("right peak %g after AllNotesOff, want <= %g", p, floor)
	}
}

func buildTinySF2() []byte {
	le := binary.LittleEndian
	u16 := func(b *bytes.Buffer, v uint16) { binary.Write(b, le, v) }
	u32 := func(b *bytes.Buffer, v uint32) { binary.Write(b, le, v) }
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

	sdta := &bytes.Buffer{}
	sdta.WriteString("sdta")
	chunk(sdta, "smpl", make([]byte, 128))

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
	gens := func(genType, value uint16) []byte {
		b := &bytes.Buffer{}
		u16(b, genType)
		u16(b, value)
		u16(b, 0)
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

	pdta := &bytes.Buffer{}
	pdta.WriteString("pdta")
	chunk(pdta, "phdr", phdr.Bytes())
	chunk(pdta, "pbag", bag(0, 1))
	chunk(pdta, "pmod", make([]byte, 10))
	chunk(pdta, "pgen", gens(41, 0))
	chunk(pdta, "inst", inst.Bytes())
	chunk(pdta, "ibag", bag(0, 1))
	chunk(pdta, "imod", make([]byte, 10))
	chunk(pdta, "igen", gens(53, 0))
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

func tinySoundFontVoice(t *testing.T) *soundFontVoice {
	t.Helper()
	sf, err := meltysynth.NewSoundFont(bytes.NewReader(buildTinySF2()))
	if err != nil {
		t.Fatalf("parsing the built-in tiny SoundFont: %v", err)
	}
	v, ok := newSoundFontVoice(sf, 48000, 25).(*soundFontVoice)
	if !ok {
		t.Fatal("newSoundFontVoice fell back to the pluck")
	}
	return v
}

func heldSlot(t *testing.T, v *soundFontVoice) *sfSlot {
	t.Helper()
	var held *sfSlot
	for i := range v.slots {
		if s := &v.slots[i]; s.held {
			if held != nil {
				t.Fatal("two slots held, want one")
			}
			held = s
		}
	}
	if held == nil {
		t.Fatal("no held slot")
	}
	return held
}

func TestSoundFontChainedLegatoKeepsPitch(t *testing.T) {
	steps := []struct {
		name string
		from int
		key  int
	}{
		{"first slur up a third", 60, 64},
		{"chained slur up again", 64, 67},
		{"chained slur back down", 67, 60},
	}
	v := tinySoundFontVoice(t)
	v.NoteOn(60, 0.9)
	for _, st := range steps {
		v.NoteOnSpec(NoteSpec{Key: st.key, Velocity: 0.9, Attack: AttackLegato, From: st.from})
		s := heldSlot(t, v)
		if s.logical != int32(st.key) {
			t.Fatalf("%s: slot logical key %d, want %d", st.name, s.logical, st.key)
		}
		if got := float64(s.key) + s.bend; got != float64(st.key) {
			t.Errorf("%s: channel sounds MIDI %.1f (struck %d, bend %+.1f), want %d",
				st.name, got, s.key, s.bend, st.key)
		}
	}
}

func TestSoundFontChainedSlideArrives(t *testing.T) {
	v := tinySoundFontVoice(t)
	v.NoteOn(60, 0.9)
	v.NoteOnSpec(NoteSpec{Key: 65, Velocity: 0.9, Attack: AttackSlide, From: 60})
	v.NoteOnSpec(NoteSpec{Key: 62, Velocity: 0.9, Attack: AttackSlide, From: 65})
	s := heldSlot(t, v)
	if got := float64(s.key) + s.dst; got != 62 {
		t.Errorf("chained slide heads for MIDI %.1f (struck %d, dst %+.1f), want 62",
			got, s.key, s.dst)
	}
}

func TestSoundFontNoteOnSpecReport(t *testing.T) {
	v := tinySoundFontVoice(t)
	if v.NoteOnSpecReport(NoteSpec{Key: 60, Velocity: 0.9}) {
		t.Error("a plain attack reported as a continuation")
	}
	if !v.NoteOnSpecReport(NoteSpec{Key: 64, Velocity: 0.9, Attack: AttackLegato, From: 60}) {
		t.Error("an in-range takeover reported as a fresh attack")
	}
	if v.NoteOnSpecReport(NoteSpec{Key: 80, Velocity: 0.9, Attack: AttackLegato, From: 64}) {
		t.Error("a refused takeover (20 semitones from the struck key) reported as a continuation")
	}
	if v.NoteOnSpecReport(NoteSpec{Key: 62, Velocity: 0, Attack: AttackLegato, From: 80}) {
		t.Error("an ignored zero-velocity note reported as a continuation")
	}
}

func TestSoundFontLegatoBeyondBendRangeReattacks(t *testing.T) {
	v := tinySoundFontVoice(t)
	v.NoteOn(60, 0.9)
	v.NoteOnSpec(NoteSpec{Key: 70, Velocity: 0.9, Attack: AttackLegato, From: 60})

	v.NoteOnSpec(NoteSpec{Key: 80, Velocity: 0.9, Attack: AttackLegato, From: 70})
	var sounding *sfSlot
	for i := range v.slots {
		if s := &v.slots[i]; s.held && s.logical == 80 {
			sounding = s
		}
	}
	if sounding == nil {
		t.Fatal("no held slot at the chain's last note")
	}
	if got := float64(sounding.key) + sounding.bend; got != 80 {
		t.Errorf("chain's last note sounds MIDI %.1f, want 80", got)
	}
}
