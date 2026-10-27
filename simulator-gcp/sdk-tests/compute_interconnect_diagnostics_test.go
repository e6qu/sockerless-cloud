package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

// TestCompute_InterconnectDiagnosticsComeFromTheInterconnect: most of
// interconnects.getDiagnostics comes from the interconnect's own record: the
// bundle state, link aggregation, each link's circuit and demarcation
// identifiers, and MACsec state and key. What only the equipment knows (optical
// power, negotiated LACP state, the peer's ARP cache) stays absent, which the
// schema allows; an invented number would pass for a reading.
func TestCompute_InterconnectDiagnosticsComeFromTheInterconnect(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project = "interconnect-diagnostics-project"

	_, err := svc.Interconnects.Insert(project, &compute.Interconnect{
		Name: "diag-link", Location: "iad-zone1-1", InterconnectType: "DEDICATED",
		RequestedLinkCount: 2,
		OperationalStatus:  "OS_ACTIVE",
		CircuitInfos: []*compute.InterconnectCircuitInfo{
			{GoogleCircuitId: "circuit-a", GoogleDemarcId: "demarc-a", CustomerDemarcId: "cust-a"},
			{GoogleCircuitId: "circuit-b", GoogleDemarcId: "demarc-b", CustomerDemarcId: "cust-b"},
		},
		MacsecEnabled: true,
		Macsec: &compute.InterconnectMacsec{
			PreSharedKeys: []*compute.InterconnectMacsecPreSharedKey{{Name: "primary"}},
		},
	}).Do()
	require.NoError(t, err)

	diagnostics, err := svc.Interconnects.GetDiagnostics(project, "diag-link").Do()
	require.NoError(t, err)
	require.NotNil(t, diagnostics.Result)
	result := diagnostics.Result

	assert.Equal(t, "BUNDLE_OPERATIONAL_STATUS_UP", result.BundleOperationalStatus,
		"the bundle's status is the interconnect's own operational status")
	assert.Equal(t, "BUNDLE_AGGREGATION_TYPE_LACP", result.BundleAggregationType,
		"a bundle of more than one link is aggregated")
	require.Len(t, result.Links, 2, "one link per circuit on the record")

	// The circuits come back in the order the record holds them, carrying the
	// identifiers the record assigned.
	assert.Equal(t, "circuit-a", result.Links[0].CircuitId)
	assert.Equal(t, "demarc-a", result.Links[0].GoogleDemarc)
	assert.Equal(t, "circuit-b", result.Links[1].CircuitId)
	assert.Equal(t, "LINK_OPERATIONAL_STATUS_UP", result.Links[0].OperationalStatus)

	// MACsec agrees with getMacsecConfig because both derive from the same
	// interconnect and key name.
	require.NotNil(t, result.Links[0].Macsec)
	assert.True(t, result.Links[0].Macsec.Operational)
	configuration, err := svc.Interconnects.GetMacsecConfig(project, "diag-link").Do()
	require.NoError(t, err)
	require.NotEmpty(t, configuration.Result.PreSharedKeys)
	assert.Equal(t, configuration.Result.PreSharedKeys[0].Ckn, result.Links[0].Macsec.Ckn,
		"the diagnostics and the configuration cannot report different keys")

	// The measurements a simulator cannot take are absent rather than invented.
	assert.Nil(t, result.Links[0].TransmittingOpticalPower, "there are no optics to measure")
	assert.Nil(t, result.Links[0].ReceivingOpticalPower, "there are no optics to measure")
	assert.Nil(t, result.Links[0].LacpStatus, "there is no peer to negotiate with")
	assert.Empty(t, result.ArpCaches, "there is no peer to learn an address from")

	// An interconnect that was never created has no diagnostics.
	_, err = svc.Interconnects.GetDiagnostics(project, "diag-absent").Do()
	require.Error(t, err)
}
