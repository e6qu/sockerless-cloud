package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNetworkApplicationSecurityGroups_CRUD covers the whole
// Microsoft.Network/applicationSecurityGroups surface through the official
// armnetwork client: create, get, retag, list by resource group, list by
// subscription, delete.
func TestNetworkApplicationSecurityGroups_CRUD(t *testing.T) {
	rg := "asg-rg"
	requireResourceGroup(t, rg)

	client, err := armnetwork.NewApplicationSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	poller, err := client.BeginCreateOrUpdate(ctx, rg, "web-tier", armnetwork.ApplicationSecurityGroup{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"tier": to.Ptr("web")},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, "Succeeded", string(*created.Properties.ProvisioningState))
	require.NotNil(t, created.Properties.ResourceGUID)
	assert.NotEmpty(t, *created.Properties.ResourceGUID)
	require.NotNil(t, created.Etag)

	got, err := client.Get(ctx, rg, "web-tier", nil)
	require.NoError(t, err)
	assert.Equal(t, *created.ID, *got.ID)
	// The resource GUID is minted once and must not change between reads.
	assert.Equal(t, *created.Properties.ResourceGUID, *got.Properties.ResourceGUID)

	tagged, err := client.UpdateTags(ctx, rg, "web-tier", armnetwork.TagsObject{
		Tags: map[string]*string{"tier": to.Ptr("web"), "env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", *tagged.Tags["env"])
	// Retagging must not re-mint the resource GUID.
	assert.Equal(t, *created.Properties.ResourceGUID, *tagged.Properties.ResourceGUID)

	assert.Contains(t, applicationSecurityGroupNames(t, client, rg), "web-tier")

	names := map[string]bool{}
	allPager := client.NewListAllPager(nil)
	for allPager.More() {
		page, err := allPager.NextPage(ctx)
		require.NoError(t, err)
		for _, asg := range page.Value {
			names[*asg.Name] = true
		}
	}
	assert.True(t, names["web-tier"], "subscription-wide list must contain the group")

	delPoller, err := client.BeginDelete(ctx, rg, "web-tier", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "web-tier", nil)
	require.Error(t, err, "a deleted application security group must not be readable")
}

func applicationSecurityGroupNames(t *testing.T, client *armnetwork.ApplicationSecurityGroupsClient, rg string) []string {
	t.Helper()
	var names []string
	pager := client.NewListPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, asg := range page.Value {
			names = append(names, *asg.Name)
		}
	}
	return names
}

// TestNetworkApplicationSecurityGroup_NSGRuleReference proves a security rule
// can be written against application security groups rather than addresses: the
// group references survive the round trip on both the source and the
// destination side of the rule, which is what the packet filter resolves
// membership from.
func TestNetworkApplicationSecurityGroup_NSGRuleReference(t *testing.T) {
	rg := "asg-nsg-rg"
	requireResourceGroup(t, rg)

	asgClient, err := armnetwork.NewApplicationSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	mkASG := func(name string) *armnetwork.ApplicationSecurityGroup {
		poller, err := asgClient.BeginCreateOrUpdate(ctx, rg, name, armnetwork.ApplicationSecurityGroup{
			Location: to.Ptr("eastus"),
		}, nil)
		require.NoError(t, err)
		resp, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		return &resp.ApplicationSecurityGroup
	}
	web := mkASG("asg-web")
	db := mkASG("asg-db")

	nsgClient, err := armnetwork.NewSecurityGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nsgPoller, err := nsgClient.BeginCreateOrUpdate(ctx, rg, "asg-nsg", armnetwork.SecurityGroup{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = nsgPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	ruleClient, err := armnetwork.NewSecurityRulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	rulePoller, err := ruleClient.BeginCreateOrUpdate(ctx, rg, "asg-nsg", "web-to-db", armnetwork.SecurityRule{
		Properties: &armnetwork.SecurityRulePropertiesFormat{
			Protocol:                             to.Ptr(armnetwork.SecurityRuleProtocolTCP),
			Access:                               to.Ptr(armnetwork.SecurityRuleAccessAllow),
			Direction:                            to.Ptr(armnetwork.SecurityRuleDirectionInbound),
			Priority:                             to.Ptr[int32](200),
			SourcePortRange:                      to.Ptr("*"),
			DestinationPortRange:                 to.Ptr("1433"),
			SourceApplicationSecurityGroups:      []*armnetwork.ApplicationSecurityGroup{{ID: web.ID}},
			DestinationApplicationSecurityGroups: []*armnetwork.ApplicationSecurityGroup{{ID: db.ID}},
		},
	}, nil)
	require.NoError(t, err)
	rule, err := rulePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Len(t, rule.Properties.SourceApplicationSecurityGroups, 1)
	assert.Equal(t, *web.ID, *rule.Properties.SourceApplicationSecurityGroups[0].ID)
	require.Len(t, rule.Properties.DestinationApplicationSecurityGroups, 1)
	assert.Equal(t, *db.ID, *rule.Properties.DestinationApplicationSecurityGroups[0].ID)

	read, err := ruleClient.Get(ctx, rg, "asg-nsg", "web-to-db", nil)
	require.NoError(t, err)
	require.Len(t, read.Properties.SourceApplicationSecurityGroups, 1)
	assert.Equal(t, *web.ID, *read.Properties.SourceApplicationSecurityGroups[0].ID)
}

// TestNetworkVirtualNetworkTaps_CRUD covers the whole
// Microsoft.Network/virtualNetworkTaps surface, including the rejection of a
// tap that names no collector destination.
func TestNetworkVirtualNetworkTaps_CRUD(t *testing.T) {
	rg := "tap-rg"
	requireResourceGroup(t, rg)

	client, err := armnetwork.NewVirtualNetworkTapsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// A tap with no destination is rejected before anything is stored.
	badPoller, err := client.BeginCreateOrUpdate(ctx, rg, "tap-nowhere", armnetwork.VirtualNetworkTap{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkTapPropertiesFormat{},
	}, nil)
	if err == nil {
		_, err = badPoller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "a virtual network tap without a destination must be rejected")

	collector := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Network/networkInterfaces/collector-nic/ipConfigurations/ipconfig1"
	poller, err := client.BeginCreateOrUpdate(ctx, rg, "mirror-tap", armnetwork.VirtualNetworkTap{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkTapPropertiesFormat{
			DestinationNetworkInterfaceIPConfiguration: &armnetwork.InterfaceIPConfiguration{ID: to.Ptr(collector)},
			DestinationPort: to.Ptr[int32](4789),
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, "Succeeded", string(*created.Properties.ProvisioningState))
	assert.Equal(t, int32(4789), *created.Properties.DestinationPort)
	require.NotNil(t, created.Properties.DestinationNetworkInterfaceIPConfiguration)
	assert.Equal(t, collector, *created.Properties.DestinationNetworkInterfaceIPConfiguration.ID)
	// Nothing mirrors to a freshly created tap.
	assert.Empty(t, created.Properties.NetworkInterfaceTapConfigurations)

	got, err := client.Get(ctx, rg, "mirror-tap", nil)
	require.NoError(t, err)
	assert.Equal(t, *created.ID, *got.ID)

	tagged, err := client.UpdateTags(ctx, rg, "mirror-tap", armnetwork.TagsObject{
		Tags: map[string]*string{"purpose": to.Ptr("mirror")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "mirror", *tagged.Tags["purpose"])

	found := false
	pager := client.NewListByResourceGroupPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, tap := range page.Value {
			if *tap.Name == "mirror-tap" {
				found = true
			}
		}
	}
	assert.True(t, found, "resource-group list must contain the tap")

	foundAll := false
	allPager := client.NewListAllPager(nil)
	for allPager.More() {
		page, err := allPager.NextPage(ctx)
		require.NoError(t, err)
		for _, tap := range page.Value {
			if *tap.Name == "mirror-tap" {
				foundAll = true
			}
		}
	}
	assert.True(t, foundAll, "subscription-wide list must contain the tap")

	delPoller, err := client.BeginDelete(ctx, rg, "mirror-tap", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "mirror-tap", nil)
	require.Error(t, err)
}

// TestNetworkVirtualNetworkTap_ReportsMirroringInterfaces proves the tap's
// read-only networkInterfaceTapConfigurations collection is resolved from the
// interface tap configurations that actually reference the tap: attaching one
// makes it appear, removing it takes it away.
func TestNetworkVirtualNetworkTap_ReportsMirroringInterfaces(t *testing.T) {
	requireNetworkHost(t)
	rg := "tap-attach-rg"
	requireResourceGroup(t, rg)
	subnetID := requireSubnet(t, rg, "tap-vnet", "10.40.0.0/16", "tap-subnet", "10.40.1.0/24")

	tapClient, err := armnetwork.NewVirtualNetworkTapsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	collector := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Network/networkInterfaces/collector-nic/ipConfigurations/ipconfig1"
	tapPoller, err := tapClient.BeginCreateOrUpdate(ctx, rg, "attach-tap", armnetwork.VirtualNetworkTap{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkTapPropertiesFormat{
			DestinationNetworkInterfaceIPConfiguration: &armnetwork.InterfaceIPConfiguration{ID: to.Ptr(collector)},
		},
	}, nil)
	require.NoError(t, err)
	tap, err := tapPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	nicPoller, err := nicClient.BeginCreateOrUpdate(ctx, rg, "mirrored-nic", armnetwork.Interface{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.InterfacePropertiesFormat{
			IPConfigurations: []*armnetwork.InterfaceIPConfiguration{{
				Name: to.Ptr("ipconfig1"),
				Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
					Subnet: &armnetwork.Subnet{ID: to.Ptr(subnetID)},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = nicPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	tapCfgClient, err := armnetwork.NewInterfaceTapConfigurationsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	cfgPoller, err := tapCfgClient.BeginCreateOrUpdate(ctx, rg, "mirrored-nic", "mirror-cfg",
		armnetwork.InterfaceTapConfiguration{
			Properties: &armnetwork.InterfaceTapConfigurationPropertiesFormat{
				VirtualNetworkTap: &armnetwork.VirtualNetworkTap{ID: tap.ID},
			},
		}, nil)
	require.NoError(t, err)
	cfg, err := cfgPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	attached, err := tapClient.Get(ctx, rg, "attach-tap", nil)
	require.NoError(t, err)
	require.Len(t, attached.Properties.NetworkInterfaceTapConfigurations, 1,
		"the tap must report the interface tap configuration that references it")
	assert.Equal(t, *cfg.ID, *attached.Properties.NetworkInterfaceTapConfigurations[0].ID)

	delCfg, err := tapCfgClient.BeginDelete(ctx, rg, "mirrored-nic", "mirror-cfg", nil)
	require.NoError(t, err)
	_, err = delCfg.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	detached, err := tapClient.Get(ctx, rg, "attach-tap", nil)
	require.NoError(t, err)
	assert.Empty(t, detached.Properties.NetworkInterfaceTapConfigurations,
		"removing the interface tap configuration must take it off the tap")
}

// TestNetworkServiceEndpointPolicies_CRUDAndDefinitions covers the policy
// surface and its definition sub-resource, and proves a definition written
// through the sub-resource client is reported by the policy's own read.
func TestNetworkServiceEndpointPolicies_CRUDAndDefinitions(t *testing.T) {
	rg := "sep-rg"
	requireResourceGroup(t, rg)

	client, err := armnetwork.NewServiceEndpointPoliciesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	accountID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg +
		"/providers/Microsoft.Storage/storageAccounts/allowedaccount"
	poller, err := client.BeginCreateOrUpdate(ctx, rg, "storage-policy", armnetwork.ServiceEndpointPolicy{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.ServiceEndpointPolicyPropertiesFormat{
			ServiceEndpointPolicyDefinitions: []*armnetwork.ServiceEndpointPolicyDefinition{{
				Name: to.Ptr("allow-one-account"),
				Properties: &armnetwork.ServiceEndpointPolicyDefinitionPropertiesFormat{
					Service:          to.Ptr("Microsoft.Storage"),
					ServiceResources: []*string{to.Ptr(accountID)},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Len(t, created.Properties.ServiceEndpointPolicyDefinitions, 1)
	def := created.Properties.ServiceEndpointPolicyDefinitions[0]
	assert.Equal(t, "Microsoft.Storage", *def.Properties.Service)
	require.NotNil(t, def.ID)
	assert.Equal(t, *created.ID+"/serviceEndpointPolicyDefinitions/allow-one-account", *def.ID)
	// No subnet enforces the policy yet.
	assert.Empty(t, created.Properties.Subnets)

	defClient, err := armnetwork.NewServiceEndpointPolicyDefinitionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	defPoller, err := defClient.BeginCreateOrUpdate(ctx, rg, "storage-policy", "allow-second-account",
		armnetwork.ServiceEndpointPolicyDefinition{
			Properties: &armnetwork.ServiceEndpointPolicyDefinitionPropertiesFormat{
				Description:      to.Ptr("second allowed account"),
				Service:          to.Ptr("Microsoft.Storage"),
				ServiceResources: []*string{to.Ptr(accountID + "2")},
			},
		}, nil)
	require.NoError(t, err)
	second, err := defPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "allow-second-account", *second.Name)

	readDef, err := defClient.Get(ctx, rg, "storage-policy", "allow-second-account", nil)
	require.NoError(t, err)
	assert.Equal(t, "second allowed account", *readDef.Properties.Description)

	withBoth, err := client.Get(ctx, rg, "storage-policy", nil)
	require.NoError(t, err)
	assert.Len(t, withBoth.Properties.ServiceEndpointPolicyDefinitions, 2,
		"the policy must report the definition written through the sub-resource client")

	defNames := map[string]bool{}
	defPager := defClient.NewListByResourceGroupPager(rg, "storage-policy", nil)
	for defPager.More() {
		page, err := defPager.NextPage(ctx)
		require.NoError(t, err)
		for _, d := range page.Value {
			defNames[*d.Name] = true
		}
	}
	assert.True(t, defNames["allow-one-account"] && defNames["allow-second-account"])

	tagged, err := client.UpdateTags(ctx, rg, "storage-policy", armnetwork.TagsObject{
		Tags: map[string]*string{"owner": to.Ptr("platform")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "platform", *tagged.Tags["owner"])

	inRG := false
	rgPager := client.NewListByResourceGroupPager(rg, nil)
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if *p.Name == "storage-policy" {
				inRG = true
			}
		}
	}
	assert.True(t, inRG)

	inSub := false
	subPager := client.NewListPager(nil)
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Value {
			if *p.Name == "storage-policy" {
				inSub = true
			}
		}
	}
	assert.True(t, inSub, "subscription-wide list must contain the policy")

	delDef, err := defClient.BeginDelete(ctx, rg, "storage-policy", "allow-second-account", nil)
	require.NoError(t, err)
	_, err = delDef.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	afterDelete, err := client.Get(ctx, rg, "storage-policy", nil)
	require.NoError(t, err)
	assert.Len(t, afterDelete.Properties.ServiceEndpointPolicyDefinitions, 1)

	delPolicy, err := client.BeginDelete(ctx, rg, "storage-policy", nil)
	require.NoError(t, err)
	_, err = delPolicy.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "storage-policy", nil)
	require.Error(t, err)
}

// TestNetworkServiceEndpointPolicy_ReportsEnforcingSubnets proves the policy's
// read-only subnets collection is resolved from the subnets that reference it,
// which is where the policy is actually enforced.
func TestNetworkServiceEndpointPolicy_ReportsEnforcingSubnets(t *testing.T) {
	requireNetworkHost(t)
	rg := "sep-subnet-rg"
	requireResourceGroup(t, rg)

	client, err := armnetwork.NewServiceEndpointPoliciesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreateOrUpdate(ctx, rg, "subnet-policy", armnetwork.ServiceEndpointPolicy{
		Location:   to.Ptr("eastus"),
		Properties: &armnetwork.ServiceEndpointPolicyPropertiesFormat{},
	}, nil)
	require.NoError(t, err)
	policy, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, "sep-vnet", armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr("10.41.0.0/16")}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, "sep-vnet", "sep-subnet", armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{
			AddressPrefix:           to.Ptr("10.41.1.0/24"),
			ServiceEndpointPolicies: []*armnetwork.ServiceEndpointPolicy{{ID: policy.ID}},
		},
	}, nil)
	require.NoError(t, err)
	subnet, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	enforced, err := client.Get(ctx, rg, "subnet-policy", nil)
	require.NoError(t, err)
	require.Len(t, enforced.Properties.Subnets, 1,
		"the policy must report the subnet that applies it")
	assert.Equal(t, *subnet.ID, *enforced.Properties.Subnets[0].ID)
}

// requireResourceGroup creates the resource group a test writes into.
func requireResourceGroup(t *testing.T, name string) {
	t.Helper()
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, name, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
}

// requireSubnet creates a virtual network and one subnet in it, returning the
// subnet's resource id.
func requireSubnet(t *testing.T, rg, vnetName, vnetPrefix, subnetName, subnetPrefix string) string {
	t.Helper()
	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	vnetPoller, err := vnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, armnetwork.VirtualNetwork{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.VirtualNetworkPropertiesFormat{
			AddressSpace: &armnetwork.AddressSpace{AddressPrefixes: []*string{to.Ptr(vnetPrefix)}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = vnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	subnetClient, err := armnetwork.NewSubnetsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	subnetPoller, err := subnetClient.BeginCreateOrUpdate(ctx, rg, vnetName, subnetName, armnetwork.Subnet{
		Properties: &armnetwork.SubnetPropertiesFormat{AddressPrefix: to.Ptr(subnetPrefix)},
	}, nil)
	require.NoError(t, err)
	subnet, err := subnetPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return *subnet.ID
}
