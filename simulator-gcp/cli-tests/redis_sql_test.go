package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemorystoreRedisCLI_InstanceLifecycle(t *testing.T) {
	name := "cli-redis"

	runCLI(t, gcloudCLI("redis", "instances", "create", name,
		"--region", location,
		"--size", "1",
		"--tier", "basic",
		"--redis-version", "redis_7_0",
		"--labels", "env=cli",
		"--quiet",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("redis", "instances", "delete", name,
			"--region", location,
			"--quiet",
			"--format=json",
		).Run()
	})

	out := runCLI(t, gcloudCLI("redis", "instances", "describe", name,
		"--region", location,
		"--format=json",
	))
	var inst struct {
		Name         string            `json:"name"`
		State        string            `json:"state"`
		Host         string            `json:"host"`
		Port         int               `json:"port"`
		Labels       map[string]string `json:"labels"`
		MemorySizeGb int               `json:"memorySizeGb"`
	}
	parseJSON(t, out, &inst)
	require.Equal(t, "projects/test-project/locations/us-central1/instances/cli-redis", inst.Name)
	require.Equal(t, "READY", inst.State)
	require.NotEmpty(t, inst.Host)
	require.Equal(t, 6379, inst.Port)
	require.Equal(t, "cli", inst.Labels["env"])
	require.Equal(t, 1, inst.MemorySizeGb)
}

func TestCloudSQLCLI_InstanceDatabaseUserLifecycle(t *testing.T) {
	instance := "cli-sql"

	runCLI(t, gcloudCLI("sql", "instances", "create", instance,
		"--database-version", "POSTGRES_15",
		"--region", location,
		"--tier", "db-f1-micro",
		"--root-password", "Str0ng-password-12345!",
		"--quiet",
		"--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("sql", "instances", "delete", instance,
			"--quiet",
			"--format=json",
		).Run()
	})

	out := runCLI(t, gcloudCLI("sql", "instances", "describe", instance, "--format=json"))
	var got struct {
		Name            string `json:"name"`
		State           string `json:"state"`
		DatabaseVersion string `json:"databaseVersion"`
		ConnectionName  string `json:"connectionName"`
	}
	parseJSON(t, out, &got)
	require.Equal(t, instance, got.Name)
	require.Equal(t, "RUNNABLE", got.State)
	require.Equal(t, "POSTGRES_15", got.DatabaseVersion)
	require.Equal(t, "test-project:us-central1:cli-sql", got.ConnectionName)

	runCLI(t, gcloudCLI("sql", "databases", "create", "appdb",
		"--instance", instance,
		"--quiet",
		"--format=json",
	))
	dbs := runCLI(t, gcloudCLI("sql", "databases", "list",
		"--instance", instance,
		"--format=json",
	))
	require.Contains(t, dbs, `"name": "appdb"`)

	runCLI(t, gcloudCLI("sql", "users", "create", "appuser",
		"--instance", instance,
		"--password", "Str0ng-password-12345!",
		"--quiet",
		"--format=json",
	))
	users := runCLI(t, gcloudCLI("sql", "users", "list",
		"--instance", instance,
		"--format=json",
	))
	require.Contains(t, users, `"name": "appuser"`)
}
