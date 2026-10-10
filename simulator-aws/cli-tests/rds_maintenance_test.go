package aws_cli_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliMaintainedInstance struct {
	DBInstanceStatus           string `json:"DBInstanceStatus"`
	DBInstanceClass            string `json:"DBInstanceClass"`
	AllocatedStorage           int    `json:"AllocatedStorage"`
	EngineVersion              string `json:"EngineVersion"`
	PreferredMaintenanceWindow string `json:"PreferredMaintenanceWindow"`
	DBParameterGroups          []struct {
		DBParameterGroupName string `json:"DBParameterGroupName"`
		ParameterApplyStatus string `json:"ParameterApplyStatus"`
	} `json:"DBParameterGroups"`
	PendingModifiedValues struct {
		DBInstanceClass  string `json:"DBInstanceClass"`
		AllocatedStorage int    `json:"AllocatedStorage"`
	} `json:"PendingModifiedValues"`
	Endpoint struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Endpoint"`
}

func cliDescribeMaintainedInstance(t *testing.T, instanceID string) cliMaintainedInstance {
	t.Helper()
	var described struct {
		DBInstances []cliMaintainedInstance `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", instanceID)), &described)
	require.Len(t, described.DBInstances, 1)
	return described.DBInstances[0]
}

// aws rds modify-db-instance without --apply-immediately holds a class and a
// storage change for the instance's maintenance window, which
// describe-db-instances reports as pending modified values; a later
// --apply-immediately applies them, and a window opening at the next minute
// applies what it holds when it opens. describe-db-instances is the only
// signal of the window's work the CLI has, so the test reads it at the
// db-instance-available waiter's 30-second cadence.
func TestRDSCLI_MaintenanceWindowAppliesPendingModifications(t *testing.T) {
	id := fmt.Sprintf("cli-maintenance-%d", time.Now().UnixNano())
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--backup-retention-period", "0"))
	cliCleanupDBInstance(t, id)
	cliWaitDBInstanceAvailable(t, id)
	assert.Regexp(t, `^[a-z]{3}:\d\d:\d\d-[a-z]{3}:\d\d:\d\d$`, cliDescribeMaintainedInstance(t, id).PreferredMaintenanceWindow)

	var modified struct {
		DBInstance cliMaintainedInstance `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-instance",
		"--db-instance-identifier", id, "--allocated-storage", "30", "--no-apply-immediately")), &modified)
	assert.Equal(t, 20, modified.DBInstance.AllocatedStorage)
	assert.Equal(t, 30, modified.DBInstance.PendingModifiedValues.AllocatedStorage)
	modified.DBInstance = cliMaintainedInstance{}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-instance",
		"--db-instance-identifier", id, "--apply-immediately")), &modified)
	assert.Equal(t, 30, modified.DBInstance.AllocatedStorage, "--apply-immediately applies the pending changes")
	assert.Zero(t, modified.DBInstance.PendingModifiedValues.AllocatedStorage)

	start := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
	weekly := func(at time.Time) string { return strings.ToLower(at.Format("Mon")) + ":" + at.Format("15:04") }
	backup := start.Add(12 * time.Hour)
	window := weekly(start) + "-" + weekly(start.Add(30*time.Minute))
	modified.DBInstance = cliMaintainedInstance{}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.small",
		"--allocated-storage", "40",
		"--preferred-maintenance-window", window,
		"--preferred-backup-window", backup.Format("15:04")+"-"+backup.Add(30*time.Minute).Format("15:04"))), &modified)
	assert.Equal(t, window, modified.DBInstance.PreferredMaintenanceWindow)
	assert.Equal(t, "db.t3.micro", modified.DBInstance.DBInstanceClass)
	assert.Equal(t, "db.t3.small", modified.DBInstance.PendingModifiedValues.DBInstanceClass)
	assert.Equal(t, 40, modified.DBInstance.PendingModifiedValues.AllocatedStorage)

	deadline := time.Now().Add(5 * time.Minute)
	instance := cliDescribeMaintainedInstance(t, id)
	for instance.PendingModifiedValues.DBInstanceClass != "" {
		require.True(t, time.Now().Before(deadline), "the maintenance window %s must apply the pending changes", window)
		time.Sleep(30 * time.Second)
		instance = cliDescribeMaintainedInstance(t, id)
	}
	cliWaitDBInstanceAvailable(t, id)
	instance = cliDescribeMaintainedInstance(t, id)
	assert.Equal(t, "db.t3.small", instance.DBInstanceClass)
	assert.Equal(t, 40, instance.AllocatedStorage)
}

