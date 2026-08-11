package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRDSCLI_DBClusterLifecycle(t *testing.T) {
	id := "cli-aurora-cluster"
	out := runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", id,
		"--engine", "aurora-mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--database-name", "appdb",
		"--backup-retention-period", "5",
		"--tags", "Key=env,Value=cli"))
	var created struct {
		DBCluster struct {
			DBClusterIdentifier   string `json:"DBClusterIdentifier"`
			Engine                string `json:"Engine"`
			Status                string `json:"Status"`
			DatabaseName          string `json:"DatabaseName"`
			Port                  int    `json:"Port"`
			BackupRetentionPeriod int    `json:"BackupRetentionPeriod"`
			Endpoint              string `json:"Endpoint"`
			ReaderEndpoint        string `json:"ReaderEndpoint"`
			DBClusterArn          string `json:"DBClusterArn"`
		} `json:"DBCluster"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, id, created.DBCluster.DBClusterIdentifier)
	assert.Equal(t, "aurora-mysql", created.DBCluster.Engine)
	assert.Equal(t, "available", created.DBCluster.Status)
	assert.Equal(t, "appdb", created.DBCluster.DatabaseName)
	assert.Equal(t, 3306, created.DBCluster.Port)
	assert.Equal(t, 5, created.DBCluster.BackupRetentionPeriod)
	assert.NotEmpty(t, created.DBCluster.Endpoint)
	assert.NotEmpty(t, created.DBCluster.ReaderEndpoint)
	arn := created.DBCluster.DBClusterArn
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", id,
			"--skip-final-snapshot").Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-clusters",
		"--db-cluster-identifier", id))
	var described struct {
		DBClusters []struct {
			DBClusterIdentifier string `json:"DBClusterIdentifier"`
		} `json:"DBClusters"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.DBClusters, 1)
	assert.Equal(t, id, described.DBClusters[0].DBClusterIdentifier)

	runCLI(t, awsCLI("rds", "list-tags-for-resource",
		"--resource-name", arn))

	runCLI(t, awsCLI("rds", "modify-db-cluster",
		"--db-cluster-identifier", id,
		"--backup-retention-period", "10",
		"--apply-immediately"))
	out = runCLI(t, awsCLI("rds", "describe-db-clusters",
		"--db-cluster-identifier", id))
	var described2 struct {
		DBClusters []struct {
			BackupRetentionPeriod int `json:"BackupRetentionPeriod"`
		} `json:"DBClusters"`
	}
	parseJSON(t, out, &described2)
	require.Len(t, described2.DBClusters, 1)
	assert.Equal(t, 10, described2.DBClusters[0].BackupRetentionPeriod)

	runCLI(t, awsCLI("rds", "delete-db-cluster",
		"--db-cluster-identifier", id,
		"--skip-final-snapshot"))
}

func TestRDSCLI_RebootDBInstance(t *testing.T) {
	id := "cli-reboot-db"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", id,
			"--skip-final-snapshot").Run()
	})

	out := runCLI(t, awsCLI("rds", "reboot-db-instance",
		"--db-instance-identifier", id))
	var rebooted struct {
		DBInstance struct {
			DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &rebooted)
	assert.Equal(t, id, rebooted.DBInstance.DBInstanceIdentifier)
}

func TestRDSCLI_DBSubnetGroupLifecycle(t *testing.T) {
	name := "cli-subnet-group"
	out := runCLI(t, awsCLI("rds", "create-db-subnet-group",
		"--db-subnet-group-name", name,
		"--db-subnet-group-description", "cli subnet group",
		"--subnet-ids", "subnet-1111aaaa", "subnet-2222bbbb",
		"--tags", "Key=env,Value=cli"))
	var created struct {
		DBSubnetGroup struct {
			DBSubnetGroupName        string `json:"DBSubnetGroupName"`
			DBSubnetGroupDescription string `json:"DBSubnetGroupDescription"`
			SubnetGroupStatus        string `json:"SubnetGroupStatus"`
			DBSubnetGroupArn         string `json:"DBSubnetGroupArn"`
			Subnets                  []struct {
				SubnetIdentifier string `json:"SubnetIdentifier"`
			} `json:"Subnets"`
		} `json:"DBSubnetGroup"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, name, created.DBSubnetGroup.DBSubnetGroupName)
	assert.Equal(t, "Complete", created.DBSubnetGroup.SubnetGroupStatus)
	require.Len(t, created.DBSubnetGroup.Subnets, 2)
	assert.Equal(t, "subnet-1111aaaa", created.DBSubnetGroup.Subnets[0].SubnetIdentifier)
	arn := created.DBSubnetGroup.DBSubnetGroupArn
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-subnet-group",
			"--db-subnet-group-name", name).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-subnet-groups",
		"--db-subnet-group-name", name))
	var described struct {
		DBSubnetGroups []struct {
			DBSubnetGroupName string `json:"DBSubnetGroupName"`
		} `json:"DBSubnetGroups"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.DBSubnetGroups, 1)
	assert.Equal(t, name, described.DBSubnetGroups[0].DBSubnetGroupName)

	runCLI(t, awsCLI("rds", "modify-db-subnet-group",
		"--db-subnet-group-name", name,
		"--db-subnet-group-description", "updated",
		"--subnet-ids", "subnet-3333cccc", "subnet-4444dddd"))
	out = runCLI(t, awsCLI("rds", "describe-db-subnet-groups",
		"--db-subnet-group-name", name))
	var described2 struct {
		DBSubnetGroups []struct {
			DBSubnetGroupDescription string `json:"DBSubnetGroupDescription"`
		} `json:"DBSubnetGroups"`
	}
	parseJSON(t, out, &described2)
	require.Len(t, described2.DBSubnetGroups, 1)
	assert.Equal(t, "updated", described2.DBSubnetGroups[0].DBSubnetGroupDescription)

	runCLI(t, awsCLI("rds", "delete-db-subnet-group",
		"--db-subnet-group-name", name))
}

func TestRDSCLI_DBParameterGroupLifecycle(t *testing.T) {
	name := "cli-param-group"
	out := runCLI(t, awsCLI("rds", "create-db-parameter-group",
		"--db-parameter-group-name", name,
		"--db-parameter-group-family", "mysql8.0",
		"--description", "cli parameter group",
		"--tags", "Key=env,Value=cli"))
	var created struct {
		DBParameterGroup struct {
			DBParameterGroupName   string `json:"DBParameterGroupName"`
			DBParameterGroupFamily string `json:"DBParameterGroupFamily"`
			DBParameterGroupArn    string `json:"DBParameterGroupArn"`
		} `json:"DBParameterGroup"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, name, created.DBParameterGroup.DBParameterGroupName)
	assert.Equal(t, "mysql8.0", created.DBParameterGroup.DBParameterGroupFamily)
	require.NotEmpty(t, created.DBParameterGroup.DBParameterGroupArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-parameter-group",
			"--db-parameter-group-name", name).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-parameter-groups",
		"--db-parameter-group-name", name))
	var described struct {
		DBParameterGroups []struct {
			DBParameterGroupName string `json:"DBParameterGroupName"`
		} `json:"DBParameterGroups"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.DBParameterGroups, 1)
	assert.Equal(t, name, described.DBParameterGroups[0].DBParameterGroupName)

	runCLI(t, awsCLI("rds", "delete-db-parameter-group",
		"--db-parameter-group-name", name))
}
