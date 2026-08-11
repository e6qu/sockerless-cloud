package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzurePGFlexibleServer_CompletionSurfaces exercises the completion of the
// Microsoft.DBforPostgreSQL/flexibleServers control plane: provider operations
// metadata, the location/server capability catalogs, log files, quota usages,
// virtual-network subnet usage, the private-DNS-zone suffix, long-term-retention
// backups, server threat protection, migrations, and private endpoint
// connections / private link resources — through the official SDK clients.
func TestAzurePGFlexibleServer_CompletionSurfaces(t *testing.T) {
	rg := "pg-completion-rg"
	server := "pg-completion-srv"
	loc := "eastus"
	createResourceGroup(t, rg)
	pgCreateServer(t, rg, server)
	cred := &fakeCredential{}

	t.Run("Operations", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewOperationsClient(cred, clientOpts())
		require.NoError(t, err)
		resp, err := client.List(ctx, nil)
		require.NoError(t, err)
		assert.NotEmpty(t, resp.Value)
	})

	t.Run("GetPrivateDNSZoneSuffix", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewGetPrivateDNSZoneSuffixClient(cred, clientOpts())
		require.NoError(t, err)
		resp, err := client.Execute(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, "private.postgres.database.azure.com", *resp.Value)
	})

	t.Run("LocationBasedCapabilities", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewLocationBasedCapabilitiesClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		page, err := client.NewExecutePager(loc, nil).NextPage(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, page.Value)
		assert.Equal(t, loc, *page.Value[0].Name)
	})

	t.Run("ServerCapabilities", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewServerCapabilitiesClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		page, err := client.NewListPager(rg, server, nil).NextPage(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, page.Value)
	})

	t.Run("LogFiles", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewLogFilesClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		_, err = client.NewListByServerPager(rg, server, nil).NextPage(ctx)
		require.NoError(t, err)
	})

	t.Run("VirtualNetworkSubnetUsage", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewVirtualNetworkSubnetUsageClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		resp, err := client.Execute(ctx, loc, armpostgresqlflexibleservers.VirtualNetworkSubnetUsageParameter{
			VirtualNetworkArmResourceID: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Network/virtualNetworks/vnet"),
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, loc, *resp.Location)
	})

	t.Run("LtrBackups", func(t *testing.T) {
		fsClient, err := armpostgresqlflexibleservers.NewFlexibleServerClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		pre, err := fsClient.TriggerLtrPreBackup(ctx, rg, server, armpostgresqlflexibleservers.LtrPreBackupRequest{
			BackupSettings: &armpostgresqlflexibleservers.BackupSettings{BackupName: to.Ptr("ltr1")},
		}, nil)
		require.NoError(t, err)
		assert.NotNil(t, pre.Properties.NumberOfContainers)

		poller, err := fsClient.BeginStartLtrBackup(ctx, rg, server, armpostgresqlflexibleservers.LtrBackupRequest{
			BackupSettings: &armpostgresqlflexibleservers.BackupSettings{BackupName: to.Ptr("ltr1")},
			TargetDetails: &armpostgresqlflexibleservers.BackupStoreDetails{
				SasURIList: []*string{to.Ptr("https://example.blob.core.windows.net/ltr?sas")},
			},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		ltrClient, err := armpostgresqlflexibleservers.NewLtrBackupOperationsClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		_, err = ltrClient.NewListByServerPager(rg, server, nil).NextPage(ctx)
		require.NoError(t, err)
		got, err := ltrClient.Get(ctx, rg, server, "ltr1", nil)
		require.NoError(t, err)
		assert.Equal(t, "ltr1", *got.Name)
	})

	t.Run("ServerThreatProtection", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewServerThreatProtectionSettingsClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		poller, err := client.BeginCreateOrUpdate(ctx, rg, server, armpostgresqlflexibleservers.ThreatProtectionNameDefault,
			armpostgresqlflexibleservers.ServerThreatProtectionSettingsModel{
				Properties: &armpostgresqlflexibleservers.ServerThreatProtectionProperties{
					State: to.Ptr(armpostgresqlflexibleservers.ThreatProtectionStateEnabled),
				},
			}, nil)
		require.NoError(t, err)
		updated, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, armpostgresqlflexibleservers.ThreatProtectionStateEnabled, *updated.Properties.State)

		got, err := client.Get(ctx, rg, server, armpostgresqlflexibleservers.ThreatProtectionNameDefault, nil)
		require.NoError(t, err)
		assert.Equal(t, armpostgresqlflexibleservers.ThreatProtectionStateEnabled, *got.Properties.State)

		page, err := client.NewListByServerPager(rg, server, nil).NextPage(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, page.Value)
	})

	t.Run("Migrations", func(t *testing.T) {
		mgmt, err := armpostgresqlflexibleservers.NewPostgreSQLServerManagementClient(cred, clientOpts())
		require.NoError(t, err)
		avail, err := mgmt.CheckMigrationNameAvailability(ctx, subscriptionID, rg, server,
			armpostgresqlflexibleservers.MigrationNameAvailabilityResource{
				Name: to.Ptr("mig1"),
				Type: to.Ptr("Microsoft.DBforPostgreSQL/flexibleServers/migrations"),
			}, nil)
		require.NoError(t, err)
		assert.True(t, *avail.NameAvailable)

		client, err := armpostgresqlflexibleservers.NewMigrationsClient(cred, clientOpts())
		require.NoError(t, err)
		created, err := client.Create(ctx, subscriptionID, rg, server, "mig1", armpostgresqlflexibleservers.MigrationResource{
			Location: to.Ptr(loc),
			Properties: &armpostgresqlflexibleservers.MigrationResourceProperties{
				MigrationMode: to.Ptr(armpostgresqlflexibleservers.MigrationModeOffline),
			},
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "mig1", *created.Name)

		got, err := client.Get(ctx, subscriptionID, rg, server, "mig1", nil)
		require.NoError(t, err)
		assert.Equal(t, "mig1", *got.Name)

		_, err = client.Update(ctx, subscriptionID, rg, server, "mig1", armpostgresqlflexibleservers.MigrationResourceForPatch{
			Properties: &armpostgresqlflexibleservers.MigrationResourcePropertiesForPatch{
				MigrationMode: to.Ptr(armpostgresqlflexibleservers.MigrationModeOnline),
			},
		}, nil)
		require.NoError(t, err)

		page, err := client.NewListByTargetServerPager(subscriptionID, rg, server, nil).NextPage(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, page.Value)

		_, err = client.Delete(ctx, subscriptionID, rg, server, "mig1", nil)
		require.NoError(t, err)
	})

	t.Run("PrivateLinkResources", func(t *testing.T) {
		client, err := armpostgresqlflexibleservers.NewPrivateLinkResourcesClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		page, err := client.NewListByServerPager(rg, server, nil).NextPage(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, page.Value)
		got, err := client.Get(ctx, rg, server, "postgresqlServer", nil)
		require.NoError(t, err)
		assert.Equal(t, "postgresqlServer", *got.Properties.GroupID)
	})

	t.Run("PrivateEndpointConnections", func(t *testing.T) {
		single, err := armpostgresqlflexibleservers.NewPrivateEndpointConnectionClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		poller, err := single.BeginUpdate(ctx, rg, server, "pec1", armpostgresqlflexibleservers.PrivateEndpointConnection{
			Properties: &armpostgresqlflexibleservers.PrivateEndpointConnectionProperties{
				PrivateLinkServiceConnectionState: &armpostgresqlflexibleservers.PrivateLinkServiceConnectionState{
					Status:      to.Ptr(armpostgresqlflexibleservers.PrivateEndpointServiceConnectionStatusApproved),
					Description: to.Ptr("approved"),
				},
			},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		plural, err := armpostgresqlflexibleservers.NewPrivateEndpointConnectionsClient(subscriptionID, cred, clientOpts())
		require.NoError(t, err)
		got, err := plural.Get(ctx, rg, server, "pec1", nil)
		require.NoError(t, err)
		assert.Equal(t, "pec1", *got.Name)
		page, err := plural.NewListByServerPager(rg, server, nil).NextPage(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, page.Value)

		delPoller, err := single.BeginDelete(ctx, rg, server, "pec1", nil)
		require.NoError(t, err)
		_, err = delPoller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	})

	// TuningOptions and QuotaUsages are 2025-08-01 surfaces with no client in the
	// vendored v4 SDK; exercise them through a direct ARM request.
	t.Run("QuotaUsages", func(t *testing.T) {
		resp := armReq(t, "GET", "/subscriptions/"+subscriptionID+"/providers/Microsoft.DBforPostgreSQL/locations/"+loc+"/resourceType/flexibleServers/usages", "")
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	})

	t.Run("TuningOptions", func(t *testing.T) {
		base := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.DBforPostgreSQL/flexibleServers/" + server + "/tuningOptions"
		for _, p := range []string{base, base + "/index", base + "/index/recommendations"} {
			resp := armReq(t, "GET", p, "")
			require.Equal(t, 200, resp.StatusCode, p)
			resp.Body.Close()
		}
	})
}
