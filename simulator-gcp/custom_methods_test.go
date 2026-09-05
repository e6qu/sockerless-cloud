package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// gcpCustomMethodTestServer builds the full simulator in-process the way the
// conformance tests do.
func gcpCustomMethodTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	return srv
}

func TestMemorystoreAclPolicyRevisionsAreImmutableSnapshots(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)
	const host = "redis.googleapis.com"
	const base = "/v1/projects/test-project/locations/us-central1/aclPolicies"

	status, body := gcpDo(t, srv, http.MethodPost, host, base+"?aclPolicyId=policy-1",
		`{"rules":[{"username":"app","rule":"+get ~old:*"}]}`)
	if status != http.StatusOK {
		t.Fatalf("create ACL policy: got %d %s", status, body)
	}

	status, body = gcpDo(t, srv, http.MethodPatch, host, base+"/policy-1?updateMask=rules",
		`{"rules":[{"username":"app","rule":"+get ~new:*"}]}`)
	if status != http.StatusOK {
		t.Fatalf("patch ACL policy: got %d %s", status, body)
	}

	status, body = gcpDo(t, srv, http.MethodGet, host, base+"/policy-1/revisions?pageSize=1", "")
	if status != http.StatusOK {
		t.Fatalf("list ACL policy revisions: got %d %s", status, body)
	}
	var firstPage struct {
		Revisions []MSRedisAclPolicyRevision `json:"aclPolicyRevisions"`
		NextToken string                     `json:"nextPageToken"`
	}
	if err := json.Unmarshal([]byte(body), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Revisions) != 1 || firstPage.Revisions[0].RevisionNumber != "2" ||
		firstPage.Revisions[0].Snapshot.Rules[0].Rule != "+get ~new:*" || firstPage.NextToken == "" {
		t.Fatalf("unexpected first revision page: %s", body)
	}

	status, body = gcpDo(t, srv, http.MethodGet, host,
		base+"/policy-1/revisions?pageSize=1&pageToken="+firstPage.NextToken, "")
	if status != http.StatusOK {
		t.Fatalf("list second ACL policy revision page: got %d %s", status, body)
	}
	var secondPage struct {
		Revisions []MSRedisAclPolicyRevision `json:"aclPolicyRevisions"`
	}
	if err := json.Unmarshal([]byte(body), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Revisions) != 1 || secondPage.Revisions[0].RevisionNumber != "1" ||
		secondPage.Revisions[0].Snapshot.Rules[0].Rule != "+get ~old:*" {
		t.Fatalf("unexpected second revision page: %s", body)
	}

	status, body = gcpDo(t, srv, http.MethodGet, host, base+"/policy-1/revisions/1", "")
	if status != http.StatusOK || !strings.Contains(body, `"+get ~old:*"`) {
		t.Fatalf("get immutable ACL policy revision: got %d %s", status, body)
	}
}

