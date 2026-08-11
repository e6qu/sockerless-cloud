package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queueSDKService builds an azqueue service client for one account at the
// simulator's coordinate — the same client code a consumer runs against Azure
// Queue Storage, differing only in the endpoint it is pointed at.
func queueSDKService(t *testing.T, account string) *azqueue.ServiceClient {
	t.Helper()
	client, err := azqueue.NewServiceClientWithNoCredential(storageSDKURL(t, account, "queue"),
		&azqueue.ClientOptions{ClientOptions: storageSDKOptions()})
	require.NoError(t, err)
	return client
}

// TestQueueSDK_UpdateMessageExtendsInvisibility drives Update Message, the call
// a consumer still working on a message makes to keep it invisible to everyone
// else. It is the operation that makes a long-running queue consumer possible,
// so the pop-receipt check, the extension and the fresh receipt all have to be
// real.
func TestQueueSDK_UpdateMessageExtendsInvisibility(t *testing.T) {
	const (
		account   = "sdkqueueupdate"
		queueName = "sdkupdatequeue"
	)
	serviceClient := queueSDKService(t, account)
	_, err := serviceClient.CreateQueue(ctx, queueName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })

	queueClient := serviceClient.NewQueueClient(queueName)
	_, err = queueClient.EnqueueMessage(ctx, "work item", nil)
	require.NoError(t, err)

	dequeued, err := queueClient.DequeueMessage(ctx, &azqueue.DequeueMessageOptions{
		VisibilityTimeout: to.Ptr(int32(1)),
	})
	require.NoError(t, err)
	require.Len(t, dequeued.Messages, 1)
	require.NotNil(t, dequeued.Messages[0].MessageID)
	require.NotNil(t, dequeued.Messages[0].PopReceipt)
	messageID := *dequeued.Messages[0].MessageID
	popReceipt := *dequeued.Messages[0].PopReceipt

	// A pop receipt the service never issued updates nothing.
	_, err = queueClient.UpdateMessage(ctx, messageID, "not-the-receipt", "work item",
		&azqueue.UpdateMessageOptions{VisibilityTimeout: to.Ptr(int32(600))})
	require.Error(t, err, "Update Message must reject a pop receipt it did not issue")

	// The holder extends the invisibility and replaces the content.
	updated, err := queueClient.UpdateMessage(ctx, messageID, popReceipt, "work item, still running",
		&azqueue.UpdateMessageOptions{VisibilityTimeout: to.Ptr(int32(600))})
	require.NoError(t, err)
	require.NotNil(t, updated.PopReceipt)
	assert.NotEqual(t, popReceipt, *updated.PopReceipt,
		"Update Message must issue a pop receipt that supersedes the one it was given")
	require.NotNil(t, updated.TimeNextVisible)
	assert.True(t, updated.TimeNextVisible.After(time.Now().Add(5*time.Minute)),
		"the extended visibility timeout must actually move the time the message reappears")

	// The extension holds: nothing else may dequeue the message.
	time.Sleep(1500 * time.Millisecond)
	other, err := queueClient.DequeueMessage(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, other.Messages,
		"the message must stay invisible for the timeout Update Message set, not the one the dequeue set")

	// The superseded receipt can no longer delete the message; the fresh one can.
	_, err = queueClient.DeleteMessage(ctx, messageID, popReceipt, nil)
	require.Error(t, err, "the superseded pop receipt must not delete the message")
	_, err = queueClient.DeleteMessage(ctx, messageID, *updated.PopReceipt, nil)
	require.NoError(t, err)

	props, err := queueClient.GetProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.ApproximateMessagesCount)
	assert.Equal(t, int32(0), *props.ApproximateMessagesCount)
}

// TestQueueSDK_UpdateMessageMakesAMessageVisibleAgain: a zero visibility
// timeout is how a consumer that cannot finish hands the message straight back.
func TestQueueSDK_UpdateMessageMakesAMessageVisibleAgain(t *testing.T) {
	const (
		account   = "sdkqueuereturn"
		queueName = "sdkreturnqueue"
	)
	serviceClient := queueSDKService(t, account)
	_, err := serviceClient.CreateQueue(ctx, queueName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })

	queueClient := serviceClient.NewQueueClient(queueName)
	_, err = queueClient.EnqueueMessage(ctx, "handed back", nil)
	require.NoError(t, err)

	dequeued, err := queueClient.DequeueMessage(ctx, &azqueue.DequeueMessageOptions{
		VisibilityTimeout: to.Ptr(int32(600)),
	})
	require.NoError(t, err)
	require.Len(t, dequeued.Messages, 1)

	_, err = queueClient.UpdateMessage(ctx, *dequeued.Messages[0].MessageID, *dequeued.Messages[0].PopReceipt,
		"handed back", &azqueue.UpdateMessageOptions{VisibilityTimeout: to.Ptr(int32(0))})
	require.NoError(t, err)

	again, err := queueClient.DequeueMessage(ctx, nil)
	require.NoError(t, err)
	require.Len(t, again.Messages, 1, "a zero visibility timeout must return the message to the queue at once")
	require.NotNil(t, again.Messages[0].MessageText)
	assert.Equal(t, "handed back", *again.Messages[0].MessageText)
	require.NotNil(t, again.Messages[0].DequeueCount)
	assert.Equal(t, int64(2), *again.Messages[0].DequeueCount,
		"the second delivery must be counted")
}

