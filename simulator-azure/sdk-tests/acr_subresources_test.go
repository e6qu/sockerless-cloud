package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acrEnsureRegistry creates a resource group and an ACR registry so the child
// sub-resource round-trips below have a parent to hang off of.
func acrEnsureRegistry(t *testing.T, rg, registryName string) {
	t.Helper()
	ensureRG(t, rg)
	registriesClient, err := armcontainerregistry.NewRegistriesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	poller, err := registriesClient.BeginCreate(ctx, rg, registryName, armcontainerregistry.Registry{
		Location: ptrStr("eastus"),
		SKU:      &armcontainerregistry.SKU{Name: ptrSKU(armcontainerregistry.SKUNamePremium)},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestACR_ReplicationCRUD round-trips the `replications` sub-resource via
// armcontainerregistry.ReplicationsClient against the simulator.
func TestACR_ReplicationCRUD(t *testing.T) {
	const (
		rg           = "acr-repl-rg"
		registryName = "aclreplregistry"
		replName     = "westus"
	)
	acrEnsureRegistry(t, rg, registryName)

	client, err := armcontainerregistry.NewReplicationsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createPoller, err := client.BeginCreate(ctx, rg, registryName, replName, armcontainerregistry.Replication{
		Location: ptrStr("westus"),
		Properties: &armcontainerregistry.ReplicationProperties{
			RegionEndpointEnabled: to.Ptr(true),
			ZoneRedundancy:        to.Ptr(armcontainerregistry.ZoneRedundancyDisabled),
		},
	}, nil)
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, replName, *created.Name)
	require.NotNil(t, created.Properties)
	require.NotNil(t, created.Properties.RegionEndpointEnabled)
	assert.True(t, *created.Properties.RegionEndpointEnabled)

	got, err := client.Get(ctx, rg, registryName, replName, nil)
	require.NoError(t, err)
	assert.Equal(t, "westus", *got.Location)

	updatePoller, err := client.BeginUpdate(ctx, rg, registryName, replName, armcontainerregistry.ReplicationUpdateParameters{
		Tags: map[string]*string{"env": ptrStr("test")},
	}, nil)
	require.NoError(t, err)
	updated, err := updatePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", *updated.Tags["env"])

	var found bool
	pager := client.NewListPager(rg, registryName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, rrep := range page.Value {
			if rrep != nil && rrep.Name != nil && *rrep.Name == replName {
				found = true
			}
		}
	}
	assert.True(t, found, "List must include the created replication")

	delPoller, err := client.BeginDelete(ctx, rg, registryName, replName, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	_, err = client.Get(ctx, rg, registryName, replName, nil)
	assert.Error(t, err, "Get after delete must fail")
}

// TestACR_WebhookCRUD round-trips the `webhooks` sub-resource, including the
// write-only serviceUri the response schema must NOT echo (getCallbackConfig
// returns it instead).
func TestACR_WebhookCRUD(t *testing.T) {
	const (
		rg           = "acr-webhook-rg"
		registryName = "aclwebhookregistry"
		webhookName  = "buildhook"
	)
	acrEnsureRegistry(t, rg, registryName)

	client, err := armcontainerregistry.NewWebhooksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createPoller, err := client.BeginCreate(ctx, rg, registryName, webhookName, armcontainerregistry.WebhookCreateParameters{
		Location: ptrStr("eastus"),
		Properties: &armcontainerregistry.WebhookPropertiesCreateParameters{
			ServiceURI: ptrStr("https://example.com/hook"),
			Actions:    []*armcontainerregistry.WebhookAction{to.Ptr(armcontainerregistry.WebhookActionPush)},
			Status:     to.Ptr(armcontainerregistry.WebhookStatusEnabled),
			Scope:      ptrStr("myrepo:*"),
			CustomHeaders: map[string]*string{
				"Authorization": ptrStr("Bearer token"),
			},
		},
	}, nil)
	require.NoError(t, err)
	created, err := createPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, webhookName, *created.Name)
	require.NotNil(t, created.Properties)
	require.Len(t, created.Properties.Actions, 1)
	assert.Equal(t, armcontainerregistry.WebhookActionPush, *created.Properties.Actions[0])

	// The callback config (not the Webhook response) carries the secret URI.
	cb, err := client.GetCallbackConfig(ctx, rg, registryName, webhookName, nil)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/hook", *cb.ServiceURI)
	assert.Equal(t, "Bearer token", *cb.CustomHeaders["Authorization"])

	_, err = client.Ping(ctx, rg, registryName, webhookName, nil)
	require.NoError(t, err)

	eventsPager := client.NewListEventsPager(rg, registryName, webhookName, nil)
	for eventsPager.More() {
		_, err := eventsPager.NextPage(ctx)
		require.NoError(t, err)
	}

	delPoller, err := client.BeginDelete(ctx, rg, registryName, webhookName, nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestACR_ScopeMapAndTokenCRUD round-trips the `scopeMaps` and `tokens`
// sub-resources, which back ACR's repository-scoped token authentication.
func TestACR_ScopeMapAndTokenCRUD(t *testing.T) {
	const (
		rg           = "acr-token-rg"
		registryName = "acltokenregistry"
		scopeMapName = "readonly"
		tokenName    = "ci-token"
	)
	acrEnsureRegistry(t, rg, registryName)

	scopeMaps, err := armcontainerregistry.NewScopeMapsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	smPoller, err := scopeMaps.BeginCreate(ctx, rg, registryName, scopeMapName, armcontainerregistry.ScopeMap{
		Properties: &armcontainerregistry.ScopeMapProperties{
			Description: ptrStr("read-only pulls"),
			Actions:     []*string{ptrStr("repositories/myrepo/content/read")},
		},
	}, nil)
	require.NoError(t, err)
	sm, err := smPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, scopeMapName, *sm.Name)
	require.Len(t, sm.Properties.Actions, 1)
	assert.Equal(t, "repositories/myrepo/content/read", *sm.Properties.Actions[0])

	smGet, err := scopeMaps.Get(ctx, rg, registryName, scopeMapName, nil)
	require.NoError(t, err)
	assert.Equal(t, "read-only pulls", *smGet.Properties.Description)

	scopeMapID := *sm.ID
	tokens, err := armcontainerregistry.NewTokensClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	tokPoller, err := tokens.BeginCreate(ctx, rg, registryName, tokenName, armcontainerregistry.Token{
		Properties: &armcontainerregistry.TokenProperties{
			ScopeMapID: ptrStr(scopeMapID),
			Status:     to.Ptr(armcontainerregistry.TokenStatusEnabled),
		},
	}, nil)
	require.NoError(t, err)
	tok, err := tokPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, tokenName, *tok.Name)
	assert.Equal(t, scopeMapID, *tok.Properties.ScopeMapID)

	var foundToken bool
	tokPager := tokens.NewListPager(rg, registryName, nil)
	for tokPager.More() {
		page, err := tokPager.NextPage(ctx)
		require.NoError(t, err)
		for _, tk := range page.Value {
			if tk != nil && tk.Name != nil && *tk.Name == tokenName {
				foundToken = true
			}
		}
	}
	assert.True(t, foundToken, "List must include the created token")

	delTok, err := tokens.BeginDelete(ctx, rg, registryName, tokenName, nil)
	require.NoError(t, err)
	_, err = delTok.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	delSM, err := scopeMaps.BeginDelete(ctx, rg, registryName, scopeMapName, nil)
	require.NoError(t, err)
	_, err = delSM.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// TestACR_TaskAndTaskRunCRUD round-trips the registry-tasks `tasks` and
// `taskRuns` children via armcontainerregistry.{TasksClient,TaskRunsClient}.
func TestACR_TaskAndTaskRunCRUD(t *testing.T) {
	const (
		rg           = "acr-task-rg"
		registryName = "acltaskregistry"
		taskName     = "build-task"
		taskRunName  = "manual-run"
	)
	acrEnsureRegistry(t, rg, registryName)

	tasksClient, err := armcontainerregistry.NewTasksClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	taskPoller, err := tasksClient.BeginCreate(ctx, rg, registryName, taskName, armcontainerregistry.Task{
		Location: ptrStr("eastus"),
		Properties: &armcontainerregistry.TaskProperties{
			Status:   to.Ptr(armcontainerregistry.TaskStatusEnabled),
			Platform: &armcontainerregistry.PlatformProperties{OS: to.Ptr(armcontainerregistry.OSLinux)},
		},
	}, nil)
	require.NoError(t, err)
	task, err := taskPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, taskName, *task.Name)
	require.NotNil(t, task.Properties)
	require.NotNil(t, task.Properties.Platform)
	assert.Equal(t, armcontainerregistry.OSLinux, *task.Properties.Platform.OS)

	taskGet, err := tasksClient.Get(ctx, rg, registryName, taskName, nil)
	require.NoError(t, err)
	assert.Equal(t, "eastus", *taskGet.Location)

	details, err := tasksClient.GetDetails(ctx, rg, registryName, taskName, nil)
	require.NoError(t, err)
	assert.Equal(t, taskName, *details.Name)

	taskRuns, err := armcontainerregistry.NewTaskRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	trPoller, err := taskRuns.BeginCreate(ctx, rg, registryName, taskRunName, armcontainerregistry.TaskRun{
		Location: ptrStr("eastus"),
		Properties: &armcontainerregistry.TaskRunProperties{
			ForceUpdateTag: ptrStr("run-1"),
		},
	}, nil)
	require.NoError(t, err)
	tr, err := trPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, taskRunName, *tr.Name)
	assert.Equal(t, "run-1", *tr.Properties.ForceUpdateTag)

	delTR, err := taskRuns.BeginDelete(ctx, rg, registryName, taskRunName, nil)
	require.NoError(t, err)
	_, err = delTR.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	delTask, err := tasksClient.BeginDelete(ctx, rg, registryName, taskName, nil)
	require.NoError(t, err)
	_, err = delTask.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}
