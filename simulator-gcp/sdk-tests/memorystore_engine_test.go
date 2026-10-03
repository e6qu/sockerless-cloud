package gcp_sdk_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	redisinstances "cloud.google.com/go/redis/apiv1"
	"cloud.google.com/go/redis/apiv1/redispb"
	rediscluster "cloud.google.com/go/redis/cluster/apiv1"
	"cloud.google.com/go/redis/cluster/apiv1/clusterpb"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	redis "google.golang.org/api/redis/v1"
)

func redisInstancesClient(t *testing.T) *redisinstances.CloudRedisClient {
	t.Helper()
	client, err := redisinstances.NewCloudRedisRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func redisClusterClient(t *testing.T) *rediscluster.CloudRedisClusterClient {
	t.Helper()
	client, err := rediscluster.NewCloudRedisClusterRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// createRedisCluster creates a cluster, waits for its operation, deletes it
// when the test ends and returns it as the service reports it.
func createRedisCluster(t *testing.T, svc *redis.Service, parent, id string, cluster *redis.Cluster) *redis.Cluster {
	t.Helper()
	name := parent + "/clusters/" + id
	op, err := svc.Projects.Locations.Clusters.Create(parent, cluster).ClusterId(id).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	deleteRedisOnCleanup(t, svc, name)
	got, err := svc.Projects.Locations.Clusters.Get(name).Do()
	require.NoError(t, err)
	return got
}

func patchRedisCluster(t *testing.T, svc *redis.Service, name, mask string, cluster *redis.Cluster) *redis.Cluster {
	t.Helper()
	op, err := svc.Projects.Locations.Clusters.Patch(name, cluster).UpdateMask(mask).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	got, err := svc.Projects.Locations.Clusters.Get(name).Do()
	require.NoError(t, err)
	return got
}

func patchRedisInstance(t *testing.T, svc *redis.Service, name, mask string, instance *redis.Instance) *redis.Instance {
	t.Helper()
	op, err := svc.Projects.Locations.Instances.Patch(name, instance).UpdateMask(mask).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	got, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	return got
}

func discoveryAddress(cluster *redis.Cluster) string {
	return redisEndpoint(cluster.DiscoveryEndpoints[0].Address, cluster.DiscoveryEndpoints[0].Port)
}

// clusterClient connects to a cluster's discovery endpoint with options.
func clusterClient(t *testing.T, cluster *redis.Cluster, options goredis.ClusterOptions) *goredis.ClusterClient {
	t.Helper()
	options.Addrs = []string{discoveryAddress(cluster)}
	client := goredis.NewClusterClient(&options)
	t.Cleanup(func() { client.Close() })
	return client
}

func writeKeys(t *testing.T, client goredis.Cmdable, prefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		require.NoError(t, client.Set(ctx, fmt.Sprintf("%s-%d", prefix, i), fmt.Sprintf("value-%d", i), 0).Err())
	}
}

func requireKeys(t *testing.T, client goredis.Cmdable, prefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		value, err := client.Get(ctx, fmt.Sprintf("%s-%d", prefix, i)).Result()
		require.NoError(t, err, "key %s-%d", prefix, i)
		require.Equal(t, fmt.Sprintf("value-%d", i), value)
	}
}

// requireShards asserts the topology a fresh client reads from the cluster:
// shards shards whose slots cover the keyspace, each with its primary and
// replicas replicas.
func requireShards(t *testing.T, cluster *redis.Cluster, options goredis.ClusterOptions, shards, replicas int) {
	t.Helper()
	client := clusterClient(t, cluster, options)
	got, err := client.ClusterShards(ctx).Result()
	require.NoError(t, err)
	require.Len(t, got, shards)
	covered := 0
	for _, shard := range got {
		assert.Len(t, shard.Nodes, replicas+1, "shard %v", shard.Slots)
		for _, slots := range shard.Slots {
			covered += int(slots.End-slots.Start) + 1
		}
	}
	assert.Equal(t, 16384, covered, "the shards' slots cover the keyspace")
}

func caPool(t *testing.T, pems ...string) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	for _, pem := range pems {
		require.True(t, pool.AppendCertsFromPEM([]byte(pem)), "the reported CA is PEM")
	}
	return pool
}

