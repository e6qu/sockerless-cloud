package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSCLI_ClusterSnapshotAndParamGroup covers the DB cluster
// snapshot and DB cluster parameter group families end-to-end through
// the aws CLI. Grouped into one round-trip func (appdata2 carries RDS).
func TestRDSCLI_ClusterSnapshotAndParamGroup(t *testing.T) {
	clusterID := "cli-ext-aurora-cluster"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--engine", "aurora-mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", clusterID,
			"--skip-final-snapshot").Run()
	})

	// --- DB cluster snapshot ---
	snapID := "cli-ext-cluster-snap"
	out := runCLI(t, awsCLI("rds", "create-db-cluster-snapshot",
		"--db-cluster-snapshot-identifier", snapID,
		"--db-cluster-identifier", clusterID,
		"--tags", "Key=team,Value=db"))
	var created struct {
		DBClusterSnapshot struct {
			DBClusterSnapshotIdentifier string `json:"DBClusterSnapshotIdentifier"`
			DBClusterIdentifier         string `json:"DBClusterIdentifier"`
			Status                      string `json:"Status"`
			Engine                      string `json:"Engine"`
			SnapshotType                string `json:"SnapshotType"`
			DBClusterSnapshotArn        string `json:"DBClusterSnapshotArn"`
		} `json:"DBClusterSnapshot"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, snapID, created.DBClusterSnapshot.DBClusterSnapshotIdentifier)
	assert.Equal(t, clusterID, created.DBClusterSnapshot.DBClusterIdentifier)
	assert.Equal(t, "available", created.DBClusterSnapshot.Status)
	assert.Equal(t, "aurora-mysql", created.DBClusterSnapshot.Engine)
	assert.Equal(t, "manual", created.DBClusterSnapshot.SnapshotType)
	csArn := created.DBClusterSnapshot.DBClusterSnapshotArn
	require.NotEmpty(t, csArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-snapshot",
			"--db-cluster-snapshot-identifier", snapID).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-cluster-snapshots",
		"--db-cluster-snapshot-identifier", snapID))
	var described struct {
		DBClusterSnapshots []struct {
			DBClusterSnapshotIdentifier string `json:"DBClusterSnapshotIdentifier"`
		} `json:"DBClusterSnapshots"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.DBClusterSnapshots, 1)

	out = runCLI(t, awsCLI("rds", "list-tags-for-resource",
		"--resource-name", csArn))
	var tagsCS struct {
		TagList []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"TagList"`
	}
	parseJSON(t, out, &tagsCS)
	require.Len(t, tagsCS.TagList, 1)
	assert.Equal(t, "team", tagsCS.TagList[0].Key)

	runCLI(t, awsCLI("rds", "delete-db-cluster-snapshot",
		"--db-cluster-snapshot-identifier", snapID))

	// --- DB cluster parameter group ---
	pgName := "cli-ext-cluster-pg"
	out = runCLI(t, awsCLI("rds", "create-db-cluster-parameter-group",
		"--db-cluster-parameter-group-name", pgName,
		"--db-parameter-group-family", "aurora-mysql8.0",
		"--description", "cli ext cluster pg",
		"--tags", "Key=env,Value=cli"))
	var createdPG struct {
		DBClusterParameterGroup struct {
			DBClusterParameterGroupName string `json:"DBClusterParameterGroupName"`
			DBParameterGroupFamily      string `json:"DBParameterGroupFamily"`
			DBClusterParameterGroupArn  string `json:"DBClusterParameterGroupArn"`
		} `json:"DBClusterParameterGroup"`
	}
	parseJSON(t, out, &createdPG)
	require.Equal(t, pgName, createdPG.DBClusterParameterGroup.DBClusterParameterGroupName)
	assert.Equal(t, "aurora-mysql8.0", createdPG.DBClusterParameterGroup.DBParameterGroupFamily)
	cpgArn := createdPG.DBClusterParameterGroup.DBClusterParameterGroupArn
	require.NotEmpty(t, cpgArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-parameter-group",
			"--db-cluster-parameter-group-name", pgName).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-cluster-parameter-groups",
		"--db-cluster-parameter-group-name", pgName))
	var describedPG struct {
		DBClusterParameterGroups []struct {
			DBClusterParameterGroupName string `json:"DBClusterParameterGroupName"`
		} `json:"DBClusterParameterGroups"`
	}
	parseJSON(t, out, &describedPG)
	require.Len(t, describedPG.DBClusterParameterGroups, 1)

	runCLI(t, awsCLI("rds", "list-tags-for-resource", "--resource-name", cpgArn))
	runCLI(t, awsCLI("rds", "delete-db-cluster-parameter-group",
		"--db-cluster-parameter-group-name", pgName))
}

// TestRDSCLI_OptionGroupAndReplica covers option groups,
// read replicas, and CopyDBSnapshot through the aws CLI.
func TestRDSCLI_OptionGroupAndReplica(t *testing.T) {
	// --- Option group ---
	ogName := "cli-ext-option-group"
	out := runCLI(t, awsCLI("rds", "create-option-group",
		"--option-group-name", ogName,
		"--engine-name", "mysql",
		"--major-engine-version", "8.0",
		"--option-group-description", "cli ext option group",
		"--tags", "Key=env,Value=cli"))
	var createdOG struct {
		OptionGroup struct {
			OptionGroupName    string `json:"OptionGroupName"`
			EngineName         string `json:"EngineName"`
			MajorEngineVersion string `json:"MajorEngineVersion"`
			OptionGroupArn     string `json:"OptionGroupArn"`
		} `json:"OptionGroup"`
	}
	parseJSON(t, out, &createdOG)
	require.Equal(t, ogName, createdOG.OptionGroup.OptionGroupName)
	assert.Equal(t, "mysql", createdOG.OptionGroup.EngineName)
	assert.Equal(t, "8.0", createdOG.OptionGroup.MajorEngineVersion)
	ogArn := createdOG.OptionGroup.OptionGroupArn
	require.NotEmpty(t, ogArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-option-group",
			"--option-group-name", ogName).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-option-groups",
		"--option-group-name", ogName))
	var describedOG struct {
		OptionGroupsList []struct {
			OptionGroupName string `json:"OptionGroupName"`
		} `json:"OptionGroupsList"`
	}
	parseJSON(t, out, &describedOG)
	require.Len(t, describedOG.OptionGroupsList, 1)

	runCLI(t, awsCLI("rds", "list-tags-for-resource", "--resource-name", ogArn))
	runCLI(t, awsCLI("rds", "delete-option-group", "--option-group-name", ogName))

	// --- Read replica + CopyDBSnapshot ---
	srcID := "cli-ext-replica-src"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", srcID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", srcID,
			"--skip-final-snapshot").Run()
	})

	replicaID := "cli-ext-replica"
	out = runCLI(t, awsCLI("rds", "create-db-instance-read-replica",
		"--db-instance-identifier", replicaID,
		"--source-db-instance-identifier", srcID))
	var replica struct {
		DBInstance struct {
			DBInstanceIdentifier                  string `json:"DBInstanceIdentifier"`
			ReadReplicaSourceDBInstanceIdentifier string `json:"ReadReplicaSourceDBInstanceIdentifier"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &replica)
	require.Equal(t, replicaID, replica.DBInstance.DBInstanceIdentifier)
	assert.Equal(t, srcID, replica.DBInstance.ReadReplicaSourceDBInstanceIdentifier)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", replicaID,
			"--skip-final-snapshot").Run()
	})

	// CopyDBSnapshot.
	srcSnap := "cli-ext-copy-src"
	runCLI(t, awsCLI("rds", "create-db-snapshot",
		"--db-snapshot-identifier", srcSnap,
		"--db-instance-identifier", srcID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-snapshot",
			"--db-snapshot-identifier", srcSnap).Run()
	})

	dstSnap := "cli-ext-copy-dst"
	out = runCLI(t, awsCLI("rds", "copy-db-snapshot",
		"--source-db-snapshot-identifier", srcSnap,
		"--target-db-snapshot-identifier", dstSnap))
	var copied struct {
		DBSnapshot struct {
			DBSnapshotIdentifier       string `json:"DBSnapshotIdentifier"`
			Status                     string `json:"Status"`
			SourceDBSnapshotIdentifier string `json:"SourceDBSnapshotIdentifier"`
		} `json:"DBSnapshot"`
	}
	parseJSON(t, out, &copied)
	require.Equal(t, dstSnap, copied.DBSnapshot.DBSnapshotIdentifier)
	assert.Equal(t, "available", copied.DBSnapshot.Status)
	assert.NotEmpty(t, copied.DBSnapshot.SourceDBSnapshotIdentifier)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-snapshot",
			"--db-snapshot-identifier", dstSnap).Run()
	})
}

