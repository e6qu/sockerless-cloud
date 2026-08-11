package main

import (
	"encoding/binary"
	"testing"
)

// FuzzDecodeSSMInputFrame feeds arbitrary bytes (the frame comes off the ECS
// ExecuteCommand WebSocket) through the SSM input-frame decoder. A declared
// payload length must never slice past the buffer.
func FuzzDecodeSSMInputFrame(f *testing.F) {
	hdr := make([]byte, ssmFixedHeaderLen)
	f.Add([]byte{})
	f.Add(hdr)
	f.Add(append(append([]byte{}, hdr...), 1, 2, 3))
	overflow := make([]byte, ssmFixedHeaderLen)
	binary.BigEndian.PutUint32(overflow[116:120], 0xFFFFFFFF)
	f.Add(overflow)
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _, _ = decodeSSMInputFrame(b) // must not panic
	})
}
