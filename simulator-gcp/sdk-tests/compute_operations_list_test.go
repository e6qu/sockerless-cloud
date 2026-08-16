package gcp_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
)

// Compute Engine records every mutation as an operation in the scope that owns
// the resource, and the per-scope and aggregated listings report them. The
// listings used to answer with a hardcoded empty set whatever the project had
// done, so a client enumerating its own work saw nothing; these assertions are
// what stops that returning.
//
// A disk is zonal and needs no host networking, so the zonal listing is checked
// on every host. Networks and addresses provision real Linux networking, so the
// global and regional listings are checked wherever that kernel support exists —
// the same gate every other Compute networking test here uses.
func TestCompute_OperationListsReportTheWorkTheProjectDid(t *testing.T) {
	svc := computeService(t)
	const (
		project = "test-project"
		zone    = "us-central1-a"
	)

	zoneOp, err := svc.Disks.Insert(project, zone, &compute.Disk{
		Name:   "op-list-disk",
		SizeGb: 10,
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Disks.Delete(project, zone, "op-list-disk").Do() })
	require.NotEmpty(t, zoneOp.Name, "a disk insert must hand the client an operation")

	zoneList, err := svc.ZoneOperations.List(project, zone).Do()
	require.NoError(t, err)
	require.NotEmpty(t, zoneList.Items, "zone operations listed nothing after a disk was created in that zone")
	assert.Contains(t, operationNames(zoneList.Items), zoneOp.Name,
		"the zone operation list does not report the operation the insert returned")

	// A zone the project has done nothing in reports its own emptiness rather
	// than another zone's operations.
	elsewhere, err := svc.ZoneOperations.List(project, "europe-west4-a").Do()
	require.NoError(t, err)
	assert.NotContains(t, operationNames(elsewhere.Items), zoneOp.Name,
		"an operation leaked into a zone it was not issued in")

	// The aggregated listing groups the same records by scope, which is how a
	// client enumerates a whole project in one call.
	aggregated, err := svc.GlobalOperations.AggregatedList(project).Do()
	require.NoError(t, err)
	aggregatedNames := map[string]bool{}
	for _, scoped := range aggregated.Items {
		for _, item := range scoped.Operations {
			aggregatedNames[item.Name] = true
		}
	}
	assert.True(t, aggregatedNames[zoneOp.Name], "the aggregated list omits the zonal operation")

	t.Run("global and regional scopes", func(t *testing.T) {
		requireNetworkHost(t)
		const region = "us-central1"

		globalOp, err := svc.Networks.Insert(project, &compute.Network{
			Name:                  "op-list-network",
			AutoCreateSubnetworks: false,
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Networks.Delete(project, "op-list-network").Do() })

		regionOp, err := svc.Addresses.Insert(project, region, &compute.Address{
			Name: "op-list-address",
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Addresses.Delete(project, region, "op-list-address").Do() })

		globalList, err := svc.GlobalOperations.List(project).Do()
		require.NoError(t, err)
		assert.Contains(t, operationNames(globalList.Items), globalOp.Name,
			"the global operation list does not report the operation the insert returned")

		regionList, err := svc.RegionOperations.List(project, region).Do()
		require.NoError(t, err)
		assert.Contains(t, operationNames(regionList.Items), regionOp.Name,
			"the region operation list does not report the operation the insert returned")

		aggregated, err := svc.GlobalOperations.AggregatedList(project).Do()
		require.NoError(t, err)
		names := map[string]bool{}
		for _, scoped := range aggregated.Items {
			for _, item := range scoped.Operations {
				names[item.Name] = true
			}
		}
		assert.True(t, names[globalOp.Name], "the aggregated list omits the global operation")
		assert.True(t, names[regionOp.Name], "the aggregated list omits the regional operation")
	})
}

func operationNames(items []*compute.Operation) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

// The typed client hides the envelope, so the three scoped listing routes are
// also read on the wire to confirm they answer with the kind Compute Engine
// declares for them:
//
//	GET /compute/v1/projects/{project}/zones/{zone}/operations
//	GET /compute/v1/projects/{project}/regions/{region}/operations
//	GET /compute/v1/projects/{project}/global/operations
func TestCompute_OperationListsAnswerWithTheDeclaredKind(t *testing.T) {
	for _, path := range []string{
		"/compute/v1/projects/test-project/zones/us-central1-a/operations",
		"/compute/v1/projects/test-project/regions/us-central1/operations",
		"/compute/v1/projects/test-project/global/operations",
	} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
			require.NoError(t, err)
			token, err := simTokenSource().Token()
			require.NoError(t, err)
			token.SetAuthHeader(req)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

			var envelope struct {
				Kind  string            `json:"kind"`
				Items []json.RawMessage `json:"items"`
			}
			require.NoError(t, json.Unmarshal(body, &envelope), "body: %s", body)
			assert.Equal(t, "compute#operationList", envelope.Kind,
				"the listing must declare the kind the service declares")
		})
	}
}
