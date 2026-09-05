package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Artifact Registry Docker data plane refuses on four axes, and each one
// has its own wire shape on the live service: a request that presented no
// credential, one that presented a credential the registry could not accept,
// one whose credential is a token the registry's own token service issued
// without an identity, and one whose principal cannot reach the repository.
// These exercise all four against the whole simulator — the same handler chain
// main() serves, bearer middleware included — so a refusal that the middleware
// or the shared /v2/ dispatcher would swallow shows up here.

// arTestServer builds the simulator and returns it together with the base URL
// of an httptest listener in front of it.
func arTestServer(t *testing.T) (*sim.Server, string) {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts.URL
}

// arTestAccessToken mints an OAuth 2.0 access token the way every one of the
// simulator's token minters does, so the data plane verifies it against the
// same key. Callers that need an expired credential ask for one directly.
func arTestAccessToken(subject string, issuedAt, expiresAt time.Time) string {
	return signAccessToken(subject, issuedAt, expiresAt)
}

// arTestDo sends a request and returns the response with its body read.
func arTestDo(t *testing.T, method, url, authorization string) (*http.Response, string) {
	t.Helper()
	return arTestDoAtRegistry(t, method, url, authorization, "")
}

// arTestDoAtRegistry sends a request addressed to a named registry endpoint —
// the `LOCATION-docker.pkg.dev` coordinate a client resolving the real service
// dials — with the simulator's address carrying it. An empty registry leaves
// the simulator's own coordinate in the `Host` header.
func arTestDoAtRegistry(t *testing.T, method, url, authorization, registry string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if registry != "" {
		req.Host = registry
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(body)
}

// arTestErrorBody decodes the Docker Registry v2 error envelope and returns the
// first error's code and message.
func arTestErrorBody(t *testing.T, body string) (string, string) {
	t.Helper()
	var envelope struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode registry error envelope %q: %v", body, err)
	}
	if len(envelope.Errors) != 1 {
		t.Fatalf("expected exactly one error in %q", body)
	}
	return envelope.Errors[0].Code, envelope.Errors[0].Message
}

// arTestCreateRepository creates an Artifact Registry repository through the
// real control-plane route, so the data plane sees a repository that exists the
// only way one ever comes into existence.
func arTestCreateRepository(t *testing.T, baseURL, project, location, repoID string) {
	t.Helper()
	now := time.Now()
	token := arTestAccessToken("sockerless-sim", now, now.Add(time.Hour))
	url := fmt.Sprintf("%s/v1/projects/%s/locations/%s/repositories?repositoryId=%s",
		baseURL, project, location, repoID)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"format":"DOCKER"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create repository: HTTP %d: %s", resp.StatusCode, body)
	}
}

