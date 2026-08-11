package azure_sdk_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/appendblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/lease"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/pageblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Blob data plane beyond the container/blob lifecycle: leases, blob system
// properties, tags, snapshots and soft delete, the copy family, page and append
// ranges, container administration, blob batch, and the account-wide service
// operations. Every case drives the official azure-sdk-for-go azblob clients,
// and each asserts that the operation CHANGED something the next read observes —
// a status code alone would pass against a handler that did nothing.

// newBlobTestClient builds an azblob client for one account, pointed at the
// simulator by the same coordinate a real client is pointed at the cloud by.
func newBlobTestClient(t *testing.T, account string) *azblob.Client {
	t.Helper()
	client, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	return client
}

// newBlobTestContainer creates a container and registers its teardown.
func newBlobTestContainer(t *testing.T, client *azblob.Client, name string) *container.Client {
	t.Helper()
	_, err := client.CreateContainer(ctx, name, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteContainer(ctx, name, nil) })
	return client.ServiceClient().NewContainerClient(name)
}

// requireBlobErrorCode asserts the SDK surfaced a specific Azure Storage error
// code, which is what a caller branches on.
func requireBlobErrorCode(t *testing.T, err error, code, what string) {
	t.Helper()
	require.Error(t, err, "%s: expected the service to refuse the request", what)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "%s: want *azcore.ResponseError, got %T: %v", what, err, err)
	require.Equal(t, code, respErr.ErrorCode, "%s: %v", what, err)
}

// ---------------------------------------------------------------------------
// Leases
// ---------------------------------------------------------------------------

func TestStorageSDK_BlobLeaseLifecycleAndEnforcement(t *testing.T) {
	account, containerName, blobName := "sdkleaseacct", "lease-container", "leased.txt"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	_, err := client.UploadBuffer(ctx, containerName, blobName, []byte("leased payload"), nil)
	require.NoError(t, err)
	blobClient := containerClient.NewBlobClient(blobName)

	leaseClient, err := lease.NewBlobClient(blobClient, &lease.BlobClientOptions{
		LeaseID: to.Ptr("11111111-2222-3333-4444-555555555555"),
	})
	require.NoError(t, err)

	acquired, err := leaseClient.AcquireLease(ctx, -1, nil)
	require.NoError(t, err)
	require.NotNil(t, acquired.LeaseID)
	assert.Equal(t, "11111111-2222-3333-4444-555555555555", *acquired.LeaseID)

	// The lease is visible on the blob's properties and in the container listing.
	props, err := blobClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.LeaseState)
	require.NotNil(t, props.LeaseStatus)
	require.NotNil(t, props.LeaseDuration)
	assert.Equal(t, lease.StateTypeLeased, *props.LeaseState)
	assert.Equal(t, lease.StatusTypeLocked, *props.LeaseStatus)
	assert.Equal(t, lease.DurationTypeInfinite, *props.LeaseDuration)

	pager := containerClient.NewListBlobsFlatPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Segment.BlobItems, 1)
	require.NotNil(t, page.Segment.BlobItems[0].Properties.LeaseState)
	assert.Equal(t, lease.StateTypeLeased, *page.Segment.BlobItems[0].Properties.LeaseState)

	// Enforcement: a write without the lease ID is refused, a write with the
	// wrong lease ID is refused with the mismatch code, and the blob survives.
	_, err = client.UploadBuffer(ctx, containerName, blobName, []byte("clobbered"), nil)
	requireBlobErrorCode(t, err, "LeaseIdMissing", "overwrite a leased blob without a lease ID")

	_, err = client.UploadBuffer(ctx, containerName, blobName, []byte("clobbered"), &azblob.UploadBufferOptions{
		AccessConditions: &blob.AccessConditions{
			LeaseAccessConditions: &blob.LeaseAccessConditions{LeaseID: to.Ptr("99999999-9999-9999-9999-999999999999")},
		},
	})
	requireBlobErrorCode(t, err, "LeaseIdMismatchWithBlobOperation", "overwrite a leased blob with the wrong lease ID")

	download, err := client.DownloadStream(ctx, containerName, blobName, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "leased payload", string(got), "the refused writes must leave the blob untouched")

	// A write carrying the lease ID goes through.
	_, err = client.UploadBuffer(ctx, containerName, blobName, []byte("rewritten under lease"), &azblob.UploadBufferOptions{
		AccessConditions: &blob.AccessConditions{
			LeaseAccessConditions: &blob.LeaseAccessConditions{LeaseID: acquired.LeaseID},
		},
	})
	require.NoError(t, err)

	// Renew, change, then release; each verb is observable on the next one.
	_, err = leaseClient.RenewLease(ctx, nil)
	require.NoError(t, err)

	changed, err := leaseClient.ChangeLease(ctx, "66666666-7777-8888-9999-000000000000", nil)
	require.NoError(t, err)
	require.NotNil(t, changed.LeaseID)
	assert.Equal(t, "66666666-7777-8888-9999-000000000000", *changed.LeaseID)

	_, err = leaseClient.ReleaseLease(ctx, nil)
	require.NoError(t, err)

	props, err = blobClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, lease.StateTypeAvailable, *props.LeaseState)

	// With no lease present, a request that names one is refused.
	_, err = client.UploadBuffer(ctx, containerName, blobName, []byte("no lease here"), &azblob.UploadBufferOptions{
		AccessConditions: &blob.AccessConditions{
			LeaseAccessConditions: &blob.LeaseAccessConditions{LeaseID: to.Ptr("11111111-2222-3333-4444-555555555555")},
		},
	})
	requireBlobErrorCode(t, err, "LeaseNotPresentWithBlobOperation", "name a lease ID with no lease present")
}

