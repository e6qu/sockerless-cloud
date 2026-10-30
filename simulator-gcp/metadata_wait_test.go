package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A metadata read carries an ETag of its content; a read that waits for a
// change returns once the content differs from last_etag, at once when it
// already does, and with the content unchanged once timeout_sec passes. A
// directory waits only in its recursive form, and a token never: the metadata
// server refuses to wait on either.
func TestMetadataReadWaitsForAChange(t *testing.T) {
	get := metadataTreeServer(t)
	const path = "/computeMetadata/v1/instance/attributes/?recursive=true"
	const before = `{"enable-guest-attributes":"TRUE"}`
	first := get(path)
	if first.Code != http.StatusOK || first.Body.String() != before || first.Header().Get("ETag") == "" {
		t.Fatalf("GET %s = %d %q with ETag %q, want %s with one", path, first.Code, first.Body.String(), first.Header().Get("ETag"), before)
	}
	etag := first.Header().Get("ETag")
	if again := get(path); again.Header().Get("ETag") != etag {
		t.Fatalf("the same content answered with ETag %q, then %q", etag, again.Header().Get("ETag"))
	}
	if listing := get("/computeMetadata/v1/instance/attributes/?wait_for_change=true"); listing.Code != http.StatusBadRequest {
		t.Fatalf("a wait on a directory listing answered %d %q, want 400", listing.Code, listing.Body.String())
	}
	if token := get("/computeMetadata/v1/instance/service-accounts/default/token?wait_for_change=true"); token.Code != http.StatusBadRequest {
		t.Fatalf("a wait on the token answered %d %q, want 400", token.Code, token.Body.String())
	}

	waited := make(chan *httptest.ResponseRecorder, 1)
	go func() { waited <- get(path + "&wait_for_change=true&last_etag=" + etag) }()
	select {
	case rec := <-waited:
		t.Fatalf("the read returned before anything changed: %d %q", rec.Code, rec.Body.String())
	case <-time.After(200 * time.Millisecond):
	}
	const after = `{"enable-guest-attributes":"TRUE","marker":"changed"}`
	gcpInstances.Update(metadataReaderSelfLink, func(inst *ComputeInstance) {
		inst.Metadata.Items = append(inst.Metadata.Items, ComputeInstanceMetadataItem{Key: "marker", Value: "changed"})
	})
	rec := <-waited
	if rec.Code != http.StatusOK || rec.Body.String() != after || rec.Header().Get("ETag") == etag {
		t.Fatalf("the waiting read answered %d %q with ETag %q, want %s under a new ETag", rec.Code, rec.Body.String(), rec.Header().Get("ETag"), after)
	}

	if stale := get(path + "&wait_for_change=true&last_etag=" + etag); stale.Body.String() != after {
		t.Fatalf("a read naming an ETag the content no longer has waited: %d %q", stale.Code, stale.Body.String())
	}
	if bad := get(path + "&wait_for_change=true&timeout_sec=soon"); bad.Code != http.StatusBadRequest {
		t.Fatalf("timeout_sec=soon answered %d, want 400", bad.Code)
	}
	start := time.Now()
	timedOut := get(path + "&wait_for_change=true&timeout_sec=1")
	if timedOut.Code != http.StatusOK || timedOut.Body.String() != after || time.Since(start) < time.Second {
		t.Fatalf("a read that timed out answered %d %q after %s", timedOut.Code, timedOut.Body.String(), time.Since(start))
	}
}
