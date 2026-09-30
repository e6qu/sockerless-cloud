package main

import (
	"reflect"
	"testing"
)

func TestDecodeStreamElementsStopsWhereTheClientHungUp(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"complete stream", `[{"a":1},{"a":2}]`, 2},
		{"empty body", ``, 0},
		{"opened, nothing sent", `[`, 0},
		{"cut after an element", `[{"a":1},`, 1},
		{"cut inside an element", `[{"a":1},{"a":`, 1},
	}
	for _, c := range cases {
		got, err := decodeStreamElements([]byte(c.body))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Fatalf("%s: decoded %d elements, want %d", c.name, len(got), c.want)
		}
	}
	got, _ := decodeStreamElements([]byte(`[{"a":1}]`))
	if !reflect.DeepEqual(got, []any{map[string]any{"a": float64(1)}}) {
		t.Fatalf("decoded %#v", got)
	}
	for _, bad := range []string{`{"a":1}`, `[{"a":}]`, `[1 2]`} {
		if _, err := decodeStreamElements([]byte(bad)); err == nil {
			t.Fatalf("%q decoded as a stream", bad)
		}
	}
}
