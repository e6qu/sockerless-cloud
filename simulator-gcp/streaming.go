package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// openStreamingBody wraps r.Body with a sentinel-aware reader that
// transparently decodes the encodings real GCS / Cloud Run / AR
// clients put on the wire — currently `Content-Encoding: gzip`.
// Other shapes (multipart/related for GCS metadata-prefix upload,
// Content-Range for resumable uploads, OCI Distribution PATCH
// chunked uploads for AR) carry semantics each handler decides
// per-op; this helper only handles transparent-decode encodings.
//
// The skill
// `sim-streaming-body-handler` codifies the pre-write check.
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
