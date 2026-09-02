package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// A URL map's validate, the difference between patch and update, the settings
// singletons, and the regional instance group a regional manager keeps.

func TestCompute_URLMapValidateChecksItsTestsAgainstItsOwnRouting(t *testing.T) {
	svc := computeService(t)
	const project = "validate-project"

	urlMap := &compute.UrlMap{
		Name:           "routed",
		DefaultService: "global/backendServices/fallback",
		HostRules: []*compute.HostRule{{
			Hosts: []string{"shop.example.com"}, PathMatcher: "shop",
		}},
		PathMatchers: []*compute.PathMatcher{{
			Name:           "shop",
			DefaultService: "global/backendServices/web",
			PathRules: []*compute.PathRule{{
				Paths: []string{"/api/*"}, Service: "global/backendServices/api",
			}},
		}},
		Tests: []*compute.UrlMapTest{
			{Host: "shop.example.com", Path: "/api/orders", Service: "global/backendServices/api"},
			{Host: "shop.example.com", Path: "/", Service: "global/backendServices/web"},
			{Host: "other.example.com", Path: "/", Service: "global/backendServices/fallback"},
		},
	}
	_, err := svc.UrlMaps.Insert(project, urlMap).Do()
	require.NoError(t, err)

	got, err := svc.UrlMaps.Validate(project, urlMap.Name,
		&compute.UrlMapsValidateRequest{Resource: urlMap}).Do()
	require.NoError(t, err)
	require.NotNil(t, got.Result)
	assert.True(t, got.Result.LoadSucceeded)
	assert.True(t, got.Result.TestPassed, "every test names the service its request reaches")

	// A test that expects the wrong service is reported with what the request
	// actually reaches, which is what makes the failure actionable.
	urlMap.Tests = append(urlMap.Tests, &compute.UrlMapTest{
		Host: "shop.example.com", Path: "/api/orders", Service: "global/backendServices/web",
	})
	got, err = svc.UrlMaps.Validate(project, urlMap.Name,
		&compute.UrlMapsValidateRequest{Resource: urlMap}).Do()
	require.NoError(t, err)
	assert.False(t, got.Result.TestPassed)
	require.Len(t, got.Result.TestFailures, 1)
	assert.Equal(t, "/api/orders", got.Result.TestFailures[0].Path)
	assert.Contains(t, got.Result.TestFailures[0].ActualService, "api")

	// The regional URL maps validate the same way, through their own route.
	const region = "us-central1"
	_, err = svc.RegionUrlMaps.Insert(project, region, &compute.UrlMap{
		Name: "regional", DefaultService: "regions/" + region + "/backendServices/web",
	}).Do()
	require.NoError(t, err)
	regional, err := svc.RegionUrlMaps.Validate(project, region, "regional",
		&compute.RegionUrlMapsValidateRequest{Resource: urlMap}).Do()
	require.NoError(t, err)
	require.NotNil(t, regional.Result)
	assert.True(t, regional.Result.LoadSucceeded)

	// A map naming a path matcher it does not carry does not load, and its
	// tests are not run against a map that would not route.
	broken := &compute.UrlMap{
		Name:           "broken",
		DefaultService: "global/backendServices/fallback",
		HostRules:      []*compute.HostRule{{Hosts: []string{"a.example.com"}, PathMatcher: "absent"}},
	}
	got, err = svc.UrlMaps.Validate(project, urlMap.Name,
		&compute.UrlMapsValidateRequest{Resource: broken}).Do()
	require.NoError(t, err)
	assert.False(t, got.Result.LoadSucceeded)
	require.NotEmpty(t, got.Result.LoadErrors)
	assert.Contains(t, got.Result.LoadErrors[0], "absent")
}