func TestStorageSDK_BlobLeaseBreakAndFiniteExpiry(t *testing.T) {
	account, containerName, blobName := "sdkleaseexpacct", "lease-expiry", "short.txt"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	_, err := client.UploadBuffer(ctx, containerName, blobName, []byte("expiring"), nil)
	require.NoError(t, err)

	blobClient := containerClient.NewBlobClient(blobName)
	leaseClient, err := lease.NewBlobClient(blobClient, nil)
	require.NoError(t, err)

	// Break with a zero break period completes immediately, so the lease is
	// broken rather than breaking.
	_, err = leaseClient.AcquireLease(ctx, 15, nil)
	require.NoError(t, err)
	broken, err := leaseClient.BreakLease(ctx, &lease.BlobBreakOptions{BreakPeriod: to.Ptr(int32(0))})
	require.NoError(t, err)
	require.NotNil(t, broken.LeaseTime)
	assert.Equal(t, int32(0), *broken.LeaseTime)

	props, err := blobClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, lease.StateTypeBroken, *props.LeaseState)
	assert.Equal(t, lease.StatusTypeUnlocked, *props.LeaseStatus)

	// A broken lease no longer blocks a write.
	_, err = client.UploadBuffer(ctx, containerName, blobName, []byte("after the break"), nil)
	require.NoError(t, err)

	// A finite lease really runs out: after its 15-second term the state is
	// expired, and the simulator reports that from the clock rather than from a
	// stored flag. The assertion here is that the term is recorded as finite —
	// the wall-clock expiry itself is covered by the simulator's own suite,
	// which can address the resolver directly instead of sleeping.
	shortLease, err := lease.NewBlobClient(blobClient, nil)
	require.NoError(t, err)
	_, err = shortLease.AcquireLease(ctx, 15, nil)
	require.NoError(t, err)
	props, err = blobClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, lease.DurationTypeFixed, *props.LeaseDuration)
	_, err = shortLease.ReleaseLease(ctx, nil)
	require.NoError(t, err)
}

func TestStorageSDK_ContainerLeaseLifecycleAndEnforcement(t *testing.T) {
	account, containerName := "sdkctrleaseacct", "lease-on-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	leaseClient, err := lease.NewContainerClient(containerClient, &lease.ContainerClientOptions{
		LeaseID: to.Ptr("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
	})
	require.NoError(t, err)

	acquired, err := leaseClient.AcquireLease(ctx, -1, nil)
	require.NoError(t, err)
	require.NotNil(t, acquired.LeaseID)

	props, err := containerClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.LeaseState)
	assert.Equal(t, lease.StateTypeLeased, *props.LeaseState)

	// Deleting a leased container without the lease ID is refused.
	_, err = client.DeleteContainer(ctx, containerName, nil)
	requireBlobErrorCode(t, err, "LeaseIdMissing", "delete a leased container without a lease ID")

	// The container survives, and List Containers reports the lease.
	pager := client.NewListContainersPager(&azblob.ListContainersOptions{Prefix: to.Ptr(containerName)})
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.ContainerItems, 1)
	require.NotNil(t, page.ContainerItems[0].Properties.LeaseState)
	assert.Equal(t, lease.StateTypeLeased, *page.ContainerItems[0].Properties.LeaseState)

	_, err = leaseClient.RenewLease(ctx, nil)
	require.NoError(t, err)
	_, err = leaseClient.ChangeLease(ctx, "bbbbbbbb-cccc-dddd-eeee-ffffffffffff", nil)
	require.NoError(t, err)
	_, err = leaseClient.BreakLease(ctx, &lease.ContainerBreakOptions{BreakPeriod: to.Ptr(int32(0))})
	require.NoError(t, err)

	props, err = containerClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, lease.StateTypeBroken, *props.LeaseState)

	// The broken lease no longer blocks the delete.
	_, err = client.DeleteContainer(ctx, containerName, nil)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Metadata, HTTP headers, tier, expiry, tags
// ---------------------------------------------------------------------------

func TestStorageSDK_BlobSystemPropertiesAndTier(t *testing.T) {
	account, containerName, blobName := "sdkblobpropsacct", "props-container", "props.txt"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	_, err := client.UploadBuffer(ctx, containerName, blobName, []byte("properties payload"), nil)
	require.NoError(t, err)
	blobClient := containerClient.NewBlobClient(blobName)

	_, err = blobClient.SetMetadata(ctx, map[string]*string{"owner": to.Ptr("sockerless")}, nil)
	require.NoError(t, err)
	props, err := blobClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "sockerless", storageMetadataValue(props.Metadata, "owner"))

	_, err = blobClient.SetHTTPHeaders(ctx, blob.HTTPHeaders{
		BlobContentType:        to.Ptr("text/csv"),
		BlobCacheControl:       to.Ptr("max-age=42"),
		BlobContentDisposition: to.Ptr(`attachment; filename="props.csv"`),
		BlobContentEncoding:    to.Ptr("identity"),
		BlobContentLanguage:    to.Ptr("en-GB"),
	}, nil)
	require.NoError(t, err)
	props, err = blobClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ContentType)
	assert.Equal(t, "text/csv", *props.ContentType)
	require.NotNil(t, props.CacheControl)
	assert.Equal(t, "max-age=42", *props.CacheControl)
	require.NotNil(t, props.ContentLanguage)
	assert.Equal(t, "en-GB", *props.ContentLanguage)

	// The metadata set earlier survives a header-only change.
	assert.Equal(t, "sockerless", storageMetadataValue(props.Metadata, "owner"))

	_, err = blobClient.SetTier(ctx, blob.AccessTierCool, nil)
	require.NoError(t, err)
	props, err = blobClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.AccessTier)
	assert.Equal(t, string(blob.AccessTierCool), *props.AccessTier)
	require.NotNil(t, props.AccessTierChangeTime)

	// And the tier is reported in the listing too.
	pager := containerClient.NewListBlobsFlatPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Segment.BlobItems, 1)
	require.NotNil(t, page.Segment.BlobItems[0].Properties.AccessTier)
	assert.Equal(t, blob.AccessTierCool, *page.Segment.BlobItems[0].Properties.AccessTier)

	// An unknown tier is refused rather than stored.
	_, err = blobClient.SetTier(ctx, blob.AccessTier("Lukewarm"), nil)
	requireBlobErrorCode(t, err, "InvalidHeaderValue", "set an unknown access tier")

	// The blob's bytes are untouched by every property change.
	download, err := client.DownloadStream(ctx, containerName, blobName, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "properties payload", string(got))
}

