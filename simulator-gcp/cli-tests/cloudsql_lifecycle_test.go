package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliSQLInstance struct {
	GceZone          string `json:"gceZone"`
	SecondaryGceZone string `json:"secondaryGceZone"`
	Settings         struct {
		DataDiskSizeGb string `json:"dataDiskSizeGb"`
	} `json:"settings"`
}

func describeCLISQLInstance(t *testing.T, name string) cliSQLInstance {
	t.Helper()
	var inst cliSQLInstance
	parseJSON(t, runCLI(t, gcloudCLI("sql", "instances", "describe", name, "--format=json")), &inst)
	return inst
}

// gcloud sql instances restart, failover, get-storage-shrink-config and
// perform-storage-shrink each wait on the operation the action runs.
func TestCloudSQLCLI_RestartFailoverAndStorageShrink(t *testing.T) {
	t.Parallel()
	instance := "cli-sql-lifecycle"
	runCLI(t, gcloudCLI("sql", "instances", "create", instance,
		"--database-version", "POSTGRES_15",
		"--tier", "db-custom-1-3840",
		"--storage-size", "20GB",
		"--availability-type", "REGIONAL",
		"--zone", "us-central1-a",
		"--secondary-zone", "us-central1-c",
		"--quiet", "--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("sql", "instances", "delete", instance, "--quiet", "--format=json").Run()
	})
	created := describeCLISQLInstance(t, instance)
	assert.Equal(t, "us-central1-a", created.GceZone)
	assert.Equal(t, "us-central1-c", created.SecondaryGceZone)

	runCLI(t, gcloudCLI("sql", "instances", "restart", instance, "--quiet", "--format=json"))

	runCLI(t, gcloudCLI("sql", "instances", "failover", instance, "--quiet", "--format=json"))
	moved := describeCLISQLInstance(t, instance)
	assert.Equal(t, "us-central1-c", moved.GceZone, "the failover moves the primary to the standby's zone")
	assert.Equal(t, "us-central1-a", moved.SecondaryGceZone)

	var shrinkConfig struct {
		MinimalTargetSizeGb string `json:"minimalTargetSizeGb"`
	}
	parseJSON(t, runCLI(t, gcloudCLI("sql", "instances", "get-storage-shrink-config", instance, "--format=json")), &shrinkConfig)
	assert.Equal(t, "10", shrinkConfig.MinimalTargetSizeGb)

	runCLI(t, gcloudCLI("sql", "instances", "perform-storage-shrink", instance, "--storage-size", "15GB", "--quiet", "--format=json"))
	assert.Equal(t, "15", describeCLISQLInstance(t, instance).Settings.DataDiskSizeGb)

	out, err := gcloudCLI("sql", "instances", "perform-storage-shrink", instance, "--storage-size", "25GB", "--quiet", "--format=json").CombinedOutput()
	require.Error(t, err, "a shrink to a larger size must fail: %s", out)
	assert.Contains(t, string(out), "must be smaller")
}

// gcloud surfaces the refusal Cloud SQL gives each replication action on a
// stand-alone primary; reschedule-maintenance reads the instance's
// scheduledMaintenance and refuses before it calls the API.
func TestCloudSQLCLI_ReplicationActionsRefuseAStandalonePrimary(t *testing.T) {
	t.Parallel()
	instance := "cli-sql-primary"
	runCLI(t, gcloudCLI("sql", "instances", "create", instance,
		"--database-version", "POSTGRES_15",
		"--region", location,
		"--tier", "db-custom-1-3840",
		"--quiet", "--format=json",
	))
	t.Cleanup(func() {
		_ = gcloudCLI("sql", "instances", "delete", instance, "--quiet", "--format=json").Run()
	})

	for _, args := range [][]string{
		{"sql", "instances", "promote-replica", instance},
		{"sql", "instances", "switchover", instance},
	} {
		out, err := gcloudCLI(append(args, "--quiet", "--format=json")...).CombinedOutput()
		require.Error(t, err, "%v on a primary must fail: %s", args, out)
		assert.Contains(t, string(out), "is not a read replica", "%v", args)
	}

	out, err := gcloudCLI("sql", "instances", "failover", instance, "--quiet", "--format=json").CombinedOutput()
	require.Error(t, err, "a zonal instance has no standby to fail over to: %s", out)
	assert.Contains(t, string(out), "high availability")

	out, err = gcloudCLI("sql", "reschedule-maintenance", instance, "--reschedule-type", "IMMEDIATE", "--quiet", "--format=json").CombinedOutput()
	require.Error(t, err, "an instance with no scheduled maintenance has none to reschedule: %s", out)
	assert.Contains(t, string(out), "does not have any scheduled maintenance")

	out, err = gcloudCLI("sql", "instances", "pre-check-major-version-upgrade", instance,
		"--target-database-version", "POSTGRES_14", "--quiet", "--format=json").CombinedOutput()
	require.Error(t, err, "a precheck to an older version must fail: %s", out)
	assert.Contains(t, string(out), "later major version")
}
