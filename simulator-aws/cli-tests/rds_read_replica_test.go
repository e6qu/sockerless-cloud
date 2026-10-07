package aws_cli_test

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliReplicaInstance struct {
	DBInstanceStatus                      string   `json:"DBInstanceStatus"`
	EngineVersion                         string   `json:"EngineVersion"`
	BackupRetentionPeriod                 int      `json:"BackupRetentionPeriod"`
	ReadReplicaSourceDBInstanceIdentifier string   `json:"ReadReplicaSourceDBInstanceIdentifier"`
	ReadReplicaDBInstanceIdentifiers      []string `json:"ReadReplicaDBInstanceIdentifiers"`
	DBParameterGroups                     []struct {
		DBParameterGroupName string `json:"DBParameterGroupName"`
	} `json:"DBParameterGroups"`
	StatusInfos []struct {
		StatusType string `json:"StatusType"`
		Status     string `json:"Status"`
		Normal     bool   `json:"Normal"`
	} `json:"StatusInfos"`
	PendingModifiedValues struct {
		EngineVersion string `json:"EngineVersion"`
	} `json:"PendingModifiedValues"`
	Endpoint struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Endpoint"`
}

func cliDescribeReplicaInstance(t *testing.T, instanceID string) cliReplicaInstance {
	t.Helper()
	var described struct {
		DBInstances []cliReplicaInstance `json:"DBInstances"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-instances", "--db-instance-identifier", instanceID)), &described)
	require.Len(t, described.DBInstances, 1)
	return described.DBInstances[0]
}

// aws rds create-db-instance-read-replica provisions an RDS for MySQL replica
// that holds its source's rows, serves them read-only, applies the source's
// later writes, and reports its read replication status and its ReplicaLag
// metric; aws rds promote-read-replica ends the replication and reopens the
// replica for writes. The instances use their engine family's default DB
// parameter group.
func TestRDSCLI_ReadReplicaReplicatesAndPromotes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	sourceID := fmt.Sprintf("cli-rr-src-%d", time.Now().UnixNano())
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", sourceID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--allocated-storage", "20",
		"--master-username", cliRestoreUsername,
		"--master-user-password", cliRestorePassword,
		"--db-name", cliRestoreDatabase,
		"--backup-retention-period", "1"))
	cliCleanupDBInstance(t, sourceID)
	source := cliAvailableDBInstance(t, sourceID)
	sourceDB := cliConnectMySQLAs(t, source.Endpoint.Address, source.Endpoint.Port, cliRestoreUsername, cliRestorePassword)
	for _, statement := range []string{
		`CREATE TABLE ledger (entry varchar(64) NOT NULL)`,
		`INSERT INTO ledger VALUES ('before-replica')`,
	} {
		_, err := sourceDB.ExecContext(ctx, statement)
		require.NoError(t, err, statement)
	}

	replicaID := sourceID + "-replica"
	var created struct {
		DBInstance cliReplicaInstance `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "create-db-instance-read-replica",
		"--db-instance-identifier", replicaID,
		"--source-db-instance-identifier", sourceID)), &created)
	cliCleanupDBInstance(t, replicaID)
	assert.Equal(t, "creating", created.DBInstance.DBInstanceStatus)
	assert.Equal(t, sourceID, created.DBInstance.ReadReplicaSourceDBInstanceIdentifier)

	cliWaitDBInstanceAvailable(t, replicaID)
	replica := cliDescribeReplicaInstance(t, replicaID)
	require.Len(t, replica.StatusInfos, 1)
	assert.Equal(t, "read replication", replica.StatusInfos[0].StatusType)
	assert.Equal(t, "replicating", replica.StatusInfos[0].Status)
	assert.True(t, replica.StatusInfos[0].Normal)
	require.Len(t, replica.DBParameterGroups, 1)
	assert.Equal(t, "default.mysql8.0", replica.DBParameterGroups[0].DBParameterGroupName)

	replicaDB := cliConnectMySQLAs(t, replica.Endpoint.Address, replica.Endpoint.Port, cliRestoreUsername, cliRestorePassword)
	assert.Equal(t, []string{"before-replica"}, cliMySQLLedger(t, ctx, replicaDB))
	_, err := replicaDB.ExecContext(ctx, `INSERT INTO ledger VALUES ('written-to-replica')`)
	assert.Error(t, err, "the replica serves its sessions read-only")
	_, err = sourceDB.ExecContext(ctx, `INSERT INTO ledger VALUES ('after-replica')`)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		entries := cliMySQLLedger(t, ctx, replicaDB)
		return len(entries) == 2 && entries[0] == "after-replica"
	}, 2*time.Minute, 100*time.Millisecond, "the replica applies the source's later writes")

	var lag struct {
		Datapoints []struct {
			Maximum float64 `json:"Maximum"`
		} `json:"Datapoints"`
	}
	parseJSON(t, runCLI(t, awsCLI("cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/RDS", "--metric-name", "ReplicaLag",
		"--dimensions", "Name=DBInstanceIdentifier,Value="+replicaID,
		"--start-time", time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339),
		"--end-time", time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		"--period", "60", "--statistics", "Maximum")), &lag)
	require.NotEmpty(t, lag.Datapoints, "the replica publishes ReplicaLag")

	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "stop-db-instance", "--db-instance-identifier", sourceID)),
		"InvalidDBInstanceState")

	var promoted struct {
		DBInstance cliReplicaInstance `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "promote-read-replica", "--db-instance-identifier", replicaID)), &promoted)
	assert.Equal(t, "modifying", promoted.DBInstance.DBInstanceStatus)
	cliWaitDBInstanceAvailable(t, replicaID)
	standalone := cliDescribeReplicaInstance(t, replicaID)
	assert.Empty(t, standalone.ReadReplicaSourceDBInstanceIdentifier)
	assert.Empty(t, standalone.StatusInfos)
	assert.Empty(t, cliDescribeReplicaInstance(t, sourceID).ReadReplicaDBInstanceIdentifiers)
	promotedDB := cliConnectMySQLAs(t, standalone.Endpoint.Address, standalone.Endpoint.Port, cliRestoreUsername, cliRestorePassword)
	_, err = promotedDB.ExecContext(ctx, `INSERT INTO ledger VALUES ('written-to-promoted')`)
	require.NoError(t, err)
	assert.Equal(t, []string{"after-replica", "before-replica", "written-to-promoted"}, cliMySQLLedger(t, ctx, promotedDB))
}

// aws rds create-db-instance runs the engine release of the version it names
// and refuses a version Amazon RDS does not offer; aws rds modify-db-instance
// upgrades the engine in place, and describe-db-engine-versions names the
// default version an instance created without one runs.
func TestRDSCLI_ReleasedEngineVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var defaults struct {
		DBEngineVersions []struct {
			EngineVersion          string `json:"EngineVersion"`
			DBParameterGroupFamily string `json:"DBParameterGroupFamily"`
		} `json:"DBEngineVersions"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "describe-db-engine-versions", "--engine", "postgres", "--default-only")), &defaults)
	require.Len(t, defaults.DBEngineVersions, 1)
	assert.Equal(t, "16.15", defaults.DBEngineVersions[0].EngineVersion)

	id := fmt.Sprintf("cli-version-%d", time.Now().UnixNano())
	create := func(instanceID, version string) *exec.Cmd {
		return awsCLI("rds", "create-db-instance",
			"--db-instance-identifier", instanceID,
			"--db-instance-class", "db.t3.micro",
			"--engine", "postgres",
			"--engine-version", version,
			"--allocated-storage", "20",
			"--master-username", cliRestoreUsername,
			"--master-user-password", cliRestorePassword,
			"--db-name", cliRestoreDatabase,
			"--backup-retention-period", "0")
	}
	assert.Contains(t, runCLIExpectError(t, create(id+"-unoffered", "15.4")), "InvalidParameterCombination")

	runCLI(t, create(id, "16.14"))
	cliCleanupDBInstance(t, id)
	instance := cliAvailableDBInstance(t, id)
	described := cliDescribeReplicaInstance(t, id)
	require.Len(t, described.DBParameterGroups, 1)
	assert.Equal(t, "default.postgres16", described.DBParameterGroups[0].DBParameterGroupName)
	conn := cliConnectPostgres(t, ctx, instance.Endpoint.Address, instance.Endpoint.Port)
	var version string
	require.NoError(t, conn.QueryRow(ctx, `SHOW server_version`).Scan(&version))
	assert.Equal(t, "16.14", version)
	_, err := conn.Exec(ctx, `CREATE TABLE ledger (entry varchar(64) NOT NULL); INSERT INTO ledger VALUES ('before-upgrade')`)
	require.NoError(t, err)

	var modified struct {
		DBInstance cliReplicaInstance `json:"DBInstance"`
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-instance",
		"--db-instance-identifier", id, "--engine-version", "16.15", "--apply-immediately")), &modified)
	assert.Equal(t, "upgrading", modified.DBInstance.DBInstanceStatus)
	assert.Equal(t, "16.15", modified.DBInstance.PendingModifiedValues.EngineVersion)
	upgraded := cliAvailableDBInstance(t, id)
	assert.Equal(t, "16.15", cliDescribeReplicaInstance(t, id).EngineVersion)
	conn = cliConnectPostgres(t, ctx, upgraded.Endpoint.Address, upgraded.Endpoint.Port)
	require.NoError(t, conn.QueryRow(ctx, `SHOW server_version`).Scan(&version))
	assert.Equal(t, "16.15", version)
	assert.Equal(t, []string{"before-upgrade"}, cliLedger(t, ctx, conn))
	assert.Contains(t, runCLIExpectError(t, awsCLI("rds", "modify-db-instance",
		"--db-instance-identifier", id, "--engine-version", "16.14", "--apply-immediately")), "InvalidParameterCombination")
}
