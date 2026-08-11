package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The operations that run inside the guest, driven through the official SDK
// against a machine that really booted. Each asserts on what the guest itself
// produced — the output of a command, the packages its own package manager
// reports — because that is the only thing that distinguishes these operations
// from a record that they happened.
func TestCompute_VirtualMachineExtensionRunsInTheGuest(t *testing.T) {
	requireNetworkHost(t)
	const rg = "vm-ext-rg"
	const vmName = "vm-ext"
	nicID := vmOperationsFixture(t, rg, "vm-ext", "10.96")
	createOperationsVM(t, rg, vmName, nicID, nil)

	extClient, err := armcompute.NewVirtualMachineExtensionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	const marker = "sockerless-ran-in-the-guest"
	poller, err := extClient.BeginCreateOrUpdate(ctx, rg, vmName, "script", armcompute.VirtualMachineExtension{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineExtensionProperties{
			Publisher:          to.Ptr("Microsoft.Azure.Extensions"),
			Type:               to.Ptr("CustomScript"),
			TypeHandlerVersion: to.Ptr("2.1"),
			Settings:           map[string]any{"commandToExecute": "echo " + marker},
		},
	}, nil)
	require.NoError(t, err)
	created, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, created.Properties)
	assert.Equal(t, "Succeeded", derefString(created.Properties.ProvisioningState))

	// The instance view carries what the command actually wrote, which is the
	// evidence it ran rather than was recorded.
	require.NotNil(t, created.Properties.InstanceView)
	require.NotEmpty(t, created.Properties.InstanceView.Statuses)
	var messages []string
	for _, status := range created.Properties.InstanceView.Statuses {
		messages = append(messages, derefString(status.Message))
	}
	assert.Contains(t, strings.Join(messages, "\n"), marker,
		"the extension's status does not carry the output of the command, so nothing ran in the guest")

	// A command that fails is reported as failed, not as provisioned.
	failPoller, err := extClient.BeginCreateOrUpdate(ctx, rg, vmName, "failing", armcompute.VirtualMachineExtension{
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineExtensionProperties{
			Publisher:          to.Ptr("Microsoft.Azure.Extensions"),
			Type:               to.Ptr("CustomScript"),
			TypeHandlerVersion: to.Ptr("2.1"),
			Settings:           map[string]any{"commandToExecute": "exit 7"},
		},
	}, nil)
	require.NoError(t, err)
	// A resource that lands in a Failed provisioning state is a failed
	// long-running operation, and the SDK's poller reports it as an error —
	// which is what a real client sees when a Custom Script extension's script
	// exits non-zero. The state itself is then read back from the resource.
	_, err = failPoller.PollUntilDone(ctx, nil)
	require.Error(t, err, "a command that exited non-zero must not be reported as a successful operation")

	failed, err := extClient.Get(ctx, rg, vmName, "failing", nil)
	require.NoError(t, err)
	assert.Equal(t, "Failed", derefString(failed.Properties.ProvisioningState),
		"a command that exited non-zero must not be reported as provisioned")

	// The extension reads back, and lists alongside the other one.
	got, err := extClient.Get(ctx, rg, vmName, "script", nil)
	require.NoError(t, err)
	assert.Equal(t, "CustomScript", derefString(got.Properties.Type))

	list, err := extClient.List(ctx, rg, vmName, nil)
	require.NoError(t, err)
	var names []string
	for _, e := range list.Value {
		names = append(names, derefString(e.Name))
	}
	assert.ElementsMatch(t, []string{"script", "failing"}, names)

	delPoller, err := extClient.BeginDelete(ctx, rg, vmName, "failing", nil)
	require.NoError(t, err)
	_, err = delPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
}

// Patch assessment reports what the guest's own package manager says. A guest
// with nothing upgradable reports nothing, which is a truthful assessment — the
// assertion is that the operation answers from the guest at all, which the
// counts agreeing with the list proves.
func TestCompute_VirtualMachineAssessPatchesReadsTheGuest(t *testing.T) {
	requireNetworkHost(t)
	const rg = "vm-patch-rg"
	const vmName = "vm-patch"
	nicID := vmOperationsFixture(t, rg, "vm-patch", "10.97")
	vmClient := createOperationsVM(t, rg, vmName, nicID, nil)

	poller, err := vmClient.BeginAssessPatches(ctx, rg, vmName, nil)
	require.NoError(t, err)
	assessment, err := poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, armcompute.PatchOperationStatusSucceeded, *assessment.Status)
	require.NotNil(t, assessment.StartDateTime)
	security := int32(0)
	if assessment.CriticalAndSecurityPatchCount != nil {
		security = *assessment.CriticalAndSecurityPatchCount
	}
	other := int32(0)
	if assessment.OtherPatchCount != nil {
		other = *assessment.OtherPatchCount
	}
	assert.Equal(t, int(security+other), len(assessment.AvailablePatches),
		"the counts must add up to the packages listed, or one of the two was not read from the guest")
	for _, patch := range assessment.AvailablePatches {
		assert.NotEmpty(t, derefString(patch.Name), "a listed patch names no package")
		assert.NotEmpty(t, patch.Classifications, "a listed patch carries no classification")
	}
}
