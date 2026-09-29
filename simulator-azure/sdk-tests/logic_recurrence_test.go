package azure_sdk_test

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogicApps_RecurrenceTriggerRunsSDK proves a Recurrence trigger runs its
// workflow on its schedule: every second, runs accumulate, each started by
// the recurrence trigger, and a malformed recurrence is refused.
func TestLogicApps_RecurrenceTriggerRunsSDK(t *testing.T) {
	rg := "logic-recurrence-rg"
	wfName := "logic-recurrence-wf"
	cred := &fakeCredential{}
	workflows, err := armlogic.NewWorkflowsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)
	runs, err := armlogic.NewWorkflowRunsClient(subscriptionID, cred, clientOpts())
	require.NoError(t, err)

	definition := func(frequency string) map[string]any {
		return map[string]any{
			"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
			"contentVersion": "1.0.0.0",
			"triggers": map[string]any{"every-second": map[string]any{
				"type":       "Recurrence",
				"recurrence": map[string]any{"frequency": frequency, "interval": 1},
			}},
			"actions": map[string]any{},
			"outputs": map[string]any{},
		}
	}
	_, err = workflows.CreateOrUpdate(ctx, rg, wfName+"-bad", armlogic.Workflow{
		Location:   to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{Definition: definition("Fortnight")},
	}, nil)
	require.Error(t, err)

	_, err = workflows.CreateOrUpdate(ctx, rg, wfName, armlogic.Workflow{
		Location:   to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{Definition: definition("Second")},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = workflows.Delete(ctx, rg, wfName, nil) })

	var listed []*armlogic.WorkflowRun
	require.Eventually(t, func() bool {
		listed = nil
		pager := runs.NewListPager(rg, wfName, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				return false
			}
			listed = append(listed, page.Value...)
		}
		return len(listed) >= 2
	}, 20*time.Second, 250*time.Millisecond, "the recurrence did not run the workflow twice")
	require.NotNil(t, listed[0].Properties)
	require.NotNil(t, listed[0].Properties.Trigger)
	assert.Equal(t, "every-second", *listed[0].Properties.Trigger.Name)
}
