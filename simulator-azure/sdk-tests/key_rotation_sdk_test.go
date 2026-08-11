package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storageAccountKeyValues extracts the key1/key2 values from an
// AccountListKeysResult keys slice.
func storageAccountKeyValues(t *testing.T, keys []*armstorage.AccountKey) (key1, key2 string) {
	t.Helper()
	for _, k := range keys {
		require.NotNil(t, k.KeyName)
		require.NotNil(t, k.Value)
		switch *k.KeyName {
		case "key1":
			key1 = *k.Value
		case "key2":
			key2 = *k.Value
		}
	}
	require.NotEmpty(t, key1, "listKeys must include key1")
	require.NotEmpty(t, key2, "listKeys must include key2")
	return key1, key2
}

// TestStorageARM_RegenerateKeyRotation drives the Microsoft.Storage account
// key rotation contract: regenerateKey changes only the named key, listKeys
// observes the rotation, a second regenerate yields yet another value, and
// deleting the account resets the keys for a later account of the same name.
func TestStorageARM_RegenerateKeyRotation(t *testing.T) {
	rg := "storagearm-rotation-rg"
	account := "storagearmrotation"
	client := createStorageAccountForARM(t, rg, account)

	initial, err := client.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	key1Before, key2Before := storageAccountKeyValues(t, initial.Keys)

	regen, err := client.RegenerateKey(ctx, rg, account, armstorage.AccountRegenerateKeyParameters{
		KeyName: to.Ptr("key1"),
	}, nil)
	require.NoError(t, err)
	key1Rotated, key2Rotated := storageAccountKeyValues(t, regen.Keys)
	assert.NotEqual(t, key1Before, key1Rotated, "regenerating key1 must change key1")
	assert.Equal(t, key2Before, key2Rotated, "regenerating key1 must not change key2")

	listed, err := client.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	key1Listed, key2Listed := storageAccountKeyValues(t, listed.Keys)
	assert.Equal(t, key1Rotated, key1Listed, "listKeys must return the rotated key1")
	assert.Equal(t, key2Before, key2Listed, "listKeys must keep the untouched key2")

	again, err := client.RegenerateKey(ctx, rg, account, armstorage.AccountRegenerateKeyParameters{
		KeyName: to.Ptr("key1"),
	}, nil)
	require.NoError(t, err)
	key1Again, _ := storageAccountKeyValues(t, again.Keys)
	assert.NotEqual(t, key1Rotated, key1Again, "a second regenerate of key1 must yield yet another value")

	// Delete + recreate under the same name starts from fresh key material.
	_, err = client.Delete(ctx, rg, account, nil)
	require.NoError(t, err)
	createStorageAccountForARM(t, rg, account)
	fresh, err := client.ListKeys(ctx, rg, account, nil)
	require.NoError(t, err)
	key1Fresh, key2Fresh := storageAccountKeyValues(t, fresh.Keys)
	assert.Equal(t, key1Before, key1Fresh, "a recreated account must start from unrotated key1")
	assert.Equal(t, key2Before, key2Fresh, "a recreated account must start from unrotated key2")
}

