package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSCLI_Complete exercises the final RDS operations through the aws
// CLI in one grouped round-trip (the appdata2 shard carries RDS):
// custom DB engine versions, DB recommendations, DB snapshot tenant
// databases, valid DB instance modifications, Serverless v1 cluster
// capacity, option-group option membership (the CLI exposes the
// ModifyOptionGroup API as add-option-to-option-group /
// remove-option-from-option-group), automated-backups replication, and
// the global-cluster / read-replica switchovers.
//
// DescribeServerlessV2PlatformVersions is not exposed by aws CLI 2.26.6
// (no `describe-serverless-v2-platform-versions` subcommand), so its
// contract hook is covered SDK-side only.
func TestRDSCLI_Complete(t *testing.T) {
	// --- custom DB engine versions ---
	cevEngine := "custom-oracle-ee"
	cevVersion := "19.cev.cli.1"
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-custom-db-engine-version",
			"--engine", cevEngine, "--engine-version", cevVersion).Run()
	})
	out := runCLI(t, awsCLI("rds", "create-custom-db-engine-version",
		"--engine", cevEngine,
		"--engine-version", cevVersion,
		"--description", "cli custom build"))
	var cev struct {
		Engine        string `json:"Engine"`
		EngineVersion string `json:"EngineVersion"`
		Status        string `json:"Status"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &cev))
	assert.Equal(t, cevEngine, cev.Engine)
	assert.Equal(t, "available", cev.Status)

	out = runCLI(t, awsCLI("rds", "modify-custom-db-engine-version",
		"--engine", cevEngine, "--engine-version", cevVersion, "--status", "inactive"))
	require.NoError(t, json.Unmarshal([]byte(out), &cev))
	assert.Equal(t, "inactive", cev.Status)

	runCLI(t, awsCLI("rds", "delete-custom-db-engine-version",
		"--engine", cevEngine, "--engine-version", cevVersion))

	// --- DB instance for recommendations / valid-modifications / backups ---
	instID := "cli-cmpl-pg-db"
	out = runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instID,
		"--engine", "postgres",
		"--db-instance-class", "db.t3.micro",
		"--allocated-storage", "20",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	var inst struct {
		DBInstance struct {
			DBInstanceArn string `json:"DBInstanceArn"`
		} `json:"DBInstance"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &inst))
	instArn := inst.DBInstance.DBInstanceArn
	require.NotEmpty(t, instArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "stop-db-instance-automated-backups-replication",
			"--source-db-instance-arn", instArn).Run()
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instID, "--skip-final-snapshot").Run()
	})

	// --- DB recommendations ---
	out = runCLI(t, awsCLI("rds", "describe-db-recommendations",
		"--filters", "Name=db-instance-arn,Values="+instArn))
	var recs struct {
		DBRecommendations []struct {
			RecommendationId string `json:"RecommendationId"`
			Status           string `json:"Status"`
			ResourceArn      string `json:"ResourceArn"`
		} `json:"DBRecommendations"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &recs))
	require.NotEmpty(t, recs.DBRecommendations)
	recID := recs.DBRecommendations[0].RecommendationId
	assert.Equal(t, instArn, recs.DBRecommendations[0].ResourceArn)

	out = runCLI(t, awsCLI("rds", "modify-db-recommendation",
		"--recommendation-id", recID, "--status", "dismissed"))
	var modRec struct {
		DBRecommendation struct {
			Status string `json:"Status"`
		} `json:"DBRecommendation"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &modRec))
	assert.Equal(t, "dismissed", modRec.DBRecommendation.Status)

	// --- valid DB instance modifications ---
	out = runCLI(t, awsCLI("rds", "describe-valid-db-instance-modifications",
		"--db-instance-identifier", instID))
	var vmod struct {
		ValidDBInstanceModificationsMessage struct {
			Storage []struct {
				StorageType string `json:"StorageType"`
			} `json:"Storage"`
		} `json:"ValidDBInstanceModificationsMessage"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &vmod))
	require.NotEmpty(t, vmod.ValidDBInstanceModificationsMessage.Storage)
	assert.Equal(t, "gp2", vmod.ValidDBInstanceModificationsMessage.Storage[0].StorageType)

	// --- automated-backups replication ---
	out = runCLI(t, awsCLI("rds", "start-db-instance-automated-backups-replication",
		"--source-db-instance-arn", instArn,
		"--backup-retention-period", "14"))
	var startBak struct {
		DBInstanceAutomatedBackup struct {
			Status string `json:"Status"`
		} `json:"DBInstanceAutomatedBackup"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &startBak))
	assert.Equal(t, "replicating", startBak.DBInstanceAutomatedBackup.Status)

	out = runCLI(t, awsCLI("rds", "stop-db-instance-automated-backups-replication",
		"--source-db-instance-arn", instArn))
	var stopBak struct {
		DBInstanceAutomatedBackup struct {
			Status string `json:"Status"`
		} `json:"DBInstanceAutomatedBackup"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &stopBak))
	assert.Equal(t, "stopped", stopBak.DBInstanceAutomatedBackup.Status)

	// --- DB snapshot tenant databases (off an Oracle multi-tenant instance) ---
	oracleID := "cli-cmpl-oracle-db"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", oracleID,
		"--engine", "oracle-ee",
		"--db-instance-class", "db.t3.medium",
		"--allocated-storage", "20",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", oracleID, "--skip-final-snapshot").Run()
	})
	runCLI(t, awsCLI("rds", "create-tenant-database",
		"--db-instance-identifier", oracleID,
		"--tenant-db-name", "CLITENANT",
		"--master-username", "tadmin",
		"--master-user-password", "password123!"))
	snapID := "cli-cmpl-snap"
	runCLI(t, awsCLI("rds", "create-db-snapshot",
		"--db-snapshot-identifier", snapID,
		"--db-instance-identifier", oracleID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-snapshot", "--db-snapshot-identifier", snapID).Run()
	})
	out = runCLI(t, awsCLI("rds", "describe-db-snapshot-tenant-databases",
		"--db-snapshot-identifier", snapID))
	var stdb struct {
		DBSnapshotTenantDatabases []struct {
			TenantDBName         string `json:"TenantDBName"`
			DBSnapshotIdentifier string `json:"DBSnapshotIdentifier"`
		} `json:"DBSnapshotTenantDatabases"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &stdb))
	require.NotEmpty(t, stdb.DBSnapshotTenantDatabases)
	assert.Equal(t, "CLITENANT", stdb.DBSnapshotTenantDatabases[0].TenantDBName)

	// --- Serverless v1 cluster capacity ---
	clusterID := "cli-cmpl-aurora-cluster"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--engine", "aurora-mysql",
		"--engine-mode", "serverless",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", clusterID, "--skip-final-snapshot").Run()
	})
	out = runCLI(t, awsCLI("rds", "modify-current-db-cluster-capacity",
		"--db-cluster-identifier", clusterID, "--capacity", "8"))
	var cap struct {
		CurrentCapacity int    `json:"CurrentCapacity"`
		DBClusterID     string `json:"DBClusterIdentifier"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &cap))
	assert.Equal(t, 8, cap.CurrentCapacity)

	// --- option-group option membership (ModifyOptionGroup API) ---
	ogName := "cli-cmpl-options"
	runCLI(t, awsCLI("rds", "create-option-group",
		"--option-group-name", ogName,
		"--engine-name", "mysql",
		"--major-engine-version", "8.0",
		"--option-group-description", "cli options"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-option-group", "--option-group-name", ogName).Run()
	})
	out = runCLI(t, awsCLI("rds", "add-option-to-option-group",
		"--option-group-name", ogName,
		"--apply-immediately",
		"--options", "OptionName=MARIADB_AUDIT_PLUGIN"))
	var addOpt struct {
		OptionGroup struct {
			Options []struct {
				OptionName string `json:"OptionName"`
			} `json:"Options"`
		} `json:"OptionGroup"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &addOpt))
	require.Len(t, addOpt.OptionGroup.Options, 1)
	assert.Equal(t, "MARIADB_AUDIT_PLUGIN", addOpt.OptionGroup.Options[0].OptionName)

	out = runCLI(t, awsCLI("rds", "remove-option-from-option-group",
		"--option-group-name", ogName,
		"--apply-immediately",
		"--options", "MARIADB_AUDIT_PLUGIN"))
	var rmOpt struct {
		OptionGroup struct {
			Options []struct {
				OptionName string `json:"OptionName"`
			} `json:"Options"`
		} `json:"OptionGroup"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &rmOpt))
	assert.Empty(t, rmOpt.OptionGroup.Options)

	// --- read-replica switchover ---
	primaryID := "cli-cmpl-primary-db"
	replicaID := "cli-cmpl-replica-db"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", primaryID,
		"--engine", "postgres",
		"--db-instance-class", "db.t3.micro",
		"--allocated-storage", "20",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", replicaID, "--skip-final-snapshot").Run()
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", primaryID, "--skip-final-snapshot").Run()
	})
	runCLI(t, awsCLI("rds", "create-db-instance-read-replica",
		"--db-instance-identifier", replicaID,
		"--source-db-instance-identifier", primaryID))
	out = runCLI(t, awsCLI("rds", "switchover-read-replica",
		"--db-instance-identifier", replicaID))
	var swRR struct {
		DBInstance struct {
			ReadReplicaSourceDBInstanceIdentifier string `json:"ReadReplicaSourceDBInstanceIdentifier"`
		} `json:"DBInstance"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &swRR))
	assert.Empty(t, swRR.DBInstance.ReadReplicaSourceDBInstanceIdentifier)

	// --- global-cluster switchover ---
	gWriter := "cli-cmpl-g-writer"
	gGlobal := "cli-cmpl-global"
	out = runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", gWriter,
		"--engine", "aurora-postgresql",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	var gCl struct {
		DBCluster struct {
			DBClusterArn string `json:"DBClusterArn"`
		} `json:"DBCluster"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &gCl))
	writerArn := gCl.DBCluster.DBClusterArn
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-global-cluster", "--global-cluster-identifier", gGlobal).Run()
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", gWriter, "--skip-final-snapshot").Run()
	})
	runCLI(t, awsCLI("rds", "create-global-cluster",
		"--global-cluster-identifier", gGlobal,
		"--source-db-cluster-identifier", writerArn))
	out = runCLI(t, awsCLI("rds", "switchover-global-cluster",
		"--global-cluster-identifier", gGlobal,
		"--target-db-cluster-identifier", writerArn))
	var swG struct {
		GlobalCluster struct {
			GlobalClusterIdentifier string `json:"GlobalClusterIdentifier"`
			GlobalClusterMembers    []struct {
				IsWriter bool `json:"IsWriter"`
			} `json:"GlobalClusterMembers"`
		} `json:"GlobalCluster"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &swG))
	assert.Equal(t, gGlobal, swG.GlobalCluster.GlobalClusterIdentifier)
	require.NotEmpty(t, swG.GlobalCluster.GlobalClusterMembers)
	assert.True(t, swG.GlobalCluster.GlobalClusterMembers[0].IsWriter)
}
