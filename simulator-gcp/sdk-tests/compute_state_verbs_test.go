package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The verbs that move a resource through its lifecycle, and the reads that
// report what its parts are doing.

func TestCompute_PublicAdvertisedPrefixAnnounceAndWithdraw(t *testing.T) {
	svc := computeService(t)
	const project, name = "advertised", "owned-range"

	_, err := svc.PublicAdvertisedPrefixes.Insert(project, &compute.PublicAdvertisedPrefix{
		Name: name, IpCidrRange: "192.0.2.0/24", DnsVerificationIp: "192.0.2.1",
	}).Do()
	require.NoError(t, err)

	// Withdrawing one that was never announced is refused: a no-op would hide
	// the mistake.
	_, err = svc.PublicAdvertisedPrefixes.Withdraw(project, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be withdrawn")

	_, err = svc.PublicAdvertisedPrefixes.Announce(project, name).Do()
	require.NoError(t, err)
	got, err := svc.PublicAdvertisedPrefixes.Get(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ANNOUNCED_TO_INTERNET", got.Status)

	_, err = svc.PublicAdvertisedPrefixes.Announce(project, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be announced")

	_, err = svc.PublicAdvertisedPrefixes.Withdraw(project, name).Do()
	require.NoError(t, err)
	got, err = svc.PublicAdvertisedPrefixes.Get(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "INITIAL", got.Status)
}

func TestCompute_FutureReservationCancel(t *testing.T) {
	svc := computeService(t)
	const project, zone, name = "future-res", "us-central1-a", "capacity"

	_, err := svc.FutureReservations.Insert(project, zone, &compute.FutureReservation{
		Name: name, Description: "next quarter",
	}).Do()
	require.NoError(t, err)

	_, err = svc.FutureReservations.Cancel(project, zone, name).Do()
	require.NoError(t, err)
	got, err := svc.FutureReservations.Get(project, zone, name).Do()
	require.NoError(t, err)
	require.NotNil(t, got.Status)
	assert.Equal(t, "CANCELLED", got.Status.ProcurementStatus,
		"a future reservation keeps its lifecycle state inside its status")

	// Cancelling it twice is refused rather than passing silently.
	_, err = svc.FutureReservations.Cancel(project, zone, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be cancelled")
}

// A status read is derived from the resource, so it cannot claim more than the
// resource says.
func TestCompute_StatusReadsReportWhatTheResourceHolds(t *testing.T) {
	svc := computeService(t)
	const project, region = "status-reads", "us-central1"

	_, err := svc.VpnGateways.Insert(project, region, &compute.VpnGateway{
		Name: "border", Network: "global/networks/default",
	}).Do()
	require.NoError(t, err)
	status, err := svc.VpnGateways.GetStatus(project, region, "border").Do()
	require.NoError(t, err)
	require.NotNil(t, status.Result)
	assert.Empty(t, status.Result.VpnConnections, "a gateway with no tunnels reports none")

	// An interconnect group with no members cannot carry traffic, so it is
	// degraded rather than fully up — saying otherwise would tell a client its
	// topology is redundant when it is empty.
	_, err = svc.InterconnectGroups.Insert(project, &compute.InterconnectGroup{
		Name: "sites", Description: "two sites",
	}).Do()
	require.NoError(t, err)
	group, err := svc.InterconnectGroups.GetOperationalStatus(project, "sites").Do()
	require.NoError(t, err)
	require.NotNil(t, group.Result)
	assert.Equal(t, "GROUP_STATUS_DEGRADED", group.Result.GroupStatus)

	_, err = svc.InterconnectAttachmentGroups.Insert(project, &compute.InterconnectAttachmentGroup{
		Name: "landings",
	}).Do()
	require.NoError(t, err)
	attachments, err := svc.InterconnectAttachmentGroups.GetOperationalStatus(project, "landings").Do()
	require.NoError(t, err)
	require.NotNil(t, attachments.Result)
	assert.Equal(t, "GROUP_STATUS_DEGRADED", attachments.Result.GroupStatus)

	// A resource that is not there has no status.
	_, err = svc.VpnGateways.GetStatus(project, region, "absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
