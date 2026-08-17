package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestAMQPValueReaderRefusesDeepNesting pins the nesting bound the fuzz targets
// cannot reach on their own: their seeds are all shallow, so removing
// amqpMaxDepth would leave every one of them passing. A descriptor byte (0x00)
// recurses into readValue, so a run of them is nesting an attacker controls
// from the wire. The guard has to be what stops it — an "unexpected end of
// input" from running off the buffer proves only that the input was short.
func TestAMQPValueReaderRefusesDeepNesting(t *testing.T) {
	deep := &amqpValueReader{data: append(bytes.Repeat([]byte{0x00}, amqpMaxDepth+64), 0x45)}
	value, err := deep.readValue()
	if err == nil {
		t.Fatalf("nesting %d deep was accepted, value %v", amqpMaxDepth+64, value)
	}
	if !strings.Contains(err.Error(), "nesting too deep") {
		t.Fatalf("the refusal must come from the depth bound, got %q", err)
	}
	if value != nil {
		t.Fatalf("a refused value must come back nil, got %v", value)
	}

	// Positive control: the bound must not be refusing ordinary nesting. One
	// descriptor around an empty list is the shape a real frame carries.
	shallow := &amqpValueReader{data: []byte{0x00, 0x45, 0x45}}
	if _, err := shallow.readValue(); err != nil {
		t.Fatalf("an ordinary described value must still decode: %v", err)
	}
}

// FuzzAMQPParseFrame feeds arbitrary bytes through the Service Bus AMQP
// frame decoder (reachable from raw WebSocket binary frames). Malformed,
// short, or truncated frames must return an error, never panic.
func FuzzAMQPParseFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 8, 2, 0, 0, 0}) // empty (heartbeat) frame
	f.Add([]byte{0, 0, 0, 0})             // too short for header
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 2, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 16, 2, 0, 0, 0, 0x00, 0x53, 0x10, 0xd0, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := parseAMQPFrame(data)
		if err != nil {
			// A refused frame hands back nothing a caller that checked only
			// the payload could act on — no half-decoded performative, no
			// body carved out of a length the decoder rejected.
			if frame.fields != nil || frame.payload != nil || frame.desc != 0 ||
				frame.frameType != 0 || frame.channel != 0 {
				t.Fatalf("a refused frame must come back zero-valued, got %+v beside error %v", frame, err)
			}
			return
		}
		if len(frame.payload) > len(data) {
			t.Fatalf("the decoded payload is %d bytes, longer than the %d-byte frame it came from",
				len(frame.payload), len(data))
		}
	})
}

// FuzzAMQPReadValue exercises the AMQP value reader directly over arbitrary
// bytes (list/map/array length prefixes are attacker-controlled).
func FuzzAMQPReadValue(f *testing.F) {
	f.Add([]byte{0x45})                                     // empty list
	f.Add([]byte{0xd0, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}) // huge list count
	f.Add([]byte{0xf0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0xb1, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := &amqpValueReader{data: data}
		_, _ = r.readValue()
		// The reader's own bounds are the property worth holding it to: a
		// length prefix on this path is attacker-supplied, so the cursor must
		// never be walked past the buffer it was handed and the value budget
		// must stop a frame that declares more work than the bound allows.
		// (A zero value beside an error is ordinary and not asserted against.)
		if r.off > len(r.data) {
			t.Fatalf("the reader advanced its cursor to %d, past the %d bytes it was given", r.off, len(r.data))
		}
		if r.values > amqpMaxValues+1 {
			t.Fatalf("the reader decoded %d values, past the %d-value bound", r.values, amqpMaxValues)
		}
	})
}
