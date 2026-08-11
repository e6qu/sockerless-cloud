package main

// Fuzz target for the GCS Content-Range header parser — untrusted client input
// (a resumable-upload range line). Contract: never panic; a malformed range
// returns an error rather than out-of-order/negative offsets with nil error.

import "testing"

func FuzzParseGCSContentRange(f *testing.F) {
	for _, s := range []string{
		"", "bytes */*", "bytes 0-0/1", "bytes 0-99/100", "bytes 0-99/*",
		"bytes */100", "bytes -1-5/10", "bytes 5-1/10", "garbage",
		"bytes 9223372036854775807-9223372036854775807/9223372036854775807",
		"bytes a-b/c", "bytes 0-/10", "bytes /10",
	} {
		f.Add(s, int64(100))
	}
	f.Fuzz(func(t *testing.T, s string, chunkLen int64) {
		// end and total use -1 as the legitimate "unknown" sentinel (a `*` in
		// the range or total, per the resumable-upload protocol). The starting
		// offset, however, is always a concrete non-negative byte position.
		start, _, _, err := parseGCSContentRange(s, chunkLen)
		if err == nil && start < 0 {
			t.Fatalf("parseGCSContentRange(%q,%d) ok but start=%d < 0", s, chunkLen, start)
		}
	})
}
