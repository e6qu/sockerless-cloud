package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Deleting a resource group deletes what it holds through each resource's own
// provider before the group goes, and a second delete finds no group.
func TestResourceGroupDeleteDeletesItsResources(t *testing.T) {
	srv := newMoveTestServer(t)
	const group = "rg-delete-contents"
	moveEnsureRG(t, srv, group)
	groupPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", moveTestSubscription, group)
	identityID := groupPath + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/rg-delete-identity"
	if status, body := moveARM(t, srv, http.MethodPut, identityID+"?api-version=2023-01-31", `{"location":"eastus"}`); status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("PUT %s: %d %s", identityID, status, body)
	}

	location := deleteResourceGroupAndWait(t, srv, group)
	if status, body := moveARM(t, srv, http.MethodGet, location, ""); status != http.StatusOK {
		t.Fatalf("the finished delete's Location answered %d %s", status, body)
	}
	if status, body := moveARM(t, srv, http.MethodGet, identityID+"?api-version=2023-01-31", ""); status != http.StatusNotFound {
		t.Errorf("%s survived its group's delete: %d %s", identityID, status, body)
	}
	if rows := azureEnumerateResources(moveTestSubscription, group); len(rows) != 0 {
		t.Errorf("the deleted group still lists %d resources", len(rows))
	}
	status, body := moveARM(t, srv, http.MethodGet, groupPath, "")
	if status != http.StatusNotFound || !strings.Contains(string(body), "ResourceGroupNotFound") {
		t.Fatalf("the deleted group answered %d %s", status, body)
	}
	status, body = moveARM(t, srv, http.MethodDelete, groupPath, "")
	if status != http.StatusNotFound || !strings.Contains(string(body), "ResourceGroupNotFound") {
		t.Fatalf("deleting a missing group answered %d %s", status, body)
	}
}

// deleteResourceGroupAndWait deletes the group, waits for the operation its
// Location names to settle, and returns that Location's path and query.
func deleteResourceGroupAndWait(t *testing.T, srv *sim.Server, group string) string {
	t.Helper()
	groupPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", moveTestSubscription, group)
	req := httptest.NewRequest(http.MethodDelete, "http://"+moveTestARMHost+groupPath+"?api-version=2021-04-01", nil)
	req.Header.Set("Authorization", "Bearer "+moveARMToken(t))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete group: %d %s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location %q: %v", rec.Header().Get("Location"), err)
	}
	prefix := fmt.Sprintf("/subscriptions/%s/operationresults/", moveTestSubscription)
	if !strings.HasPrefix(location.Path, prefix) {
		t.Fatalf("Location %q is not under %s", location, prefix)
	}
	if err := azureAwaitAsyncOperation(context.Background(), strings.TrimPrefix(location.Path, prefix)); err != nil {
		t.Fatalf("await the delete: %v", err)
	}
	return location.RequestURI()
}

func TestResourceGroupDeleteOperationResultReportsFailure(t *testing.T) {
	azureLROTestStore(t)
	opID := issueAzureAsyncOperationOutcome(func() *AsyncOperationError {
		return &AsyncOperationError{Code: "ResourceGroupDeletionBlocked", Message: "blocked"}
	})
	req := httptest.NewRequest(http.MethodGet, "/subscriptions/sub/operationresults/"+opID, nil)
	rec := httptest.NewRecorder()
	req.SetPathValue("operationId", opID)
	handleSubscriptionOperationResult(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a blocked delete's result answered %d, want 409", rec.Code)
	}
	var envelope struct {
		Error AsyncOperationError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if envelope.Error.Code != "ResourceGroupDeletionBlocked" {
		t.Fatalf("error envelope %s", rec.Body.String())
	}
}
