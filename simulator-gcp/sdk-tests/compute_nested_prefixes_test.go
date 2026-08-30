package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// A wire group is addressed through the cross-site network that owns it, and a
// regional public delegated prefix carries announce and withdraw on top of its
// lifecycle. Both are read back after every write, because an Operation says
// nothing about what was stored.

func TestCompute_WireGroupsLiveInsideTheirCrossSiteNetwork(t *testing.T) {
	svc := computeService(t)
	const project, network, group = "wire-project", "sites", "east-west"

	_, err := svc.CrossSiteNetworks.Insert(project, &compute.CrossSiteNetwork{
		Name: network, Description: "two sites",
	}).Do()
	require.NoError(t, err)

	_, err = svc.WireGroups.Insert(project, network, &compute.WireGroup{
		Name: group, Description: "primary",
	}).Do()
	require.NoError(t, err)

	got, err := svc.WireGroups.Get(project, network, group).Do()
	require.NoError(t, err)
	assert.Equal(t, "primary", got.Description)
	assert.Equal(t, "compute#wireGroup", got.Kind)

	listed, err := svc.WireGroups.List(project, network).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)

	_, err = svc.WireGroups.Patch(project, network, group, &compute.WireGroup{
		Description: "primary and secondary",
	}).Do()
	require.NoError(t, err)
	got, err = svc.WireGroups.Get(project, network, group).Do()
	require.NoError(t, err)
	assert.Equal(t, "primary and secondary", got.Description)

	// A second cross-site network sees none of the first one's groups: the
	// group belongs to the network it was created under.
	_, err = svc.CrossSiteNetworks.Insert(project, &compute.CrossSiteNetwork{Name: "other"}).Do()
	require.NoError(t, err)
	listed, err = svc.WireGroups.List(project, "other").Do()
	require.NoError(t, err)
	assert.Empty(t, listed.Items)

	// A network that does not exist cannot hold a group.
	_, err = svc.WireGroups.Insert(project, "absent", &compute.WireGroup{Name: "x"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-site network")

	_, err = svc.WireGroups.Delete(project, network, group).Do()
	require.NoError(t, err)
	_, err = svc.WireGroups.Get(project, network, group).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCompute_RegionalPublicDelegatedPrefixAnnounceAndWithdraw(t *testing.T) {
	svc := computeService(t)
	const project, region, name = "prefix-project", "us-central1", "delegated"

	_, err := svc.PublicDelegatedPrefixes.Insert(project, region, &compute.PublicDelegatedPrefix{
		Name: name, IpCidrRange: "192.0.2.0/24",
	}).Do()
	require.NoError(t, err)

	// Withdrawing one that was never announced is refused rather than passing
	// silently: a no-op would hide the mistake.
	_, err = svc.PublicDelegatedPrefixes.Withdraw(project, region, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be withdrawn")

	_, err = svc.PublicDelegatedPrefixes.Announce(project, region, name).Do()
	require.NoError(t, err)
	got, err := svc.PublicDelegatedPrefixes.Get(project, region, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "ANNOUNCED", got.Status)

	// And announcing it twice is refused for the same reason.
	_, err = svc.PublicDelegatedPrefixes.Announce(project, region, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be announced")

	_, err = svc.PublicDelegatedPrefixes.Withdraw(project, region, name).Do()
	require.NoError(t, err)
	got, err = svc.PublicDelegatedPrefixes.Get(project, region, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "INITIAL", got.Status)

	// The regional prefixes are their own collection: the global ones do not
	// see this one.
	_, err = svc.GlobalPublicDelegatedPrefixes.Get(project, name).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
