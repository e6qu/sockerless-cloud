package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCache_ServerlessMigration drives a serverless cache migration
// through PrepareMigration, StartMigration, StopMigration and
// CompleteMigration, with the errors each answers out of turn.
func TestElastiCache_ServerlessMigration(t *testing.T) {
	t.Parallel()
	c := ecClient()
	ctx := t.Context()
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

	_, err = c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "ServerlessCacheNotUnderMigrationFault")
	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName:      aws.String("sl-no-such-cache"),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{{Address: aws.String("10.0.0.5"), Port: aws.Int32(6379)}},
	})
	assertAWSAPIErrorCode(t, err, "ServerlessCacheNotFoundFault")
	_, err = c.PrepareMigration(ctx, &elasticache.PrepareMigrationInput{
		ServerlessCacheName: aws.String(name),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{
			{Address: aws.String("10.0.0.5"), Port: aws.Int32(6379)},
			{Address: aws.String("10.0.0.6"), Port: aws.Int32(6379)},
		},
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")

	prepare := &elasticache.PrepareMigrationInput{
		ServerlessCacheName:      aws.String(name),
		CustomerNodeEndpointList: []ectypes.CustomerNodeEndpoint{{Address: aws.String("10.0.0.5"), Port: aws.Int32(6379)}},
		SourceTlsConfiguration:   &ectypes.SourceTlsConfiguration{SourceTlsMode: ectypes.TlsModeNone},
	}
	prepared, err := c.PrepareMigration(ctx, prepare)
	require.NoError(t, err)
	require.NotNil(t, prepared.ServerlessCache.Migration)
	require.NotNil(t, prepared.ServerlessCache.Migration.MigrationEndpoint)
	assert.Equal(t, aws.ToString(created.ServerlessCache.Endpoint.Address), aws.ToString(prepared.ServerlessCache.Migration.MigrationEndpoint.Address),
		"the migration endpoint is the cache's standard endpoint")
	_, err = c.PrepareMigration(ctx, prepare)
	assertAWSAPIErrorCode(t, err, "ServerlessCacheAlreadyUnderMigrationFault")
	_, err = c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidServerlessCacheStateFault")

	started, err := c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, started.ServerlessCache)
	assert.NotNil(t, started.ServerlessCache.Migration)

	stopped, err := c.StopMigration(ctx, &elasticache.StopMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.NotNil(t, stopped.ServerlessCache.Migration, "a stopped data sync leaves the migration open")
	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	assertAWSAPIErrorCode(t, err, "MigrationHealthFault")

	_, err = c.StartMigration(ctx, &elasticache.StartMigrationInput{ServerlessCacheName: aws.String(name), ClearTargetCacheData: aws.Bool(true)})
	require.NoError(t, err)
	completed, err := c.CompleteMigration(ctx, &elasticache.CompleteMigrationInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	assert.Nil(t, completed.ServerlessCache.Migration)

	desc, err := c.DescribeServerlessCaches(ctx, &elasticache.DescribeServerlessCachesInput{ServerlessCacheName: aws.String(name)})
	require.NoError(t, err)
	require.Len(t, desc.ServerlessCaches, 1)
	assert.Nil(t, desc.ServerlessCaches[0].Migration)
}
