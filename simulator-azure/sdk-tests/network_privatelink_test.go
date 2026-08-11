package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkPrivateLink_ServiceAndEndpointShareOneConnection is the Azure
// Private Link round trip across both halves of the feature. It proves the
// consumer's private endpoint and the provider's private link service see ONE
// connection object rather than two copies: the endpoint's create opens the
// connection on the service, the service's owner approves it there, and the
// endpoint's own read reports the owner's decision.
func TestNetworkPrivateLink_ServiceAndEndpointShareOneConnection(t *testing.T) {
	requireNetworkHost(t)
	rg := "plink-rg"
	requireResourceGroup(t, rg)
	providerSubnet := requireSubnet(t, rg, "plink-provider-vnet", "10.50.0.0/16", "nat-subnet", "10.50.1.0/24")
	consumerSubnet := requireSubnet(t, rg, "plink-consumer-vnet", "10.51.0.0/16", "pe-subnet", "10.51.1.0/24")

	plsClient, err := armnetwork.NewPrivateLinkServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	plsPoller, err := plsClient.BeginCreateOrUpdate(ctx, rg, "billing-pls", armnetwork.PrivateLinkService{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateLinkServiceProperties{
			IPConfigurations: []*armnetwork.PrivateLinkServiceIPConfiguration{{
				Name: to.Ptr("natcfg1"),
				Properties: &armnetwork.PrivateLinkServiceIPConfigurationProperties{
					Subnet:                    &armnetwork.Subnet{ID: to.Ptr(providerSubnet)},
					PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
				},
			}},
			EnableProxyProtocol: to.Ptr(true),
		},
	}, nil)
	require.NoError(t, err)
	pls, err := plsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, pls.Properties.Alias)
	assert.NotEmpty(t, *pls.Properties.Alias, "the service must be given an alias")
	assert.Equal(t, armnetwork.AccessMode("Default"), *pls.Properties.AccessMode)
	// The NAT configuration took a real address out of the provider subnet, and
	// the service reports the interface that holds it.
	require.Len(t, pls.Properties.IPConfigurations, 1)
	require.NotNil(t, pls.Properties.IPConfigurations[0].Properties.PrivateIPAddress)
	natAddress := *pls.Properties.IPConfigurations[0].Properties.PrivateIPAddress
	assert.NotEmpty(t, natAddress)
	require.Len(t, pls.Properties.NetworkInterfaces, 1)
	assert.Empty(t, pls.Properties.PrivateEndpointConnections, "no consumer has connected yet")

	// The NAT interface is an ordinary network interface: it answers Get and
	// carries the address the subnet allocated.
	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	natNICName := resourceName(*pls.Properties.NetworkInterfaces[0].ID)
	natNIC, err := nicClient.Get(ctx, rg, natNICName, nil)
	require.NoError(t, err)
	require.Len(t, natNIC.Properties.IPConfigurations, 1)
	assert.Equal(t, natAddress, *natNIC.Properties.IPConfigurations[0].Properties.PrivateIPAddress)

	peClient, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "billing-pe", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(consumerSubnet)},
			ManualPrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name: to.Ptr("billing-pe"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: pls.ID,
					RequestMessage:       to.Ptr("please connect us"),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	pe, err := pePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Len(t, pe.Properties.NetworkInterfaces, 1)
	peNIC, err := nicClient.Get(ctx, rg, resourceName(*pe.Properties.NetworkInterfaces[0].ID), nil)
	require.NoError(t, err)
	peAddress := *peNIC.Properties.IPConfigurations[0].Properties.PrivateIPAddress
	assert.NotEmpty(t, peAddress)
	assert.NotEqual(t, natAddress, peAddress, "the endpoint's address is its own, from its own subnet")

	// A manual connection waits for the service owner.
	require.Len(t, pe.Properties.ManualPrivateLinkServiceConnections, 1)
	state := pe.Properties.ManualPrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState
	require.NotNil(t, state)
	assert.Equal(t, "Pending", *state.Status)

	// The very same connection is what the service lists.
	var connNames []string
	connPager := plsClient.NewListPrivateEndpointConnectionsPager(rg, "billing-pls", nil)
	for connPager.More() {
		page, err := connPager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			connNames = append(connNames, *c.Name)
		}
	}
	require.Equal(t, []string{"billing-pe"}, connNames)

	conn, err := plsClient.GetPrivateEndpointConnection(ctx, rg, "billing-pls", "billing-pe", nil)
	require.NoError(t, err)
	assert.Equal(t, "Pending", *conn.Properties.PrivateLinkServiceConnectionState.Status)
	require.NotNil(t, conn.Properties.PrivateEndpoint)
	assert.Equal(t, *pe.ID, *conn.Properties.PrivateEndpoint.ID)
	require.NotNil(t, conn.Properties.LinkIdentifier)
	assert.NotEmpty(t, *conn.Properties.LinkIdentifier)

	// The service reports the connection on its own read too.
	withConn, err := plsClient.Get(ctx, rg, "billing-pls", nil)
	require.NoError(t, err)
	require.Len(t, withConn.Properties.PrivateEndpointConnections, 1)

	// The owner approves; the consumer's endpoint reads the decision back.
	approved, err := plsClient.UpdatePrivateEndpointConnection(ctx, rg, "billing-pls", "billing-pe",
		armnetwork.PrivateEndpointConnection{
			Properties: &armnetwork.PrivateEndpointConnectionProperties{
				PrivateLinkServiceConnectionState: &armnetwork.PrivateLinkServiceConnectionState{
					Status:      to.Ptr("Approved"),
					Description: to.Ptr("approved by billing"),
				},
			},
		}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Approved", *approved.Properties.PrivateLinkServiceConnectionState.Status)

	reread, err := peClient.Get(ctx, rg, "billing-pe", nil)
	require.NoError(t, err)
	state = reread.Properties.ManualPrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState
	require.NotNil(t, state)
	assert.Equal(t, "Approved", *state.Status,
		"the endpoint must report the decision made on the service")
	assert.Equal(t, "approved by billing", *state.Description)

	// Removing the connection on the service disconnects the endpoint.
	delConn, err := plsClient.BeginDeletePrivateEndpointConnection(ctx, rg, "billing-pls", "billing-pe", nil)
	require.NoError(t, err)
	_, err = delConn.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	disconnected, err := peClient.Get(ctx, rg, "billing-pe", nil)
	require.NoError(t, err)
	assert.Equal(t, "Disconnected",
		*disconnected.Properties.ManualPrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)

	// Deleting the endpoint releases the interface it owned.
	delPE, err := peClient.BeginDelete(ctx, rg, "billing-pe", nil)
	require.NoError(t, err)
	_, err = delPE.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = nicClient.Get(ctx, rg, resourceName(*pe.Properties.NetworkInterfaces[0].ID), nil)
	require.Error(t, err, "the endpoint's interface must be released with the endpoint")

	delPLS, err := plsClient.BeginDelete(ctx, rg, "billing-pls", nil)
	require.NoError(t, err)
	_, err = delPLS.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = nicClient.Get(ctx, rg, natNICName, nil)
	require.Error(t, err, "the service's NAT interface must be released with the service")
}

