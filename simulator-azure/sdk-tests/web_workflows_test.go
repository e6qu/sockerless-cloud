package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// web_workflows_test.go exercises the App-Service-hosted Logic Apps workflow
// surface in simulator-azure/web_workflows.go: deployWorkflowArtifacts, the
// WorkflowEnvelope read surface (production + slot), listWorkflowsConnections,
// and the hostruntime management bridge (workflows, runs, run actions,
// triggers, trigger histories, versions, access keys) through the
// armappservice Workflow* clients — the same entities the standalone
// Microsoft.Logic slice stores, addressed at their site-scoped coordinate.

// webWorkflowsDeploy pushes one workflow (and a connections manifest) into a
// site through the real deployWorkflowArtifacts surface.
func webWorkflowsDeploy(t *testing.T, client *armappservice.WebAppsClient, rg, site string) {
	t.Helper()
	_, err := client.DeployWorkflowArtifacts(ctx, rg, site, &armappservice.WebAppsClientDeployWorkflowArtifactsOptions{
		WorkflowArtifacts: &armappservice.WorkflowArtifacts{
			AppSettings: map[string]*string{"WORKFLOWS_SUBSCRIPTION_ID": to.Ptr(subscriptionID)},
			Files: map[string]any{
				"wf1/workflow.json": map[string]any{
					"kind": "Stateful",
					"definition": map[string]any{
						"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
						"contentVersion": "1.0.0.0",
						"triggers": map[string]any{
							"manual": map[string]any{"type": "Request", "kind": "Http"},
						},
						"actions": map[string]any{
							"respond": map[string]any{"type": "Response"},
						},
						"outputs": map[string]any{},
					},
				},
				"connections.json": map[string]any{
					"managedApiConnections": map[string]any{},
				},
			},
		},
	})
	require.NoError(t, err)
}

func TestSDK_WebWorkflows_DeployAndEnvelopes(t *testing.T) {
	rg := "webwf-envelope-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "webwf-env-plan")

	client, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name, slot := "webwf-env-site", "staging"
	webMoreCreateSite(t, client, rg, name, planID)
	webChildrenCreateSlot(t, client, rg, name, slot, planID)
	webWorkflowsDeploy(t, client, rg, name)

	// deployWorkflowArtifacts merges the artifact appSettings into the site's
	// application settings.
	settings, err := client.ListApplicationSettings(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, settings.Properties["WORKFLOWS_SUBSCRIPTION_ID"])
	assert.Equal(t, subscriptionID, *settings.Properties["WORKFLOWS_SUBSCRIPTION_ID"])

	// The deployed workflow appears as a WorkflowEnvelope, files included.
	got, err := client.GetWorkflow(ctx, rg, name, "wf1", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, armappservice.WorkflowStateEnabled, *got.Properties.FlowState)
	require.Contains(t, got.Properties.Files, "workflow.json")
	require.NotNil(t, got.Kind)
	assert.Equal(t, "Stateful", *got.Kind)

	found := false
	pager := client.NewListWorkflowsPager(rg, name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, wf := range page.Value {
			if wf.Name != nil && *wf.Name == "wf1" {
				found = true
			}
		}
	}
	assert.True(t, found, "deployed workflow must appear in the workflow list")

	// The shared connections manifest reads back through
	// listWorkflowsConnections.
	conns, err := client.ListWorkflowsConnections(ctx, rg, name, nil)
	require.NoError(t, err)
	require.NotNil(t, conns.Properties)
	assert.Contains(t, conns.Properties.Files, "connections.json")

	// Slot deployments are the slot's own: deploy a different workflow to the
	// slot and read it back through the instance-slot surface.
	_, err = client.DeployWorkflowArtifactsSlot(ctx, rg, name, slot, &armappservice.WebAppsClientDeployWorkflowArtifactsSlotOptions{
		WorkflowArtifacts: &armappservice.WorkflowArtifacts{
			Files: map[string]any{
				"slotwf/workflow.json": map[string]any{
					"kind":       "Stateless",
					"definition": map[string]any{"triggers": map[string]any{}, "actions": map[string]any{}},
				},
			},
		},
	})
	require.NoError(t, err)

	slotGot, err := client.GetInstanceWorkflowSlot(ctx, rg, name, slot, "slotwf", nil)
	require.NoError(t, err)
	require.NotNil(t, slotGot.Properties)

	slotNames := map[string]bool{}
	slotPager := client.NewListInstanceWorkflowsSlotPager(rg, name, slot, nil)
	for slotPager.More() {
		page, err := slotPager.NextPage(ctx)
		require.NoError(t, err)
		for _, wf := range page.Value {
			if wf.Name != nil {
				slotNames[*wf.Name] = true
			}
		}
	}
	assert.True(t, slotNames["slotwf"], "slot workflow must appear in the slot list")
	assert.False(t, slotNames["wf1"], "production workflows must not leak into the slot list")

	// The slot's connections manifest is empty — nothing was deployed there.
	slotConns, err := client.ListWorkflowsConnectionsSlot(ctx, rg, name, slot, nil)
	require.NoError(t, err)
	require.NotNil(t, slotConns.Properties)
	assert.NotContains(t, slotConns.Properties.Files, "connections.json")

	// Deleting the file removes the workflow.
	_, err = client.DeployWorkflowArtifacts(ctx, rg, name, &armappservice.WebAppsClientDeployWorkflowArtifactsOptions{
		WorkflowArtifacts: &armappservice.WorkflowArtifacts{
			FilesToDelete: []*string{to.Ptr("wf1/workflow.json")},
		},
	})
	require.NoError(t, err)
	_, err = client.GetWorkflow(ctx, rg, name, "wf1", nil)
	require.Error(t, err, "a workflow whose file was deleted must not read back")
}

