package azure_sdk_test

import (
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Azure Virtual Network Manager through the official armnetwork client: the
// manager resource, the commit that deploys configurations to a set of regions,
// and the per-region deployment status that commit produces.

func TestNetworkManager_CommitAndDeploymentStatus(t *testing.T) {
	rg := "netmanager-rg"
	requireResourceGroup(t, rg)

	client, err := armnetwork.NewManagersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	created, err := client.CreateOrUpdate(ctx, rg, "corp-manager", armnetwork.Manager{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"owner": to.Ptr("platform")},
		Properties: &armnetwork.ManagerProperties{
			Description: to.Ptr("corporate network manager"),
			NetworkManagerScopes: &armnetwork.ManagerPropertiesNetworkManagerScopes{
				Subscriptions: []*string{to.Ptr("/subscriptions/" + subscriptionID)},
			},
			NetworkManagerScopeAccesses: []*armnetwork.ConfigurationType{
				to.Ptr(armnetwork.ConfigurationTypeConnectivity),
				to.Ptr(armnetwork.ConfigurationTypeSecurityAdmin),
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, armnetwork.ProvisioningStateSucceeded, *created.Properties.ProvisioningState)
	require.NotNil(t, created.Properties.ResourceGUID)
	assert.NotEmpty(t, *created.Properties.ResourceGUID)
	require.NotNil(t, created.SystemData)
	require.NotNil(t, created.SystemData.CreatedAt)
	require.NotNil(t, created.Etag)

	got, err := client.Get(ctx, rg, "corp-manager", nil)
	require.NoError(t, err)
	assert.Equal(t, "corporate network manager", *got.Properties.Description)
	require.Len(t, got.Properties.NetworkManagerScopeAccesses, 2)

	patched, err := client.Patch(ctx, rg, "corp-manager", armnetwork.PatchObject{
		Tags: map[string]*string{"owner": to.Ptr("platform"), "env": to.Ptr("test")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", *patched.Tags["env"])
	// A tag patch must not re-mint the resource GUID.
	assert.Equal(t, *created.Properties.ResourceGUID, *patched.Properties.ResourceGUID)

	assert.Contains(t, networkManagerNames(t, client, rg), "corp-manager")
	all := map[string]bool{}
	subPager := client.NewListBySubscriptionPager(nil)
	for subPager.More() {
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		for _, manager := range page.Value {
			all[*manager.Name] = true
		}
	}
	assert.True(t, all["corp-manager"], "the subscription-wide list must contain the manager")

	// A commit deploys to the regions it names. With no configurations named it
	// is the commit that removes whatever those regions currently hold, which is
	// a legitimate commit and the one the simulator can carry out in full.
	commitClient, err := armnetwork.NewManagerCommitsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	commitPoller, err := commitClient.BeginPost(ctx, rg, "corp-manager", armnetwork.ManagerCommit{
		CommitType:      to.Ptr(armnetwork.ConfigurationTypeConnectivity),
		TargetLocations: []*string{to.Ptr("eastus"), to.Ptr("westus")},
	}, nil)
	require.NoError(t, err)
	commit, err := commitPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, commit.CommitID)
	assert.NotEmpty(t, *commit.CommitID)

	statusClient, err := armnetwork.NewManagerDeploymentStatusClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	status, err := statusClient.List(ctx, rg, "corp-manager", armnetwork.ManagerDeploymentStatusParameter{}, nil)
	require.NoError(t, err)
	require.Len(t, status.Value, 2, "the commit deployed to both regions it named")
	regions := map[string]armnetwork.DeploymentStatus{}
	for _, entry := range status.Value {
		regions[*entry.Region] = *entry.DeploymentStatus
		assert.Equal(t, armnetwork.ConfigurationTypeConnectivity, *entry.DeploymentType)
		require.NotNil(t, entry.CommitTime)
	}
	assert.Equal(t, armnetwork.DeploymentStatusDeployed, regions["eastus"])
	assert.Equal(t, armnetwork.DeploymentStatusDeployed, regions["westus"])

	// The status query filters by region and by configuration type, and a type
	// nothing was committed for holds nothing.
	filtered, err := statusClient.List(ctx, rg, "corp-manager", armnetwork.ManagerDeploymentStatusParameter{
		Regions: []*string{to.Ptr("westus")},
	}, nil)
	require.NoError(t, err)
	require.Len(t, filtered.Value, 1)
	assert.Equal(t, "westus", *filtered.Value[0].Region)

	byType, err := statusClient.List(ctx, rg, "corp-manager", armnetwork.ManagerDeploymentStatusParameter{
		DeploymentTypes: []*armnetwork.ConfigurationType{to.Ptr(armnetwork.ConfigurationTypeSecurityAdmin)},
	}, nil)
	require.NoError(t, err)
	assert.Empty(t, byType.Value, "no security admin configuration has been committed")

	delPoller, err := client.BeginDelete(ctx, rg, "corp-manager", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, "corp-manager", nil)
	require.Error(t, err, "a deleted network manager must not be readable")
}

// TestNetworkManager_RejectsUnscopedManagerAndUnknownConfiguration covers the
// two things the resource provider refuses: a manager that governs nothing, and
// a commit that names a configuration which does not exist.
func TestNetworkManager_RejectsUnscopedManagerAndUnknownConfiguration(t *testing.T) {
	rg := "netmanager-reject-rg"
	requireResourceGroup(t, rg)

	client, err := armnetwork.NewManagersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.CreateOrUpdate(ctx, rg, "no-scope", armnetwork.Manager{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.ManagerProperties{
			NetworkManagerScopes: &armnetwork.ManagerPropertiesNetworkManagerScopes{},
		},
	}, nil)
	require.Error(t, err, "a network manager that governs nothing must be rejected")

	_, err = client.CreateOrUpdate(ctx, rg, "scoped", armnetwork.Manager{
		Location: to.Ptr("eastus"),
		Properties: &armnetwork.ManagerProperties{
			NetworkManagerScopes: &armnetwork.ManagerPropertiesNetworkManagerScopes{
				Subscriptions: []*string{to.Ptr("/subscriptions/" + subscriptionID)},
			},
		},
	}, nil)
	require.NoError(t, err)

	commitClient, err := armnetwork.NewManagerCommitsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	missing := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkManagers/scoped/connectivityConfigurations/absent",
		subscriptionID, rg)
	poller, err := commitClient.BeginPost(ctx, rg, "scoped", armnetwork.ManagerCommit{
		CommitType:       to.Ptr(armnetwork.ConfigurationTypeConnectivity),
		TargetLocations:  []*string{to.Ptr("eastus")},
		ConfigurationIDs: []*string{to.Ptr(missing)},
	}, nil)
	if err == nil {
		_, err = poller.PollUntilDone(ctx, nil)
	}
	require.Error(t, err, "a commit naming a configuration that does not exist must be rejected")
	assert.Contains(t, err.Error(), "ResourceNotFound")

	// Nothing was deployed by the refused commit.
	statusClient, err := armnetwork.NewManagerDeploymentStatusClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	status, err := statusClient.List(ctx, rg, "scoped", armnetwork.ManagerDeploymentStatusParameter{}, nil)
	require.NoError(t, err)
	assert.Empty(t, status.Value)
}

func networkManagerNames(t *testing.T, client *armnetwork.ManagersClient, rg string) []string {
	t.Helper()
	var names []string
	pager := client.NewListPager(rg, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, manager := range page.Value {
			names = append(names, *manager.Name)
		}
	}
	return names
}