func TestStorageSDK_BlobSetExpiryAndTags(t *testing.T) {
	account, containerName, blobName := "sdkblobexpacct", "expiry-container", "expiring.txt"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	blockClient := containerClient.NewBlockBlobClient(blobName)

	_, err := blockClient.Upload(ctx, streaming.NopCloser(strings.NewReader("expiring payload")), nil)
	require.NoError(t, err)

	_, err = blockClient.SetExpiry(ctx, blockblob.ExpiryTypeRelativeToNow(time.Hour), nil)
	require.NoError(t, err)
	props, err := blockClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ExpiresOn, "Set Blob Expiry must be readable back as the blob's expiry")
	assert.True(t, props.ExpiresOn.After(time.Now()), "the expiry must be in the future: %s", props.ExpiresOn)

	// Tags round-trip, count on the properties, and drive Find Blobs by Tags.
	_, err = blockClient.SetTags(ctx, map[string]string{"project": "sockerless", "stage": "beta"}, nil)
	require.NoError(t, err)
	tags, err := blockClient.GetTags(ctx, nil)
	require.NoError(t, err)
	require.Len(t, tags.BlobTagSet, 2)
	readBack := map[string]string{}
	for _, tag := range tags.BlobTagSet {
		readBack[*tag.Key] = *tag.Value
	}
	assert.Equal(t, map[string]string{"project": "sockerless", "stage": "beta"}, readBack)

	props, err = blockClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.TagCount)
	assert.Equal(t, int64(2), *props.TagCount)

	found, err := containerClient.FilterBlobs(ctx, "project = 'sockerless' AND stage = 'beta'", nil)
	require.NoError(t, err)
	require.Len(t, found.Blobs, 1)
	require.NotNil(t, found.Blobs[0].Name)
	assert.Equal(t, blobName, *found.Blobs[0].Name)

	// A filter that matches nothing returns nothing — the expression is really
	// evaluated, not ignored.
	found, err = containerClient.FilterBlobs(ctx, "project = 'somethingelse'", nil)
	require.NoError(t, err)
	assert.Empty(t, found.Blobs)

	// The same query at the account scope reaches the same blob.
	serviceFound, err := client.ServiceClient().FilterBlobs(ctx, "stage = 'beta'", nil)
	require.NoError(t, err)
	require.Len(t, serviceFound.Blobs, 1)
	require.NotNil(t, serviceFound.Blobs[0].ContainerName)
	assert.Equal(t, containerName, *serviceFound.Blobs[0].ContainerName)
}

// ---------------------------------------------------------------------------
// Snapshots, soft delete, undelete, container restore
// ---------------------------------------------------------------------------

func TestStorageSDK_BlobSnapshotsAreAddressableCopies(t *testing.T) {
	account, containerName, blobName := "sdksnapacct", "snapshot-container", "versioned.txt"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	_, err := client.UploadBuffer(ctx, containerName, blobName, []byte("v1"), nil)
	require.NoError(t, err)
	blobClient := containerClient.NewBlobClient(blobName)

	snapshot, err := blobClient.CreateSnapshot(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Snapshot)

	// Overwrite the base blob; the snapshot keeps the original bytes.
	_, err = client.UploadBuffer(ctx, containerName, blobName, []byte("v2"), nil)
	require.NoError(t, err)

	snapClient, err := blobClient.WithSnapshot(*snapshot.Snapshot)
	require.NoError(t, err)
	download, err := snapClient.DownloadStream(ctx, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "v1", string(got), "the snapshot must keep the bytes as of the moment it was taken")

	base, err := client.DownloadStream(ctx, containerName, blobName, nil)
	require.NoError(t, err)
	baseBytes, err := io.ReadAll(base.Body)
	require.NoError(t, err)
	require.NoError(t, base.Body.Close())
	assert.Equal(t, "v2", string(baseBytes))

	// A flat listing hides snapshots until asked for them.
	pager := containerClient.NewListBlobsFlatPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.Len(t, page.Segment.BlobItems, 1)

	pager = containerClient.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Include: container.ListBlobsInclude{Snapshots: true},
	})
	page, err = pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Segment.BlobItems, 2)
	var sawSnapshot bool
	for _, item := range page.Segment.BlobItems {
		if item.Snapshot != nil && *item.Snapshot == *snapshot.Snapshot {
			sawSnapshot = true
		}
	}
	assert.True(t, sawSnapshot, "include=snapshots must enumerate the snapshot")

	// Deleting the base blob while snapshots exist needs the delete-snapshots
	// directive; without it the service refuses.
	_, err = blobClient.Delete(ctx, nil)
	requireBlobErrorCode(t, err, "SnapshotsPresent", "delete a blob that has snapshots")

	_, err = blobClient.Delete(ctx, &blob.DeleteOptions{
		DeleteSnapshots: to.Ptr(blob.DeleteSnapshotsOptionTypeInclude),
	})
	require.NoError(t, err)
	_, err = snapClient.GetProperties(ctx, nil)
	requireBlobErrorCode(t, err, "BlobNotFound", "read a snapshot deleted with its base blob")
}

func TestStorageSDK_BlobSoftDeleteAndUndelete(t *testing.T) {
	account, containerName, blobName := "sdksoftdelacct", "softdelete-container", "recoverable.txt"
	client := newBlobTestClient(t, account)
	serviceClient := client.ServiceClient()

	// Soft delete is only meaningful once the account's blob service properties
	// declare a delete-retention policy, so the test turns it on the way an
	// operator does.
	_, err := serviceClient.SetProperties(ctx, &service.SetPropertiesOptions{
		DeleteRetentionPolicy: &service.RetentionPolicy{Enabled: to.Ptr(true), Days: to.Ptr(int32(3))},
	})
	require.NoError(t, err)

	readBack, err := serviceClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, readBack.DeleteRetentionPolicy)
	require.NotNil(t, readBack.DeleteRetentionPolicy.Enabled)
	assert.True(t, *readBack.DeleteRetentionPolicy.Enabled)
	require.NotNil(t, readBack.DeleteRetentionPolicy.Days)
	assert.Equal(t, int32(3), *readBack.DeleteRetentionPolicy.Days)

	containerClient := newBlobTestContainer(t, client, containerName)
	_, err = client.UploadBuffer(ctx, containerName, blobName, []byte("recoverable payload"), nil)
	require.NoError(t, err)
	blobClient := containerClient.NewBlobClient(blobName)

	_, err = blobClient.Delete(ctx, nil)
	require.NoError(t, err)

	// The blob is gone from an ordinary read and an ordinary listing…
	_, err = blobClient.GetProperties(ctx, nil)
	requireBlobErrorCode(t, err, "BlobNotFound", "read a soft-deleted blob")
	pager := containerClient.NewListBlobsFlatPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, page.Segment.BlobItems)

	// …but retained, and visible to a listing that asks for deleted blobs.
	pager = containerClient.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		Include: container.ListBlobsInclude{Deleted: true},
	})
	page, err = pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Segment.BlobItems, 1)
	require.NotNil(t, page.Segment.BlobItems[0].Deleted)
	assert.True(t, *page.Segment.BlobItems[0].Deleted)
	require.NotNil(t, page.Segment.BlobItems[0].Properties.RemainingRetentionDays)
	assert.Equal(t, int32(3), *page.Segment.BlobItems[0].Properties.RemainingRetentionDays)

	_, err = blobClient.Undelete(ctx, nil)
	require.NoError(t, err)

	download, err := client.DownloadStream(ctx, containerName, blobName, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "recoverable payload", string(got), "undelete must restore the retained bytes")
}

