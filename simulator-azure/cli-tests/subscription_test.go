package azure_cli_test

// Microsoft.Subscription (2021-10-01) through the Azure CLI. Routes exercised:
//
//	PUT /providers/Microsoft.Subscription/aliases/{aliasName}
//	GET /providers/Microsoft.Subscription/aliases/{aliasName}
//	DELETE /providers/Microsoft.Subscription/aliases/{aliasName}
//	POST /providers/Microsoft.Subscription/subscriptions/{subscriptionId}/acceptOwnership
//	GET /providers/Microsoft.Subscription/subscriptions/{subscriptionId}/acceptOwnershipStatus
//	GET /providers/Microsoft.Subscription/subscriptionOperations/{operationId}
//	PUT /providers/Microsoft.Subscription/policies/default
//	GET /providers/Microsoft.Subscription/policies/default
//	GET /providers/Microsoft.Subscription/policies
//	GET /providers/Microsoft.Subscription/operations
//	GET /providers/Microsoft.Billing/billingAccounts/{billingAccountId}/providers/Microsoft.Subscription/policies/default

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const subscriptionAPIVersion = "2021-10-01"

func microsoftSubscriptionURL(path string) string {
	return baseURL + path + "?api-version=" + subscriptionAPIVersion
}

// tenantPolicyBody is the bare PutTenantPolicyRequestProperties body the tenant
// policy PUT takes — no resource envelope.
func tenantPolicyBody(blockLeaving, blockInto bool, exempted []string) string {
	principals, err := json.Marshal(exempted)
	if err != nil {
		panic(err)
	}
	if exempted == nil {
		principals = []byte("[]")
	}
	return fmt.Sprintf(`{"blockSubscriptionsLeavingTenant":%t,"blockSubscriptionsIntoTenant":%t,"exemptedPrincipals":%s}`,
		blockLeaving, blockInto, principals)
}

func resetCLITenantPolicy(t *testing.T) {
	t.Helper()
	runCLI(t, azRest("PUT", microsoftSubscriptionURL("/providers/Microsoft.Subscription/policies/default"),
		tenantPolicyBody(false, false, nil)))
}

// TestCLISubscriptionTenantPolicy round-trips the tenant policy singleton and
// reads it back through both the get and the list operation.
func TestCLISubscriptionTenantPolicy(t *testing.T) {
	t.Cleanup(func() { resetCLITenantPolicy(t) })

	type policyResponse struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Type       string `json:"type"`
		Properties struct {
			PolicyID                        string   `json:"policyId"`
			BlockSubscriptionsLeavingTenant bool     `json:"blockSubscriptionsLeavingTenant"`
			BlockSubscriptionsIntoTenant    bool     `json:"blockSubscriptionsIntoTenant"`
			ExemptedPrincipals              []string `json:"exemptedPrincipals"`
		} `json:"properties"`
	}

	out := runCLI(t, azRest("GET", microsoftSubscriptionURL("/providers/Microsoft.Subscription/policies/default"), ""))
	var initial policyResponse
	parseJSON(t, out, &initial)
	if initial.Name != "default" {
		t.Fatalf("tenant policy name = %q, want %q", initial.Name, "default")
	}
	if initial.Properties.PolicyID == "" {
		t.Fatal("a tenant policy must carry a policyId")
	}

	const exempted = "e879cf0f-2b4d-4431-909a-f72fc9868693"
	out = runCLI(t, azRest("PUT", microsoftSubscriptionURL("/providers/Microsoft.Subscription/policies/default"),
		tenantPolicyBody(true, true, []string{exempted})))
	var written policyResponse
	parseJSON(t, out, &written)
	if !written.Properties.BlockSubscriptionsLeavingTenant || !written.Properties.BlockSubscriptionsIntoTenant {
		t.Fatalf("the written policy was not read back: %#v", written.Properties)
	}
	if len(written.Properties.ExemptedPrincipals) != 1 || written.Properties.ExemptedPrincipals[0] != exempted {
		t.Fatalf("exemptedPrincipals = %#v", written.Properties.ExemptedPrincipals)
	}
	if written.Properties.PolicyID != initial.Properties.PolicyID {
		t.Errorf("a tenant keeps one policy identifier: %q then %q",
			initial.Properties.PolicyID, written.Properties.PolicyID)
	}

	out = runCLI(t, azRest("GET", microsoftSubscriptionURL("/providers/Microsoft.Subscription/policies"), ""))
	var list struct {
		Value []policyResponse `json:"value"`
	}
	parseJSON(t, out, &list)
	if len(list.Value) != 1 {
		t.Fatalf("a tenant holds exactly one subscription policy, got %d", len(list.Value))
	}
	if list.Value[0].Properties.PolicyID != initial.Properties.PolicyID {
		t.Errorf("the listed policy is not the tenant's policy")
	}
}

