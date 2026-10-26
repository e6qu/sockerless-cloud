package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Every operation the specification declares, mapped to the action that
// authorizes it; reads of one resource share an action.
var subscriptionSpecActions = map[string]string{
	"Alias_Get":                                   "Microsoft.Subscription/aliases/read",
	"Alias_List":                                  "Microsoft.Subscription/aliases/read",
	"Alias_Create":                                "Microsoft.Subscription/aliases/write",
	"Alias_Delete":                                "Microsoft.Subscription/aliases/delete",
	"Subscription_Cancel":                         "Microsoft.Subscription/cancel/action",
	"Subscription_Rename":                         "Microsoft.Subscription/rename/action",
	"Subscription_Enable":                         "Microsoft.Subscription/enable/action",
	"Subscription_AcceptOwnership":                "Microsoft.Subscription/acceptOwnership/action",
	"Subscription_AcceptOwnershipStatus":          "Microsoft.Subscription/acceptOwnershipStatus/read",
	"SubscriptionOperation_Get":                   "Microsoft.Subscription/subscriptionOperations/read",
	"SubscriptionPolicy_GetPolicyForTenant":       "Microsoft.Subscription/policies/read",
	"SubscriptionPolicy_ListPolicyForTenant":      "Microsoft.Subscription/policies/read",
	"SubscriptionPolicy_AddUpdatePolicyForTenant": "Microsoft.Subscription/policies/write",
	"BillingAccount_GetPolicy":                    "Microsoft.Subscription/policies/read",
	"Operations_List":                             "Microsoft.Subscription/operations/read",
}

// The catalog carries an action for every documented operation and none no
// operation needs. An operation added to the vendored specification without a
// mapping fails here.
func TestSubscriptionOperationCatalogCoversSpec(t *testing.T) {
	specPath := filepath.Join("..", "specs", "cloud-api", "azure",
		"subscription-arm-subscriptions-2021-10-01.swagger.json.gz")
	f, err := os.Open(specPath)
	if err != nil {
		t.Fatalf("open vendored Microsoft.Subscription swagger: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip vendored Microsoft.Subscription swagger: %v", err)
	}
	defer gz.Close()
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.NewDecoder(gz).Decode(&doc); err != nil {
		t.Fatalf("decode vendored Microsoft.Subscription swagger: %v", err)
	}

	catalog := map[string]bool{}
	for _, op := range subscriptionOperationCatalog {
		if catalog[op.Name] {
			t.Errorf("catalog lists action %q twice", op.Name)
		}
		catalog[op.Name] = true
	}

	needed := map[string]bool{}
	var operationIDs []string
	for _, methods := range doc.Paths {
		for _, op := range methods {
			if op.OperationID == "" {
				continue
			}
			operationIDs = append(operationIDs, op.OperationID)
			action, ok := subscriptionSpecActions[op.OperationID]
			if !ok {
				t.Errorf("operation %q has no Azure Resource Manager action mapped; add it here and to subscriptionOperationCatalog", op.OperationID)
				continue
			}
			needed[action] = true
			if !catalog[action] {
				t.Errorf("operation %q needs action %q, which subscriptionOperationCatalog does not list", op.OperationID, action)
			}
		}
	}
	if len(operationIDs) == 0 {
		t.Fatal("vendored Microsoft.Subscription swagger declared no operations")
	}

	var stale []string
	for action := range catalog {
		if !needed[action] {
			stale = append(stale, action)
		}
	}
	sort.Strings(stale)
	for _, action := range stale {
		t.Errorf("subscriptionOperationCatalog lists action %q, which no documented operation requires", action)
	}
}
