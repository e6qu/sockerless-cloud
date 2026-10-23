package aws_cli_test

import (
	"testing"

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

// TestElastiCacheCLI_ServerlessMigration drives a serverless cache
// migration through prepare-migration, start-migration, stop-migration and
// complete-migration.
func TestElastiCacheCLI_ServerlessMigration(t *testing.T) {
	t.Parallel()
	name := "cli-sl-migrating"
	runCLI(t, awsCLI("elasticache", "create-serverless-cache",
		"--serverless-cache-name", name,
		"--engine", "valkey"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-serverless-cache", "--serverless-cache-name", name).Run()
	})

	notUnder := runCLIExpectError(t, awsCLI("elasticache", "stop-migration", "--serverless-cache-name", name))
	assert.Contains(t, notUnder, "ServerlessCacheNotUnderMigrationFault")

	out := runCLI(t, awsCLI("elasticache", "prepare-migration",
		"--serverless-cache-name", name,
		"--customer-node-endpoint-list", "Address=10.0.0.5,Port=6379",
		"--source-tls-configuration", "SourceTlsMode=none"))
	var prepared cliServerlessCacheMigration
	parseJSON(t, out, &prepared)
	require.NotNil(t, prepared.ServerlessCache.Migration)
	assert.Equal(t, prepared.ServerlessCache.Endpoint.Address, prepared.ServerlessCache.Migration.MigrationEndpoint.Address)
	already := runCLIExpectError(t, awsCLI("elasticache", "prepare-migration",
		"--serverless-cache-name", name,
		"--customer-node-endpoint-list", "Address=10.0.0.5,Port=6379"))
	assert.Contains(t, already, "ServerlessCacheAlreadyUnderMigrationFault")

	runCLI(t, awsCLI("elasticache", "start-migration", "--serverless-cache-name", name))
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
