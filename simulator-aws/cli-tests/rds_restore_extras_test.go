package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSCLI_RestoreAndReserved covers the cluster/instance restore
// family and the reserved-instance catalog/purchase flow through the
// aws CLI. Grouped because appdata2 carries RDS.
func TestRDSCLI_RestoreAndReserved(t *testing.T) {
	// --- Restore family ---
	srcCluster := "cli-rext-src-cluster"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", srcCluster,
		"--engine", "aurora-mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", srcCluster, "--skip-final-snapshot").Run()
	})

	snapID := "cli-rext-src-snap"
	runCLI(t, awsCLI("rds", "create-db-cluster-snapshot",
		"--db-cluster-snapshot-identifier", snapID,
		"--db-cluster-identifier", srcCluster))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-snapshot",
			"--db-cluster-snapshot-identifier", snapID).Run()
	})

	restored := "cli-rext-restored"
	out := runCLI(t, awsCLI("rds", "restore-db-cluster-from-snapshot",
		"--db-cluster-identifier", restored,
		"--snapshot-identifier", snapID,
		"--engine", "aurora-mysql"))
	var rOut struct {
		DBCluster struct {
			DBClusterIdentifier string `json:"DBClusterIdentifier"`
			Status              string `json:"Status"`
		} `json:"DBCluster"`
	}
	parseJSON(t, out, &rOut)
	assert.Equal(t, restored, rOut.DBCluster.DBClusterIdentifier)
	assert.Equal(t, "available", rOut.DBCluster.Status)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", restored, "--skip-final-snapshot").Run()
	})

	pit := "cli-rext-cluster-pit"
	runCLI(t, awsCLI("rds", "restore-db-cluster-to-point-in-time",
		"--db-cluster-identifier", pit,
		"--source-db-cluster-identifier", srcCluster,
		"--use-latest-restorable-time"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", pit, "--skip-final-snapshot").Run()
	})

	s3cluster := "cli-rext-cluster-s3"
	runCLI(t, awsCLI("rds", "restore-db-cluster-from-s3",
		"--db-cluster-identifier", s3cluster,
		"--engine", "aurora-mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--source-engine", "mysql",
		"--source-engine-version", "8.0",
		"--s3-bucket-name", "my-backup-bucket",
		"--s3-ingestion-role-arn", "arn:aws:iam::123456789012:role/rds-s3"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", s3cluster, "--skip-final-snapshot").Run()
	})

	srcInst := "cli-rext-src-instance"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", srcInst,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", srcInst, "--skip-final-snapshot").Run()
	})

	instPIT := "cli-rext-instance-pit"
	runCLI(t, awsCLI("rds", "restore-db-instance-to-point-in-time",
		"--source-db-instance-identifier", srcInst,
		"--target-db-instance-identifier", instPIT,
		"--use-latest-restorable-time"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instPIT, "--skip-final-snapshot").Run()
	})

	instS3 := "cli-rext-instance-s3"
	runCLI(t, awsCLI("rds", "restore-db-instance-from-s3",
		"--db-instance-identifier", instS3,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20",
		"--source-engine", "mysql",
		"--source-engine-version", "8.0",
		"--s3-bucket-name", "my-backup-bucket",
		"--s3-ingestion-role-arn", "arn:aws:iam::123456789012:role/rds-s3"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instS3, "--skip-final-snapshot").Run()
	})

	// --- Reserved instances ---
	offerOut := runCLI(t, awsCLI("rds", "describe-reserved-db-instances-offerings"))
	var offerings struct {
		ReservedDBInstancesOfferings []struct {
			ReservedDBInstancesOfferingId string `json:"ReservedDBInstancesOfferingId"`
		} `json:"ReservedDBInstancesOfferings"`
	}
	parseJSON(t, offerOut, &offerings)
	require.NotEmpty(t, offerings.ReservedDBInstancesOfferings)
	offeringID := offerings.ReservedDBInstancesOfferings[0].ReservedDBInstancesOfferingId

	purchaseOut := runCLI(t, awsCLI("rds", "purchase-reserved-db-instances-offering",
		"--reserved-db-instances-offering-id", offeringID,
		"--reserved-db-instance-id", "cli-rext-reservation",
		"--db-instance-count", "2"))
	var purchase struct {
		ReservedDBInstance struct {
			ReservedDBInstanceId string `json:"ReservedDBInstanceId"`
			State                string `json:"State"`
			DBInstanceCount      int    `json:"DBInstanceCount"`
		} `json:"ReservedDBInstance"`
	}
	parseJSON(t, purchaseOut, &purchase)
	assert.Equal(t, "cli-rext-reservation", purchase.ReservedDBInstance.ReservedDBInstanceId)
	assert.Equal(t, "active", purchase.ReservedDBInstance.State)

	descOut := runCLI(t, awsCLI("rds", "describe-reserved-db-instances",
		"--reserved-db-instance-id", "cli-rext-reservation"))
	var desc struct {
		ReservedDBInstances []struct {
			ReservedDBInstanceId string `json:"ReservedDBInstanceId"`
		} `json:"ReservedDBInstances"`
	}
	parseJSON(t, descOut, &desc)
	require.Len(t, desc.ReservedDBInstances, 1)
}