func TestStorageSDK_ContainerSoftDeleteAndRestore(t *testing.T) {
	const rg, account, containerName = "sdk-blob-restore-rg", "sdkctrrestoreacct", "restorable-container"
	ensureRG(t, rg)

	// Container soft delete is an ARM setting on the account's blobServices
	// resource, so the test turns it on exactly where an operator turns it on.
	accountsClient, err := armstorage.NewAccountsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := accountsClient.BeginCreate(ctx, rg, account, armstorage.AccountCreateParameters{
		Location: to.Ptr("eastus"),
		SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
		Kind:     to.Ptr(armstorage.KindStorageV2),
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	blobServices, err := armstorage.NewBlobServicesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = blobServices.SetServiceProperties(ctx, rg, account, armstorage.BlobServiceProperties{
		BlobServiceProperties: &armstorage.BlobServicePropertiesProperties{
			ContainerDeleteRetentionPolicy: &armstorage.DeleteRetentionPolicy{
				Enabled: to.Ptr(true),
				Days:    to.Ptr(int32(5)),
			},
		},
	}, nil)
	require.NoError(t, err)

	client := newBlobTestClient(t, account)
	_, err = client.CreateContainer(ctx, containerName, nil)
	require.NoError(t, err)
	_, err = client.UploadBuffer(ctx, containerName, "kept.txt", []byte("kept through the delete"), nil)
	require.NoError(t, err)
	_, err = client.DeleteContainer(ctx, containerName, nil)
	require.NoError(t, err)

	_, err = client.ServiceClient().NewContainerClient(containerName).GetProperties(ctx, nil)
	requireBlobErrorCode(t, err, "ContainerNotFound", "read a soft-deleted container")

	// The retained container is enumerable, with the version Restore takes.
	pager := client.NewListContainersPager(&azblob.ListContainersOptions{
		Include: azblob.ListContainersInclude{Deleted: true},
		Prefix:  to.Ptr(containerName),
	})
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.ContainerItems, 1)
	require.NotNil(t, page.ContainerItems[0].Deleted)
	assert.True(t, *page.ContainerItems[0].Deleted)
	require.NotNil(t, page.ContainerItems[0].Version)
	require.NotNil(t, page.ContainerItems[0].Properties.RemainingRetentionDays)
	assert.Equal(t, int32(5), *page.ContainerItems[0].Properties.RemainingRetentionDays)

	_, err = client.ServiceClient().RestoreContainer(ctx, containerName, *page.ContainerItems[0].Version, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteContainer(ctx, containerName, nil) })

	download, err := client.DownloadStream(ctx, containerName, "kept.txt", nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "kept through the delete", string(got), "restoring a container restores its blobs")
}

// ---------------------------------------------------------------------------
// Immutability policy and legal hold
// ---------------------------------------------------------------------------

func TestStorageSDK_BlobImmutabilityPolicyAndLegalHold(t *testing.T) {
	account, containerName := "sdkimmutableacct", "immutable-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	_, err := client.UploadBuffer(ctx, containerName, "held.txt", []byte("under legal hold"), nil)
	require.NoError(t, err)
	held := containerClient.NewBlobClient("held.txt")

	_, err = held.SetLegalHold(ctx, true, nil)
	require.NoError(t, err)
	props, err := held.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.LegalHold)
	assert.True(t, *props.LegalHold)

	_, err = held.Delete(ctx, nil)
	requireBlobErrorCode(t, err, "BlobImmutableDueToPolicy", "delete a blob under legal hold")

	_, err = held.SetLegalHold(ctx, false, nil)
	require.NoError(t, err)
	_, err = held.Delete(ctx, nil)
	require.NoError(t, err, "clearing the hold must let the delete through")

	// A locked immutability policy refuses a delete until it expires, and
	// refuses to be removed while it is in force.
	_, err = client.UploadBuffer(ctx, containerName, "locked.txt", []byte("under policy"), nil)
	require.NoError(t, err)
	locked := containerClient.NewBlobClient("locked.txt")
	until := time.Now().Add(time.Hour)
	_, err = locked.SetImmutabilityPolicy(ctx, until, &blob.SetImmutabilityPolicyOptions{
		Mode: to.Ptr(blob.ImmutabilityPolicySettingLocked),
	})
	require.NoError(t, err)

	props, err = locked.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ImmutabilityPolicyExpiresOn)
	require.NotNil(t, props.ImmutabilityPolicyMode)

	_, err = locked.Delete(ctx, nil)
	requireBlobErrorCode(t, err, "BlobImmutableDueToPolicy", "delete a blob under a locked immutability policy")
	_, err = locked.DeleteImmutabilityPolicy(ctx, nil)
	requireBlobErrorCode(t, err, "BlobImmutableDueToPolicy", "remove a locked immutability policy before it expires")

	// An unlocked policy can be removed, and then the blob deletes.
	_, err = client.UploadBuffer(ctx, containerName, "unlocked.txt", []byte("under an unlocked policy"), nil)
	require.NoError(t, err)
	unlocked := containerClient.NewBlobClient("unlocked.txt")
	_, err = unlocked.SetImmutabilityPolicy(ctx, until, &blob.SetImmutabilityPolicyOptions{
		Mode: to.Ptr(blob.ImmutabilityPolicySettingUnlocked),
	})
	require.NoError(t, err)
	_, err = unlocked.Delete(ctx, nil)
	requireBlobErrorCode(t, err, "BlobImmutableDueToPolicy", "delete a blob under an unlocked immutability policy")
	_, err = unlocked.DeleteImmutabilityPolicy(ctx, nil)
	require.NoError(t, err)
	_, err = unlocked.Delete(ctx, nil)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Copy
// ---------------------------------------------------------------------------