// TestARDataPlaneChallengesAnUncredentialledRequest covers the refusal of a
// request that presented no credential at all: the Docker Registry v2 Bearer
// challenge that tells a client where the token service is, and the two message
// spellings the live service uses depending on whether the request addressed
// the registry or a repository.
func TestARDataPlaneChallengesAnUncredentialledRequest(t *testing.T) {
	_, baseURL := arTestServer(t)
	host := strings.TrimPrefix(baseURL, "http://")
	realm := fmt.Sprintf(`Bearer realm="%s/v2/token"`, baseURL)

	for _, tc := range []struct {
		name      string
		method    string
		path      string
		challenge string
		message   string
	}{{
		name:      "base endpoint is told only where the token service is",
		method:    http.MethodGet,
		path:      "/v2/",
		challenge: realm,
		message:   "not authenticated: No credential was supplied.",
	}, {
		name:      "a manifest read is asked for the pull scope",
		method:    http.MethodGet,
		path:      "/v2/test-project/my-repo/app/manifests/latest",
		challenge: realm + fmt.Sprintf(`,service="%s",scope="repository:test-project/my-repo/app:pull"`, host),
		message:   "not authenticated: No valid credential was supplied.",
	}, {
		name:      "a blob read is asked for the pull scope",
		method:    http.MethodGet,
		path:      "/v2/test-project/my-repo/app/blobs/sha256:" + strings.Repeat("0", 64),
		challenge: realm + fmt.Sprintf(`,service="%s",scope="repository:test-project/my-repo/app:pull"`, host),
		message:   "not authenticated: No valid credential was supplied.",
	}, {
		name:      "a tag listing is asked for the pull scope",
		method:    http.MethodGet,
		path:      "/v2/test-project/my-repo/app/tags/list",
		challenge: realm + fmt.Sprintf(`,service="%s",scope="repository:test-project/my-repo/app:pull"`, host),
		message:   "not authenticated: No valid credential was supplied.",
	}, {
		name:      "starting an upload is asked for the pull,push pair",
		method:    http.MethodPost,
		path:      "/v2/test-project/my-repo/app/blobs/uploads/",
		challenge: realm + fmt.Sprintf(`,service="%s",scope="repository:test-project/my-repo/app:pull,push"`, host),
		message:   "not authenticated: No valid credential was supplied.",
	}, {
		name:      "a manifest write is asked for the pull,push pair",
		method:    http.MethodPut,
		path:      "/v2/test-project/my-repo/app/manifests/v1",
		challenge: realm + fmt.Sprintf(`,service="%s",scope="repository:test-project/my-repo/app:pull,push"`, host),
		message:   "not authenticated: No valid credential was supplied.",
	}, {
		// A delete's IAM permission is deleteArtifacts, but the scope the
		// challenge asks the client to obtain is still the write pair.
		name:      "a manifest delete is asked for the pull,push pair",
		method:    http.MethodDelete,
		path:      "/v2/test-project/my-repo/app/manifests/v1",
		challenge: realm + fmt.Sprintf(`,service="%s",scope="repository:test-project/my-repo/app:pull,push"`, host),
		message:   "not authenticated: No valid credential was supplied.",
	}, {
		// A path naming a project and a repository but no image is reported
		// with Artifact Registry's substituted image component.
		name:      "a path with no image component is reported with pkg",
		method:    http.MethodGet,
		path:      "/v2/test-project/my-repo/manifests/latest",
		challenge: realm + fmt.Sprintf(`,service="%s",scope="repository:test-project/my-repo/pkg:pull"`, host),
		message:   "not authenticated: No valid credential was supplied.",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := arTestDo(t, tc.method, baseURL+tc.path, "")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", resp.StatusCode, body)
			}
			if got := resp.Header.Get("WWW-Authenticate"); got != tc.challenge {
				t.Errorf("WWW-Authenticate =\n  %s\nwant\n  %s", got, tc.challenge)
			}
			if got := resp.Header.Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
				t.Errorf("Docker-Distribution-Api-Version = %q, want registry/2.0", got)
			}
			code, message := arTestErrorBody(t, body)
			if code != "UNAUTHORIZED" {
				t.Errorf("code = %q, want UNAUTHORIZED", code)
			}
			if message != tc.message {
				t.Errorf("message = %q, want %q", message, tc.message)
			}
		})
	}
}

