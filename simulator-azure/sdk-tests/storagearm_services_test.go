package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStorageARM_BlobServicesAndContainers exercises the blobServices service
// list and the blob-container management actions (update, legal hold,
// immutability policy lifecycle, lease, object-level WORM migration).
func TestStorageARM_BlobServicesAndContainers(t *testing.T) {
	rg := "storagearm-blob-rg"
	account := "storagearmblob"
	createStorageAccountForARM(t, rg, account)

	bsClient, err := armstorage.NewBlobServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	bsPager := bsClient.NewListPager(rg, account, nil)
	bsPage, err := bsPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, bsPage.Value)

	bcClient, err := armstorage.NewBlobContainersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	container := "worm-container"
	_, err = bcClient.Create(ctx, rg, account, container, armstorage.BlobContainer{
		ContainerProperties: &armstorage.ContainerProperties{PublicAccess: to.Ptr(armstorage.PublicAccessNone)},
	}, nil)
	require.NoError(t, err)

	updated, err := bcClient.Update(ctx, rg, account, container, armstorage.BlobContainer{
		ContainerProperties: &armstorage.ContainerProperties{
			Metadata: map[string]*string{"team": to.Ptr("ci")},
		},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.ContainerProperties)
	require.NotNil(t, updated.ContainerProperties.Metadata["team"])

	legal, err := bcClient.SetLegalHold(ctx, rg, account, container, armstorage.LegalHold{
		Tags: []*string{to.Ptr("tag1")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, legal.HasLegalHold)
	assert.True(t, *legal.HasLegalHold)
	_, err = bcClient.ClearLegalHold(ctx, rg, account, container, armstorage.LegalHold{
		Tags: []*string{to.Ptr("tag1")},
	}, nil)
	require.NoError(t, err)

	imm, err := bcClient.CreateOrUpdateImmutabilityPolicy(ctx, rg, account, container, &armstorage.BlobContainersClientCreateOrUpdateImmutabilityPolicyOptions{
		Parameters: &armstorage.ImmutabilityPolicy{
			Properties: &armstorage.ImmutabilityPolicyProperty{
				ImmutabilityPeriodSinceCreationInDays: to.Ptr(int32(5)),
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, imm.ETag)
	etag := *imm.ETag

	immGet, err := bcClient.GetImmutabilityPolicy(ctx, rg, account, container, nil)
	require.NoError(t, err)
	require.NotNil(t, immGet.Properties)

	extended, err := bcClient.ExtendImmutabilityPolicy(ctx, rg, account, container, etag, &armstorage.BlobContainersClientExtendImmutabilityPolicyOptions{
		Parameters: &armstorage.ImmutabilityPolicy{
			Properties: &armstorage.ImmutabilityPolicyProperty{ImmutabilityPeriodSinceCreationInDays: to.Ptr(int32(10))},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, extended.ETag)

	locked, err := bcClient.LockImmutabilityPolicy(ctx, rg, account, container, *extended.ETag, nil)
	require.NoError(t, err)
	require.NotNil(t, locked.Properties)
	require.NotNil(t, locked.Properties.State)
	assert.Equal(t, armstorage.ImmutabilityPolicyStateLocked, *locked.Properties.State)

	_, err = bcClient.DeleteImmutabilityPolicy(ctx, rg, account, container, *locked.ETag, nil)
	require.NoError(t, err)

	lease, err := bcClient.Lease(ctx, rg, account, container, &armstorage.BlobContainersClientLeaseOptions{
		Parameters: &armstorage.LeaseContainerRequest{
			Action:        to.Ptr(armstorage.LeaseContainerRequestActionAcquire),
			LeaseDuration: to.Ptr(int32(-1)),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, lease.LeaseID)

	wormPoller, err := bcClient.BeginObjectLevelWorm(ctx, rg, account, container, nil)
	require.NoError(t, err)
	_, err = wormPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestStorageARM_FileServicesAndShares exercises the fileServices service
// surface (list/set/get + service usages) and the file-share update/lease/restore
// actions.
func TestStorageARM_FileServicesAndShares(t *testing.T) {
	rg := "storagearm-file-rg"
	account := "storagearmfile"
	createStorageAccountForARM(t, rg, account)

	fsClient, err := armstorage.NewFileServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	list, err := fsClient.List(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, list.Value)

	_, err = fsClient.SetServiceProperties(ctx, rg, account, armstorage.FileServiceProperties{
		FileServiceProperties: &armstorage.FileServicePropertiesProperties{
			ShareDeleteRetentionPolicy: &armstorage.DeleteRetentionPolicy{
				Enabled: to.Ptr(true),
				Days:    to.Ptr(int32(7)),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = fsClient.GetServiceProperties(ctx, rg, account, nil)
	require.NoError(t, err)

	usagePager := fsClient.NewListServiceUsagesPager(rg, account, nil)
	_, err = usagePager.NextPage(ctx)
	require.NoError(t, err)
	_, err = fsClient.GetServiceUsage(ctx, rg, account, nil)
	require.NoError(t, err)

	shareClient, err := armstorage.NewFileSharesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	share := "arm-share"
	start := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	expiry := start.Add(24 * time.Hour)
	_, err = shareClient.Create(ctx, rg, account, share, armstorage.FileShare{
		FileShareProperties: &armstorage.FileShareProperties{
			ShareQuota: to.Ptr(int32(100)),
			SignedIdentifiers: []*armstorage.SignedIdentifier{{
				ID: to.Ptr("arm-policy"),
				AccessPolicy: &armstorage.AccessPolicy{
					StartTime:  &start,
					ExpiryTime: &expiry,
					Permission: to.Ptr("rwdl"),
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	upd, err := shareClient.Update(ctx, rg, account, share, armstorage.FileShare{
		FileShareProperties: &armstorage.FileShareProperties{ShareQuota: to.Ptr(int32(200))},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, upd.FileShareProperties)
	require.NotNil(t, upd.FileShareProperties.ShareQuota)
	assert.Equal(t, int32(200), *upd.FileShareProperties.ShareQuota)
	require.Len(t, upd.FileShareProperties.SignedIdentifiers, 1)
	require.NotNil(t, upd.FileShareProperties.SignedIdentifiers[0].ID)
	require.NotNil(t, upd.FileShareProperties.SignedIdentifiers[0].AccessPolicy)
	assert.Equal(t, "arm-policy", *upd.FileShareProperties.SignedIdentifiers[0].ID)
	assert.Equal(t, "rwdl", *upd.FileShareProperties.SignedIdentifiers[0].AccessPolicy.Permission)
	assert.Equal(t, start, *upd.FileShareProperties.SignedIdentifiers[0].AccessPolicy.StartTime)
	assert.Equal(t, expiry, *upd.FileShareProperties.SignedIdentifiers[0].AccessPolicy.ExpiryTime)

	leaseResp, err := shareClient.Lease(ctx, rg, account, share, &armstorage.FileSharesClientLeaseOptions{
		Parameters: &armstorage.LeaseShareRequest{
			Action:        to.Ptr(armstorage.LeaseShareActionAcquire),
			LeaseDuration: to.Ptr(int32(-1)),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, leaseResp.LeaseID)
}

// TestStorageARM_QueueAndTableServices exercises queueServices + storage queues
// and the tableServices service surface.
func TestStorageARM_QueueAndTableServices(t *testing.T) {
	rg := "storagearm-qt-rg"
	account := "storagearmqt"
	createStorageAccountForARM(t, rg, account)

	qsClient, err := armstorage.NewQueueServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	qsList, err := qsClient.List(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, qsList.Value)
	_, err = qsClient.SetServiceProperties(ctx, rg, account, armstorage.QueueServiceProperties{
		QueueServiceProperties: &armstorage.QueueServicePropertiesProperties{},
	}, nil)
	require.NoError(t, err)
	_, err = qsClient.GetServiceProperties(ctx, rg, account, nil)
	require.NoError(t, err)

	qClient, err := armstorage.NewQueueClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	queue := "armqueue"
	_, err = qClient.Create(ctx, rg, account, queue, armstorage.Queue{
		QueueProperties: &armstorage.QueueProperties{Metadata: map[string]*string{"env": to.Ptr("test")}},
	}, nil)
	require.NoError(t, err)
	_, err = qClient.Update(ctx, rg, account, queue, armstorage.Queue{
		QueueProperties: &armstorage.QueueProperties{Metadata: map[string]*string{"env": to.Ptr("prod")}},
	}, nil)
	require.NoError(t, err)
	qGet, err := qClient.Get(ctx, rg, account, queue, nil)
	require.NoError(t, err)
	require.NotNil(t, qGet.Name)
	qPager := qClient.NewListPager(rg, account, nil)
	qPage, err := qPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, qPage.Value)
	_, err = qClient.Delete(ctx, rg, account, queue, nil)
	require.NoError(t, err)

	tsClient, err := armstorage.NewTableServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	tsList, err := tsClient.List(ctx, rg, account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, tsList.Value)
	_, err = tsClient.SetServiceProperties(ctx, rg, account, armstorage.TableServiceProperties{
		TableServiceProperties: &armstorage.TableServicePropertiesProperties{},
	}, nil)
	require.NoError(t, err)
	_, err = tsClient.GetServiceProperties(ctx, rg, account, nil)
	require.NoError(t, err)
}
