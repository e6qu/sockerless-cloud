package main

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// Concurrent setIamPolicy calls that carry the etag one getIamPolicy returned
// race for it: exactly one writes, every other is refused ABORTED, and the
// policy holds the binding of the one that won.
func TestSetIamPolicyAdmitsOneWriterPerEtag(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "cloudresourcemanager.googleapis.com"
	policy := gcpHostOK(t, srv, host, http.MethodPost, "/v1/projects/test-project:getIamPolicy", `{}`)
	etag := fmt.Sprint(policy["etag"])

	const writers = 64
	codes := make([]int, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = gcpHostCall(t, srv, host, http.MethodPost, "/v1/projects/test-project:setIamPolicy",
				fmt.Sprintf(`{"policy":{"etag":%q,"bindings":[{"role":"roles/viewer","members":["user:writer-%d@example.com"]}]}}`, etag, i))
		}()
	}
	wg.Wait()
	won := -1
	for i, code := range codes {
		switch code {
		case http.StatusOK:
			if won >= 0 {
				t.Fatalf("writers %d and %d both wrote over etag %s", won, i, etag)
			}
			won = i
		case http.StatusConflict:
		default:
			t.Fatalf("writer %d got %d", i, code)
		}
	}
	if won < 0 {
		t.Fatal("no writer wrote")
	}
	after := gcpHostOK(t, srv, host, http.MethodPost, "/v1/projects/test-project:getIamPolicy", `{}`)
	if got := fmt.Sprint(after["bindings"]); got != fmt.Sprintf("[map[members:[user:writer-%d@example.com] role:roles/viewer]]", won) {
		t.Fatalf("policy bindings = %s, want writer %d's", got, won)
	}
}
