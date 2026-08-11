package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// awsChunkedReader unwraps a body sent with AWS chunked-encoding —
// the framing used by aws-sdk-go-v2 (and other AWS SDKs) when the
// request body is non-seekable and the SDK must stream the payload.
//
// Real S3 unwraps this framing server-side; the sim must do the same
// or it would store the chunk-encoding envelope verbatim.
//
// Wire format (per AWS documentation + aws-sdk-go-v2's
// internal/checksum/aws_chunked_encoding.go encoder, inverted):
//
//	<chunk-size-hex>[;<extension>=<value>]\r\n
//	<payload bytes of size chunk-size>\r\n
//	<chunk-size-hex>[;<extension>=<value>]\r\n
//	<payload bytes>\r\n
//	...
//	0[;<extension>=<value>]\r\n
//	[<trailer-header>: <value>\r\n]*
//	\r\n
//
// Extensions (the `;name=value` suffix after the size) include
// `chunk-signature=<hex>` for the streaming-signed variant; the
// decoder ignores extensions because per-chunk signature checking
// would require replaying the SigV4 streaming-payload chain and is
// out of scope for the sim's auth-passthrough model. Trailer
// headers are read and discarded — they carry per-payload checksums
// like `x-amz-checksum-crc32` that real S3 validates after decode;
// the sim currently does not enforce them.
//
// Sentinel headers that select this decoder live in the caller:
//
//	Content-Encoding: aws-chunked
//	x-amz-content-sha256: STREAMING-AWS4-HMAC-SHA256-PAYLOAD
//	x-amz-content-sha256: STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER
//	x-amz-content-sha256: STREAMING-UNSIGNED-PAYLOAD-TRAILER
//	x-amz-content-sha256: STREAMING-UNSIGNED-PAYLOAD
//
// `x-amz-decoded-content-length` carries the unwrapped payload
// length; callers can use it as a sentinel and/or pre-size the
// destination buffer.
type awsChunkedReader struct {
	br        *bufio.Reader
	remaining int64 // bytes left in the current chunk payload
	done      bool
}

func newAWSChunkedReader(r io.Reader) *awsChunkedReader {
	return &awsChunkedReader{br: bufio.NewReader(r)}
}

// Read decodes the next chunked-envelope bytes into p, returning
// only the unwrapped payload.
func (a *awsChunkedReader) Read(p []byte) (int, error) {
	if a.done {
		return 0, io.EOF
	}
	if a.remaining == 0 {
		if err := a.advance(); err != nil {
			return 0, err
		}
		if a.done {
			return 0, io.EOF
		}
	}
	// Limit this read to what's left in the current chunk.
	want := int64(len(p))
	if want > a.remaining {
		want = a.remaining
	}
	n, err := io.ReadFull(a.br, p[:want])
	a.remaining -= int64(n)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return n, err
	}
	if a.remaining == 0 {
		// Consume the per-chunk \r\n trailer.
		if err := a.consumeCRLF(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// advance reads the next chunk size line; if size==0 reads the
// trailing headers block and marks the reader done.
func (a *awsChunkedReader) advance() error {
	line, err := a.readLine()
	if err != nil {
		return err
	}
	size, err := parseChunkSize(line)
	if err != nil {
		return err
	}
	if size == 0 {
		// Terminal chunk. Read trailing-header block until blank line.
		for {
			tl, err := a.readLine()
			if err != nil {
				return err
			}
			if tl == "" {
				break
			}
		}
		a.done = true
		return nil
	}
	a.remaining = size
	return nil
}

func (a *awsChunkedReader) consumeCRLF() error {
	cr, err := a.br.ReadByte()
	if err != nil {
		return err
	}
	if cr != '\r' {
		return fmt.Errorf("aws-chunked: expected \\r after chunk payload, got 0x%02x", cr)
	}
	lf, err := a.br.ReadByte()
	if err != nil {
		return err
	}
	if lf != '\n' {
		return fmt.Errorf("aws-chunked: expected \\n after chunk payload, got 0x%02x", lf)
	}
	return nil
}

// readLine consumes one CRLF-terminated line and returns the
// content without the terminator.
func (a *awsChunkedReader) readLine() (string, error) {
	line, err := a.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", fmt.Errorf("aws-chunked: line missing CRLF terminator: %q", line)
	}
	return line[:len(line)-2], nil
}

// parseChunkSize parses `<hex-size>[;name=value][;name=value]...`.
// The extensions are discarded.
func parseChunkSize(line string) (int64, error) {
	if i := strings.IndexByte(line, ';'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, fmt.Errorf("aws-chunked: empty chunk-size line")
	}
	n, err := strconv.ParseInt(line, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("aws-chunked: invalid hex chunk size %q: %w", line, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("aws-chunked: negative chunk size %d", n)
	}
	return n, nil
}

// isAWSChunkedRequest returns true when any header signals the
// request body is wrapped in AWS chunked-encoding.
func isAWSChunkedRequest(h interface{ Get(string) string }) bool {
	if h.Get("Content-Encoding") == "aws-chunked" {
		return true
	}
	if sha := h.Get("x-amz-content-sha256"); strings.HasPrefix(sha, "STREAMING-") {
		return true
	}
	if h.Get("x-amz-decoded-content-length") != "" {
		return true
	}
	return false
}
