package sim

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The cap these tests exercise is two gibibytes, and reaching it costs that
// much memory: proving it at full size peaked at 7.7 GiB of resident memory
// under the race detector, more than the 7 GiB a hosted runner has, which is
// what had been killing the race job. The property is the boundary — a body of
// exactly the cap is returned whole, one byte more is refused rather than
// truncated, on the plain path and after inflation alike — and a boundary is
// proved at any size, so these supply a small cap and assert both of its sides
// exactly, which the full-size version could never afford to do.
const ociTestBodyLimit = 1 << 16 // 64 KiB

// TestOCIReadBodyRejectsGzipBomb verifies the gunzip path is bounded: a tiny
// gzip blob that inflates past the cap is rejected rather than buffered into
// memory (a zip-bomb DoS).
func TestOCIReadBodyRejectsGzipBomb(t *testing.T) {
	// Zeros compress about a thousand to one, so this is the bomb's shape:
	// small on the wire, past the cap once inflated.
	bomb := gzipOf(t, make([]byte, ociTestBodyLimit+1))
	if len(bomb) >= ociTestBodyLimit {
		t.Fatalf("the compressed bomb (%d bytes) must be smaller than the cap it defeats", len(bomb))
	}

	req := httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", bytes.NewReader(bomb))
	req.Header.Set("Content-Encoding", "gzip")
	if _, err := ociReadBodyLimited(req, ociTestBodyLimit); err == nil {
		t.Fatal("expected a gzip body that inflates past the cap to be rejected, got nil error")
	} else if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected a limit error, got: %v", err)
	}

	// One byte less inflates to exactly the cap, and is returned whole rather
	// than refused — the other side of the same boundary.
	atLimit := gzipOf(t, make([]byte, ociTestBodyLimit))
	req = httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", bytes.NewReader(atLimit))
	req.Header.Set("Content-Encoding", "gzip")
	got, err := ociReadBodyLimited(req, ociTestBodyLimit)
	if err != nil {
		t.Fatalf("a gzip body inflating to exactly the cap must be accepted: %v", err)
	}
	if len(got) != ociTestBodyLimit {
		t.Fatalf("inflated body is %d bytes, want the full %d", len(got), ociTestBodyLimit)
	}
}

// TestOCIReadBodyRejectsOversizedIdentity verifies the plain (identity) path is
// bounded too, and that it refuses rather than truncates.
func TestOCIReadBodyRejectsOversizedIdentity(t *testing.T) {
	// A reader that never ends: the cap is the only thing that can stop it.
	req := httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", &infiniteReader{})
	if _, err := ociReadBodyLimited(req, ociTestBodyLimit); err == nil {
		t.Fatal("expected an endless identity body to be rejected")
	} else if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected a limit error, got: %v", err)
	}

	// Exactly the cap is accepted whole, so the refusal above is the boundary
	// and not an off-by-one that would truncate a legitimate upload.
	body := bytes.Repeat([]byte("A"), ociTestBodyLimit)
	req = httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", bytes.NewReader(body))
	got, err := ociReadBodyLimited(req, ociTestBodyLimit)
	if err != nil {
		t.Fatalf("a body of exactly the cap must be accepted: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("a body of exactly the cap must be returned whole, got %d bytes", len(got))
	}
}

// TestOCIReadBodyUsesTheRegistryCap pins the cap the served path applies, which
// the boundary tests above deliberately do not reach.
func TestOCIReadBodyUsesTheRegistryCap(t *testing.T) {
	if ociMaxBodyBytes != 2<<30 {
		t.Fatalf("the OCI body cap is %d bytes; update this test deliberately if it moves", int64(ociMaxBodyBytes))
	}
	// The served entry point applies that cap rather than one of its own: a
	// body over the test cap but under the real one is accepted here, and was
	// refused above.
	body := bytes.Repeat([]byte("A"), ociTestBodyLimit+1)
	req := httptest.NewRequest(http.MethodPut, "http://sim/v2/r/manifests/x", bytes.NewReader(body))
	got, err := ociReadBody(req)
	if err != nil {
		t.Fatalf("the registry cap is far larger than this body: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("body round-trip is %d bytes, want %d", len(got), len(body))
	}
}

// gzipOf compresses payload, which the bomb tests use to build a body that is
// small on the wire and large once inflated.
func gzipOf(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
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