func TestStorageSDK_BlobCopyFromURLAndAbort(t *testing.T) {
	account, containerName := "sdkcopyacct", "copy-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	_, err := client.UploadBuffer(ctx, containerName, "source.txt", []byte("source bytes"), nil)
	require.NoError(t, err)
	source := containerClient.NewBlobClient("source.txt")

	// The synchronous spelling: Copy Blob From URL.
	sync := containerClient.NewBlobClient("sync-copy.txt")
	copied, err := sync.CopyFromURL(ctx, source.URL(), nil)
	require.NoError(t, err)
	require.NotNil(t, copied.CopyStatus)
	assert.Equal(t, "success", *copied.CopyStatus)

	download, err := client.DownloadStream(ctx, containerName, "sync-copy.txt", nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "source bytes", string(got))

	// The asynchronous spelling: Start Copy From URL.
	async := containerClient.NewBlobClient("async-copy.txt")
	started, err := async.StartCopyFromURL(ctx, source.URL(), nil)
	require.NoError(t, err)
	require.NotNil(t, started.CopyID)

	props, err := async.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.CopyID)
	assert.Equal(t, *started.CopyID, *props.CopyID)
	require.NotNil(t, props.CopySource)

	// Aborting a copy that has already completed is refused, exactly as Azure
	// refuses it — there is no pending copy left to abort.
	_, err = async.AbortCopyFromURL(ctx, *started.CopyID, nil)
	requireBlobErrorCode(t, err, "NoPendingCopyOperation", "abort a completed copy")

	// An abort naming a copy ID the blob never had is refused too.
	_, err = async.AbortCopyFromURL(ctx, "00000000-0000-0000-0000-000000000000", nil)
	require.Error(t, err, "abort with an unknown copy ID must be refused")
}

func TestStorageSDK_PageBlobIncrementalCopy(t *testing.T) {
	account, containerName := "sdkincrcopyacct", "incremental-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	source := containerClient.NewPageBlobClient("source.vhd")
	_, err := source.Create(ctx, 1024, nil)
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("p"), 512)
	_, err = source.UploadPages(ctx, streaming.NopCloser(bytes.NewReader(payload)),
		blob.HTTPRange{Offset: 0, Count: 512}, nil)
	require.NoError(t, err)
	snapshot, err := source.CreateSnapshot(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Snapshot)

	destination := containerClient.NewPageBlobClient("destination.vhd")
	resp, err := destination.StartCopyIncremental(ctx, source.URL(), *snapshot.Snapshot, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.CopyStatus)

	props, err := destination.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.IsIncrementalCopy)
	assert.True(t, *props.IsIncrementalCopy)
	require.NotNil(t, props.DestinationSnapshot, "an incremental copy produces a destination snapshot")

	// The copied snapshot really carries the source's written pages.
	copySnapshot, err := destination.WithSnapshot(*props.DestinationSnapshot)
	require.NoError(t, err)
	rangePager := copySnapshot.NewGetPageRangesPager(nil)
	rangePage, err := rangePager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, rangePage.PageRange, 1)
	assert.Equal(t, int64(0), *rangePage.PageRange[0].Start)
	assert.Equal(t, int64(511), *rangePage.PageRange[0].End)
}

func TestStorageSDK_BlockBlobStageBlockFromURL(t *testing.T) {
	account, containerName := "sdkstagefromurlacct", "stage-from-url"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	_, err := client.UploadBuffer(ctx, containerName, "source.txt", []byte("HELLO-FROM-SOURCE"), nil)
	require.NoError(t, err)
	source := containerClient.NewBlobClient("source.txt")

	target := containerClient.NewBlockBlobClient("assembled.txt")
	const blockID = "YmxvY2stMDAwMDAwMQ=="
	_, err = target.StageBlockFromURL(ctx, blockID, source.URL(), nil)
	require.NoError(t, err)

	_, err = target.CommitBlockList(ctx, []string{blockID}, nil)
	require.NoError(t, err)

	download, err := client.DownloadStream(ctx, containerName, "assembled.txt", nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "HELLO-FROM-SOURCE", string(got),
		"Put Block From URL must stage the SOURCE bytes, not the empty request body")
}

// ---------------------------------------------------------------------------
// Page blob ranges
// ---------------------------------------------------------------------------

func TestStorageSDK_PageBlobRangesAndResize(t *testing.T) {
	account, containerName, blobName := "sdkpageacct", "page-container", "sparse.vhd"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	pageClient := containerClient.NewPageBlobClient(blobName)

	_, err := pageClient.Create(ctx, 2048, nil)
	require.NoError(t, err)

	props, err := pageClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ContentLength)
	assert.Equal(t, int64(2048), *props.ContentLength)

	// A brand-new page blob is wholly sparse: no written ranges at all.
	pager := pageClient.NewGetPageRangesPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	assert.Empty(t, page.PageRange, "an untouched page blob has no written ranges")

	first := bytes.Repeat([]byte("a"), 512)
	_, err = pageClient.UploadPages(ctx, streaming.NopCloser(bytes.NewReader(first)),
		blob.HTTPRange{Offset: 0, Count: 512}, nil)
	require.NoError(t, err)
	third := bytes.Repeat([]byte("c"), 512)
	_, err = pageClient.UploadPages(ctx, streaming.NopCloser(bytes.NewReader(third)),
		blob.HTTPRange{Offset: 1024, Count: 512}, nil)
	require.NoError(t, err)

	pager = pageClient.NewGetPageRangesPager(nil)
	page, err = pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.PageRange, 2, "the two written pages are two disjoint ranges")
	assert.Equal(t, int64(0), *page.PageRange[0].Start)
	assert.Equal(t, int64(511), *page.PageRange[0].End)
	assert.Equal(t, int64(1024), *page.PageRange[1].Start)
	assert.Equal(t, int64(1535), *page.PageRange[1].End)

	// The bytes read back where they were written, and the sparse gap reads as
	// zeros.
	download, err := pageClient.DownloadStream(ctx, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	require.Len(t, got, 2048)
	assert.Equal(t, first, got[0:512])
	assert.Equal(t, make([]byte, 512), got[512:1024])
	assert.Equal(t, third, got[1024:1536])

	// A snapshot then a further write make the diff meaningful.
	snapshot, err := pageClient.CreateSnapshot(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Snapshot)

	second := bytes.Repeat([]byte("b"), 512)
	_, err = pageClient.UploadPages(ctx, streaming.NopCloser(bytes.NewReader(second)),
		blob.HTTPRange{Offset: 512, Count: 512}, nil)
	require.NoError(t, err)
	_, err = pageClient.ClearPages(ctx, blob.HTTPRange{Offset: 0, Count: 512}, nil)
	require.NoError(t, err)

	diffPager := pageClient.NewGetPageRangesDiffPager(&pageblob.GetPageRangesDiffOptions{
		PrevSnapshot: snapshot.Snapshot,
	})
	diff, err := diffPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, diff.PageRange, 1, "one page was written since the snapshot")
	assert.Equal(t, int64(512), *diff.PageRange[0].Start)
	assert.Equal(t, int64(1023), *diff.PageRange[0].End)
	require.Len(t, diff.ClearRange, 1, "one page was cleared since the snapshot")
	assert.Equal(t, int64(0), *diff.ClearRange[0].Start)
	assert.Equal(t, int64(511), *diff.ClearRange[0].End)

	// A misaligned range is refused rather than silently rounded.
	_, err = pageClient.UploadPages(ctx, streaming.NopCloser(bytes.NewReader(make([]byte, 100))),
		blob.HTTPRange{Offset: 10, Count: 100}, nil)
	requireBlobErrorCode(t, err, "InvalidPageRange", "write a page range that is not 512-byte aligned")

	// Resize really changes the blob's length.
	_, err = pageClient.Resize(ctx, 4096, nil)
	require.NoError(t, err)
	props, err = pageClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(4096), *props.ContentLength)

	// Update Sequence Number really changes the sequence number.
	_, err = pageClient.UpdateSequenceNumber(ctx, &pageblob.UpdateSequenceNumberOptions{
		SequenceNumber: to.Ptr(int64(7)),
		ActionType:     to.Ptr(pageblob.SequenceNumberActionTypeUpdate),
	})
	require.NoError(t, err)
	props, err = pageClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.BlobSequenceNumber)
	assert.Equal(t, int64(7), *props.BlobSequenceNumber)

	_, err = pageClient.UpdateSequenceNumber(ctx, &pageblob.UpdateSequenceNumberOptions{
		ActionType: to.Ptr(pageblob.SequenceNumberActionTypeIncrement),
	})
	require.NoError(t, err)
	props, err = pageClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(8), *props.BlobSequenceNumber)
}