// TestRDSCLI_DeploymentsAndIntegrations covers blue/green deployments,
// zero-ETL integrations, tenant databases, and Aurora Limitless shard
// groups through the aws CLI.
func TestRDSCLI_DeploymentsAndIntegrations(t *testing.T) {
	// --- Blue/green ---
	source := "arn:aws:rds:us-east-1:123456789012:cluster:cli-bg-source"
	bgOut := runCLI(t, awsCLI("rds", "create-blue-green-deployment",
		"--blue-green-deployment-name", "cli-rext-bg",
		"--source", source))
	var bg struct {
		BlueGreenDeployment struct {
			BlueGreenDeploymentIdentifier string `json:"BlueGreenDeploymentIdentifier"`
			Source                        string `json:"Source"`
			Target                        string `json:"Target"`
		} `json:"BlueGreenDeployment"`
	}
	parseJSON(t, bgOut, &bg)
	bgID := bg.BlueGreenDeployment.BlueGreenDeploymentIdentifier
	require.NotEmpty(t, bgID)
	assert.Equal(t, source, bg.BlueGreenDeployment.Source)
	target := bg.BlueGreenDeployment.Target
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-blue-green-deployment",
			"--blue-green-deployment-identifier", bgID).Run()
	})

	descBG := runCLI(t, awsCLI("rds", "describe-blue-green-deployments",
		"--blue-green-deployment-identifier", bgID))
	var dbg struct {
		BlueGreenDeployments []struct {
			BlueGreenDeploymentIdentifier string `json:"BlueGreenDeploymentIdentifier"`
		} `json:"BlueGreenDeployments"`
	}
	parseJSON(t, descBG, &dbg)
	require.Len(t, dbg.BlueGreenDeployments, 1)

	swOut := runCLI(t, awsCLI("rds", "switchover-blue-green-deployment",
		"--blue-green-deployment-identifier", bgID))
	var sw struct {
		BlueGreenDeployment struct {
			Status string `json:"Status"`
			Source string `json:"Source"`
		} `json:"BlueGreenDeployment"`
	}
	parseJSON(t, swOut, &sw)
	assert.Equal(t, "SWITCHOVER_COMPLETED", sw.BlueGreenDeployment.Status)
	assert.Equal(t, target, sw.BlueGreenDeployment.Source)

	// --- Integrations ---
	intSrc := "arn:aws:rds:us-east-1:123456789012:cluster:cli-int-source"
	intTgt := "arn:aws:redshift-serverless:us-east-1:123456789012:namespace/ns-1"
	intOut := runCLI(t, awsCLI("rds", "create-integration",
		"--integration-name", "cli-rext-integration",
		"--source-arn", intSrc,
		"--target-arn", intTgt))
	var integ struct {
		IntegrationArn string `json:"IntegrationArn"`
		SourceArn      string `json:"SourceArn"`
	}
	parseJSON(t, intOut, &integ)
	require.NotEmpty(t, integ.IntegrationArn)
	assert.Equal(t, intSrc, integ.SourceArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-integration",
			"--integration-identifier", integ.IntegrationArn).Run()
	})

	descInt := runCLI(t, awsCLI("rds", "describe-integrations",
		"--integration-identifier", integ.IntegrationArn))
	var dint struct {
		Integrations []struct {
			TargetArn string `json:"TargetArn"`
		} `json:"Integrations"`
	}
	parseJSON(t, descInt, &dint)
	require.Len(t, dint.Integrations, 1)
	assert.Equal(t, intTgt, dint.Integrations[0].TargetArn)

	runCLI(t, awsCLI("rds", "modify-integration",
		"--integration-identifier", integ.IntegrationArn))

	// --- Tenant databases ---
	cdbInst := "cli-rext-oracle-cdb"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", cdbInst,
		"--db-instance-class", "db.t3.medium",
		"--engine", "oracle-ee",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", cdbInst, "--skip-final-snapshot").Run()
	})

	tenOut := runCLI(t, awsCLI("rds", "create-tenant-database",
		"--db-instance-identifier", cdbInst,
		"--tenant-db-name", "tenant1",
		"--master-username", "tadmin",
		"--master-user-password", "password123!"))
	var ten struct {
		TenantDatabase struct {
			TenantDBName string `json:"TenantDBName"`
			Status       string `json:"Status"`
		} `json:"TenantDatabase"`
	}
	parseJSON(t, tenOut, &ten)
	assert.Equal(t, "tenant1", ten.TenantDatabase.TenantDBName)
	assert.Equal(t, "available", ten.TenantDatabase.Status)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-tenant-database",
			"--db-instance-identifier", cdbInst, "--tenant-db-name", "tenant1").Run()
	})

	descTen := runCLI(t, awsCLI("rds", "describe-tenant-databases",
		"--db-instance-identifier", cdbInst))
	var dten struct {
		TenantDatabases []struct {
			TenantDBName string `json:"TenantDBName"`
		} `json:"TenantDatabases"`
	}
	parseJSON(t, descTen, &dten)
	require.Len(t, dten.TenantDatabases, 1)

	runCLI(t, awsCLI("rds", "modify-tenant-database",
		"--db-instance-identifier", cdbInst,
		"--tenant-db-name", "tenant1",
		"--new-tenant-db-name", "tenant1renamed"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-tenant-database",
			"--db-instance-identifier", cdbInst, "--tenant-db-name", "tenant1renamed").Run()
	})

	// --- Shard groups ---
	limitlessCluster := "cli-rext-limitless"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", limitlessCluster,
		"--engine", "aurora-postgresql",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", limitlessCluster, "--skip-final-snapshot").Run()
	})

	sgID := "cli-rext-shard-group"
	sgOut := runCLI(t, awsCLI("rds", "create-db-shard-group",
		"--db-shard-group-identifier", sgID,
		"--db-cluster-identifier", limitlessCluster,
		"--max-acu", "16",
		"--min-acu", "2"))
	var sg struct {
		DBShardGroupIdentifier string  `json:"DBShardGroupIdentifier"`
		MaxACU                 float64 `json:"MaxACU"`
	}
	parseJSON(t, sgOut, &sg)
	assert.Equal(t, sgID, sg.DBShardGroupIdentifier)
	assert.Equal(t, float64(16), sg.MaxACU)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-shard-group",
			"--db-shard-group-identifier", sgID).Run()
	})

	descSG := runCLI(t, awsCLI("rds", "describe-db-shard-groups",
		"--db-shard-group-identifier", sgID))
	var dsg struct {
		DBShardGroups []struct {
			DBClusterIdentifier string `json:"DBClusterIdentifier"`
		} `json:"DBShardGroups"`
	}
	parseJSON(t, descSG, &dsg)
	require.Len(t, dsg.DBShardGroups, 1)
	assert.Equal(t, limitlessCluster, dsg.DBShardGroups[0].DBClusterIdentifier)

	modSG := runCLI(t, awsCLI("rds", "modify-db-shard-group",
		"--db-shard-group-identifier", sgID,
		"--max-acu", "32"))
	var msg struct {
		MaxACU float64 `json:"MaxACU"`
	}
	parseJSON(t, modSG, &msg)
	assert.Equal(t, float64(32), msg.MaxACU)

	runCLI(t, awsCLI("rds", "reboot-db-shard-group",
		"--db-shard-group-identifier", sgID))
}

