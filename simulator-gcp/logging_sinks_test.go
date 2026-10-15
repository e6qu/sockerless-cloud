package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// The exclusions API reads and writes the _Default sink's exclusions.
func TestLoggingExclusionsLiveOnTheDefaultSink(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	created := gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/ex/exclusions", `{"name":"debug","filter":"severity<INFO"}`)
	if created["name"] != "debug" || created["createTime"] == nil {
		t.Fatalf("created exclusion = %v", created)
	}
	sink := gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/ex/sinks/_Default", ``)
	exclusions, _ := sink["exclusions"].([]any)
	if len(exclusions) != 1 || exclusions[0].(map[string]any)["name"] != "debug" {
		t.Fatalf("the _Default sink's exclusions = %v", sink["exclusions"])
	}
	if code, _ := gcpHostCall(t, srv, host, http.MethodPost, "/v2/projects/ex/exclusions", `{"name":"debug","filter":"severity<INFO"}`); code != http.StatusConflict {
		t.Errorf("a second exclusion named debug answered %d", code)
	}
	gcpHostOK(t, srv, host, http.MethodDelete, "/v2/projects/ex/exclusions/debug", ``)
	sink = gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/ex/sinks/_Default", ``)
	if exclusions, _ := sink["exclusions"].([]any); len(exclusions) != 0 {
		t.Fatalf("after the delete the _Default sink's exclusions = %v", sink["exclusions"])
	}
}

