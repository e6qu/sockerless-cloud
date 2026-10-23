package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A metadata read carries an ETag of its content; a read that waits for a
// change returns once the content differs from last_etag, at once when it
// already does, and with the content unchanged once timeout_sec passes.
func TestMetadataReadWaitsForAChange(t *testing.T) {
	get := metadataTreeServer(t)
	const path = "/computeMetadata/v1/instance/attributes/"
	first := get(path)
	if first.Code != http.StatusOK || first.Body.String() != "" || first.Header().Get("ETag") == "" {
		t.Fatalf("GET %s = %d %q with ETag %q, want the empty listing with one", path, first.Code, first.Body.String(), first.Header().Get("ETag"))
	}
	etag := first.Header().Get("ETag")
	if again := get(path); again.Header().Get("ETag") != etag {
		t.Fatalf("the same content answered with ETag %q, then %q", etag, again.Header().Get("ETag"))
	}

	waited := make(chan *httptest.ResponseRecorder, 1)
	go func() { waited <- get(path + "?wait_for_change=true&last_etag=" + etag) }()
	select {
	case rec := <-waited:
		t.Fatalf("the read returned before anything changed: %d %q", rec.Code, rec.Body.String())
	case <-time.After(200 * time.Millisecond):
	}
	selfLink := "https://www.googleapis.com/compute/v1/projects/" + gcpTestProjectID + "/zones/us-central1-a/instances/metadata-reader"
	gcpInstances.Update(selfLink, func(inst *ComputeInstance) {
		inst.Metadata = &ComputeInstanceMetadata{Items: []ComputeInstanceMetadataItem{{Key: "marker", Value: "changed"}}}
	})
	rec := <-waited
	if rec.Code != http.StatusOK || rec.Body.String() != "marker\n" || rec.Header().Get("ETag") == etag {
		t.Fatalf("the waiting read answered %d %q with ETag %q, want the new listing under a new ETag", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}

	if stale := get(path + "?wait_for_change=true&last_etag=" + etag); stale.Body.String() != "marker\n" {
		t.Fatalf("a read naming an ETag the content no longer has waited: %d %q", stale.Code, stale.Body.String())
	}
	if bad := get(path + "?wait_for_change=true&timeout_sec=soon"); bad.Code != http.StatusBadRequest {
		t.Fatalf("timeout_sec=soon answered %d, want 400", bad.Code)
	}
	start := time.Now()
	timedOut := get(path + "?wait_for_change=true&timeout_sec=1")
	if timedOut.Code != http.StatusOK || timedOut.Body.String() != "marker\n" || time.Since(start) < time.Second {
		t.Fatalf("a read that timed out answered %d %q after %s", timedOut.Code, timedOut.Body.String(), time.Since(start))
	}
}
