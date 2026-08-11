package azure_cli_test

// Microsoft.Subscription alias API (2021-10-01) through the Azure CLI.
// The alias and subscription-action commands (`az account alias create/
// show/list/delete`, `az account subscription cancel/rename/enable`) live in
// the preview `account` CLI extension, not azure-cli core, so — following the
// harness-wide pattern every ARM control-plane test here uses — the same wire
// requests those commands issue are driven through `az rest` with the
// extension's documented method, path, api-version, and body. Routes:
//
//	PUT /providers/Microsoft.Subscription/aliases/{aliasName}
//	GET /providers/Microsoft.Subscription/aliases/{aliasName}
//	DELETE /providers/Microsoft.Subscription/aliases/{aliasName}
//	GET /providers/Microsoft.Subscription/aliases
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/cancel
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/rename
//	POST /subscriptions/{subscriptionId}/providers/Microsoft.Subscription/enable

import (
	"fmt"
	"testing"
	"time"
)

type cliAliasResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Properties struct {
		SubscriptionID    string `json:"subscriptionId"`
		DisplayName       string `json:"displayName"`
		ProvisioningState string `json:"provisioningState"`
	} `json:"properties"`
}

type cliSubscription struct {
	SubscriptionID string `json:"subscriptionId"`
	DisplayName    string `json:"displayName"`
	State          string `json:"state"`
}