// TestCLISubscriptionOwnershipAcceptance drives the ownership handover: a
// directed alias leaves the subscription Pending, the acceptance is accepted,
// and the ownership status settles Completed with the name the new owner gave.
func TestCLISubscriptionOwnershipAcceptance(t *testing.T) {
	const aliasName = "cli-test-ownership-alias"
	const aliasPath = "/providers/Microsoft.Subscription/aliases/" + aliasName
	body := fmt.Sprintf(`{"properties":{"displayName":"CLI Directed Subscription","workload":"Production",`+
		`"billingScope":"/providers/Microsoft.Billing/billingAccounts/cli-billing-account/enrollmentAccounts/cli-enrollment",`+
		`"additionalProperties":{"subscriptionTenantId":%q,"subscriptionOwnerId":"f09b39eb-c496-482c-9ab9-afd799572f4c"}}}`,
		simTenantID)
	out := runCLI(t, azRest("PUT", microsoftSubscriptionURL(aliasPath), body))
	var created struct {
		Properties struct {
			SubscriptionID       string `json:"subscriptionId"`
			AcceptOwnershipState string `json:"acceptOwnershipState"`
			AcceptOwnershipURL   string `json:"acceptOwnershipUrl"`
		} `json:"properties"`
	}
	parseJSON(t, out, &created)
	subID := created.Properties.SubscriptionID
	if subID == "" {
		t.Fatalf("alias creation reported no subscriptionId: %s", out)
	}
	t.Cleanup(func() { runCLI(t, azRest("DELETE", microsoftSubscriptionURL(aliasPath), "")) })
	if created.Properties.AcceptOwnershipState != "Pending" {
		t.Fatalf("acceptOwnershipState = %q, want Pending", created.Properties.AcceptOwnershipState)
	}
	wantAcceptURL := "/providers/Microsoft.Subscription/subscriptions/" + subID + "/acceptOwnership"
	if created.Properties.AcceptOwnershipURL != wantAcceptURL {
		t.Fatalf("acceptOwnershipUrl = %q, want %q", created.Properties.AcceptOwnershipURL, wantAcceptURL)
	}

	statusPath := "/providers/Microsoft.Subscription/subscriptions/" + subID + "/acceptOwnershipStatus"
	status := readCLIOwnershipStatus(t, statusPath)
	if status.AcceptOwnershipState != "Pending" || status.ProvisioningState != "Pending" {
		t.Fatalf("ownership status before acceptance = %#v", status)
	}
	if status.SubscriptionTenantID != simTenantID {
		t.Fatalf("ownership status names tenant %q, want %q", status.SubscriptionTenantID, simTenantID)
	}

	runCLI(t, azRest("POST", microsoftSubscriptionURL(wantAcceptURL),
		`{"properties":{"displayName":"CLI Accepted Subscription","tags":{"owner":"accepted"}}}`))

	deadline := time.Now().Add(30 * time.Second)
	for {
		status = readCLIOwnershipStatus(t, statusPath)
		if status.AcceptOwnershipState == "Completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ownership acceptance never completed: %#v", status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status.ProvisioningState != "Succeeded" {
		t.Errorf("provisioningState after acceptance = %q, want Succeeded", status.ProvisioningState)
	}
	if status.DisplayName != "CLI Accepted Subscription" {
		t.Errorf("displayName = %q, want the name the new owner gave it", status.DisplayName)
	}

	out = runCLI(t, azRest("GET", microsoftSubscriptionURL(aliasPath), ""))
	var settled struct {
		Properties struct {
			AcceptOwnershipState string `json:"acceptOwnershipState"`
		} `json:"properties"`
	}
	parseJSON(t, out, &settled)
	if settled.Properties.AcceptOwnershipState != "Completed" {
		t.Fatalf("the alias must report the completed handover, got %q", settled.Properties.AcceptOwnershipState)
	}
}

type cliOwnershipStatus struct {
	SubscriptionID       string `json:"subscriptionId"`
	AcceptOwnershipState string `json:"acceptOwnershipState"`
	ProvisioningState    string `json:"provisioningState"`
	SubscriptionTenantID string `json:"subscriptionTenantId"`
	DisplayName          string `json:"displayName"`
}

func readCLIOwnershipStatus(t *testing.T, statusPath string) cliOwnershipStatus {
	t.Helper()
	out := runCLI(t, azRest("GET", microsoftSubscriptionURL(statusPath), ""))
	var status cliOwnershipStatus
	parseJSON(t, out, &status)
	return status
}

// TestCLISubscriptionOperationUnknown asserts the Microsoft.Subscription
// operation registry answers for its own operations only.
func TestCLISubscriptionOperationUnknown(t *testing.T) {
	cmd := azRest("GET",
		microsoftSubscriptionURL("/providers/Microsoft.Subscription/subscriptionOperations/00000000-0000-0000-0000-0000000d0e5f"), "")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected an unknown operation to fail, got output: %s", out)
	}
	if !strings.Contains(string(out), "cannot be found") {
		t.Fatalf("expected a not-found error, got: %s", out)
	}
}

