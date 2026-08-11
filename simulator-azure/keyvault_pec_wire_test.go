package main

import (
	"testing"
)

// TestKeyVaultPECWireDropsForeignMembers pins the projection every
// Microsoft.KeyVault private-endpoint-connection response goes through. The
// connection object is shared with the Microsoft.Network private-endpoint
// surface, which stores members of its own; Key Vault's contract declares
// only three, and answering with the others is a wire divergence its clients
// cannot parse against the published schema.
func TestKeyVaultPECWireDropsForeignMembers(t *testing.T) {
	stored := KeyVaultPrivateEndpointConnection{
		ID:   "/subscriptions/s/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/v/privateEndpointConnections/pec",
		Name: "pec",
		Type: "Microsoft.KeyVault/vaults/privateEndpointConnections",
		Properties: map[string]any{
			"privateEndpoint":                   map[string]any{"id": "/subscriptions/s/pe"},
			"privateLinkServiceConnectionState": map[string]any{"status": "Approved"},
			"provisioningState":                 "Succeeded",
			// Members the Microsoft.Network surface keeps on the same object.
			"groupIds":                []string{"vault"},
			"linkIdentifier":          "12345",
			"privateEndpointLocation": "eastus",
			"privateIPAddress":        "10.0.0.4",
			"requestMessage":          "please approve",
		},
	}

	got := keyVaultPECWire(stored)

	for _, declared := range []string{"privateEndpoint", "privateLinkServiceConnectionState", "provisioningState"} {
		if _, ok := got.Properties[declared]; !ok {
			t.Errorf("Microsoft.KeyVault declares %q; it must survive the projection", declared)
		}
	}
	for _, foreign := range []string{"groupIds", "linkIdentifier", "privateEndpointLocation", "privateIPAddress", "requestMessage"} {
		if _, ok := got.Properties[foreign]; ok {
			t.Errorf("%q belongs to the Microsoft.Network surface and must not reach a Key Vault client", foreign)
		}
	}
	// The shared record itself is untouched, so the network surface still sees
	// everything it stored.
	if len(stored.Properties) != 8 {
		t.Errorf("the projection must not mutate the shared record; it now has %d members", len(stored.Properties))
	}
}