// TestNetworkPrivateLinkService_VisibilityAndAutoApproval covers the two
// consumer-facing discovery operations and the auto-approval they drive: a
// service that auto-approves the caller's subscription connects an endpoint
// without the owner acting, and one that does not leaves it pending.
func TestNetworkPrivateLinkService_VisibilityAndAutoApproval(t *testing.T) {
	requireNetworkHost(t)
	rg := "plink-visibility-rg"
	requireResourceGroup(t, rg)
	natSubnet := requireSubnet(t, rg, "vis-provider-vnet", "10.52.0.0/16", "nat-subnet", "10.52.1.0/24")
	peSubnet := requireSubnet(t, rg, "vis-consumer-vnet", "10.53.0.0/16", "pe-subnet", "10.53.1.0/24")

	plsClient, err := armnetwork.NewPrivateLinkServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := plsClient.BeginCreateOrUpdate(ctx, rg, "open-pls", armnetwork.PrivateLinkService{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateLinkServiceProperties{
			IPConfigurations: []*armnetwork.PrivateLinkServiceIPConfiguration{{
				Name: to.Ptr("natcfg1"),
				Properties: &armnetwork.PrivateLinkServiceIPConfigurationProperties{
					Subnet: &armnetwork.Subnet{ID: to.Ptr(natSubnet)},
				},
			}},
			Visibility: &armnetwork.PrivateLinkServicePropertiesVisibility{
				Subscriptions: []*string{to.Ptr(subscriptionID)},
			},
			AutoApproval: &armnetwork.PrivateLinkServicePropertiesAutoApproval{
				Subscriptions: []*string{to.Ptr(subscriptionID)},
			},
		},
	}, nil)
	require.NoError(t, err)
	pls, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	visPoller, err := plsClient.BeginCheckPrivateLinkServiceVisibility(ctx, "eastus",
		armnetwork.CheckPrivateLinkServiceVisibilityRequest{PrivateLinkServiceAlias: pls.Properties.Alias}, nil)
	require.NoError(t, err)
	vis, err := visPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, vis.Visible)
	assert.True(t, *vis.Visible, "the service lists this subscription in its visibility set")

	unknownPoller, err := plsClient.BeginCheckPrivateLinkServiceVisibility(ctx, "eastus",
		armnetwork.CheckPrivateLinkServiceVisibilityRequest{
			PrivateLinkServiceAlias: to.Ptr("nothing.00000000.eastus.azure.privatelinkservice"),
		}, nil)
	require.NoError(t, err)
	unknown, err := unknownPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, unknown.Visible)
	assert.False(t, *unknown.Visible, "an alias no service carries is not visible")

	autoApproved := map[string]bool{}
	autoPager := plsClient.NewListAutoApprovedPrivateLinkServicesPager("eastus", nil)
	for autoPager.More() {
		page, err := autoPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			autoApproved[*s.PrivateLinkService] = true
		}
	}
	assert.True(t, autoApproved[*pls.ID], "the service auto-approves this subscription")

	byRG := map[string]bool{}
	rgPager := plsClient.NewListAutoApprovedPrivateLinkServicesByResourceGroupPager("eastus", rg, nil)
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			byRG[*s.PrivateLinkService] = true
		}
	}
	assert.True(t, byRG[*pls.ID])

	// An automatic connection to an auto-approving service is connected at once.
	peClient, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "auto-pe", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(peSubnet)},
			PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name: to.Ptr("auto-pe"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: pls.ID,
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	pe, err := pePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	state := pe.Properties.PrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState
	require.NotNil(t, state)
	assert.Equal(t, "Approved", *state.Status)
	assert.Equal(t, "Auto-Approved", *state.Description)

	// A restricted service that lists no subscription is invisible.
	restrictedPoller, err := plsClient.BeginCreateOrUpdate(ctx, rg, "closed-pls", armnetwork.PrivateLinkService{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateLinkServiceProperties{
			AccessMode: to.Ptr(armnetwork.AccessMode("Restricted")),
			IPConfigurations: []*armnetwork.PrivateLinkServiceIPConfiguration{{
				Name: to.Ptr("natcfg1"),
				Properties: &armnetwork.PrivateLinkServiceIPConfigurationProperties{
					Subnet: &armnetwork.Subnet{ID: to.Ptr(natSubnet)},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	restricted, err := restrictedPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	closedVisPoller, err := plsClient.BeginCheckPrivateLinkServiceVisibilityByResourceGroup(ctx, "eastus", rg,
		armnetwork.CheckPrivateLinkServiceVisibilityRequest{PrivateLinkServiceAlias: restricted.Properties.Alias}, nil)
	require.NoError(t, err)
	closedVis, err := closedVisPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, closedVis.Visible)
	assert.False(t, *closedVis.Visible, "a restricted service with an empty visibility list is not visible")
}

// TestNetworkPrivateEndpoint_KeyVaultConnectionIsOneObject proves the
// integration across resource providers: an endpoint pointed at a Key Vault
// opens its connection in the vault's own privateEndpointConnections
// collection, so the armkeyvault client lists it, the vault owner's rejection
// there is what the endpoint reads back, and the endpoint publishes the vault's
// name under a custom DNS configuration.
func TestNetworkPrivateEndpoint_KeyVaultConnectionIsOneObject(t *testing.T) {
	requireNetworkHost(t)
	rg := "plink-kv-rg"
	requireResourceGroup(t, rg)
	subnetID := requireSubnet(t, rg, "kv-pe-vnet", "10.54.0.0/16", "pe-subnet", "10.54.1.0/24")

	vaultClient, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vaultPoller, err := vaultClient.BeginCreateOrUpdate(ctx, rg, "pelinkvault", armkeyvault.VaultCreateOrUpdateParameters{
		Location: to.Ptr("eastus"),
		Properties: &armkeyvault.VaultProperties{
			TenantID: to.Ptr(simTenantID),
			SKU: &armkeyvault.SKU{
				Family: to.Ptr(armkeyvault.SKUFamilyA),
				Name:   to.Ptr(armkeyvault.SKUNameStandard),
			},
		},
	}, nil)
	require.NoError(t, err)
	vault, err := vaultPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	peClient, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "vault-pe", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID)},
			PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name: to.Ptr("vault-pe"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: vault.ID,
					GroupIDs:             []*string{to.Ptr("vault")},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	pe, err := pePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The endpoint and the vault are in the same subscription, so the vault
	// owner's write access auto-approves the connection.
	state := pe.Properties.PrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState
	require.NotNil(t, state)
	assert.Equal(t, "Approved", *state.Status)

	// The endpoint publishes the vault's public name at its private address.
	require.Len(t, pe.Properties.CustomDNSConfigs, 1)
	assert.Equal(t, "pelinkvault.vault.azure.net", *pe.Properties.CustomDNSConfigs[0].Fqdn)
	require.Len(t, pe.Properties.CustomDNSConfigs[0].IPAddresses, 1)
	peAddress := *pe.Properties.CustomDNSConfigs[0].IPAddresses[0]
	assert.NotEmpty(t, peAddress)

	// The vault's own private-endpoint-connection surface lists it.
	kvConnClient, err := armkeyvault.NewPrivateEndpointConnectionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	kvConn, err := kvConnClient.Get(ctx, rg, "pelinkvault", "vault-pe", nil)
	require.NoError(t, err)
	require.NotNil(t, kvConn.Properties.PrivateLinkServiceConnectionState)
	assert.Equal(t, armkeyvault.PrivateEndpointServiceConnectionStatus("Approved"),
		*kvConn.Properties.PrivateLinkServiceConnectionState.Status)
	require.NotNil(t, kvConn.Properties.PrivateEndpoint)
	assert.Equal(t, *pe.ID, *kvConn.Properties.PrivateEndpoint.ID)

	listed := map[string]bool{}
	kvPager := kvConnClient.NewListByResourcePager(rg, "pelinkvault", nil)
	for kvPager.More() {
		page, err := kvPager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			listed[*c.Name] = true
		}
	}
	assert.True(t, listed["vault-pe"])

	// The vault owner rejects the connection on the vault; the endpoint reads
	// the rejection back, because both surfaces address one object.
	_, err = kvConnClient.Put(ctx, rg, "pelinkvault", "vault-pe", armkeyvault.PrivateEndpointConnection{
		Properties: &armkeyvault.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armkeyvault.PrivateLinkServiceConnectionState{
				Status:      to.Ptr(armkeyvault.PrivateEndpointServiceConnectionStatus("Rejected")),
				Description: to.Ptr("not this one"),
			},
		},
	}, nil)
	require.NoError(t, err)

	rejected, err := peClient.Get(ctx, rg, "vault-pe", nil)
	require.NoError(t, err)
	assert.Equal(t, "Rejected",
		*rejected.Properties.PrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)

	// A rewrite of the endpoint must not silently re-approve what the vault
	// owner rejected.
	rewritePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "vault-pe", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID)},
			PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name: to.Ptr("vault-pe"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: vault.ID,
					GroupIDs:             []*string{to.Ptr("vault")},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	rewritten, err := rewritePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "Rejected",
		*rewritten.Properties.PrivateLinkServiceConnections[0].Properties.PrivateLinkServiceConnectionState.Status)

	// Deleting the endpoint closes the connection on the vault.
	delPE, err := peClient.BeginDelete(ctx, rg, "vault-pe", nil)
	require.NoError(t, err)
	_, err = delPE.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = kvConnClient.Get(ctx, rg, "pelinkvault", "vault-pe", nil)
	require.Error(t, err, "deleting the endpoint must close the connection on the vault")
}