// TestQueueSDK_AccessPolicyRoundTrip drives the queue's stored access policies,
// the signed identifiers a shared access signature is issued against.
func TestQueueSDK_AccessPolicyRoundTrip(t *testing.T) {
	const (
		account   = "sdkqueueacl"
		queueName = "sdkaclqueue"
	)
	serviceClient := queueSDKService(t, account)
	_, err := serviceClient.CreateQueue(ctx, queueName, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = serviceClient.DeleteQueue(ctx, queueName, nil) })

	queueClient := serviceClient.NewQueueClient(queueName)
	empty, err := queueClient.GetAccessPolicy(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty.SignedIdentifiers, "a queue starts with no stored access policies")

	start := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	expiry := start.Add(24 * time.Hour)
	_, err = queueClient.SetAccessPolicy(ctx, &azqueue.SetAccessPolicyOptions{
		QueueACL: []*azqueue.SignedIdentifier{{
			ID: to.Ptr("consumer"),
			AccessPolicy: &azqueue.AccessPolicy{
				Start:      &start,
				Expiry:     &expiry,
				Permission: to.Ptr("rap"),
			},
		}},
	})
	require.NoError(t, err)

	got, err := queueClient.GetAccessPolicy(ctx, nil)
	require.NoError(t, err)
	require.Len(t, got.SignedIdentifiers, 1)
	require.NotNil(t, got.SignedIdentifiers[0].ID)
	assert.Equal(t, "consumer", *got.SignedIdentifiers[0].ID)
	require.NotNil(t, got.SignedIdentifiers[0].AccessPolicy)
	require.NotNil(t, got.SignedIdentifiers[0].AccessPolicy.Permission)
	assert.Equal(t, "rap", *got.SignedIdentifiers[0].AccessPolicy.Permission)
	require.NotNil(t, got.SignedIdentifiers[0].AccessPolicy.Start)
	assert.Equal(t, start, *got.SignedIdentifiers[0].AccessPolicy.Start)
	require.NotNil(t, got.SignedIdentifiers[0].AccessPolicy.Expiry)
	assert.Equal(t, expiry, *got.SignedIdentifiers[0].AccessPolicy.Expiry)

	// Setting an empty policy list clears the queue's policies.
	_, err = queueClient.SetAccessPolicy(ctx, &azqueue.SetAccessPolicyOptions{QueueACL: nil})
	require.NoError(t, err)
	cleared, err := queueClient.GetAccessPolicy(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, cleared.SignedIdentifiers)
}

// TestQueueSDK_ServicePropertiesAndStatistics drives the service-level
// configuration a write has to actually change, and the replication statistics
// a client reads from the secondary endpoint.
func TestQueueSDK_ServicePropertiesAndStatistics(t *testing.T) {
	const account = "sdkqueueservice"
	serviceClient := queueSDKService(t, account)

	_, err := serviceClient.SetProperties(ctx, &azqueue.SetPropertiesOptions{
		Logging: &azqueue.Logging{
			Version: to.Ptr("1.0"),
			Delete:  to.Ptr(true),
			Read:    to.Ptr(true),
			Write:   to.Ptr(true),
			RetentionPolicy: &azqueue.RetentionPolicy{
				Enabled: to.Ptr(true),
				Days:    to.Ptr(int32(4)),
			},
		},
		CORS: []*azqueue.CORSRule{{
			AllowedOrigins:  to.Ptr("https://console.example"),
			AllowedMethods:  to.Ptr("GET,PUT"),
			AllowedHeaders:  to.Ptr("x-ms-meta-*"),
			ExposedHeaders:  to.Ptr("x-ms-meta-*"),
			MaxAgeInSeconds: to.Ptr(int32(60)),
		}},
	})
	require.NoError(t, err)

	props, err := serviceClient.GetServiceProperties(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, props.Logging)
	require.NotNil(t, props.Logging.Delete)
	assert.True(t, *props.Logging.Delete, "Get Service Properties must read back what Set wrote")
	require.NotNil(t, props.Logging.RetentionPolicy)
	require.NotNil(t, props.Logging.RetentionPolicy.Days)
	assert.Equal(t, int32(4), *props.Logging.RetentionPolicy.Days)
	require.Len(t, props.CORS, 1)
	require.NotNil(t, props.CORS[0].AllowedOrigins)
	assert.Equal(t, "https://console.example", *props.CORS[0].AllowedOrigins)

	// Get Service Statistics reports the replication state of the account's
	// queues.
	stats, err := serviceClient.GetStatistics(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, stats.GeoReplication)
	require.NotNil(t, stats.GeoReplication.Status)
	assert.Equal(t, "live", string(*stats.GeoReplication.Status))
	require.NotNil(t, stats.GeoReplication.LastSyncTime,
		"Get Service Statistics must report a last sync time")
}
