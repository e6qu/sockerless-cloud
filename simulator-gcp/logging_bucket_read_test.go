package main

import (
	"net/http"
	"slices"
	"testing"
)

// entries.list with a log bucket or one of its views as the resource name reads
// what the scope's sinks route to the bucket, narrowed by the view's filter; a
// project resource name reads that project's logs and not another's whose ID
// it prefixes.
func TestLoggingEntriesListReadsBucketsAndViews(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "logging.googleapis.com"
	bucket := "projects/p/locations/global/buckets/archive"
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/p/locations/global/buckets?bucketId=archive", `{}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/p/locations/global/buckets/archive/views?viewId=gce",
		`{"filter":"SOURCE(\"projects/p\") AND resource.type = \"gce_instance\""}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/projects/p/sinks", `{"name":"to-archive",
		"destination":"logging.googleapis.com/`+bucket+`","filter":"severity>=WARNING"}`)
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/p/logs/app","resource":{"type":"gce_instance"},"timestamp":"2026-09-01T10:00:00Z","severity":"ERROR","textPayload":"routed vm"},
		{"logName":"projects/p/logs/app","resource":{"type":"global"},"timestamp":"2026-09-01T10:01:00Z","severity":"WARNING","textPayload":"routed global"},
		{"logName":"projects/p/logs/app","resource":{"type":"gce_instance"},"timestamp":"2026-09-01T10:02:00Z","severity":"INFO","textPayload":"not routed"},
		{"logName":"projects/p2/logs/app","resource":{"type":"global"},"timestamp":"2026-09-01T10:03:00Z","severity":"ERROR","textPayload":"other project"}]}`)

	read := func(resourceName string) []string {
		t.Helper()
		out := gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:list", `{"resourceNames":["`+resourceName+`"]}`)
		entries, _ := out["entries"].([]any)
		var payloads []string
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			payload, _ := entry["textPayload"].(string)
			payloads = append(payloads, payload)
		}
		slices.Sort(payloads)
		return payloads
	}
	if got := read(bucket); !slices.Equal(got, []string{"routed global", "routed vm"}) {
		t.Fatalf("the bucket reads %v", got)
	}
	if got := read(bucket + "/views/gce"); !slices.Equal(got, []string{"routed vm"}) {
		t.Fatalf("the view reads %v", got)
	}
	if got := read("projects/p"); !slices.Equal(got, []string{"not routed", "routed global", "routed vm"}) {
		t.Fatalf("the project reads %v", got)
	}
}
