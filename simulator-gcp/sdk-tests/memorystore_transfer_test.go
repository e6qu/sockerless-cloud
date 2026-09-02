package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	redis "google.golang.org/api/redis/v1"
)

// Moving a Memorystore instance's contents to or from Cloud Storage. Both
// directions need somewhere to read from or write to — a transfer with no URI
// is not a transfer, and accepting one would report success for a move that
// never happened.
func TestMemorystore_ImportAndExportInstance(t *testing.T) {
	svc := redisService(t)
	const project, location, id = "redis-transfer", "us-central1", "cache"
	parent := "projects/" + project + "/locations/" + location
	name := parent + "/instances/" + id

	_, err := svc.Projects.Locations.Instances.Create(parent, &redis.Instance{
		Tier: "BASIC", MemorySizeGb: 1,
	}).InstanceId(id).Do()
	require.NoError(t, err)

	exported, err := svc.Projects.Locations.Instances.Export(name, &redis.ExportInstanceRequest{
		OutputConfig: &redis.OutputConfig{
			GcsDestination: &redis.GcsDestination{Uri: "gs://backups/cache.rdb"},
		},
	}).Do()
	require.NoError(t, err)
	assert.True(t, exported.Done)

	imported, err := svc.Projects.Locations.Instances.Import(name, &redis.ImportInstanceRequest{
		InputConfig: &redis.InputConfig{
			GcsSource: &redis.GcsSource{Uri: "gs://backups/cache.rdb"},
		},
	}).Do()
	require.NoError(t, err)
	assert.True(t, imported.Done)

	// The instance settles back to READY, which is what a client polling it
	// sees once the transfer is over.
	got, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", got.State)

	// A transfer with nowhere to go is refused.
	_, err = svc.Projects.Locations.Instances.Export(name,
		&redis.ExportInstanceRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cloud Storage URI")

	// And one pointed somewhere that is not Cloud Storage.
	_, err = svc.Projects.Locations.Instances.Import(name, &redis.ImportInstanceRequest{
		InputConfig: &redis.InputConfig{
			GcsSource: &redis.GcsSource{Uri: "https://example.com/cache.rdb"},
		},
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a Cloud Storage URI")

	// An instance that is not there has nothing to move.
	_, err = svc.Projects.Locations.Instances.Export(parent+"/instances/absent",
		&redis.ExportInstanceRequest{
			OutputConfig: &redis.OutputConfig{
				GcsDestination: &redis.GcsDestination{Uri: "gs://backups/absent.rdb"},
			},
		}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