// TestRedis_RegenerateKeyRotation drives the Azure Cache for Redis key
// rotation contract: regenerateKey changes only the requested slot, listKeys
// observes the rotation, repeated regenerates keep producing new values, and
// delete + recreate resets the keys.
func TestRedis_RegenerateKeyRotation(t *testing.T) {
	rg := "redis-rotation-rg"
	name := "redis-rotation"
	ensureRedisResourceGroup(t, rg)
	client := newRedisCacheForTest(t, rg, name)

	initial, err := client.ListKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, initial.PrimaryKey)
	require.NotNil(t, initial.SecondaryKey)
	primaryBefore, secondaryBefore := *initial.PrimaryKey, *initial.SecondaryKey

	regen, err := client.RegenerateKey(ctx, rg, name, armredis.RegenerateKeyParameters{
		KeyType: ptr(armredis.RedisKeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, regen.PrimaryKey)
	assert.NotEqual(t, primaryBefore, *regen.PrimaryKey, "regenerating Primary must change the primary key")
	assert.Equal(t, secondaryBefore, *regen.SecondaryKey, "regenerating Primary must not change the secondary key")

	listed, err := client.ListKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, *regen.PrimaryKey, *listed.PrimaryKey, "listKeys must return the rotated primary key")
	assert.Equal(t, secondaryBefore, *listed.SecondaryKey, "listKeys must keep the untouched secondary key")

	again, err := client.RegenerateKey(ctx, rg, name, armredis.RegenerateKeyParameters{
		KeyType: ptr(armredis.RedisKeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, *regen.PrimaryKey, *again.PrimaryKey, "a second regenerate of Primary must yield yet another value")

	// Delete + recreate under the same name starts from fresh key material.
	delPoller, err := client.BeginDelete(ctx, rg, name, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	newRedisCacheForTest(t, rg, name)
	fresh, err := client.ListKeys(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, primaryBefore, *fresh.PrimaryKey, "a recreated cache must start from the unrotated primary key")
	assert.Equal(t, secondaryBefore, *fresh.SecondaryKey, "a recreated cache must start from the unrotated secondary key")
}

// TestEventHubsARM_RegenerateKeysRotation drives the Event Hubs
// authorization-rule key rotation contract at namespace and event hub scope:
// regenerateKeys changes only the requested slot, the returned connection
// strings embed the rotated key, listKeys observes the rotation, the optional
// key field pins caller-supplied material, and deleting the rule resets its
// keys for a later rule of the same name.
func TestEventHubsARM_RegenerateKeysRotation(t *testing.T) {
	rg := "eh-rotation-rg"
	ns := "sdk-eh-rotation-ns"
	rule := "sdk-eh-rotation-rule"

	clientFactory, err := armeventhub.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := clientFactory.NewNamespacesClient()

	createPoller, err := namespaces.BeginCreateOrUpdate(ctx, rg, ns, armeventhub.EHNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armeventhub.SKU{
			Name: to.Ptr(armeventhub.SKUNameStandard),
			Tier: to.Ptr(armeventhub.SKUTierStandard),
		},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		deletePoller, err := namespaces.BeginDelete(ctx, rg, ns, nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(ctx, nil)
		}
	})

	_, err = namespaces.CreateOrUpdateAuthorizationRule(ctx, rg, ns, rule, armeventhub.AuthorizationRule{
		Properties: &armeventhub.AuthorizationRuleProperties{
			Rights: []*armeventhub.AccessRights{to.Ptr(armeventhub.AccessRightsListen), to.Ptr(armeventhub.AccessRightsSend)},
		},
	}, nil)
	require.NoError(t, err)

	initial, err := namespaces.ListKeys(ctx, rg, ns, rule, nil)
	require.NoError(t, err)
	require.NotNil(t, initial.PrimaryKey)
	require.NotNil(t, initial.SecondaryKey)
	primaryBefore, secondaryBefore := *initial.PrimaryKey, *initial.SecondaryKey
	assert.NotEqual(t, primaryBefore, secondaryBefore, "primary and secondary keys must be distinct")
	require.NotNil(t, initial.PrimaryConnectionString)
	assert.Contains(t, *initial.PrimaryConnectionString, "SharedAccessKey="+primaryBefore,
		"the primary connection string must embed the primary key")
	require.NotNil(t, initial.SecondaryConnectionString)
	assert.Contains(t, *initial.SecondaryConnectionString, "SharedAccessKey="+secondaryBefore,
		"the secondary connection string must embed the secondary key")

	regen, err := namespaces.RegenerateKeys(ctx, rg, ns, rule, armeventhub.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armeventhub.KeyTypePrimaryKey),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, regen.PrimaryKey)
	assert.NotEqual(t, primaryBefore, *regen.PrimaryKey, "regenerating PrimaryKey must change the primary key")
	assert.Equal(t, secondaryBefore, *regen.SecondaryKey, "regenerating PrimaryKey must not change the secondary key")
	require.NotNil(t, regen.PrimaryConnectionString)
	assert.Contains(t, *regen.PrimaryConnectionString, "SharedAccessKey="+*regen.PrimaryKey,
		"the primary connection string must embed the rotated primary key")

	listed, err := namespaces.ListKeys(ctx, rg, ns, rule, nil)
	require.NoError(t, err)
	assert.Equal(t, *regen.PrimaryKey, *listed.PrimaryKey, "listKeys must return the rotated primary key")
	assert.Equal(t, secondaryBefore, *listed.SecondaryKey, "listKeys must keep the untouched secondary key")

	// The optional key field pins caller-supplied material.
	pinned := "PinnedEventHubsKeyPinnedEventHubsKeyPinned1="
	pinResp, err := namespaces.RegenerateKeys(ctx, rg, ns, rule, armeventhub.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armeventhub.KeyTypeSecondaryKey),
		Key:     to.Ptr(pinned),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, pinned, *pinResp.SecondaryKey, "the caller-supplied key must become the secondary key")
	pinListed, err := namespaces.ListKeys(ctx, rg, ns, rule, nil)
	require.NoError(t, err)
	assert.Equal(t, pinned, *pinListed.SecondaryKey, "listKeys must return the caller-supplied secondary key")
	assert.Contains(t, *pinListed.SecondaryConnectionString, "SharedAccessKey="+pinned,
		"the secondary connection string must embed the caller-supplied key")

	// Delete + recreate the rule under the same name starts from fresh keys.
	_, err = namespaces.DeleteAuthorizationRule(ctx, rg, ns, rule, nil)
	require.NoError(t, err)
	_, err = namespaces.CreateOrUpdateAuthorizationRule(ctx, rg, ns, rule, armeventhub.AuthorizationRule{
		Properties: &armeventhub.AuthorizationRuleProperties{
			Rights: []*armeventhub.AccessRights{to.Ptr(armeventhub.AccessRightsListen)},
		},
	}, nil)
	require.NoError(t, err)
	fresh, err := namespaces.ListKeys(ctx, rg, ns, rule, nil)
	require.NoError(t, err)
	assert.Equal(t, primaryBefore, *fresh.PrimaryKey, "a recreated rule must start from the unrotated primary key")
	assert.Equal(t, secondaryBefore, *fresh.SecondaryKey, "a recreated rule must start from the unrotated secondary key")

	// Event hub scoped rules rotate independently, and their connection
	// strings carry the EntityPath.
	hubs := clientFactory.NewEventHubsClient()
	hub := "rotation-hub"
	_, err = hubs.CreateOrUpdate(ctx, rg, ns, hub, armeventhub.Eventhub{
		Properties: &armeventhub.Properties{PartitionCount: to.Ptr[int64](1)},
	}, nil)
	require.NoError(t, err)
	_, err = hubs.CreateOrUpdateAuthorizationRule(ctx, rg, ns, hub, rule, armeventhub.AuthorizationRule{
		Properties: &armeventhub.AuthorizationRuleProperties{
			Rights: []*armeventhub.AccessRights{to.Ptr(armeventhub.AccessRightsListen)},
		},
	}, nil)
	require.NoError(t, err)
	hubKeys, err := hubs.ListKeys(ctx, rg, ns, hub, rule, nil)
	require.NoError(t, err)
	require.NotNil(t, hubKeys.PrimaryConnectionString)
	assert.Contains(t, *hubKeys.PrimaryConnectionString, ";EntityPath="+hub,
		"an event hub scoped connection string must carry the EntityPath")
	hubRegen, err := hubs.RegenerateKeys(ctx, rg, ns, hub, rule, armeventhub.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armeventhub.KeyTypePrimaryKey),
	}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, *hubKeys.PrimaryKey, *hubRegen.PrimaryKey, "the hub scoped rule must rotate")
	assert.Equal(t, *fresh.PrimaryKey, primaryBefore,
		"rotating the hub scoped rule must not disturb the namespace scoped rule")
}