// TestNetworkPrivateEndpoint_DNSZoneGroupPublishesRecords proves a private DNS
// zone group publishes real A records: the records it reports are the records
// the private DNS zone's own record surface serves, and deleting the group
// takes them back out.
func TestNetworkPrivateEndpoint_DNSZoneGroupPublishesRecords(t *testing.T) {
	requireNetworkHost(t)
	rg := "plink-dns-rg"
	requireResourceGroup(t, rg)
	subnetID := requireSubnet(t, rg, "dns-pe-vnet", "10.55.0.0/16", "pe-subnet", "10.55.1.0/24")

	vaultClient, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vaultPoller, err := vaultClient.BeginCreateOrUpdate(ctx, rg, "dnslinkvault", armkeyvault.VaultCreateOrUpdateParameters{
		Location: to.Ptr("eastus"),
		Properties: &armkeyvault.VaultProperties{
			TenantID: to.Ptr(simTenantID),
			SKU: &armkeyvault.SKU{
				Family: to.Ptr(armkeyvault.SKUFamilyA),
				Name:   to.Ptr(armkeyvault.SKUNameStandard),
			},
		},
	}, nil)
	require.NoError(t, err)
	vault, err := vaultPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	zoneClient, err := armprivatedns.NewPrivateZonesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	const zoneName = "privatelink.vaultcore.azure.net"
	zonePoller, err := zoneClient.BeginCreateOrUpdate(ctx, rg, zoneName, armprivatedns.PrivateZone{
		Location: to.Ptr("global"),
	}, nil)
	require.NoError(t, err)
	zone, err := zonePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	peClient, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "dns-pe", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID)},
			PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name: to.Ptr("dns-pe"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
					PrivateLinkServiceID: vault.ID,
					GroupIDs:             []*string{to.Ptr("vault")},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	pe, err := pePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	peAddress := *pe.Properties.CustomDNSConfigs[0].IPAddresses[0]

	groupClient, err := armnetwork.NewPrivateDNSZoneGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	groupPoller, err := groupClient.BeginCreateOrUpdate(ctx, rg, "dns-pe", "default",
		armnetwork.PrivateDNSZoneGroup{
			Properties: &armnetwork.PrivateDNSZoneGroupPropertiesFormat{
				PrivateDNSZoneConfigs: []*armnetwork.PrivateDNSZoneConfig{{
					Name: to.Ptr("vault-config"),
					Properties: &armnetwork.PrivateDNSZonePropertiesFormat{
						PrivateDNSZoneID: zone.ID,
					},
				}},
			},
		}, nil)
	require.NoError(t, err)
	group, err := groupPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Len(t, group.Properties.PrivateDNSZoneConfigs, 1)
	records := group.Properties.PrivateDNSZoneConfigs[0].Properties.RecordSets
	require.Len(t, records, 1)
	assert.Equal(t, "A", *records[0].RecordType)
	assert.Equal(t, "dnslinkvault", *records[0].RecordSetName)
	require.Len(t, records[0].IPAddresses, 1)
	assert.Equal(t, peAddress, *records[0].IPAddresses[0])

	// The record the group reports is a real record set in the zone.
	recordClient, err := armprivatedns.NewRecordSetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	record, err := recordClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeA, "dnslinkvault", nil)
	require.NoError(t, err)
	require.Len(t, record.Properties.ARecords, 1)
	assert.Equal(t, peAddress, *record.Properties.ARecords[0].IPv4Address)

	got, err := groupClient.Get(ctx, rg, "dns-pe", "default", nil)
	require.NoError(t, err)
	assert.Equal(t, "Succeeded", string(*got.Properties.ProvisioningState))

	groups := map[string]bool{}
	groupPager := groupClient.NewListPager("dns-pe", rg, nil)
	for groupPager.More() {
		page, err := groupPager.NextPage(ctx)
		require.NoError(t, err)
		for _, g := range page.Value {
			groups[*g.Name] = true
		}
	}
	assert.True(t, groups["default"])

	delGroup, err := groupClient.BeginDelete(ctx, rg, "dns-pe", "default", nil)
	require.NoError(t, err)
	_, err = delGroup.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = recordClient.Get(ctx, rg, zoneName, armprivatedns.RecordTypeA, "dnslinkvault", nil)
	require.Error(t, err, "removing the zone group must remove the records it published")
}

