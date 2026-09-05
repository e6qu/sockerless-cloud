package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.Subscription ownership acceptance, driven through the simulator's
// real handler chain — bearer verification, api-version enforcement, mux.
//
// The Azure Resource Manager long-running-operation contract is the reason
// these tests talk HTTP rather than going through the SDK: an SDK poller
// reports only the settled result, while the contract this surface has to keep
// is that Subscription_AcceptOwnership answers 202 with a Location naming a
// Microsoft.Subscription operation, and that the operation answers 202 while
// the acceptance runs and 200 with the accepted subscription's link once it has
// settled. The intermediate answer is the part a client's poller depends on and
// the part only a direct request can observe.

const subscriptionAPIVersion = "2021-10-01"

type subscriptionTestClient struct {
	t     *testing.T
	srv   *sim.Server
	token string
}

func newSubscriptionTestClient(t *testing.T) *subscriptionTestClient {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint Azure Resource Manager token: %v", err)
	}
	return &subscriptionTestClient{t: t, srv: srv, token: token}
}

// do issues one authenticated Azure Resource Manager request. A path already
// carrying a query string (a Location header the simulator emitted) is sent as
// given, so the api-version the simulator advertised is the one used.
func (c *subscriptionTestClient) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	url := path
	if !strings.Contains(url, "?") {
		url += "?api-version=" + subscriptionAPIVersion
	}
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, reader)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c.srv.ServeHTTP(rec, req)
	return rec
}

func (c *subscriptionTestClient) doJSON(method, path string, body any, wantStatus int) map[string]any {
	c.t.Helper()
	rec := c.do(method, path, body)
	if rec.Code != wantStatus {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		c.t.Fatalf("%s %s: decode response: %v: %s", method, path, err, rec.Body.String())
	}
	return out
}

func subscriptionProps(t *testing.T, resource map[string]any) map[string]any {
	t.Helper()
	props, ok := resource["properties"].(map[string]any)
	if !ok {
		t.Fatalf("response carried no properties object: %#v", resource)
	}
	return props
}