func TestCompute_URLMapUpdateDropsWhatPatchKeeps(t *testing.T) {
	svc := computeService(t)
	const project, name = "rewrite-project", "rules"

	_, err := svc.UrlMaps.Insert(project, &compute.UrlMap{
		Name:           name,
		Description:    "the original",
		DefaultService: "global/backendServices/web",
		HostRules:      []*compute.HostRule{{Hosts: []string{"a.example.com"}, PathMatcher: "m"}},
		PathMatchers:   []*compute.PathMatcher{{Name: "m", DefaultService: "global/backendServices/web"}},
	}).Do()
	require.NoError(t, err)

	// A patch leaves the members it did not mention in place.
	_, err = svc.UrlMaps.Patch(project, name, &compute.UrlMap{Description: "patched"}).Do()
	require.NoError(t, err)
	got, err := svc.UrlMaps.Get(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "patched", got.Description)
	require.Len(t, got.HostRules, 1, "a patch keeps the rules it did not mention")

	// An update replaces the resource: the rules the client left out are gone.
	_, err = svc.UrlMaps.Update(project, name, &compute.UrlMap{
		DefaultService: "global/backendServices/other",
	}).Do()
	require.NoError(t, err)
	got, err = svc.UrlMaps.Get(project, name).Do()
	require.NoError(t, err)
	assert.Empty(t, got.HostRules, "an update drops what it did not carry")
	assert.Empty(t, got.Description)
	assert.Equal(t, name, got.Name, "identity survives an update")
	assert.Contains(t, got.DefaultService, "other")

	// invalidateCache needs the path whose content is being dropped.
	_, err = svc.UrlMaps.InvalidateCache(project, name,
		&compute.CacheInvalidationRule{Path: "/assets/*"}).Do()
	require.NoError(t, err)
	_, err = svc.UrlMaps.InvalidateCache(project, name, &compute.CacheInvalidationRule{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path")
}

func TestCompute_HealthCheckPatchAndUpdate(t *testing.T) {
	svc := computeService(t)
	const project, name = "hc-project", "probe"

	_, err := svc.HealthChecks.Insert(project, &compute.HealthCheck{
		Name: name, Type: "HTTP", Description: "the original",
		HttpHealthCheck: &compute.HTTPHealthCheck{Port: 8080, RequestPath: "/healthz"},
	}).Do()
	require.NoError(t, err)

	_, err = svc.HealthChecks.Patch(project, name, &compute.HealthCheck{TimeoutSec: 9}).Do()
	require.NoError(t, err)
	got, err := svc.HealthChecks.Get(project, name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(9), got.TimeoutSec)
	require.NotNil(t, got.HttpHealthCheck, "a patch keeps the probe it did not mention")
	assert.Equal(t, int64(8080), got.HttpHealthCheck.Port)

	_, err = svc.HealthChecks.Update(project, name, &compute.HealthCheck{
		Type: "TCP", TcpHealthCheck: &compute.TCPHealthCheck{Port: 443},
	}).Do()
	require.NoError(t, err)
	got, err = svc.HealthChecks.Get(project, name).Do()
	require.NoError(t, err)
	assert.Nil(t, got.HttpHealthCheck, "an update drops the probe it replaced")
	require.NotNil(t, got.TcpHealthCheck)
	assert.Equal(t, int64(443), got.TcpHealthCheck.Port)
	assert.Empty(t, got.Description)
}

func TestCompute_SettingsSingletonsAnswerDefaultsBeforeAnyPatch(t *testing.T) {
	svc := computeService(t)
	const project, region, zone = "settings-project", "us-central1", "us-central1-a"

	// A settings resource is never created, so a read before any write is the
	// defaults rather than a 404.
	snapshots, err := svc.SnapshotSettings.Get(project).Do()
	require.NoError(t, err)
	require.NotNil(t, snapshots.StorageLocation)
	assert.Equal(t, "NEAREST_MULTI_REGION", snapshots.StorageLocation.Policy)

	_, err = svc.SnapshotSettings.Patch(project, &compute.SnapshotSettings{
		StorageLocation: &compute.SnapshotSettingsStorageLocationSettings{Policy: "LOCAL_REGION"},
	}).Do()
	require.NoError(t, err)
	snapshots, err = svc.SnapshotSettings.Get(project).Do()
	require.NoError(t, err)
	assert.Equal(t, "LOCAL_REGION", snapshots.StorageLocation.Policy)

	// The regional snapshot settings are their own resource: the global patch
	// did not reach them.
	regional, err := svc.RegionSnapshotSettings.Get(project, region).Do()
	require.NoError(t, err)
	assert.Equal(t, "NEAREST_MULTI_REGION", regional.StorageLocation.Policy)

	instances, err := svc.InstanceSettings.Get(project, zone).Do()
	require.NoError(t, err)
	assert.Contains(t, instances.Zone, zone)

	_, err = svc.InstanceSettings.Patch(project, zone, &compute.InstanceSettings{
		Metadata: &compute.InstanceSettingsMetadata{Items: map[string]string{"team": "platform"}},
	}).Do()
	require.NoError(t, err)
	instances, err = svc.InstanceSettings.Get(project, zone).Do()
	require.NoError(t, err)
	require.NotNil(t, instances.Metadata)
	assert.Equal(t, "platform", instances.Metadata.Items["team"])
}

func TestCompute_RegionInstanceGroupFollowsItsManager(t *testing.T) {
	svc := computeService(t)
	const project, region, name = "rig-project", "us-central1", "fleet"

	_, err := svc.RegionInstanceGroupManagers.Insert(project, region, &compute.InstanceGroupManager{
		Name: name, BaseInstanceName: "fleet", TargetSize: 2,
		InstanceTemplate: "global/instanceTemplates/base",
	}).Do()
	require.NoError(t, err)

	// The group is not created: it is the manager's, so it reports the size
	// the manager is at.
	group, err := svc.RegionInstanceGroups.Get(project, region, name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), group.Size)

	listed, err := svc.RegionInstanceGroups.List(project, region).Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)

	instances, err := svc.RegionInstanceGroups.ListInstances(project, region, name,
		&compute.RegionInstanceGroupsListInstancesRequest{InstanceState: "ALL"}).Do()
	require.NoError(t, err)
	require.Len(t, instances.Items, 2, "the group holds the instances its manager manages")

	// A resize on the manager is visible through the group, because the group
	// is derived from it rather than copied.
	_, err = svc.RegionInstanceGroupManagers.Resize(project, region, name, 3).Do()
	require.NoError(t, err)
	group, err = svc.RegionInstanceGroups.Get(project, region, name).Do()
	require.NoError(t, err)
	assert.Equal(t, int64(3), group.Size)

	// Named ports belong to the group, and the fingerprint guards them.
	_, err = svc.RegionInstanceGroups.SetNamedPorts(project, region, name,
		&compute.RegionInstanceGroupsSetNamedPortsRequest{
			NamedPorts: []*compute.NamedPort{{Name: "http", Port: 8080}},
		}).Do()
	require.NoError(t, err)
	group, err = svc.RegionInstanceGroups.Get(project, region, name).Do()
	require.NoError(t, err)
	require.Len(t, group.NamedPorts, 1)
	assert.Equal(t, int64(8080), group.NamedPorts[0].Port)

	_, err = svc.RegionInstanceGroups.SetNamedPorts(project, region, name,
		&compute.RegionInstanceGroupsSetNamedPortsRequest{
			NamedPorts:  []*compute.NamedPort{{Name: "http", Port: 9090}},
			Fingerprint: "stale",
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed since")

	// A group with no manager behind it does not exist.
	_, err = svc.RegionInstanceGroups.Get(project, region, "absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