// An instance that encrypts in transit serves TLS on port 6378 with a
// certificate its reported server CA signed, and nothing in plaintext; an RDB
// persistence config takes snapshots on the schedule it names.
func TestMemorystoreRedis_InstanceTLSAndPersistence(t *testing.T) {
	svc := redisService(t)
	parent := "projects/redis-tls/locations/us-central1"
	start := time.Now().Add(15 * time.Second).UTC().Truncate(time.Second)
	inst := createRedisInstance(t, svc, parent, "encrypted", &redis.Instance{
		Tier: "STANDARD_HA", MemorySizeGb: 1, RedisVersion: "REDIS_7_2", AuthEnabled: true,
		ReadReplicasMode: "READ_REPLICAS_ENABLED", ReplicaCount: 1,
		TransitEncryptionMode: "SERVER_AUTHENTICATION",
		PersistenceConfig: &redis.PersistenceConfig{
			PersistenceMode: "RDB", RdbSnapshotPeriod: "ONE_HOUR", RdbSnapshotStartTime: start.Format(time.RFC3339),
		},
	})
	assert.Equal(t, int64(6378), inst.Port, "an instance that encrypts in transit serves port 6378")
	assert.Equal(t, int64(6378), inst.ReadEndpointPort)
	require.Len(t, inst.ServerCaCerts, 1)
	assert.NotEmpty(t, inst.ServerCaCerts[0].SerialNumber)
	assert.Len(t, inst.ServerCaCerts[0].Sha1Fingerprint, 40)
	require.NotNil(t, inst.PersistenceConfig)
	assert.Equal(t, "RDB", inst.PersistenceConfig.PersistenceMode)
	assert.Equal(t, "ONE_HOUR", inst.PersistenceConfig.RdbSnapshotPeriod)
	assert.Equal(t, start.Format(time.RFC3339), inst.PersistenceConfig.RdbNextSnapshotTime,
		"a start time still ahead is the first snapshot")

	// The cloud.google.com/go/redis client reads the same server CA.
	typed, err := redisInstancesClient(t).GetInstance(ctx, &redispb.GetInstanceRequest{Name: inst.Name})
	require.NoError(t, err)
	require.Len(t, typed.ServerCaCerts, 1)
	assert.Equal(t, inst.ServerCaCerts[0].Cert, typed.ServerCaCerts[0].Cert)
	assert.Equal(t, redispb.Instance_SERVER_AUTHENTICATION, typed.TransitEncryptionMode)
	assert.Equal(t, redispb.PersistenceConfig_RDB, typed.PersistenceConfig.PersistenceMode)

	auth, err := svc.Projects.Locations.Instances.GetAuthString(inst.Name).Do()
	require.NoError(t, err)
	tlsConfig := &tls.Config{RootCAs: caPool(t, inst.ServerCaCerts[0].Cert), MinVersion: tls.VersionTLS12}
	primary := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port), Password: auth.AuthString, TLSConfig: tlsConfig})
	t.Cleanup(func() { primary.Close() })
	require.NoError(t, primary.Set(ctx, "greeting", "over tls", 0).Err())
	acknowledged, err := primary.Wait(ctx, 1, 0).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), acknowledged)
	reader := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.ReadEndpoint, inst.ReadEndpointPort), Password: auth.AuthString, TLSConfig: tlsConfig})
	t.Cleanup(func() { reader.Close() })
	value, err := reader.Get(ctx, "greeting").Result()
	require.NoError(t, err)
	assert.Equal(t, "over tls", value)

	untrusted := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port), Password: auth.AuthString,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxRetries: -1})
	t.Cleanup(func() { untrusted.Close() })
	err = untrusted.Ping(ctx).Err()
	require.Error(t, err, "a client that does not trust the server CA refuses the endpoint")
	var unknownAuthority x509.UnknownAuthorityError
	assert.True(t, errors.As(err, &unknownAuthority), "got %v", err)

	plaintext := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port), Password: auth.AuthString,
		MaxRetries: -1, DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second})
	t.Cleanup(func() { plaintext.Close() })
	assert.Error(t, plaintext.Ping(ctx).Err(), "the endpoint speaks no plaintext")
	_, err = net.DialTimeout("tcp", redisEndpoint(inst.Host, 6379), 2*time.Second)
	assert.Error(t, err, "nothing serves Redis's plaintext port")

	// Snapshots are the service's schedule, not the engine's save points.
	saves, err := primary.ConfigGet(ctx, "save").Result()
	require.NoError(t, err)
	assert.Equal(t, "", saves["save"])
	appendonly, err := primary.ConfigGet(ctx, "appendonly").Result()
	require.NoError(t, err)
	assert.Equal(t, "no", appendonly["appendonly"])

	// The engine writes its snapshot at the time the API reported.
	time.Sleep(time.Until(start))
	deadline := start.Add(30 * time.Second)
	for {
		last, err := primary.LastSave(ctx).Result()
		require.NoError(t, err)
		if last >= start.Unix() {
			break
		}
		require.True(t, time.Now().Before(deadline), "no RDB snapshot by %s; LASTSAVE is %d", deadline, last)
		time.Sleep(200 * time.Millisecond)
	}
	got, err := svc.Projects.Locations.Instances.Get(inst.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, start.Add(time.Hour).Format(time.RFC3339), got.PersistenceConfig.RdbNextSnapshotTime)

	disabled := patchRedisInstance(t, svc, inst.Name, "persistenceConfig", &redis.Instance{
		PersistenceConfig: &redis.PersistenceConfig{PersistenceMode: "DISABLED"},
	})
	assert.Equal(t, "DISABLED", disabled.PersistenceConfig.PersistenceMode)
	assert.Empty(t, disabled.PersistenceConfig.RdbNextSnapshotTime)
}

