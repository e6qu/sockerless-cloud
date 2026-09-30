package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A workflow run executes its actions: the Http action reaches its endpoint
// with the body the expressions built, the condition takes the branch the
// response selects, and an action type the simulator cannot execute fails the
// run and names the type.
func TestLogicApps_RunExecutesActionsSDK(t *testing.T) {
	rg := "logic-exec-rg"
	ensureRG(t, rg)
	cred := &fakeCredential{}
	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	triggers, err := armlogic.NewWorkflowTriggersClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runs, err := armlogic.NewWorkflowRunsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runActions, err := armlogic.NewWorkflowRunActionsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	calls := make(chan map[string]any, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		calls <- decoded
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(backend.Close)

	deploy := func(name string, actions map[string]any) {
		t.Helper()
		_, err := workflows.CreateOrUpdate(ctx, rg, name, armlogic.Workflow{
			Location: to.Ptr("eastus"),
			Properties: &armlogic.WorkflowProperties{Definition: map[string]any{
				"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
				"contentVersion": "1.0.0.0",
				"triggers":       map[string]any{"manual": map[string]any{"type": "Request", "kind": "Http"}},
				"actions":        actions,
				"outputs":        map[string]any{},
			}},
		}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = workflows.Delete(ctx, rg, name, nil) })
	}
	lastRun := func(name string) *armlogic.WorkflowRun {
		t.Helper()
		_, err := triggers.Run(ctx, rg, name, "manual", nil)
		require.NoError(t, err)
		page, err := runs.NewListPager(rg, name, nil).NextPage(ctx)
		require.NoError(t, err)
		require.Len(t, page.Value, 1)
		return page.Value[0]
	}
	actionStatus := func(workflow, run, action string) armlogic.WorkflowStatus {
		t.Helper()
		got, err := runActions.Get(ctx, rg, workflow, run, action, nil)
		require.NoError(t, err)
		return ptrVal(got.Properties.Status)
	}

	deploy("exec-ok", map[string]any{
		"init": map[string]any{"type": "InitializeVariable", "inputs": map[string]any{
			"variables": []any{map[string]any{"name": "who", "type": "string", "value": "sockerless"}}}},
		"call": map[string]any{"type": "Http", "runAfter": map[string]any{"init": []any{"Succeeded"}},
			"inputs": map[string]any{"method": "POST", "uri": backend.URL, "body": map[string]any{"from": "@variables('who')"}}},
		"check": map[string]any{"type": "If", "runAfter": map[string]any{"call": []any{"Succeeded"}},
			"expression": map[string]any{"and": []any{map[string]any{"equals": []any{"@body('call')?['accepted']", true}}}},
			"actions":    map[string]any{"accepted": map[string]any{"type": "Compose", "inputs": "yes"}},
			"else":       map[string]any{"actions": map[string]any{"refused": map[string]any{"type": "Compose", "inputs": "no"}}}},
	})
	run := lastRun("exec-ok")
	assert.Equal(t, armlogic.WorkflowStatusSucceeded, ptrVal(run.Properties.Status))
	select {
	case call := <-calls:
		assert.Equal(t, map[string]any{"from": "sockerless"}, call)
	default:
		t.Fatal("the Http action never reached its endpoint")
	}
	runName := ptrVal(run.Name)
	assert.Equal(t, armlogic.WorkflowStatusSucceeded, actionStatus("exec-ok", runName, "call"))
	assert.Equal(t, armlogic.WorkflowStatusSucceeded, actionStatus("exec-ok", runName, "accepted"))
	_, err = runActions.Get(ctx, rg, "exec-ok", runName, "refused", nil)
	require.Error(t, err, "the branch the condition did not select must not run")

	deploy("exec-unsupported", map[string]any{
		"send": map[string]any{"type": "ApiConnection", "inputs": map[string]any{}},
	})
	run = lastRun("exec-unsupported")
	assert.Equal(t, armlogic.WorkflowStatusFailed, ptrVal(run.Properties.Status))
	send, err := runActions.Get(ctx, rg, "exec-unsupported", ptrVal(run.Name), "send", nil)
	require.NoError(t, err)
	assert.Equal(t, armlogic.WorkflowStatusFailed, ptrVal(send.Properties.Status))
	errBody, _ := json.Marshal(send.Properties.Error)
	assert.Contains(t, string(errBody), "ApiConnection")
}
