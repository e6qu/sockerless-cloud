package gcp_sdk_test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	redis "google.golang.org/api/redis/v1"
)

func redisEndpoint(host string, port int64) string {
	return net.JoinHostPort(host, strconv.FormatInt(port, 10))
}

// deleteRedisOnCleanup deletes a Memorystore instance or cluster when the test
// ends, which stops its engine and removes its volume.
func deleteRedisOnCleanup(t *testing.T, svc *redis.Service, name string) {
	t.Helper()
	t.Cleanup(func() {
		var op *redis.Operation
		var err error
		if strings.Contains(name, "/clusters/") {
			op, err = svc.Projects.Locations.Clusters.Delete(name).Do()
		} else {
			op, err = svc.Projects.Locations.Instances.Delete(name).Do()
		}
		if err == nil {
			awaitRedisLRO(t, svc, op)
		}
	})
}

// createRedisInstance creates an instance, waits for its operation and returns
// the instance as the service reports it.
func createRedisInstance(t *testing.T, svc *redis.Service, parent, id string, instance *redis.Instance) *redis.Instance {
	t.Helper()
	op, err := svc.Projects.Locations.Instances.Create(parent, instance).InstanceId(id).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	t.Cleanup(func() {
		if op, err := svc.Projects.Locations.Instances.Delete(parent + "/instances/" + id).Do(); err == nil {
			awaitRedisLRO(t, svc, op)
		}
	})
	got, err := svc.Projects.Locations.Instances.Get(parent + "/instances/" + id).Do()
	require.NoError(t, err)
	return got
}

// An instance is a Redis server at the host and port it reports: writes go to
// the primary, the read endpoint serves the replicas, and AUTH takes the
// string getAuthString returns.
func TestMemorystoreRedis_InstanceServesRedis(t *testing.T) {
	svc := redisService(t)
	parent := "projects/redis-engine/locations/us-central1"
	inst := createRedisInstance(t, svc, parent, "served", &redis.Instance{
		Tier: "STANDARD_HA", MemorySizeGb: 1, RedisVersion: "REDIS_7_2",
		AuthEnabled: true, ReadReplicasMode: "READ_REPLICAS_ENABLED", ReplicaCount: 1,
	})
	require.NotEmpty(t, inst.Host)
	assert.Equal(t, int64(6379), inst.Port)
	require.NotEmpty(t, inst.ReadEndpoint)
	assert.Equal(t, int64(1), inst.ReplicaCount)

	auth, err := svc.Projects.Locations.Instances.GetAuthString(inst.Name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, auth.AuthString)

	anonymous := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port)})
	t.Cleanup(func() { anonymous.Close() })
	err = anonymous.Get(ctx, "greeting").Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOAUTH", "an AUTH-enabled instance refuses a client that does not authenticate")

	primary := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port), Password: auth.AuthString})
	t.Cleanup(func() { primary.Close() })
	require.NoError(t, primary.Set(ctx, "greeting", "hello", 0).Err())
	value, err := primary.Get(ctx, "greeting").Result()
	require.NoError(t, err)
	assert.Equal(t, "hello", value)
	// WAIT returns once the replica acknowledged the write.
	acknowledged, err := primary.Wait(ctx, 1, 0).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), acknowledged)

	reader := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.ReadEndpoint, inst.ReadEndpointPort), Password: auth.AuthString})
	t.Cleanup(func() { reader.Close() })
	value, err = reader.Get(ctx, "greeting").Result()
	require.NoError(t, err)
	assert.Equal(t, "hello", value, "the read endpoint serves the replicated dataset")
	err = reader.Set(ctx, "greeting", "overwritten", 0).Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "READONLY")

	// A failover promotes the replica; the primary endpoint follows it.
	op, err := svc.Projects.Locations.Instances.Failover(inst.Name, &redis.FailoverInstanceRequest{}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	promoted := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port), Password: auth.AuthString})
	t.Cleanup(func() { promoted.Close() })
	value, err = promoted.Get(ctx, "greeting").Result()
	require.NoError(t, err)
	assert.Equal(t, "hello", value)
	require.NoError(t, promoted.Set(ctx, "after-failover", "written", 0).Err())

	op, err = svc.Projects.Locations.Instances.Delete(inst.Name).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	_, err = net.Dial("tcp", redisEndpoint(inst.Host, inst.Port))
	require.Error(t, err, "a deleted instance's endpoint no longer accepts connections")
}