// A replicaCount update starts or stops replica nodes while the primary keeps
// serving, and an authEnabled update makes the engine require the AUTH string.
func TestMemorystoreRedis_InstanceReplicaCountAndAuthUpdate(t *testing.T) {
	svc := redisService(t)
	parent := "projects/redis-resize/locations/us-central1"
	inst := createRedisInstance(t, svc, parent, "resized", &redis.Instance{
		Tier: "STANDARD_HA", MemorySizeGb: 1, RedisVersion: "REDIS_7_2",
		ReadReplicasMode: "READ_REPLICAS_ENABLED", ReplicaCount: 1,
	})
	primary := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port)})
	t.Cleanup(func() { primary.Close() })
	writeKeys(t, primary, "resize", 20)

	connectedReplicas := func(client *goredis.Client) int {
		t.Helper()
		info, err := client.Info(ctx, "replication").Result()
		require.NoError(t, err)
		for _, line := range strings.Split(info, "\n") {
			if value, found := strings.CutPrefix(strings.TrimSpace(line), "connected_slaves:"); found {
				count, err := strconv.Atoi(value)
				require.NoError(t, err)
				return count
			}
		}
		t.Fatalf("INFO replication has no connected_slaves:\n%s", info)
		return 0
	}
	assert.Equal(t, 1, connectedReplicas(primary))

	grown := patchRedisInstance(t, svc, inst.Name, "replicaCount", &redis.Instance{ReplicaCount: 3})
	assert.Equal(t, int64(3), grown.ReplicaCount)
	assert.Equal(t, "READY", grown.State)
	assert.Equal(t, 3, connectedReplicas(primary), "the primary serves three replicas")
	acknowledged, err := primary.Wait(ctx, 3, 0).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(3), acknowledged)
	reader := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(grown.ReadEndpoint, grown.ReadEndpointPort)})
	t.Cleanup(func() { reader.Close() })
	requireKeys(t, reader, "resize", 20)

	shrunk := patchRedisInstance(t, svc, inst.Name, "replicaCount", &redis.Instance{ReplicaCount: 2})
	assert.Equal(t, int64(2), shrunk.ReplicaCount)
	assert.Equal(t, 2, connectedReplicas(primary))
	requireKeys(t, primary, "resize", 20)

	_, err = svc.Projects.Locations.Instances.Patch(inst.Name, &redis.Instance{ReplicaCount: 6}).UpdateMask("replicaCount").Do()
	require.Error(t, err, "an instance runs at most five read replicas")
	assert.Contains(t, err.Error(), "replicaCount")

	secured := patchRedisInstance(t, svc, inst.Name, "authEnabled", &redis.Instance{AuthEnabled: true})
	assert.True(t, secured.AuthEnabled)
	auth, err := svc.Projects.Locations.Instances.GetAuthString(inst.Name).Do()
	require.NoError(t, err)
	require.NotEmpty(t, auth.AuthString)
	anonymous := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port), MaxRetries: -1})
	t.Cleanup(func() { anonymous.Close() })
	err = anonymous.Get(ctx, "resize-0").Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOAUTH")
	authenticated := goredis.NewClient(&goredis.Options{Addr: redisEndpoint(inst.Host, inst.Port), Password: auth.AuthString})
	t.Cleanup(func() { authenticated.Close() })
	requireKeys(t, authenticated, "resize", 20)
	acknowledged, err = authenticated.Wait(ctx, 2, 0).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(2), acknowledged, "the replicas authenticate to the primary with the new AUTH string")
}

