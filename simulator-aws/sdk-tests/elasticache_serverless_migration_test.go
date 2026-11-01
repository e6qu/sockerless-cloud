package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecAwaitMigration polls the serverless cache's Migration.Status until it
// reads want, or until the cache has no migration when want is empty.
func ecAwaitMigration(t *testing.T, c *elasticache.Client, name, want string) ectypes.ServerlessCache {
	t.Helper()
	deadline := time.Now().Add(ecEngineWait)
	for {
		out, err := c.DescribeServerlessCaches(t.Context(), &elasticache.DescribeServerlessCachesInput{ServerlessCacheName: aws.String(name)})
		require.NoError(t, err)
		require.Len(t, out.ServerlessCaches, 1)
		got := out.ServerlessCaches[0]
		status := ""
		if got.Migration != nil {
			status = aws.ToString(got.Migration.Status)
		}
		if status == want {
			return got
		}
		require.True(t, time.Now().Before(deadline), "the migration of %s reads %q, not %q", name, status, want)
		time.Sleep(time.Second)
	}
}

func ecUnansweredPort(t *testing.T) int32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return int32(port)
}

// A write the source primary that owns key has handed to every replica it
// follows, the migration's link among them.
func ecWriteReplicated(t *testing.T, source *goredis.ClusterClient, key, value string) {
	t.Helper()
	ctx := t.Context()
	primary, err := source.MasterForKey(ctx, key)
	require.NoError(t, err)
	conn := primary.Conn()
	defer conn.Close()
	require.NoError(t, conn.Set(ctx, key, value, 0).Err())
	acknowledged, err := conn.Wait(ctx, 1, 0).Result()
	require.NoError(t, err)
	require.GreaterOrEqual(t, acknowledged, int64(1))
}