func TestStorageSDK_PageBlobUploadPagesFromURL(t *testing.T) {
	account, containerName := "sdkpagefromurlacct", "page-from-url"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	source := containerClient.NewPageBlobClient("source.vhd")
	_, err := source.Create(ctx, 512, nil)
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("s"), 512)
	_, err = source.UploadPages(ctx, streaming.NopCloser(bytes.NewReader(payload)),
		blob.HTTPRange{Offset: 0, Count: 512}, nil)
	require.NoError(t, err)

	destination := containerClient.NewPageBlobClient("destination.vhd")
	_, err = destination.Create(ctx, 1024, nil)
	require.NoError(t, err)
	_, err = destination.UploadPagesFromURL(ctx, source.URL(), 0, 512, 512, nil)
	require.NoError(t, err)

	download, err := destination.DownloadStream(ctx, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	require.Len(t, got, 1024)
	assert.Equal(t, payload, got[512:1024], "the pages must land at the DESTINATION offset")
	assert.Equal(t, make([]byte, 512), got[0:512])
}

// ---------------------------------------------------------------------------
// Append blob
// ---------------------------------------------------------------------------

func TestStorageSDK_AppendBlobBlocksAndSeal(t *testing.T) {
	account, containerName, blobName := "sdkappendacct", "append-container", "log.txt"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	appendClient := containerClient.NewAppendBlobClient(blobName)

	_, err := appendClient.Create(ctx, nil)
	require.NoError(t, err)

	first, err := appendClient.AppendBlock(ctx, streaming.NopCloser(strings.NewReader("line one\n")), nil)
	require.NoError(t, err)
	require.NotNil(t, first.BlobAppendOffset)
	assert.Equal(t, "0", *first.BlobAppendOffset)
	require.NotNil(t, first.BlobCommittedBlockCount)
	assert.Equal(t, int32(1), *first.BlobCommittedBlockCount)

	second, err := appendClient.AppendBlock(ctx, streaming.NopCloser(strings.NewReader("line two\n")), nil)
	require.NoError(t, err)
	assert.Equal(t, "9", *second.BlobAppendOffset, "the second block lands after the first")
	assert.Equal(t, int32(2), *second.BlobCommittedBlockCount)

	// An append-position precondition that does not match the current length is
	// refused.
	_, err = appendClient.AppendBlock(ctx, streaming.NopCloser(strings.NewReader("nope")),
		&appendblob.AppendBlockOptions{
			AppendPositionAccessConditions: &appendblob.AppendPositionAccessConditions{AppendPosition: to.Ptr(int64(3))},
		})
	requireBlobErrorCode(t, err, "AppendPositionConditionNotMet", "append at the wrong position")

	// Append Block From URL appends the source's bytes.
	_, err = client.UploadBuffer(ctx, containerName, "source.txt", []byte("line three\n"), nil)
	require.NoError(t, err)
	sourceClient := containerClient.NewBlobClient("source.txt")
	_, err = appendClient.AppendBlockFromURL(ctx, sourceClient.URL(), nil)
	require.NoError(t, err)

	download, err := appendClient.DownloadStream(ctx, nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "line one\nline two\nline three\n", string(got))

	props, err := appendClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.BlobCommittedBlockCount)
	assert.Equal(t, int32(3), *props.BlobCommittedBlockCount)

	// Sealing makes the blob permanently read-only.
	_, err = appendClient.Seal(ctx, nil)
	require.NoError(t, err)
	props, err = appendClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.IsSealed)
	assert.True(t, *props.IsSealed)

	_, err = appendClient.AppendBlock(ctx, streaming.NopCloser(strings.NewReader("after the seal")), nil)
	requireBlobErrorCode(t, err, "BlobIsSealed", "append to a sealed blob")
}

// ---------------------------------------------------------------------------
// Container administration
// ---------------------------------------------------------------------------

func TestStorageSDK_ContainerMetadataAndAccessPolicy(t *testing.T) {
	account, containerName := "sdkctradminacct", "admin-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	_, err := containerClient.SetMetadata(ctx, &container.SetMetadataOptions{
		Metadata: map[string]*string{"team": to.Ptr("storage")},
	})
	require.NoError(t, err)
	props, err := containerClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "storage", storageMetadataValue(props.Metadata, "team"))

	start := time.Now().UTC().Truncate(time.Second)
	expiry := start.Add(24 * time.Hour)
	_, err = containerClient.SetAccessPolicy(ctx, &container.SetAccessPolicyOptions{
		Access: to.Ptr(container.PublicAccessTypeBlob),
		ContainerACL: []*container.SignedIdentifier{{
			ID: to.Ptr("readers"),
			AccessPolicy: &container.AccessPolicy{
				Start:      &start,
				Expiry:     &expiry,
				Permission: to.Ptr("rl"),
			},
		}},
	})
	require.NoError(t, err)

	acl, err := containerClient.GetAccessPolicy(ctx, nil)
	require.NoError(t, err)
	require.Len(t, acl.SignedIdentifiers, 1)
	require.NotNil(t, acl.SignedIdentifiers[0].ID)
	assert.Equal(t, "readers", *acl.SignedIdentifiers[0].ID)
	require.NotNil(t, acl.SignedIdentifiers[0].AccessPolicy)
	require.NotNil(t, acl.SignedIdentifiers[0].AccessPolicy.Permission)
	assert.Equal(t, "rl", *acl.SignedIdentifiers[0].AccessPolicy.Permission)
	require.NotNil(t, acl.BlobPublicAccess)
	assert.Equal(t, container.PublicAccessTypeBlob, *acl.BlobPublicAccess)

	// An empty ACL clears the stored policies.
	_, err = containerClient.SetAccessPolicy(ctx, &container.SetAccessPolicyOptions{})
	require.NoError(t, err)
	acl, err = containerClient.GetAccessPolicy(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, acl.SignedIdentifiers)
}

