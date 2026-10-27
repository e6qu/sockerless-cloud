package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// awsChunkedReader unwraps aws-chunked framing, which the SDKs use to stream a
// non-seekable body; Amazon S3 unwraps it server-side.
//
//	<chunk-size-hex>[;<extension>=<value>]\r\n
//	<payload bytes>\r\n
//	...
//	0[;<extension>=<value>]\r\n
//	[<trailer-header>: <value>\r\n]*
//	\r\n
//
// The reader ignores extensions such as chunk-signature, and reads and
// discards trailer headers such as x-amz-checksum-crc32, which Amazon S3
// validates.
type awsChunkedReader struct {
	br        *bufio.Reader
	remaining int64
	done      bool
}

func newAWSChunkedReader(r io.Reader) *awsChunkedReader {
	return &awsChunkedReader{br: bufio.NewReader(r)}
}

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
		if err := a.consumeCRLF(); err != nil {
			return n, err
		}
	}
	return n, nil
}

// A zero size reads the trailer block and marks the reader done.
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

// Extensions after the size are discarded.
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