// TestCLISubscriptionBillingAccountPolicy reads a billing account's policy and
// asserts it reports the tenants the account actually serves.
func TestCLISubscriptionBillingAccountPolicy(t *testing.T) {
	const billingAccountID = "cli-billing-account-policy"
	const aliasName = "cli-test-billing-account-alias"
	const aliasPath = "/providers/Microsoft.Subscription/aliases/" + aliasName
	policyPath := "/providers/Microsoft.Billing/billingAccounts/" + billingAccountID +
		"/providers/Microsoft.Subscription/policies/default"

	type billingPolicy struct {
		Name       string `json:"name"`
		Properties struct {
			AllowTransfers bool `json:"allowTransfers"`
			ServiceTenants []struct {
				TenantID   string `json:"tenantId"`
				TenantName string `json:"tenantName"`
			} `json:"serviceTenants"`
		} `json:"properties"`
	}

	out := runCLI(t, azRest("GET", microsoftSubscriptionURL(policyPath), ""))
	var empty billingPolicy
	parseJSON(t, out, &empty)
	if empty.Name != billingAccountID {
		t.Fatalf("billing account policy name = %q, want the billing account id", empty.Name)
	}
	if !empty.Properties.AllowTransfers {
		t.Fatalf("allowTransfers = %t, want true", empty.Properties.AllowTransfers)
	}
	if len(empty.Properties.ServiceTenants) != 0 {
		t.Fatalf("a billing account that has funded no subscription serves no tenant, got %#v",
			empty.Properties.ServiceTenants)
	}

	body := fmt.Sprintf(`{"properties":{"displayName":"CLI Billing Account Subscription","workload":"Production",`+
		`"billingScope":"/providers/Microsoft.Billing/billingAccounts/%s/enrollmentAccounts/cli-enrollment"}}`,
		billingAccountID)
	runCLI(t, azRest("PUT", microsoftSubscriptionURL(aliasPath), body))
	t.Cleanup(func() { runCLI(t, azRest("DELETE", microsoftSubscriptionURL(aliasPath), "")) })

	out = runCLI(t, azRest("GET", microsoftSubscriptionURL(policyPath), ""))
	var served billingPolicy
	parseJSON(t, out, &served)
	if len(served.Properties.ServiceTenants) != 1 {
		t.Fatalf("serviceTenants = %#v, want the one tenant the billing account now serves",
			served.Properties.ServiceTenants)
	}
	if served.Properties.ServiceTenants[0].TenantID != simTenantID {
		t.Errorf("serviceTenants tenantId = %q, want %q", served.Properties.ServiceTenants[0].TenantID, simTenantID)
	}
	if served.Properties.ServiceTenants[0].TenantName == "" {
		t.Error("the simulator's own tenant has a name to report")
	}
}

// TestCLISubscriptionOperationsList reads the resource provider's operation
// catalog — the actions a role assignment can name.
func TestCLISubscriptionOperationsList(t *testing.T) {
	out := runCLI(t, azRest("GET", microsoftSubscriptionURL("/providers/Microsoft.Subscription/operations"), ""))
	var list struct {
		Value []struct {
			Name         string `json:"name"`
			IsDataAction bool   `json:"isDataAction"`
			Display      struct {
				Provider  string `json:"provider"`
				Resource  string `json:"resource"`
				Operation string `json:"operation"`
			} `json:"display"`
		} `json:"value"`
	}
	parseJSON(t, out, &list)
	if len(list.Value) == 0 {
		t.Fatal("the operation catalog is empty")
	}
	seen := map[string]bool{}
	for _, op := range list.Value {
		seen[op.Name] = true
		if op.IsDataAction {
			t.Errorf("%s: isDataAction = true — Microsoft.Subscription publishes no data plane", op.Name)
		}
		if op.Display.Provider != "Microsoft Subscription" {
			t.Errorf("%s: display.provider = %q", op.Name, op.Display.Provider)
		}
		if op.Display.Resource == "" || op.Display.Operation == "" {
			t.Errorf("%s: display = %#v", op.Name, op.Display)
		}
	}
	for _, action := range []string{
		"Microsoft.Subscription/aliases/read",
		"Microsoft.Subscription/aliases/write",
		"Microsoft.Subscription/aliases/delete",
		"Microsoft.Subscription/cancel/action",
		"Microsoft.Subscription/rename/action",
		"Microsoft.Subscription/enable/action",
		"Microsoft.Subscription/acceptOwnership/action",
		"Microsoft.Subscription/acceptOwnershipStatus/read",
		"Microsoft.Subscription/subscriptionOperations/read",
		"Microsoft.Subscription/policies/read",
		"Microsoft.Subscription/policies/write",
		"Microsoft.Subscription/operations/read",
	} {
		if !seen[action] {
			t.Errorf("operation catalog omits %q", action)
		}
	}
}
