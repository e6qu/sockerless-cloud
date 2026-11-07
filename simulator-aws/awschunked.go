package main

import (
	"bufio"
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// awsChunkedReader unwraps aws-chunked framing, which the SDKs use to stream a
// non-seekable body; Amazon S3 unwraps it server-side.
//
//	<chunk-size-hex>[;chunk-signature=<sig>]\r\n
//	<payload bytes>\r\n
//	...
//	0[;chunk-signature=<sig>]\r\n
//	[<trailer-header>:<value>\r\n]*
//	\r\n
//
// With a signer it verifies each chunk's signature, and a signed trailer's,
// as it reads them. Trailers holds the trailer headers once the body is read.
type awsChunkedReader struct {
	br        *bufio.Reader
	remaining int64
	done      bool
	signer    *awsChunkSigner
	// chunkSig and chunkHash are the current chunk's claimed signature and
	// the running hash of its payload.
	chunkSig  string
	chunkHash hashWriter
	Trailers  map[string]string
}

// errAWSChunkSignature is a chunk or trailer whose signature does not verify.
var errAWSChunkSignature = errors.New("aws-chunked: chunk signature does not match")

func newAWSChunkedReader(r io.Reader) *awsChunkedReader {
	return &awsChunkedReader{br: bufio.NewReader(r), Trailers: map[string]string{}, chunkHash: newSHA256Writer()}
}

// newSignedAWSChunkedReader verifies the chunks against signer, seeded with
// the request's own signature.
func newSignedAWSChunkedReader(r io.Reader, signer *awsChunkSigner) *awsChunkedReader {
	a := newAWSChunkedReader(r)
	a.signer = signer
	return a
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
	_, _ = a.chunkHash.Write(p[:n])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return n, err
	}
	if a.remaining == 0 {
		if err := a.consumeCRLF(); err != nil {
			return n, err
		}
		if err := a.verifyChunk(); err != nil {
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
	size, sig, err := parseChunkHeader(line)
	if err != nil {
		return err
	}
	a.chunkSig = sig
	a.chunkHash = newSHA256Writer()
	if size == 0 {
		if err := a.verifyChunk(); err != nil {
			return err
		}
		for {
			tl, err := a.readLine()
			if err != nil {
				return err
			}
			if tl == "" {
				break
			}
			name, value, ok := strings.Cut(tl, ":")
			if !ok {
				return fmt.Errorf("aws-chunked: malformed trailer %q", tl)
			}
			a.Trailers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
		}
		if err := a.verifyTrailer(); err != nil {
			return err
		}
		a.done = true
		return nil
	}
	a.remaining = size
	return nil
}

func (a *awsChunkedReader) verifyChunk() error {
	if a.signer == nil || !a.signer.signedChunks {
		return nil
	}
	if !equalSignature(a.signer.next("AWS4-HMAC-SHA256-PAYLOAD", hexSHA256(nil)+"\n"+hex.EncodeToString(a.chunkHash.Sum(nil))), a.chunkSig) {
		return errAWSChunkSignature
	}
	return nil
}

// verifyTrailer checks x-amz-trailer-signature, which signs the canonical
// form of the other trailer headers chained to the last chunk's signature.
func (a *awsChunkedReader) verifyTrailer() error {
	if a.signer == nil || !a.signer.signedTrailer {
		return nil
	}
	claimed, ok := a.Trailers["x-amz-trailer-signature"]
	if !ok {
		return errAWSChunkSignature
	}
	names := make([]string, 0, len(a.Trailers))
	for name := range a.Trailers {
		if name != "x-amz-trailer-signature" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var canonical strings.Builder
	for _, name := range names {
		canonical.WriteString(name + ":" + a.Trailers[name] + "\n")
	}
	if !equalSignature(a.signer.next("AWS4-HMAC-SHA256-TRAILER", hexSHA256([]byte(canonical.String()))), claimed) {
		return errAWSChunkSignature
	}
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

// parseChunkHeader reads a chunk's size and its chunk-signature extension.
func parseChunkHeader(line string) (int64, string, error) {
	sizePart, extensions, _ := strings.Cut(line, ";")
	signature := ""
	for _, ext := range strings.Split(extensions, ";") {
		if name, value, ok := strings.Cut(ext, "="); ok && strings.TrimSpace(name) == "chunk-signature" {
			signature = strings.TrimSpace(value)
		}
	}
	sizePart = strings.TrimSpace(sizePart)
	if sizePart == "" {
		return 0, "", fmt.Errorf("aws-chunked: empty chunk-size line")
	}
	n, err := strconv.ParseInt(sizePart, 16, 64)
	if err != nil {
		return 0, "", fmt.Errorf("aws-chunked: invalid hex chunk size %q: %w", sizePart, err)
	}
	if n < 0 {
		return 0, "", fmt.Errorf("aws-chunked: negative chunk size %d", n)
	}
	return n, signature, nil
}

// awsChunkSigner chains the signatures of a streaming SigV4 upload: each
// chunk's string to sign is the chunk algorithm, the request's timestamp and
// credential scope, the previous signature and the chunk's payload part
// (aws-c-auth-sigv4-signing.c, s_build_canonical_request_body_chunk and
// s_build_canonical_request_trailing_headers).
type awsChunkSigner struct {
	key           []byte
	amzDate       string
	scope         string
	previous      string
	signedChunks  bool
	signedTrailer bool
}

func (s *awsChunkSigner) next(algorithm, payload string) string {
	stringToSign := algorithm + "\n" + s.amzDate + "\n" + s.scope + "\n" + s.previous + "\n" + payload
	s.previous = hex.EncodeToString(hmacSHA256(s.key, []byte(stringToSign)))
	return s.previous
}

// equalSignature compares two hex signatures in constant time.
func equalSignature(a, b string) bool {
	return hmac.Equal([]byte(strings.ToLower(a)), []byte(strings.ToLower(b)))
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
