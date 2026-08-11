package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAWSChunkedReader_RoundTrip(t *testing.T) {
	// Encode "hello world" as a single chunk + terminal.
	// Format:
	//   "b\r\n"                           ← 11 == 0xb chunk size
	//   "hello world\r\n"                 ← payload
	//   "0\r\nx-amz-checksum-crc32:abc==\r\n\r\n"
	chunked := "b\r\nhello world\r\n0\r\nx-amz-checksum-crc32:abc==\r\n\r\n"
	r := newAWSChunkedReader(strings.NewReader(chunked))
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("got %q, want %q", string(got), "hello world")
	}
}

func TestAWSChunkedReader_MultipleChunks(t *testing.T) {
	// Two payload chunks plus terminal.
	chunked := "5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"
	r := newAWSChunkedReader(strings.NewReader(chunked))
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("got %q, want %q", string(got), "hello world")
	}
}

func TestAWSChunkedReader_SignedExtensionIgnored(t *testing.T) {
	// SDK signed-streaming chunks carry `;chunk-signature=<hex>` on
	// every size line. The decoder must ignore extensions.
	sig := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	chunked := "b;chunk-signature=" + sig + "\r\nhello world\r\n" +
		"0;chunk-signature=" + sig + "\r\n\r\n"
	r := newAWSChunkedReader(strings.NewReader(chunked))
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("got %q, want %q", string(got), "hello world")
	}
}

func TestAWSChunkedReader_NoTrailingHeader(t *testing.T) {
	// STREAMING-AWS4-HMAC-SHA256-PAYLOAD (no trailer) — terminal
	// chunk is followed only by a single \r\n.
	chunked := "5\r\nhello\r\n0\r\n\r\n"
	r := newAWSChunkedReader(strings.NewReader(chunked))
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", string(got), "hello")
	}
}

func TestAWSChunkedReader_ReadInSmallSlices(t *testing.T) {
	// Read 1 byte at a time to exercise the chunk-boundary path.
	chunked := "b\r\nhello world\r\n0\r\n\r\n"
	r := newAWSChunkedReader(strings.NewReader(chunked))
	var buf bytes.Buffer
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	if buf.String() != "hello world" {
		t.Errorf("got %q, want %q", buf.String(), "hello world")
	}
}

func TestAWSChunkedReader_InvalidHexSize(t *testing.T) {
	chunked := "ZZZ\r\nhello\r\n"
	r := newAWSChunkedReader(strings.NewReader(chunked))
	_, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "invalid hex chunk size") {
		t.Errorf("expected invalid-hex error; got %v", err)
	}
}

func TestAWSChunkedReader_MissingCRLF(t *testing.T) {
	// Chunk size line missing CRLF terminator.
	chunked := "b\nhello world\r\n0\r\n\r\n"
	r := newAWSChunkedReader(strings.NewReader(chunked))
	_, err := io.ReadAll(r)
	if err == nil || !strings.Contains(err.Error(), "missing CRLF") {
		t.Errorf("expected missing-CRLF error; got %v", err)
	}
}

func TestIsAWSChunkedRequest(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"plain put", map[string]string{}, false},
		{"plain sha256", map[string]string{"x-amz-content-sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}, false},
		{"content-encoding aws-chunked", map[string]string{"Content-Encoding": "aws-chunked"}, true},
		{"streaming signed (no trailer)", map[string]string{"x-amz-content-sha256": "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"}, true},
		{"streaming signed (trailer)", map[string]string{"x-amz-content-sha256": "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"}, true},
		{"streaming unsigned (trailer)", map[string]string{"x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}, true},
		{"decoded length sentinel only", map[string]string{"x-amz-decoded-content-length": "11"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			if got := isAWSChunkedRequest(h); got != tc.want {
				t.Errorf("isAWSChunkedRequest: got %v, want %v", got, tc.want)
			}
		})
	}
}
