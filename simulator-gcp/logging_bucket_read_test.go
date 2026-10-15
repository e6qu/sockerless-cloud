package main

import (
	"net/http"
	"slices"
	"testing"
	"time"
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
	day := time.Now().UTC().AddDate(0, 0, -2).Truncate(24 * time.Hour)
	at := func(hour, minute int) string {
		return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute).Format(time.RFC3339)
	}
	gcpHostOK(t, srv, host, http.MethodPost, "/v2/entries:write", `{"entries":[
		{"logName":"projects/p/logs/app","resource":{"type":"gce_instance"},"timestamp":"`+at(10, 0)+`","severity":"ERROR","textPayload":"routed vm"},
		{"logName":"projects/p/logs/app","resource":{"type":"global"},"timestamp":"`+at(10, 1)+`","severity":"WARNING","textPayload":"routed global"},
		{"logName":"projects/p/logs/app","resource":{"type":"gce_instance"},"timestamp":"`+at(10, 2)+`","severity":"INFO","textPayload":"not routed"},
		{"logName":"projects/p2/logs/app","resource":{"type":"global"},"timestamp":"`+at(10, 3)+`","severity":"ERROR","textPayload":"other project"}]}`)

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