// TestRDSCLI_ClusterOpsAndStatics covers activity streams, backtrack,
// export tasks, the assorted cluster operations, snapshot attributes,
// and the static catalog describes through the aws CLI.
func TestRDSCLI_ClusterOpsAndStatics(t *testing.T) {
	// --- Activity streams ---
	dasArn := "arn:aws:rds:us-east-1:123456789012:cluster:cli-das"
	saOut := runCLI(t, awsCLI("rds", "start-activity-stream",
		"--resource-arn", dasArn,
		"--mode", "async",
		"--kms-key-id", "arn:aws:kms:us-east-1:123456789012:key/abc",
		"--apply-immediately"))
	var sa struct {
		Status string `json:"Status"`
	}
	parseJSON(t, saOut, &sa)
	assert.Equal(t, "starting", sa.Status)
	runCLI(t, awsCLI("rds", "modify-activity-stream", "--resource-arn", dasArn))
	runCLI(t, awsCLI("rds", "stop-activity-stream", "--resource-arn", dasArn))

	// --- Backtrack + cluster ops ---
	clusterID := "cli-rext-ops-cluster"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--engine", "aurora-mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--backtrack-window", "3600"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", clusterID, "--skip-final-snapshot").Run()
	})
	clOut := runCLI(t, awsCLI("rds", "describe-db-clusters",
		"--db-cluster-identifier", clusterID))
	var cl struct {
		DBClusters []struct {
			DBClusterArn string `json:"DBClusterArn"`
		} `json:"DBClusters"`
	}
	parseJSON(t, clOut, &cl)
	require.Len(t, cl.DBClusters, 1)
	clusterArn := cl.DBClusters[0].DBClusterArn

	btOut := runCLI(t, awsCLI("rds", "backtrack-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--backtrack-to", "2026-01-01T00:00:00Z"))
	var bt struct {
		DBClusterIdentifier string `json:"DBClusterIdentifier"`
		BacktrackIdentifier string `json:"BacktrackIdentifier"`
	}
	parseJSON(t, btOut, &bt)
	assert.Equal(t, clusterID, bt.DBClusterIdentifier)
	require.NotEmpty(t, bt.BacktrackIdentifier)

	descBT := runCLI(t, awsCLI("rds", "describe-db-cluster-backtracks",
		"--db-cluster-identifier", clusterID))
	var dbt struct {
		DBClusterBacktracks []struct {
			Status string `json:"Status"`
		} `json:"DBClusterBacktracks"`
	}
	parseJSON(t, descBT, &dbt)
	require.Len(t, dbt.DBClusterBacktracks, 1)
	assert.Equal(t, "COMPLETED", dbt.DBClusterBacktracks[0].Status)

	runCLI(t, awsCLI("rds", "reboot-db-cluster", "--db-cluster-identifier", clusterID))
	runCLI(t, awsCLI("rds", "promote-read-replica-db-cluster", "--db-cluster-identifier", clusterID))

	if clusterArn != "" {
		enOut := runCLI(t, awsCLI("rds", "enable-http-endpoint", "--resource-arn", clusterArn))
		var en struct {
			HttpEndpointEnabled bool `json:"HttpEndpointEnabled"`
		}
		parseJSON(t, enOut, &en)
		assert.True(t, en.HttpEndpointEnabled)
		runCLI(t, awsCLI("rds", "disable-http-endpoint", "--resource-arn", clusterArn))
	}

	// Reset cluster parameter group.
	pgName := "cli-rext-cluster-pg"
	runCLI(t, awsCLI("rds", "create-db-cluster-parameter-group",
		"--db-cluster-parameter-group-name", pgName,
		"--db-parameter-group-family", "aurora-mysql8.0",
		"--description", "cli-rext"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-parameter-group",
			"--db-cluster-parameter-group-name", pgName).Run()
	})
	runCLI(t, awsCLI("rds", "reset-db-cluster-parameter-group",
		"--db-cluster-parameter-group-name", pgName,
		"--reset-all-parameters"))

	// Modify cluster endpoint.
	epID := "cli-rext-custom-ep"
	runCLI(t, awsCLI("rds", "create-db-cluster-endpoint",
		"--db-cluster-identifier", clusterID,
		"--db-cluster-endpoint-identifier", epID,
		"--endpoint-type", "READER"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-endpoint",
			"--db-cluster-endpoint-identifier", epID).Run()
	})
	runCLI(t, awsCLI("rds", "modify-db-cluster-endpoint",
		"--db-cluster-endpoint-identifier", epID,
		"--endpoint-type", "ANY"))

	// Global cluster failover/remove.
	gcID := "cli-rext-global"
	runCLI(t, awsCLI("rds", "create-global-cluster",
		"--global-cluster-identifier", gcID,
		"--source-db-cluster-identifier", clusterArn))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-global-cluster",
			"--global-cluster-identifier", gcID).Run()
	})
	if clusterArn != "" {
		runCLI(t, awsCLI("rds", "failover-global-cluster",
			"--global-cluster-identifier", gcID,
			"--target-db-cluster-identifier", clusterArn))
		runCLI(t, awsCLI("rds", "remove-from-global-cluster",
			"--global-cluster-identifier", gcID,
			"--db-cluster-identifier", clusterArn))
	}

	// --- Cluster-snapshot attributes ---
	csID := "cli-rext-snapattr-snap"
	runCLI(t, awsCLI("rds", "create-db-cluster-snapshot",
		"--db-cluster-snapshot-identifier", csID,
		"--db-cluster-identifier", clusterID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-snapshot",
			"--db-cluster-snapshot-identifier", csID).Run()
	})
	runCLI(t, awsCLI("rds", "modify-db-cluster-snapshot-attribute",
		"--db-cluster-snapshot-identifier", csID,
		"--attribute-name", "restore",
		"--values-to-add", "123456789012", "all"))
	descAttr := runCLI(t, awsCLI("rds", "describe-db-cluster-snapshot-attributes",
		"--db-cluster-snapshot-identifier", csID))
	var attr struct {
		DBClusterSnapshotAttributesResult struct {
			DBClusterSnapshotAttributes []struct {
				AttributeValues []string `json:"AttributeValues"`
			} `json:"DBClusterSnapshotAttributes"`
		} `json:"DBClusterSnapshotAttributesResult"`
	}
	parseJSON(t, descAttr, &attr)
	require.Len(t, attr.DBClusterSnapshotAttributesResult.DBClusterSnapshotAttributes, 1)
	assert.ElementsMatch(t, []string{"123456789012", "all"},
		attr.DBClusterSnapshotAttributesResult.DBClusterSnapshotAttributes[0].AttributeValues)

	// --- ModifyDBSnapshot + export tasks ---
	instID := "cli-rext-snapmod-instance"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--engine-version", "8.0.39",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instID, "--skip-final-snapshot").Run()
	})
	snapID := "cli-rext-snapmod-snap"
	runCLI(t, awsCLI("rds", "create-db-snapshot",
		"--db-snapshot-identifier", snapID,
		"--db-instance-identifier", instID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-snapshot",
			"--db-snapshot-identifier", snapID).Run()
	})
	modSnap := runCLI(t, awsCLI("rds", "modify-db-snapshot",
		"--db-snapshot-identifier", snapID,
		"--engine-version", "8.0.40"))
	var ms struct {
		DBSnapshot struct {
			EngineVersion string `json:"EngineVersion"`
		} `json:"DBSnapshot"`
	}
	parseJSON(t, modSnap, &ms)
	assert.Equal(t, "8.0.40", ms.DBSnapshot.EngineVersion)

	exportID := "cli-rext-export"
	expOut := runCLI(t, awsCLI("rds", "start-export-task",
		"--export-task-identifier", exportID,
		"--source-arn", "arn:aws:rds:us-east-1:123456789012:snapshot:exp",
		"--s3-bucket-name", "exports-bucket",
		"--iam-role-arn", "arn:aws:iam::123456789012:role/export",
		"--kms-key-id", "arn:aws:kms:us-east-1:123456789012:key/exp"))
	var exp struct {
		ExportTaskIdentifier string `json:"ExportTaskIdentifier"`
		Status               string `json:"Status"`
	}
	parseJSON(t, expOut, &exp)
	assert.Equal(t, exportID, exp.ExportTaskIdentifier)
	assert.Equal(t, "COMPLETE", exp.Status)
	descExp := runCLI(t, awsCLI("rds", "describe-export-tasks",
		"--export-task-identifier", exportID))
	var dexp struct {
		ExportTasks []struct {
			S3Bucket string `json:"S3Bucket"`
		} `json:"ExportTasks"`
	}
	parseJSON(t, descExp, &dexp)
	require.Len(t, dexp.ExportTasks, 1)
	assert.Equal(t, "exports-bucket", dexp.ExportTasks[0].S3Bucket)
	runCLI(t, awsCLI("rds", "cancel-export-task", "--export-task-identifier", exportID))

	// --- Static describes ---
	ogo := runCLI(t, awsCLI("rds", "describe-option-group-options", "--engine-name", "mysql"))
	var ogoR struct {
		OptionGroupOptions []struct {
			EngineName string `json:"EngineName"`
		} `json:"OptionGroupOptions"`
	}
	parseJSON(t, ogo, &ogoR)
	require.NotEmpty(t, ogoR.OptionGroupOptions)

	// describe-engine-default-parameters is a paginated operation; the
	// CLI's client-side pagination flattens EngineDefaults so we assert
	// on the Parameters list (the family lands on the top-level result).
	edp := runCLI(t, awsCLI("rds", "describe-engine-default-parameters",
		"--db-parameter-group-family", "mysql8.0"))
	var edpR struct {
		EngineDefaults struct {
			Parameters []any `json:"Parameters"`
		} `json:"EngineDefaults"`
	}
	parseJSON(t, edp, &edpR)
	require.NotEmpty(t, edpR.EngineDefaults.Parameters)

	runCLI(t, awsCLI("rds", "describe-engine-default-cluster-parameters",
		"--db-parameter-group-family", "aurora-mysql8.0"))

	sr := runCLI(t, awsCLI("rds", "describe-source-regions"))
	var srR struct {
		SourceRegions []any `json:"SourceRegions"`
	}
	parseJSON(t, sr, &srR)
	require.NotEmpty(t, srR.SourceRegions)
}