// TestARDataPlaneRefusesAnUnacceptableCredential covers the other 401: a
// request that DID present a credential the registry could not accept. The
// live service answers these without any challenge header, and with the
// message that drops the "not authenticated: " prefix.
func TestARDataPlaneRefusesAnUnacceptableCredential(t *testing.T) {
	_, baseURL := arTestServer(t)
	now := time.Now()
	basic := func(username, password string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	}

	for _, tc := range []struct {
		name          string
		authorization string
	}{
		{"a bearer that is not a token this simulator issued", "Bearer notarealtoken"},
		{"a bearer whose signature does not verify", "Bearer " + arTestAccessToken("sa@test-project.iam.gserviceaccount.com", now, now.Add(time.Hour)) + "tampered"},
		// An access token that has passed its expiry is a credential the
		// registry can read and must still refuse.
		{"an expired access token", "Bearer " + arTestAccessToken("sa@test-project.iam.gserviceaccount.com", now.Add(-2*time.Hour), now.Add(-time.Hour))},
		{"an expired access token as a basic password", basic(arUserAccessToken, arTestAccessToken("sa@test-project.iam.gserviceaccount.com", now.Add(-2*time.Hour), now.Add(-time.Hour)))},
		{"a basic password that is not a token", basic(arUserAccessToken, "bogus")},
		{"a basic username Artifact Registry does not reserve", basic("admin", "hunter2")},
		{"a basic credential that is not base64", "Basic !!!not-base64!!!"},
		{"a basic credential with no colon", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon"))},
		{"a service-account key that is not JSON", basic(arUserJSONKey, "not json at all")},
		{"a service-account key naming an account that is not registered", basic(arUserJSONKey, `{"type":"service_account","client_email":"ghost@test-project.iam.gserviceaccount.com","private_key_id":"k1","private_key":"x"}`)},
		{"a base64 service-account key that is not base64", basic(arUserJSONKeyBase64, "!!!")},
		{"an authorization scheme the registry does not implement", "Potato abcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := arTestDo(t, http.MethodGet,
				baseURL+"/v2/test-project/my-repo/app/manifests/latest", tc.authorization)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", resp.StatusCode, body)
			}
			if got := resp.Header.Get("WWW-Authenticate"); got != "" {
				t.Errorf("WWW-Authenticate = %q, want no challenge on a rejected credential", got)
			}
			code, message := arTestErrorBody(t, body)
			if code != "UNAUTHORIZED" {
				t.Errorf("code = %q, want UNAUTHORIZED", code)
			}
			if message != "No valid credential was supplied." {
				t.Errorf("message = %q, want %q", message, "No valid credential was supplied.")
			}
		})
	}
}

// TestARDataPlaneRejectsAMalformedRepositoryPath covers the shape check that
// runs before any credential is examined: a /v2/ path with a single component
// names no repository, and the live service answers 400 NAME_INVALID.
func TestARDataPlaneRejectsAMalformedRepositoryPath(t *testing.T) {
	_, baseURL := arTestServer(t)
	resp, body := arTestDo(t, http.MethodGet, baseURL+"/v2/foo/manifests/latest", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q, want none — the path is rejected before the credential is read", got)
	}
	code, message := arTestErrorBody(t, body)
	if code != "NAME_INVALID" {
		t.Errorf("code = %q, want NAME_INVALID", code)
	}
	if message != "invalid number of path components: 1" {
		t.Errorf("message = %q", message)
	}
}

