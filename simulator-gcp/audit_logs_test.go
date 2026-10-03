package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/genproto/googleapis/cloud/audit"
)

// auditEntries lists the audit entries of a project's log that match filter.
func auditEntries(t *testing.T, srv *sim.Server, project, logID, filter string) []map[string]any {
	t.Helper()
	full := `logName="projects/` + project + `/logs/cloudaudit.googleapis.com%2F` + logID + `"`
	if filter != "" {
		full += " AND " + filter
	}
	body, err := json.Marshal(map[string]any{"resourceNames": []string{"projects/" + project}, "filter": full})
	if err != nil {
		t.Fatal(err)
	}
	out := gcpHostOK(t, srv, "logging.googleapis.com", http.MethodPost, "/v2/entries:list", string(body))
	raw, _ := out["entries"].([]any)
	entries := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		entries = append(entries, e.(map[string]any))
	}
	return entries
}

func auditPayload(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	payload, ok := entry["protoPayload"].(map[string]any)
	if !ok {
		t.Fatalf("entry carries no protoPayload: %v", entry)
	}
	return payload
}

// Every JSON API call that changes a bucket writes an Admin Activity entry
// whose protoPayload is the AuditLog Cloud Storage writes, and the entry
// reaches a gRPC reader as a google.cloud.audit.AuditLog.
func TestAuditLogs_BucketCreateWritesAdminActivity(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	createTestProject(t, srv, "audit-p")
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=audit-p",
		`{"name":"audit-bucket","location":"us-east1"}`)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPatch, "/storage/v1/b/audit-bucket",
		`{"labels":{"team":"a"}}`)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodGet, "/storage/v1/b/audit-bucket", "")

	entries := auditEntries(t, srv, "audit-p", auditLogActivity, `protoPayload.methodName="storage.buckets.create"`)
	if len(entries) != 1 {
		t.Fatalf("want one storage.buckets.create entry, got %v", entries)
	}
	entry := entries[0]
	if entry["severity"] != "NOTICE" {
		t.Errorf("severity = %v, want NOTICE", entry["severity"])
	}
	resource := entry["resource"].(map[string]any)
	labels := resource["labels"].(map[string]any)
	if resource["type"] != "gcs_bucket" || labels["bucket_name"] != "audit-bucket" || labels["project_id"] != "audit-p" || labels["location"] != "us-east1" {
		t.Errorf("resource = %v", resource)
	}
	payload := auditPayload(t, entry)
	for field, want := range map[string]string{
		"@type":        auditLogPayloadType,
		"serviceName":  "storage.googleapis.com",
		"methodName":   "storage.buckets.create",
		"resourceName": "projects/_/buckets/audit-bucket",
	} {
		if payload[field] != want {
			t.Errorf("protoPayload.%s = %v, want %s", field, payload[field], want)
		}
	}
	authz := payload["authorizationInfo"].([]any)[0].(map[string]any)
	if authz["permission"] != "storage.buckets.create" || authz["granted"] != true {
		t.Errorf("authorizationInfo = %v", authz)
	}

	if got := auditEntries(t, srv, "audit-p", auditLogActivity, `protoPayload.methodName="storage.buckets.update"`); len(got) != 1 {
		t.Errorf("want one storage.buckets.update entry, got %d", len(got))
	}
	if got := auditEntries(t, srv, "audit-p", auditLogDataAccess, ""); len(got) != 0 {
		t.Errorf("Data Access logs are off by default, got %v", got)
	}

	stored, _, err := listLogEntries(`protoPayload.methodName="storage.buckets.create"`, []string{"projects/audit-p"}, 0, "", "")
	if err != nil || len(stored) != 1 {
		t.Fatalf("list: %v %v", stored, err)
	}
	pe, err := logEntryToProto(stored[0])
	if err != nil {
		t.Fatalf("logEntryToProto: %v", err)
	}
	msg, err := pe.GetProtoPayload().UnmarshalNew()
	if err != nil {
		t.Fatalf("protoPayload does not unmarshal: %v", err)
	}
	auditLog, ok := msg.(*audit.AuditLog)
	if !ok {
		t.Fatalf("protoPayload is %T", msg)
	}
	if auditLog.GetMethodName() != "storage.buckets.create" || auditLog.GetResourceLocation().GetCurrentLocations()[0] != "us-east1" {
		t.Errorf("AuditLog = %v", auditLog)
	}
	if pe.GetSeverity().String() != "NOTICE" || pe.GetReceiveTimestamp() == nil {
		t.Errorf("entry severity %v receiveTimestamp %v", pe.GetSeverity(), pe.GetReceiveTimestamp())
	}
	back, err := protoToLogEntry(pe)
	if err != nil || back.ProtoPayload["methodName"] != "storage.buckets.create" {
		t.Errorf("protoToLogEntry = %v, %v", back.ProtoPayload, err)
	}
}

