package fretting

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/S95F/musicTutor/internal/score"
)

func single(keys ...int) [][]int {
	beats := make([][]int, len(keys))
	for i, k := range keys {
		beats[i] = []int{k}
	}
	return beats
}

func TestPentatonicLowPosition(t *testing.T) {

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

func TestAssignPathologicalWideStaff(t *testing.T) {
	tuning := make(score.Tuning, 40)
	for i := range tuning {
		tuning[i] = 40
	}
	chord := make([]int, 15)
	for i := range chord {
		chord[i] = 40
	}
	start := time.Now()
	got, unp := Assign([][]int{chord}, tuning, 0)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Assign took %v, want under 1s (search budget not applied?)", elapsed)
	}
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	seen := map[int]bool{}
	for i, p := range got[0] {
		if p.String == 0 {
			t.Fatalf("chord note %d got no assignment", i)
		}
		if seen[p.String] {
			t.Errorf("string %d assigned to more than one chord note", p.String)
		}
		seen[p.String] = true
	}
}

func TestAssignBudgetFallsBackGreedy(t *testing.T) {
	tuning := make(score.Tuning, 25)
	for i := range tuning {
		tuning[i] = 40
	}
	chord := []int{45, 46, 50, 51, 55, 56}
	start := time.Now()
	got, unp := Assign([][]int{chord}, tuning, 0)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Assign took %v, want under 1s (search budget not applied?)", elapsed)
	}
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none (25 strings fit 6 notes greedily)", unp)
	}
	seen := map[int]bool{}
	for i, p := range got[0] {
		if p.String == 0 {
			t.Fatalf("chord note %d got no assignment", i)
		}
		if seen[p.String] {
			t.Errorf("string %d assigned to more than one chord note", p.String)
		}
		seen[p.String] = true
	}
	got2, unp2 := Assign([][]int{chord}, tuning, 0)
	if !reflect.DeepEqual(got, got2) || !reflect.DeepEqual(unp, unp2) {
		t.Errorf("budget fallback is not deterministic:\n%v %v\n%v %v", got, unp, got2, unp2)
	}
}

func TestAssignManyStringsDistinct(t *testing.T) {
	tuning := make(score.Tuning, 33)
	for i := range tuning {
		tuning[i] = 120
	}
	tuning[31], tuning[32] = 40, 40
	got, unp := Assign([][]int{{40, 40}}, tuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	a, b := got[0][0], got[0][1]
	if a.String == 0 || b.String == 0 {
		t.Fatalf("assignments = %v/%v, want both notes placed", a, b)
	}
	if a.String == b.String {
		t.Errorf("both notes landed on string %d; want distinct strings (mask aliasing above string 31)", a.String)
	}
}

func TestCapoShiftsRange(t *testing.T) {

	got, unp := Assign([][]int{{41}, {42}}, score.StandardTuning, 2)
	if len(unp) != 1 || unp[0].Key != 41 {
		t.Fatalf("unplayable = %v, want one report for key 41", unp)
	}
	if got[1][0] != (Position{String: 6, Fret: 0}) {
		t.Errorf("key 42 with capo 2 = %v, want 6/0", got[1][0])
	}
}

func TestAssignWideTuningJointFingering(t *testing.T) {
	tuning := score.Tuning{69, 64, 59, 54, 50, 46, 42, 40, 37, 34, 30, 27, 22, 18}
	keys := []int{55, 64, 42, 51, 65, 53, 62, 63}
	beats := [][]int{keys}

	start := time.Now()
	positions, unplayable := Assign(beats, tuning, 0)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("assignment took %v, want well under a second", elapsed)
	}
	if len(unplayable) != 0 {
		t.Fatalf("a jointly-fingerable chord reported unplayables: %v", unplayable)
	}
	if len(positions[0]) != len(keys) {
		t.Fatalf("placed %d of %d notes", len(positions[0]), len(keys))
	}
	seen := map[int]bool{}
	minF, maxF := 0, 0
	for _, p := range positions[0] {
		if p.String == 0 {
			t.Fatalf("a note came back unplaced: %v", positions[0])
		}
		if seen[p.String] {
			t.Fatalf("two notes on string %d: %v", p.String, positions[0])
		}
		seen[p.String] = true
		if p.Fret > 0 {
			if minF == 0 || p.Fret < minF {
				minF = p.Fret
			}
			if p.Fret > maxF {
				maxF = p.Fret
			}
		}
	}
	if maxF-minF > MaxSpan {
		t.Errorf("fingering spans frets %d-%d, past MaxSpan %d", minF, maxF, MaxSpan)
	}
}