// TestARDataPlaneDeniesAnUnreachableRepository covers the 403: a credential the
// registry accepted, whose principal cannot reach the repository the request
// addresses. Artifact Registry names the IAM permission and the repository
// resource, and deliberately conflates "no permission" with "does not exist".
//
// The location in that resource is the one the registry endpoint serves, which
// the live service reports back for a repository that does not exist anywhere:
//
//	$ curl 'https://us-central1-docker.pkg.dev/v2/token?service=us-central1-docker.pkg.dev&scope=repository:sockerless-nonexistent-project/sockerless-nonexistent-repo/app:pull'
//	{"errors":[{"code":"DENIED","message":"Unauthenticated request. Unauthenticated requests do not have permission \"artifactregistry.repositories.downloadArtifacts\" on resource \"projects/sockerless-nonexistent-project/locations/us-central1/repositories/sockerless-nonexistent-repo\" (or it may not exist)"}]}
//
// so the requests here address that coordinate, and the location each denial
// names is the one they addressed rather than one the simulator chose.
func TestARDataPlaneDeniesAnUnreachableRepository(t *testing.T) {
	_, baseURL := arTestServer(t)
	now := time.Now()
	authorization := "Bearer " + arTestAccessToken("sa@test-project.iam.gserviceaccount.com", now, now.Add(time.Hour))

	for _, tc := range []struct {
		name       string
		method     string
		registry   string
		path       string
		permission string
		resource   string
	}{{
		name:       "reading from a repository that was never created",
		method:     http.MethodGet,
		registry:   "us-central1-docker.pkg.dev",
		path:       "/v2/other-project/other-repo/app/manifests/latest",
		permission: arPermDownload,
		resource:   "projects/other-project/locations/us-central1/repositories/other-repo",
	}, {
		name:       "starting an upload into a repository that was never created",
		method:     http.MethodPost,
		registry:   "us-central1-docker.pkg.dev",
		path:       "/v2/other-project/other-repo/app/blobs/uploads/",
		permission: arPermUpload,
		resource:   "projects/other-project/locations/us-central1/repositories/other-repo",
	}, {
		name:       "deleting from a repository that was never created",
		method:     http.MethodDelete,
		registry:   "us-central1-docker.pkg.dev",
		path:       "/v2/other-project/other-repo/app/manifests/v1",
		permission: arPermDelete,
		resource:   "projects/other-project/locations/us-central1/repositories/other-repo",
	}, {
		// The endpoint decides the location, so the identical path denied
		// above names a different resource at a different registry.
		name:       "reading the same path at another location's registry",
		method:     http.MethodGet,
		registry:   "europe-west1-docker.pkg.dev",
		path:       "/v2/other-project/other-repo/app/manifests/latest",
		permission: arPermDownload,
		resource:   "projects/other-project/locations/europe-west1/repositories/other-repo",
	}, {
		name:       "reading the same path at a multi-region registry",
		method:     http.MethodGet,
		registry:   "us-docker.pkg.dev",
		path:       "/v2/other-project/other-repo/app/manifests/latest",
		permission: arPermDownload,
		resource:   "projects/other-project/locations/us/repositories/other-repo",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := arTestDoAtRegistry(t, tc.method, baseURL+tc.path, authorization, tc.registry)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
			}
			code, message := arTestErrorBody(t, body)
			if code != "DENIED" {
				t.Errorf("code = %q, want DENIED", code)
			}
			want := fmt.Sprintf("Permission %q denied on resource %q (or it may not exist)", tc.permission, tc.resource)
			if message != want {
				t.Errorf("message =\n  %s\nwant\n  %s", message, want)
			}
		})
	}
}

// TestARDataPlaneRefusesARepositoryNoLocationHolds is the case that has no
// counterpart at the real service: the simulator's own coordinate names no
// location, so a path naming a repository the control plane never created
// anywhere identifies no Artifact Registry resource at all. Rather than name
// one at a location nobody supplied, the registry answers with the OCI
// Distribution code for a repository it does not know.
func TestARDataPlaneRefusesARepositoryNoLocationHolds(t *testing.T) {
	_, baseURL := arTestServer(t)
	now := time.Now()
	authorization := "Bearer " + arTestAccessToken("sa@test-project.iam.gserviceaccount.com", now, now.Add(time.Hour))

	resp, body := arTestDo(t, http.MethodGet, baseURL+"/v2/other-project/other-repo/app/manifests/latest", authorization)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", resp.StatusCode, body)
	}
	code, message := arTestErrorBody(t, body)
	if code != "NAME_UNKNOWN" {
		t.Errorf("code = %q, want NAME_UNKNOWN", code)
	}
	if message != "repository name not known to registry" {
		t.Errorf("message = %q", message)
	}
	if strings.Contains(body, "us-central1") {
		t.Errorf("refusal named a location the request never supplied: %s", body)
	}
}

