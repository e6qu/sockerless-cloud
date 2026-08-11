package main

import (
	"bufio"
	"bytes"
	"testing"
)

// FuzzSBAMQPReadFrame feeds arbitrary bytes (the raw transport is fed by a
// pre-auth TLS listener) through the length-prefixed frame reader. An absurd
// size prefix must error, not allocate gigabytes.
func FuzzSBAMQPReadFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{'A', 'M', 'Q', 'P', 0, 1, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 12, 2, 0, 0, 0, 1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = sbAMQPReadFrame(bufio.NewReader(bytes.NewReader(data))) // must not OOM/panic
	})
}