// TestServiceBusARM_RegenerateKeysRotation drives the Service Bus
// authorization-rule key rotation contract at namespace and queue scope:
// regenerateKeys changes only the requested slot, the returned connection
// strings embed the rotated key, listKeys observes the rotation, and deleting
// the queue resets the keys of its scoped rules.
func TestServiceBusARM_RegenerateKeysRotation(t *testing.T) {
	rg := "sb-rotation-rg"
	ns := "sdk-sb-rotation-ns"

	clientFactory, err := armservicebus.NewClientFactory(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	namespaces := clientFactory.NewNamespacesClient()

	createPoller, err := namespaces.BeginCreateOrUpdate(ctx, rg, ns, armservicebus.SBNamespace{
		Location: to.Ptr("eastus"),
		SKU: &armservicebus.SBSKU{
			Name: to.Ptr(armservicebus.SKUNameStandard),
			Tier: to.Ptr(armservicebus.SKUTierStandard),
		},
	}, nil)
	require.NoError(t, err)
	_, err = createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		deletePoller, err := namespaces.BeginDelete(ctx, rg, ns, nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(ctx, nil)
		}
	})

	initial, err := namespaces.ListKeys(ctx, rg, ns, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	require.NotNil(t, initial.PrimaryKey)
	require.NotNil(t, initial.SecondaryKey)
	primaryBefore, secondaryBefore := *initial.PrimaryKey, *initial.SecondaryKey
	require.NotNil(t, initial.PrimaryConnectionString)
	assert.Contains(t, *initial.PrimaryConnectionString, "SharedAccessKey="+primaryBefore,
		"the primary connection string must embed the primary key")

	regen, err := namespaces.RegenerateKeys(ctx, rg, ns, "RootManageSharedAccessKey", armservicebus.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armservicebus.KeyTypePrimaryKey),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, regen.PrimaryKey)
	assert.NotEqual(t, primaryBefore, *regen.PrimaryKey, "regenerating PrimaryKey must change the primary key")
	assert.Equal(t, secondaryBefore, *regen.SecondaryKey, "regenerating PrimaryKey must not change the secondary key")
	require.NotNil(t, regen.PrimaryConnectionString)
	assert.Contains(t, *regen.PrimaryConnectionString, "SharedAccessKey="+*regen.PrimaryKey,
		"the primary connection string must embed the rotated primary key")
	assert.False(t, strings.Contains(*regen.PrimaryConnectionString, "SharedAccessKey="+primaryBefore),
		"the primary connection string must no longer embed the pre-rotation key")

	listed, err := namespaces.ListKeys(ctx, rg, ns, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	assert.Equal(t, *regen.PrimaryKey, *listed.PrimaryKey, "listKeys must return the rotated primary key")
	assert.Equal(t, secondaryBefore, *listed.SecondaryKey, "listKeys must keep the untouched secondary key")

	// Queue scoped rules rotate independently, and deleting the queue resets
	// the keys a later rule of the same name derives.
	queues := clientFactory.NewQueuesClient()
	queue := "rotation-queue"
	rule := "sdk-sb-rotation-rule"
	_, err = queues.CreateOrUpdate(ctx, rg, ns, queue, armservicebus.SBQueue{}, nil)
	require.NoError(t, err)
	_, err = queues.CreateOrUpdateAuthorizationRule(ctx, rg, ns, queue, rule, armservicebus.SBAuthorizationRule{
		Properties: &armservicebus.SBAuthorizationRuleProperties{
			Rights: []*armservicebus.AccessRights{to.Ptr(armservicebus.AccessRightsListen)},
		},
	}, nil)
	require.NoError(t, err)

	queueKeys, err := queues.ListKeys(ctx, rg, ns, queue, rule, nil)
	require.NoError(t, err)
	queuePrimaryBefore := *queueKeys.PrimaryKey

	queueRegen, err := queues.RegenerateKeys(ctx, rg, ns, queue, rule, armservicebus.RegenerateAccessKeyParameters{
		KeyType: to.Ptr(armservicebus.KeyTypePrimaryKey),
	}, nil)
	require.NoError(t, err)
	assert.NotEqual(t, queuePrimaryBefore, *queueRegen.PrimaryKey, "the queue scoped rule must rotate")

	rootAfterQueueRotation, err := namespaces.ListKeys(ctx, rg, ns, "RootManageSharedAccessKey", nil)
	require.NoError(t, err)
	assert.Equal(t, *listed.PrimaryKey, *rootAfterQueueRotation.PrimaryKey,
		"rotating the queue scoped rule must not disturb the namespace scoped rule")

	// Deleting the queue deletes its scoped rules and their rotation state.
	_, err = queues.Delete(ctx, rg, ns, queue, nil)
	require.NoError(t, err)
	_, err = queues.CreateOrUpdate(ctx, rg, ns, queue, armservicebus.SBQueue{}, nil)
	require.NoError(t, err)
	_, err = queues.CreateOrUpdateAuthorizationRule(ctx, rg, ns, queue, rule, armservicebus.SBAuthorizationRule{
		Properties: &armservicebus.SBAuthorizationRuleProperties{
			Rights: []*armservicebus.AccessRights{to.Ptr(armservicebus.AccessRightsListen)},
		},
	}, nil)
	require.NoError(t, err)
	queueFresh, err := queues.ListKeys(ctx, rg, ns, queue, rule, nil)
	require.NoError(t, err)
	assert.Equal(t, queuePrimaryBefore, *queueFresh.PrimaryKey,
		"a rule recreated after the queue was deleted must start from unrotated keys")
}
