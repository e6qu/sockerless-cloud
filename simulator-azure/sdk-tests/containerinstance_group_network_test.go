package azure_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance"
	"github.com/stretchr/testify/require"
)

// The containers of one group share a network namespace, so one reaches
// another's listener on localhost.
func TestACIGroupContainersShareLocalhost(t *testing.T) {
	t.Parallel()
	rg := "aci-localhost-rg"
	groupName := "aci-localhost-group"
	ensureRG(t, rg)
	groups, err := armcontainerinstance.NewContainerGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	containers, err := armcontainerinstance.NewContainersClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	cpu, mem := 1.0, 1.0
	container := func(name string, command ...string) *armcontainerinstance.Container {
		return &armcontainerinstance.Container{
			Name: to.Ptr(name),
			Properties: &armcontainerinstance.ContainerProperties{
				Image:   to.Ptr(commandImageName),
				Command: to.SliceOfPtrs(command...),
				Resources: &armcontainerinstance.ResourceRequirements{
					Requests: &armcontainerinstance.ResourceRequests{CPU: &cpu, MemoryInGB: &mem},
				},
			},
		}
	}
	poller, err := groups.BeginCreateOrUpdate(ctx, rg, groupName, armcontainerinstance.ContainerGroup{
		Location: to.Ptr("eastus"),
		Properties: &armcontainerinstance.ContainerGroupProperties{
			OSType:        to.Ptr(armcontainerinstance.OperatingSystemTypesLinux),
			RestartPolicy: to.Ptr(armcontainerinstance.ContainerGroupRestartPolicyNever),
			Containers: []*armcontainerinstance.Container{
				container("server", "/usr/local/bin/container-command", "http", "8080", "from-the-group"),
				container("client", "/usr/local/bin/container-command", "probe-http", "http://localhost:8080/", "from-the-group", "30"),
			},
		},
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		deletePoller, err := groups.BeginDelete(ctx, rg, groupName, nil)
		if err == nil {
			_, _ = deletePoller.PollUntilDone(ctx, nil)
		}
	})

	// Container Instances offers nothing to wait on for a container's output.
	require.Eventually(t, func() bool {
		logs, err := containers.ListLogs(ctx, rg, groupName, "client", nil)
		return err == nil && strings.Contains(ptrVal(logs.Content), "from-the-group")
	}, 60*time.Second, 250*time.Millisecond, "the client reaches the server on localhost")
}
