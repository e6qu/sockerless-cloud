package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACIOperationsList exercises the Microsoft.ContainerInstance provider
// operation metadata endpoint (Operations_List).
func TestACIOperationsList(t *testing.T) {
	client, err := armcontainerinstance.NewOperationsClient(&fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := client.NewListPager(nil)
	var names []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, op := range page.Value {
			require.NotNil(t, op.Name)
			require.NotNil(t, op.Display, "operation %s must carry display info", ptrVal(op.Name))
			names = append(names, ptrVal(op.Name))
		}
	}
	require.NotEmpty(t, names)
	assert.Contains(t, names, "Microsoft.ContainerInstance/containerGroups/read")
	assert.Contains(t, names, "Microsoft.ContainerInstance/containerGroups/write")
}

// TestACILocationUsages exercises the per-region usage endpoint
// (Location_ListUsage): the simulator reports container-group consumption
// computed from the groups it actually holds.
func TestACILocationUsages(t *testing.T) {
	client, err := armcontainerinstance.NewLocationClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := client.NewListUsagePager("eastus", nil)
	seen := map[string]bool{}
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, u := range page.Value {
			require.NotNil(t, u.Name)
			seen[ptrVal(u.Name.Value)] = true
			require.NotNil(t, u.Limit)
			require.NotNil(t, u.CurrentValue)
		}
	}
	assert.True(t, seen["ContainerGroups"], "ContainerGroups usage must be reported")
	assert.True(t, seen["StandardCores"], "StandardCores usage must be reported")
}

// TestACILocationCapabilities exercises the per-region capability endpoint
// (Location_ListCapabilities).
func TestACILocationCapabilities(t *testing.T) {
	client, err := armcontainerinstance.NewLocationClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := client.NewListCapabilitiesPager("eastus", nil)
	foundLinux := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, c := range page.Value {
			if ptrVal(c.OSType) == "Linux" {
				foundLinux = true
				require.NotNil(t, c.Capabilities)
				require.NotNil(t, c.Capabilities.MaxCPU)
				assert.Greater(t, ptrVal(c.Capabilities.MaxCPU), float32(0))
			}
		}
	}
	assert.True(t, foundLinux, "a Linux containerGroups capability must be reported")
}

// TestACILocationCachedImages exercises the cached-images endpoint
// (Location_ListCachedImages). The simulator maintains no platform image cache,
// so the pager simply iterates without error.
func TestACILocationCachedImages(t *testing.T) {
	client, err := armcontainerinstance.NewLocationClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	pager := client.NewListCachedImagesPager("eastus", nil)
	for pager.More() {
		_, err := pager.NextPage(ctx)
		require.NoError(t, err)
	}
}

// TestACIOutboundNetworkDependenciesAndAttach exercises a container group's
// outbound network dependency endpoints (always empty) and the container attach
// handshake (Containers_Attach), which returns a websocket URI for a running
// container.
func TestACIOutboundNetworkDependenciesAndAttach(t *testing.T) {
	rg := "aci-ops-rg"
	groupName := "aci-ops-group"
	ensureRG(t, rg)

	groups, err := armcontainerinstance.NewContainerGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	cpu := 1.0
	mem := 1.0
	poller, err := groups.BeginCreateOrUpdate(ctx, rg, groupName, armcontainerinstance.ContainerGroup{
		Location: to.Ptr("eastus"),
		Properties: &armcontainerinstance.ContainerGroupProperties{
			OSType:        to.Ptr(armcontainerinstance.OperatingSystemTypesLinux),
			RestartPolicy: to.Ptr(armcontainerinstance.ContainerGroupRestartPolicyNever),
			Containers: []*armcontainerinstance.Container{{
				Name: to.Ptr("main"),
				Properties: &armcontainerinstance.ContainerProperties{
					Image:   to.Ptr(commandImageName),
					Command: []*string{to.Ptr("/usr/local/bin/container-command"), to.Ptr("hold")},
					Resources: &armcontainerinstance.ResourceRequirements{
						Requests: &armcontainerinstance.ResourceRequests{CPU: &cpu, MemoryInGB: &mem},
					},
				},
			}},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		delPoller, err := groups.BeginDelete(ctx, rg, groupName, nil)
		if err == nil {
			_, _ = delPoller.PollUntilDone(ctx, nil)
		}
	})

	deps, err := groups.GetOutboundNetworkDependenciesEndpoints(ctx, rg, groupName, nil)
	require.NoError(t, err)
	assert.Empty(t, deps.StringArray, "outbound network dependency endpoints must be an empty list")

	containers, err := armcontainerinstance.NewContainersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	attach, err := containers.Attach(ctx, rg, groupName, "main", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, ptrVal(attach.WebSocketURI), "attach must return a websocket URI")
	assert.NotEmpty(t, ptrVal(attach.Password), "attach must return a password")
}
