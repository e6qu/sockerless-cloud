package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// gateTestServer serves the simulator behind its authentication layer and
// the IAM gate, as main does, with the in-package test project created.
func gateTestServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	gcpTestCreateProject(t, srv, gcpTestProjectID)
	srv.WrapHandler(iamGateMiddleware(srv))
	srv.WrapHandler(bearerAuthMiddleware(srv))
	return srv
}

func gateTestCall(t *testing.T, srv *sim.Server, email, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	now := time.Now()
	req.Header.Set("Authorization", "Bearer "+signAccessToken(email, now, now.Add(time.Hour)))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// gateTestGrant adds a binding to the policy behind a get and set pair, read
// modify write, as the bootstrap account.
func gateTestGrant(t *testing.T, srv *sim.Server, getMethod, getPath, setMethod, setPath string, wrap bool, role, member string) {
	t.Helper()
	got := gateTestCall(t, srv, gcpBootstrapServiceAccountEmail, getMethod, getPath, "")
	if got.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", getMethod, getPath, got.Code, got.Body)
	}
	var policy IAMPolicy
	if err := json.Unmarshal(got.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	policy.Bindings = append(policy.Bindings, IAMBinding{Role: role, Members: []string{member}})
	var body []byte
	if wrap {
		body, _ = json.Marshal(map[string]any{"policy": policy})
	} else {
		body, _ = json.Marshal(policy)
	}
	if set := gateTestCall(t, srv, gcpBootstrapServiceAccountEmail, setMethod, setPath, string(body)); set.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", setMethod, setPath, set.Code, set.Body)
	}
}

