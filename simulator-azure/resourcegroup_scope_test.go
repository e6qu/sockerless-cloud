package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// createTestResourceGroup creates a resource group the way a client must
// before it creates anything in it.
func createTestResourceGroup(t *testing.T, srv *sim.Server, subscription, group string) {
	t.Helper()
	now := time.Now()
	token, err := mintBootstrapSimJWT("https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/subscriptions/%s/resourceGroups/%s?api-version=2021-04-01", subscription, group),
		strings.NewReader(`{"location":"eastus"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create resource group %s: status %d: %s", group, rec.Code, rec.Body.String())
	}
}

func TestResourceGroupScopeRefusesAMissingGroup(t *testing.T) {
	srv := newMoveTestServer(t)
	const group = "rg-scope-missing"
	identity := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/scope-identity?api-version=2023-01-31",
		moveTestSubscription, group)
	for _, method := range []string{http.MethodPut, http.MethodGet, http.MethodPatch, http.MethodDelete} {
		body := ""
		if method == http.MethodPut || method == http.MethodPatch {
			body = `{"location":"eastus"}`
		}
		status, resp := moveARM(t, srv, method, identity, body)
		if status != http.StatusNotFound || !strings.Contains(string(resp), `"code":"ResourceGroupNotFound"`) ||
			!strings.Contains(string(resp), "Resource group '"+group+"' could not be found.") {
			t.Errorf("%s in a missing group answered %d %s", method, status, resp)
		}
	}
	lowercase := strings.Replace(identity, "/resourceGroups/", "/resourcegroups/", 1)
	if status, resp := moveARM(t, srv, http.MethodPut, lowercase, `{"location":"eastus"}`); status != http.StatusNotFound ||
		!strings.Contains(string(resp), "ResourceGroupNotFound") {
		t.Errorf("PUT through a lowercase resourcegroups segment answered %d %s", status, resp)
	}
	groupScoped := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/resources?api-version=2021-04-01", moveTestSubscription, group)
	if status, resp := moveARM(t, srv, http.MethodGet, groupScoped, ""); status != http.StatusNotFound ||
		!strings.Contains(string(resp), "ResourceGroupNotFound") {
		t.Errorf("listing a missing group's resources answered %d %s", status, resp)
	}
	if rows := azureEnumerateResources(moveTestSubscription, group); len(rows) != 0 {
		t.Errorf("a missing group holds %d resources", len(rows))
	}

	moveEnsureRG(t, srv, strings.ToUpper(group))
	if status, resp := moveARM(t, srv, http.MethodPut, identity, `{"location":"eastus"}`); status != http.StatusCreated && status != http.StatusOK {
		t.Errorf("PUT in a group named in another case answered %d %s", status, resp)
	}
}

func TestResourceGroupScopeRefusesWritesWhileTheGroupDeletes(t *testing.T) {
	srv := newMoveTestServer(t)
	const group = "rg-scope-deleting"
	moveEnsureRG(t, srv, group)
	groupID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", moveTestSubscription, group)
	identity := groupID + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/scope-identity?api-version=2023-01-31"
	if status, resp := moveARM(t, srv, http.MethodPut, identity, `{"location":"eastus"}`); status != http.StatusCreated {
		t.Fatalf("PUT identity: %d %s", status, resp)
	}

	// handleResourceGroupDelete marks the group Deleting for as long as its
	// operation runs.
	azureResourceGroups.Update(groupID, func(g *ResourceGroup) { g.Properties.ProvisioningState = "Deleting" })
	other := groupID + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/scope-other?api-version=2023-01-31"
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		status, resp := moveARM(t, srv, method, other, `{"location":"eastus"}`)
		if status != http.StatusConflict || !strings.Contains(string(resp), `"code":"ResourceGroupBeingDeleted"`) ||
			!strings.Contains(string(resp), "The resource group '"+group+"' is in deprovisioning state and cannot perform this operation.") {
			t.Errorf("%s in a deleting group answered %d %s", method, status, resp)
		}
	}
	if status, resp := moveARM(t, srv, http.MethodGet, identity, ""); status != http.StatusOK {
		t.Errorf("GET in a deleting group answered %d %s", status, resp)
	}
	if status, resp := moveARM(t, srv, http.MethodDelete, identity, ""); status != http.StatusOK {
		t.Errorf("DELETE in a deleting group answered %d %s", status, resp)
	}
	azureResourceGroups.Update(groupID, func(g *ResourceGroup) { g.Properties.ProvisioningState = "Succeeded" })

	if status, resp := moveARM(t, srv, http.MethodPut, other, `{"location":"eastus"}`); status != http.StatusCreated {
		t.Fatalf("PUT identity: %d %s", status, resp)
	}
	deleteResourceGroupAndWait(t, srv, group)
	if status, resp := moveARM(t, srv, http.MethodGet, other, ""); status != http.StatusNotFound ||
		!strings.Contains(string(resp), "ResourceGroupNotFound") {
		t.Errorf("GET after the group's delete answered %d %s", status, resp)
	}
}
