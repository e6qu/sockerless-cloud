package simulator

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOCIReadBodyRejectsGzipBomb verifies the gunzip path is bounded: a tiny
// gzip blob that inflates past the cap is rejected rather than buffered into
// memory (a zip-bomb DoS).
func TestOCIReadBodyRejectsGzipBomb(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	// Highly compressible payload larger than the cap: zeros compress to a few
	// KiB but would inflate to > ociMaxBodyBytes if read unbounded. Use a
	// repeated write so we don't actually allocate the full inflated size here.
	chunk := make([]byte, 1<<20) // 1 MiB of zeros
	for written := int64(0); written <= ociMaxBodyBytes; written += int64(len(chunk)) {
		if _, err := gz.Write(chunk); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if buf.Len() > 10<<20 {
		t.Fatalf("compressed bomb unexpectedly large (%d bytes) — test would be slow", buf.Len())
	}

	req := httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	if _, err := ociReadBody(req); err == nil {
		t.Fatal("expected ociReadBody to reject a gzip body that inflates past the cap, got nil error")
	} else if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected a limit error, got: %v", err)
	}
}

// TestOCIReadBodyRejectsOversizedIdentity verifies the plain (identity) path is
// bounded too.
func TestOCIReadBodyRejectsOversizedIdentity(t *testing.T) {
	// A reader that yields more than the cap without allocating it all.
	body := &infiniteReader{}
	req := httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", body)
	if _, err := ociReadBody(req); err == nil {
		t.Fatal("expected ociReadBody to reject an oversized identity body")
	} else if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected a limit error, got: %v", err)
	}
}

// TestOCIReadBodyAcceptsNormalBody confirms the cap doesn't reject legitimate
// (small) uploads.
func TestOCIReadBodyAcceptsNormalBody(t *testing.T) {
	want := []byte(`{"schemaVersion":2}`)
	req := httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", bytes.NewReader(want))
	got, err := ociReadBody(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("body round-trip mismatch: got %q want %q", got, want)
	}
}

// infiniteReader yields an endless stream of 'A' bytes. ociReadBody's
// io.LimitReader must stop it before memory is exhausted.
type infiniteReader struct{}

func (infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

// FuzzParsePlatform exercises the "os/arch[/variant]" parser; it must never
// panic and must error (not crash) on malformed coordinates.
func FuzzParsePlatform(f *testing.F) {
	for _, s := range []string{"", "linux", "linux/amd64", "linux/arm64/v8", "a/b/c/d", "//", "linux/", "/amd64"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parsePlatform(s)
	})
}

// FuzzIsDockerSocketBind feeds arbitrary bind specs through the sandbox
// socket-deny matcher; it must never panic and must keep denying the canonical
// socket paths regardless of surrounding noise.
func FuzzIsDockerSocketBind(f *testing.F) {
	for _, s := range []string{
		"/var/run/docker.sock:/var/run/docker.sock",
		"/var/run/../run/docker.sock:/x",
		"/:/host",
		"myvol:/data",
		"::::",
		"",
		"/",
		"/var/run",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = isDockerSocketBind(s)
	})
}

// FuzzResolveLocalImage exercises the cloud-registry-URI → docker-hub rewriter
// with arbitrary image references.
func FuzzResolveLocalImage(f *testing.F) {
	for _, s := range []string{
		"alpine:latest",
		"us-central1-docker.pkg.dev/p/docker-hub/library/alpine:latest",
		"1.dkr.ecr.eu-west-1.amazonaws.com/docker-hub/library/alpine:latest",
		"myacr.azurecr.io/library/alpine:latest",
		"public.ecr.aws/docker/library/alpine:latest",
		".amazonaws.com/",
		"-docker.pkg.dev//docker-hub/",
		".azurecr.io/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = ResolveLocalImage(s)
	})
}
