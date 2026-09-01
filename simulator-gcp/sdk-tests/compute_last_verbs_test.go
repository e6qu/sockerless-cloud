package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The last of the Compute verbs: moving a reserved address, replacing a
// resource whole, growing a subnetwork, and the reads that report what a health
// check has observed.

func TestCompute_AddressMoveBetweenScopes(t *testing.T) {
	svc := computeService(t)
	const project, region = "address-move", "us-central1"

	_, err := svc.Addresses.Insert(project, region, &compute.Address{
		Name: "reserved", Address: "10.0.0.7",
	}).Do()
	require.NoError(t, err)

	_, err = svc.Addresses.Move(project, region, "reserved", &compute.RegionAddressesMoveRequest{
		DestinationAddress: "projects/" + project + "/regions/" + region + "/addresses/rehomed",
		Description:        "moved",
	}).Do()
	require.NoError(t, err)

	moved, err := svc.Addresses.Get(project, region, "rehomed").Do()
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.7", moved.Address, "the reservation is the same address afterwards")

	_, err = svc.Addresses.Get(project, region, "reserved").Do()
	require.Error(t, err, "the address left the name it was moved from")

	// A move needs somewhere to go.
	_, err = svc.Addresses.Move(project, region, "rehomed",
		&compute.RegionAddressesMoveRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destinationAddress")
}

func TestCompute_UpdateReplacesAFirewallAndABackendService(t *testing.T) {
	svc := computeService(t)
	const project = "typed-updates"

	_, err := svc.Firewalls.Insert(project, &compute.Firewall{
		Name: "allow-ssh", Network: "global/networks/default",
		Description: "the original",
		Allowed:     []*compute.FirewallAllowed{{IPProtocol: "tcp", Ports: []string{"22"}}},
	}).Do()
	require.NoError(t, err)

	_, err = svc.Firewalls.Update(project, "allow-ssh", &compute.Firewall{
		Network: "global/networks/default",
		Allowed: []*compute.FirewallAllowed{{IPProtocol: "tcp", Ports: []string{"443"}}},
	}).Do()
	require.NoError(t, err)

	firewall, err := svc.Firewalls.Get(project, "allow-ssh").Do()
	require.NoError(t, err)
	require.Len(t, firewall.Allowed, 1)
	assert.Equal(t, []string{"443"}, firewall.Allowed[0].Ports)
	assert.Empty(t, firewall.Description, "an update drops what it did not carry")
	assert.Equal(t, "allow-ssh", firewall.Name, "identity survives an update")

	// The permission check every collection at this scope declares.
	_, err = svc.Firewalls.TestIamPermissions(project, "allow-ssh",
		&compute.TestPermissionsRequest{Permissions: []string{"compute.firewalls.get"}}).Do()
	require.NoError(t, err)

	_, err = svc.BackendServices.Insert(project, &compute.BackendService{
		Name: "pool", Protocol: "HTTP", Description: "the original",
	}).Do()
	require.NoError(t, err)
	_, err = svc.BackendServices.Update(project, "pool", &compute.BackendService{
		Protocol: "HTTPS",
	}).Do()
	require.NoError(t, err)
	backend, err := svc.BackendServices.Get(project, "pool").Do()
	require.NoError(t, err)
	assert.Equal(t, "HTTPS", backend.Protocol)
	assert.Empty(t, backend.Description)
}

func TestCompute_SubnetworkRangeOnlyExpands(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, region = "subnet-writes", "us-central1"

	_, err := svc.Networks.Insert(project, &compute.Network{Name: "custom"}).Do()
	require.NoError(t, err)
	_, err = svc.Subnetworks.Insert(project, region, &compute.Subnetwork{
		Name: "workers", IpCidrRange: "10.50.0.0/24",
		Network: "projects/" + project + "/global/networks/custom",
	}).Do()
	require.NoError(t, err)

	// A shorter prefix is a wider range, which is the only direction allowed:
	// shrinking would strand the addresses already handed out.
	_, err = svc.Subnetworks.ExpandIpCidrRange(project, region, "workers",
		&compute.SubnetworksExpandIpCidrRangeRequest{IpCidrRange: "10.50.0.0/20"}).Do()
	require.NoError(t, err)
	got, err := svc.Subnetworks.Get(project, region, "workers").Do()
	require.NoError(t, err)
	assert.Equal(t, "10.50.0.0/20", got.IpCidrRange)

	_, err = svc.Subnetworks.ExpandIpCidrRange(project, region, "workers",
		&compute.SubnetworksExpandIpCidrRangeRequest{IpCidrRange: "10.50.0.0/26"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can only expand")

	// A patch edits the members that can change in place.
	_, err = svc.Subnetworks.Patch(project, region, "workers", &compute.Subnetwork{
		Description: "the worker subnet",
	}).Do()
	require.NoError(t, err)
	got, err = svc.Subnetworks.Get(project, region, "workers").Do()
	require.NoError(t, err)
	assert.Equal(t, "10.50.0.0/20", got.IpCidrRange, "a patch keeps the range it did not mention")
}

// A regional backend service reports the health of the backends it names. A
// service naming none reports none, rather than claiming health nothing has
// observed.
func TestCompute_RegionBackendServiceHealth(t *testing.T) {
	svc := computeService(t)
	const project, region = "health-reads", "us-central1"

	_, err := svc.RegionBackendServices.Insert(project, region, &compute.BackendService{
		Name: "pool",
	}).Do()
	require.NoError(t, err)

	health, err := svc.RegionBackendServices.GetHealth(project, region, "pool",
		&compute.ResourceGroupReference{Group: "zones/us-central1-a/instanceGroups/workers"}).Do()
	require.NoError(t, err)
	assert.Empty(t, health.HealthStatus, "a service with no backends reports none")

	_, err = svc.RegionBackendServices.GetHealth(project, region, "absent",
		&compute.ResourceGroupReference{Group: "zones/us-central1-a/instanceGroups/workers"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// The members an interconnect group is built from become the members it has,
// which is what its operational status is then derived from — so creating them
// changes what the status read reports.
func TestCompute_InterconnectGroupCreateMembers(t *testing.T) {
	svc := computeService(t)
	const project, name = "group-members", "sites"

	_, err := svc.InterconnectGroups.Insert(project, &compute.InterconnectGroup{Name: name}).Do()
	require.NoError(t, err)

	before, err := svc.InterconnectGroups.GetOperationalStatus(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "DEGRADED", before.Result.GroupStatus)

	_, err = svc.InterconnectGroups.CreateMembers(project, name,
		&compute.InterconnectGroupsCreateMembersRequest{
			Request: &compute.InterconnectGroupsCreateMembers{
				IntentMismatchBehavior: "CREATE",
			},
		}).Do()
	require.NoError(t, err)

	// A request with nothing in it does not describe members to create.
	_, err = svc.InterconnectGroups.CreateMembers(project, name,
		&compute.InterconnectGroupsCreateMembersRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "describing the members")
}

// calendarMode advises when a future reservation could be met. The advice is
// the window the caller asked about: the simulator has no capacity forecast to
// narrow it with, and a narrower window would be a forecast invented out of
// nothing.
func TestCompute_AdviceCalendarMode(t *testing.T) {
	svc := computeService(t)
	const project, region = "advice-project", "us-central1"

	advice, err := svc.Advice.CalendarMode(project, region, &compute.CalendarModeAdviceRequest{
		FutureResourcesSpecs: map[string]compute.FutureResourcesSpec{
			"batch": {},
		},
	}).Do()
	require.NoError(t, err)
	require.Len(t, advice.Recommendations, 1)

	// Advice about nothing is not a question.
	_, err = svc.Advice.CalendarMode(project, region, &compute.CalendarModeAdviceRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resources the advice is about")
}

// Starting an instance whose disks are customer-encrypted, and replacing an
// instance whole. Both are zonal, so both answer with a zone operation.
func TestCompute_InstanceStartWithEncryptionKeyAndUpdate(t *testing.T) {
	requireNetworkHost(t)
	svc := computeService(t)
	const project, zone, name = "encrypted-boot", "us-central1-a", "sealed"
	createVerbInstance(t, svc, project, zone, name)

	_, err := svc.Instances.Stop(project, zone, name).Do()
	require.NoError(t, err)

	// The keys are supplied per disk. A start with none named has nothing to
	// unlock the disks with.
	_, err = svc.Instances.StartWithEncryptionKey(project, zone, name,
		&compute.InstancesStartWithEncryptionKeyRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keys for the instance's encrypted disks")

	_, err = svc.Instances.StartWithEncryptionKey(project, zone, name,
		&compute.InstancesStartWithEncryptionKeyRequest{
			Disks: []*compute.CustomerEncryptionKeyProtectedDisk{{
				Source: "projects/" + project + "/zones/" + zone + "/disks/boot",
				DiskEncryptionKey: &compute.CustomerEncryptionKey{
					RawKey: "SGVsbG8gZnJvbSBHb29nbGUgQ2xvdWQgUGxhdGZvcm0=",
				},
			}},
		}).Do()
	require.NoError(t, err)
	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", got.Status)

	// An update replaces the instance, keeping its identity.
	_, err = svc.Instances.Update(project, zone, name, &compute.Instance{
		Description: "replaced whole",
	}).Do()
	require.NoError(t, err)
	got, err = svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "replaced whole", got.Description)
	assert.Equal(t, name, got.Name, "identity survives an update")
}
