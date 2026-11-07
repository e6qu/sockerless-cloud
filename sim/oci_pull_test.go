package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestOCIRegistry() *OCIRegistry {
	return &OCIRegistry{
		Manifests: MakeStore[OCIManifest](nil, "test_oci_manifests"),
		Blobs:     MakeStore[OCIBlob](nil, "test_oci_blobs"),
		Uploads:   MakeStore[OCIUpload](nil, "test_oci_uploads"),
	}
}

// seedIndex stores an index of two platform manifests, each with a config and
// a layer, and returns the index.
func seedIndex(t *testing.T, reg *OCIRegistry, repo, tag string) OCIPull {
	t.Helper()
	var pull OCIPull
	var children []map[string]any
	for _, arch := range []string{"amd64", "arm64"} {
		config, layer := []byte(`{"architecture":"`+arch+`"}`), []byte("layer-"+arch)
		pull.Blobs = append(pull.Blobs,
			OCIPulledBlob{Digest: ociDigest(config), ContentType: "application/vnd.oci.image.config.v1+json", Data: config},
			OCIPulledBlob{Digest: ociDigest(layer), ContentType: "application/vnd.oci.image.layer.v1.tar", Data: layer})
		manifest, _ := json.Marshal(map[string]any{
			"schemaVersion": 2,
			"mediaType":     "application/vnd.oci.image.manifest.v1+json",
			"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": ociDigest(config), "size": len(config)},
			"layers":        []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": ociDigest(layer), "size": len(layer)}},
		})
		pull.Manifests = append(pull.Manifests, OCIPulledManifest{Digest: ociDigest(manifest), ContentType: "application/vnd.oci.image.manifest.v1+json", Data: manifest})
		children = append(children, map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": ociDigest(manifest), "size": len(manifest), "platform": map[string]any{"os": "linux", "architecture": arch}})
	}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})
	pull.Root = OCIPulledManifest{Digest: ociDigest(index), ContentType: "application/vnd.oci.image.index.v1+json", Data: index}
	pull.Manifests = append([]OCIPulledManifest{pull.Root}, pull.Manifests...)
	reg.ImportImage("", repo, pull, []string{tag})
	return pull
}

// A pull through the Registry v2 API answers the bearer challenge at its realm
// and copies the index, both platform manifests and their blobs byte for
// byte; ImportImage then serves the same digests from another registry.
func TestPullOCIImageFollowsTheBearerChallengeAndCopiesAnIndex(t *testing.T) {
	source := newTestOCIRegistry()
	want := seedIndex(t, source, "library/app", "v1")

	const token = "pull-token"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if user, pass, ok := r.BasicAuth(); !ok || user != "reader" || pass != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(r.URL.Query().Get("scope"), "repository:library/app:pull") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test",scope="repository:library/app:pull"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		source.serve(w, r)
	}))
	defer srv.Close()

	got, err := PullOCIImage(context.Background(), srv.Client(), srv.URL, "library/app", "v1", "reader", "secret", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root.Digest != want.Root.Digest || len(got.Manifests) != 3 || len(got.Blobs) != 4 {
		t.Fatalf("pulled root %s with %d manifests and %d blobs, want %s with 3 and 4", got.Root.Digest, len(got.Manifests), len(got.Blobs), want.Root.Digest)
	}
	if _, err := PullOCIImage(context.Background(), srv.Client(), srv.URL, "library/app", "v1", "reader", "secret", 2); err != ErrOCITooManyManifests {
		t.Fatalf("an index past the manifest limit must be refused, got %v", err)
	}

	target := newTestOCIRegistry()
	target.ImportImage("", "mirror/app", got, []string{"copied"})
	exported, err := target.ExportImage("", "mirror/app", "copied", 50)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Root.Digest != want.Root.Digest || len(exported.Blobs) != 4 {
		t.Fatalf("the imported tag serves %s with %d blobs", exported.Root.Digest, len(exported.Blobs))
	}
}
