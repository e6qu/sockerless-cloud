package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ensureRedisResourceGroup creates (idempotently) a resource group for the
// Redis tests.
func ensureRedisResourceGroup(t *testing.T, name string) {
	t.Helper()
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, name, armresources.ResourceGroup{Location: ptrStr("eastus")}, nil)
	require.NoError(t, err)
}

func newRedisCacheForTest(t *testing.T, rg, name string) *armredis.Client {
	t.Helper()
	client, err := armredis.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := client.BeginCreate(ctx, rg, name, armredis.CreateParameters{
		Location: ptrStr("eastus"),
		Properties: &armredis.CreateProperties{
			SKU: &armredis.SKU{
				Name:     ptr(armredis.SKUNameBasic),
				Family:   ptr(armredis.SKUFamilyC),
				Capacity: ptr[int32](0),
			},
		},
	}, nil)
	require.NoError(t, err)
	resp, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, name, *resp.Name)
	return client
}

func TestRedis_CreateGetListLifecycle(t *testing.T) {
	rg := "redis-lifecycle-rg"
	ensureRedisResourceGroup(t, rg)
	client := newRedisCacheForTest(t, rg, "redis-lifecycle")

	got, err := client.Get(ctx, rg, "redis-lifecycle", nil)
	require.NoError(t, err)
	assert.Equal(t, "redis-lifecycle", *got.Name)

	// ListByResourceGroup + ListBySubscription pagers.
	rgPager := client.NewListByResourceGroupPager(rg, nil)
	found := false
	for rgPager.More() {
		page, err := rgPager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			if *c.Name == "redis-lifecycle" {
				found = true
			}
		}
	}
	assert.True(t, found, "cache must appear in ListByResourceGroup")

	subPager := client.NewListBySubscriptionPager(nil)
	for subPager.More() {
		_, err := subPager.NextPage(ctx)
		require.NoError(t, err)
	}
}

func TestRedis_CheckNameAvailability(t *testing.T) {
	rg := "redis-checkname-rg"
	ensureRedisResourceGroup(t, rg)
	client, err := armredis.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	// Fresh, unused name → available.
	resp, err := client.CheckNameAvailability(ctx, armredis.CheckNameAvailabilityParameters{
		Name: ptrStr("redis-never-used-name"),
		Type: ptrStr("Microsoft.Cache/redis"),
	}, nil)
	require.NoError(t, err)
	_ = resp

	// A name already taken by a created cache → unavailable.
	newRedisCacheForTest(t, rg, "redis-taken-name")
	_, err = client.CheckNameAvailability(ctx, armredis.CheckNameAvailabilityParameters{
		Name: ptrStr("redis-taken-name"),
		Type: ptrStr("Microsoft.Cache/redis"),
	}, nil)
	require.Error(t, err, "a taken cache name must surface as a Conflict error from the SDK")
}

