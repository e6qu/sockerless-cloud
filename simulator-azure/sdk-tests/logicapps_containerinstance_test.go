package azure_sdk_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogicApps_WorkflowLifecycleSDK(t *testing.T) {
	client, err := armlogic.NewWorkflowsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	triggers, err := armlogic.NewWorkflowTriggersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	runs, err := armlogic.NewWorkflowRunsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	definition := map[string]any{
		"$schema":        "https://schema.management.azure.com/providers/Microsoft.Logic/schemas/2016-06-01/workflowdefinition.json#",
		"contentVersion": "1.0.0.0",
		"triggers":       map[string]any{"manual": map[string]any{"type": "Request", "kind": "Http"}},
		"actions":        map[string]any{},
		"outputs":        map[string]any{},
	}
	created, err := client.CreateOrUpdate(ctx, "sdk-rg", "sdk-logic", armlogic.Workflow{
		Location: to.Ptr("eastus"),
		Tags:     map[string]*string{"env": to.Ptr("sdk")},
		Properties: &armlogic.WorkflowProperties{
			Definition: definition,
			Parameters: map[string]*armlogic.WorkflowParameter{},
		},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.Delete(ctx, "sdk-rg", "sdk-logic", nil)
	})
	require.NotNil(t, created.Properties)
	assert.Equal(t, "sdk-logic", ptrVal(created.Name))
	assert.Equal(t, armlogic.WorkflowStateEnabled, ptrVal(created.Properties.State))
	assert.Equal(t, armlogic.WorkflowProvisioningStateSucceeded, ptrVal(created.Properties.ProvisioningState))

	_, err = client.ValidateByResourceGroup(ctx, "sdk-rg", "sdk-logic", armlogic.Workflow{
		Location:   to.Ptr("eastus"),
		Properties: &armlogic.WorkflowProperties{Definition: definition},
	}, nil)
	require.NoError(t, err)

	_, err = client.Disable(ctx, "sdk-rg", "sdk-logic", nil)
	require.NoError(t, err)
	got, err := client.Get(ctx, "sdk-rg", "sdk-logic", nil)
	require.NoError(t, err)
	require.NotNil(t, got.Properties)
	assert.Equal(t, armlogic.WorkflowStateDisabled, ptrVal(got.Properties.State))

	_, err = client.Enable(ctx, "sdk-rg", "sdk-logic", nil)
	require.NoError(t, err)
	got, err = client.Get(ctx, "sdk-rg", "sdk-logic", nil)
	require.NoError(t, err)
	assert.Equal(t, armlogic.WorkflowStateEnabled, ptrVal(got.Properties.State))

	_, err = triggers.Run(ctx, "sdk-rg", "sdk-logic", "manual", nil)
	require.NoError(t, err)
	runPager := runs.NewListPager("sdk-rg", "sdk-logic", nil)
	require.True(t, runPager.More())
	runPage, err := runPager.NextPage(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, runPage.Value)
	runName := ptrVal(runPage.Value[0].Name)
	assert.Equal(t, armlogic.WorkflowStatusSucceeded, ptrVal(runPage.Value[0].Properties.Status))
	run, err := runs.Get(ctx, "sdk-rg", "sdk-logic", runName, nil)
	require.NoError(t, err)
	assert.Equal(t, armlogic.WorkflowStatusSucceeded, ptrVal(run.Properties.Status))
	_, err = runs.Cancel(ctx, "sdk-rg", "sdk-logic", runName, nil)
	require.NoError(t, err)
	run, err = runs.Get(ctx, "sdk-rg", "sdk-logic", runName, nil)
	require.NoError(t, err)
	assert.Equal(t, armlogic.WorkflowStatusCancelled, ptrVal(run.Properties.Status))

	pager := client.NewListByResourceGroupPager("sdk-rg", nil)
	found := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, wf := range page.Value {
			if ptrVal(wf.Name) == "sdk-logic" {
				found = true
			}
		}
	}
	assert.True(t, found)
}