// Data Access entries follow the project's auditConfigs: none before the
// policy enables a log type for the service, one per call after, and none for
// an exempted member.
func TestAuditLogs_DataAccessFollowsAuditConfig(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=test-project", `{"name":"audit-da-bucket"}`)
	upload := func(name string) {
		gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost,
			"/upload/storage/v1/b/audit-da-bucket/o?uploadType=media&name="+url.QueryEscape(name), "payload")
	}
	upload("before.txt")
	if got := auditEntries(t, srv, "test-project", auditLogDataAccess, ""); len(got) != 0 {
		t.Fatalf("no auditConfig enables Data Access yet, got %v", got)
	}

	policy := gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/test-project:getIamPolicy", `{}`)
	gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/test-project:setIamPolicy",
		`{"policy":{"etag":"`+policy["etag"].(string)+`","auditConfigs":[{"service":"storage.googleapis.com","auditLogConfigs":[{"logType":"DATA_WRITE"}]}]}}`)
	if got := gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/test-project:getIamPolicy", `{}`); got["auditConfigs"] != nil {
		t.Fatalf("auditConfigs changed without updateMask naming them: %v", got)
	}
	policy = gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/test-project:getIamPolicy", `{}`)
	gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/test-project:setIamPolicy",
		`{"updateMask":"bindings,etag,auditConfigs","policy":{"etag":"`+policy["etag"].(string)+`","auditConfigs":[{"service":"storage.googleapis.com","auditLogConfigs":[{"logType":"DATA_WRITE"}]}]}}`)

	upload("reports/after.txt")
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodGet, "/storage/v1/b/audit-da-bucket/o/before.txt", "")
	entries := auditEntries(t, srv, "test-project", auditLogDataAccess, "")
	if len(entries) != 1 {
		t.Fatalf("want the one DATA_WRITE entry, DATA_READ staying off, got %v", entries)
	}
	payload := auditPayload(t, entries[0])
	if payload["methodName"] != "storage.objects.create" || payload["resourceName"] != "projects/_/buckets/audit-da-bucket/objects/reports/after.txt" {
		t.Errorf("payload = %v", payload)
	}
	if entries[0]["severity"] != "INFO" {
		t.Errorf("severity = %v, want INFO", entries[0]["severity"])
	}

	if code, out := gcpHostCall(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/test-project:setIamPolicy",
		`{"updateMask":"auditConfigs","policy":{"auditConfigs":[{"service":"storage.googleapis.com","auditLogConfigs":[{"logType":"EVERYTHING"}]}]}}`); code != http.StatusBadRequest {
		t.Errorf("an unknown logType: %d %v", code, out)
	}
}

