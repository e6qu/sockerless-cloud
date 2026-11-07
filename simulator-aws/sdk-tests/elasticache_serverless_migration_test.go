package aws_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
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
	acknowledged, err := conn.Wait(ctx, 1, 60_000).Result()
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

	identity, err := stsClient().GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	keyPolicy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"kms:*","Resource":"*"},
		{"Effect":"Allow","Principal":{"Service":"elasticache.amazonaws.com"},"Action":"kms:Decrypt","Resource":"*"}]}`, aws.ToString(identity.Account))
	key, err := kmsClient().CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("migration secret key"), Policy: aws.String(keyPolicy)})
	require.NoError(t, err)
	sm := smClient()
	secretString, err := json.Marshal(map[string]string{"password": token})
	require.NoError(t, err)
	grantElastiCache := func(arn *string) {
		_, err := sm.PutResourcePolicy(ctx, &secretsmanager.PutResourcePolicyInput{SecretId: arn, ResourcePolicy: aws.String(fmt.Sprintf(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"elasticache.amazonaws.com"},"Action":"secretsmanager:GetSecretValue","Resource":"%s"}]}`,
			aws.ToString(arn)))})
		require.NoError(t, err)
	}
	secret, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(uniquename.New("sl-migration-source")), KmsKeyId: key.KeyMetadata.KeyId, SecretString: aws.String(string(secretString)),
	})
	require.NoError(t, err)
	ungranted, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(uniquename.New("sl-migration-ungranted")), KmsKeyId: key.KeyMetadata.KeyId, SecretString: aws.String(string(secretString)),
	})
	require.NoError(t, err)
	awsManaged, err := sm.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(uniquename.New("sl-migration-managed")), SecretString: aws.String(string(secretString)),
	})
	require.NoError(t, err)
	grantElastiCache(awsManaged.ARN)

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
		{ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint, SourceAuthenticationConfiguration: withSecret(secret.ARN),
			SourceTlsConfiguration: &ectypes.SourceTlsConfiguration{SourceTlsMode: ectypes.TlsModeVerifyCa}},
	} {
		_, err = c.PrepareMigration(ctx, refused)
		assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	}
	// The prepare step validates the source and the credentials after it
	// answers: a failure logs an event on the cache and removes the migration.
	for reason, failing := range map[string]*elasticache.PrepareMigrationInput{
		"Unable to connect to the source": {ServerlessCacheName: aws.String(name),
			CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{{Address: aws.String("127.0.0.1"), Port: aws.Int32(ecUnansweredPort(t))}}},
		"Unable to connect":    {ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint},
		"AWS managed key":      {ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint, SourceAuthenticationConfiguration: withSecret(awsManaged.ARN)},
		"Access to the secret": {ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint, SourceAuthenticationConfiguration: withSecret(ungranted.ARN)},
	} {
		started, err := c.PrepareMigration(ctx, failing)
		require.NoError(t, err)
		assert.Equal(t, "preparing", aws.ToString(started.ServerlessCache.Migration.Status))
		ecAwaitMigration(t, c, name, "")
		assert.True(t, ecHasEvent(t, c, ectypes.SourceTypeServerlessCache, name, "Migration preparation failed: ", reason), "a failed prepare logs an event naming %q", reason)
	}
	grantElastiCache(secret.ARN)

	prepare := &elasticache.PrepareMigrationInput{
		ServerlessCacheName:               aws.String(name),
		CustomerNodeEndpointList:          sourceEndpoint,
		SourceAuthenticationConfiguration: withSecret(secret.ARN),
		SourceTlsConfiguration:            &ectypes.SourceTlsConfiguration{SourceTlsMode: ectypes.TlsModeNone},
	}
	prepared, err := c.PrepareMigration(ctx, prepare)
	require.NoError(t, err)
	require.NotNil(t, prepared.ServerlessCache.Migration)
	assert.Equal(t, "preparing", aws.ToString(prepared.ServerlessCache.Migration.Status))
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidServerlessCacheStateFault")
	ready := ecAwaitMigration(t, c, name, "ready-to-sync")
	assert.Equal(t, "manual", aws.ToString(ready.Migration.CutoverMode))
	require.NotNil(t, ready.Migration.MigrationEndpoint)
	assert.Equal(t, aws.ToString(cache.Endpoint.Address), aws.ToString(ready.Migration.MigrationEndpoint.Address),
		"the migration endpoint is the cache's standard endpoint")
	assert.True(t, ecHasEvent(t, c, ectypes.SourceTypeServerlessCache, name, "Migration prepared"))
	_, err = c.PrepareMigration(ctx, prepare)
	assertAWSAPIErrorCode(t, err, "ServerlessCacheAlreadyUnderMigrationFault")
	_, err = c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidServerlessCacheStateFault")
	_, err = c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")

	// Until cutover the migration endpoint serves from the source: the
	// application's own credentials go to the source.
	endpoint := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(cache.Endpoint)}, TLSConfig: ecClientTLS(), Password: token})
	t.Cleanup(func() { endpoint.Close() })
	require.NoError(t, endpoint.Set(ctx, "written-through-the-endpoint", "to the source", 0).Err())
	assert.Equal(t, "to the source", source.Get(ctx, "written-through-the-endpoint").Val(), "the migration endpoint routes to the source before cutover")
	ecRequireKeys(t, endpoint, "migrated", 25)

	started, err := c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "initiating-sync", aws.ToString(started.ServerlessCache.Migration.Status))
	ecAwaitMigration(t, c, name, "syncing")
	ecWriteReplicated(t, source, "written-while-syncing", "followed")

	stopped, err := c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "stopping", aws.ToString(stopped.ServerlessCache.Migration.Status))
	ecAwaitMigration(t, c, name, "ready-to-sync")
	require.NoError(t, source.Del(ctx, "written-while-syncing").Err())
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	ecAwaitMigration(t, c, name, "ready-to-sync")
	assert.True(t, ecHasEvent(t, c, ectypes.SourceTypeServerlessCache, name, "Migration data sync failed to start: "),
		"a start that found the cache holding data logs why it did not start")

	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name), ClearTargetCacheData: aws.Bool(true)})
	require.NoError(t, err)
	ecAwaitMigration(t, c, name, "syncing")
	completed, err := c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "cutting-over", aws.ToString(completed.ServerlessCache.Migration.Status))
	ecAwaitMigration(t, c, name, "")
	for _, message := range []string{"Migration data sync started", "Migration data sync stopped", "Migration cutover started", "Migration completed"} {
		assert.True(t, ecHasEvent(t, c, ectypes.SourceTypeServerlessCache, name, message), "the migration logs %q", message)
	}

	target := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(cache.Endpoint)}, TLSConfig: ecClientTLS()})
	t.Cleanup(func() { target.Close() })
	require.NoError(t, source.Set(ctx, "written-after-cutover", "source only", 0).Err())
	assert.Equal(t, goredis.Nil, target.Get(ctx, "written-after-cutover").Err(), "after cutover the endpoint serves the cache")
	ecRequireKeys(t, target, "migrated", 25)
	assert.Equal(t, "to the source", target.Get(ctx, "written-through-the-endpoint").Val(), "a write through the endpoint before cutover reached the cache through the sync")
	assert.Equal(t, goredis.Nil, target.Get(ctx, "written-while-syncing").Err(), "ClearTargetCacheData emptied the cache before the second sync")
}