// TestCLISubscriptionAliasLifecycle drives subscription creation, use, and
// the rename/cancel/enable actions exactly as `az account alias` and
// `az account subscription` shape them on the wire.
func TestCLISubscriptionAliasLifecycle(t *testing.T) {
	const aliasName = "cli-test-sub-alias"
	aliasURL := fmt.Sprintf("%s/providers/Microsoft.Subscription/aliases/%s?api-version=2021-10-01", baseURL, aliasName)

	// az account alias create --name cli-test-sub-alias --display-name ...
	// --workload Production --billing-scope ...
	out := runCLI(t, azRest("PUT", aliasURL,
		`{"properties":{"displayName":"CLI Created Subscription","workload":"Production","billingScope":"/providers/Microsoft.Billing/billingAccounts/sim-billing-account/enrollmentAccounts/sim-enrollment"}}`))
	var created cliAliasResponse
	parseJSON(t, out, &created)
	if created.Properties.ProvisioningState != "Accepted" && created.Properties.ProvisioningState != "Succeeded" {
		t.Fatalf("alias create provisioningState = %q, want Accepted or Succeeded", created.Properties.ProvisioningState)
	}
	newSubID := created.Properties.SubscriptionID
	if newSubID == "" || newSubID == subscriptionID {
		t.Fatalf("alias create must mint a fresh subscription id, got %q", newSubID)
	}

	// az account alias show — also the LRO poll the extension performs.
	var shown cliAliasResponse
	deadline := time.Now().Add(30 * time.Second)
	for {
		out = runCLI(t, azRest("GET", aliasURL, ""))
		parseJSON(t, out, &shown)
		if shown.Properties.ProvisioningState == "Succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alias %q never reached Succeeded; last state %q", aliasName, shown.Properties.ProvisioningState)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if shown.Type != "Microsoft.Subscription/aliases" {
		t.Fatalf("alias type = %q, want Microsoft.Subscription/aliases", shown.Type)
	}

	// az account alias list
	out = runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.Subscription/aliases?api-version=2021-10-01", ""))
	var aliasList struct {
		Value []cliAliasResponse `json:"value"`
	}
	parseJSON(t, out, &aliasList)
	foundAlias := false
	for _, alias := range aliasList.Value {
		if alias.Name == aliasName {
			foundAlias = true
		}
	}
	if !foundAlias {
		t.Fatalf("alias list did not include %q: %s", aliasName, out)
	}

	// az account subscription list — the created subscription is Enabled and
	// enumerable.
	out = runCLI(t, azRest("GET", baseURL+"/subscriptions?api-version=2021-10-01", ""))
	var subList struct {
		Value []cliSubscription `json:"value"`
	}
	parseJSON(t, out, &subList)
	var listed *cliSubscription
	for i := range subList.Value {
		if subList.Value[i].SubscriptionID == newSubID {
			listed = &subList.Value[i]
		}
	}
	if listed == nil {
		t.Fatalf("created subscription %q missing from subscriptions list: %s", newSubID, out)
	}
	if listed.DisplayName != "CLI Created Subscription" || listed.State != "Enabled" {
		t.Fatalf("created subscription listed as %+v, want display 'CLI Created Subscription' state Enabled", *listed)
	}

	// The new subscription is a real resource scope: create a resource group
	// inside it.
	rgURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/cli-test-sub-alias-rg?api-version=2021-04-01", baseURL, newSubID)
	out = runCLI(t, azRest("PUT", rgURL, `{"location":"eastus"}`))
	var rg struct {
		ID string `json:"id"`
	}
	parseJSON(t, out, &rg)
	wantRGPrefix := "/subscriptions/" + newSubID + "/resourceGroups/cli-test-sub-alias-rg"
	if rg.ID != wantRGPrefix {
		t.Fatalf("resource group id = %q, want %q", rg.ID, wantRGPrefix)
	}

	// az account subscription rename --subscription-name ...
	actionURL := func(action string) string {
		return fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Subscription/%s?api-version=2021-10-01", baseURL, newSubID, action)
	}
	out = runCLI(t, azRest("POST", actionURL("rename"), `{"subscriptionName":"CLI Renamed Subscription"}`))
	var action struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	parseJSON(t, out, &action)
	if action.SubscriptionID != newSubID {
		t.Fatalf("rename returned subscriptionId %q, want %q", action.SubscriptionID, newSubID)
	}

	// az account subscription cancel
	out = runCLI(t, azRest("POST", actionURL("cancel"), ""))
	parseJSON(t, out, &action)
	if action.SubscriptionID != newSubID {
		t.Fatalf("cancel returned subscriptionId %q, want %q", action.SubscriptionID, newSubID)
	}
	out = runCLI(t, azRest("GET", fmt.Sprintf("%s/subscriptions/%s?api-version=2021-10-01", baseURL, newSubID), ""))
	var sub cliSubscription
	parseJSON(t, out, &sub)
	if sub.State != "Disabled" || sub.DisplayName != "CLI Renamed Subscription" {
		t.Fatalf("after rename+cancel subscription = %+v, want display 'CLI Renamed Subscription' state Disabled", sub)
	}

	// az account subscription enable
	out = runCLI(t, azRest("POST", actionURL("enable"), ""))
	parseJSON(t, out, &action)
	if action.SubscriptionID != newSubID {
		t.Fatalf("enable returned subscriptionId %q, want %q", action.SubscriptionID, newSubID)
	}
	out = runCLI(t, azRest("GET", fmt.Sprintf("%s/subscriptions/%s?api-version=2021-10-01", baseURL, newSubID), ""))
	parseJSON(t, out, &sub)
	if sub.State != "Enabled" {
		t.Fatalf("after enable subscription state = %q, want Enabled", sub.State)
	}

	// az account alias delete — removes the alias, never the subscription.
	runCLI(t, azRest("DELETE", aliasURL, ""))
	// `az rest` exits non-zero on 404 and runCLI requires success, so assert
	// the alias is gone via the list.
	out = runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.Subscription/aliases?api-version=2021-10-01", ""))
	parseJSON(t, out, &aliasList)
	for _, alias := range aliasList.Value {
		if alias.Name == aliasName {
			t.Fatalf("alias %q still listed after delete", aliasName)
		}
	}
	out = runCLI(t, azRest("GET", fmt.Sprintf("%s/subscriptions/%s?api-version=2021-10-01", baseURL, newSubID), ""))
	parseJSON(t, out, &sub)
	if sub.State != "Enabled" {
		t.Fatalf("subscription %q must survive alias deletion, state = %q", newSubID, sub.State)
	}
}