// The RPC-defined APIs audit a REST call under the RPC's full name, with the
// request in its message's JSON form and the resource the call names.
func TestAuditLogs_RESTCallsResolveToTheirRPC(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, "secretmanager.googleapis.com", http.MethodPost, "/v1/projects/audit-op/secrets?secretId=db-password",
		`{"replication":{"automatic":{}}}`)
	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPut, "/v1/projects/audit-op/topics/audit-topic", `{}`)
	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPost, "/v1/projects/audit-op/topics/audit-topic:publish",
		`{"messages":[{"data":"aGk="}]}`)

	secret := auditEntries(t, srv, "audit-op", auditLogActivity,
		`protoPayload.methodName="google.cloud.secretmanager.v1.SecretManagerService.CreateSecret"`)
	if len(secret) != 1 {
		t.Fatalf("want one CreateSecret entry, got %v", secret)
	}
	payload := auditPayload(t, secret[0])
	request := payload["request"].(map[string]any)
	if payload["serviceName"] != "secretmanager.googleapis.com" || payload["resourceName"] != "projects/audit-op/secrets/db-password" ||
		request["@type"] != "type.googleapis.com/google.cloud.secretmanager.v1.CreateSecretRequest" ||
		request["parent"] != "projects/audit-op" || request["secretId"] != "db-password" || request["secret"] == nil {
		t.Errorf("CreateSecret payload = %v", payload)
	}
	response := payload["response"].(map[string]any)
	if response["@type"] != "type.googleapis.com/google.cloud.secretmanager.v1.Secret" || response["name"] != "projects/audit-op/secrets/db-password" {
		t.Errorf("CreateSecret response = %v", response)
	}

	topic := auditEntries(t, srv, "audit-op", auditLogActivity, `protoPayload.methodName="google.pubsub.v1.Publisher.CreateTopic"`)
	if len(topic) != 1 {
		t.Fatalf("want one CreateTopic entry, got %v", topic)
	}
	resource := topic[0]["resource"].(map[string]any)
	if resource["type"] != "pubsub_topic" || resource["labels"].(map[string]any)["topic_id"] != "audit-topic" {
		t.Errorf("CreateTopic resource = %v", resource)
	}
	if got := auditEntries(t, srv, "audit-op", auditLogActivity, `protoPayload.methodName="google.pubsub.v1.Publisher.Publish"`); len(got) != 0 {
		t.Errorf("Publish writes no Admin Activity entry, got %v", got)
	}
}

func TestAuditLogs_BindingsMatchTheirTemplates(t *testing.T) {
	for _, tc := range []struct {
		method, path, rpc string
	}{
		{"POST", "/v2/projects/p/locations/us-central1/services", "google.cloud.run.v2.Services.CreateService"},
		{"GET", "/v2/projects/p/locations/us-central1/services/svc", "google.cloud.run.v2.Services.GetService"},
		{"POST", "/v2/projects/p/locations/us-central1/services/svc:setIamPolicy", "google.cloud.run.v2.Services.SetIamPolicy"},
		{"POST", "/v2/projects/p/locations/us-central1/jobs/j:run", "google.cloud.run.v2.Jobs.RunJob"},
		{"GET", "/v2/projects/p/locations/us-central1/jobs/j/executions/e", "google.cloud.run.v2.Executions.GetExecution"},
		{"POST", "/v1/projects/p/secrets/s:addVersion", "google.cloud.secretmanager.v1.SecretManagerService.AddSecretVersion"},
		{"GET", "/v1/projects/p/secrets/s/versions/latest:access", "google.cloud.secretmanager.v1.SecretManagerService.AccessSecretVersion"},
		{"DELETE", "/v2/projects/p/locations/us-central1/functions/f", "google.cloud.functions.v2.FunctionService.DeleteFunction"},
		{"POST", "/v1/projects/p/locations/us/repositories", "google.devtools.artifactregistry.v1.ArtifactRegistry.CreateRepository"},
	} {
		b, _, ok := auditMatchREST(tc.method, tc.path)
		if !ok {
			t.Errorf("%s %s matched no RPC", tc.method, tc.path)
			continue
		}
		if got := string(b.rpc.method.FullName()); got != tc.rpc {
			t.Errorf("%s %s → %s, want %s", tc.method, tc.path, got, tc.rpc)
		}
	}
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/projects/p/topics/t:publish"},
		{"POST", "/v1/projects/p/subscriptions/s:pull"},
		{"GET", "/v2/projects/p/locations/us-central1/services/svc:getSomething"},
	} {
		if b, _, ok := auditMatchREST(tc.method, tc.path); ok {
			t.Errorf("%s %s matched %s", tc.method, tc.path, b.rpc.method.FullName())
		}
	}

	b, vars, _ := auditMatchREST("POST", "/v2/projects/p/locations/us-central1/services")
	request := map[string]any{"serviceId": "svc"}
	for field, value := range vars {
		auditSetPath(request, auditJSONPath(b.rpc.method.Input(), field), value)
	}
	if got := b.rpc.resourceName(request); got != "projects/p/locations/us-central1/services/svc" {
		t.Errorf("CreateService resourceName = %q", got)
	}
	if b.rpc.serviceName != "run.googleapis.com" || b.rpc.logType != auditAdminWrite {
		t.Errorf("CreateService is %s %s", b.rpc.serviceName, b.rpc.logType)
	}
}

