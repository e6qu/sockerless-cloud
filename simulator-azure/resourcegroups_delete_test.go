package main

import (
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
// provider before the group goes. The App Service Environment refuses while an
// App Service plan is still deployed in it, so the delete has to come back to
// it once the plan is gone.
func TestResourceGroupDeleteDeletesItsResourcesInDependencyOrder(t *testing.T) {
	srv := newMoveTestServer(t)
	const group = "rg-delete-order"
	moveEnsureRG(t, srv, group)
	groupPath := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", moveTestSubscription, group)
	vnetID := groupPath + "/providers/Microsoft.Network/virtualNetworks/rg-delete-vnet"
	aseID := groupPath + "/providers/Microsoft.Web/hostingEnvironments/rg-delete-ase"
	planID := groupPath + "/providers/Microsoft.Web/serverfarms/rg-delete-plan"

	for _, put := range []struct{ path, body string }{
		{vnetID + "?api-version=2024-05-01", `{"location":"eastus","properties":{"addressSpace":{"addressPrefixes":["10.61.0.0/16"]},"subnets":[{"name":"ase","properties":{"addressPrefix":"10.61.1.0/24"}}]}}`},
		{aseID + "?api-version=2024-04-01", `{"location":"eastus","kind":"ASEV3","properties":{"virtualNetwork":{"id":"` + vnetID + `/subnets/ase"}}}`},
		{planID + "?api-version=2024-04-01", `{"location":"eastus","sku":{"name":"I1v2","tier":"IsolatedV2"},"properties":{"hostingEnvironmentProfile":{"id":"` + aseID + `"}}}`},
	} {
		if status, body := moveARM(t, srv, http.MethodPut, put.path, put.body); status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("PUT %s: %d %s", put.path, status, body)
		}
	}
	if status, body := moveARM(t, srv, http.MethodDelete, aseID+"?api-version=2024-04-01", ""); status != http.StatusConflict {
		t.Fatalf("the environment must refuse its own delete while the plan is deployed in it: %d %s", status, body)
	}

	location := deleteResourceGroupAndWait(t, srv, group)
	if status, body := moveARM(t, srv, http.MethodGet, location, ""); status != http.StatusOK {
		t.Fatalf("the finished delete's Location answered %d %s", status, body)
	}

	for _, id := range []string{vnetID, aseID, planID} {
		if status, body := moveARM(t, srv, http.MethodGet, id+"?api-version=2024-04-01", ""); status != http.StatusNotFound {
			t.Errorf("%s survived its group's delete: %d %s", id, status, body)
		}
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
	if settled := azureAsyncOperationSettled(strings.TrimPrefix(location.Path, prefix)); settled != nil {
		<-settled
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
