package main

import (
	"strings"
	"testing"
)

// TestAzureTrackedResourceKeys pins the registry's key shape. Every key is the
// lowercased "provider/type" spelling the move-hook table uses, so a
// `resourceType eq` filter and a move hook name the same resource the same way.
func TestAzureTrackedResourceKeys(t *testing.T) {
	if len(azureTrackedResources) == 0 {
		t.Fatal("the resource registry is empty")
	}
	for key, enumerate := range azureTrackedResources {
		if key != strings.ToLower(key) {
			t.Errorf("registry key %q is not lowercased", key)
		}
		if !strings.HasPrefix(key, "microsoft.") {
			t.Errorf("registry key %q does not name a Microsoft resource provider", key)
		}
		if strings.Count(key, "/") < 1 {
			t.Errorf("registry key %q is not a provider/type spelling", key)
		}
		if enumerate == nil {
			t.Errorf("registry key %q has no enumerator", key)
		}
	}
	// The families the resource lists must cover, one per provider the
	// simulator tracks resources for.
	for _, key := range []string{
		"microsoft.web/sites",
		"microsoft.storage/storageaccounts",
		"microsoft.keyvault/vaults",
		"microsoft.keyvault/managedhsms",
		"microsoft.network/virtualnetworks",
		"microsoft.compute/virtualmachines",
		"microsoft.app/containerapps",
		"microsoft.containerinstance/containergroups",
		"microsoft.containerregistry/registries",
		"microsoft.servicebus/namespaces",
		"microsoft.eventgrid/topics",
		"microsoft.documentdb/databaseaccounts",
		"microsoft.operationalinsights/workspaces",
		"microsoft.managedidentity/userassignedidentities",
		"microsoft.logic/workflows",
	} {
		if _, ok := azureTrackedResources[key]; !ok {
			t.Errorf("registry is missing %q", key)
		}
	}
}

// TestParseAzureResourceFilter covers the $filter grammar Resources_List
// documents and real clients send, and the forms it must refuse rather than
// silently match everything with.
func TestParseAzureResourceFilter(t *testing.T) {
	row := azureResourceRow{
		ID: "/subscriptions/S/resourceGroups/rg-one/providers/" +
			"Microsoft.KeyVault/vaults/kv-one",
		Name:     "kv-one",
		Type:     "Microsoft.KeyVault/vaults",
		Location: "eastus",
		Tags:     map[string]string{"env": "prod", "owner": "team"},
	}
	other := azureResourceRow{
		ID: "/subscriptions/S/resourceGroups/rg-two/providers/" +
			"Microsoft.Network/virtualNetworks/vnet-two",
		Name:     "vnet-two",
		Type:     "Microsoft.Network/virtualNetworks",
		Location: "westus",
	}

	accepted := []struct {
		filter     string
		matchRow   bool
		matchOther bool
		tagScoped  bool
	}{
		{filter: "resourceGroup eq 'rg-one'", matchRow: true},
		{filter: "resourceGroup eq 'RG-ONE'", matchRow: true},
		{filter: "name eq 'kv-one'", matchRow: true},
		{filter: "location eq 'eastus'", matchRow: true},
		{filter: "location eq 'westus'", matchOther: true},
		{filter: "resourceType eq 'Microsoft.KeyVault/vaults'", matchRow: true},
		{filter: "resourceType ne 'Microsoft.KeyVault/vaults'", matchOther: true},
		{filter: "substringof('kv', name)", matchRow: true},
		{filter: "substringof('one', resourceGroup)", matchRow: true},
		{filter: "tagname eq 'env'", matchRow: true, tagScoped: true},
		{filter: "tagname eq 'env' and tagvalue eq 'prod'", matchRow: true, tagScoped: true},
		{filter: "tagname eq 'env' and tagvalue eq 'staging'", tagScoped: true},
		{filter: "startswith(tagname, 'ow')", matchRow: true, tagScoped: true},
		{
			filter:   "resourceGroup eq 'rg-one' and resourceType eq 'Microsoft.KeyVault/vaults'",
			matchRow: true,
		},
		{
			filter:     "name eq 'kv-one' or name eq 'vnet-two'",
			matchRow:   true,
			matchOther: true,
		},
	}
	for _, tc := range accepted {
		filter, err := parseAzureResourceFilter(tc.filter)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.filter, err)
			continue
		}
		if got := filter.matches(row); got != tc.matchRow {
			t.Errorf("%q: matches(vault) = %v, want %v", tc.filter, got, tc.matchRow)
		}
		if got := filter.matches(other); got != tc.matchOther {
			t.Errorf("%q: matches(vnet) = %v, want %v", tc.filter, got, tc.matchOther)
		}
		if filter.tagScoped != tc.tagScoped {
			t.Errorf("%q: tagScoped = %v, want %v", tc.filter, filter.tagScoped, tc.tagScoped)
		}
	}

	// An empty filter matches everything, which is how an unfiltered list
	// reaches the same code path.
	empty, err := parseAzureResourceFilter("")
	if err != nil || empty != nil {
		t.Fatalf("empty filter: got (%v, %v), want (nil, nil)", empty, err)
	}
	if !empty.matches(row) {
		t.Error("an absent filter must match every row")
	}

	for _, bad := range []string{
		"managedBy eq 'someone'",
		"identity/principalId eq 'x'",
		"plan/publisher eq 'x'",
		"kind = 'functionapp'",
		"substringof('x', location)",
		"startswith(name, 'kv')",
		"(name eq 'kv-one')",
		"name eq kv-one",
		"name",
	} {
		if _, err := parseAzureResourceFilter(bad); err == nil {
			t.Errorf("%q: expected the filter to be refused", bad)
		}
	}
}

// TestAzureIDSegmentAfter pins the scope reading the list's scoping and its
// resourceGroup filter both depend on.
func TestAzureIDSegmentAfter(t *testing.T) {
	const id = "/subscriptions/Sub-1/resourceGroups/Group-1/providers/" +
		"Microsoft.Storage/storageAccounts/acct"
	if got := azureSubscriptionOfID(id); got != "Sub-1" {
		t.Errorf("subscription = %q, want %q", got, "Sub-1")
	}
	if got := azureResourceGroupOfID(id); got != "Group-1" {
		t.Errorf("resource group = %q, want %q", got, "Group-1")
	}
	// A subscription-scoped id carries no resource group.
	if got := azureResourceGroupOfID("/subscriptions/Sub-1"); got != "" {
		t.Errorf("resource group of a subscription scope = %q, want empty", got)
	}
}