// TestARDataPlaneLocatesARepositoryTheControlPlaneCreated is the positive
// control for the refusal above: at the simulator's own coordinate the
// repository the control plane created supplies the location, so the denial
// names the resource at the location it was created in — never a default.
func TestARDataPlaneLocatesARepositoryTheControlPlaneCreated(t *testing.T) {
	_, baseURL := arTestServer(t)
	arTestCreateRepository(t, baseURL, "test-project", "europe-west4", "located-repo")

	resp, body := arTestDo(t, http.MethodGet,
		baseURL+"/v2/test-project/located-repo/app/manifests/latest", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", resp.StatusCode, body)
	}

	// An anonymous identity reaches the resource and is denied there, which is
	// where the location shows up.
	encoded, err := json.Marshal(arRegistryTokenEnvelope{TokenID: 1, Product: arRegistryTokenProduct})
	if err != nil {
		t.Fatalf("marshal anonymous token: %v", err)
	}
	resp, body = arTestDo(t, http.MethodGet,
		baseURL+"/v2/test-project/located-repo/app/manifests/latest",
		"Bearer "+base64.StdEncoding.EncodeToString(encoded))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
	}
	_, message := arTestErrorBody(t, body)
	want := fmt.Sprintf("Unauthenticated request. Unauthenticated requests do not have permission %q on resource %q (or it may not exist)",
		arPermDownload, "projects/test-project/locations/europe-west4/repositories/located-repo")
	if message != want {
		t.Errorf("message =\n  %s\nwant\n  %s", message, want)
	}
}

// TestARDataPlaneServesARepositoryTheControlPlaneCreated is the positive
// control for the denial above: the same credential reaches a repository that
// exists, and the request is served by the registry rather than refused by the
// authorization hook. A missing manifest inside a repository the caller can
// reach is MANIFEST_UNKNOWN, not DENIED — the distinction the 403's "(or it may
// not exist)" hides from a caller who cannot reach the repository at all.
func TestARDataPlaneServesARepositoryTheControlPlaneCreated(t *testing.T) {
	_, baseURL := arTestServer(t)
	arTestCreateRepository(t, baseURL, "test-project", "us-central1", "my-repo")
	now := time.Now()
	authorization := "Bearer " + arTestAccessToken("sa@test-project.iam.gserviceaccount.com", now, now.Add(time.Hour))

	resp, body := arTestDo(t, http.MethodGet,
		baseURL+"/v2/test-project/my-repo/app/manifests/latest", authorization)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", resp.StatusCode, body)
	}
	code, _ := arTestErrorBody(t, body)
	if code != "MANIFEST_UNKNOWN" {
		t.Errorf("code = %q, want MANIFEST_UNKNOWN — the repository is reachable, the manifest is not there", code)
	}
}

