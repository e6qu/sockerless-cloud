package azure_sdk_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sbMoveAMQPClient builds a client from a connection string the caller
// captured, not the current one.
func sbMoveAMQPClient(t *testing.T, connectionString string) *azservicebus.Client {
	t.Helper()
	client, err := azservicebus.NewClientFromConnectionString(connectionString, &azservicebus.ClientOptions{
		CustomEndpoint: sbAMQPEndpoint,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- test-only self-signed simulator certificate.
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client
}

func sbMoveSend(t *testing.T, client *azservicebus.Client, queue, body string) {
	t.Helper()
	sender, err := client.NewSender(queue, nil)
	require.NoError(t, err)
	defer func() { _ = sender.Close(context.Background()) }()
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, sender.SendMessage(sendCtx, &azservicebus.Message{Body: []byte(body)}, nil))
}

func sbMoveReceive(t *testing.T, client *azservicebus.Client, queue string) string {
	t.Helper()
	receiver, err := client.NewReceiverForQueue(queue, &azservicebus.ReceiverOptions{
		ReceiveMode: azservicebus.ReceiveModeReceiveAndDelete,
	})
	require.NoError(t, err)
	defer func() { _ = receiver.Close(context.Background()) }()
	// The message is already enqueued; the timeout covers AMQP link setup.
	receiveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	messages, err := receiver.ReceiveMessages(receiveCtx, 1, nil)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	return string(messages[0].Body)
}

// TestAzureResources_MoveServiceBusNamespace: the ARM plane re-keys (the
// namespace, its queue and both authorization rules answer under the
// destination group only, and the namespace's tags move along), while the
// credential does not move: Shared Access Signature keys derive from each
// rule's resource ID, so they are pinned, and the connection string captured
// before the move still sends and receives over AMQP.
func TestAzureResources_MoveServiceBusNamespace(t *testing.T) {
	t.Parallel()
	srcRG, dstRG := uniquename.New("sb-move-src-rg"), uniquename.New("sb-move-dst-rg")
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	namespace := uniquename.New("sdk-sb-move-ns")
	const queue = "sb-move-queue"

	factory, err := armservicebus.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := factory.NewNamespacesClient()
	createPoller, err := namespaces.BeginCreateOrUpdate(ctx, srcRG, namespace, armservicebus.SBNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armservicebus.SBSKU{
			Name: to.Ptr(armservicebus.SKUNameStandard),
			Tier: to.Ptr(armservicebus.SKUTierStandard),
		},
		Tags: map[string]*string{"origin": to.Ptr("resource-put")},
	}, nil)
	require.NoError(t, err)
	createdNS, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, createdNS.ID)
	namespaceID := *createdNS.ID

	queues := factory.NewQueuesClient()
	_, err = queues.CreateOrUpdate(ctx, srcRG, namespace, queue, armservicebus.SBQueue{}, nil)
	require.NoError(t, err)

	// The queue rule and the root rule each derive their keys from their own
	// resource ID.
	queueRules := factory.NewQueuesClient()
	_, err = queueRules.CreateOrUpdateAuthorizationRule(ctx, srcRG, namespace, queue, "sender",
		armservicebus.SBAuthorizationRule{
			Properties: &armservicebus.SBAuthorizationRuleProperties{
				Rights: []*armservicebus.AccessRights{to.Ptr(armservicebus.AccessRightsSend), to.Ptr(armservicebus.AccessRightsListen)},
			},
		}, nil)
	require.NoError(t, err)

	// Capture the operator's credential before the move.
	keysBefore, err := namespaces.ListKeys(ctx, srcRG, namespace, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, keysBefore.PrimaryKey)
	require.NotNil(t, keysBefore.PrimaryConnectionString)
	queueKeysBefore, err := queues.ListKeys(ctx, srcRG, namespace, queue, "sender", nil)
	require.NoError(t, err)
	require.NotNil(t, queueKeysBefore.PrimaryKey)

	// The connection string names the namespace host, which the harness reaches
	// through the AMQP listener.
	connBefore := messagingConnectionString(namespace, namespace+".servicebus.localhost", *keysBefore.PrimaryKey)
	before := sbMoveAMQPClient(t, connBefore)
	sbMoveSend(t, before, queue, "enqueued before the move")

	tags, err := armresources.NewTagsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = tags.UpdateAtScope(ctx, namespaceID, armresources.TagsPatchResource{
		Operation:  to.Ptr(armresources.TagsPatchOperationMerge),
		Properties: &armresources.Tags{Tags: map[string]*string{"team": to.Ptr("messaging")}},
	}, nil)
	require.NoError(t, err)

	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	move := armresources.MoveInfo{
		Resources:           []*string{to.Ptr(namespaceID)},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + dstRG),
	}
	valPoller, err := client.BeginValidateMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = valPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	movePoller, err := client.BeginMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = movePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	moved, err := namespaces.Get(ctx, dstRG, namespace, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = namespaces.Get(ctx, srcRG, namespace, nil)
	require.Error(t, err, "the moved namespace must no longer resolve under the source group")

	movedQueue, err := queues.Get(ctx, dstRG, namespace, queue, nil)
	require.NoError(t, err)
	require.NotNil(t, movedQueue.ID)
	assert.Contains(t, *movedQueue.ID, "/resourceGroups/"+dstRG+"/")
	_, err = queues.Get(ctx, srcRG, namespace, queue, nil)
	require.Error(t, err, "the moved queue must no longer resolve under the source group")

	// The tags written at the old scope answer at the new one, and the namespace's
	// own read agrees.
	movedTags, err := tags.GetAtScope(ctx, *moved.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"origin": "resource-put", "team": "messaging"},
		tagsOf(movedTags.Properties.Tags), "tags must move with the resource")
	assert.Equal(t, map[string]string{"origin": "resource-put", "team": "messaging"}, tagsOf(moved.Tags))

	// A move never rotates a namespace's keys.
	keysAfter, err := namespaces.ListKeys(ctx, dstRG, namespace, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	assert.Equal(t, *keysBefore.PrimaryKey, *keysAfter.PrimaryKey,
		"a cross-group move must not rotate the namespace's primary key")
	assert.Equal(t, *keysBefore.SecondaryKey, *keysAfter.SecondaryKey,
		"a cross-group move must not rotate the namespace's secondary key")
	assert.Equal(t, *keysBefore.PrimaryConnectionString, *keysAfter.PrimaryConnectionString,
		"the connection string an operator holds must survive the move verbatim")
	queueKeysAfter, err := queues.ListKeys(ctx, dstRG, namespace, queue, "sender", nil)
	require.NoError(t, err)
	assert.Equal(t, *queueKeysBefore.PrimaryKey, *queueKeysAfter.PrimaryKey,
		"a cross-group move must not rotate a queue rule's key either")

	// The pre-move credential receives the message enqueued before the move and
	// completes a fresh round trip.
	after := sbMoveAMQPClient(t, connBefore)
	assert.Equal(t, "enqueued before the move", sbMoveReceive(t, after, queue),
		"a message enqueued before the move must still be delivered after it")
	sbMoveSend(t, after, queue, "enqueued after the move")
	assert.Equal(t, "enqueued after the move", sbMoveReceive(t, after, queue),
		"the pre-move credential must still send and receive after the move")

	// Re-validating the stale source ID fails: Azure Resource Manager reports an
	// ID naming no live resource as absent.
	_, err = client.BeginValidateMoveResources(ctx, srcRG, move, nil)
	require.Error(t, err, "re-validating the stale source id must fail")
	var staleErr *azcore.ResponseError
	require.True(t, errors.As(err, &staleErr), "want *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, "ResourceNotFound", staleErr.ErrorCode)
	assert.Equal(t, http.StatusNotFound, staleErr.StatusCode)
}
