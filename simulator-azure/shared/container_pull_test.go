package simulator

import (
	"errors"
	"strings"
	"testing"
)

func TestDrainImagePullSurfacesStreamErrors(t *testing.T) {
	// Pull errors arrive as JSON events inside a 200 body.
	failed := `{"status":"Pulling from library/node"}
{"errorDetail":{"message":"received unexpected HTTP status: 503 Service Unavailable"},"error":"received unexpected HTTP status: 503 Service Unavailable"}
`
	err := drainImagePull(strings.NewReader(failed), "public.ecr.aws/docker/library/node:20-alpine")
	if err == nil {
		t.Fatal("failed pull stream must surface an error")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "node:20-alpine") {
		t.Errorf("error should carry the stream failure + image: %v", err)
	}

	ok := `{"status":"Pulling from library/alpine"}
{"status":"Pull complete"}
{"status":"Status: Downloaded newer image for alpine:latest"}
`
	if err := drainImagePull(strings.NewReader(ok), "alpine:latest"); err != nil {
		t.Errorf("clean pull stream errored: %v", err)
	}

	if err := drainImagePull(strings.NewReader("not-json"), "x"); err == nil {
		t.Error("malformed stream should error, not pass silently")
	}
}

// TestIsTransientRegistryErr pins the retry classifier: registry-side
// throttling and momentary unavailability retry; everything else
// (auth, not-found, malformed) fails immediately.
func TestIsTransientRegistryErr(t *testing.T) {
	transient := []string{
		"image pull x: toomanyrequests: Rate exceeded",
		"image pull x: received unexpected HTTP status: 503 Service Unavailable",
		"image pull x: too many requests",
		"image pull x: status code 429",
	}
	for _, msg := range transient {
		if !isTransientRegistryErr(errors.New(msg)) {
			t.Errorf("%q should classify transient", msg)
		}
	}
	permanent := []string{
		"image pull x: manifest unknown",
		"image pull x: pull access denied",
		"image pull x: unauthorized: authentication required",
		"image pull x: malformed pull stream: unexpected EOF",
	}
	for _, msg := range permanent {
		if isTransientRegistryErr(errors.New(msg)) {
			t.Errorf("%q should NOT classify transient", msg)
		}
	}
}
