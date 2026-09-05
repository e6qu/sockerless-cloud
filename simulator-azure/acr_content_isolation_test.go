package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// acrTestDigest is the content digest a registry client computes before it
// pushes, the same sha256 the registry verifies the upload against.
func acrTestDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// A registry's repositories, manifests and blobs are its own. Two registries
// served by one simulator hold unrelated content, exactly as two real Azure
// Container Registries do: the content stores are keyed by the registry's ARM
// resource ID, the same key the rest of the Azure stores use.

const acrIsolationManifest = `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"layers":[]}`

// acrPushManifest pushes one manifest to a registry's data plane with the
// admin credential, the way a Docker-protocol client does.
func acrPushManifest(t *testing.T, srv *sim.Server, reg Registry, auth, repo, tag, manifest string) {
	t.Helper()
	req := acrDataPlaneRequest(reg, http.MethodPut, "/v2/"+repo+"/manifests/"+tag, manifest)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	resp, body := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("push %s/%s:%s: status %d: %s", reg.Name, repo, tag, resp.StatusCode, body)
	}
}

// acrPushBlob pushes one monolithic blob to a registry's data plane.
func acrPushBlob(t *testing.T, srv *sim.Server, reg Registry, auth, repo, digest, content string) {
	t.Helper()
	req := acrDataPlaneRequest(reg, http.MethodPost,
		fmt.Sprintf("/v2/%s/blobs/uploads/?digest=%s", repo, digest), content)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, body := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("push blob to %s/%s: status %d: %s", reg.Name, repo, resp.StatusCode, body)
	}
}

// TestACRContentIsPerRegistry pushes a repository to one registry and proves
// every read surface of a second registry — manifest, blob, tag list, the ACR
// catalog and the ACR tags API — reports it absent, each read authenticated
// with that second registry's own admin credential.
func TestACRContentIsPerRegistry(t *testing.T) {
	srv := newACRTestServer(t)
	regA := acrTestRegistry(t, srv, "isolationregistrya", `{"adminUserEnabled":true}`)
	regB := acrTestRegistry(t, srv, "isolationregistryb", `{"adminUserEnabled":true}`)
	userA, passwordsA := acrTestAdminCredentials(t, srv, regA)
	userB, passwordsB := acrTestAdminCredentials(t, srv, regB)
	authA := acrBasicHeader(userA, passwordsA[0])
	authB := acrBasicHeader(userB, passwordsB[0])

	const repo = "team/app"
	layer := "isolation-layer-bytes"
	digest := acrTestDigest([]byte(layer))

	acrPushBlob(t, srv, regA, authA, repo, digest, layer)
	acrPushManifest(t, srv, regA, authA, repo, "v1", acrIsolationManifest)

	// The registry that was pushed to serves the content.
	resp, body := acrServe(t, srv, acrAuthorizedRequest(regA, authA, http.MethodGet, "/v2/"+repo+"/manifests/v1"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the pushed manifest must be readable from its own registry: status %d: %s", resp.StatusCode, body)
	}
	resp, body = acrServe(t, srv, acrAuthorizedRequest(regA, authA, http.MethodGet, "/v2/"+repo+"/blobs/"+digest))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the pushed blob must be readable from its own registry: status %d: %s", resp.StatusCode, body)
	}

	// The other registry has never heard of it.
	for _, tc := range []struct {
		what string
		path string
	}{
		{"manifest by tag", "/v2/" + repo + "/manifests/v1"},
		{"manifest by digest", "/v2/" + repo + "/manifests/" + acrTestDigest([]byte(acrIsolationManifest))},
		{"blob", "/v2/" + repo + "/blobs/" + digest},
	} {
		resp, body := acrServe(t, srv, acrAuthorizedRequest(regB, authB, http.MethodGet, tc.path))
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: registry %s served another registry's content: status %d: %s",
				tc.what, regB.Name, resp.StatusCode, body)
		}
	}

	if tags := acrTagsList(t, srv, regB, authB, repo); len(tags) != 0 {
		t.Errorf("the second registry's tag list reported another registry's tags: %v", tags)
	}
	if tags := acrTagsList(t, srv, regA, authA, repo); len(tags) != 1 || tags[0] != "v1" {
		t.Errorf("the pushed registry's tag list must report its own tag, got %v", tags)
	}

	if repos := acrCatalog(t, srv, regB, authB); len(repos) != 0 {
		t.Errorf("the second registry's catalog reported another registry's repositories: %v", repos)
	}
	if repos := acrCatalog(t, srv, regA, authA); len(repos) != 1 || repos[0] != repo {
		t.Errorf("the pushed registry's catalog must report its own repository, got %v", repos)
	}

	if names := acrV1Tags(t, srv, regB, authB, repo); len(names) != 0 {
		t.Errorf("the second registry's ACR tags API reported another registry's tags: %v", names)
	}
	if names := acrV1Tags(t, srv, regA, authA, repo); len(names) != 1 || names[0] != "v1" {
		t.Errorf("the pushed registry's ACR tags API must report its own tag, got %v", names)
	}
}

