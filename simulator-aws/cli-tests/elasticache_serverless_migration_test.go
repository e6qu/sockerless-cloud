package aws_cli_test

import (
	"fmt"
	"strconv"
	"testing"
	"time"

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
			Status            string `json:"Status"`
			CutoverMode       string `json:"CutoverMode"`
			MigrationEndpoint struct {
				Address string `json:"Address"`
			} `json:"MigrationEndpoint"`
		} `json:"Migration"`
	} `json:"ServerlessCache"`
}

// ecCLIAwaitMigration polls describe-serverless-caches until the cache's
// Migration.Status reads want, or until the cache has no migration when want
// is empty.
func ecCLIAwaitMigration(t *testing.T, name, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var described struct {
			ServerlessCaches []struct {
				Migration *struct {
					Status string `json:"Status"`
				} `json:"Migration"`
			} `json:"ServerlessCaches"`
		}
		parseJSON(t, runCLI(t, awsCLI("elasticache", "describe-serverless-caches", "--serverless-cache-name", name)), &described)
		require.Len(t, described.ServerlessCaches, 1)
		status := ""
		if migration := described.ServerlessCaches[0].Migration; migration != nil {
			status = migration.Status
		}
		if status == want {
			return
		}
		require.True(t, time.Now().Before(deadline), "the migration of %s reads %q, not %q", name, status, want)
		time.Sleep(time.Second)
	}
}

// TestElastiCacheCLI_ServerlessMigration migrates a cluster-mode Redis OSS
// replication group into a serverless cache through prepare-migration,
// start-migration with --auto-cutover, stop-migration and complete-migration.
func TestElastiCacheCLI_ServerlessMigration(t *testing.T) {
	t.Parallel()
	sourceID := "cli-sl-migration-source"
	runCLI(t, awsCLI("elasticache", "create-replication-group",
		"--replication-group-id", sourceID,
		"--replication-group-description", "migration source",
		"--engine", "redis",
		"--cache-node-type", "cache.t3.micro",
		"--cluster-mode", "enabled",
		"--num-node-groups", "1",
		"--replicas-per-node-group", "0"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-replication-group", "--replication-group-id", sourceID).Run()
	})
	name := "cli-sl-migrating"
	runCLI(t, awsCLI("elasticache", "create-serverless-cache",
		"--serverless-cache-name", name,
		"--engine", "valkey"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-serverless-cache", "--serverless-cache-name", name).Run()
	})
	group := ecCLIAwaitReplGroup(t, sourceID)
	source := goredis.NewClusterClient(&goredis.ClusterOptions{Addrs: []string{group.ConfigurationEndpoint.address()}})
	t.Cleanup(func() { source.Close() })
	for i := 0; i < 10; i++ {
		require.NoError(t, source.Set(t.Context(), fmt.Sprintf("cli-migrated-%d", i), i, 0).Err())
	}
	cache := ecCLIAwaitServerless(t, name)
	sourceEndpoint := fmt.Sprintf("Address=%s,Port=%d", group.ConfigurationEndpoint.Address, group.ConfigurationEndpoint.Port)

	notUnder := runCLIExpectError(t, awsCLI("elasticache", "stop-migration", "--serverless-cache-name", name))
	assert.Contains(t, notUnder, "ServerlessCacheNotUnderMigrationFault")

	out := runCLI(t, awsCLI("elasticache", "prepare-migration",
		"--serverless-cache-name", name,
		"--customer-node-endpoint-list", sourceEndpoint,
		"--source-tls-configuration", "SourceTlsMode=none"))
	var prepared cliServerlessCacheMigration
	parseJSON(t, out, &prepared)
	require.NotNil(t, prepared.ServerlessCache.Migration)
	assert.Equal(t, "ready-to-sync", prepared.ServerlessCache.Migration.Status)
	assert.Equal(t, prepared.ServerlessCache.Endpoint.Address, prepared.ServerlessCache.Migration.MigrationEndpoint.Address)
	already := runCLIExpectError(t, awsCLI("elasticache", "prepare-migration",
		"--serverless-cache-name", name,
		"--customer-node-endpoint-list", sourceEndpoint))
	assert.Contains(t, already, "ServerlessCacheAlreadyUnderMigrationFault")

	runCLI(t, awsCLI("elasticache", "start-migration", "--serverless-cache-name", name))
	ecCLIAwaitMigration(t, name, "syncing")
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
	require.NotNil(t, stopped.ServerlessCache.Migration, "a stopped data sync leaves the migration open")
	assert.Equal(t, "stopping", stopped.ServerlessCache.Migration.Status)
	ecCLIAwaitMigration(t, name, "ready-to-sync")
	notSyncing := runCLIExpectError(t, awsCLI("elasticache", "stop-migration", "--serverless-cache-name", name))
	assert.Contains(t, notSyncing, "InvalidServerlessCacheStateFault")

	out = runCLI(t, awsCLI("elasticache", "start-migration", "--serverless-cache-name", name,
		"--clear-target-cache-data", "--auto-cutover"))
	var started cliServerlessCacheMigration
	parseJSON(t, out, &started)
	require.NotNil(t, started.ServerlessCache.Migration)
	assert.Equal(t, "auto", started.ServerlessCache.Migration.CutoverMode)
	ecCLIAwaitMigration(t, name, "")
	value, err := target.Get(t.Context(), "cli-migrated-3").Result()
	require.NoError(t, err)
	assert.Equal(t, "3", value, "the automatic cutover left the migrated data in the cache")

	notUnder = runCLIExpectError(t, awsCLI("elasticache", "complete-migration", "--serverless-cache-name", name))
	assert.Contains(t, notUnder, "ServerlessCacheNotUnderMigrationFault")
}

