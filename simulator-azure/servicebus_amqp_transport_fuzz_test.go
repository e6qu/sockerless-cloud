package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

// The raw transport is fed by a pre-auth TLS listener, so an absurd size
// prefix must error, not allocate. Not panicking proves nothing: without the
// bound, make([]byte, 4294967295) succeeds lazily under overcommit and
// io.ReadFull returns ErrUnexpectedEOF, so the oracle reads the size prefix as
// the reader does and holds it to the bound.
func FuzzSBAMQPReadFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{'A', 'M', 'Q', 'P', 0, 1, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	// One byte past sbAMQPMaxFrameSize: small enough that a reader without the
	// bound would allocate it rather than fail.
	f.Add([]byte{0x01, 0x00, 0x00, 0x01, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 12, 2, 0, 0, 0, 1, 2, 3, 4})
	// A size the bound admits with the body cut short: the reader must not return
	// its zero-padded buffer beside the error.
	f.Add([]byte{0, 0, 0, 12, 2, 0, 0, 0, 1})
	f.Add([]byte{0x00, 0x30, 0x2f, 0x30, 2, 0, 0, 0, 1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := sbAMQPReadFrame(bufio.NewReader(bytes.NewReader(data)))

		if len(frame) > sbAMQPMaxFrameSize {
			t.Fatalf("the reader returned a %d-byte frame, past the %d-byte bound", len(frame), sbAMQPMaxFrameSize)
		}
		if err != nil && frame != nil {
			t.Fatalf("a refused frame must come back nil, got %d bytes beside error %v", len(frame), err)
		}
		if len(data) < 8 {
			return
		}
		if bytes.Equal(data[:4], []byte("AMQP")) {
			// The protocol-id header is returned verbatim.
			if err == nil && len(frame) != 8 {
				t.Fatalf("the AMQP protocol header must come back as its 8 bytes, got %d", len(frame))
			}
			return
		}
		size := binary.BigEndian.Uint32(data[:4])
		switch {
		case size < 8 || uint64(size) > uint64(sbAMQPMaxFrameSize):
			if err == nil {
				t.Fatalf("a declared frame size of %d is outside [8, %d] and must be refused", size, sbAMQPMaxFrameSize)
			}
		case err == nil && uint64(len(frame)) != uint64(size):
			t.Fatalf("an accepted frame must be exactly its declared size %d, got %d", size, len(frame))
		}
	})
}