// TestACRUploadsArePerRegistry proves an in-flight chunked upload belongs to
// the registry it was started on: the second registry does not know the upload
// UUID, so it can neither append to it nor finalize it.
func TestACRUploadsArePerRegistry(t *testing.T) {
	srv := newACRTestServer(t)
	regA := acrTestRegistry(t, srv, "uploadregistrya", `{"adminUserEnabled":true}`)
	regB := acrTestRegistry(t, srv, "uploadregistryb", `{"adminUserEnabled":true}`)
	userA, passwordsA := acrTestAdminCredentials(t, srv, regA)
	userB, passwordsB := acrTestAdminCredentials(t, srv, regB)
	authA := acrBasicHeader(userA, passwordsA[0])
	authB := acrBasicHeader(userB, passwordsB[0])

	const repo = "team/chunked"
	req := acrAuthorizedRequest(regA, authA, http.MethodPost, "/v2/"+repo+"/blobs/uploads/")
	resp, body := acrServe(t, srv, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start upload: status %d: %s", resp.StatusCode, body)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("the upload start returned no Location")
	}

	resp, body = acrServe(t, srv, acrAuthorizedRequest(regB, authB, http.MethodGet, location))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the second registry must not know another registry's upload: status %d: %s", resp.StatusCode, body)
	}
	resp, body = acrServe(t, srv, acrAuthorizedRequest(regA, authA, http.MethodGet, location))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the upload must still be in flight on its own registry: status %d: %s", resp.StatusCode, body)
	}
}

// acrAuthorizedRequest builds a data-plane request at a registry's login
// server carrying an Authorization header.
func acrAuthorizedRequest(reg Registry, authorization, method, path string) *http.Request {
	req := acrDataPlaneRequest(reg, method, path, "")
	req.Header.Set("Authorization", authorization)
	return req
}

func acrTagsList(t *testing.T, srv *sim.Server, reg Registry, auth, repo string) []string {
	t.Helper()
	resp, body := acrServe(t, srv, acrAuthorizedRequest(reg, auth, http.MethodGet, "/v2/"+repo+"/tags/list"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tags/list on %s: status %d: %s", reg.Name, resp.StatusCode, body)
	}
	var out struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode tags list: %v", err)
	}
	return out.Tags
}

func acrCatalog(t *testing.T, srv *sim.Server, reg Registry, auth string) []string {
	t.Helper()
	resp, body := acrServe(t, srv, acrAuthorizedRequest(reg, auth, http.MethodGet, "/acr/v1/_catalog"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("catalog on %s: status %d: %s", reg.Name, resp.StatusCode, body)
	}
	var out struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	return out.Repositories
}

func acrV1Tags(t *testing.T, srv *sim.Server, reg Registry, auth, repo string) []string {
	t.Helper()
	resp, body := acrServe(t, srv, acrAuthorizedRequest(reg, auth, http.MethodGet, "/acr/v1/"+repo+"/_tags"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ACR tags API on %s: status %d: %s", reg.Name, resp.StatusCode, body)
	}
	var out struct {
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode ACR tags: %v", err)
	}
	names := make([]string, 0, len(out.Tags))
	for _, tag := range out.Tags {
		names = append(names, tag.Name)
	}
	return names
}