// TestElastiCache_ServerlessMigration migrates a cluster-mode Redis OSS
// replication group that requires its AUTH token into a serverless cache,
// reading the token from a Secrets Manager secret: the data sync loads every
// shard, keeps following the source's writes, stops, restarts, and cuts over.
func TestElastiCache_ServerlessMigration(t *testing.T) {
	t.Parallel()
	c := ecClient()
	ctx := t.Context()

	const token = "migrationtoken0123456789"
	sourceID := uniquename.New("sl-mig-source")
	_, err := c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId: aws.String(sourceID), ReplicationGroupDescription: aws.String("migration source"),
		Engine: aws.String("redis"), CacheNodeType: aws.String("cache.t3.micro"),
		ClusterMode: ectypes.ClusterModeEnabled, NumNodeGroups: aws.Int32(2), ReplicasPerNodeGroup: aws.Int32(0),
		TransitEncryptionEnabled: aws.Bool(true), TransitEncryptionMode: ectypes.TransitEncryptionModePreferred,
		AuthToken: aws.String(token),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(sourceID)})
	})
	name := uniquename.New("sl-migrating")
	created, err := c.CreateServerlessCache(ctx, &elasticache.CreateServerlessCacheInput{
		ServerlessCacheName: aws.String(name), Engine: aws.String("valkey"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCache(ctx, &elasticache.DeleteServerlessCacheInput{ServerlessCacheName: aws.String(name)})
	})
	assert.Nil(t, created.ServerlessCache.Migration)
	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName:      aws.String(name),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{{Address: aws.String("127.0.0.1"), Port: aws.Int32(6379)}},
	})
	assertAWSAPIErrorCode(t, err, "InvalidServerlessCacheStateFault")

	key, err := kmsClient().CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("migration secret key")})
	require.NoError(t, err)
	sm := smClient()
	secretString, err := json.Marshal(map[string]string{"password": token})
	require.NoError(t, err)
	secret, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(uniquename.New("sl-migration-source")), KmsKeyId: key.KeyMetadata.KeyId, SecretString: aws.String(string(secretString)),
	})
	require.NoError(t, err)
	awsManaged, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(uniquename.New("sl-migration-managed")), SecretString: aws.String(string(secretString)),
	})
	require.NoError(t, err)

	group := ecAwaitReplGroup(t, c, sourceID)
	source := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(group.ConfigurationEndpoint)}, Password: token})
	t.Cleanup(func() { source.Close() })
	for i := 0; i < 25; i++ {
		require.NoError(t, source.Set(ctx, fmt.Sprintf("migrated-%d", i), i, 0).Err())
	}
	sourceEndpoint := []ectypes.CustomerNodeEndpoint{{Address: group.ConfigurationEndpoint.Address, Port: group.ConfigurationEndpoint.Port}}
	withSecret := func(arn *string) *ectypes.SourceAuthenticationConfiguration {
		return &ectypes.SourceAuthenticationConfiguration{SecretManagerConfig: &ectypes.SecretManagerAuthConfig{SecretArn: arn}}
	}

	cache := ecAwaitServerless(t, c, name)

	_, err = c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "ServerlessCacheNotUnderMigrationFault")
	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName: aws.String(uniquename.New("sl-no-such-cache")), CustomerNodeEndpointList: sourceEndpoint,
	})
	assertAWSAPIErrorCode(t, err, "ServerlessCacheNotFoundFault")
	for _, refused := range []*elasticache.PrepareMigrationInput{
		{ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: append(sourceEndpoint, sourceEndpoint...), SourceAuthenticationConfiguration: withSecret(secret.ARN)},
		{ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{{Address: aws.String("127.0.0.1"), Port: aws.Int32(ecUnansweredPort(t))}}},
		{ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint},
		{ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint, SourceAuthenticationConfiguration: withSecret(awsManaged.ARN)},
		{ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint, SourceAuthenticationConfiguration: withSecret(secret.ARN),
			SourceTlsConfiguration: &ectypes.SourceTlsConfiguration{SourceTlsMode: ectypes.TlsModeVerifyCa}},
	} {
		_, err = c.PrepareMigration(ctx, refused)
		assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	}

	prepare := &elasticache.PrepareMigrationInput{
		ServerlessCacheName:               aws.String(name),
		CustomerNodeEndpointList:          sourceEndpoint,
		SourceAuthenticationConfiguration: withSecret(secret.ARN),
		SourceTlsConfiguration:            &ectypes.SourceTlsConfiguration{SourceTlsMode: ectypes.TlsModeNone},
	}
	prepared, err := c.PrepareMigration(ctx, prepare)
	require.NoError(t, err)
	require.NotNil(t, prepared.ServerlessCache.Migration)
	assert.Equal(t, "ready-to-sync", aws.ToString(prepared.ServerlessCache.Migration.Status))
	assert.Equal(t, "manual", aws.ToString(prepared.ServerlessCache.Migration.CutoverMode))
	require.NotNil(t, prepared.ServerlessCache.Migration.MigrationEndpoint)
	assert.Equal(t, aws.ToString(cache.Endpoint.Address), aws.ToString(prepared.ServerlessCache.Migration.MigrationEndpoint.Address),
		"the migration endpoint is the cache's standard endpoint")
	_, err = c.PrepareMigration(ctx, prepare)
	assertAWSAPIErrorCode(t, err, "ServerlessCacheAlreadyUnderMigrationFault")
	_, err = c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidServerlessCacheStateFault")
	_, err = c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")

	target := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(cache.Endpoint)}, TLSConfig: ecClientTLS()})
	t.Cleanup(func() { target.Close() })
	started, err := c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "initiating-sync", aws.ToString(started.ServerlessCache.Migration.Status))
	ecAwaitMigration(t, c, name, "syncing")
	ecRequireKeys(t, target, "migrated", 25)
	ecWriteReplicated(t, source, "written-while-syncing", "followed")
	value, err := target.Get(ctx, "written-while-syncing").Result()
	require.NoError(t, err)
	assert.Equal(t, "followed", value, "the data sync keeps applying the source's writes")

	stopped, err := c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "stopping", aws.ToString(stopped.ServerlessCache.Migration.Status))
	ecAwaitMigration(t, c, name, "ready-to-sync")
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	ecAwaitMigration(t, c, name, "ready-to-sync")
	assert.Equal(t, "followed", target.Get(ctx, "written-while-syncing").Val(), "a start that found the cache holding data left it as it was")

	require.NoError(t, target.Set(ctx, "written-to-the-target", "only", 0).Err())
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name), ClearTargetCacheData: aws.Bool(true)})
	require.NoError(t, err)
	ecAwaitMigration(t, c, name, "syncing")
	ecRequireKeys(t, target, "migrated", 25)
	assert.Equal(t, goredis.Nil, target.Get(ctx, "written-to-the-target").Err(), "ClearTargetCacheData emptied the cache first")

	completed, err := c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "cutting-over", aws.ToString(completed.ServerlessCache.Migration.Status))
	ecAwaitMigration(t, c, name, "")
	require.NoError(t, source.Set(ctx, "written-after-cutover", "source only", 0).Err())
	assert.Equal(t, goredis.Nil, target.Get(ctx, "written-after-cutover").Err(), "the cutover ended the data sync")
	ecRequireKeys(t, target, "migrated", 25)
}