// createDirectedAlias creates a subscription directed at a tenant and an owner
// and waits for the alias to finish provisioning. It returns the new
// subscription id and the settled alias resource.
func (c *subscriptionTestClient) createDirectedAlias(aliasName, displayName, tenantID string) (string, map[string]any) {
	c.t.Helper()
	created := c.doJSON(http.MethodPut, "/providers/Microsoft.Subscription/aliases/"+aliasName, map[string]any{
		"properties": map[string]any{
			"displayName":  displayName,
			"workload":     "Production",
			"billingScope": "/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment",
			"additionalProperties": map[string]any{
				"subscriptionTenantId": tenantID,
				"subscriptionOwnerId":  entraDefaultUser.OID,
				"tags":                 map[string]string{"purpose": "ownership"},
			},
		},
	}, http.StatusCreated)
	props := subscriptionProps(c.t, created)
	subscriptionID, _ := props["subscriptionId"].(string)
	if subscriptionID == "" {
		c.t.Fatalf("alias creation reported no subscriptionId: %#v", props)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		alias := c.doJSON(http.MethodGet, "/providers/Microsoft.Subscription/aliases/"+aliasName, nil, http.StatusOK)
		props = subscriptionProps(c.t, alias)
		if props["provisioningState"] == "Succeeded" {
			return subscriptionID, alias
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("alias %q never reached Succeeded: %#v", aliasName, props)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSubscriptionAcceptOwnershipLongRunningOperation walks the whole ownership
// handover: a directed alias leaves the subscription's ownership Pending with
// the URL an owner accepts it at, the acceptance answers 202 pointing at a
// Microsoft.Subscription operation, that operation reports 202 while the
// acceptance runs and 200 with the subscription's link afterwards, and both the
// ownership status and the alias report the Completed state.
func TestSubscriptionAcceptOwnershipLongRunningOperation(t *testing.T) {
	c := newSubscriptionTestClient(t)
	const aliasName = "ownership-alias"
	subscriptionID, alias := c.createDirectedAlias(aliasName, "Directed Subscription", simTenantID)

	props := subscriptionProps(t, alias)
	if props["acceptOwnershipState"] != "Pending" {
		t.Fatalf("a directed subscription stays Pending until its owner accepts it, got %v", props["acceptOwnershipState"])
	}
	wantURL := "/providers/Microsoft.Subscription/subscriptions/" + subscriptionID + "/acceptOwnership"
	if props["acceptOwnershipUrl"] != wantURL {
		t.Fatalf("acceptOwnershipUrl = %v, want %q", props["acceptOwnershipUrl"], wantURL)
	}

	statusPath := "/providers/Microsoft.Subscription/subscriptions/" + subscriptionID + "/acceptOwnershipStatus"
	status := c.doJSON(http.MethodGet, statusPath, nil, http.StatusOK)
	if status["acceptOwnershipState"] != "Pending" || status["provisioningState"] != "Pending" {
		t.Fatalf("ownership status before acceptance = %#v", status)
	}
	if status["subscriptionTenantId"] != simTenantID {
		t.Fatalf("ownership status names tenant %v, want %q", status["subscriptionTenantId"], simTenantID)
	}
	if status["billingOwner"] != entraDefaultUser.PreferredUsername {
		t.Fatalf("billingOwner = %v, want the user principal name of the principal that raised the request (%q)",
			status["billingOwner"], entraDefaultUser.PreferredUsername)
	}

	rec := c.do(http.MethodPost, wantURL, map[string]any{
		"properties": map[string]any{
			"displayName": "Accepted Subscription",
			"tags":        map[string]string{"owner": "accepted"},
		},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("accept ownership: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if location == "" {
		t.Fatal("accept ownership answered 202 without a Location naming the operation to poll")
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("accept ownership answered 202 without a Retry-After")
	}
	operationPath := subscriptionLocationPath(t, location)

	// While the acceptance runs the operation answers 202 and points at
	// itself; the poll target of an Azure Resource Manager Location poll is
	// stable across polls.
	running := c.do(http.MethodGet, operationPath, nil)
	if running.Code != http.StatusAccepted {
		t.Fatalf("in-progress operation: status %d, want 202: %s", running.Code, running.Body.String())
	}
	if subscriptionLocationPath(t, running.Header().Get("Location")) != operationPath {
		t.Errorf("in-progress operation pointed at %q, want itself (%q)", running.Header().Get("Location"), operationPath)
	}

	var completed map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		poll := c.do(http.MethodGet, operationPath, nil)
		if poll.Code == http.StatusOK {
			if err := json.Unmarshal(poll.Body.Bytes(), &completed); err != nil {
				t.Fatalf("decode completed operation: %v: %s", err, poll.Body.String())
			}
			break
		}
		if poll.Code != http.StatusAccepted {
			t.Fatalf("operation poll: status %d: %s", poll.Code, poll.Body.String())
		}
		if time.Now().After(deadline) {
			t.Fatal("the ownership acceptance operation never completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if completed["subscriptionLink"] != "/subscriptions/"+subscriptionID {
		t.Fatalf("completed operation subscriptionLink = %v, want %q", completed["subscriptionLink"], "/subscriptions/"+subscriptionID)
	}

	status = c.doJSON(http.MethodGet, statusPath, nil, http.StatusOK)
	if status["acceptOwnershipState"] != "Completed" || status["provisioningState"] != "Succeeded" {
		t.Fatalf("ownership status after acceptance = %#v", status)
	}
	if status["displayName"] != "Accepted Subscription" {
		t.Errorf("the accepted subscription takes the name its new owner gave it, got %v", status["displayName"])
	}

	alias = c.doJSON(http.MethodGet, "/providers/Microsoft.Subscription/aliases/"+aliasName, nil, http.StatusOK)
	props = subscriptionProps(t, alias)
	if props["acceptOwnershipState"] != "Completed" {
		t.Fatalf("the alias must report the completed handover, got %v", props["acceptOwnershipState"])
	}
	if _, ok := props["acceptOwnershipUrl"]; ok {
		t.Error("an accepted subscription no longer advertises an acceptOwnershipUrl")
	}

	subscription := c.doJSON(http.MethodGet, "/subscriptions/"+subscriptionID, nil, http.StatusOK)
	if subscription["displayName"] != "Accepted Subscription" {
		t.Errorf("subscription displayName = %v, want the name the new owner gave it", subscription["displayName"])
	}
}

// TestSubscriptionAcceptOwnershipRejectsNonPending covers the states in which
// there is nothing to accept: a subscription that never had an ownership
// request, and one whose request has already been accepted.
func TestSubscriptionAcceptOwnershipRejectsNonPending(t *testing.T) {
	c := newSubscriptionTestClient(t)

	missing := c.do(http.MethodPost,
		"/providers/Microsoft.Subscription/subscriptions/"+azureDefaultSubscriptionID+"/acceptOwnership",
		map[string]any{"properties": map[string]any{"displayName": "No Request"}})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("accepting a subscription with no ownership request: status %d, want 404: %s", missing.Code, missing.Body.String())
	}

	subscriptionID, _ := c.createDirectedAlias("ownership-conflict-alias", "Directed Subscription", simTenantID)
	acceptPath := "/providers/Microsoft.Subscription/subscriptions/" + subscriptionID + "/acceptOwnership"
	accept := map[string]any{"properties": map[string]any{"displayName": "Accepted Subscription"}}
	if rec := c.do(http.MethodPost, acceptPath, accept); rec.Code != http.StatusAccepted {
		t.Fatalf("accept ownership: status %d, want 202: %s", rec.Code, rec.Body.String())
	}

	statusPath := "/providers/Microsoft.Subscription/subscriptions/" + subscriptionID + "/acceptOwnershipStatus"
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := c.doJSON(http.MethodGet, statusPath, nil, http.StatusOK)
		if status["acceptOwnershipState"] == "Completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ownership acceptance never completed: %#v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	again := c.do(http.MethodPost, acceptPath, accept)
	if again.Code != http.StatusConflict {
		t.Fatalf("accepting an already-accepted subscription: status %d, want 409: %s", again.Code, again.Body.String())
	}
}

// TestSubscriptionAcceptOwnershipRequiresDisplayName asserts the one required
// member of the request body.
func TestSubscriptionAcceptOwnershipRequiresDisplayName(t *testing.T) {
	c := newSubscriptionTestClient(t)
	subscriptionID, _ := c.createDirectedAlias("ownership-displayname-alias", "Directed Subscription", simTenantID)
	rec := c.do(http.MethodPost,
		"/providers/Microsoft.Subscription/subscriptions/"+subscriptionID+"/acceptOwnership",
		map[string]any{"properties": map[string]any{}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("accept ownership without displayName: status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestSubscriptionOperationGetUnknown asserts that the operation registry
// answers for its own operations only — an identifier it never issued is not
// found, rather than reported as a settled operation with no link.
func TestSubscriptionOperationGetUnknown(t *testing.T) {
	c := newSubscriptionTestClient(t)
	rec := c.do(http.MethodGet, "/providers/Microsoft.Subscription/subscriptionOperations/"+generateUUID(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown operation: status %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// subscriptionLocationPath reduces an absolute Location header to the
// path-and-query the test re-issues, and fails if the header is not the
// absolute URL an Azure Resource Manager poller needs to follow it.
func subscriptionLocationPath(t *testing.T, location string) string {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Location %q: %v", location, err)
	}
	if !parsed.IsAbs() {
		t.Fatalf("Location %q is not an absolute URL", location)
	}
	return parsed.RequestURI()
}
