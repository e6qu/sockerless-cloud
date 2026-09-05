package sim

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newFuzzRegistry() *OCIRegistry {
	return &OCIRegistry{
		Manifests: NewStateStore[OCIManifest](),
		Blobs:     NewStateStore[OCIBlob](),
		Uploads:   NewStateStore[OCIUpload](),
	}
}

// FuzzOCIServe drives the OCI /v2/ data-plane request router with arbitrary
// paths, methods, digest query params, and bodies. The path splitting
// (strings.Index on /blobs/, /manifests/, etc.), the digest prefix handling in
// storeBlob, the manifest JSON handling, and ociReadBody must never panic on
// any input — they should return an HTTP error response instead.
//
// The second half of that sentence is the half a panic-only oracle cannot see,
// so the response is inspected too. Each iteration gets a registry with nothing
// in it, which makes two properties checkable on arbitrary input: a read of
// content that was never pushed cannot succeed, and no request may leave the
// registry holding content under an empty key — the shape a digest splitter
// that mishandles "sha256:" or ":" would produce.
func FuzzOCIServe(f *testing.F) {
	type seed struct {
		method, path, digest, enc string
		body                      []byte
	}
	seeds := []seed{
		{"GET", "/v2/", "", "", nil},
		{"GET", "/v2", "", "", nil},
		{"GET", "/v2/foo/manifests/latest", "", "", nil},
		{"PUT", "/v2/foo/manifests/latest", "", "", []byte(`{"schemaVersion":2}`)},
		{"PUT", "/v2/foo/manifests/sha256:abc", "", "", []byte("not json")},
		{"GET", "/v2/foo/blobs/sha256:deadbeef", "", "", nil},
		{"POST", "/v2/foo/blobs/uploads/", "sha256:0000", "", []byte("x")},
		{"POST", "/v2/foo/blobs/uploads/", "", "", nil},
		{"PATCH", "/v2/foo/blobs/uploads/uuid-1", "", "", []byte("chunk")},
		{"PUT", "/v2/foo/blobs/uploads/uuid-1", "sha256:zzz", "", []byte("final")},
		{"GET", "/v2/foo/tags/list", "", "", nil},
		// adversarial path shapes
		{"GET", "/v2//blobs//", "", "", nil},
		{"GET", "/v2/blobs/uploads/", "", "", nil},
		{"GET", "/v2//manifests/", "", "", nil},
		{"DELETE", "/v2/r/manifests/sha256:", "", "", nil},
		{"PUT", "/v2/r/manifests/x", "", "", bytes.Repeat([]byte("{"), 4096)},
		// gzip header with non-gzip body
		{"PUT", "/v2/r/manifests/x", "", "gzip", []byte("not gzip")},
		{"PUT", "/v2/r/manifests/x", "", "unknownenc", []byte("body")},
		{"GET", "/v2/\x00\xff/manifests/\x00", "", "", nil},
		// digest-prefix edge cases the storeBlob splitter handles
		{"PUT", "/v2/r/blobs/uploads/u1", "sha256:", "", []byte("d")},
		{"PUT", "/v2/r/blobs/uploads/u1", ":", "", []byte("d")},
		{"PUT", "/v2/r/blobs/uploads/u1", "sha256:" + repeatA(4096), "", []byte("d")},
		{"GET", "/v2/r/blobs/sha256:" + repeatA(8192), "", "", nil},
		{"GET", "/v2/" + repeatA(8192) + "/manifests/" + repeatA(8192), "", "", nil},
		// many path segments
		{"GET", "/v2/a/b/c/d/e/f/manifests/latest", "", "", nil},
		{"DELETE", "/v2/r/blobs/sha256:abc", "", "", nil},
		// trailing /blobs/uploads with no id
		{"POST", "/v2/r/blobs/uploads", "", "", nil},
		// gzip-encoded empty + valid gzip handled by ociReadBody
		{"PUT", "/v2/r/manifests/g", "", "gzip", gzipBytes([]byte(`{"schemaVersion":2}`))},
	}
	for _, s := range seeds {
		f.Add(s.method, s.path, s.digest, s.enc, s.body)
	}
	f.Fuzz(func(t *testing.T, method, path, digest, enc string, body []byte) {
		// http.NewRequest rejects some method/path values; skip those — we're
		// fuzzing the handler, not net/http's request validation.
		if method == "" {
			method = "GET"
		}
		target := path
		if digest != "" {
			target = path + "?digest=" + digest
		}
		req, err := http.NewRequest(method, "http://sim"+ensureLeadingSlash(target), bytes.NewReader(body))
		if err != nil {
			return
		}
		if enc != "" {
			req.Header.Set("Content-Encoding", enc)
		}
		reg := newFuzzRegistry()
		rec := httptest.NewRecorder()
		reg.serve(rec, req)

		if rec.Code < 100 || rec.Code > 599 {
			t.Fatalf("%s %s answered with %d, which is not an HTTP status", method, target, rec.Code)
		}
		// Nothing was ever pushed to this registry, so no read of a manifest or
		// a blob can succeed. "/v2/" itself is the API-version probe and does
		// legitimately answer 200.
		isRead := method == http.MethodGet || method == http.MethodHead || method == http.MethodDelete
		addressesContent := strings.Contains(req.URL.Path, "/manifests/") || strings.Contains(req.URL.Path, "/blobs/")
		if isRead && addressesContent && rec.Code >= 200 && rec.Code < 300 {
			t.Fatalf("%s %s succeeded with %d against an empty registry — nothing was ever pushed to read back",
				method, req.URL.Path, rec.Code)
		}
		// A digest or repository name the splitter reduced to nothing must not
		// become a storage key: content filed under "" is unreachable by any
		// legitimate client and collides with the next such write.
		if _, ok := reg.Blobs.Get(""); ok {
			t.Fatalf("%s %s stored a blob under an empty key", method, target)
		}
		if _, ok := reg.Manifests.Get(""); ok {
			t.Fatalf("%s %s stored a manifest under an empty key", method, target)
		}
	})
}

func ensureLeadingSlash(p string) string {
	if len(p) == 0 || p[0] != '/' {
		return "/" + p
	}
	return p
}

func repeatA(n int) string { return strings.Repeat("a", n) }

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}
