package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
)

// TestCompute_InterconnectRemoteLocationsAreTheVendoredCatalog covers
// interconnectRemoteLocations.list and .get — the third-party facilities Cloud
// Interconnect peers with for Cross-Cloud Interconnect, vendored from the four
// provider pages Google publishes them on.
func TestCompute_InterconnectRemoteLocationsAreTheVendoredCatalog(t *testing.T) {
	svc := computeService(t)
	const project = "interconnect-remote-project"

	list, err := svc.InterconnectRemoteLocations.List(project).Do()
	require.NoError(t, err)
	require.NotEmpty(t, list.Items)
	assert.Equal(t, "compute#interconnectRemoteLocationList", list.Kind)

	providers := map[string]bool{}
	for _, location := range list.Items {
		require.NotEmpty(t, location.Name)
		require.NotEmpty(t, location.City, "%s: every entry states its metropolitan area", location.Name)
		assert.Equal(t, "compute#interconnectRemoteLocation", location.Kind)
		for _, prefix := range []string{"aws-", "azure-", "oci-", "alibaba-"} {
			if len(location.Name) > len(prefix) && location.Name[:len(prefix)] == prefix {
				providers[prefix] = true
			}
		}
	}
	assert.Len(t, providers, 4, "all four provider catalogues are in the listing")

	// A remote location read by name carries the facilities it may connect to,
	// which is the field an operator picks a colocation site by.
	one, err := svc.InterconnectRemoteLocations.Get(project, "aws-lgknx").Do()
	require.NoError(t, err)
	assert.Equal(t, "aws-lgknx", one.Name)
	assert.Equal(t, "Seoul", one.City,
		"its city is rowspanned from the entry above it on Google's page")
	assert.Equal(t, "LGKNX", one.FacilityProviderFacilityId)
	require.Len(t, one.PermittedConnections, 2)
	assert.Equal(t, "icn-zone1-7674", one.PermittedConnections[0].InterconnectLocation)

	// Those connections name colocation facilities the other catalogue serves,
	// so the two agree rather than describing different worlds.
	paired, err := svc.InterconnectLocations.Get(project, one.PermittedConnections[0].InterconnectLocation).Do()
	require.NoError(t, err, "a permitted connection names a facility the colocation catalogue has")
	assert.Equal(t, "Seoul", paired.City)

	// What the pages do not state is absent rather than invented.
	assert.Empty(t, one.Continent)
	assert.Empty(t, one.Address)
	assert.Empty(t, one.RemoteService)

	_, err = svc.InterconnectRemoteLocations.Get(project, "aws-nowhere").Do()
	require.Error(t, err)
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Code)
}