func gcpDo(t *testing.T, srv *sim.Server, method, host, path, body string) (int, string) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = host
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestUnservedCustomMethodsAreMethodNotFound pins the API-frontend behaviour
// the simulator owes a client that calls a custom method it does not serve: the
// method is resolved before the resource, so the answer is 404 "Method not
// found." — never a story about the resource being missing, inaccessible, or
// (for Firestore) a document to create.
func TestUnservedCustomMethodsAreMethodNotFound(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)

	for _, tc := range []struct {
		name, method, host, path, body string
	}{
		{
			// projects.move is a v3 verb; Cloud Resource Manager v1 does not
			// define it. The v1 fan-in must reject it as an unrouted method
			// rather than report the project — which here does not exist — as
			// inaccessible.
			name:   "CloudResourceManagerV1UnroutedProjectMethod",
			method: http.MethodPost,
			host:   "cloudresourcemanager.googleapis.com",
			path:   "/v1/projects/no-such-project:move",
			body:   `{}`,
		},
		{
			// sessions:adapter is served now, so an unrouted sibling stands in
			// its place: the fan-in that routes it must still answer a method
			// it does not serve with Method not found rather than reporting on
			// the database.
			name:   "SpannerUnroutedSessionsMethod",
			method: http.MethodPost,
			host:   "spanner.googleapis.com",
			path:   "/spanner/v1/projects/test-project/instances/inst/databases/db/sessions:notAMethod",
			body:   `{}`,
		},
		{
			// The v3 projects fan-in receives every POST custom method
			// addressed to a project. An unrouted one must not be answered
			// with the project's 403 — and the project here does not exist, so
			// only method-before-resource ordering can produce a 404.
			name:   "CloudResourceManagerV3UnroutedProjectMethod",
			method: http.MethodPost,
			host:   "cloudresourcemanager.googleapis.com",
			path:   "/v3/projects/no-such-project:getOrgPolicy",
			body:   `{}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := gcpDo(t, srv, tc.method, tc.host, tc.path, tc.body)
			if status != http.StatusNotFound || !strings.Contains(body, "Method not found.") {
				t.Fatalf("got %d %s, want 404 with \"Method not found.\"", status, strings.TrimSpace(body))
			}
			if !strings.Contains(body, `"status":"NOT_FOUND"`) {
				t.Fatalf("response is not a Google error envelope: %s", strings.TrimSpace(body))
			}
		})
	}
}

// TestServedCustomMethodsStillReachTheResource is the other half: resolving the
// method first must not swallow the methods the simulator does serve — those
// still answer with the resource's own state.
func TestServedCustomMethodsStillReachTheResource(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)

	// AddSecretVersion on a secret that does not exist: the method is served,
	// so the answer is about the secret.
	status, body := gcpDo(t, srv, http.MethodPost, "secretmanager.googleapis.com",
		"/v1/projects/test-project/secrets/absent:addVersion", `{"payload":{"data":""}}`)
	if status != http.StatusNotFound || !strings.Contains(body, "secret projects/test-project/secrets/absent not found") {
		t.Fatalf("addVersion: got %d %s, want the secret's own NOT_FOUND", status, strings.TrimSpace(body))
	}

	// UndeleteProject on the seeded, ACTIVE project: served, and answers with
	// the project's lifecycle state.
	status, body = gcpDo(t, srv, http.MethodPost, "cloudresourcemanager.googleapis.com",
		"/v1/projects/test-project:undelete", `{}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "undelete requires DELETE_REQUESTED") {
		t.Fatalf("undelete: got %d %s, want the project's FAILED_PRECONDITION", status, strings.TrimSpace(body))
	}

	// MoveProject (v3) on a project that does not exist: the method is served,
	// so the answer is the project's own 403, not method-not-found.
	status, body = gcpDo(t, srv, http.MethodPost, "cloudresourcemanager.googleapis.com",
		"/v3/projects/no-such-project:move", `{"destinationParent":"folders/1"}`)
	if status != http.StatusForbidden {
		t.Fatalf("move: got %d %s, want the project's own PERMISSION_DENIED", status, strings.TrimSpace(body))
	}

	// GetProject without a custom method still reads the project.
	status, body = gcpDo(t, srv, http.MethodGet, "cloudresourcemanager.googleapis.com", "/v1/projects/test-project", "")
	if status != http.StatusOK || !strings.Contains(body, `"projectId":"test-project"`) {
		t.Fatalf("GetProject: got %d %s, want the project", status, strings.TrimSpace(body))
	}

	// GetCryptoKeyVersion without a custom method still reads the version.
	status, body = gcpDo(t, srv, http.MethodGet, "cloudkms.googleapis.com",
		"/v1/projects/test-project/locations/us-central1/keyRings/kr/cryptoKeys/ck/cryptoKeyVersions/1", "")
	if status != http.StatusNotFound || !strings.Contains(body, "CryptoKeyVersion projects/test-project/locations/us-central1/keyRings/kr/cryptoKeys/ck/cryptoKeyVersions/1 not found") {
		t.Fatalf("GetCryptoKeyVersion: got %d %s, want the version's own NOT_FOUND", status, strings.TrimSpace(body))
	}

	// Firestore CreateDocument on a plain collection path still creates.
	status, body = gcpDo(t, srv, http.MethodPost, "firestore.googleapis.com",
		"/v1/projects/test-project/databases/(default)/documents/things?documentId=doc1", `{"fields":{}}`)
	if status != http.StatusOK || !strings.Contains(body, "/documents/things/doc1") {
		t.Fatalf("CreateDocument: got %d %s, want the created document", status, strings.TrimSpace(body))
	}

	// Spanner CreateSession is routed by the same sub-router whose unrouted
	// tails now answer method-not-found.
	status, body = gcpDo(t, srv, http.MethodPost, "spanner.googleapis.com",
		"/spanner/v1/projects/test-project/instances/inst/databases/db/sessions", `{}`)
	if status == http.StatusNotFound && strings.Contains(body, "Method not found.") {
		t.Fatalf("CreateSession: answered method-not-found (%s) — the sub-router lost a served route", strings.TrimSpace(body))
	}
}
