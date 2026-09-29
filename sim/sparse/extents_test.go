package sparse

import (
	"reflect"
	"testing"
)

func TestExtentSetArithmetic(t *testing.T) {
	set := Merge(nil, 10, 19)
	set = Merge(set, 30, 39)
	set = Merge(set, 20, 24)
	if want := []Extent{{10, 24}, {30, 39}}; !reflect.DeepEqual(set, want) {
		t.Fatalf("Merge = %v, want %v", set, want)
	}
	if got, want := Merge(set, 0, 100), []Extent{{0, 100}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge over everything = %v", got)
	}
	if got, want := Subtract(set, 15, 32), []Extent{{10, 14}, {33, 39}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Subtract = %v, want %v", got, want)
	}
	if got, want := Clip(set, 20, 35), []Extent{{20, 24}, {30, 35}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Clip = %v, want %v", got, want)
	}
	if got, want := Diff(set, []Extent{{12, 13}, {30, 39}}), []Extent{{10, 11}, {14, 24}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Diff = %v, want %v", got, want)
	}
	if got := Diff(set, set); len(got) != 0 {
		t.Fatalf("Diff of a set with itself = %v", got)
	}
}