// A principal performs a method only when a policy on the resource or an
// ancestor grants the method's permission, and is refused in the API's own
// shape otherwise.
func TestIAMGate_EnforcesPolicyOnEveryMethod(t *testing.T) {
	srv := gateTestServer(t)
	owner := gcpBootstrapServiceAccountEmail
	reader := "gate-reader@" + gcpTestProjectID + ".iam.gserviceaccount.com"
	inherited := "gate-inherited@" + gcpTestProjectID + ".iam.gserviceaccount.com"

	if rec := gateTestCall(t, srv, owner, "POST", "/storage/v1/b?project="+gcpTestProjectID,
		`{"name":"gate-bucket","iamConfiguration":{"uniformBucketLevelAccess":{"enabled":true}}}`); rec.Code != http.StatusOK {
		t.Fatalf("owner creates the bucket: %d %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest("POST", "/upload/storage/v1/b/gate-bucket/o?uploadType=media&name=doc.txt", strings.NewReader("hello"))
	now := time.Now()
	req.Header.Set("Authorization", "Bearer "+signAccessToken(owner, now, now.Add(time.Hour)))
	req.Header.Set("Content-Type", "text/plain")
	upload := httptest.NewRecorder()
	srv.ServeHTTP(upload, req)
	if upload.Code != http.StatusOK {
		t.Fatalf("owner uploads the object: %d %s", upload.Code, upload.Body)
	}

	if rec := gateTestCall(t, srv, reader, "GET", "/storage/v1/b/gate-bucket/o/doc.txt", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("a principal bound to nothing reads the object: %d %s", rec.Code, rec.Body)
	}
	gateTestGrant(t, srv, "GET", "/storage/v1/b/gate-bucket/iam", "PUT", "/storage/v1/b/gate-bucket/iam", false,
		"roles/storage.objectViewer", "serviceAccount:"+reader)
	if rec := gateTestCall(t, srv, reader, "GET", "/storage/v1/b/gate-bucket/o/doc.txt", ""); rec.Code != http.StatusOK {
		t.Fatalf("an object viewer reads the object: %d %s", rec.Code, rec.Body)
	}
	denied := gateTestCall(t, srv, reader, "DELETE", "/storage/v1/b/gate-bucket/o/doc.txt", "")
	want := reader + " does not have storage.objects.delete access to the Google Cloud Storage object. " +
		"Permission 'storage.objects.delete' denied on resource (or it may not exist)."
	var failure struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(denied.Body.Bytes(), &failure)
	if denied.Code != http.StatusForbidden || failure.Error.Message != want || len(failure.Error.Errors) != 1 || failure.Error.Errors[0].Reason != "forbidden" {
		t.Fatalf("an object viewer deletes the object: %d %s", denied.Code, denied.Body)
	}

	gateTestGrant(t, srv, "POST", "/v1/projects/"+gcpTestProjectID+":getIamPolicy", "POST", "/v1/projects/"+gcpTestProjectID+":setIamPolicy", true,
		"roles/storage.objectViewer", "serviceAccount:"+inherited)
	if rec := gateTestCall(t, srv, inherited, "GET", "/storage/v1/b/gate-bucket/o/doc.txt", ""); rec.Code != http.StatusOK {
		t.Fatalf("a project-level object viewer reads the bucket's object: %d %s", rec.Code, rec.Body)
	}

	gateTestGrant(t, srv, "GET", "/storage/v1/b/gate-bucket/iam", "PUT", "/storage/v1/b/gate-bucket/iam", false,
		"roles/storage.objectAdmin", "serviceAccount:"+reader)
	if rec := gateTestCall(t, srv, reader, "DELETE", "/storage/v1/b/gate-bucket/o/doc.txt", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("an object admin deletes the object: %d %s", rec.Code, rec.Body)
	}

	topic := "/v1/projects/" + gcpTestProjectID + "/topics/gate-topic"
	if rec := gateTestCall(t, srv, owner, "PUT", topic, "{}"); rec.Code != http.StatusOK {
		t.Fatalf("owner creates the topic: %d %s", rec.Code, rec.Body)
	}
	publish := `{"messages":[{"data":"aGk="}]}`
	if rec := gateTestCall(t, srv, reader, "POST", topic+":publish", publish); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "User not authorized to perform this action.") {
		t.Fatalf("a principal without pubsub.topics.publish publishes: %d %s", rec.Code, rec.Body)
	}
	role := gateTestCall(t, srv, owner, "POST", "/v1/projects/"+gcpTestProjectID+"/roles",
		`{"roleId":"gatePublisher","role":{"title":"Gate publisher","includedPermissions":["pubsub.topics.publish"],"stage":"GA"}}`)
	if role.Code != http.StatusOK {
		t.Fatalf("owner creates the custom role: %d %s", role.Code, role.Body)
	}
	gateTestGrant(t, srv, "POST", topic+":getIamPolicy", "POST", topic+":setIamPolicy", true,
		"projects/"+gcpTestProjectID+"/roles/gatePublisher", "serviceAccount:"+reader)
	if rec := gateTestCall(t, srv, reader, "POST", topic+":publish", publish); rec.Code != http.StatusOK {
		t.Fatalf("a principal holding a custom role with pubsub.topics.publish publishes: %d %s", rec.Code, rec.Body)
	}

	secret := gateTestCall(t, srv, reader, "POST", "/v1/projects/"+gcpTestProjectID+"/secrets?secretId=gate", `{"replication":{"automatic":{}}}`)
	if secret.Code != http.StatusForbidden || !strings.Contains(secret.Body.String(), `"reason":"IAM_PERMISSION_DENIED"`) ||
		!strings.Contains(secret.Body.String(), "Permission 'secretmanager.secrets.create' denied on resource") {
		t.Fatalf("a principal without secretmanager.secrets.create creates a secret: %d %s", secret.Code, secret.Body)
	}
}

// Every call resolves to the permission the method's source names, on the
// resource it addresses.
func TestIAMGate_DerivesEachMethodsPermission(t *testing.T) {
	cases := []struct {
		host, method, path, query, permission, resource string
	}{
		{"", "DELETE", "/storage/v1/b/bkt/o/a%2Fb.txt", "", "storage.objects.delete", "projects/_/buckets/bkt/objects/a/b.txt"},
		{"", "GET", "/storage/v1/b/bkt/o/a.txt", "alt=media", "storage.objects.get", "projects/_/buckets/bkt/objects/a.txt"},
		{"", "GET", "/download/storage/v1/b/bkt/o/a.txt", "alt=media", "storage.objects.get", "projects/_/buckets/bkt/objects/a.txt"},
		{"", "POST", "/upload/storage/v1/b/bkt/o", "uploadType=media&name=x", "storage.objects.create", "projects/_/buckets/bkt"},
		{"", "POST", "/storage/v1/b", "project=p1", "storage.buckets.create", "projects/p1"},
		{"", "GET", "/storage/v1/b", "project=p1", "storage.buckets.list", "projects/p1"},
		{"", "POST", "/v1/projects/p1/topics/t1:publish", "", "pubsub.topics.publish", "projects/p1/topics/t1"},
		{"", "PUT", "/v1/projects/p1/topics/t1", "", "pubsub.topics.create", "projects/p1"},
		{"", "GET", "/v1/projects/p1/secrets/s1/versions/latest:access", "", "secretmanager.versions.access", "projects/p1/secrets/s1/versions/latest"},
		{"", "POST", "/v1/projects/p1/secrets/s1:addVersion", "", "secretmanager.versions.add", "projects/p1/secrets/s1"},
		{"", "POST", "/v1/projects/p1/locations/global/keyRings/k/cryptoKeys/c:encrypt", "", "cloudkms.cryptoKeyVersions.useToEncrypt", "projects/p1/locations/global/keyRings/k/cryptoKeys/c"},
		{"", "DELETE", "/v2/projects/p1/locations/us-central1/services/s1", "", "run.services.delete", "projects/p1/locations/us-central1/services/s1"},
		{"", "GET", "/compute/v1/projects/p1/zones/us-central1-a/instances/i1", "", "compute.instances.get", "projects/p1/zones/us-central1-a/instances/i1"},
		{"", "POST", "/compute/v1/projects/p1/zones/us-central1-a/instances", "", "compute.instances.create", "projects/p1/zones/us-central1-a/instances"},
		{"", "GET", "/v1/projects/p1", "", "resourcemanager.projects.get", "projects/p1"},
		{"", "GET", "/v3/projects/p1", "", "resourcemanager.projects.get", "projects/p1"},
		{"", "POST", "/v1/projects/p1/serviceAccounts", "", "iam.serviceAccounts.create", "projects/p1/serviceAccounts"},
		{"", "POST", "/v1/projects/-/serviceAccounts/sa@p1.iam.gserviceaccount.com:generateAccessToken", "", "iam.serviceAccounts.getAccessToken", "projects/-/serviceAccounts/sa@p1.iam.gserviceaccount.com"},
		{"", "POST", "/v1/projects/p1/databases/(default)/documents/c1/d1", "", "", ""},
		{"", "GET", "/v1/projects/p1/databases/(default)/documents/c1/d1", "", "datastore.entities.get", "projects/p1/databases/(default)/documents/c1/d1"},
		{"", "GET", "/bigquery/v2/projects/p1/datasets/d1/tables/t1/data", "", "bigquery.tables.getData", "projects/p1/datasets/d1/tables/t1/data"},
		{"", "POST", "/bigquery/v2/projects/p1/queries", "", "bigquery.jobs.create", "projects/p1/queries"},
		{"", "GET", "/sql/v1beta4/projects/p1/instances/i1", "", "cloudsql.instances.get", "projects/p1/instances/i1"},
		{"", "GET", "/dns/v1/projects/p1/managedZones/z1", "", "dns.managedZones.get", "projects/p1/managedZones/z1"},
		{"", "POST", "/v2/projects/p1/locations/us-central1/queues/q1/tasks", "", "cloudtasks.tasks.create", "projects/p1/locations/us-central1/queues/q1/tasks"},
		{"", "GET", "/sql/v1beta4/projects/p1/operations/op1", "", "cloudsql.instances.get", "projects/p1/operations/op1"},
		{"spanner.googleapis.com", "POST", "/v1/projects/p1/instances/i1/databases/d1/sessions/s1:executeSql", "", "spanner.databases.select", "projects/p1/instances/i1/databases/d1"},
		{"spanner.localhost:4567", "GET", "/v1/projects/p1/instances/i1/databases/d1/operations/op1", "", "spanner.databaseOperations.get", "projects/p1/instances/i1/databases/d1/operations/op1"},
		{"spanner.googleapis.com", "GET", "/v1/projects/p1/instances/i1", "", "spanner.instances.get", "projects/p1/instances/i1"},
		{"", "GET", "/v1/projects/p1/instances/i1", "", "cloudsql.instances.get", "projects/p1/instances/i1"},
	}
	for _, tc := range cases {
		query, _ := url.ParseQuery(tc.query)
		host := tc.host
		if host == "" {
			host = "localhost"
		}
		call := &gateCall{httpMethod: tc.method, path: tc.path, host: host, query: query, fields: func() map[string]any { return map[string]any{} }}
		decision, ok := gateResolve(call)
		if tc.permission == "" {
			continue
		}
		if !ok || len(decision.checks) == 0 || len(decision.checks[0]) == 0 {
			t.Errorf("%s %s resolves to nothing (%v, %+v)", tc.method, tc.path, ok, decision)
			continue
		}
		got := decision.checks[0][0]
		if got.permission != tc.permission || got.resource != tc.resource || got.unsourced {
			t.Errorf("%s %s (%v) checks %q on %q (unsourced %v), want %q on %q", tc.method, tc.path, decision.methods, got.permission, got.resource, got.unsourced, tc.permission, tc.resource)
		}
	}

	// Several APIs publish one operations path; the host names the API, and
	// without one the caller may hold any of their permissions.
	operation := "/v2/projects/p1/locations/us-central1/operations/op1"
	shared := &gateCall{httpMethod: "GET", path: operation, host: "localhost", query: url.Values{}, fields: func() map[string]any { return nil }}
	if decision, ok := gateResolve(shared); !ok || len(decision.checks) < 2 {
		t.Errorf("an operations path several APIs publish resolves to %+v", decision)
	}
	hosted := &gateCall{httpMethod: "GET", path: operation, host: "run.googleapis.com", query: url.Values{}, fields: func() map[string]any { return nil }}
	if decision, ok := gateResolve(hosted); !ok || len(decision.checks) != 1 || decision.checks[0][0].permission != "run.operations.get" {
		t.Errorf("Cloud Run's host resolves its operations path to %+v", decision)
	}
}

func TestIAMGate_PoolPrincipalSetCoversThePoolsSubjects(t *testing.T) {
	const set = "principalSet://iam.googleapis.com/locations/global/workforcePools/operators/*"
	now := time.Now()
	for principal, want := range map[string]bool{
		"principal://iam.googleapis.com/locations/global/workforcePools/operators/subject/alice": true,
		"principal://iam.googleapis.com/locations/global/workforcePools/others/subject/alice":    false,
		"serviceAccount:robot@p.iam.gserviceaccount.com":                                         false,
		"": false,
	} {
		if got := gcpMemberMatches(set, principal, now); got != want {
			t.Errorf("gcpMemberMatches(%q) = %v, want %v", principal, got, want)
		}
	}
}

// A project Owner granted through a pool's principal set holds the bucket
// permissions a bucket's default policy grants projectOwner.
func TestIAMGate_ConvenienceMembersCoverPoolPrincipals(t *testing.T) {
	gateTestServer(t)
	const subject = "principal://iam.googleapis.com/locations/global/workforcePools/operators/subject/alice"
	gcpProjectPolicies.Put("project/"+gcpTestProjectID, IAMPolicy{Bindings: []IAMBinding{{
		Role: gcpRoleOwner, Members: []string{"principalSet://iam.googleapis.com/locations/global/workforcePools/operators/*"},
	}}})
	if !gcpMemberMatches("projectOwner:"+gcpTestProjectID, subject, time.Now()) {
		t.Fatal("projectOwner does not cover a subject of the pool the project's Owner binding names")
	}
	if gcpMemberMatches("projectEditor:"+gcpTestProjectID, subject, time.Now()) {
		t.Fatal("projectEditor covers a subject that holds only Owner")
	}
}