func TestRedis_KeysAndReboot(t *testing.T) {
	rg := "redis-keys-rg"
	ensureRedisResourceGroup(t, rg)
	client := newRedisCacheForTest(t, rg, "redis-keys")

	keys, err := client.ListKeys(ctx, rg, "redis-keys", nil)
	require.NoError(t, err)
	require.NotNil(t, keys.PrimaryKey)
	primaryBefore := *keys.PrimaryKey
	secondaryBefore := *keys.SecondaryKey

	regen, err := client.RegenerateKey(ctx, rg, "redis-keys", armredis.RegenerateKeyParameters{
		KeyType: ptr(armredis.RedisKeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, regen.PrimaryKey)
	assert.NotEqual(t, primaryBefore, *regen.PrimaryKey, "regenerating Primary must change the primary key")
	assert.Equal(t, secondaryBefore, *regen.SecondaryKey, "regenerating Primary must not change the secondary key")

	reboot, err := client.ForceReboot(ctx, rg, "redis-keys", armredis.RebootParameters{
		RebootType: ptr(armredis.RebootTypeAllNodes),
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, reboot.Message)
}

func TestRedis_DataActions(t *testing.T) {
	rg := "redis-data-rg"
	ensureRedisResourceGroup(t, rg)
	client := newRedisCacheForTest(t, rg, "redis-data")

	impPoller, err := client.BeginImportData(ctx, rg, "redis-data", armredis.ImportRDBParameters{
		Files:  []*string{ptrStr("https://example.blob.core.windows.net/backups/dump.rdb")},
		Format: ptrStr("RDB"),
	}, nil)
	require.NoError(t, err)
	_, err = impPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	expPoller, err := client.BeginExportData(ctx, rg, "redis-data", armredis.ExportRDBParameters{
		Container: ptrStr("https://example.blob.core.windows.net/backups?sas"),
		Prefix:    ptrStr("export-"),
	}, nil)
	require.NoError(t, err)
	_, err = expPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	flushPoller, err := client.BeginFlushCache(ctx, rg, "redis-data", nil)
	require.NoError(t, err)
	_, err = flushPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	notifPager := client.NewListUpgradeNotificationsPager(rg, "redis-data", 5000, nil)
	for notifPager.More() {
		_, err := notifPager.NextPage(ctx)
		require.NoError(t, err)
	}
}

func TestRedis_AccessPoliciesAndAssignments(t *testing.T) {
	rg := "redis-ap-rg"
	ensureRedisResourceGroup(t, rg)
	newRedisCacheForTest(t, rg, "redis-ap")

	apClient, err := armredis.NewAccessPolicyClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	apPoller, err := apClient.BeginCreateUpdate(ctx, rg, "redis-ap", "policy1", armredis.CacheAccessPolicy{
		Properties: &armredis.CacheAccessPolicyProperties{
			Permissions: ptrStr("+get +hget allkeys"),
		},
	}, nil)
	require.NoError(t, err)
	apResp, err := apPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "policy1", *apResp.Name)

	apGot, err := apClient.Get(ctx, rg, "redis-ap", "policy1", nil)
	require.NoError(t, err)
	require.NotNil(t, apGot.Properties)
	require.NotNil(t, apGot.Properties.Permissions)
	assert.Equal(t, "+get +hget allkeys", *apGot.Properties.Permissions)

	apList := apClient.NewListPager(rg, "redis-ap", nil)
	apCount := 0
	for apList.More() {
		page, err := apList.NextPage(ctx)
		require.NoError(t, err)
		apCount += len(page.Value)
	}
	assert.GreaterOrEqual(t, apCount, 1)

	apaClient, err := armredis.NewAccessPolicyAssignmentClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	apaPoller, err := apaClient.BeginCreateUpdate(ctx, rg, "redis-ap", "assignment1", armredis.CacheAccessPolicyAssignment{
		Properties: &armredis.CacheAccessPolicyAssignmentProperties{
			AccessPolicyName: ptrStr("policy1"),
			ObjectID:         ptrStr("11111111-1111-1111-1111-111111111111"),
			ObjectIDAlias:    ptrStr("test-user"),
		},
	}, nil)
	require.NoError(t, err)
	apaResp, err := apaPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "assignment1", *apaResp.Name)

	apaGot, err := apaClient.Get(ctx, rg, "redis-ap", "assignment1", nil)
	require.NoError(t, err)
	require.NotNil(t, apaGot.Properties)
	assert.Equal(t, "policy1", *apaGot.Properties.AccessPolicyName)

	apaList := apaClient.NewListPager(rg, "redis-ap", nil)
	for apaList.More() {
		_, err := apaList.NextPage(ctx)
		require.NoError(t, err)
	}

	delAssign, err := apaClient.BeginDelete(ctx, rg, "redis-ap", "assignment1", nil)
	require.NoError(t, err)
	_, err = delAssign.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	delPolicy, err := apClient.BeginDelete(ctx, rg, "redis-ap", "policy1", nil)
	require.NoError(t, err)
	_, err = delPolicy.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestRedis_LinkedServers(t *testing.T) {
	rg := "redis-link-rg"
	ensureRedisResourceGroup(t, rg)
	newRedisCacheForTest(t, rg, "redis-primary")
	newRedisCacheForTest(t, rg, "redis-secondary")

	lsClient, err := armredis.NewLinkedServerClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	secondaryID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Cache/Redis/redis-secondary"
	lsPoller, err := lsClient.BeginCreate(ctx, rg, "redis-primary", "redis-secondary", armredis.LinkedServerCreateParameters{
		Properties: &armredis.LinkedServerCreateProperties{
			LinkedRedisCacheID:       ptrStr(secondaryID),
			LinkedRedisCacheLocation: ptrStr("eastus"),
			ServerRole:               ptr(armredis.ReplicationRoleSecondary),
		},
	}, nil)
	require.NoError(t, err)
	lsResp, err := lsPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "redis-secondary", *lsResp.Name)

	lsGot, err := lsClient.Get(ctx, rg, "redis-primary", "redis-secondary", nil)
	require.NoError(t, err)
	assert.Equal(t, "redis-secondary", *lsGot.Name)

	lsList := lsClient.NewListPager(rg, "redis-primary", nil)
	for lsList.More() {
		_, err := lsList.NextPage(ctx)
		require.NoError(t, err)
	}

	lsDel, err := lsClient.BeginDelete(ctx, rg, "redis-primary", "redis-secondary", nil)
	require.NoError(t, err)
	_, err = lsDel.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

func TestRedis_PatchSchedules(t *testing.T) {
	rg := "redis-patch-rg"
	ensureRedisResourceGroup(t, rg)
	newRedisCacheForTest(t, rg, "redis-patch")

	psClient, err := armredis.NewPatchSchedulesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = psClient.CreateOrUpdate(ctx, rg, "redis-patch", armredis.DefaultNameDefault, armredis.PatchSchedule{
		Properties: &armredis.ScheduleEntries{
			ScheduleEntries: []*armredis.ScheduleEntry{{
				DayOfWeek:    ptr(armredis.DayOfWeekMonday),
				StartHourUTC: ptr[int32](2),
			}},
		},
	}, nil)
	require.NoError(t, err)

	got, err := psClient.Get(ctx, rg, "redis-patch", armredis.DefaultNameDefault, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	require.Len(t, got.Properties.ScheduleEntries, 1)

	psList := psClient.NewListByRedisResourcePager(rg, "redis-patch", nil)
	for psList.More() {
		_, err := psList.NextPage(ctx)
		require.NoError(t, err)
	}

	_, err = psClient.Delete(ctx, rg, "redis-patch", armredis.DefaultNameDefault, nil)
	require.NoError(t, err)
}

func TestRedis_PrivateEndpointAndLink(t *testing.T) {
	rg := "redis-pe-rg"
	ensureRedisResourceGroup(t, rg)
	newRedisCacheForTest(t, rg, "redis-pe")

	pecClient, err := armredis.NewPrivateEndpointConnectionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pecPoller, err := pecClient.BeginPut(ctx, rg, "redis-pe", "pec1", armredis.PrivateEndpointConnection{
		Properties: &armredis.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armredis.PrivateLinkServiceConnectionState{
				Status: ptr(armredis.PrivateEndpointServiceConnectionStatusApproved),
			},
		},
	}, nil)
	require.NoError(t, err)
	pecResp, err := pecPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "pec1", *pecResp.Name)

	pecGot, err := pecClient.Get(ctx, rg, "redis-pe", "pec1", nil)
	require.NoError(t, err)
	assert.Equal(t, "pec1", *pecGot.Name)

	pecList := pecClient.NewListPager(rg, "redis-pe", nil)
	for pecList.More() {
		_, err := pecList.NextPage(ctx)
		require.NoError(t, err)
	}

	_, err = pecClient.Delete(ctx, rg, "redis-pe", "pec1", nil)
	require.NoError(t, err)

	plrClient, err := armredis.NewPrivateLinkResourcesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	plrList := plrClient.NewListByRedisCachePager(rg, "redis-pe", nil)
	groupFound := false
	for plrList.More() {
		page, err := plrList.NextPage(ctx)
		require.NoError(t, err)
		for _, g := range page.Value {
			if g.Name != nil && *g.Name == "redisCache" {
				groupFound = true
			}
		}
	}
	assert.True(t, groupFound, "private link resource group 'redisCache' must be present")
}

func TestRedis_OperationsList(t *testing.T) {
	opsClient, err := armredis.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)
	pager := opsClient.NewListPager(nil)
	count := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		count += len(page.Value)
	}
	assert.Greater(t, count, 0, "Microsoft.Cache operations list must be non-empty")
}
