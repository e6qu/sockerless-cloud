package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createResourceGroup provisions a resource group via the SDK so per-service
// tests have a real parent scope.
func createResourceGroup(t *testing.T, name string) {
	t.Helper()
	client, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = client.CreateOrUpdate(ctx, name, armresources.ResourceGroup{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
}

// pgCreateServer provisions a flexible server through the SDK ServersClient and
// returns once it reports Ready, so the per-resource subtests have a real parent.
func pgCreateServer(t *testing.T, rg, name string) {
	t.Helper()
	client, err := armpostgresqlflexibleservers.NewServersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreate(ctx, rg, name, armpostgresqlflexibleservers.Server{
		Location: to.Ptr("eastus"),
		SKU: &armpostgresqlflexibleservers.SKU{
			Name: to.Ptr("Standard_B1ms"),
			Tier: to.Ptr(armpostgresqlflexibleservers.SKUTierBurstable),
		},
		Properties: &armpostgresqlflexibleservers.ServerProperties{
			AdministratorLogin:         to.Ptr("psqladmin"),
			AdministratorLoginPassword: to.Ptr("Sup3rSecret!"),
			Version:                    to.Ptr(armpostgresqlflexibleservers.ServerVersionFifteen),
			Storage:                    &armpostgresqlflexibleservers.Storage{StorageSizeGB: to.Ptr(int32(32))},
		},
	}, nil)
	require.NoError(t, err)
	resp, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, name, *resp.Name)
	require.NotNil(t, resp.SKU)
	assert.Equal(t, "Standard_B1ms", *resp.SKU.Name)
	assert.Equal(t, armpostgresqlflexibleservers.SKUTierBurstable, *resp.SKU.Tier)
}

func TestAzurePGFlexibleServer_LifecycleActions(t *testing.T) {
	rg := "pg-actions-rg"
	server := "pg-actions-srv"
	createResourceGroup(t, rg)
	pgCreateServer(t, rg, server)

	client, err := armpostgresqlflexibleservers.NewServersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	t.Run("Stop", func(t *testing.T) {
		poller, err := client.BeginStop(ctx, rg, server, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		got, err := client.Get(ctx, rg, server, nil)
		require.NoError(t, err)
		assert.Equal(t, armpostgresqlflexibleservers.ServerState("Stopped"), *got.Properties.State)
	})

	t.Run("Start", func(t *testing.T) {
		poller, err := client.BeginStart(ctx, rg, server, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		got, err := client.Get(ctx, rg, server, nil)
		require.NoError(t, err)
		assert.Equal(t, armpostgresqlflexibleservers.ServerState("Ready"), *got.Properties.State)
	})

	t.Run("Restart", func(t *testing.T) {
		poller, err := client.BeginRestart(ctx, rg, server, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	})

	t.Run("Update", func(t *testing.T) {
		poller, err := client.BeginUpdate(ctx, rg, server, armpostgresqlflexibleservers.ServerForUpdate{
			Tags: map[string]*string{"env": to.Ptr("test")},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		got, err := client.Get(ctx, rg, server, nil)
		require.NoError(t, err)
		assert.Equal(t, "test", *got.Tags["env"])
	})

	t.Run("ListBySubscriptionAndResourceGroup", func(t *testing.T) {
		subPager := client.NewListPager(nil)
		page, err := subPager.NextPage(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, page.Value)

		rgPager := client.NewListByResourceGroupPager(rg, nil)
		rgPage, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		found := false
		for _, s := range rgPage.Value {
			if *s.Name == server {
				found = true
			}
		}
		assert.True(t, found, "server should appear in resource-group list")
	})
}

func TestAzurePGFlexibleServer_NestedResources(t *testing.T) {
	rg := "pg-nested-rg"
	server := "pg-nested-srv"
	createResourceGroup(t, rg)
	pgCreateServer(t, rg, server)

	t.Run("Administrators", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewAdministratorsClient(subscriptionID, &fakeCredential{}, clientOpts())
		require.NoError(t, err)
		objectID := "11111111-1111-1111-1111-111111111111"
		poller, err := client.BeginCreate(ctx, rg, server, objectID, armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
			Properties: &armpostgresqlflexibleservers.AdministratorPropertiesForAdd{
				PrincipalName: to.Ptr("admin@contoso.com"),
				PrincipalType: to.Ptr(armpostgresqlflexibleservers.PrincipalTypeUser),
				TenantID:      to.Ptr("00000000-0000-0000-0000-000000000000"),
			},
		}, nil)
		require.NoError(t, err)
		created, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, "admin@contoso.com", *created.Properties.PrincipalName)

		got, err := client.Get(ctx, rg, server, objectID, nil)
		require.NoError(t, err)
		assert.Equal(t, objectID, *got.Name)

		pager := client.NewListByServerPager(rg, server, nil)
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		assert.Len(t, page.Value, 1)

		delPoller, err := client.BeginDelete(ctx, rg, server, objectID, nil)
		require.NoError(t, err)
		_, err = delPoller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	})

	t.Run("Backups", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewBackupsClient(subscriptionID, &fakeCredential{}, clientOpts())
		require.NoError(t, err)
		backupName := "ondemand-backup-1"
		poller, err := client.BeginCreate(ctx, rg, server, backupName, nil)
		require.NoError(t, err)
		created, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, backupName, *created.Name)

		got, err := client.Get(ctx, rg, server, backupName, nil)
		require.NoError(t, err)
		assert.Equal(t, backupName, *got.Name)

		pager := client.NewListByServerPager(rg, server, nil)
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		assert.Len(t, page.Value, 1)

		delPoller, err := client.BeginDelete(ctx, rg, server, backupName, nil)
		require.NoError(t, err)
		_, err = delPoller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	})

	t.Run("Replicas", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewReplicasClient(subscriptionID, &fakeCredential{}, clientOpts())
		require.NoError(t, err)
		pager := client.NewListByServerPager(rg, server, nil)
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value)
	})

	t.Run("VirtualEndpoints", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewVirtualEndpointsClient(subscriptionID, &fakeCredential{}, clientOpts())
		require.NoError(t, err)
		veName := "pg-nested-ve"
		poller, err := client.BeginCreate(ctx, rg, server, veName, armpostgresqlflexibleservers.VirtualEndpointResource{
			Properties: &armpostgresqlflexibleservers.VirtualEndpointResourceProperties{
				EndpointType: to.Ptr(armpostgresqlflexibleservers.VirtualEndpointTypeReadWrite),
				Members:      []*string{to.Ptr(server)},
			},
		}, nil)
		require.NoError(t, err)
		created, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, veName, *created.Name)
		assert.Equal(t, armpostgresqlflexibleservers.VirtualEndpointTypeReadWrite, *created.Properties.EndpointType)

		updPoller, err := client.BeginUpdate(ctx, rg, server, veName, armpostgresqlflexibleservers.VirtualEndpointResourceForPatch{
			Properties: &armpostgresqlflexibleservers.VirtualEndpointResourceProperties{
				EndpointType: to.Ptr(armpostgresqlflexibleservers.VirtualEndpointTypeReadWrite),
				Members:      []*string{to.Ptr(server)},
			},
		}, nil)
		require.NoError(t, err)
		_, err = updPoller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		got, err := client.Get(ctx, rg, server, veName, nil)
		require.NoError(t, err)
		assert.Equal(t, veName, *got.Name)

		pager := client.NewListByServerPager(rg, server, nil)
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		assert.Len(t, page.Value, 1)

		delPoller, err := client.BeginDelete(ctx, rg, server, veName, nil)
		require.NoError(t, err)
		_, err = delPoller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	})

	t.Run("ConfigurationUpdate", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewConfigurationsClient(subscriptionID, &fakeCredential{}, clientOpts())
		require.NoError(t, err)
		poller, err := client.BeginUpdate(ctx, rg, server, "maintenance_work_mem", armpostgresqlflexibleservers.ConfigurationForUpdate{
			Properties: &armpostgresqlflexibleservers.ConfigurationProperties{
				Value:  to.Ptr("131072"),
				Source: to.Ptr("user-override"),
			},
		}, nil)
		require.NoError(t, err)
		updated, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, "131072", *updated.Properties.Value)
	})
}

