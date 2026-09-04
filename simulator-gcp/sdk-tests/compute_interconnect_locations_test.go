package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
)

// TestCompute_InterconnectLocationsAreTheVendoredCatalog covers
// interconnectLocations.list and .get, which used to decline as a catalogue
// only Google can publish. It is published, and it is vendored here from
// Google's own documentation the way the Azure slice vendors Application
// Gateway's managed WAF rule sets.
//
// The test holds the catalogue to being a real one — a facility a caller can
// look up by name, carrying what the source states about it — and to leaving
// absent what the source does not state, rather than filling those fields with
// something a client could not tell from a reading.
func TestCompute_InterconnectLocationsAreTheVendoredCatalog(t *testing.T) {
	svc := computeService(t)
	const project = "interconnect-location-project"

	list, err := svc.InterconnectLocations.List(project).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Items, "the catalogue is not empty; an empty list would say Google runs no facilities")
	assert.Equal(t, "compute#interconnectLocationList", list.Kind)

	byName := map[string]bool{}
	for _, location := range list.Items {
		require.NotEmpty(t, location.Name)
		require.NotEmpty(t, location.City, "%s: every facility on the page states its metropolitan area", location.Name)
		require.NotEmpty(t, location.Description, "%s: every facility states which facility it is", location.Name)
		require.NotEmpty(t, location.AvailabilityZone, "%s: the name encodes the zone", location.Name)
		assert.Equal(t, "compute#interconnectLocation", location.Kind)
		byName[location.Name] = true
	}
	assert.True(t, byName["ams-zone1-1236"],
		"a facility named in Google's documentation is in the catalogue")

	// One facility read by name is the same facility the listing carried.
	one, err := svc.InterconnectLocations.Get(project, "ams-zone1-1236").Do()
	require.NoError(t, err)
	assert.Equal(t, "ams-zone1-1236", one.Name)
	assert.Equal(t, "Amsterdam", one.City)
	assert.Equal(t, "zone1", one.AvailabilityZone)
	assert.Equal(t, "1236", one.PeeringdbFacilityId)
	require.NotEmpty(t, one.RegionInfos, "the page gives this facility a low-latency region")
	assert.Equal(t, "europe-west4", one.RegionInfos[0].Region)

	// What the source does not state is absent rather than invented.
	assert.Empty(t, one.Address, "the page gives no street address")
	assert.Empty(t, one.FacilityProvider, "the page names the facility, not its provider as a field")
	assert.Empty(t, one.Continent, "the page groups facilities geographically, which is not the enum")

	// A facility Google does not run is not found.
	_, err = svc.InterconnectLocations.Get(project, "zzz-zone9-0").Do()
	require.Error(t, err)
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Code)
}