// TestElastiCacheCLI_ReplicationGroupMigration migrates a Redis OSS cache
// cluster into a replication group through test-migration, start-migration
// and complete-migration.
func TestElastiCacheCLI_ReplicationGroupMigration(t *testing.T) {
	t.Parallel()
	sourceID := "cli-rg-migration-source"
	runCLI(t, awsCLI("elasticache", "create-cache-cluster",
		"--cache-cluster-id", sourceID,
		"--cache-node-type", "cache.t3.micro",
		"--engine", "redis",
		"--num-cache-nodes", "1"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-cluster", "--cache-cluster-id", sourceID).Run()
	})
	groupID := "cli-rg-migration-target"
	runCLI(t, awsCLI("elasticache", "create-replication-group",
		"--replication-group-id", groupID,
		"--replication-group-description", "migration target",
		"--engine", "redis",
		"--cache-node-type", "cache.t3.micro",
		"--num-cache-clusters", "1"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-replication-group", "--replication-group-id", groupID).Run()
	})
	sourceNode := ecCLIAwaitCluster(t, sourceID).CacheNodes[0].Endpoint
	source := ecCLIRedis(t, &goredis.Options{Addr: sourceNode.address()})
	for i := 0; i < 10; i++ {
		require.NoError(t, source.Set(t.Context(), fmt.Sprintf("cli-online-%d", i), i, 0).Err())
	}
	group := ecCLIAwaitReplGroup(t, groupID)
	endpoint := fmt.Sprintf("Address=%s,Port=%d", sourceNode.Address, sourceNode.Port)

	var replied struct {
		ReplicationGroup struct {
			Status string `json:"Status"`
		} `json:"ReplicationGroup"`
	}
	parseJSON(t, runCLI(t, awsCLI("elasticache", "test-migration",
		"--replication-group-id", groupID, "--customer-node-endpoint-list", endpoint)), &replied)
	assert.Equal(t, "available", replied.ReplicationGroup.Status)
	parseJSON(t, runCLI(t, awsCLI("elasticache", "start-migration",
		"--replication-group-id", groupID, "--customer-node-endpoint-list", endpoint)), &replied)
	assert.Equal(t, "migrating", replied.ReplicationGroup.Status)

	conn := source.Conn()
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.Set(t.Context(), "cli-written-while-migrating", "followed", 0).Err())
	acknowledged, err := conn.Wait(t.Context(), 1, 0).Result()
	require.NoError(t, err)
	require.GreaterOrEqual(t, acknowledged, int64(1))
	primary := ecCLIRedis(t, &goredis.Options{Addr: group.NodeGroups[0].PrimaryEndpoint.address()})
	assert.Equal(t, "followed", primary.Get(t.Context(), "cli-written-while-migrating").Val())
	assert.Equal(t, "4", primary.Get(t.Context(), "cli-online-4").Val())

	runCLI(t, awsCLI("elasticache", "complete-migration", "--replication-group-id", groupID))
	ecCLIAwaitReplGroup(t, groupID)
	require.NoError(t, primary.Set(t.Context(), "cli-written-after-migration", "taken", 0).Err())
	notUnder := runCLIExpectError(t, awsCLI("elasticache", "complete-migration", "--replication-group-id", groupID))
	assert.Contains(t, notUnder, "ReplicationGroupNotUnderMigrationFault")
}