// TestNetworkPrivateEndpoint_ListsAndAvailableTypes covers the endpoint list
// operations and the available-private-endpoint-type catalog, which reports the
// resource types an endpoint can actually terminate on.
func TestNetworkPrivateEndpoint_ListsAndAvailableTypes(t *testing.T) {
	requireNetworkHost(t)
	rg := "plink-list-rg"
	requireResourceGroup(t, rg)
	subnetID := requireSubnet(t, rg, "list-pe-vnet", "10.56.0.0/16", "pe-subnet", "10.56.1.0/24")

	plsClient, err := armnetwork.NewPrivateLinkServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	natSubnet := requireSubnet(t, rg, "list-pls-vnet", "10.57.0.0/16", "nat-subnet", "10.57.1.0/24")
	plsPoller, err := plsClient.BeginCreateOrUpdate(ctx, rg, "list-pls", armnetwork.PrivateLinkService{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateLinkServiceProperties{
			IPConfigurations: []*armnetwork.PrivateLinkServiceIPConfiguration{{
				Name: to.Ptr("natcfg1"),
				Properties: &armnetwork.PrivateLinkServiceIPConfigurationProperties{
					Subnet: &armnetwork.Subnet{ID: to.Ptr(natSubnet)},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	pls, err := plsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	peClient, err := armnetwork.NewPrivateEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pePoller, err := peClient.BeginCreateOrUpdate(ctx, rg, "list-pe", armnetwork.PrivateEndpoint{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID)},
			ManualPrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{{
				Name:       to.Ptr("list-pe"),
				Properties: &armnetwork.PrivateLinkServiceConnectionProperties{PrivateLinkServiceID: pls.ID},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = pePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	inRG := false
	rgPager := peClient.NewListPager(rg, nil)
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if *p.Name == "list-pe" {
				inRG = true
			}
		}
	}
	assert.True(t, inRG)

	inSub := false
	subPager := peClient.NewListBySubscriptionPager(nil)
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if *p.Name == "list-pe" {
				inSub = true
			}
		}
	}
	assert.True(t, inSub)

	plsInSub := false
	plsPager := plsClient.NewListBySubscriptionPager(nil)
	for plsPager.More() {
		page, err := plsPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			if *s.Name == "list-pls" {
				plsInSub = true
			}
		}
	}
	assert.True(t, plsInSub)

	plsInRG := false
	plsRGPager := plsClient.NewListPager(rg, nil)
	for plsRGPager.More() {
		page, err := plsRGPager.NextPage(ctx)
		require.NoError(t, err)
		for _, s := range page.Value {
			if *s.Name == "list-pls" {
				plsInRG = true
			}
		}
	}
	assert.True(t, plsInRG)

	typesClient, err := armnetwork.NewAvailablePrivateEndpointTypesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	types := map[string]bool{}
	typePager := typesClient.NewListPager("eastus", nil)
	for typePager.More() {
		page, err := typePager.NextPage(ctx)
		require.NoError(t, err)
		for _, tp := range page.Value {
			types[*tp.ResourceName] = true
		}
	}
	assert.True(t, types["Microsoft.KeyVault/vaults"])
	assert.True(t, types["Microsoft.Network/privateLinkServices"])
	assert.True(t, types["Microsoft.Storage/storageAccounts"])

	byRG := map[string]bool{}
	typeRGPager := typesClient.NewListByResourceGroupPager("eastus", rg, nil)
	for typeRGPager.More() {
		page, err := typeRGPager.NextPage(ctx)
		require.NoError(t, err)
		for _, tp := range page.Value {
			byRG[*tp.ResourceName] = true
		}
	}
	assert.True(t, byRG["Microsoft.KeyVault/vaults"])
}

// TestNetworkProfiles_CRUD covers the whole Microsoft.Network/networkProfiles
// surface and the subnet validation its container network interface
// configurations are subject to.
func TestNetworkProfiles_CRUD(t *testing.T) {
	requireNetworkHost(t)
	rg := "netprofile-rg"
	requireResourceGroup(t, rg)
	subnetID := requireSubnet(t, rg, "profile-vnet", "10.58.0.0/16", "aci-subnet", "10.58.1.0/24")

	client, err := armnetwork.NewProfilesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// A configuration naming a subnet that does not exist is rejected.
	_, err = client.CreateOrUpdate(ctx, rg, "bad-profile", armnetwork.Profile{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.ProfilePropertiesFormat{
			ContainerNetworkInterfaceConfigurations: []*armnetwork.ContainerNetworkInterfaceConfiguration{{
				Name: to.Ptr("cnic"),
				Properties: &armnetwork.ContainerNetworkInterfaceConfigurationPropertiesFormat{
					IPConfigurations: []*armnetwork.IPConfigurationProfile{{
						Name: to.Ptr("ipconfig"),
						Properties: &armnetwork.IPConfigurationProfilePropertiesFormat{
							Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID + "-missing")},
						},
					}},
				},
			}},
		},
	}, nil)
	require.Error(t, err, "a network profile naming a nonexistent subnet must be rejected")

	created, err := client.CreateOrUpdate(ctx, rg, "aci-profile", armnetwork.Profile{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.ProfilePropertiesFormat{
			ContainerNetworkInterfaceConfigurations: []*armnetwork.ContainerNetworkInterfaceConfiguration{{
				Name: to.Ptr("aci-cnic"),
				Properties: &armnetwork.ContainerNetworkInterfaceConfigurationPropertiesFormat{
					IPConfigurations: []*armnetwork.IPConfigurationProfile{{
						Name: to.Ptr("ipconfig1"),
						Properties: &armnetwork.IPConfigurationProfilePropertiesFormat{
							Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID)},
						},
					}},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, created.Properties.ContainerNetworkInterfaceConfigurations, 1)
	cfg := created.Properties.ContainerNetworkInterfaceConfigurations[0]
	assert.Equal(t, *created.ID+"/containerNetworkInterfaceConfigurations/aci-cnic", *cfg.ID)
	require.Len(t, cfg.Properties.IPConfigurations, 1)
	assert.Equal(t, subnetID, *cfg.Properties.IPConfigurations[0].Properties.Subnet.ID)
	// Nothing has been deployed against the profile, so it has realized no
	// container network interfaces.
	assert.Empty(t, created.Properties.ContainerNetworkInterfaces)

	got, err := client.Get(ctx, rg, "aci-profile", nil)
	require.NoError(t, err)
	assert.Equal(t, *created.Properties.ResourceGUID, *got.Properties.ResourceGUID)

	tagged, err := client.UpdateTags(ctx, rg, "aci-profile", armnetwork.TagsObject{
		Tags: map[string]*string{"workload": to.Ptr("aci")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "aci", *tagged.Tags["workload"])

	inRG := false
	rgPager := client.NewListPager(rg, nil)
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if *p.Name == "aci-profile" {
				inRG = true
			}
		}
	}
	assert.True(t, inRG)

	inSub := false
	allPager := client.NewListAllPager(nil)
	for allPager.More() {
		page, err := allPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if *p.Name == "aci-profile" {
				inSub = true
			}
		}
	}
	assert.True(t, inSub)

	delPoller, err := client.BeginDelete(ctx, rg, "aci-profile", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "aci-profile", nil)
	require.Error(t, err)
}

// resourceName returns the last segment of an ARM resource id.
func resourceName(id string) string {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '/' {
			return id[i+1:]
		}
	}
	return id
}
