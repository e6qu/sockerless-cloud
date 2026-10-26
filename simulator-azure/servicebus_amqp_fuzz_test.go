package main

import (
	"bytes"
	"strings"
	"testing"
)

// The fuzz seeds are all shallow and would pass without amqpMaxDepth. A run of
// descriptor bytes (0x00) recurses into readValue; the guard must be what stops
// it, not running off the buffer.
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

	// Positive control: one descriptor around an empty list, a real frame's shape.
	shallow := &amqpValueReader{data: []byte{0x00, 0x45, 0x45}}
	if _, err := shallow.readValue(); err != nil {
		t.Fatalf("an ordinary described value must still decode: %v", err)
	}
}

// The decoder is reachable from raw WebSocket binary frames.
func FuzzAMQPParseFrame(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 8, 2, 0, 0, 0}) // empty (heartbeat) frame
	f.Add([]byte{0, 0, 0, 0})             // too short for header
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 2, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 16, 2, 0, 0, 0, 0x00, 0x53, 0x10, 0xd0, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := parseAMQPFrame(data)
		if err != nil {
			// A refused frame hands back no half-decoded performative or payload.
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

// List, map and array length prefixes are attacker-controlled.
func FuzzAMQPReadValue(f *testing.F) {
	f.Add([]byte{0x45})                                     // empty list
	f.Add([]byte{0xd0, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}) // huge list count
	f.Add([]byte{0xf0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0xb1, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := &amqpValueReader{data: data}
		_, _ = r.readValue()
		// The cursor must never pass the buffer and the value budget must stop
		// over-declared work. A zero value beside an error is ordinary.
		if r.off > len(r.data) {
			t.Fatalf("the reader advanced its cursor to %d, past the %d bytes it was given", r.off, len(r.data))
		}
		if r.values > amqpMaxValues+1 {
			t.Fatalf("the reader decoded %d values, past the %d-value bound", r.values, amqpMaxValues)
		}
	})
}
