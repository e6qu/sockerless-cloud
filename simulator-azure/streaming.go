package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openStreamingBody wraps r.Body with a sentinel-aware reader that
// transparently decodes the encodings real Azure SDK / azcopy / az
// CLI clients put on the wire — currently `Content-Encoding: gzip`.
// Other encodings (`aws-chunked` family is AWS-specific; the
// streaming-blob/file flows on Azure use `Content-Range` for chunked
// PUTs which are handled by the per-handler range logic, not this
// helper) pass through to a plain reader.
//
// The skill
// `sim-streaming-body-handler` codifies the pre-write check.
// Caller still needs to inspect `Content-Range` / SSE-C headers
// per-handler when those semantics matter; this helper handles
// only the transparent-decode case.
func openStreamingBody(r *http.Request) (io.ReadCloser, error) {
	ce := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	switch ce {
	case "", "identity":
		return r.Body, nil
	case "gzip":
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip body: %w", err)
		}
		return gzipReadCloser{r.Body, gz}, nil
	default:
		// Unsupported encoding. Real Azure rejects with 415
		// Unsupported Media Type; the sim does the same so
		// operators see the issue clearly instead of storing
		// the wrapped bytes verbatim.
		return nil, fmt.Errorf("unsupported Content-Encoding %q", ce)
	}
}

// gzipReadCloser closes both the gzip reader and the underlying body.
type gzipReadCloser struct {
	orig io.Closer
	gz   *gzip.Reader
}

func (g gzipReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g gzipReadCloser) Close() error {
	_ = g.gz.Close()
	return g.orig.Close()
}