// A sink can route to another project's log bucket, or to another project,
// whose own sinks then route the entry.
func TestLoggingSinksRouteAcrossProjects(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	central := "projects/central/locations/global/buckets/central"
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/central/locations/global/buckets?bucketId=central", `{}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/app/sinks", `{"name":"to-central",
		"destination":"logging.googleapis.com/`+central+`","filter":"severity>=ERROR"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/app/sinks", `{"name":"to-hub",
		"destination":"logging.googleapis.com/projects/hub","filter":"severity=WARNING"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/app/logs/svc","timestamp":"`+nowRFC3339()+`","severity":"ERROR","textPayload":"failure"},
		{"logName":"projects/app/logs/svc","timestamp":"`+nowRFC3339()+`","severity":"WARNING","textPayload":"warning"}]}`)
	if got := loggingPayloads(t, srv, central); !slices.Equal(got, []string{"failure"}) {
		t.Errorf("the central bucket holds %v", got)
	}
	if got := loggingPayloads(t, srv, "projects/hub/locations/global/buckets/_Default"); !slices.Equal(got, []string{"warning"}) {
		t.Errorf("the hub project's _Default bucket holds %v", got)
	}
}

// A folder's sink with includeChildren routes its projects' entries, and one
// that intercepts them keeps them from the projects' own sinks but _Required.
func TestLoggingAggregatedSinksSeeChildProjects(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	crmFolders.Put("folders/42", CRMFolder{Name: "folders/42", Parent: "organizations/7"})
	crmProjects.Put("child", CRMProject{Name: "projects/9001", ProjectId: "child", Parent: "folders/42", State: "ACTIVE"})

	audit := "projects/audit/locations/global/buckets/audit"
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/audit/locations/global/buckets?bucketId=audit", `{}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/folders/42/sinks", `{"name":"all-errors","includeChildren":true,
		"destination":"logging.googleapis.com/`+audit+`","filter":"severity>=ERROR"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/organizations/7/sinks", `{"name":"intercept-debug","includeChildren":true,
		"interceptChildren":true,"destination":"logging.googleapis.com/projects/audit","filter":"severity=DEBUG"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/child/logs/svc","timestamp":"`+nowRFC3339()+`","severity":"ERROR","textPayload":"error"},
		{"logName":"projects/child/logs/svc","timestamp":"`+nowRFC3339()+`","severity":"DEBUG","textPayload":"debug"},
		{"logName":"projects/child/logs/cloudaudit.googleapis.com%2Factivity","timestamp":"`+nowRFC3339()+`","severity":"DEBUG","textPayload":"audit debug"}]}`)
	if got := loggingPayloads(t, srv, audit); !slices.Equal(got, []string{"error"}) {
		t.Errorf("the folder sink's bucket holds %v", got)
	}
	if got := loggingPayloads(t, srv, "projects/child/locations/global/buckets/_Default"); !slices.Equal(got, []string{"error"}) {
		t.Errorf("the child's _Default bucket holds %v, want only what no sink intercepted", got)
	}
	if got := loggingPayloads(t, srv, "projects/child/locations/global/buckets/_Required"); !slices.Equal(got, []string{"audit debug"}) {
		t.Errorf("the child's _Required bucket holds %v", got)
	}
	if got := loggingPayloads(t, srv, "projects/audit/locations/global/buckets/_Default"); !slices.Equal(got, []string{"debug"}) {
		t.Errorf("the intercepting sink's project's _Default bucket holds %v", got)
	}
	if got := loggingPayloads(t, srv, "projects/audit/locations/global/buckets/_Required"); !slices.Equal(got, []string{"audit debug"}) {
		t.Errorf("the intercepting sink's project's _Required bucket holds %v", got)
	}
}

// An organization's settings shape the _Default sink its new projects start
// with.
func TestLoggingSettingsShapeNewProjectsDefaultSink(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	crmProjects.Put("quiet", CRMProject{Name: "projects/9002", ProjectId: "quiet", Parent: "organizations/8", State: "ACTIVE"})
	crmProjects.Put("trimmed", CRMProject{Name: "projects/9003", ProjectId: "trimmed", Parent: "organizations/9", State: "ACTIVE"})
	gcpHostOK(t, srv, host, http.MethodPatch, "/v2/organizations/8/settings", `{"disableDefaultSink":true}`)
	gcpHostOK(t, srv, host, http.MethodPatch, "/v2/organizations/9/settings",
		`{"defaultSinkConfig":{"mode":"APPEND","filter":"severity>=WARNING","exclusions":[{"name":"noisy","filter":"textPayload:\"noise\""}]}}`)
	if sink := gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/quiet/sinks/_Default", ``); sink["disabled"] != true {
		t.Errorf("under disableDefaultSink the _Default sink = %v", sink)
	}
	sink := gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/trimmed/sinks/_Default", ``)
	filter, _ := sink["filter"].(string)
	if !strings.HasSuffix(filter, "AND (severity>=WARNING)") || len(sink["exclusions"].([]any)) != 1 {
		t.Fatalf("under defaultSinkConfig the _Default sink = %v", sink)
	}
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/trimmed/logs/a","timestamp":"`+nowRFC3339()+`","severity":"INFO","textPayload":"info"},
		{"logName":"projects/trimmed/logs/a","timestamp":"`+nowRFC3339()+`","severity":"ERROR","textPayload":"noise"},
		{"logName":"projects/trimmed/logs/a","timestamp":"`+nowRFC3339()+`","severity":"ERROR","textPayload":"kept"}]}`)
	if got := loggingPayloads(t, srv, "projects/trimmed"); !slices.Equal(got, []string{"kept"}) {
		t.Errorf("the project holds %v", got)
	}
}