// TestARTokenServiceRefusesAScopeAndIssuesTheRest covers both halves of the
// Artifact Registry token service. A repository scope an uncredentialled caller
// cannot reach is refused at the mint, the denial naming the permission and the
// resource — the path a `docker pull` with no prior `docker login` takes, and
// the reason its error is a `denied:` rather than an authentication failure. A
// scope naming no repository is minted anyway, and the token it yields carries
// an identity rather than a permission, so the data plane evaluates access per
// request against the repository addressed.
func TestARTokenServiceRefusesAScopeAndIssuesTheRest(t *testing.T) {
	_, baseURL := arTestServer(t)
	arTestCreateRepository(t, baseURL, "test-project", "us-central1", "my-repo")

	resp, body := arTestDo(t, http.MethodGet,
		baseURL+"/v2/token?service=registry&scope=repository:test-project/my-repo/app:pull", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("token service status = %d, want 403 (body %s)", resp.StatusCode, body)
	}
	deniedCode, deniedMessage := arTestErrorBody(t, body)
	if deniedCode != "DENIED" {
		t.Errorf("mint code = %q, want DENIED", deniedCode)
	}
	wantDenied := fmt.Sprintf("Unauthenticated request. Unauthenticated requests do not have permission %q on resource %q (or it may not exist)",
		arPermDownload, "projects/test-project/locations/us-central1/repositories/my-repo")
	if deniedMessage != wantDenied {
		t.Errorf("mint message =\n  %s\nwant\n  %s", deniedMessage, wantDenied)
	}

	resp, body = arTestDo(t, http.MethodGet,
		baseURL+"/v2/token?service=registry&scope=registry:catalog:*", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token service status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var issued struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(body), &issued); err != nil {
		t.Fatalf("decode token response %q: %v", body, err)
	}
	if issued.Token == "" {
		t.Fatal("token service returned no token for an uncredentialled request")
	}
	if issued.ExpiresIn != int(arRegistryTokenTTL.Seconds()) {
		t.Errorf("expires_in = %d, want %d", issued.ExpiresIn, int(arRegistryTokenTTL.Seconds()))
	}

	// The token is accepted at the base endpoint — that is the endpoint a
	// client probes to discover the registry — and refused at the repository.
	resp, body = arTestDo(t, http.MethodGet, baseURL+"/v2/", "Bearer "+issued.Token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("base endpoint with an anonymous token: status = %d, want 200 (body %s)", resp.StatusCode, body)
	}

	resp, body = arTestDo(t, http.MethodGet,
		baseURL+"/v2/test-project/my-repo/app/manifests/latest", "Bearer "+issued.Token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", resp.StatusCode, body)
	}
	code, message := arTestErrorBody(t, body)
	if code != "DENIED" {
		t.Errorf("code = %q, want DENIED", code)
	}
	want := fmt.Sprintf("Unauthenticated request. Unauthenticated requests do not have permission %q on resource %q (or it may not exist)",
		arPermDownload, "projects/test-project/locations/us-central1/repositories/my-repo")
	if message != want {
		t.Errorf("message =\n  %s\nwant\n  %s", message, want)
	}
}

// TestARTokenServiceContract covers the token service's own refusals and its
// method surface: a credential it cannot accept is reported with its own
// message, and any verb other than GET is 405.
func TestARTokenServiceContract(t *testing.T) {
	_, baseURL := arTestServer(t)
	tokenURL := baseURL + "/v2/token?service=registry&scope=repository:test-project/my-repo/app:pull"

	t.Run("a credential it cannot accept is an authentication failure", func(t *testing.T) {
		authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(arUserAccessToken+":bogus"))
		resp, body := arTestDo(t, http.MethodGet, tokenURL, authorization)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 (body %s)", resp.StatusCode, body)
		}
		code, message := arTestErrorBody(t, body)
		if code != "UNAUTHORIZED" {
			t.Errorf("code = %q, want UNAUTHORIZED", code)
		}
		if message != "authentication failed" {
			t.Errorf("message = %q, want %q", message, "authentication failed")
		}
	})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method+" is not allowed", func(t *testing.T) {
			resp, body := arTestDo(t, method, tokenURL, "")
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405 (body %s)", resp.StatusCode, body)
			}
			if got := resp.Header.Get("Allow"); got != http.MethodGet {
				t.Errorf("Allow = %q, want GET", got)
			}
		})
	}
}

// TestARPermissionForMethod locks the method-to-permission mapping against the
// method table Artifact Registry publishes for its Docker Registry API audit
// logs: reads download, removals delete, and every upload verb uploads.
func TestARPermissionForMethod(t *testing.T) {
	for method, want := range map[string]string{
		http.MethodGet:    arPermDownload,
		http.MethodHead:   arPermDownload,
		http.MethodDelete: arPermDelete,
		http.MethodPost:   arPermUpload,
		http.MethodPatch:  arPermUpload,
		http.MethodPut:    arPermUpload,
	} {
		if got := arPermissionForMethod(method); got != want {
			t.Errorf("arPermissionForMethod(%s) = %q, want %q", method, got, want)
		}
	}
}