// TestElastiCache_ReplicationGroupMigration migrates a Redis OSS cache
// cluster's data into a replication group online: the group's primary
// replicates from the source until CompleteMigration promotes it.
func TestElastiCache_ReplicationGroupMigration(t *testing.T) {
	t.Parallel()
	c := ecClient()
	ctx := t.Context()

	sourceID := uniquename.New("rg-mig-source")
	_, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(sourceID), Engine: aws.String("redis"),
		CacheNodeType: aws.String("cache.t3.micro"), NumCacheNodes: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(sourceID)})
	})
	groupID := uniquename.New("rg-mig-target")
	_, err = c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId: aws.String(groupID), ReplicationGroupDescription: aws.String("migration target"),
		Engine: aws.String("redis"), CacheNodeType: aws.String("cache.t3.micro"), NumCacheClusters: aws.Int32(2),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(groupID)})
	})
	sourceNode := ecAwaitCluster(t, c, sourceID).CacheNodes[0].Endpoint
	source := redisClient(t, &goredis.Options{Addr: ecAddress(sourceNode)})
	for i := 0; i < 20; i++ {
		require.NoError(t, source.Set(ctx, fmt.Sprintf("online-%d", i), i, 0).Err())
	}
	group := ecAwaitReplGroup(t, c, groupID)
	primary := redisClient(t, &goredis.Options{Addr: ecAddress(group.NodeGroups[0].PrimaryEndpoint)})
	require.NoError(t, primary.Set(ctx, "target-before-migration", "replaced", 0).Err())

	endpoint := []ectypes.CustomerNodeEndpoint{{Address: sourceNode.Address, Port: sourceNode.Port}}
	_, err = c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ReplicationGroupId: aws.String(groupID)})
	assertAWSAPIErrorCode(t, err, "ReplicationGroupNotUnderMigrationFault")
	_, err = c.TestMigration(ctx, &elasticache.TestMigrationInput{
		ReplicationGroupId:       aws.String(groupID),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{{Address: aws.String("127.0.0.1"), Port: aws.Int32(ecUnansweredPort(t))}},
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{
		ReplicationGroupId: aws.String(groupID), CustomerNodeEndpointList: append(endpoint, endpoint...),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	tested, err := c.TestMigration(ctx, &elasticache.TestMigrationInput{ReplicationGroupId: aws.String(groupID), CustomerNodeEndpointList: endpoint})
	require.NoError(t, err)
	assert.Equal(t, "available", aws.ToString(tested.ReplicationGroup.Status), "TestMigration migrates no data")

	started, err := c.StartMigration(ctx, &elasticache.StartMigrationInput{ReplicationGroupId: aws.String(groupID), CustomerNodeEndpointList: endpoint})
	require.NoError(t, err)
	assert.Equal(t, "migrating", aws.ToString(started.ReplicationGroup.Status))
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ReplicationGroupId: aws.String(groupID), CustomerNodeEndpointList: endpoint})
	assertAWSAPIErrorCode(t, err, "ReplicationGroupAlreadyUnderMigrationFault")

	conn := source.Conn()
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.Set(ctx, "written-while-migrating", "followed", 0).Err())
	acknowledged, err := conn.Wait(ctx, 1, 0).Result()
	require.NoError(t, err)
	require.GreaterOrEqual(t, acknowledged, int64(1), "the group's primary replicates from the source")
	ecRequireKeys(t, primary, "online", 20)
	assert.Equal(t, "followed", primary.Get(ctx, "written-while-migrating").Val())
	assert.Equal(t, goredis.Nil, primary.Get(ctx, "target-before-migration").Err(), "the migration replaced the group's data with the source's")
	assert.ErrorContains(t, primary.Set(ctx, "written-to-the-target", "refused", 0).Err(), "READONLY", "the group takes no writes while it migrates")

	completed, err := c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ReplicationGroupId: aws.String(groupID)})
	require.NoError(t, err)
	assert.Equal(t, "migrating", aws.ToString(completed.ReplicationGroup.Status))
	ecAwaitReplGroup(t, c, groupID)
	require.NoError(t, primary.Set(ctx, "written-after-migration", "taken", 0).Err())
	require.NoError(t, source.Set(ctx, "written-to-the-source-after", "not followed", 0).Err())
	assert.Equal(t, goredis.Nil, primary.Get(ctx, "written-to-the-source-after").Err(), "the promoted primary no longer follows the source")
	ecRequireKeys(t, primary, "online", 20)
}
