package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"time"

	"github.com/S95F/musicTutor/internal/synth"
)

const (
	auditionHold = 0.30
	auditionTail = 0.20
	auditionGain = 0.5
)

func renderAudition(program, key int) []byte {
	v := synth.NewBuiltin(sampleRate, program)
	hold := int(auditionHold * sampleRate)
	tail := int(auditionTail * sampleRate)
	l := make([]float32, hold+tail)
	r := make([]float32, hold+tail)
	v.NoteOn(key, 0.85)
	v.Render(l[:hold], r[:hold])
	v.NoteOff(key)
	v.Render(l[hold:], r[hold:])
	fade := tail / 2
	for i := 0; i < fade; i++ {
		g := float32(fade-i) / float32(fade)
		l[hold+tail-fade+i] *= g
		r[hold+tail-fade+i] *= g
	}
	buf := make([]byte, 0, (hold+tail)*8)
	for i := range l {
		buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(l[i]*auditionGain))
		buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(r[i]*auditionGain))
	}
	return buf
}

func (o *shellOpener) audition(program, key int) {
	ctx, err := o.audioContext()
	if err != nil {
		return
	}
	p := ctx.NewPlayer(bytes.NewReader(renderAudition(program, key)))
	p.Play()
	go func() {
		for p.IsPlaying() {
			time.Sleep(50 * time.Millisecond)
		}
		_ = p.Close()
	}()
}