// A Basic Tier instance has no replica, so the service refuses its failover.
func TestMemorystoreRedis_BasicTierFailoverRefused(t *testing.T) {
	svc := redisService(t)
	parent := "projects/redis-failover/locations/us-central1"
	inst := createRedisInstance(t, svc, parent, "basic", &redis.Instance{Tier: "BASIC", MemorySizeGb: 1, RedisVersion: "REDIS_7_2"})
	_, err := svc.Projects.Locations.Instances.Failover(inst.Name, &redis.FailoverInstanceRequest{DataProtectionMode: "LIMITED_DATA_LOSS"}).Do()
	require.Error(t, err)
	var apiErr *googleapi.Error
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 400, apiErr.Code)
	assert.Contains(t, apiErr.Body, "FAILED_PRECONDITION")
	got, err := svc.Projects.Locations.Instances.Get(inst.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", got.State)
}

// A shardCount or replicaCount update reshapes the running cluster: new
// shards take slots and keys from the old ones, removed shards hand theirs
// back, and every key stays readable throughout.
func TestMemorystoreRedis_ClusterReshard(t *testing.T) {
	svc := redisService(t)
	parent := "projects/redis-reshard/locations/us-central1"
	cluster := createRedisCluster(t, svc, parent, "reshaped", &redis.Cluster{
		ShardCount: 2, ReplicaCount: 0,
		PersistenceConfig: &redis.ClusterPersistenceConfig{Mode: "AOF", AofConfig: &redis.AOFConfig{AppendFsync: "ALWAYS"}},
	})
	require.NotNil(t, cluster.PersistenceConfig)
	assert.Equal(t, "AOF", cluster.PersistenceConfig.Mode)
	assert.Equal(t, "ALWAYS", cluster.PersistenceConfig.AofConfig.AppendFsync)
	client := clusterClient(t, cluster, goredis.ClusterOptions{})
	writeKeys(t, client, "reshard", 300)
	requireShards(t, cluster, goredis.ClusterOptions{}, 2, 0)

	cluster = patchRedisCluster(t, svc, cluster.Name, "shardCount", &redis.Cluster{ShardCount: 3})
	assert.Equal(t, int64(3), cluster.ShardCount)
	assert.Equal(t, "ACTIVE", cluster.State)
	requireShards(t, cluster, goredis.ClusterOptions{}, 3, 0)
	requireKeys(t, client, "reshard", 300)
	err := client.ForEachMaster(ctx, func(ctx context.Context, node *goredis.Client) error {
		size, err := node.DBSize(ctx).Result()
		if err == nil && size == 0 {
			return fmt.Errorf("primary %s holds no keys after the reshard", node.Options().Addr)
		}
		return err
	})
	require.NoError(t, err, "the new shard holds keys moved from the old ones")

	cluster = patchRedisCluster(t, svc, cluster.Name, "replicaCount", &redis.Cluster{ReplicaCount: 1})
	assert.Equal(t, int64(1), cluster.ReplicaCount)
	requireShards(t, cluster, goredis.ClusterOptions{}, 3, 1)
	err = client.ForEachShard(ctx, func(ctx context.Context, node *goredis.Client) error {
		config, err := node.ConfigGet(ctx, "append*").Result()
		if err != nil {
			return err
		}
		if config["appendonly"] != "yes" || config["appendfsync"] != "always" {
			return fmt.Errorf("node %s runs appendonly=%q appendfsync=%q", node.Options().Addr, config["appendonly"], config["appendfsync"])
		}
		return nil
	})
	require.NoError(t, err, "every node, the new replicas included, appends as the persistence config says")

	cluster = patchRedisCluster(t, svc, cluster.Name, "shardCount", &redis.Cluster{ShardCount: 2})
	assert.Equal(t, int64(2), cluster.ShardCount)
	requireShards(t, cluster, goredis.ClusterOptions{}, 2, 1)
	fresh := clusterClient(t, cluster, goredis.ClusterOptions{})
	requireKeys(t, fresh, "reshard", 300)

	cluster = patchRedisCluster(t, svc, cluster.Name, "persistenceConfig", &redis.Cluster{
		PersistenceConfig: &redis.ClusterPersistenceConfig{Mode: "RDB", RdbConfig: &redis.RDBConfig{RdbSnapshotPeriod: "SIX_HOURS"}},
	})
	assert.Equal(t, "RDB", cluster.PersistenceConfig.Mode)
	assert.Equal(t, "SIX_HOURS", cluster.PersistenceConfig.RdbConfig.RdbSnapshotPeriod)
	err = fresh.ForEachShard(ctx, func(ctx context.Context, node *goredis.Client) error {
		config, err := node.ConfigGet(ctx, "appendonly").Result()
		if err == nil && config["appendonly"] != "no" {
			return fmt.Errorf("node %s still appends", node.Options().Addr)
		}
		return err
	})
	require.NoError(t, err)

	_, err = svc.Projects.Locations.Clusters.Patch(cluster.Name, &redis.Cluster{ShardCount: 0, ForceSendFields: []string{"ShardCount"}}).UpdateMask("shardCount").Do()
	require.Error(t, err, "a cluster keeps at least one shard")
}

