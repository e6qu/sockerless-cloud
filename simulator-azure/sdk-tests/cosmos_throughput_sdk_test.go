package azure_sdk_test

import (
	"encoding/json"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCosmos_ThroughputOffers: azcosmos ReadThroughput and ReplaceThroughput
// round-trip manual and autoscale ThroughputProperties. The SDK finds the
// container's offer through its _rid with a POST /offers query, then reads and
// replaces GET and PUT /offers/{offer}.
func TestCosmos_ThroughputOffers(t *testing.T) {
	t.Parallel()
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	client := cosmosSimClient(t, dataPlaneAccount)

	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "thrudb"}, nil)
	require.NoError(t, err, "CreateDatabase")
	db, err := client.NewDatabase("thrudb")
	require.NoError(t, err)
	// Only a container with dedicated throughput has its own offer; a
	// shared-throughput container has none and ReadThroughput returns 404.
	startThroughput := azcosmos.NewManualThroughputProperties(400)
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "thruc",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}, &azcosmos.CreateContainerOptions{ThroughputProperties: &startThroughput})
	require.NoError(t, err, "CreateContainer with throughput")

	cc, err := client.NewContainer("thrudb", "thruc")
	require.NoError(t, err)

	readResp, err := cc.ReadThroughput(ctx, nil)
	require.NoError(t, err, "ReadThroughput")
	manual, ok := readResp.ThroughputProperties.ManualThroughput()
	require.True(t, ok, "provisioned offer must be manual")
	assert.EqualValues(t, 400, manual)

	newManual := azcosmos.NewManualThroughputProperties(1000)
	replResp, err := cc.ReplaceThroughput(ctx, newManual, nil)
	require.NoError(t, err, "ReplaceThroughput (manual)")
	rm, ok := replResp.ThroughputProperties.ManualThroughput()
	require.True(t, ok)
	assert.EqualValues(t, 1000, rm)

	re, err := cc.ReadThroughput(ctx, nil)
	require.NoError(t, err, "ReadThroughput after manual replace")
	rm2, ok := re.ThroughputProperties.ManualThroughput()
	require.True(t, ok)
	assert.EqualValues(t, 1000, rm2)

	// The autoscale maximum round-trips in x-ms-cosmos-offer-autopilot-settings.
	auto := azcosmos.NewAutoscaleThroughputProperties(4000)
	_, err = cc.ReplaceThroughput(ctx, auto, nil)
	require.NoError(t, err, "ReplaceThroughput (autoscale)")

	ra, err := cc.ReadThroughput(ctx, nil)
	require.NoError(t, err, "ReadThroughput after autoscale replace")
	maxRU, ok := ra.ThroughputProperties.AutoscaleMaxThroughput()
	require.True(t, ok, "offer must now be autoscale")
	assert.EqualValues(t, 4000, maxRU)
}

// TestCosmos_DatabaseThroughputOffers: the database-level offer round-trips
// through the same offers data plane, keyed off the database _rid.
func TestCosmos_DatabaseThroughputOffers(t *testing.T) {
	t.Parallel()
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	client := cosmosSimClient(t, dataPlaneAccount)
	dbThroughput := azcosmos.NewManualThroughputProperties(400)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "thrudb2"},
		&azcosmos.CreateDatabaseOptions{ThroughputProperties: &dbThroughput})
	require.NoError(t, err, "CreateDatabase with throughput")
	db, err := client.NewDatabase("thrudb2")
	require.NoError(t, err)

	read, err := db.ReadThroughput(ctx, nil)
	require.NoError(t, err, "DB ReadThroughput")
	manual, ok := read.ThroughputProperties.ManualThroughput()
	require.True(t, ok)
	assert.EqualValues(t, 400, manual)

	_, err = db.ReplaceThroughput(ctx, azcosmos.NewManualThroughputProperties(800), nil)
	require.NoError(t, err, "DB ReplaceThroughput")

	read2, err := db.ReadThroughput(ctx, nil)
	require.NoError(t, err)
	m2, ok := read2.ThroughputProperties.ManualThroughput()
	require.True(t, ok)
	assert.EqualValues(t, 800, m2)
}

// TestCosmos_RequestCharge: every response carries a non-zero
// x-ms-request-charge, and a write costs more than a point read of the same
// item.
func TestCosmos_RequestCharge(t *testing.T) {
	t.Parallel()
	dataPlaneAccount := uniquename.Alnum("sdkdataplane")
	client := cosmosSimClient(t, dataPlaneAccount)
	_, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "rudb"}, nil)
	require.NoError(t, err)
	db, _ := client.NewDatabase("rudb")
	_, err = db.CreateContainer(ctx, azcosmos.ContainerProperties{
		ID:                     "ruc",
		PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/pk"}},
	}, nil)
	require.NoError(t, err)
	cc, _ := client.NewContainer("rudb", "ruc")

	pk := azcosmos.NewPartitionKeyString("p")
	raw, _ := json.Marshal(map[string]any{"id": "x", "pk": "p", "name": "alice", "age": 30})
	createResp, err := cc.CreateItem(ctx, pk, raw, nil)
	require.NoError(t, err, "CreateItem")
	assert.Greater(t, createResp.RequestCharge, float32(0), "create must report a request charge")

	readResp, err := cc.ReadItem(ctx, pk, "x", nil)
	require.NoError(t, err, "ReadItem")
	assert.Greater(t, readResp.RequestCharge, float32(0), "read must report a request charge")

	assert.Greater(t, createResp.RequestCharge, readResp.RequestCharge,
		"a create must cost more RU than a point read of the same item")
}
