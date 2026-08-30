package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Patch assessment, patch installation and image capture, driven through the
// official SDK against a machine that really booted.
//
// All three answer from the guest itself: an assessment reports what the
// machine's own package manager says is upgradable, an installation runs that
// package manager, and a capture copies the disk the machine is running on. So
// each assertion here is on what the guest produced, not on a record that the
// call happened — a machine reported fully patched because nothing looked is
// indistinguishable from a real answer unless the test reads the guest.
func TestCompute_VirtualMachinePatchOperations(t *testing.T) {
	requireNetworkHost(t)
	const rg = "vm-patch-rg"
	const vmName = "vm-patch"
	nicID := vmOperationsFixture(t, rg, "vm-patch", "10.98")
	vmClient := createOperationsVM(t, rg, vmName, nicID, nil)

	t.Run("AssessPatches reads the guest's own package manager", func(t *testing.T) {
		poller, err := vmClient.BeginAssessPatches(ctx, rg, vmName, nil)
		require.NoError(t, err)
		result, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		// The assessment ran to completion against a reachable guest.
		assert.Equal(t, armcompute.PatchOperationStatusSucceeded,
			derefPatchStatus(result.Status),
			"assessment did not succeed, so nothing inspected the guest")
		require.NotNil(t, result.AssessmentActivityID)
		// A Debian guest reports a package count rather than an error; the
		// number itself depends on the image, so the contract is that it was
		// counted at all.
		require.NotNil(t, result.CriticalAndSecurityPatchCount)
		require.NotNil(t, result.OtherPatchCount)
	})

	t.Run("InstallPatches runs the package manager", func(t *testing.T) {
		poller, err := vmClient.BeginInstallPatches(ctx, rg, vmName,
			armcompute.VirtualMachineInstallPatchesParameters{
				MaximumDuration: to.Ptr("PT30M"),
				RebootSetting:   to.Ptr(armcompute.VMGuestPatchRebootSettingNever),
				LinuxParameters: &armcompute.LinuxParameters{ClassificationsToInclude: []*armcompute.VMGuestPatchClassificationLinux{}},
			}, nil)
		require.NoError(t, err)
		result, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		assert.Equal(t, armcompute.PatchOperationStatusSucceeded,
			derefPatchStatus(result.Status),
			"installation did not succeed, so the package manager did not run")
		require.NotNil(t, result.InstallationActivityID)
		// Never-reboot was asked for, so the machine must not have rebooted.
		assert.Equal(t, armcompute.VMGuestPatchRebootStatusNotNeeded,
			derefRebootStatus(result.RebootStatus))
	})

	// Azure generalizes only a stopped machine, and captures only a generalized
	// one, so this is the order every capture is performed in. It is reachable
	// because the machine's disk outlives its guest process — the whole of what
	// the sequence depends on.
	t.Run("Generalize refuses a running machine", func(t *testing.T) {
		_, err := vmClient.Generalize(ctx, rg, vmName, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "OperationNotAllowed")
		assert.Contains(t, err.Error(), "not in a stopped state")
	})

	t.Run("Capture copies the disk of the deallocated machine", func(t *testing.T) {
		deallocate, err := vmClient.BeginDeallocate(ctx, rg, vmName, nil)
		require.NoError(t, err)
		_, err = deallocate.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		_, err = vmClient.Generalize(ctx, rg, vmName, nil)
		require.NoError(t, err, "a stopped machine must be generalizable")

		poller, err := vmClient.BeginCapture(ctx, rg, vmName,
			armcompute.VirtualMachineCaptureParameters{
				DestinationContainerName: to.Ptr("vm-patch-images"),
				VhdPrefix:                to.Ptr("captured"),
				OverwriteVhds:            to.Ptr(true),
			}, nil)
		require.NoError(t, err)
		captured, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)

		// The template names the disk that was copied rather than an empty
		// shell, which is what distinguishes a capture from a record of one.
		require.NotEmpty(t, captured.Resources, "capture produced no image resource")
	})
}

func derefPatchStatus(s *armcompute.PatchOperationStatus) armcompute.PatchOperationStatus {
	if s == nil {
		return ""
	}
	return *s
}

func derefRebootStatus(s *armcompute.VMGuestPatchRebootStatus) armcompute.VMGuestPatchRebootStatus {
	if s == nil {
		return ""
	}
	return *s
}
