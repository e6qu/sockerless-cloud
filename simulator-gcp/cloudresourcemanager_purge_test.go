package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A project shut down 30 days ago is deleted for good, and its ID is never
// handed out again; one still inside its recovery window stays.
func TestCRMPurgesProjectsPastTheirRecoveryWindow(t *testing.T) {
	srv := gcpCustomMethodTestServer(t)
	now := time.Now().UTC()
	for id, deleted := range map[string]time.Time{
		"purge-me-31": now.Add(-31 * 24 * time.Hour),
		"keep-me-29":  now.Add(-29 * 24 * time.Hour),
	} {
		crmProjects.Put(id, CRMProject{Name: "projects/" + gcpNumericID(12), ProjectId: id, State: "DELETE_REQUESTED",
			Parent: crmDefaultOrganization, DeleteTime: deleted.Format(time.RFC3339Nano)})
	}
	next, pending := crmPurgeDue(now)
	if !pending || next.Sub(now) > 24*time.Hour+time.Minute {
		t.Fatalf("the next purge is due at %v (pending %v), a day from now", next, pending)
	}
	if _, ok := crmProjects.Get("purge-me-31"); ok {
		t.Fatal("a project past its 30-day window is still stored")
	}
	if _, ok := crmProjects.Get("keep-me-29"); !ok {
		t.Fatal("a project inside its window was purged")
	}
	org := strings.TrimPrefix(crmDefaultOrganization, "organizations/")
	for path, body := range map[string]string{
		"/v3/projects": `{"projectId":"purge-me-31","parent":"` + crmDefaultOrganization + `"}`,
		"/v1/projects": `{"projectId":"purge-me-31","parent":{"type":"organization","id":"` + org + `"}}`,
	} {
		status, body := gcpDo(t, srv, http.MethodPost, "cloudresourcemanager.googleapis.com", path, body)
		if status != http.StatusConflict {
			t.Fatalf("POST %s reusing a purged ID → %d %s, want 409", path, status, body)
		}
	}
	status, body := gcpDo(t, srv, http.MethodGet, "cloudresourcemanager.googleapis.com", "/v3/projects/purge-me-31", "")
	if status == http.StatusOK {
		t.Fatalf("a purged project is still retrievable: %s", body)
	}
}
