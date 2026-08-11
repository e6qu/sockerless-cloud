package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aca_env_storages_test.go covers the managedEnvironments/{env}/storages
// sub-resource — the Azure Files mount definition an ACA app or job references
// by name to get a shared volume. The aca backend provisions these when a
// docker volume is bound into a container, so the whole create → get → list →
// delete cycle is exercised through the same ManagedEnvironmentsStoragesClient
// the backend and terraform-provider-azurerm drive.
//
//	PUT    /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{envName}/storages/{storageName}
//	GET    /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{envName}/storages/{storageName}
//	DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{envName}/storages/{storageName}
//	GET    /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/managedEnvironments/{envName}/storages
func TestSDK_ManagedEnvironmentsStorages_CRUD(t *testing.T) {
	rg := "aca-envstorage-rg"
	env := "envstorage-env"
	ensureRG(t, rg)
	ensureManagedEnv(t, rg, env)

	client, err := armappcontainers.NewManagedEnvironmentsStoragesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const storageName = "sdk-share"

	created, err := client.CreateOrUpdate(ctx, rg, env, storageName, armappcontainers.ManagedEnvironmentStorage{
		Properties: &armappcontainers.ManagedEnvironmentStorageProperties{
			AzureFile: &armappcontainers.AzureFileProperties{
				AccountName: to.Ptr("simstorageacct"),
				ShareName:   to.Ptr("simshare"),
				AccountKey:  to.Ptr("c2ltdWxhdG9yLWFjY291bnQta2V5"),
				AccessMode:  to.Ptr(armappcontainers.AccessModeReadWrite),
			},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Name)
	assert.Equal(t, storageName, *created.Name)
	require.NotNil(t, created.ID)
	assert.Contains(t, *created.ID, "/managedEnvironments/"+env+"/storages/"+storageName)
	require.NotNil(t, created.Type)
	assert.Equal(t, "Microsoft.App/managedEnvironments/storages", *created.Type)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.AzureFile)
	require.NotNil(t, created.Properties.AzureFile.ShareName)
	assert.Equal(t, "simshare", *created.Properties.AzureFile.ShareName)

	got, err := client.Get(ctx, rg, env, storageName, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.NotNil(t, got.Properties.AzureFile)
	require.NotNil(t, got.Properties.AzureFile.AccountName)
	assert.Equal(t, "simstorageacct", *got.Properties.AzureFile.AccountName)
	require.NotNil(t, got.Properties.AzureFile.AccessMode)
	assert.Equal(t, armappcontainers.AccessModeReadWrite, *got.Properties.AzureFile.AccessMode)

	list, err := client.List(ctx, rg, env, nil)
	require.NoError(t, err)
	found := false
	for _, s := range list.Value {
		if s != nil && s.Name != nil && *s.Name == storageName {
			found = true
			require.NotNil(t, s.Properties)
			require.NotNil(t, s.Properties.AzureFile)
			require.NotNil(t, s.Properties.AzureFile.ShareName)
			assert.Equal(t, "simshare", *s.Properties.AzureFile.ShareName)
		}
	}
	assert.True(t, found, "list must include the storage created above")

	_, err = client.Delete(ctx, rg, env, storageName, nil)
	require.NoError(t, err)

	_, err = client.Get(ctx, rg, env, storageName, nil)
	require.Error(t, err, "GET after DELETE must fail")

	afterList, err := client.List(ctx, rg, env, nil)
	require.NoError(t, err)
	for _, s := range afterList.Value {
		if s != nil && s.Name != nil {
			assert.NotEqual(t, storageName, *s.Name, "deleted storage must not be listed")
		}
	}
}

// TestSDK_ManagedEnvironmentsStorages_ParentValidation checks the sim rejects a
// storage write under an environment that does not exist, the way real ARM
// does, rather than silently creating an orphan row.
func TestSDK_ManagedEnvironmentsStorages_ParentValidation(t *testing.T) {
	rg := "aca-envstorage-rg"
	ensureRG(t, rg)

	client, err := armappcontainers.NewManagedEnvironmentsStoragesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdate(ctx, rg, "no-such-env", "orphan", armappcontainers.ManagedEnvironmentStorage{
		Properties: &armappcontainers.ManagedEnvironmentStorageProperties{
			AzureFile: &armappcontainers.AzureFileProperties{
				AccountName: to.Ptr("simstorageacct"),
				ShareName:   to.Ptr("simshare"),
				AccessMode:  to.Ptr(armappcontainers.AccessModeReadOnly),
			},
		},
	}, nil)
	require.Error(t, err, "storage write under a missing environment must fail")
}
