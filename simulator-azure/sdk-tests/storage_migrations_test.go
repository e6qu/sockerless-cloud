package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for a storage account's migrations and its point-in-time blob
// restore:
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/startAccountMigration
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/accountMigrations/{migrationName}
//	POST /subscriptions/{subscriptionId}/resourcegroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/hnsonmigration
//	POST /subscriptions/{subscriptionId}/resourcegroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/aborthnsonmigration
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Storage/storageAccounts/{accountName}/restoreBlobRanges

// A migration changes the account, not just a status document: the SKU it names
// is the SKU the account reports afterwards, and the hierarchical-namespace
// migration turns the namespace on.
func TestSDK_StorageAccount_Migrations(t *testing.T) {
	const rg, account = "storage-migration-rg", "storagemigrationacct"
	createStorageAccountForARM(t, rg, account)

	accounts, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Nothing has been migrated yet, which is the negative control for the read.
	_, err = accounts.GetCustomerInitiatedMigration(ctx, rg, account, armstorage.MigrationNameDefault, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No customer-initiated migration")

	before, err := accounts.GetProperties(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotNil(t, before.SKU)
	startingSKU := string(*before.SKU.Name)

	// A migration to the SKU the account is already on is refused: there is
	// nothing to migrate.
	_, err = accounts.BeginCustomerInitiatedMigration(ctx, rg, account, armstorage.AccountMigration{
		StorageAccountMigrationDetails: &armstorage.AccountMigrationProperties{
			TargetSKUName: to.Ptr(armstorage.SKUName(startingSKU)),
		},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already on SKU")

	poller, err := accounts.BeginCustomerInitiatedMigration(ctx, rg, account, armstorage.AccountMigration{
		StorageAccountMigrationDetails: &armstorage.AccountMigrationProperties{
			TargetSKUName: to.Ptr(armstorage.SKUNameStandardGRS),
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The account is on the target SKU — the migration moved it, rather than
	// recording that it would.
	after, err := accounts.GetProperties(ctx, rg, account, nil)
	require.NoError(t, err)
	assert.Equal(t, armstorage.SKUNameStandardGRS, *after.SKU.Name)

	migration, err := accounts.GetCustomerInitiatedMigration(ctx, rg, account, armstorage.MigrationNameDefault, nil)
	require.NoError(t, err)
	require.NotNil(t, migration.StorageAccountMigrationDetails)
	assert.Equal(t, armstorage.SKUNameStandardGRS, *migration.StorageAccountMigrationDetails.TargetSKUName)
	assert.EqualValues(t, "Complete", *migration.StorageAccountMigrationDetails.MigrationStatus)

	// A request type that is neither of the two the operation defines is
	// refused before anything is changed.
	_, err = accounts.BeginHierarchicalNamespaceMigration(ctx, rg, account, "SomethingElse", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HnsOnValidationRequest")

	// Aborting a migration nobody started is a conflict, not a success.
	_, err = accounts.BeginAbortHierarchicalNamespaceMigration(ctx, rg, account, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No hierarchical namespace migration is running")

	// A validation changes nothing, which is what makes it a validation.
	validate, err := accounts.BeginHierarchicalNamespaceMigration(ctx, rg, account, "HnsOnValidationRequest", nil)
	require.NoError(t, err)
	_, err = validate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	checked, err := accounts.GetProperties(ctx, rg, account, nil)
	require.NoError(t, err)
	assert.True(t, checked.Properties.IsHnsEnabled == nil || !*checked.Properties.IsHnsEnabled,
		"a validation must not turn the namespace on")

	// The hydration request does.
	hydrate, err := accounts.BeginHierarchicalNamespaceMigration(ctx, rg, account, "HnsOnHydrationRequest", nil)
	require.NoError(t, err)
	_, err = hydrate.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	migrated, err := accounts.GetProperties(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotNil(t, migrated.Properties.IsHnsEnabled)
	assert.True(t, *migrated.Properties.IsHnsEnabled)

	// And an account that already has one has nothing left to migrate.
	_, err = accounts.BeginHierarchicalNamespaceMigration(ctx, rg, account, "HnsOnHydrationRequest", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has a hierarchical namespace")
}

// A blob-range restore un-deletes the blobs the retention policy kept — the
// ones inside the ranges it names that were deleted after the instant it
// restores to — and leaves everything else where it is.
func TestSDK_StorageAccount_RestoreBlobRanges(t *testing.T) {
	const rg, account = "storage-restore-rg", "storagerestoreacct"
	createStorageAccountForARM(t, rg, account)

	accounts, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Without a restore policy there is nothing keeping deleted blobs, so a
	// restore has nothing to restore from and says so.
	_, err = accounts.BeginRestoreBlobRanges(ctx, rg, account, armstorage.BlobRestoreParameters{
		TimeToRestore: to.Ptr(time.Now().UTC().Add(-time.Hour)),
		BlobRanges:    []*armstorage.BlobRestoreRange{{StartRange: to.Ptr(""), EndRange: to.Ptr("")}},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Point-in-time restore is not enabled")

	// Turn on the delete-retention and restore policies the way a client does,
	// through the blob service properties.
	blobServices, err := armstorage.NewBlobServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = blobServices.SetServiceProperties(ctx, rg, account, armstorage.BlobServiceProperties{
		BlobServiceProperties: &armstorage.BlobServicePropertiesProperties{
			DeleteRetentionPolicy: &armstorage.DeleteRetentionPolicy{
				Enabled: to.Ptr(true), Days: to.Ptr[int32](7),
			},
			RestorePolicy: &armstorage.RestorePolicyProperties{
				Enabled: to.Ptr(true), Days: to.Ptr[int32](6),
			},
		},
	}, nil)
	require.NoError(t, err)

	// Write two blobs and delete one of them, so the restore has something real
	// to bring back and something real to leave alone.
	client := newBlobTestClient(t, account)

	// Blob soft delete is what keeps a deleted blob available to restore, and
	// the blob service's own properties are where it is set.
	_, err = client.ServiceClient().SetProperties(ctx, &service.SetPropertiesOptions{
		DeleteRetentionPolicy: &service.RetentionPolicy{
			Enabled: to.Ptr(true), Days: to.Ptr[int32](7),
		},
	})
	require.NoError(t, err)

	const container = "restored"
	_, err = client.CreateContainer(ctx, container, nil)
	require.NoError(t, err)
	for _, name := range []string{"keep.txt", "gone.txt"} {
		_, err = client.UploadBuffer(ctx, container, name, []byte("payload"), nil)
		require.NoError(t, err)
	}

	// The restore point sits after both writes and before the deletion, and a
	// blob's recorded times carry seconds, so the two are separated by one.
	time.Sleep(time.Second)
	restoreTo := time.Now().UTC()
	time.Sleep(time.Second)

	_, err = client.DeleteBlob(ctx, container, "gone.txt", nil)
	require.NoError(t, err)

	// A blob written after the restore point did not exist then, so the restore
	// must take it away again.
	_, err = client.UploadBuffer(ctx, container, "later.txt", []byte("after"), nil)
	require.NoError(t, err)

	// The deleted blob is gone from the container before the restore, which is
	// what makes the restore observable.
	_, err = client.DownloadStream(ctx, container, "gone.txt", nil)
	require.Error(t, err)

	poller, err := accounts.BeginRestoreBlobRanges(ctx, rg, account, armstorage.BlobRestoreParameters{
		TimeToRestore: to.Ptr(restoreTo),
		BlobRanges: []*armstorage.BlobRestoreRange{
			{StartRange: to.Ptr(container + "/"), EndRange: to.Ptr(container + "0")},
		},
	}, nil)
	require.NoError(t, err)
	restored, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, restored.RestoreID)
	assert.EqualValues(t, armstorage.BlobRestoreProgressStatusComplete, *restored.Status)

	// The blob is back, and reads its content — the retention policy kept it
	// and the restore un-deleted it.
	got, err := client.DownloadStream(ctx, container, "gone.txt", nil)
	require.NoError(t, err, "the restore must bring back the blob deleted after the restore point")
	got.Body.Close()

	// The one that was never deleted, and existed at the restore point, is
	// untouched.
	kept, err := client.DownloadStream(ctx, container, "keep.txt", nil)
	require.NoError(t, err)
	kept.Body.Close()

	// And the one written afterwards is gone, because it had not been written
	// at the instant the account was restored to.
	_, err = client.DownloadStream(ctx, container, "later.txt", nil)
	require.Error(t, err, "a blob written after the restore point must not survive the restore")
}