func TestContainerInstances_GroupLogsExecSDK(t *testing.T) {
	groups, err := armcontainerinstance.NewContainerGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	containers, err := armcontainerinstance.NewContainersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	createACIGroup := func(name string, command []*string) {
		t.Helper()
		cpu := 1.0
		mem := 1.0
		poller, err := groups.BeginCreateOrUpdate(ctx, "sdk-rg", name, armcontainerinstance.ContainerGroup{
			Location: to.Ptr("eastus"),
			Properties: &armcontainerinstance.ContainerGroupProperties{
				OSType:        to.Ptr(armcontainerinstance.OperatingSystemTypesLinux),
				RestartPolicy: to.Ptr(armcontainerinstance.ContainerGroupRestartPolicyNever),
				Containers: []*armcontainerinstance.Container{{
					Name: to.Ptr("main"),
					Properties: &armcontainerinstance.ContainerProperties{
						Image:   to.Ptr(commandImageName),
						Command: command,
						Resources: &armcontainerinstance.ResourceRequirements{
							Requests: &armcontainerinstance.ResourceRequests{
								CPU:        &cpu,
								MemoryInGB: &mem,
							},
						},
					},
				}},
			},
			Tags: map[string]*string{"env": to.Ptr("sdk")},
		}, nil)
		require.NoError(t, err)
		created, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, created.Properties)
		assert.Equal(t, "Succeeded", ptrVal(created.Properties.ProvisioningState))
		require.NotEmpty(t, created.Properties.Containers)
		require.NotNil(t, created.Properties.Containers[0].Properties)
		require.NotNil(t, created.Properties.Containers[0].Properties.Resources)
		require.NotNil(t, created.Properties.Containers[0].Properties.Resources.Requests)
		assert.Equal(t, cpu, ptrVal(created.Properties.Containers[0].Properties.Resources.Requests.CPU))
		assert.Equal(t, mem, ptrVal(created.Properties.Containers[0].Properties.Resources.Requests.MemoryInGB))
		require.NotNil(t, created.Properties.Containers[0].Properties.Ports)
		assert.Empty(t, created.Properties.Containers[0].Properties.Ports)
		require.NotNil(t, created.Properties.Containers[0].Properties.EnvironmentVariables)
		assert.Empty(t, created.Properties.Containers[0].Properties.EnvironmentVariables)
	}

	createACIGroup("sdk-aci-logs", []*string{to.Ptr("/usr/local/bin/container-command"), to.Ptr("log"), to.Ptr("aci-sdk-ready")})
	t.Cleanup(func() {
		deletePoller, err := groups.BeginDelete(ctx, "sdk-rg", "sdk-aci-logs", nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(ctx, nil)
		}
	})
	require.Eventually(t, func() bool {
		logs, err := containers.ListLogs(ctx, "sdk-rg", "sdk-aci-logs", "main", &armcontainerinstance.ContainersClientListLogsOptions{
			Tail: to.Ptr[int32](20),
		})
		return err == nil && logs.Content != nil && strings.Contains(ptrVal(logs.Content), "aci-sdk-ready")
		// Generous deadline so a slow real-container start on a loaded CI runner
		// doesn't expire before the log line is emitted.
	}, 30*time.Second, 250*time.Millisecond)

	createACIGroup("sdk-aci", []*string{to.Ptr("/usr/local/bin/container-command"), to.Ptr("hold")})
	t.Cleanup(func() {
		deletePoller, err := groups.BeginDelete(ctx, "sdk-rg", "sdk-aci", nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(ctx, nil)
		}
	})

	execResp, err := containers.ExecuteCommand(ctx, "sdk-rg", "sdk-aci", "main", armcontainerinstance.ContainerExecRequest{
		Command: to.Ptr("/usr/local/bin/container-command print aci-sdk-exec"),
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, ptrVal(execResp.WebSocketURI))
	require.NotEmpty(t, ptrVal(execResp.Password))

	conn, resp, err := websocket.DefaultDialer.Dial(ptrVal(execResp.WebSocketURI), nil)
	var respBody []byte
	if resp != nil {
		respBody, _ = io.ReadAll(resp.Body)
		defer resp.Body.Close()
	}
	require.NoError(t, err, "websocket dial status=%v body=%s uri=%s", func() any {
		if resp == nil {
			return nil
		}
		return resp.Status
	}(), string(respBody), ptrVal(execResp.WebSocketURI))
	defer conn.Close()
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(msg), "aci-sdk-exec")
}

func ptrVal[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
