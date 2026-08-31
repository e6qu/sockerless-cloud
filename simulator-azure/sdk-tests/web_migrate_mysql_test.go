package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for migrating a site's in-app MySQL database:
//
//	POST .../sites/{name}/migratemysql
//	GET  .../sites/{name}/migratemysql/status
//	GET  .../sites/{name}/slots/{slot}/migratemysql/status
//
// Whether the site has a database to migrate is the app setting App Service
// switches in-app MySQL on with, so the refusal and the acceptance are both
// read from the site rather than declared.
func TestSDK_WebApps_MigrateMySql(t *testing.T) {
	rg := "sdk-mysql-rg"
	ensureRG(t, rg)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	newSite := func(name string, inApp bool) {
		t.Helper()
		config := &armappservice.SiteConfig{}
		if inApp {
			config.AppSettings = []*armappservice.NameValuePair{
				{Name: to.Ptr("WEBSITE_MYSQL_ENABLED"), Value: to.Ptr("1")},
			}
		}
		poller, err := sites.BeginCreateOrUpdate(ctx, rg, name, armappservice.Site{
			Location:   to.Ptr("eastus"),
			Properties: &armappservice.SiteProperties{SiteConfig: config},
		}, nil)
		require.NoError(t, err)
		_, err = poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
	}

	// A site without in-app MySQL has no database to move, and says so.
	newSite("sdk-mysql-plain", false)
	plain, err := sites.GetMigrateMySQLStatus(ctx, rg, "sdk-mysql-plain", nil)
	require.NoError(t, err)
	require.NotNil(t, plain.Properties)
	require.NotNil(t, plain.Properties.LocalMySQLEnabled)
	assert.False(t, *plain.Properties.LocalMySQLEnabled)
	// Nothing was ever migrated, so no operation is reported.
	assert.Nil(t, plain.Properties.OperationID)

	_, err = sites.BeginMigrateMySQL(ctx, rg, "sdk-mysql-plain", armappservice.MigrateMySQLRequest{
		Properties: &armappservice.MigrateMySQLRequestProperties{
			ConnectionString: to.Ptr("Server=remote;Database=app;Uid=admin;Pwd=secret;"),
			MigrationType:    to.Ptr(armappservice.MySQLMigrationTypeLocalToRemote),
		},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in-app MySQL")

	// A site with it enabled accepts the migration, and the status then reports
	// the operation the caller started.
	newSite("sdk-mysql-inapp", true)
	poller, err := sites.BeginMigrateMySQL(ctx, rg, "sdk-mysql-inapp", armappservice.MigrateMySQLRequest{
		Properties: &armappservice.MigrateMySQLRequestProperties{
			ConnectionString: to.Ptr("Server=remote;Database=app;Uid=admin;Pwd=secret;"),
			MigrationType:    to.Ptr(armappservice.MySQLMigrationTypeLocalToRemote),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	status, err := sites.GetMigrateMySQLStatus(ctx, rg, "sdk-mysql-inapp", nil)
	require.NoError(t, err)
	require.NotNil(t, status.Properties)
	require.NotNil(t, status.Properties.LocalMySQLEnabled)
	assert.True(t, *status.Properties.LocalMySQLEnabled)
	require.NotNil(t, status.Properties.OperationID)
	assert.NotEmpty(t, *status.Properties.OperationID)
	require.NotNil(t, status.Properties.MigrationOperationStatus)

	// A migration with no destination is refused before anything is recorded.
	_, err = sites.BeginMigrateMySQL(ctx, rg, "sdk-mysql-inapp", armappservice.MigrateMySQLRequest{
		Properties: &armappservice.MigrateMySQLRequestProperties{
			MigrationType: to.Ptr(armappservice.MySQLMigrationTypeLocalToRemote),
		},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connectionString")

	// A slot reads the status in its own right.
	slotPoller, err := sites.BeginCreateOrUpdateSlot(ctx, rg, "sdk-mysql-inapp", "staging", armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	slotStatus, err := sites.GetMigrateMySQLStatusSlot(ctx, rg, "sdk-mysql-inapp", "staging", nil)
	require.NoError(t, err)
	require.NotNil(t, slotStatus.Properties)
	require.NotNil(t, slotStatus.Properties.LocalMySQLEnabled)
	// The slot carries no in-app database of its own.
	assert.False(t, *slotStatus.Properties.LocalMySQLEnabled)
}

// SDK coverage for moving a site's content into an Azure Files share:
//
//	PUT .../sites/{name}/migrate
//
// What a caller can observe is the operation the platform starts, which the
// simulator holds; no bytes move, because these sites are served out of a
// container image rather than out of a share.
func TestSDK_WebApps_MigrateStorage(t *testing.T) {
	rg := "sdk-migrate-rg"
	ensureRG(t, rg)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := sites.BeginCreateOrUpdate(ctx, rg, "sdk-migrate-site", armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	migrate, err := sites.BeginMigrateStorage(ctx, "sdk-migrate-sub", rg, "sdk-migrate-site",
		armappservice.StorageMigrationOptions{
			Properties: &armappservice.StorageMigrationOptionsProperties{
				AzurefilesConnectionString: to.Ptr("DefaultEndpointsProtocol=https;AccountName=content"),
				AzurefilesShare:            to.Ptr("site-content"),
			},
		}, nil)
	require.NoError(t, err)
	done, err := migrate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, done.Properties)
	require.NotNil(t, done.Properties.OperationID)
	assert.NotEmpty(t, *done.Properties.OperationID,
		"the platform returns the operation identifying the migration it started")

	// A migration with no share to move into is refused before anything is
	// recorded.
	_, err = sites.BeginMigrateStorage(ctx, "sdk-migrate-sub", rg, "sdk-migrate-site",
		armappservice.StorageMigrationOptions{
			Properties: &armappservice.StorageMigrationOptionsProperties{
				AzurefilesConnectionString: to.Ptr("DefaultEndpointsProtocol=https;AccountName=content"),
			},
		}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "azurefilesShare")

	// A site that is not there is a 404.
	_, err = sites.BeginMigrateStorage(ctx, "sdk-migrate-sub", rg, "sdk-migrate-absent",
		armappservice.StorageMigrationOptions{
			Properties: &armappservice.StorageMigrationOptionsProperties{
				AzurefilesConnectionString: to.Ptr("DefaultEndpointsProtocol=https;AccountName=content"),
				AzurefilesShare:            to.Ptr("site-content"),
			},
		}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
