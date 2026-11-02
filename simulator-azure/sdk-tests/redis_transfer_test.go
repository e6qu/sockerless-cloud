package azure_sdk_test

import (
	"crypto/tls"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Export writes a cache's keys as an RDB page blob through the Blob data
// plane, and import loads blobs into another cache, as "Import and export data
// in Azure Cache for Redis" describes.
//
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Cache/Redis/{name}/export
//	POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Cache/Redis/{name}/import
//	GET /subscriptions/{subscriptionId}/providers/Microsoft.Cache/locations/{location}/operationResults/{opId}

// redisTransferStorage is a container and the container SAS an operator hands
// the cache, with the permissions Microsoft lists for export.
type redisTransferStorage struct {
	accountID string
	container *container.Client
	sasURL    string
}

func newRedisTransferStorage(t *testing.T, rg, account, name string) redisTransferStorage {
	t.Helper()
	accounts := createStorageAccountForARM(t, rg, account)
	keys, err := accounts.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	key1, _ := storageAccountKeyValues(t, keys.Keys)
	cred, err := azblob.NewSharedKeyCredential(account, key1)
	require.NoError(t, err)
	client, err := container.NewClientWithSharedKeyCredential(storageSDKURL(t, account, "blob")+name, cred,
		&container.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = client.Create(ctx, nil)
	require.NoError(t, err)
	sasURL, err := client.GetSASURL(sas.ContainerPermissions{
		Read: true, Add: true, Create: true, Write: true, Delete: true, Tag: true, Move: true, List: true,
	}, time.Now().UTC().Add(24*time.Hour), nil)
	require.NoError(t, err)
	shown, err := accounts.GetProperties(ctx, rg, account, nil)
	require.NoError(t, err)
	return redisTransferStorage{accountID: *shown.ID, container: client, sasURL: sasURL}
}

// blobURL addresses one blob of the container with the container's SAS.
func (s redisTransferStorage) blobURL(t *testing.T, name string) string {
	t.Helper()
	u, err := url.Parse(s.sasURL)
	require.NoError(t, err)
	u.Path += "/" + name
	return u.String()
}

func createPremiumRedisCache(t *testing.T, client *armredis.Client, rg, name string, identity *armredis.ManagedServiceIdentity) armredis.ResourceInfo {
	t.Helper()
	poller, err := client.BeginCreate(ctx, rg, name, armredis.CreateParameters{
		Location: ptrStr("eastus"),
		Identity: identity,
		Properties: &armredis.CreateProperties{
			SKU:               &armredis.SKU{Name: ptr(armredis.SKUNamePremium), Family: ptr(armredis.SKUFamilyP), Capacity: ptr[int32](1)},
			MinimumTLSVersion: ptr(armredis.TLSVersionOne2),
		},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		if deletion, err := client.BeginDelete(ctx, rg, name, nil); err == nil {
			_, _ = deletion.PollUntilDone(ctx, nil)
		}
	})
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	return created.ResourceInfo
}

func redisTLSClient(t *testing.T, client *armredis.Client, rg string, cache armredis.ResourceInfo) *goredis.Client {
	t.Helper()
	keys, err := client.ListKeys(ctx, rg, *cache.Name, nil)
	require.NoError(t, err)
	hostName := *cache.Properties.HostName
	// Azure presents a certificate a public root signs for the hostName; the
	// client checks encryption, not the chain.
	secured := &tls.Config{ServerName: hostName, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // see above
	return redisCacheClient(t, redisLookupHost(t, hostName), *cache.Properties.SSLPort, *keys.PrimaryKey, secured)
}

func exportRedis(client *armredis.Client, rg, name string, params armredis.ExportRDBParameters) error {
	poller, err := client.BeginExportData(ctx, rg, name, params, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func importRedis(client *armredis.Client, rg, name string, params armredis.ImportRDBParameters) error {
	poller, err := client.BeginImportData(ctx, rg, name, params, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func requireRedisTransferFailure(t *testing.T, err error, code, contains, what string) {
	t.Helper()
	require.Error(t, err, what)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "%s: want *azcore.ResponseError, got %T: %v", what, err, err)
	assert.Equal(t, code, respErr.ErrorCode, "%s: %v", what, err)
	assert.Contains(t, err.Error(), contains, what)
}

func TestRedis_ExportAndImportMoveKeysThroughBlobStorage(t *testing.T) {
	t.Parallel()
	rg := "redis-transfer-rg"
	ensureRedisResourceGroup(t, rg)
	storage := newRedisTransferStorage(t, rg, "redistransfersas", "snapshots")
	client, err := armredis.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	source := createPremiumRedisCache(t, client, rg, "redis-transfer-source",
		&armredis.ManagedServiceIdentity{Type: ptr(armredis.ManagedServiceIdentityTypeSystemAssigned)})
	target := createPremiumRedisCache(t, client, rg, "redis-transfer-target", nil)
	require.NotNil(t, source.Identity)
	require.NotNil(t, source.Identity.PrincipalID, "a system-assigned identity carries a principal")

	sourceKeys := redisTLSClient(t, client, rg, source)
	require.NoError(t, sourceKeys.Set(ctx, "greeting", "hello from the source", 0).Err())
	require.NoError(t, sourceKeys.Set(ctx, "session", "short-lived", time.Hour).Err())
	require.NoError(t, exportRedis(client, rg, "redis-transfer-source", armredis.ExportRDBParameters{
		Container: ptrStr(storage.sasURL), Prefix: ptrStr("first"), Format: ptrStr("RDB"),
	}))

	first := storage.container.NewBlobClient("first")
	props, err := first.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, blob.BlobTypePageBlob, *props.BlobType, "Premium exports an RDB page blob")
	assert.Zero(t, *props.ContentLength%512, "a page blob is a whole number of 512-byte pages")
	download, err := first.DownloadStream(ctx, nil)
	require.NoError(t, err)
	body, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.NoError(t, download.Body.Close())
	assert.True(t, strings.HasPrefix(string(body), "REDIS0009"), "a Redis 6.0 cache writes RDB version 9")

	require.NoError(t, sourceKeys.FlushAll(ctx).Err())
	require.NoError(t, sourceKeys.Set(ctx, "counter", "42", 0).Err())
	require.NoError(t, exportRedis(client, rg, "redis-transfer-source", armredis.ExportRDBParameters{
		Container: ptrStr(storage.sasURL), Prefix: ptrStr("second"),
	}))

	targetKeys := redisTLSClient(t, client, rg, target)
	require.NoError(t, targetKeys.Set(ctx, "preexisting", "dropped by the import", 0).Err())
	require.NoError(t, importRedis(client, rg, "redis-transfer-target", armredis.ImportRDBParameters{
		Files:  []*string{ptrStr(storage.blobURL(t, "first")), ptrStr(storage.blobURL(t, "second"))},
		Format: ptrStr("RDB"),
	}))
	targetKeys = redisTLSClient(t, client, rg, target)
	assert.Equal(t, "hello from the source", targetKeys.Get(ctx, "greeting").Val())
	assert.Equal(t, "42", targetKeys.Get(ctx, "counter").Val(), "every file's keys are imported")
	remaining := targetKeys.TTL(ctx, "session").Val()
	assert.Greater(t, remaining, time.Duration(0), "a key keeps its time to live")
	assert.LessOrEqual(t, remaining, time.Hour)
	assert.Zero(t, targetKeys.Exists(ctx, "preexisting").Val(), "importing deletes the cache's existing data")

	t.Run("managed identity needs a role on the storage account", func(t *testing.T) {
		err := exportRedis(client, rg, "redis-transfer-source", armredis.ExportRDBParameters{
			Container: ptrStr(strings.SplitN(storage.sasURL, "?", 2)[0]), Prefix: ptrStr("by-identity"),
			PreferredDataArchiveAuthMethod: ptrStr("ManagedIdentity"),
		})
		requireRedisTransferFailure(t, err, "AuthorizationPermissionMismatch", "by-identity", "an identity without a data role")

		assignRole(t, storage.accountID, storageBlobDataContributorRole, *source.Identity.PrincipalID)
		require.NoError(t, exportRedis(client, rg, "redis-transfer-source", armredis.ExportRDBParameters{
			Container: ptrStr(strings.SplitN(storage.sasURL, "?", 2)[0]), Prefix: ptrStr("by-identity"),
			PreferredDataArchiveAuthMethod: ptrStr("ManagedIdentity"),
			StorageSubscriptionID:          ptrStr(subscriptionID),
		}))
		_, err = storage.container.NewBlobClient("by-identity").GetProperties(ctx, nil)
		require.NoError(t, err)

		err = exportRedis(client, rg, "redis-transfer-source", armredis.ExportRDBParameters{
			Container: ptrStr(strings.SplitN(storage.sasURL, "?", 2)[0]), Prefix: ptrStr("elsewhere"),
			PreferredDataArchiveAuthMethod: ptrStr("ManagedIdentity"),
			StorageSubscriptionID:          ptrStr("00000000-0000-0000-0000-00000000beef"),
		})
		requireRedisTransferFailure(t, err, "BadRequest", "not in subscription", "a storage subscription that does not hold the account")

		err = exportRedis(client, rg, "redis-transfer-target", armredis.ExportRDBParameters{
			Container: ptrStr(strings.SplitN(storage.sasURL, "?", 2)[0]), Prefix: ptrStr("no-identity"),
			PreferredDataArchiveAuthMethod: ptrStr("ManagedIdentity"),
		})
		requireRedisTransferFailure(t, err, "BadRequest", "has no managed identity", "a cache without an identity")
	})

	t.Run("refusals", func(t *testing.T) {
		err := exportRedis(client, rg, "redis-transfer-source", armredis.ExportRDBParameters{
			Container: ptrStr("https://example.blob.core.windows.net/backups?sv=2021-12-02&sr=c&sp=rw&se=2099-01-01&sig=x"),
			Prefix:    ptrStr("unreachable"),
		})
		requireRedisTransferFailure(t, err, "BadRequest", "cannot be reached", "a host this cloud does not serve")

		err = importRedis(client, rg, "redis-transfer-target", armredis.ImportRDBParameters{
			Files: []*string{ptrStr("https://example.blob.core.windows.net/backups/dump.rdb")},
		})
		requireRedisTransferFailure(t, err, "BadRequest", "cannot be reached", "a file on a host this cloud does not serve")

		tampered := strings.Replace(storage.sasURL, "sig=", "sig=AAAA", 1)
		err = exportRedis(client, rg, "redis-transfer-source", armredis.ExportRDBParameters{
			Container: ptrStr(tampered), Prefix: ptrStr("tampered"),
		})
		requireRedisTransferFailure(t, err, "AuthenticationFailed", "tampered", "a SAS the account's keys did not sign")

		err = importRedis(client, rg, "redis-transfer-target", armredis.ImportRDBParameters{
			Files: []*string{ptrStr(storage.blobURL(t, "missing"))},
		})
		requireRedisTransferFailure(t, err, "BlobNotFound", "missing", "a file that does not exist")

		_, err = storage.container.NewBlockBlobClient("notes.txt").UploadBuffer(ctx, []byte("not a snapshot"), nil)
		require.NoError(t, err)
		err = importRedis(client, rg, "redis-transfer-target", armredis.ImportRDBParameters{
			Files: []*string{ptrStr(storage.blobURL(t, "notes.txt"))},
		})
		requireRedisTransferFailure(t, err, "BadRequest", "is not an RDB file", "a file that is not an RDB file")
		assert.Equal(t, "42", redisTLSClient(t, client, rg, target).Get(ctx, "counter").Val(),
			"a refused import leaves the cache's data in place")

		_, err = client.BeginExportData(ctx, rg, "redis-transfer-source", armredis.ExportRDBParameters{
			Container: ptrStr(storage.sasURL), Prefix: ptrStr("gz"), Format: ptrStr("GZIP"),
		}, nil)
		requireRedisTransferFailure(t, err, "BadRequest", "GZIP", "a format other than RDB")
	})
}

// Import and export are supported in the Premium tier only.
func TestRedis_ImportExportRequirePremium(t *testing.T) {
	t.Parallel()
	rg := "redis-transfer-tier-rg"
	ensureRedisResourceGroup(t, rg)
	storage := newRedisTransferStorage(t, rg, "redistransfertier", "snapshots")
	client := newRedisCacheForTest(t, rg, "redis-transfer-basic")

	_, err := client.BeginExportData(ctx, rg, "redis-transfer-basic", armredis.ExportRDBParameters{
		Container: ptrStr(storage.sasURL), Prefix: ptrStr("basic"),
	}, nil)
	requireRedisTransferFailure(t, err, "BadRequest", "Premium", "an export from a Basic cache")
	_, err = client.BeginImportData(ctx, rg, "redis-transfer-basic", armredis.ImportRDBParameters{
		Files: []*string{ptrStr(storage.blobURL(t, "basic"))},
	}, nil)
	requireRedisTransferFailure(t, err, "BadRequest", "Premium", "an import into a Basic cache")
}