// TestRDSCLI_EventsAndEngineMetadata covers DescribeEvents,
// DescribeEventCategories, DescribeDBEngineVersions, and
// DescribeOrderableDBInstanceOptions.
func TestRDSCLI_EventsAndEngineMetadata(t *testing.T) {
	out := runCLI(t, awsCLI("rds", "describe-event-categories"))
	var cats struct {
		EventCategoriesMapList []struct {
			SourceType      string   `json:"SourceType"`
			EventCategories []string `json:"EventCategories"`
		} `json:"EventCategoriesMapList"`
	}
	parseJSON(t, out, &cats)
	require.NotEmpty(t, cats.EventCategoriesMapList)

	out = runCLI(t, awsCLI("rds", "describe-events"))
	var events struct {
		Events []map[string]any `json:"Events"`
	}
	parseJSON(t, out, &events) // empty list is valid; shape must parse.

	out = runCLI(t, awsCLI("rds", "describe-db-engine-versions",
		"--engine", "postgres"))
	var versions struct {
		DBEngineVersions []struct {
			Engine                 string `json:"Engine"`
			EngineVersion          string `json:"EngineVersion"`
			DBParameterGroupFamily string `json:"DBParameterGroupFamily"`
		} `json:"DBEngineVersions"`
	}
	parseJSON(t, out, &versions)
	require.NotEmpty(t, versions.DBEngineVersions)
	assert.Equal(t, "postgres", versions.DBEngineVersions[0].Engine)

	out = runCLI(t, awsCLI("rds", "describe-orderable-db-instance-options",
		"--engine", "mysql",
		"--db-instance-class", "db.t3.micro"))
	var orderable struct {
		OrderableDBInstanceOptions []struct {
			Engine          string `json:"Engine"`
			DBInstanceClass string `json:"DBInstanceClass"`
		} `json:"OrderableDBInstanceOptions"`
	}
	parseJSON(t, out, &orderable)
	require.NotEmpty(t, orderable.OrderableDBInstanceOptions)
	assert.Equal(t, "mysql", orderable.OrderableDBInstanceOptions[0].Engine)
}
