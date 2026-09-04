package gcp_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcloud compute interconnects remote-locations against the simulator. The
// catalogue is vendored from the four "Choose your locations" pages Google
// publishes, one per cloud provider, and this drives the read an operator makes
// when picking where to land a Cross-Cloud Interconnect.
func TestGcloudComputeInterconnectRemoteLocations(t *testing.T) {
	out, err := gcloudCLI("compute", "interconnects", "remote-locations", "list",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	listing := string(out)
	for _, name := range []string{"aws-lgknx", "azure-equinix-hong-kong-hk1", "oci-equinix-sg1", "alibaba-hk-kwaichung-a"} {
		assert.Contains(t, listing, name, "all four provider catalogues are in the listing")
	}

	out, err = gcloudCLI("compute", "interconnects", "remote-locations", "describe",
		"aws-lgknx", "--format=json").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)

	var location struct {
		Name                       string `json:"name"`
		City                       string `json:"city"`
		FacilityProviderFacilityID string `json:"facilityProviderFacilityId"`
		Continent                  string `json:"continent"`
		RemoteService              string `json:"remoteService"`
		PermittedConnections       []struct {
			InterconnectLocation string `json:"interconnectLocation"`
		} `json:"permittedConnections"`
	}
	require.NoError(t, json.Unmarshal(out, &location), "describe output: %s", out)

	assert.Equal(t, "aws-lgknx", location.Name)
	assert.Equal(t, "Seoul", location.City,
		"its city is rowspanned from the entry above it on Google's page")
	assert.Equal(t, "LGKNX", location.FacilityProviderFacilityID)
	require.Len(t, location.PermittedConnections, 2)
	assert.Equal(t, "icn-zone1-7674", location.PermittedConnections[0].InterconnectLocation)

	// What the pages do not state is absent here too.
	assert.Empty(t, location.Continent)
	assert.Empty(t, location.RemoteService)
}