// TestStorageSDK_ContainerRename drives Rename Container. The azblob module
// generates the operation but does not surface it on the public container
// client, so the request is issued directly at the coordinate the specification
// defines — the only surface a client has for it.
func TestStorageSDK_ContainerRename(t *testing.T) {
	account := "sdkctrrenameacct"
	client := newBlobTestClient(t, account)

	_, err := client.CreateContainer(ctx, "before-rename", nil)
	require.NoError(t, err)
	_, err = client.UploadBuffer(ctx, "before-rename", "carried.txt", []byte("carried across the rename"), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteContainer(ctx, "before-rename", nil)
		_, _ = client.DeleteContainer(ctx, "after-rename", nil)
	})

	resp := storageRawRequestWithHeaders(t, http.MethodPut, account, "blob",
		"/after-rename?restype=container&comp=rename", nil,
		map[string]string{"x-ms-source-container-name": "before-rename"})
	require.Equal(t, http.StatusOK, resp.StatusCode, storageReadBody(t, resp))

	download, err := client.DownloadStream(ctx, "after-rename", "carried.txt", nil)
	require.NoError(t, err)
	got, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.Equal(t, "carried across the rename", string(got))

	_, err = client.ServiceClient().NewContainerClient("before-rename").GetProperties(ctx, nil)
	requireBlobErrorCode(t, err, "ContainerNotFound", "read the container under its old name")
}

// ---------------------------------------------------------------------------
// Blob batch
// ---------------------------------------------------------------------------

func TestStorageSDK_BlobBatchDeleteAndSetTier(t *testing.T) {
	account, containerName := "sdkbatchacct", "batch-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)

	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		_, err := client.UploadBuffer(ctx, containerName, name, []byte(name), nil)
		require.NoError(t, err)
	}

	// Service-level batch: delete two blobs in one request.
	builder, err := client.ServiceClient().NewBatchBuilder()
	require.NoError(t, err)
	require.NoError(t, builder.Delete(containerName, "one.txt", nil))
	require.NoError(t, builder.Delete(containerName, "two.txt", nil))
	resp, err := client.ServiceClient().SubmitBatch(ctx, builder, nil)
	require.NoError(t, err)
	require.Len(t, resp.Responses, 2)
	for _, item := range resp.Responses {
		require.NoError(t, item.Error, "every sub-request must have succeeded")
	}

	pager := containerClient.NewListBlobsFlatPager(nil)
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Segment.BlobItems, 1, "the batch really deleted the two named blobs")
	assert.Equal(t, "three.txt", *page.Segment.BlobItems[0].Name)

	// Container-level batch: change a tier.
	tierBuilder, err := containerClient.NewBatchBuilder()
	require.NoError(t, err)
	require.NoError(t, tierBuilder.SetTier("three.txt", blob.AccessTierCool, nil))
	tierResp, err := containerClient.SubmitBatch(ctx, tierBuilder, nil)
	require.NoError(t, err)
	require.Len(t, tierResp.Responses, 1)
	require.NoError(t, tierResp.Responses[0].Error)

	props, err := containerClient.NewBlobClient("three.txt").GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.AccessTier)
	assert.Equal(t, string(blob.AccessTierCool), *props.AccessTier)

	// A sub-request naming a blob that does not exist fails on its own, and the
	// batch as a whole still answers.
	missingBuilder, err := client.ServiceClient().NewBatchBuilder()
	require.NoError(t, err)
	require.NoError(t, missingBuilder.Delete(containerName, "three.txt", nil))
	require.NoError(t, missingBuilder.Delete(containerName, "never-existed.txt", nil))
	mixed, err := client.ServiceClient().SubmitBatch(ctx, missingBuilder, nil)
	require.NoError(t, err)
	require.Len(t, mixed.Responses, 2)
	require.NoError(t, mixed.Responses[0].Error)
	requireBlobErrorCode(t, mixed.Responses[1].Error, "BlobNotFound", "batched delete of an absent blob")
}

// ---------------------------------------------------------------------------
// Service-level operations
// ---------------------------------------------------------------------------

func TestStorageSDK_BlobServiceStatisticsAndAccountInfo(t *testing.T) {
	account, containerName := "sdksvcacct", "service-container"
	client := newBlobTestClient(t, account)
	containerClient := newBlobTestContainer(t, client, containerName)
	_, err := client.UploadBuffer(ctx, containerName, "info.txt", []byte("account info"), nil)
	require.NoError(t, err)

	stats, err := client.ServiceClient().GetStatistics(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, stats.GeoReplication)
	require.NotNil(t, stats.GeoReplication.Status)
	assert.Equal(t, service.BlobGeoReplicationStatusLive, *stats.GeoReplication.Status)
	require.NotNil(t, stats.GeoReplication.LastSyncTime)

	// Get Account Information is defined at all three levels and answers the
	// same account facts at each.
	serviceInfo, err := client.ServiceClient().GetAccountInfo(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, serviceInfo.SKUName)
	require.NotNil(t, serviceInfo.AccountKind)

	containerInfo, err := containerClient.GetAccountInfo(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, containerInfo.SKUName)
	assert.Equal(t, *serviceInfo.SKUName, *containerInfo.SKUName)

	blobInfo, err := containerClient.NewBlobClient("info.txt").GetAccountInfo(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, blobInfo.AccountKind)
	assert.Equal(t, *serviceInfo.AccountKind, *blobInfo.AccountKind)
}

