package azure_sdk_test

import (
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The operations that run inside the guest of a booted machine. Each asserts
// on what the guest produced, which is all that tells an operation that ran
// from a record that it happened.
func TestCompute_VirtualMachineExtensionRunsInTheGuest(t *testing.T) {
	t.Parallel()
	const rg = "vm-ext-rg"
	const vmName = "vm-ext"
	nicID := vmOperationsFixture(t, rg, "vm-ext", "10.96.0.0/16")
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

	// The instance view carries what the command wrote.
	require.NotNil(t, created.Properties.InstanceView)
	require.NotEmpty(t, created.Properties.InstanceView.Statuses)
	var messages []string
	for _, status := range created.Properties.InstanceView.Statuses {
		messages = append(messages, derefString(status.Message))
	}
	assert.Contains(t, strings.Join(messages, "\n"), marker,
		"the extension's status does not carry the output of the command, so nothing ran in the guest")

	// A command that fails reports failed, not provisioned.
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
	// A resource landing in Failed fails the long-running operation, as when a
	// Custom Script extension's script exits non-zero; read the state back from
	// the resource.
	_, err = failPoller.PollUntilDone(ctx, nil)
	require.Error(t, err, "a command that exited non-zero must not be reported as a successful operation")

	failed, err := extClient.Get(ctx, rg, vmName, "failing", nil)
	require.NoError(t, err)
	assert.Equal(t, "Failed", derefString(failed.Properties.ProvisioningState),
		"a command that exited non-zero must not be reported as provisioned")

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

// Patch assessment reports what the guest's own package manager says; with
// nothing upgradable it reports nothing, so the counts must agree with the
// list.
func TestCompute_VirtualMachineAssessPatchesReadsTheGuest(t *testing.T) {
	t.Parallel()
	const rg = "vm-patch-rg"
	const vmName = "vm-patch"
	nicID := vmOperationsFixture(t, rg, "vm-patch", "10.97.0.0/16")
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