func TestAzurePGFlexibleServer_CheckNameAvailability(t *testing.T) {
	rg := "pg-cna-rg"
	server := "pg-cna-srv"
	createResourceGroup(t, rg)
	pgCreateServer(t, rg, server)

	subClient, err := armpostgresqlflexibleservers.NewCheckNameAvailabilityClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	taken, err := subClient.Execute(ctx, armpostgresqlflexibleservers.CheckNameAvailabilityRequest{
		Name: to.Ptr(server),
		Type: to.Ptr("Microsoft.DBforPostgreSQL/flexibleServers"),
	}, nil)
	require.NoError(t, err)
	assert.False(t, *taken.NameAvailable)

	free, err := subClient.Execute(ctx, armpostgresqlflexibleservers.CheckNameAvailabilityRequest{
		Name: to.Ptr("pg-cna-unused-name"),
		Type: to.Ptr("Microsoft.DBforPostgreSQL/flexibleServers"),
	}, nil)
	require.NoError(t, err)
	assert.True(t, *free.NameAvailable)

	locClient, err := armpostgresqlflexibleservers.NewCheckNameAvailabilityWithLocationClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	locResp, err := locClient.Execute(ctx, "eastus", armpostgresqlflexibleservers.CheckNameAvailabilityRequest{
		Name: to.Ptr("pg-cna-loc-name"),
		Type: to.Ptr("Microsoft.DBforPostgreSQL/flexibleServers"),
	}, nil)
	require.NoError(t, err)
	assert.True(t, *locResp.NameAvailable)
}