func TestAssignEmptyTuning(t *testing.T) {
	got, unp := Assign([][]int{{40, 64}}, score.Tuning{}, 0)
	if len(unp) != 2 {
		t.Fatalf("unplayable = %v, want both keys reported", unp)
	}
	for _, u := range unp {
		if !strings.Contains(u.Reason, "no strings") {
			t.Errorf("key %d reason = %q, want it to name the empty tuning", u.Key, u.Reason)
		}
	}
	for _, p := range got[0] {
		if p != (Position{}) {
			t.Errorf("got %v, want no position at all", p)
		}
	}
}

func TestAssignWithFixedHoldsHandPosition(t *testing.T) {
	fixed := [][]Position{{{String: 6, Fret: 12}, {String: 5, Fret: 12}}}
	got, unp := AssignWith([][]int{{67}}, fixed, score.StandardTuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	if got[0][0] != (Position{String: 3, Fret: 12}) {
		t.Errorf("key 67 beside a 12th-fret shape = %v, want 3/12", got[0][0])
	}

	bare, _ := Assign([][]int{{67}}, score.StandardTuning, 0)
	if bare[0][0] != (Position{String: 1, Fret: 3}) {
		t.Errorf("key 67 alone = %v, want 1/3 (the position the context has to override)", bare[0][0])
	}
}

func TestAssignWithFixedHoldsItsStrings(t *testing.T) {
	fixed := [][]Position{{{String: 1, Fret: 0}}}
	got, unp := AssignWith([][]int{{64}}, fixed, score.StandardTuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	if got[0][0].String == 1 {
		t.Errorf("key 64 = %v, but string 1 is held by a fixed note", got[0][0])
	}
	if got[0][0] != (Position{String: 2, Fret: 5}) {
		t.Errorf("key 64 = %v, want the next-cheapest free string 2/5", got[0][0])
	}
}

func TestAssignWithWideFixedChordStillPlaces(t *testing.T) {
	fixed := [][]Position{{{String: 6, Fret: 10}, {String: 5, Fret: 17}}}
	got, unp := AssignWith([][]int{{67}}, fixed, score.StandardTuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	if got[0][0] != (Position{String: 3, Fret: 12}) {
		t.Errorf("key 67 beside a 10-17 stretch = %v, want 3/12, inside it", got[0][0])
	}
}

func TestAssignWithUnreachableFixedKeepsNote(t *testing.T) {

	fixed := [][]Position{{{String: 6, Fret: 1}}}
	got, unp := AssignWith([][]int{{71}}, fixed, score.StandardTuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none — the note is playable, just far", unp)
	}
	if got[0][0].String == 0 {
		t.Fatal("key 71 came back unplaced")
	}
	if got[0][0].String == 6 {
		t.Errorf("key 71 = %v, but string 6 is held by the fixed note", got[0][0])
	}
}

func TestAssignWithShortFixedLeavesBeatsFree(t *testing.T) {
	beats := [][]int{{67}, {67}}
	fixed := [][]Position{{{String: 6, Fret: 12}, {String: 5, Fret: 12}}}
	got, unp := AssignWith(beats, fixed, score.StandardTuning, 0)
	if len(unp) != 0 {
		t.Fatalf("unplayable = %v, want none", unp)
	}
	if got[0][0] != (Position{String: 3, Fret: 12}) {
		t.Errorf("beat 0 = %v, want 3/12 beside the fixed shape", got[0][0])
	}
	if got[1][0].Fret > MaxFret || got[1][0].String == 0 {
		t.Fatalf("beat 1 = %v, want a real position", got[1][0])
	}
	if got[1][0].Fret < 8 {
		t.Errorf("beat 1 = %v; with no fixed notes of its own it still pays to "+
			"stay near beat 0's hand rather than jump to fret 3", got[1][0])
	}
}
