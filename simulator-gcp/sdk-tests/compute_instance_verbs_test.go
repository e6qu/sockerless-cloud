package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The verbs an instance carries beyond its lifecycle. Each answers with an
// Operation, so every assertion reads the instance back: a verb that stored
// nothing would be indistinguishable from one that worked.

// createVerbInstance brings up an instance and waits for the boot, because
// insert answers while the machine is still provisioning and several of these
// verbs are refused on an instance that is not running yet — exactly as
// Compute Engine refuses them.
func createVerbInstance(t *testing.T, svc *compute.Service, project, zone, name string) {
	t.Helper()
	op, err := svc.Instances.Insert(project, zone, &compute.Instance{
		Name:        name,
		MachineType: "zones/" + zone + "/machineTypes/e2-micro",
		Disks: []*compute.AttachedDisk{{
			Boot: true, AutoDelete: true, DeviceName: "boot",
			InitializeParams: &compute.AttachedDiskInitializeParams{
				SourceImage: "projects/debian-cloud/global/images/family/debian-12",
			},
		}},
		NetworkInterfaces: []*compute.NetworkInterface{{Name: "nic0", Network: "global/networks/default"}},
	}).Do()
	require.NoError(t, err)
	awaitZoneOperation(t, svc, project, zone, op.Name)
}

func TestCompute_InstanceSetVerbsAreRememberedByTheInstance(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "verbs-project", "us-central1-a", "worker"
	createVerbInstance(t, svc, project, zone, name)

	_, err := svc.Instances.SetDeletionProtection(project, zone, name).DeletionProtection(true).Do()
	require.NoError(t, err)
	_, err = svc.Instances.SetMinCpuPlatform(project, zone, name,
		&compute.InstancesSetMinCpuPlatformRequest{MinCpuPlatform: "Intel Cascade Lake"}).Do()
	require.NoError(t, err)
	_, err = svc.Instances.SetScheduling(project, zone, name,
		&compute.Scheduling{OnHostMaintenance: "TERMINATE", AutomaticRestart: new(bool)}).Do()
	require.NoError(t, err)
	_, err = svc.Instances.SetServiceAccount(project, zone, name,
		&compute.InstancesSetServiceAccountRequest{
			Email:  "runner@example.iam.gserviceaccount.com",
			Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
		}).Do()
	require.NoError(t, err)

	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.True(t, got.DeletionProtection, "the instance remembers it is protected")
	assert.Equal(t, "Intel Cascade Lake", got.MinCpuPlatform)
	require.NotNil(t, got.Scheduling)
	assert.Equal(t, "TERMINATE", got.Scheduling.OnHostMaintenance)
	require.Len(t, got.ServiceAccounts, 1)
	assert.Equal(t, "runner@example.iam.gserviceaccount.com", got.ServiceAccounts[0].Email)
}

