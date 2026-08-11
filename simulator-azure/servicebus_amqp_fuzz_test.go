package main

import "testing"

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
		_, _ = parseAMQPFrame(data) // must not panic
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
		_, _ = r.readValue() // must not panic
	})
}