// Export writes the engine's own RDB snapshot to Cloud Storage, and import
// restarts another instance on that file.
func TestMemorystore_ImportAndExportInstance(t *testing.T) {
	svc := redisService(t)
	gcs := storageClient(t)
	t.Cleanup(func() { gcs.Close() })
	const project, location = "redis-transfer", "us-central1"
	parent := "projects/" + project + "/locations/" + location
	bucket := uniqueName("redis-transfer")
	requireProject(t, project)
	require.NoError(t, gcs.Bucket(bucket).Create(ctx, project, nil))

	source := createRedisInstance(t, svc, parent, "source", &redis.Instance{Tier: "BASIC", MemorySizeGb: 1, RedisVersion: "REDIS_7_2"})
	client := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(source.Host, source.Port)})
	t.Cleanup(func() { client.Close() })
	for i := 0; i < 10; i++ {
		require.NoError(t, client.Set(ctx, fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), 0).Err())
	}

	exported, err := svc.Projects.Locations.Instances.Export(source.Name, &redis.ExportInstanceRequest{
		OutputConfig: &redis.OutputConfig{GcsDestination: &redis.GcsDestination{Uri: "gs://" + bucket + "/source.rdb"}},
	}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, exported)
	reader, err := gcs.Bucket(bucket).Object("source.rdb").NewReader(ctx)
	require.NoError(t, err)
	snapshot, err := io.ReadAll(reader)
	require.NoError(t, reader.Close())
	require.NoError(t, err)
	require.Greater(t, len(snapshot), 9)
	assert.Equal(t, "REDIS", string(snapshot[:5]), "the export is an RDB file")

	target := createRedisInstance(t, svc, parent, "target", &redis.Instance{Tier: "BASIC", MemorySizeGb: 1, RedisVersion: "REDIS_7_2"})
	targetClient := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(target.Host, target.Port)})
	t.Cleanup(func() { targetClient.Close() })
	require.NoError(t, targetClient.Set(ctx, "replaced", "by the import", 0).Err())

	imported, err := svc.Projects.Locations.Instances.Import(target.Name, &redis.ImportInstanceRequest{
		InputConfig: &redis.InputConfig{GcsSource: &redis.GcsSource{Uri: "gs://" + bucket + "/source.rdb"}},
	}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, imported)
	got, err := svc.Projects.Locations.Instances.Get(target.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", got.State)
	for i := 0; i < 10; i++ {
		value, err := targetClient.Get(ctx, fmt.Sprintf("key-%d", i)).Result()
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("value-%d", i), value)
	}
	err = targetClient.Get(ctx, "replaced").Err()
	assert.True(t, errors.Is(err, goredis.Nil), "the instance holds only the imported data, got %v", err)

	// A file that is not an RDB fails the import's operation.
	writer := gcs.Bucket(bucket).Object("notes.txt").NewWriter(ctx)
	_, err = writer.Write([]byte("not a snapshot"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	failed, err := svc.Projects.Locations.Instances.Import(target.Name, &redis.ImportInstanceRequest{
		InputConfig: &redis.InputConfig{GcsSource: &redis.GcsSource{Uri: "gs://" + bucket + "/notes.txt"}},
	}).Do()
	require.NoError(t, err)
	require.True(t, failed.Done)
	require.NotNil(t, failed.Error)
	assert.Equal(t, int64(3), failed.Error.Code)

	// An export to a bucket that does not exist fails its operation.
	failed, err = svc.Projects.Locations.Instances.Export(source.Name, &redis.ExportInstanceRequest{
		OutputConfig: &redis.OutputConfig{GcsDestination: &redis.GcsDestination{Uri: "gs://" + bucket + "-absent/source.rdb"}},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, failed.Error)
	assert.Equal(t, int64(5), failed.Error.Code)

	// A transfer with nowhere to go is refused.
	_, err = svc.Projects.Locations.Instances.Export(source.Name, &redis.ExportInstanceRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cloud Storage URI")

	// And one pointed somewhere that is not Cloud Storage.
	_, err = svc.Projects.Locations.Instances.Import(source.Name, &redis.ImportInstanceRequest{
		InputConfig: &redis.InputConfig{GcsSource: &redis.GcsSource{Uri: "https://example.com/cache.rdb"}},
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a Cloud Storage URI")

	// An instance that is not there has nothing to move.
	_, err = svc.Projects.Locations.Instances.Export(parent+"/instances/absent", &redis.ExportInstanceRequest{
		OutputConfig: &redis.OutputConfig{GcsDestination: &redis.GcsDestination{Uri: "gs://" + bucket + "/absent.rdb"}},
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// A cluster is a Redis cluster behind its discovery endpoint: a cluster client
// learns the shards there and follows their redirections, and a backup holds
// each shard's RDB snapshot, which export writes to the bucket.
func TestMemorystoreRedis_ClusterServesRedis(t *testing.T) {
	svc := redisService(t)
	gcs := storageClient(t)
	t.Cleanup(func() { gcs.Close() })
	const project = "redis-cluster-engine"
	parent := "projects/" + project + "/locations/us-central1"
	name := parent + "/clusters/served"

	op, err := svc.Projects.Locations.Clusters.Create(parent, &redis.Cluster{ShardCount: 2, ReplicaCount: 1}).ClusterId("served").Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	t.Cleanup(func() {
		if op, err := svc.Projects.Locations.Clusters.Delete(name).Do(); err == nil {
			awaitRedisLRO(t, svc, op)
		}
	})
	cluster, err := svc.Projects.Locations.Clusters.Get(name).Do()
	require.NoError(t, err)
	require.Len(t, cluster.DiscoveryEndpoints, 1)
	discovery := redisEndpoint(cluster.DiscoveryEndpoints[0].Address, cluster.DiscoveryEndpoints[0].Port)

	client := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{discovery}})
	t.Cleanup(func() { client.Close() })
	for i := 0; i < 20; i++ {
		require.NoError(t, client.Set(ctx, fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), 0).Err())
	}
	for i := 0; i < 20; i++ {
		value, err := client.Get(ctx, fmt.Sprintf("key-%d", i)).Result()
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("value-%d", i), value)
	}
	shards, err := client.ClusterShards(ctx).Result()
	require.NoError(t, err)
	assert.Len(t, shards, 2)
	for _, shard := range shards {
		assert.Len(t, shard.Nodes, 2, "every shard has its primary and one replica")
	}

	op, err = svc.Projects.Locations.Clusters.Backup(name, &redis.BackupClusterRequest{BackupId: "engine-backup"}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	backupName := parent + "/backupCollections/served/backups/engine-backup"
	backup, err := svc.Projects.Locations.BackupCollections.Backups.Get(backupName).Do()
	require.NoError(t, err)
	require.Len(t, backup.BackupFiles, 2, "one RDB file per shard")
	assert.Equal(t, "redis-7.2", backup.EngineVersion)
	total := backup.TotalSizeBytes
	assert.Positive(t, total)

	bucket := uniqueName("cluster-backups")
	requireProject(t, project)
	require.NoError(t, gcs.Bucket(bucket).Create(ctx, project, nil))
	op, err = svc.Projects.Locations.BackupCollections.Backups.Export(backupName, &redis.ExportBackupRequest{GcsBucket: bucket}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	var exported int64
	for _, file := range backup.BackupFiles {
		reader, err := gcs.Bucket(bucket).Object("engine-backup/" + file.FileName).NewReader(ctx)
		require.NoError(t, err, "export wrote %s", file.FileName)
		data, err := io.ReadAll(reader)
		require.NoError(t, reader.Close())
		require.NoError(t, err)
		assert.Equal(t, "REDIS", string(data[:5]), "%s is an RDB file", file.FileName)
		assert.Equal(t, file.SizeBytes, int64(len(data)))
		exported += int64(len(data))
	}
	assert.Equal(t, total, exported)
}