func TestCompute_InstanceResourcePolicies(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "verbs-policies", "us-central1-a", "scheduled"
	createVerbInstance(t, svc, project, zone, name)

	const policy = "regions/us-central1/resourcePolicies/nightly"
	_, err := svc.Instances.AddResourcePolicies(project, zone, name,
		&compute.InstancesAddResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.NoError(t, err)

	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, got.ResourcePolicies, 1)

	// Attaching the same policy twice is refused rather than silently doubled.
	_, err = svc.Instances.AddResourcePolicies(project, zone, name,
		&compute.InstancesAddResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already attached")

	_, err = svc.Instances.RemoveResourcePolicies(project, zone, name,
		&compute.InstancesRemoveResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.NoError(t, err)
	got, err = svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Empty(t, got.ResourcePolicies)

	// Detaching one that is not attached reports that.
	_, err = svc.Instances.RemoveResourcePolicies(project, zone, name,
		&compute.InstancesRemoveResourcePoliciesRequest{ResourcePolicies: []string{policy}}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not attached")
}

func TestCompute_InstanceAccessConfigsAndInterfaces(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "verbs-network", "us-central1-a", "fronted"
	createVerbInstance(t, svc, project, zone, name)

	_, err := svc.Instances.AddAccessConfig(project, zone, name, "nic0",
		&compute.AccessConfig{Name: "external-nat", Type: "ONE_TO_ONE_NAT"}).Do()
	require.NoError(t, err)

	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, got.NetworkInterfaces, 1)
	require.Len(t, got.NetworkInterfaces[0].AccessConfigs, 1)
	assert.Equal(t, "external-nat", got.NetworkInterfaces[0].AccessConfigs[0].Name)

	// A second interface is added and then removed.
	_, err = svc.Instances.AddNetworkInterface(project, zone, name,
		&compute.NetworkInterface{Name: "nic1", Network: "global/networks/default"}).Do()
	require.NoError(t, err)
	got, err = svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, got.NetworkInterfaces, 2)

	_, err = svc.Instances.DeleteNetworkInterface(project, zone, name, "nic1").Do()
	require.NoError(t, err)
	got, err = svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, got.NetworkInterfaces, 1)

	_, err = svc.Instances.DeleteAccessConfig(project, zone, name, "external-nat", "nic0").Do()
	require.NoError(t, err)
	got, err = svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Empty(t, got.NetworkInterfaces[0].AccessConfigs)

	// An interface the instance does not have reports itself.
	_, err = svc.Instances.AddAccessConfig(project, zone, name, "nic9",
		&compute.AccessConfig{Name: "x"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no network interface named nic9")
}

func TestCompute_InstanceSuspendAndResume(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "verbs-suspend", "us-central1-a", "batch"
	createVerbInstance(t, svc, project, zone, name)

	_, err := svc.Instances.Suspend(project, zone, name).Do()
	require.NoError(t, err)
	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "SUSPENDED", got.Status)

	// Suspending a suspended machine has nothing to suspend.
	_, err = svc.Instances.Suspend(project, zone, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only a running instance can be suspended")

	_, err = svc.Instances.Resume(project, zone, name).Do()
	require.NoError(t, err)
	got, err = svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", got.Status)
}

func TestCompute_InstanceDiskAutoDeleteAndShieldedConfig(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "verbs-disk", "us-central1-a", "stateful"
	createVerbInstance(t, svc, project, zone, name)

	_, err := svc.Instances.SetDiskAutoDelete(project, zone, name, false, "boot").Do()
	require.NoError(t, err)
	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.Len(t, got.Disks, 1)
	assert.False(t, got.Disks[0].AutoDelete, "the boot disk now survives the instance")

	// A device the instance does not have reports itself.
	_, err = svc.Instances.SetDiskAutoDelete(project, zone, name, true, "absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no disk with device name absent")

	_, err = svc.Instances.UpdateShieldedInstanceConfig(project, zone, name,
		&compute.ShieldedInstanceConfig{EnableSecureBoot: true}).Do()
	require.NoError(t, err)
	got, err = svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.ShieldedInstanceConfig)
	assert.True(t, got.ShieldedInstanceConfig.EnableSecureBoot)
}

// The reads derived from state the project already holds.
func TestCompute_InstanceReferrersAndEffectiveFirewalls(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "verbs-reads", "us-central1-a", "member"
	createVerbInstance(t, svc, project, zone, name)

	referrers, err := svc.Instances.ListReferrers(project, zone, name).Do()
	require.NoError(t, err)
	assert.Empty(t, referrers.Items, "the instance belongs to no group yet")

	effective, err := svc.Instances.GetEffectiveFirewalls(project, zone, name, "nic0").Do()
	require.NoError(t, err)
	assert.NotNil(t, effective)
}

// The four that can only be answered by inventing what the hardware or the
// guest would have said are declared gaps on the wire.
func TestCompute_InstanceHardwareReadsAreDeclaredGaps(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "verbs-gaps", "us-central1-a", "opaque"
	createVerbInstance(t, svc, project, zone, name)

	_, err := svc.Instances.GetScreenshot(project, zone, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "501")

	_, err = svc.Instances.GetShieldedInstanceIdentity(project, zone, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "501")

	err = svc.Instances.SendDiagnosticInterrupt(project, zone, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "501")
}
