package azure_sdk_test

import (
	"encoding/json"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/file"
	fileservice "github.com/Azure/azure-sdk-for-go/sdk/storage/azfile/service"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStorageSDK_BlobContainerAndBlobProperties drives Get Container
// Properties and Get Blob Properties through the official azblob clients.
func TestStorageSDK_BlobContainerAndBlobProperties(t *testing.T) {
	account := "sdkblobpropsacct"
	container := "sdk-props-container"
	blobName := "props.txt"
	payload := []byte("blob properties payload")

	client, err := azblob.NewClientWithNoCredential(storageSDKURL(t, account, "blob"),
		&azblob.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = client.CreateContainer(ctx, container, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteContainer(ctx, container, nil) })

	containerClient := client.ServiceClient().NewContainerClient(container)
	containerProps, err := containerClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, containerProps.ETag, "Get Container Properties must return an ETag")
	require.NotNil(t, containerProps.LastModified, "Get Container Properties must return Last-Modified")

	_, err = client.UploadBuffer(ctx, container, blobName, payload, nil)
	require.NoError(t, err)

	blobProps, err := containerClient.NewBlobClient(blobName).GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, blobProps.ContentLength)
	assert.Equal(t, int64(len(payload)), *blobProps.ContentLength, "Get Blob Properties must report the stored size")
	require.NotNil(t, blobProps.ETag, "Get Blob Properties must return an ETag")
	require.NotNil(t, blobProps.LastModified, "Get Blob Properties must return Last-Modified")
}

// TestStorageSDK_FileShareAndFileProperties drives Get Share Properties and
// Get File Properties through the official azfile clients.
func TestStorageSDK_FileShareAndFileProperties(t *testing.T) {
	account := "sdkfilepropsacct"
	share := "sdk-props-share"
	fileName := "props.txt"
	payload := []byte("file properties payload")

	serviceClient, err := fileservice.NewClientWithNoCredential(storageSDKURL(t, account, "file"),
		&fileservice.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = serviceClient.CreateShare(ctx, share, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteShare(ctx, share, nil) })

	shareProps, err := serviceClient.NewShareClient(share).GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, shareProps.ETag, "Get Share Properties must return an ETag")
	require.NotNil(t, shareProps.LastModified, "Get Share Properties must return Last-Modified")
	require.NotNil(t, shareProps.Quota, "Get Share Properties must return the share quota")

	fileClient, err := file.NewClientWithNoCredential(storageSDKURL(t, account, "file")+share+"/"+fileName,
		&file.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	_, err = fileClient.Create(ctx, int64(len(payload)), nil)
	require.NoError(t, err)
	require.NoError(t, fileClient.UploadBuffer(ctx, payload, nil))

	fileProps, err := fileClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, fileProps.ContentLength)
	assert.Equal(t, int64(len(payload)), *fileProps.ContentLength, "Get File Properties must report the stored size")
}

// TestStorageSDK_QueueMetadataPeekAndClear drives Get Queue Metadata, Peek
// Messages and Clear Messages through the official azqueue client.
func TestStorageSDK_QueueMetadataPeekAndClear(t *testing.T) {
	account := "sdkqueuepropsacct"
	queueName := "sdkpropsqueue"

	serviceClient, err := azqueue.NewServiceClientWithNoCredential(storageSDKURL(t, account, "queue"),
		&azqueue.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = serviceClient.CreateQueue(ctx, queueName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })

	queueClient := serviceClient.NewQueueClient(queueName)
	_, err = queueClient.EnqueueMessage(ctx, "first message", nil)
	require.NoError(t, err)
	_, err = queueClient.EnqueueMessage(ctx, "second message", nil)
	require.NoError(t, err)

	props, err := queueClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ApproximateMessagesCount)
	assert.Equal(t, int32(2), *props.ApproximateMessagesCount, "Get Queue Metadata must report the visible message count")

	// Peek returns the messages without dequeuing them.
	peeked, err := queueClient.PeekMessages(ctx, &azqueue.PeekMessagesOptions{NumberOfMessages: to.Ptr(int32(2))})
	require.NoError(t, err)
	require.Len(t, peeked.Messages, 2)
	require.NotNil(t, peeked.Messages[0].MessageText)
	assert.Equal(t, "first message", *peeked.Messages[0].MessageText)

	afterPeek, err := queueClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), *afterPeek.ApproximateMessagesCount, "peeking must not dequeue")

	// Clear empties the queue.
	_, err = queueClient.ClearMessages(ctx, nil)
	require.NoError(t, err)
	afterClear, err := queueClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(0), *afterClear.ApproximateMessagesCount, "Clear Messages must empty the queue")
	dequeued, err := queueClient.DequeueMessage(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, dequeued.Messages, "no message may survive Clear Messages")
}

// TestStorageSDK_TableUpsertEntity drives Upsert Entity in both replace and
// merge modes through the official aztables client.
func TestStorageSDK_TableUpsertEntity(t *testing.T) {
	account := "sdktableupsertacct"
	tableName := "UpsertTable"

	serviceClient, err := aztables.NewServiceClientWithNoCredential(storageSDKURL(t, account, "table"),
		&aztables.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)

	_, err = serviceClient.CreateTable(ctx, tableName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteTable(ctx, tableName, nil) })

	tableClient := serviceClient.NewClient(tableName)

	// Upsert on an absent entity inserts it.
	first, err := json.Marshal(map[string]any{
		"PartitionKey": "p1",
		"RowKey":       "r1",
		"Color":        "red",
		"Size":         1,
	})
	require.NoError(t, err)
	_, err = tableClient.UpsertEntity(ctx, first, &aztables.UpsertEntityOptions{UpdateMode: aztables.UpdateModeReplace})
	require.NoError(t, err)

	// Replace mode swaps the entity wholesale: Size disappears.
	replaced, err := json.Marshal(map[string]any{
		"PartitionKey": "p1",
		"RowKey":       "r1",
		"Color":        "blue",
	})
	require.NoError(t, err)
	_, err = tableClient.UpsertEntity(ctx, replaced, &aztables.UpsertEntityOptions{UpdateMode: aztables.UpdateModeReplace})
	require.NoError(t, err)

	got, err := tableClient.GetEntity(ctx, "p1", "r1", nil)
	require.NoError(t, err)
	var entity map[string]any
	require.NoError(t, json.Unmarshal(got.Value, &entity))
	assert.Equal(t, "blue", entity["Color"])
	assert.NotContains(t, entity, "Size", "replace-mode upsert must drop properties the new entity omits")

	// Merge mode folds new properties into the stored entity.
	merged, err := json.Marshal(map[string]any{
		"PartitionKey": "p1",
		"RowKey":       "r1",
		"Weight":       7,
	})
	require.NoError(t, err)
	_, err = tableClient.UpsertEntity(ctx, merged, &aztables.UpsertEntityOptions{UpdateMode: aztables.UpdateModeMerge})
	require.NoError(t, err)

	got, err = tableClient.GetEntity(ctx, "p1", "r1", nil)
	require.NoError(t, err)
	entity = nil
	require.NoError(t, json.Unmarshal(got.Value, &entity))
	assert.Equal(t, "blue", entity["Color"], "merge-mode upsert must keep existing properties")
	assert.Equal(t, float64(7), entity["Weight"], "merge-mode upsert must add the new property")
}