// A sink to a Pub/Sub topic publishes each entry as JSON with its timestamp
// attribute.
func TestLoggingSinkPublishesToPubSub(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPut, "/v1/projects/ps/topics/logs", `{}`)
	gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPut, "/v1/projects/ps/subscriptions/logs-sub", `{"topic":"projects/ps/topics/logs"}`)
	gcpHostOK(t, srv, "logging.googleapis.com", http.MethodPost, "/v2/projects/ps/sinks", `{"name":"to-topic",
		"destination":"pubsub.googleapis.com/projects/ps/topics/logs","filter":"severity>=ERROR"}`)
	at := nowRFC3339()
	gcpHostOK(t, srv, "logging.googleapis.com", http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/ps/logs/a","timestamp":"`+at+`","severity":"ERROR","textPayload":"published"},
		{"logName":"projects/ps/logs/a","timestamp":"`+at+`","severity":"INFO","textPayload":"not published"}]}`)
	out := gcpHostOK(t, srv, "pubsub.googleapis.com", http.MethodPost, "/v1/projects/ps/subscriptions/logs-sub:pull", `{"maxMessages":10}`)
	received, _ := out["receivedMessages"].([]any)
	if len(received) != 1 {
		t.Fatalf("pulled %v", out)
	}
	msg := received[0].(map[string]any)["message"].(map[string]any)
	data, err := base64.StdEncoding.DecodeString(msg["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var entry LogEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.TextPayload != "published" {
		t.Fatalf("message data %s (%v)", data, err)
	}
	if attrs, _ := msg["attributes"].(map[string]any); attrs["logging.googleapis.com/timestamp"] != at {
		t.Errorf("message attributes = %v", msg["attributes"])
	}
}

// A sink to Cloud Storage delivers each hour's entries once the hour of their
// ingestion has passed, as a new shard of the hour's object.
func TestLoggingSinkDeliversHourlyToCloudStorage(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	gcpHostOK(t, srv, "storage.googleapis.com", http.MethodPost, "/storage/v1/b?project=test-project", `{"name":"log-archive"}`)
	gcpHostOK(t, srv, "logging.googleapis.com", http.MethodPost, "/v2/projects/gcs/sinks", `{"name":"to-gcs",
		"destination":"storage.googleapis.com/log-archive"}`)
	at := time.Now().UTC()
	write := func(payload string) {
		gcpHostOK(t, srv, "logging.googleapis.com", http.MethodPost, "/v2/entries:write", `{"entries":[
			{"logName":"projects/gcs/logs/app","timestamp":"`+at.Format(time.RFC3339)+`","textPayload":"`+payload+`"}]}`)
	}
	write("first")
	if n := loggingFlushStorageExports(at); n != 0 {
		t.Fatalf("delivered %d objects within the hour", n)
	}
	prefix := "app/" + at.Format("2006/01/02") + "/" + at.Format("15") + ":00:00_" + at.Format("15") + ":59:59"
	if n := loggingFlushStorageExports(at.Add(time.Hour)); n != 1 {
		t.Fatalf("delivered %d objects after the hour", n)
	}
	write("second")
	loggingFlushStorageExports(at.Add(time.Hour))
	for shard, want := range []string{"first", "second"} {
		data, err := GCSObjectBytes("log-archive", prefix+"_S"+string(rune('0'+shard))+".json")
		if err != nil {
			t.Fatalf("shard %d: %v", shard, err)
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil || entry.TextPayload != want {
			t.Fatalf("shard %d holds %s (%v)", shard, data, err)
		}
	}
}

// Every log bucket has the _AllLogs view, and a _Default bucket the _Default
// view; neither can be deleted.
func TestLoggingBucketsHaveBuiltInViews(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	views := func(bucket string) []string {
		out := gcpHostOK(t, srv, host, http.MethodGet, "/v2/projects/v/locations/global/buckets/"+bucket+"/views", ``)
		var names []string
		for _, v := range out["views"].([]any) {
			names = append(names, lastSegment(v.(map[string]any)["name"].(string)))
		}
		return names
	}
	if got := views("_Default"); !slices.Equal(got, []string{"_AllLogs", "_Default"}) {
		t.Errorf("the _Default bucket's views = %v", got)
	}
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/v/locations/global/buckets?bucketId=custom", `{}`)
	if got := views("custom"); !slices.Equal(got, []string{"_AllLogs"}) {
		t.Errorf("a new bucket's views = %v", got)
	}
	if code, _ := gcpHostCall(t, srv, host, http.MethodDelete, "/v2/projects/v/locations/global/buckets/custom/views/_AllLogs", ``); code != http.StatusBadRequest {
		t.Errorf("deleting _AllLogs answered %d", code)
	}
}