// ecHasEvent reports whether DescribeEvents lists an event about the source
// whose message holds every part.
func ecHasEvent(t *testing.T, c *elasticache.Client, sourceType ectypes.SourceType, id string, parts ...string) bool {
	t.Helper()
	out, err := c.DescribeEvents(t.Context(), &elasticache.DescribeEventsInput{SourceType: sourceType, SourceIdentifier: aws.String(id), Duration: aws.Int32(60)})
	require.NoError(t, err)
	for _, event := range out.Events {
		message := aws.ToString(event.Message)
		matched := true
		for _, part := range parts {
			matched = matched && strings.Contains(message, part)
		}
		if matched {
			return true
		}
	}
	return false
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
	acknowledged, err := conn.Wait(ctx, 1, 60_000).Result()
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

// TestElastiCache_ClusterModeReplicationGroupMigration migrates a
// cluster-mode Redis OSS replication group into another online: the target
// takes the source's slot layout, follows each shard's writes and
// transactions, refuses writes of its own until CompleteMigration, and logs
// each step as an event.
func TestElastiCache_ClusterModeReplicationGroupMigration(t *testing.T) {
	t.Parallel()
	c := ecClient()
	ctx := t.Context()

	create := func(prefix string) string {
		id := uniquename.New(prefix)
		_, err := c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
			ReplicationGroupId: aws.String(id), ReplicationGroupDescription: aws.String(prefix),
			Engine: aws.String("redis"), CacheNodeType: aws.String("cache.t3.micro"),
			ClusterMode: ectypes.ClusterModeEnabled, NumNodeGroups: aws.Int32(2), ReplicasPerNodeGroup: aws.Int32(0),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeleteReplicationGroup(context.Background(), &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
		})
		return id
	}
	sourceID, targetID := create("cm-mig-source"), create("cm-mig-target")
	sourceGroup := ecAwaitReplGroup(t, c, sourceID)
	targetGroup := ecAwaitReplGroup(t, c, targetID)
	source := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(sourceGroup.ConfigurationEndpoint)}})
	t.Cleanup(func() { source.Close() })
	target := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(targetGroup.ConfigurationEndpoint)}})
	t.Cleanup(func() { target.Close() })
	for i := 0; i < 20; i++ {
		require.NoError(t, source.Set(ctx, fmt.Sprintf("cluster-online-%d", i), i, 0).Err())
	}
	require.NoError(t, target.Set(ctx, "target-before-migration", "replaced", 0).Err())

	endpoint := []ectypes.CustomerNodeEndpoint{{Address: sourceGroup.ConfigurationEndpoint.Address, Port: sourceGroup.ConfigurationEndpoint.Port}}
	started, err := c.StartMigration(ctx, &elasticache.StartMigrationInput{ReplicationGroupId: aws.String(targetID), CustomerNodeEndpointList: endpoint})
	require.NoError(t, err)
	assert.Equal(t, "migrating", aws.ToString(started.ReplicationGroup.Status))
	deadline := time.Now().Add(ecEngineWait)
	for !ecHasEvent(t, c, ectypes.SourceTypeReplicationGroup, targetID, "Migration started") {
		require.False(t, ecHasEvent(t, c, ectypes.SourceTypeReplicationGroup, targetID, "Migration failed to start"), "the migration did not start")
		require.True(t, time.Now().Before(deadline), "the migration logged no start")
		time.Sleep(time.Second)
	}

	ecRequireKeys(t, target, "cluster-online", 20)
	assert.Equal(t, goredis.Nil, target.Get(ctx, "target-before-migration").Err(), "the migration replaced the group's data with the source's")
	ecWriteReplicated(t, source, "cluster-written-while-migrating", "followed")
	assert.Equal(t, "followed", target.Get(ctx, "cluster-written-while-migrating").Val())
	primary, err := source.MasterForKey(ctx, "tx{carried}")
	require.NoError(t, err)
	conn := primary.Conn()
	t.Cleanup(func() { conn.Close() })
	_, err = conn.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.IncrBy(ctx, "tx{carried}", 2)
		pipe.Set(ctx, "tx{carried}-marker", "together", 0)
		return nil
	})
	require.NoError(t, err)
	acknowledged, err := conn.Wait(ctx, 1, 60_000).Result()
	require.NoError(t, err)
	require.GreaterOrEqual(t, acknowledged, int64(1))
	assert.Equal(t, "2", target.Get(ctx, "tx{carried}").Val(), "a transaction on the source runs on the target")
	assert.Equal(t, "together", target.Get(ctx, "tx{carried}-marker").Val())
	assert.ErrorContains(t, target.Set(ctx, "written-to-the-target", "refused", 0).Err(), "READONLY", "the group takes no writes while it migrates")

	_, err = c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ReplicationGroupId: aws.String(targetID)})
	require.NoError(t, err)
	ecAwaitReplGroup(t, c, targetID)
	assert.True(t, ecHasEvent(t, c, ectypes.SourceTypeReplicationGroup, targetID, "Migration completed"))
	require.NoError(t, target.Set(ctx, "written-after-migration", "taken", 0).Err())
	require.NoError(t, source.Set(ctx, "cluster-written-to-the-source-after", "not followed", 0).Err())
	assert.Equal(t, goredis.Nil, target.Get(ctx, "cluster-written-to-the-source-after").Err(), "the group no longer follows the source")
	ecRequireKeys(t, target, "cluster-online", 20)
}
