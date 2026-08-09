package fretting

import (
	"reflect"
	"testing"

	"github.com/S95F/guitarTutor/internal/score"
)

// single wraps a melodic line as one-key beats.
func single(keys ...int) [][]int {
	beats := make([][]int, len(keys))
	for i, k := range keys {
		beats[i] = []int{k}
	}
	return beats
}

func TestPentatonicLowPosition(t *testing.T) {
	// An ascending E minor pentatonic line must map to the obvious
	// open-position fingering.
	keys := []int{40, 43, 45, 47, 50, 52, 55, 57, 59, 62, 64, 67}
	want := []Position{
		{6, 0}, {6, 3}, {5, 0}, {5, 2}, {4, 0}, {4, 2},
		{3, 0}, {3, 2}, {2, 0}, {2, 3}, {1, 0}, {1, 3},
	}
	got, unp := Assign(single(keys...), score.StandardTuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	for i := range keys {
		if got[i][0] != want[i] {
			t.Errorf("key %d: got %d/%d, want %d/%d",
				keys[i], got[i][0].String, got[i][0].Fret, want[i].String, want[i].Fret)
		}
	}
}

func TestPowerChordFingering(t *testing.T) {
	// The canonical riff's bar-3 chord: E5 = keys 40,47,52 must land on
	// strings 6/5/4 at frets 0/2/2 in standard tuning.
	got, unp := Assign([][]int{{40, 47, 52}}, score.StandardTuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	want := []Position{{6, 0}, {5, 2}, {4, 2}}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("got %v, want %v", got[0], want)
	}
}

func TestUnplayable(t *testing.T) {
	tests := []struct {
		name string
		key  int
	}{
		{"below low E", 39},
		{"far below low E", 20},
		{"above fret 24 on the high string", 89},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unp := Assign([][]int{{tt.key, 45}}, score.StandardTuning, 0)
			if len(unp) != 1 {
				t.Fatalf("got %d unplayable reports, want 1", len(unp))
			}
			if unp[0].Beat != 0 || unp[0].Key != tt.key || unp[0].Reason == "" {
				t.Errorf("report = %+v, want beat 0 key %d with a reason", unp[0], tt.key)
			}
			if got[0][0] != (Position{}) {
				t.Errorf("unplayable key assigned %v, want zero Position", got[0][0])
			}
			if got[0][1] == (Position{}) {
				t.Errorf("playable neighbor got no assignment")
			}
		})
	}
}

func TestChordDistinctStrings(t *testing.T) {
	// Two notes that can only live on string 6: the second must be
	// reported, not doubled onto the same string.
	got, unp := Assign([][]int{{40, 40}}, score.StandardTuning, 0)
	if got[0][0] != (Position{String: 6, Fret: 0}) {
		t.Errorf("first note = %v, want 6/0", got[0][0])
	}
	if len(unp) != 1 || unp[0].Key != 40 {
		t.Fatalf("unplayable = %v, want one report for key 40", unp)
	}
	if got[0][1] != (Position{}) {
		t.Errorf("second note = %v, want zero Position", got[0][1])
	}
}

func TestAssignStable(t *testing.T) {
	// Same input, same output: the heuristic must be deterministic.
	beats := [][]int{
		{40, 47, 52}, {43}, {50, 57}, {}, {45, 52, 57, 61, 64}, {40}, {64, 67},
	}
	p1, u1 := Assign(beats, score.StandardTuning, 0)
	p2, u2 := Assign(beats, score.StandardTuning, 0)
	if !reflect.DeepEqual(p1, p2) {
		t.Errorf("positions differ across runs:\n%v\n%v", p1, p2)
	}
	if !reflect.DeepEqual(u1, u2) {
		t.Errorf("unplayable reports differ across runs:\n%v\n%v", u1, u2)
	}
}

func TestCapoShiftsRange(t *testing.T) {
	// With a capo at 2 the lowest sounding note is F#2 (42); key 41 is
	// unplayable and key 42 is the open sixth string.
	got, unp := Assign([][]int{{41}, {42}}, score.StandardTuning, 2)
	if len(unp) != 1 || unp[0].Key != 41 {
		t.Fatalf("unplayable = %v, want one report for key 41", unp)
	}
	if got[1][0] != (Position{String: 6, Fret: 0}) {
		t.Errorf("key 42 with capo 2 = %v, want 6/0", got[1][0])
	}
}