func TestAuditLogs_EventarcPathPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"projects/_/buckets/b/objects/*.txt", "projects/_/buckets/b/objects/a.txt", true},
		{"/projects/_/buckets/b/objects/*.txt", "projects/_/buckets/b/objects/a.csv", false},
		{"projects/_/buckets/b/objects/*", "projects/_/buckets/b/objects/dir/a.txt", false},
		{"projects/_/buckets/b/objects/**", "projects/_/buckets/b/objects/dir/a.txt", true},
		{"projects/_/buckets/*/objects/reports/**", "projects/_/buckets/x/objects/reports/q1/a", true},
		{"projects/*/locations/*/services/web-*", "projects/p/locations/l/services/web-1", true},
		{"projects/*/locations/*/services/web-*", "projects/p/locations/l/services/api-1", false},
	} {
		if got := eventarcPathPatternMatch(tc.pattern, tc.name); got != tc.want {
			t.Errorf("match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}

	trigger := EventarcTrigger{
		Name: "projects/p/locations/us-central1/triggers/t",
		EventFilters: []EventarcEventFilter{
			{Attribute: "type", Value: eventarcAuditLogEventType},
			{Attribute: "serviceName", Value: "storage.googleapis.com"},
			{Attribute: "methodName", Value: "storage.buckets.create"},
		},
	}
	payload := map[string]any{"serviceName": "storage.googleapis.com", "methodName": "storage.buckets.create"}
	if !eventarcAuditLogMatches(trigger, payload, "p", "us-central1") {
		t.Error("a matching entry is not routed")
	}
	if eventarcAuditLogMatches(trigger, payload, "other", "us-central1") {
		t.Error("another project's entry is routed")
	}
	if eventarcAuditLogMatches(trigger, payload, "p", "europe-west1") {
		t.Error("another location's entry reaches a regional trigger")
	}
	trigger.Name = "projects/p/locations/global/triggers/t"
	if !eventarcAuditLogMatches(trigger, payload, "p", "europe-west1") {
		t.Error("a global trigger receives every location's entries")
	}
	if eventarcAuditLogMatches(trigger, map[string]any{"serviceName": "storage.googleapis.com", "methodName": "storage.buckets.delete"}, "p", "us") {
		t.Error("another method's entry is routed")
	}
}

// An Eventarc trigger for google.cloud.audit.log.v1.written delivers the
// entry of a matching call as a CloudEvent whose data is the LogEntryData.
func TestAuditLogs_EventarcDeliversAuditLogEvents(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	startPubSubPush(srv)
	type delivered struct {
		header http.Header
		body   []byte
	}
	events := make(chan delivered, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		events <- delivered{r.Header.Clone(), body}
	}))
	t.Cleanup(receiver.Close)

	code, out := gcpHostCall(t, srv, "eventarc.googleapis.com", http.MethodPost, "/v1/projects/audit-ev/locations/global/triggers?triggerId=no-method",
		`{"eventFilters":[{"attribute":"type","value":"google.cloud.audit.log.v1.written"},{"attribute":"serviceName","value":"storage.googleapis.com"}],
		  "destination":{"httpEndpoint":{"uri":"`+receiver.URL+`"}}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("a trigger without a methodName filter: %d %v", code, out)
	}
	gcpHostOK(t, srv, "localhost", http.MethodPost, "/v1/projects/audit-ev/locations/global/triggers?triggerId=bucket-audit",
		`{"eventFilters":[{"attribute":"type","value":"google.cloud.audit.log.v1.written"},
		                  {"attribute":"serviceName","value":"storage.googleapis.com"},
		                  {"attribute":"methodName","value":"storage.buckets.create"},
		                  {"attribute":"resourceName","value":"projects/_/buckets/audit-ev-*","operator":"match-path-pattern"}],
		  "destination":{"httpEndpoint":{"uri":"`+receiver.URL+`"}}}`)
	got := gcpHostOK(t, srv, "localhost", http.MethodGet, "/v1/projects/audit-ev/locations/global/triggers/bucket-audit", "")
	if got["name"] != "projects/audit-ev/locations/global/triggers/bucket-audit" {
		t.Fatalf("a global Eventarc trigger reads back as %v", got)
	}

	createTestProject(t, srv, "audit-ev")
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=audit-ev", `{"name":"skipped-bucket"}`)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=audit-ev", `{"name":"audit-ev-bucket"}`)

	var event delivered
	select {
	case event = <-events:
	case <-time.After(30 * time.Second):
		t.Fatal("no CloudEvent reached the destination")
	}
	for header, want := range map[string]string{
		"ce-type":         "google.cloud.audit.log.v1.written",
		"ce-source":       "//cloudaudit.googleapis.com/projects/audit-ev/logs/activity",
		"ce-subject":      "storage.googleapis.com/projects/_/buckets/audit-ev-bucket",
		"ce-servicename":  "storage.googleapis.com",
		"ce-methodname":   "storage.buckets.create",
		"ce-resourcename": "projects/_/buckets/audit-ev-bucket",
		"ce-specversion":  "1.0",
	} {
		if got := event.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if !strings.HasPrefix(event.header.Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type = %q", event.header.Get("Content-Type"))
	}
	var data LogEntry
	if err := json.Unmarshal(event.body, &data); err != nil {
		t.Fatalf("data is not a LogEntryData: %v %s", err, event.body)
	}
	if data.LogName != "projects/audit-ev/logs/cloudaudit.googleapis.com%2Factivity" || data.ProtoPayload["methodName"] != "storage.buckets.create" {
		t.Errorf("data = %+v", data)
	}
	select {
	case extra := <-events:
		t.Errorf("a second event arrived: %s", extra.header.Get("ce-subject"))
	default:
	}
}

// Every audited RPC records the permission IAM checks for it, spelled the way
// IAM spells it, and the methodName its service writes.
func TestAuditLogs_RPCPermissionsAndMethodNames(t *testing.T) {
	if err := auditLoadRPCs(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		rpc, methodName, logType, permission string
		request                              map[string]any
	}{
		{"google.cloud.run.v2.Services.CreateService", "google.cloud.run.v2.Services.CreateService", auditAdminWrite, "run.services.create",
			map[string]any{"parent": "projects/p/locations/l", "serviceId": "s"}},
		{"google.cloud.run.v2.Jobs.RunJob", "", auditAdminWrite, "run.jobs.run", map[string]any{"name": "projects/p/locations/l/jobs/j"}},
		{"google.cloud.secretmanager.v1.SecretManagerService.AccessSecretVersion", "", auditDataRead, "secretmanager.versions.access",
			map[string]any{"name": "projects/p/secrets/s/versions/1"}},
		{"google.cloud.secretmanager.v1.SecretManagerService.ListSecretVersions", "", auditAdminRead, "secretmanager.versions.list",
			map[string]any{"parent": "projects/p/secrets/s"}},
		{"google.pubsub.v1.Publisher.CreateTopic", "", auditAdminWrite, "pubsub.topics.create", map[string]any{"name": "projects/p/topics/t"}},
		{"google.cloud.kms.v1.KeyManagementService.CreateKeyRing", "CreateKeyRing", auditAdminWrite, "cloudkms.keyRings.create",
			map[string]any{"parent": "projects/p/locations/l", "keyRingId": "r"}},
		{"google.cloud.kms.v1.KeyManagementService.Encrypt", "Encrypt", auditDataRead, "cloudkms.cryptoKeyVersions.useToEncrypt",
			map[string]any{"name": "projects/p/locations/l/keyRings/r/cryptoKeys/k"}},
		{"google.iam.admin.v1.IAM.CreateServiceAccount", "google.iam.admin.v1.CreateServiceAccount", auditAdminWrite, "iam.serviceAccounts.create",
			map[string]any{"name": "projects/p"}},
		{"google.iam.admin.v1.IAM.CreateServiceAccountKey", "google.iam.admin.v1.CreateServiceAccountKey", auditAdminWrite, "iam.serviceAccountKeys.create",
			map[string]any{"name": "projects/p/serviceAccounts/a@p.iam.gserviceaccount.com"}},
		{"google.iam.admin.v1.IAM.SetIamPolicy", "google.iam.admin.v1.SetIAMPolicy", auditAdminWrite, "iam.serviceAccounts.setIamPolicy",
			map[string]any{"resource": "projects/p/serviceAccounts/a@p.iam.gserviceaccount.com"}},
		{"google.iam.admin.v1.IAM.PatchServiceAccount", "google.iam.admin.v1.PatchServiceAccount", auditAdminWrite, "iam.serviceAccounts.update",
			map[string]any{"serviceAccount": map[string]any{"name": "projects/p/serviceAccounts/a@p.iam.gserviceaccount.com"}}},
		{"google.devtools.cloudbuild.v1.CloudBuild.CreateBuildTrigger", "", auditAdminWrite, "cloudbuild.builds.create",
			map[string]any{"projectId": "p"}},
		{"google.devtools.cloudbuild.v1.CloudBuild.ListBuildTriggers", "", auditAdminRead, "cloudbuild.builds.list",
			map[string]any{"projectId": "p"}},
		{"google.cloud.eventarc.v1.Eventarc.CreateTrigger", "", auditAdminWrite, "eventarc.triggers.create",
			map[string]any{"parent": "projects/p/locations/l", "triggerId": "t"}},
		{"google.cloud.redis.v1.CloudRedis.FailoverInstance", "", auditAdminWrite, "redis.instances.failover",
			map[string]any{"name": "projects/p/locations/l/instances/i"}},
		{"google.spanner.admin.database.v1.DatabaseAdmin.DropDatabase", "", auditAdminWrite, "spanner.databases.drop",
			map[string]any{"database": "projects/p/instances/i/databases/d"}},
		{"google.bigtable.admin.v2.BigtableInstanceAdmin.PartialUpdateInstance", "", auditAdminWrite, "bigtable.instances.update",
			map[string]any{"instance": map[string]any{"name": "projects/p/instances/i"}}},
		{"google.bigtable.admin.v2.BigtableTableAdmin.CheckConsistency", "", auditAdminRead, "bigtable.tables.checkConsistency",
			map[string]any{"name": "projects/p/instances/i/tables/t"}},
		{"google.firestore.admin.v1.FirestoreAdmin.CreateDatabase", "", auditAdminWrite, "datastore.databases.create",
			map[string]any{"parent": "projects/p", "databaseId": "d"}},
		{"google.devtools.artifactregistry.v1.ArtifactRegistry.GetDockerImage", "", auditAdminRead, "artifactregistry.dockerimages.get",
			map[string]any{"name": "projects/p/locations/l/repositories/r/dockerImages/i"}},
	} {
		rpc, ok := auditRPCsByName[tc.rpc]
		if !ok {
			t.Errorf("%s is not audited", tc.rpc)
			continue
		}
		want := tc.methodName
		if want == "" {
			want = tc.rpc
		}
		if rpc.methodName != want || rpc.logType != tc.logType {
			t.Errorf("%s records methodName %q as %s, want %q as %s", tc.rpc, rpc.methodName, rpc.logType, want, tc.logType)
		}
		if got := rpc.permission(tc.request, rpc.resourceName(tc.request)); got != tc.permission {
			t.Errorf("%s checks %q, want %q", tc.rpc, got, tc.permission)
		}
	}
	for _, unlogged := range []string{"google.iam.admin.v1.IAM.TestIamPermissions", "google.bigtable.admin.v2.BigtableTableAdmin.TestIamPermissions"} {
		if _, ok := auditRPCsByName[unlogged]; ok {
			t.Errorf("%s is audited", unlogged)
		}
	}
}

// Eventarc and Cloud Build publish the same regional triggers collection; a
// call that names no host reaches the one the simulator routes it to.
func TestAuditLogs_SharedTriggersPathResolvesToItsService(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, "localhost", http.MethodPost, "/v1/projects/audit-tr/locations/us-central1/triggers?triggerId=ea",
		`{"eventFilters":[{"attribute":"type","value":"google.cloud.pubsub.topic.v1.messagePublished"}],
		  "destination":{"httpEndpoint":{"uri":"http://127.0.0.1:1/"}}}`)
	gcpHostOK(t, srv, "localhost", http.MethodPost, "/v1/projects/audit-tr/locations/us-central1/triggers",
		`{"name":"cb","build":{"steps":[{"name":"ubuntu"}]},"pubsubConfig":{"topic":"projects/audit-tr/topics/x"}}`)
	eventarc := auditEntries(t, srv, "audit-tr", auditLogActivity, `protoPayload.methodName="google.cloud.eventarc.v1.Eventarc.CreateTrigger"`)
	build := auditEntries(t, srv, "audit-tr", auditLogActivity, `protoPayload.methodName="google.devtools.cloudbuild.v1.CloudBuild.CreateBuildTrigger"`)
	if len(eventarc) != 1 || len(build) != 1 {
		t.Fatalf("Eventarc entries %v, Cloud Build entries %v", eventarc, build)
	}
	payload := auditPayload(t, eventarc[0])
	if payload["resourceName"] != "projects/audit-tr/locations/us-central1/triggers/ea" || payload["serviceName"] != "eventarc.googleapis.com" {
		t.Errorf("CreateTrigger payload = %v", payload)
	}
	op := eventarc[0]["operation"].(map[string]any)
	if op["first"] != true || op["last"] != true || op["producer"] != "eventarc.googleapis.com" || op["id"] == "" {
		t.Errorf("an operation done when the call answers writes one entry marked first and last, got %v", op)
	}
	if got := auditPayload(t, build[0])["authorizationInfo"].([]any)[0].(map[string]any)["permission"]; got != "cloudbuild.builds.create" {
		t.Errorf("CreateBuildTrigger checks %v", got)
	}
}

// Compute Engine records a call under its version and Discovery method ID,
// with the operation the call started; IAM names a service account by its
// unique ID; Cloud DNS names a zone relative to the project; and Cloud
// Resource Manager's SetIamPolicy carries the policy delta.
func TestAuditLogs_DiscoveryAndIAMServices(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	createTestProject(t, srv, "audit-ds")

	sa := gcpHostOK(t, srv, "iam.googleapis.com", http.MethodPost, "/v1/projects/audit-ds/serviceAccounts", `{"accountId":"audit-sa"}`)
	email, uniqueID := sa["email"].(string), sa["uniqueId"].(string)
	gcpHostOK(t, srv, "iam.googleapis.com", http.MethodDelete, "/v1/projects/audit-ds/serviceAccounts/"+email, "")
	created := auditEntries(t, srv, "audit-ds", auditLogActivity, `protoPayload.methodName="google.iam.admin.v1.CreateServiceAccount"`)
	deleted := auditEntries(t, srv, "audit-ds", auditLogActivity, `protoPayload.methodName="google.iam.admin.v1.DeleteServiceAccount"`)
	if len(created) != 1 || len(deleted) != 1 {
		t.Fatalf("IAM entries: created %v deleted %v", created, deleted)
	}
	if got := auditPayload(t, created[0])["resourceName"]; got != "projects/audit-ds" {
		t.Errorf("CreateServiceAccount resourceName %v", got)
	}
	if got := auditPayload(t, deleted[0])["resourceName"]; got != "projects/-/serviceAccounts/"+uniqueID {
		t.Errorf("DeleteServiceAccount resourceName %v", got)
	}
	for _, entry := range []map[string]any{created[0], deleted[0]} {
		resource := entry["resource"].(map[string]any)
		labels := resource["labels"].(map[string]any)
		if resource["type"] != "service_account" || labels["email_id"] != email || labels["unique_id"] != uniqueID {
			t.Errorf("IAM resource %v", resource)
		}
	}

	gcpHostOK(t, srv, "dns.googleapis.com", http.MethodPost, "/dns/v1/projects/audit-ds/managedZones",
		`{"name":"audit-zone","dnsName":"audit.example.com.","description":"audit"}`)
	zone := auditEntries(t, srv, "audit-ds", auditLogActivity, `protoPayload.methodName="dns.managedZones.create"`)
	if len(zone) != 1 {
		t.Fatalf("want one dns.managedZones.create entry, got %v", zone)
	}
	payload := auditPayload(t, zone[0])
	request := payload["request"].(map[string]any)
	if payload["resourceName"] != "managedZones/audit-zone" || request["@type"] != "type.googleapis.com/cloud.dns.api.ManagedZonesCreateRequest" ||
		request["project"] != "audit-ds" || request["managedZone"] == nil {
		t.Errorf("managedZones.create payload = %v", payload)
	}
	if resource := zone[0]["resource"].(map[string]any); resource["type"] != "dns_managed_zone" {
		t.Errorf("managedZones.create resource %v", resource)
	}

	policy := gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/audit-ds:getIamPolicy", `{}`)
	gcpHostOK(t, srv, "cloudresourcemanager.googleapis.com", http.MethodPost, "/v1/projects/audit-ds:setIamPolicy",
		`{"policy":{"etag":"`+policy["etag"].(string)+`","bindings":[{"role":"roles/viewer","members":["user:a@example.com"]}]}}`)
	set := auditEntries(t, srv, "audit-ds", auditLogActivity, `protoPayload.methodName="SetIamPolicy"`)
	if len(set) != 1 {
		t.Fatalf("want one SetIamPolicy entry, got %v", set)
	}
	payload = auditPayload(t, set[0])
	deltas := payload["serviceData"].(map[string]any)["policyDelta"].(map[string]any)["bindingDeltas"].([]any)
	if payload["serviceName"] != "cloudresourcemanager.googleapis.com" || payload["resourceName"] != "projects/audit-ds" ||
		len(deltas) != 1 || deltas[0].(map[string]any)["action"] != "ADD" || deltas[0].(map[string]any)["member"] != "user:a@example.com" {
		t.Errorf("SetIamPolicy payload = %v", payload)
	}
	if got := auditEntries(t, srv, "audit-ds", auditLogActivity, `protoPayload.serviceData.policyDelta.bindingDeltas.action="ADD"`); len(got) != 1 {
		t.Errorf("a filter on the binding delta finds %d entries", len(got))
	}
}
