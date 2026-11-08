package azure_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireNotImplemented asserts a declared 501.
func requireNotImplemented(t *testing.T, err error) {
	t.Helper()
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "expected a service error, got %v", err)
	assert.Equal(t, http.StatusNotImplemented, respErr.StatusCode)
	assert.Equal(t, "NotImplemented", respErr.ErrorCode)
}

// No site runs an in-app MySQL server, so the migration is a declared 501 and
// the status names no operation; whether a site asks for in-app MySQL is still
// read from its WEBSITE_MYSQL_ENABLED setting.
//
//	POST .../sites/{name}/migratemysql
//	GET  .../sites/{name}/migratemysql/status
//	GET  .../sites/{name}/slots/{slot}/migratemysql/status
func TestSDK_WebApps_MigrateMySql(t *testing.T) {
	t.Parallel()
	rg := "sdk-mysql-rg"
	ensureRG(t, rg)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := sites.BeginCreateOrUpdate(ctx, rg, "sdk-mysql-inapp", armappservice.Site{
		Location: to.Ptr("eastus"),
		Properties: &armappservice.SiteProperties{SiteConfig: &armappservice.SiteConfig{
			AppSettings: []*armappservice.NameValuePair{{Name: to.Ptr("WEBSITE_MYSQL_ENABLED"), Value: to.Ptr("1")}},
		}},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	_, err = sites.BeginMigrateMySQL(ctx, rg, "sdk-mysql-inapp", armappservice.MigrateMySQLRequest{
		Properties: &armappservice.MigrateMySQLRequestProperties{
			ConnectionString: to.Ptr("Server=remote;Database=app;Uid=admin;Pwd=secret;"),
			MigrationType:    to.Ptr(armappservice.MySQLMigrationTypeLocalToRemote),
		},
	}, nil)
	requireNotImplemented(t, err)

	status, err := sites.GetMigrateMySQLStatus(ctx, rg, "sdk-mysql-inapp", nil)
	require.NoError(t, err)
	require.NotNil(t, status.Properties)
	require.NotNil(t, status.Properties.LocalMySQLEnabled)
	assert.True(t, *status.Properties.LocalMySQLEnabled)
	assert.Nil(t, status.Properties.OperationID, "no migration ran")

	slotPoller, err := sites.BeginCreateOrUpdateSlot(ctx, rg, "sdk-mysql-inapp", "staging", armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = slotPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	slotStatus, err := sites.GetMigrateMySQLStatusSlot(ctx, rg, "sdk-mysql-inapp", "staging", nil)
	require.NoError(t, err)
	require.NotNil(t, slotStatus.Properties.LocalMySQLEnabled)
	assert.False(t, *slotStatus.Properties.LocalMySQLEnabled, "the slot has no in-app database of its own")
}

// Moving a site's content into an Azure Files share is a declared 501.
//
//	PUT .../sites/{name}/migrate
func TestSDK_WebApps_MigrateStorage(t *testing.T) {
	t.Parallel()
	rg := "sdk-migrate-rg"
	ensureRG(t, rg)

	sites, err := armappservice.NewWebAppsClient(subscriptionID, &clientCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := sites.BeginCreateOrUpdate(ctx, rg, "sdk-migrate-site", armappservice.Site{
		Location: to.Ptr("eastus"),
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	options := armappservice.StorageMigrationOptions{
		Properties: &armappservice.StorageMigrationOptionsProperties{
			AzurefilesConnectionString: to.Ptr("DefaultEndpointsProtocol=https;AccountName=content"),
			AzurefilesShare:            to.Ptr("site-content"),
		},
	}
	_, err = sites.BeginMigrateStorage(ctx, "sdk-migrate-sub", rg, "sdk-migrate-site", options, nil)
	requireNotImplemented(t, err)

	_, err = sites.BeginMigrateStorage(ctx, "sdk-migrate-sub", rg, "sdk-migrate-absent", options, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ResourceNotFound")
}
