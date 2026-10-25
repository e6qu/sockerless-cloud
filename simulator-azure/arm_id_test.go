package main

import "testing"

func TestARMResourceIDs(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{armSubscriptionID("sub"), "/subscriptions/sub"},
		{armResourceGroupID("sub", "rg"), "/subscriptions/sub/resourceGroups/rg"},
		{armResourceID("sub", "rg", "Microsoft.Web", "sites", "app"),
			"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Web/sites/app"},
		{armResourceID("sub", "rg", "Microsoft.Storage", "storageAccounts", "acct", "tableServices", "default", "tables", "t"),
			"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Storage/storageAccounts/acct/tableServices/default/tables/t"},
		{armResourceID("sub", "rg", "Microsoft.Network", "virtualNetworks") + "/",
			"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/"},
		{armSubscriptionResourceID("sub", "Microsoft.KeyVault", "locations", "westus", "deletedVaults", "kv"),
			"/subscriptions/sub/providers/Microsoft.KeyVault/locations/westus/deletedVaults/kv"},
		{armProviderID("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/t", "Microsoft.EventGrid", "eventSubscriptions", "es"),
			"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.EventGrid/topics/t/providers/Microsoft.EventGrid/eventSubscriptions/es"},
		{armChildID("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Cache/Redis/c", "firewallRules", "r"),
			"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Cache/Redis/c/firewallRules/r"},
		{armChildID("/subscriptions/sub"), "/subscriptions/sub"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
