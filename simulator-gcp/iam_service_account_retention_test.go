package main

import (
	"net/http"
	"testing"
	"time"
)

// A deleted service account stays restorable until 30 days after its deletion
// and is gone for good at that point. The test ages the deletion rather than
// waiting the window out: it moves the recorded deleteTime back, which is what
// the passage of the same time does to the comparison against the clock.
func TestIAM_DeletedServiceAccountRetentionWindow(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "iam.googleapis.com"

	age := func(uniqueID string, by time.Duration) {
		t.Helper()
		if !iamDeletedServiceAccounts.Update(uniqueID, func(d *gcpDeletedServiceAccount) {
			d.DeleteTime = d.DeleteTime.Add(-by)
		}) {
			t.Fatalf("no deleted account is held under %s", uniqueID)
		}
	}
	createAndDelete := func(accountID string) string {
		t.Helper()
		sa := gcpHostOK(t, srv, host, http.MethodPost, "/v1/projects/retention-p/serviceAccounts", `{"accountId":"`+accountID+`"}`)
		gcpHostOK(t, srv, host, http.MethodDelete, "/v1/projects/retention-p/serviceAccounts/"+sa["email"].(string), "")
		return sa["uniqueId"].(string)
	}

	inside := createAndDelete("retention-inside")
	age(inside, iamServiceAccountRetention-time.Minute)
	restored := gcpHostOK(t, srv, host, http.MethodPost, "/v1/projects/-/serviceAccounts/"+inside+":undelete", `{}`)
	if got := restored["restoredAccount"].(map[string]any)["uniqueId"]; got != inside {
		t.Fatalf("undelete inside the window restored %v, want unique ID %s", got, inside)
	}

	lapsed := createAndDelete("retention-lapsed")
	age(lapsed, iamServiceAccountRetention)
	code, body := gcpHostCall(t, srv, host, http.MethodPost, "/v1/projects/-/serviceAccounts/"+lapsed+":undelete", `{}`)
	if code != http.StatusForbidden {
		t.Fatalf("undelete after the window answered %d %v, want 403 PERMISSION_DENIED", code, body)
	}
	if _, held := iamDeletedServiceAccounts.Get(lapsed); held {
		t.Fatalf("an account deleted 30 days ago is still held")
	}
	code, body = gcpHostCall(t, srv, host, http.MethodPost, "/v1/projects/retention-p/serviceAccounts/"+lapsed+":undelete", `{}`)
	if code != http.StatusNotFound {
		t.Fatalf("undelete of a purged account under its project answered %d %v, want 404", code, body)
	}
}