func TestSDK_WebWorkflows_HostruntimeBridge(t *testing.T) {
	rg := "webwf-runtime-rg"
	ensureRG(t, rg)
	planID := webMoreEnsurePlan(t, rg, "webwf-rt-plan")

	webApps, err := armappservice.NewWebAppsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	name := "webwf-rt-site"
	webMoreCreateSite(t, webApps, rg, name, planID)
	webWorkflowsDeploy(t, webApps, rg, name)

	cred := &fakeCredential{}
	workflows, err := armappservice.NewWorkflowsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	triggers, err := armappservice.NewWorkflowTriggersClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runs, err := armappservice.NewWorkflowRunsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	actions, err := armappservice.NewWorkflowRunActionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	repetitions, err := armappservice.NewWorkflowRunActionRepetitionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	histories, err := armappservice.NewWorkflowTriggerHistoriesClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	versions, err := armappservice.NewWorkflowVersionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	// Validate accepts the deployed definition shape.
	_, err = workflows.Validate(ctx, rg, name, "wf1", armappservice.Workflow{
		Properties: &armappservice.WorkflowProperties{},
	}, nil)
	require.NoError(t, err)

	// Triggers: list, get, schema.
	triggerNames := map[string]bool{}
	trigPager := triggers.NewListPager(rg, name, "wf1", nil)
	for trigPager.More() {
		page, err := trigPager.NextPage(ctx)
		require.NoError(t, err)
		for _, tr := range page.Value {
			if tr.Name != nil {
				triggerNames[*tr.Name] = true
			}
		}
	}
	assert.True(t, triggerNames["manual"], "the deployed manual trigger must be listed")
	trigGot, err := triggers.Get(ctx, rg, name, "wf1", "manual", nil)
	require.NoError(t, err)
	require.NotNil(t, trigGot.Properties)
	_, err = triggers.GetSchemaJSON(ctx, rg, name, "wf1", "manual", nil)
	require.NoError(t, err)

	// The trigger callback URL is signed with the workflow's access key, so
	// regenerating the key changes the signature.
	cb1, err := triggers.ListCallbackURL(ctx, rg, name, "wf1", "manual", nil)
	require.NoError(t, err)
	require.NotNil(t, cb1.Queries)
	require.NotNil(t, cb1.Queries.Sig)
	_, err = workflows.RegenerateAccessKey(ctx, rg, name, "wf1", armappservice.RegenerateActionParameter{
		KeyType: to.Ptr(armappservice.KeyTypePrimary),
	}, nil)
	require.NoError(t, err)
	cb2, err := triggers.ListCallbackURL(ctx, rg, name, "wf1", "manual", nil)
	require.NoError(t, err)
	assert.NotEqual(t, *cb1.Queries.Sig, *cb2.Queries.Sig,
		"regenerateAccessKey must invalidate the previously issued callback signature")

	// Fire the trigger: one run, its actions, and a trigger history appear.
	runPoller, err := triggers.BeginRun(ctx, rg, name, "wf1", "manual", nil)
	require.NoError(t, err)
	_, err = runPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	var runName string
	runPager := runs.NewListPager(rg, name, "wf1", nil)
	for runPager.More() {
		page, err := runPager.NextPage(ctx)
		require.NoError(t, err)
		for _, run := range page.Value {
			runName = *run.Name
		}
	}
	require.NotEmpty(t, runName, "the fired trigger must record a run")

	runGot, err := runs.Get(ctx, rg, name, "wf1", runName, nil)
	require.NoError(t, err)
	require.NotNil(t, runGot.Properties)
	assert.Equal(t, armappservice.WorkflowStatusSucceeded, *runGot.Properties.Status)

	actionNames := map[string]bool{}
	actPager := actions.NewListPager(rg, name, "wf1", runName, nil)
	for actPager.More() {
		page, err := actPager.NextPage(ctx)
		require.NoError(t, err)
		for _, a := range page.Value {
			actionNames[*a.Name] = true
		}
	}
	assert.True(t, actionNames["respond"], "the definition's action must be recorded on the run")
	actGot, err := actions.Get(ctx, rg, name, "wf1", runName, "respond", nil)
	require.NoError(t, err)
	require.NotNil(t, actGot.Properties)
	tracePager := actions.NewListExpressionTracesPager(rg, name, "wf1", runName, "respond", nil)
	for tracePager.More() {
		page, err := tracePager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Inputs)
	}

	// Recorded runs carry no repetition state; the collection is empty.
	repPager := repetitions.NewListPager(rg, name, "wf1", runName, "respond", nil)
	for repPager.More() {
		page, err := repPager.NextPage(ctx)
		require.NoError(t, err)
		assert.Empty(t, page.Value)
	}

	// The bridge and the standalone Microsoft.Logic slice key the same run
	// store differently, and these detail handlers serve both. A run this
	// site never had is not found; one it did have answers, as it did above.
	missingTraces := actions.NewListExpressionTracesPager(rg, name, "wf1",
		"08000000000000000000000000000000", "respond", nil)
	require.True(t, missingTraces.More())
	_, err = missingTraces.NextPage(ctx)
	requireAzureNotFound(t, err, "the expression traces of a run this site never had")

	// Trigger history: list, get, resubmit (adds a second run).
	var histName string
	histPager := histories.NewListPager(rg, name, "wf1", "manual", nil)
	for histPager.More() {
		page, err := histPager.NextPage(ctx)
		require.NoError(t, err)
		for _, h := range page.Value {
			histName = *h.Name
		}
	}
	require.NotEmpty(t, histName, "the fired trigger must record a history entry")
	histGot, err := histories.Get(ctx, rg, name, "wf1", "manual", histName, nil)
	require.NoError(t, err)
	require.NotNil(t, histGot.Properties)

	resubmitPoller, err := histories.BeginResubmit(ctx, rg, name, "wf1", "manual", histName, nil)
	require.NoError(t, err)
	_, err = resubmitPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	runCount := 0
	runPager = runs.NewListPager(rg, name, "wf1", nil)
	for runPager.More() {
		page, err := runPager.NextPage(ctx)
		require.NoError(t, err)
		runCount += len(page.Value)
	}
	assert.Equal(t, 2, runCount, "resubmit must record a second run")

	// Cancel the recorded run.
	_, err = runs.Cancel(ctx, rg, name, "wf1", runName, nil)
	require.NoError(t, err)
	cancelled, err := runs.Get(ctx, rg, name, "wf1", runName, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.WorkflowStatusCancelled, *cancelled.Properties.Status)

	// Versions: each deployment snapshots one.
	var versionID string
	verPager := versions.NewListPager(rg, name, "wf1", nil)
	for verPager.More() {
		page, err := verPager.NextPage(ctx)
		require.NoError(t, err)
		for _, v := range page.Value {
			versionID = *v.Name
		}
	}
	require.NotEmpty(t, versionID, "the deployment must snapshot a workflow version")
	verGot, err := versions.Get(ctx, rg, name, "wf1", versionID, nil)
	require.NoError(t, err)
	require.NotNil(t, verGot.Properties)
}
