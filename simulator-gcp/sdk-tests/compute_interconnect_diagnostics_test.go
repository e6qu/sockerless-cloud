package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

// TestCompute_InterconnectDiagnosticsComeFromTheInterconnect covers
// interconnects.getDiagnostics, which used to decline as hardware reporting on
// itself. Most of what it reports is on the interconnect's own record: whether
// the bundle is up, whether its links are aggregated, the circuit and
// demarcation identifiers assigned to each link, and whether MACsec is
// operating and under which key.
//
// What is genuinely off the equipment — the optical power on each link, the
// LACP state the ends negotiated, the ARP caches learned from the peer — is
// absent, and the schema requires none of it. A number invented for any of them
// would be indistinguishable from a reading.
func TestCompute_InterconnectDiagnosticsComeFromTheInterconnect(t *testing.T) {
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

	// MACsec agrees with what getMacsecConfig hands the caller for that key,
	// because both derive it from the same interconnect and key name.
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
