package sidecar

import "testing"

func TestLastLinesKeepsTheLastFive(t *testing.T) {
	var l lastLines
	if got := l.String(); got != "it wrote nothing" {
		t.Fatalf("empty: %q", got)
	}
	for _, s := range []string{"a", "b", "c", "d", "e", "f\n"} {
		l.add(s)
	}
	if got, want := l.String(), "it wrote: b | c | d | e | f"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
