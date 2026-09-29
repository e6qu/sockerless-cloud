package main

import (
	"bytes"
	"context"
	"io"
	"testing"
)

// chunkedAMQPTransport hands the connection its input in fixed chunks, the
// way WebSocket messages carrying an AMQP byte stream may split it, and
// records what the connection writes back.
type chunkedAMQPTransport struct {
	chunks [][]byte
	wrote  bytes.Buffer
}

func (t *chunkedAMQPTransport) Read(context.Context) ([]byte, error) {
	if len(t.chunks) == 0 {
		return nil, io.EOF
	}
	next := t.chunks[0]
	t.chunks = t.chunks[1:]
	return next, nil
}

func (t *chunkedAMQPTransport) Write(data []byte) error {
	_, err := t.wrote.Write(data)
	return err
}

func (t *chunkedAMQPTransport) Close() error { return nil }

// TestSBAMQPConnReassemblesFramesSplitAcrossReads proves the connection treats
// its transport as the byte stream AMQP is: a protocol header and an open
// frame split at any offset are handled exactly as when they arrive whole.
func TestSBAMQPConnReassemblesFramesSplitAcrossReads(t *testing.T) {
	encoder := &chunkedAMQPTransport{}
	if err := newSBAMQPConn("", encoder).writeFrame(amqpFrameTypeAMQP, 0,
		encodeDescribedList(amqpDescOpen, []any{"client", "split-ns.servicebus.windows.net"})); err != nil {
		t.Fatalf("encode open frame: %v", err)
	}
	stream := append([]byte{'A', 'M', 'Q', 'P', 0, 1, 0, 0}, encoder.wrote.Bytes()...)

	whole := &chunkedAMQPTransport{chunks: [][]byte{stream}}
	wholeConn := newSBAMQPConn("", whole)
	wholeConn.serve(context.Background())
	if wholeConn.currentNamespace() != "split-ns" || whole.wrote.Len() == 0 {
		t.Fatalf("the whole stream must open the connection: namespace %q, %d bytes written",
			wholeConn.currentNamespace(), whole.wrote.Len())
	}

	for size := 1; size < len(stream); size++ {
		split := &chunkedAMQPTransport{}
		for start := 0; start < len(stream); start += size {
			split.chunks = append(split.chunks, stream[start:min(start+size, len(stream))])
		}
		conn := newSBAMQPConn("", split)
		conn.serve(context.Background())
		if conn.currentNamespace() != "split-ns" {
			t.Fatalf("chunks of %d bytes: namespace %q, want split-ns", size, conn.currentNamespace())
		}
		if !bytes.Equal(split.wrote.Bytes(), whole.wrote.Bytes()) {
			t.Fatalf("chunks of %d bytes: the connection answered %x, want %x", size, split.wrote.Bytes(), whole.wrote.Bytes())
		}
	}
}
