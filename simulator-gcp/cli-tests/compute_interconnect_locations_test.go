package gcp_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcloud compute interconnects locations against the simulator. The catalogue
// is vendored from Google's published colocation-facility documentation, and
// this drives the read the way an operator does when choosing where to order a
// cross-connect: list the facilities, then describe the one they picked.
func TestGcloudComputeInterconnectLocations(t *testing.T) {
	out, err := gcloudCLI("compute", "interconnects", "locations", "list",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	listing := string(out)
	assert.Contains(t, listing, "ams-zone1-1236",
		"a facility named in Google's documentation is in the catalogue")
	assert.Contains(t, listing, "cpt-zone1-99025",
		"including the one whose row Google's own markup leaves without a <tr>")

	out, err = gcloudCLI("compute", "interconnects", "locations", "describe",
		"ams-zone1-1236", "--format=json").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)

	var location struct {
		Name             string `json:"name"`
		City             string `json:"city"`
		Description      string `json:"description"`
		AvailabilityZone string `json:"availabilityZone"`
		PeeringdbID      string `json:"peeringdbFacilityId"`
		Address          string `json:"address"`
		FacilityProvider string `json:"facilityProvider"`
		Continent        string `json:"continent"`
		RegionInfos      []struct {
			Region string `json:"region"`
		} `json:"regionInfos"`
	}
	require.NoError(t, json.Unmarshal(out, &location), "describe output: %s", out)

	assert.Equal(t, "ams-zone1-1236", location.Name)
	assert.Equal(t, "Amsterdam", location.City)
	assert.Equal(t, "zone1", location.AvailabilityZone)
	assert.Equal(t, "1236", location.PeeringdbID)
	assert.NotEmpty(t, location.Description)
	require.NotEmpty(t, location.RegionInfos)
	assert.Equal(t, "europe-west4", location.RegionInfos[0].Region)

	// What Google's page does not state is absent here too, so an operator
	// reading this through the CLI cannot mistake an omission for a fact.
	assert.Empty(t, location.Address)
	assert.Empty(t, location.FacilityProvider)
	assert.Empty(t, location.Continent)
}