// A cluster created from a managed backup or from RDB files in Cloud Storage
// starts with their keys, spread over its own shards.
func TestMemorystoreRedis_ClusterImportSources(t *testing.T) {
	svc := redisService(t)
	gcs := storageClient(t)
	t.Cleanup(func() { gcs.Close() })
	const project = "redis-import"
	parent := "projects/" + project + "/locations/us-central1"

	source := createRedisCluster(t, svc, parent, "source", &redis.Cluster{ShardCount: 2})
	writeKeys(t, clusterClient(t, source, goredis.ClusterOptions{}), "imported", 100)
	op, err := svc.Projects.Locations.Clusters.Backup(source.Name, &redis.BackupClusterRequest{BackupId: "seed"}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	backupName := parent + "/backupCollections/source/backups/seed"

	fromBackup := createRedisCluster(t, svc, parent, "from-backup", &redis.Cluster{
		ShardCount:          3,
		ManagedBackupSource: &redis.ManagedBackupSource{Backup: "//redis.googleapis.com/" + backupName},
	})
	requireShards(t, fromBackup, goredis.ClusterOptions{}, 3, 0)
	requireKeys(t, clusterClient(t, fromBackup, goredis.ClusterOptions{}), "imported", 100)

	bucket := uniqueName("redis-import")
	requireProject(t, project)
	require.NoError(t, gcs.Bucket(bucket).Create(ctx, project, nil))
	op, err = svc.Projects.Locations.BackupCollections.Backups.Export(backupName, &redis.ExportBackupRequest{GcsBucket: bucket}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	backup, err := svc.Projects.Locations.BackupCollections.Backups.Get(backupName).Do()
	require.NoError(t, err)
	var uris []string
	for _, file := range backup.BackupFiles {
		uris = append(uris, "gs://"+bucket+"/seed/"+file.FileName)
	}
	fromGCS := createRedisCluster(t, svc, parent, "from-gcs", &redis.Cluster{
		ShardCount: 1, GcsSource: &redis.GcsBackupSource{Uris: uris},
	})
	requireKeys(t, clusterClient(t, fromGCS, goredis.ClusterOptions{}), "imported", 100)

	writer := gcs.Bucket(bucket).Object("notes.txt").NewWriter(ctx)
	_, err = io.WriteString(writer, "not a snapshot")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	_, err = svc.Projects.Locations.Clusters.Create(parent, &redis.Cluster{
		ShardCount: 1, GcsSource: &redis.GcsBackupSource{Uris: []string{"gs://" + bucket + "/notes.txt"}},
	}).ClusterId("from-notes").Do()
	require.Error(t, err, "an object that is not an RDB file imports nothing")
	assert.Contains(t, err.Error(), "not an RDB file")
	_, err = svc.Projects.Locations.Clusters.Get(parent + "/clusters/from-notes").Do()
	require.Error(t, err, "a cluster whose import failed is not created")

	_, err = svc.Projects.Locations.Clusters.Create(parent, &redis.Cluster{
		ShardCount: 1, ManagedBackupSource: &redis.ManagedBackupSource{Backup: parent + "/backupCollections/source/backups/absent"},
	}).ClusterId("from-absent").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// A cluster that encrypts in transit and authenticates with IAM serves TLS
// from the CA getCertificateAuthority reports, and admits a client whose AUTH
// password is an access token of a principal holding redis.clusters.connect.
func TestMemorystoreRedis_ClusterTLSAndIAMAuth(t *testing.T) {
	svc := redisService(t)
	const project = "test-project"
	parent := "projects/" + project + "/locations/us-central1"
	cluster := createRedisCluster(t, svc, parent, "iam-tls", &redis.Cluster{
		ShardCount: 2, ReplicaCount: 0,
		TransitEncryptionMode: "TRANSIT_ENCRYPTION_MODE_SERVER_AUTHENTICATION",
		AuthorizationMode:     "AUTH_MODE_IAM_AUTH",
	})
	assert.Equal(t, "SERVER_CA_MODE_GOOGLE_MANAGED_PER_INSTANCE_CA", cluster.ServerCaMode)
	authority, err := redisClusterClient(t).GetClusterCertificateAuthority(ctx, &clusterpb.GetClusterCertificateAuthorityRequest{
		Name: cluster.Name + "/certificateAuthority",
	})
	require.NoError(t, err)
	chains := authority.GetManagedServerCa().GetCaCerts()
	require.Len(t, chains, 1)
	require.NotEmpty(t, chains[0].Certificates)
	tlsConfig := &tls.Config{RootCAs: caPool(t, chains[0].Certificates...), MinVersion: tls.VersionTLS12}

	token, err := simTokenSource().Token()
	require.NoError(t, err)
	client := clusterClient(t, cluster, goredis.ClusterOptions{TLSConfig: tlsConfig, Password: token.AccessToken})
	writeKeys(t, client, "iam", 50)
	requireKeys(t, client, "iam", 50)
	requireShards(t, cluster, goredis.ClusterOptions{TLSConfig: tlsConfig, Password: token.AccessToken}, 2, 0)

	forged := clusterClient(t, cluster, goredis.ClusterOptions{TLSConfig: tlsConfig, Password: "not-a-token", MaxRetries: -1})
	err = forged.Ping(ctx).Err()
	require.Error(t, err, "a password that is not an access token is refused")
	assert.Contains(t, err.Error(), "WRONGPASS")

	anonymous := clusterClient(t, cluster, goredis.ClusterOptions{TLSConfig: tlsConfig, MaxRetries: -1})
	err = anonymous.Get(ctx, "iam-0").Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOAUTH")

	// A service account holds no redis.clusters.connect until it is granted
	// the Database Connection User role.
	_, _, keyFile := mintServiceAccountKeyFile(t, "redis-connector")
	creds := tokenSourceFromKeyFile(t, keyFile)
	accountToken, err := creds.TokenSource.Token()
	require.NoError(t, err)
	member := "serviceAccount:" + fmt.Sprint(keyFile["client_email"])
	unbound := clusterClient(t, cluster, goredis.ClusterOptions{TLSConfig: tlsConfig, Password: accountToken.AccessToken, MaxRetries: -1})
	err = unbound.Ping(ctx).Err()
	require.Error(t, err, "a principal without redis.clusters.connect is refused")
	assert.Contains(t, err.Error(), "WRONGPASS")

	editProjectPolicy(t, project, func(policy *cloudresourcemanager.Policy) {
		policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{Role: "roles/redis.dbConnectionUser", Members: []string{member}})
	})
	t.Cleanup(func() {
		editProjectPolicy(t, project, func(policy *cloudresourcemanager.Policy) {
			var kept []*cloudresourcemanager.Binding
			for _, binding := range policy.Bindings {
				if binding.Role != "roles/redis.dbConnectionUser" {
					kept = append(kept, binding)
				}
			}
			policy.Bindings = kept
		})
	})
	bound := clusterClient(t, cluster, goredis.ClusterOptions{TLSConfig: tlsConfig, Password: accountToken.AccessToken})
	requireKeys(t, bound, "iam", 50)

	_, err = net.DialTimeout("tcp", discoveryAddress(cluster), 2*time.Second)
	require.NoError(t, err)
	plaintext := clusterClient(t, cluster, goredis.ClusterOptions{Password: token.AccessToken, MaxRetries: -1,
		DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second})
	assert.Error(t, plaintext.Ping(ctx).Err(), "the cluster speaks no plaintext")

	shared, err := svc.Projects.Locations.GetSharedRegionalCertificateAuthority(parent + "/sharedRegionalCertificateAuthority").Do()
	require.NoError(t, err)
	require.NotNil(t, shared.ManagedServerCa)
	assert.NotEqual(t, chains[0].Certificates[0], shared.ManagedServerCa.CaCerts[0].Certificates[0],
		"a cluster with its own CA does not present the region's shared one")
}

// A cluster that authenticates with tokens admits a client as a token-auth
// user with one of that user's active auth tokens, and no other.
func TestMemorystoreRedis_ClusterTokenAuth(t *testing.T) {
	svc := redisService(t)
	parent := "projects/redis-token-auth/locations/us-central1"
	cluster := createRedisCluster(t, svc, parent, "tokens", &redis.Cluster{
		ShardCount: 1, ReplicaCount: 1, AuthorizationMode: "AUTH_MODE_TOKEN_AUTH",
	})
	op, err := svc.Projects.Locations.Clusters.AddTokenAuthUser(cluster.Name, &redis.AddTokenAuthUserRequest{TokenAuthUser: "app"}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	user := cluster.Name + "/tokenAuthUsers/app"
	op, err = svc.Projects.Locations.Clusters.TokenAuthUsers.AddAuthToken(user, &redis.AddAuthTokenRequest{AuthToken: &redis.AuthToken{Name: "first"}}).Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	first, err := svc.Projects.Locations.Clusters.TokenAuthUsers.AuthTokens.Get(user + "/authTokens/first").Do()
	require.NoError(t, err)
	require.NotEmpty(t, first.Token)

	client := clusterClient(t, cluster, goredis.ClusterOptions{Username: "app", Password: first.Token})
	writeKeys(t, client, "token", 20)
	requireKeys(t, client, "token", 20)
	requireShards(t, cluster, goredis.ClusterOptions{Username: "app", Password: first.Token}, 1, 1)

	wrong := clusterClient(t, cluster, goredis.ClusterOptions{Username: "app", Password: "guessed", MaxRetries: -1})
	err = wrong.Ping(ctx).Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WRONGPASS")

	op, err = svc.Projects.Locations.Clusters.TokenAuthUsers.AuthTokens.Delete(user + "/authTokens/first").Do()
	require.NoError(t, err)
	awaitRedisLRO(t, svc, op)
	revoked := clusterClient(t, cluster, goredis.ClusterOptions{Username: "app", Password: first.Token, MaxRetries: -1})
	err = revoked.Ping(ctx).Err()
	require.Error(t, err, "a deleted token no longer authenticates")
	assert.Contains(t, err.Error(), "WRONGPASS")
}