// aws rds modify-db-parameter-group sets an RDS for PostgreSQL instance's
// engine parameters: the engine starts with them, a dynamic one applied
// immediately reaches the running engine, and a static one waits for aws rds
// reboot-db-instance while describe-db-instances reports the group
// pending-reboot. describe-engine-default-parameters pages through the
// engine's whole catalog.
func TestRDSCLI_ModifiedDBParameterGroupsReachTheEngine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var defaults struct {
		EngineDefaults struct {
			Parameters []struct {
				ParameterName  string `json:"ParameterName"`
				ParameterValue string `json:"ParameterValue"`
				ApplyType      string `json:"ApplyType"`
			} `json:"Parameters"`
		} `json:"EngineDefaults"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-engine-default-parameters",
		"--db-parameter-group-family", "postgres16", "--page-size", "50")), &defaults)
	assert.Greater(t, len(defaults.EngineDefaults.Parameters), 300, "the CLI pages through every PostgreSQL 16 setting")

	group := fmt.Sprintf("cli-pg-params-%d", time.Now().UnixNano())
	runCLI(t, awsCLI("rds", "create-db-parameter-group",
		"--db-parameter-group-name", group, "--db-parameter-group-family", "postgres16", "--description", "engine parameters"))
	t.Cleanup(func() { _ = awsCLI("rds", "delete-db-parameter-group", "--db-parameter-group-name", group).Run() })
	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "modify-db-parameter-group", "--db-parameter-group-name", group,
		"--parameters", "ParameterName=max_connections,ParameterValue=150,ApplyMethod=immediate")), "InvalidParameterCombination")
	runCLI(t, awsCLI("rds", "modify-db-parameter-group", "--db-parameter-group-name", group, "--parameters",
		"ParameterName=work_mem,ParameterValue=8192,ApplyMethod=immediate",
		"ParameterName=max_connections,ParameterValue=150,ApplyMethod=pending-reboot"))
	var set struct {
		Parameters []struct {
			ParameterName  string `json:"ParameterName"`
			ParameterValue string `json:"ParameterValue"`
			Source         string `json:"Source"`
		} `json:"Parameters"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-parameters", "--db-parameter-group-name", group, "--source", "user")), &set)
	require.Len(t, set.Parameters, 2)

	id := fmt.Sprintf("cli-pg-params-%d", time.Now().UnixNano())
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--db-parameter-group-name", group,
		"--backup-retention-period", "0"))
	cliCleanupDBInstance(t, id)
	setting := func(name string) string {
		instance := cliAvailableDBInstance(t, id)
		conn := cliConnectPostgres(t, ctx, instance.Endpoint.Address, instance.Endpoint.Port)
		var value string
		require.NoError(t, conn.QueryRow(ctx, `SELECT current_setting($1)`, name).Scan(&value))
		return value
	}
	assert.Equal(t, "8MB", setting("work_mem"))
	assert.Equal(t, "150", setting("max_connections"))

	runCLI(t, awsCLI("rds", "modify-db-parameter-group", "--db-parameter-group-name", group, "--parameters",
		"ParameterName=work_mem,ParameterValue=16384,ApplyMethod=immediate",
		"ParameterName=max_connections,ParameterValue=200,ApplyMethod=pending-reboot"))
	assert.Equal(t, "16MB", setting("work_mem"))
	assert.Equal(t, "150", setting("max_connections"))
	assert.Equal(t, "pending-reboot", cliDescribeMaintainedInstance(t, id).DBParameterGroups[0].ParameterApplyStatus)

	runCLI(t, awsCLI("rds", "reboot-db-instance", "--db-instance-identifier", id))
	assert.Equal(t, "200", setting("max_connections"))
	assert.Equal(t, "in-sync", cliDescribeMaintainedInstance(t, id).DBParameterGroups[0].ParameterApplyStatus)
}
