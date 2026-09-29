package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openStreamingBody decodes a gzip transport encoding, which Cloud Storage
// removes before it stores an upload. Multipart framing and Content-Range
// carry per-operation semantics each handler applies itself.
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
		return nil, fmt.Errorf("unsupported Content-Encoding %q", ce)
	}
}

type gzipReadCloser struct {
	orig io.Closer
	gz   *gzip.Reader
}

func (g gzipReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g gzipReadCloser) Close() error {
	_ = g.gz.Close()
	return g.orig.Close()
}