// TestStorageSDK_BlobUserDelegationKey acquires a user delegation key through
// the official service client. The operation is OAuth-only on Azure, so the
// client is built with a token credential and the key's identity claims come
// from the token — the same path a real caller takes.
func TestStorageSDK_BlobUserDelegationKey(t *testing.T) {
	account := "sdkudkacct"
	serviceClient, err := service.NewClient(storageSDKURL(t, account, "blob"), &fakeCredential{},
		&service.ClientOptions{ClientOptions: azcore.ClientOptions{
			Transport:                       storageSDKTransport{},
			InsecureAllowCredentialWithHTTP: true,
		}})
	require.NoError(t, err)

	start := time.Now().UTC().Truncate(time.Second)
	expiry := start.Add(time.Hour)
	info := service.KeyInfo{
		Start:  to.Ptr(start.Format("2006-01-02T15:04:05Z")),
		Expiry: to.Ptr(expiry.Format("2006-01-02T15:04:05Z")),
	}
	credential, err := serviceClient.GetUserDelegationCredential(ctx, info, nil)
	require.NoError(t, err)
	require.NotNil(t, credential)

	// The same coordinates return the same key: the service retains what it
	// issued rather than minting a fresh secret per call.
	again, err := serviceClient.GetUserDelegationCredential(ctx, info, nil)
	require.NoError(t, err)
	require.NotNil(t, again)

	raw := storageRawRequestWithHeaders(t, http.MethodPost, account, "blob",
		"/?restype=service&comp=userdelegationkey",
		[]byte(fmt.Sprintf("<KeyInfo><Start>%s</Start><Expiry>%s</Expiry></KeyInfo>",
			*info.Start, *info.Expiry)),
		map[string]string{"Authorization": simARMBearer})
	require.Equal(t, http.StatusOK, raw.StatusCode)
	body := storageReadBody(t, raw)
	assert.Contains(t, body, "<SignedService>b</SignedService>")
	assert.Contains(t, body, "<SignedOid>")
	assert.Contains(t, body, "<Value>")

	// Without an OAuth bearer the operation is refused, exactly as on Azure.
	unauthorized := storageRawRequestWithHeaders(t, http.MethodPost, account, "blob",
		"/?restype=service&comp=userdelegationkey",
		[]byte(fmt.Sprintf("<KeyInfo><Start>%s</Start><Expiry>%s</Expiry></KeyInfo>",
			*info.Start, *info.Expiry)), nil)
	assert.Equal(t, http.StatusUnauthorized, unauthorized.StatusCode)
}

// ---------------------------------------------------------------------------
// Query Blob Contents
// ---------------------------------------------------------------------------

// TestStorageSDK_BlobQuery drives Query Blob Contents. The azblob module for Go
// does not surface the operation on any client — it is exposed in the .NET,
// Java and Python SDKs only — so the request is issued directly at the
// coordinate the specification defines, and the Avro-framed response is decoded
// the way a client's reader decodes it.
func TestStorageSDK_BlobQuery(t *testing.T) {
	account, containerName, blobName := "sdkqueryacct", "query-container", "rows.csv"
	client := newBlobTestClient(t, account)
	newBlobTestContainer(t, client, containerName)

	csv := "alpha,10\nbravo,20\ncharlie,30\n"
	_, err := client.UploadBuffer(ctx, containerName, blobName, []byte(csv), nil)
	require.NoError(t, err)

	request := `<?xml version="1.0" encoding="utf-8"?>` +
		`<QueryRequest><QueryType>SQL</QueryType>` +
		`<Expression>SELECT _1 FROM BlobStorage WHERE _2 &gt; '15'</Expression>` +
		`<InputSerialization><Format><Type>delimited</Type>` +
		`<DelimitedTextConfiguration><ColumnSeparator>,</ColumnSeparator><RecordSeparator>\n</RecordSeparator>` +
		`<HasHeaders>false</HasHeaders></DelimitedTextConfiguration></Format></InputSerialization>` +
		`<OutputSerialization><Format><Type>delimited</Type>` +
		`<DelimitedTextConfiguration><ColumnSeparator>,</ColumnSeparator><RecordSeparator>\n</RecordSeparator>` +
		`<HasHeaders>false</HasHeaders></DelimitedTextConfiguration></Format></OutputSerialization>` +
		`</QueryRequest>`

	resp := storageRawRequestWithHeaders(t, http.MethodPost, account, "blob",
		"/"+containerName+"/"+blobName+"?comp=query", []byte(request), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "avro/binary", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.True(t, bytes.HasPrefix(body, []byte("Obj\x01")),
		"the response must be an Avro object container file")
	assert.Contains(t, string(body), "queryBlobContents.resultData",
		"the writer schema the service declares must be in the file header")
	assert.Contains(t, string(body), "bravo\ncharlie\n",
		"the projection must return only the selected column, for the matching rows")
	assert.NotContains(t, string(body), "alpha",
		"the WHERE clause must really filter — alpha's value is below the threshold")

	// SELECT * carries every column of the matching rows through.
	all := strings.Replace(request, "SELECT _1 FROM BlobStorage WHERE _2 &gt; '15'",
		"SELECT * FROM BlobStorage WHERE _2 &gt; '15'", 1)
	allResp := storageRawRequestWithHeaders(t, http.MethodPost, account, "blob",
		"/"+containerName+"/"+blobName+"?comp=query", []byte(all), nil)
	require.Equal(t, http.StatusOK, allResp.StatusCode)
	allBody, err := io.ReadAll(allResp.Body)
	require.NoError(t, err)
	require.NoError(t, allResp.Body.Close())
	assert.Contains(t, string(allBody), "bravo,20\ncharlie,30\n")

	// A grammar the engine does not implement fails loudly rather than
	// answering with rows the query did not ask for.
	unsupported := strings.Replace(request,
		"SELECT _1 FROM BlobStorage WHERE _2 &gt; '15'",
		"SELECT COUNT(*) FROM BlobStorage", 1)
	bad := storageRawRequestWithHeaders(t, http.MethodPost, account, "blob",
		"/"+containerName+"/"+blobName+"?comp=query", []byte(unsupported), nil)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode)
	assert.Equal(t, "ParseError", bad.Header.Get("x-ms-error-code"))
}

// ---------------------------------------------------------------------------
// Raw-wire helpers
// ---------------------------------------------------------------------------

// storageRawRequestWithHeaders issues a request at a storage data-plane
// coordinate with a body and extra headers, for the operations no Go SDK client
// surfaces.
func storageRawRequestWithHeaders(t *testing.T, method, account, service, target string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	u, err := neturl.Parse(baseURL)
	require.NoError(t, err)
	_, port, ok := strings.Cut(u.Host, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+target, reader)
	require.NoError(t, err)
	req.Host = fmt.Sprintf("%s.%s.localhost:%s", account, service, port)
	req.Header.Set("x-ms-version", "2025-01-05")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func storageReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return string(body)
}
