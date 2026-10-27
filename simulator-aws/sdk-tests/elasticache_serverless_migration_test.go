package aws_sdk_test

import (
	"fmt"
	"net"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCache_ServerlessMigration migrates a Redis OSS cache cluster's data
// into a serverless cache, with the error each step answers out of turn.
func TestElastiCache_ServerlessMigration(t *testing.T) {
	t.Parallel()
	c := ecClient()
	ctx := t.Context()

	sourceID := "sl-migration-source"
	_, err := c.CreateCacheCluster(ctx, &elasticache.CreateCacheClusterInput{
		CacheClusterId: aws.String(sourceID),
		CacheNodeType:  aws.String("cache.t3.micro"),
		Engine:         aws.String("redis"),
		NumCacheNodes:  aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheCluster(ctx, &elasticache.DeleteCacheClusterInput{CacheClusterId: aws.String(sourceID)})
	})
	name := "sl-migrating-cache"
	created, err := c.CreateServerlessCache(ctx, &elasticache.CreateServerlessCacheInput{
		ServerlessCacheName: aws.String(name),
		Engine:              aws.String("valkey"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteServerlessCache(ctx, &elasticache.DeleteServerlessCacheInput{ServerlessCacheName: aws.String(name)})
	})
	assert.Nil(t, created.ServerlessCache.Migration)

	sourceNode := ecAwaitCluster(t, c, sourceID).CacheNodes[0].Endpoint
	source := redisClient(t, &goredis.Options{Addr: ecAddress(sourceNode)})
	for i := 0; i < 25; i++ {
		require.NoError(t, source.Set(ctx, fmt.Sprintf("migrated-%d", i), i, 0).Err())
	}
	sourceEndpoint := []ectypes.CustomerNodeEndpoint{{Address: sourceNode.Address, Port: sourceNode.Port}}

	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName: aws.String(name), CustomerNodeEndpointList: sourceEndpoint,
	})
	assertAWSAPIErrorCode(t, err, "InvalidServerlessCacheStateFault")
	cache := ecAwaitServerless(t, c, name)

	_, err = c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "ServerlessCacheNotUnderMigrationFault")
	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName: aws.String("sl-no-such-cache"), CustomerNodeEndpointList: sourceEndpoint,
	})
	assertAWSAPIErrorCode(t, err, "ServerlessCacheNotFoundFault")
	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName:      aws.String(name),
		CustomerNodeEndpointList: append(sourceEndpoint, ectypes.CustomerNodeEndpoint{Address: aws.String("127.0.0.1"), Port: aws.Int32(6379)}),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	unanswered, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := unanswered.Addr().(*net.TCPAddr).Port
	require.NoError(t, unanswered.Close())
	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName:      aws.String(name),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{{Address: aws.String("127.0.0.1"), Port: aws.Int32(int32(port))}},
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")

	prepare := &elasticache.PrepareMigrationInput{
		ServerlessCacheName:      aws.String(name),
		CustomerNodeEndpointList: sourceEndpoint,
		SourceTlsConfiguration:   &ectypes.SourceTlsConfiguration{SourceTlsMode: ectypes.TlsModeNone},
	}
	prepared, err := c.PrepareMigration(ctx, prepare)
	require.NoError(t, err)
	require.NotNil(t, prepared.ServerlessCache.Migration)
	require.NotNil(t, prepared.ServerlessCache.Migration.MigrationEndpoint)
	assert.Equal(t, aws.ToString(cache.Endpoint.Address), aws.ToString(prepared.ServerlessCache.Migration.MigrationEndpoint.Address),
		"the migration endpoint is the cache's standard endpoint")
	_, err = c.PrepareMigration(ctx, prepare)
	assertAWSAPIErrorCode(t, err, "ServerlessCacheAlreadyUnderMigrationFault")
	_, err = c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidServerlessCacheStateFault")

	target := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{ecAddress(cache.Endpoint)}, TLSConfig: ecClientTLS()})
	t.Cleanup(func() { target.Close() })
	requireMigrated := func() {
		t.Helper()
		for i := 0; i < 25; i++ {
			value, err := target.Get(ctx, fmt.Sprintf("migrated-%d", i)).Result()
			require.NoError(t, err)
			assert.Equal(t, strconv.Itoa(i), value)
		}
	}
	started, err := c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, started.ServerlessCache)
	assert.NotNil(t, started.ServerlessCache.Migration)
	requireMigrated()

	require.NoError(t, target.Set(ctx, "written-to-the-target", "only", 0).Err())
	stopped, err := c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.NotNil(t, stopped.ServerlessCache.Migration, "a stopped data sync leaves the migration open")
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "MigrationHealthFault")

	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name), ClearTargetCacheData: aws.Bool(true)})
	require.NoError(t, err)
	requireMigrated()
	assert.Equal(t, goredis.Nil, target.Get(ctx, "written-to-the-target").Err(), "ClearTargetCacheData emptied the cache first")
	completed, err := c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Nil(t, completed.ServerlessCache.Migration)

	desc, err := c.DescribeServerlessCaches(ctx, &elasticache.DescribeServerlessCachesInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	require.Len(t, desc.ServerlessCaches, 1)
	assert.Nil(t, desc.ServerlessCaches[0].Migration)
}
