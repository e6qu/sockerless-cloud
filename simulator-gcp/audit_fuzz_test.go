package main

// parseGCSContentRange reads untrusted client input, a resumable-upload range
// line: it must never panic, and a malformed range returns an error, never a
// negative or out-of-order offset.

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
		// end and total use -1 for a `*` in the range or total, as the resumable-upload
		// protocol allows; the starting offset is always a concrete byte position.
		start, _, _, err := parseGCSContentRange(s, chunkLen)
		if err == nil && start < 0 {
			t.Fatalf("parseGCSContentRange(%q,%d) ok but start=%d < 0", s, chunkLen, start)
		}
	})
}
