package aws_cli_test

import (
	"fmt"
	"strconv"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliServerlessCacheMigration struct {
	ServerlessCache struct {
		Endpoint struct {
			Address string `json:"Address"`
		} `json:"Endpoint"`
		Migration *struct {
			MigrationEndpoint struct {
				Address string `json:"Address"`
			} `json:"MigrationEndpoint"`
		} `json:"Migration"`
	} `json:"ServerlessCache"`
}

// TestElastiCacheCLI_ServerlessMigration migrates a Redis OSS cache
// cluster's data into a serverless cache through prepare-migration,
// start-migration, stop-migration and complete-migration.
func TestElastiCacheCLI_ServerlessMigration(t *testing.T) {
	t.Parallel()
	sourceID := "cli-sl-migration-source"
	runCLI(t, awsCLI("elasticache", "create-cache-cluster",
		"--cache-cluster-id", sourceID,
		"--cache-node-type", "cache.t3.micro",
		"--engine", "redis",
		"--num-cache-nodes", "1"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-cluster", "--cache-cluster-id", sourceID).Run()
	})
	name := "cli-sl-migrating"
	runCLI(t, awsCLI("elasticache", "create-serverless-cache",
		"--serverless-cache-name", name,
		"--engine", "valkey"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-serverless-cache", "--serverless-cache-name", name).Run()
	})
	sourceNode := ecCLIAwaitCluster(t, sourceID).CacheNodes[0].Endpoint
	source := ecCLIRedis(t, &goredis.Options{Addr: sourceNode.address()})
	for i := 0; i < 10; i++ {
		require.NoError(t, source.Set(t.Context(), fmt.Sprintf("cli-migrated-%d", i), i, 0).Err())
	}
	cache := ecCLIAwaitServerless(t, name)
	sourceEndpoint := fmt.Sprintf("Address=%s,Port=%d", sourceNode.Address, sourceNode.Port)

	notUnder := runCLIExpectError(t, awsCLI("elasticache", "stop-migration", "--serverless-cache-name", name))
	assert.Contains(t, notUnder, "ServerlessCacheNotUnderMigrationFault")

	out := runCLI(t, awsCLI("elasticache", "prepare-migration",
		"--serverless-cache-name", name,
		"--customer-node-endpoint-list", sourceEndpoint,
		"--source-tls-configuration", "SourceTlsMode=none"))
	var prepared cliServerlessCacheMigration
	parseJSON(t, out, &prepared)
	require.NotNil(t, prepared.ServerlessCache.Migration)
	assert.Equal(t, prepared.ServerlessCache.Endpoint.Address, prepared.ServerlessCache.Migration.MigrationEndpoint.Address)
	already := runCLIExpectError(t, awsCLI("elasticache", "prepare-migration",
		"--serverless-cache-name", name,
		"--customer-node-endpoint-list", sourceEndpoint))
	assert.Contains(t, already, "ServerlessCacheAlreadyUnderMigrationFault")

	runCLI(t, awsCLI("elasticache", "start-migration", "--serverless-cache-name", name))
	target := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{cache.Endpoint.address()}, TLSConfig: ecCLIClientTLS()})
	t.Cleanup(func() { target.Close() })
	for i := 0; i < 10; i++ {
		value, err := target.Get(t.Context(), fmt.Sprintf("cli-migrated-%d", i)).Result()
		require.NoError(t, err)
		assert.Equal(t, strconv.Itoa(i), value)
	}
	out = runCLI(t, awsCLI("elasticache", "stop-migration", "--serverless-cache-name", name))
	var stopped cliServerlessCacheMigration
	parseJSON(t, out, &stopped)
	assert.NotNil(t, stopped.ServerlessCache.Migration, "a stopped data sync leaves the migration open")
	notSyncing := runCLIExpectError(t, awsCLI("elasticache", "stop-migration", "--serverless-cache-name", name))
	assert.Contains(t, notSyncing, "InvalidServerlessCacheStateFault")

	runCLI(t, awsCLI("elasticache", "start-migration", "--serverless-cache-name", name, "--clear-target-cache-data"))
	out = runCLI(t, awsCLI("elasticache", "complete-migration", "--serverless-cache-name", name))
	var completed cliServerlessCacheMigration
	parseJSON(t, out, &completed)
	assert.Nil(t, completed.ServerlessCache.Migration)
}
