package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRDSCLI_DBInstanceLifecycle(t *testing.T) {
	id := "cli-rds-db"

	out := runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20",
		"--tags", "Key=env,Value=cli"))
	var created struct {
		DBInstance struct {
			DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
			DBInstanceClass      string `json:"DBInstanceClass"`
			Engine               string `json:"Engine"`
			DBInstanceStatus     string `json:"DBInstanceStatus"`
			DBInstanceArn        string `json:"DBInstanceArn"`
			Endpoint             struct {
				Port int `json:"Port"`
			} `json:"Endpoint"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, id, created.DBInstance.DBInstanceIdentifier)
	assert.Equal(t, "db.t3.micro", created.DBInstance.DBInstanceClass)
	assert.Equal(t, "postgres", created.DBInstance.Engine)
	assert.Equal(t, "available", created.DBInstance.DBInstanceStatus)
	arn := created.DBInstance.DBInstanceArn
	require.NotEmpty(t, arn)
	assert.Positive(t, created.DBInstance.Endpoint.Port)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", id,
			"--skip-final-snapshot").Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-instances",
		"--db-instance-identifier", id))
	var described struct {
		DBInstances []struct {
			DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
			DBInstanceClass      string `json:"DBInstanceClass"`
		} `json:"DBInstances"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.DBInstances, 1)
	assert.Equal(t, id, described.DBInstances[0].DBInstanceIdentifier)
	assert.Equal(t, "db.t3.micro", described.DBInstances[0].DBInstanceClass)

	runCLI(t, awsCLI("rds", "modify-db-instance",
		"--db-instance-identifier", id,
		"--db-instance-class", "db.t3.small",
		"--apply-immediately"))

	runCLI(t, awsCLI("rds", "add-tags-to-resource",
		"--resource-name", arn,
		"--tags", "Key=phase,Value=cli"))
	out = runCLI(t, awsCLI("rds", "list-tags-for-resource",
		"--resource-name", arn))
	var tags struct {
		TagList []cliTag `json:"TagList"`
	}
	parseJSON(t, out, &tags)
	require.Contains(t, tagMap(tags.TagList), "phase")
	assert.Equal(t, "cli", tagMap(tags.TagList)["phase"])

	snapshotID := "cli-rds-snapshot"
	out = runCLI(t, awsCLI("rds", "create-db-snapshot",
		"--db-instance-identifier", id,
		"--db-snapshot-identifier", snapshotID,
		"--tags", "Key=env,Value=cli"))
	var createdSnapshot struct {
		DBSnapshot struct {
			DBSnapshotIdentifier string `json:"DBSnapshotIdentifier"`
			DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
			Status               string `json:"Status"`
			DBSnapshotArn        string `json:"DBSnapshotArn"`
		} `json:"DBSnapshot"`
	}
	parseJSON(t, out, &createdSnapshot)
	require.Equal(t, snapshotID, createdSnapshot.DBSnapshot.DBSnapshotIdentifier)
	assert.Equal(t, id, createdSnapshot.DBSnapshot.DBInstanceIdentifier)
	assert.Equal(t, "available", createdSnapshot.DBSnapshot.Status)
	require.NotEmpty(t, createdSnapshot.DBSnapshot.DBSnapshotArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-snapshot",
			"--db-snapshot-identifier", snapshotID).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-snapshots",
		"--db-snapshot-identifier", snapshotID))
	var describedSnapshots struct {
		DBSnapshots []struct {
			DBSnapshotIdentifier string `json:"DBSnapshotIdentifier"`
		} `json:"DBSnapshots"`
	}
	parseJSON(t, out, &describedSnapshots)
	require.Len(t, describedSnapshots.DBSnapshots, 1)
	assert.Equal(t, snapshotID, describedSnapshots.DBSnapshots[0].DBSnapshotIdentifier)

	out = runCLI(t, awsCLI("rds", "describe-db-snapshot-attributes",
		"--db-snapshot-identifier", snapshotID))
	var describedSnapshotAttrs struct {
		DBSnapshotAttributesResult struct {
			DBSnapshotIdentifier string `json:"DBSnapshotIdentifier"`
		} `json:"DBSnapshotAttributesResult"`
	}
	parseJSON(t, out, &describedSnapshotAttrs)
	assert.Equal(t, snapshotID, describedSnapshotAttrs.DBSnapshotAttributesResult.DBSnapshotIdentifier)

	restoredID := "cli-rds-restored"
	out = runCLI(t, awsCLI("rds", "restore-db-instance-from-db-snapshot",
		"--db-instance-identifier", restoredID,
		"--db-snapshot-identifier", snapshotID,
		"--db-instance-class", "db.t3.micro",
		"--tags", "Key=env,Value=cli"))
	var restored struct {
		DBInstance struct {
			DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
			Engine               string `json:"Engine"`
			DBInstanceStatus     string `json:"DBInstanceStatus"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &restored)
	require.Equal(t, restoredID, restored.DBInstance.DBInstanceIdentifier)
	assert.Equal(t, "postgres", restored.DBInstance.Engine)
	assert.Equal(t, "available", restored.DBInstance.DBInstanceStatus)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", restoredID,
			"--skip-final-snapshot").Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-instances",
		"--db-instance-identifier", restoredID))
	var describedRestored struct {
		DBInstances []struct {
			DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
			Engine               string `json:"Engine"`
		} `json:"DBInstances"`
	}
	parseJSON(t, out, &describedRestored)
	require.Len(t, describedRestored.DBInstances, 1)
	assert.Equal(t, restoredID, describedRestored.DBInstances[0].DBInstanceIdentifier)
	assert.Equal(t, "postgres", describedRestored.DBInstances[0].Engine)
}

func TestElastiCacheCLI_ClusterLifecycle(t *testing.T) {
	id := "cli-cache"

	out := runCLI(t, awsCLI("elasticache", "create-cache-cluster",
		"--cache-cluster-id", id,
		"--cache-node-type", "cache.t3.micro",
		"--engine", "redis",
		"--num-cache-nodes", "1",
		"--tags", "Key=env,Value=cli"))
	var created struct {
		CacheCluster struct {
			CacheClusterId     string `json:"CacheClusterId"`
			CacheNodeType      string `json:"CacheNodeType"`
			Engine             string `json:"Engine"`
			CacheClusterStatus string `json:"CacheClusterStatus"`
			ARN                string `json:"ARN"`
		} `json:"CacheCluster"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, id, created.CacheCluster.CacheClusterId)
	assert.Equal(t, "cache.t3.micro", created.CacheCluster.CacheNodeType)
	assert.Equal(t, "redis", created.CacheCluster.Engine)
	assert.Equal(t, "available", created.CacheCluster.CacheClusterStatus)
	arn := created.CacheCluster.ARN
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-cluster",
			"--cache-cluster-id", id).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "describe-cache-clusters",
		"--cache-cluster-id", id,
		"--show-cache-node-info"))
	var described struct {
		CacheClusters []struct {
			CacheClusterId string `json:"CacheClusterId"`
			CacheNodeType  string `json:"CacheNodeType"`
		} `json:"CacheClusters"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.CacheClusters, 1)
	assert.Equal(t, id, described.CacheClusters[0].CacheClusterId)
	assert.Equal(t, "cache.t3.micro", described.CacheClusters[0].CacheNodeType)

	runCLI(t, awsCLI("elasticache", "modify-cache-cluster",
		"--cache-cluster-id", id,
		"--cache-node-type", "cache.t3.small",
		"--apply-immediately"))

	runCLI(t, awsCLI("elasticache", "add-tags-to-resource",
		"--resource-name", arn,
		"--tags", "Key=phase,Value=cli"))
	out = runCLI(t, awsCLI("elasticache", "list-tags-for-resource",
		"--resource-name", arn))
	var tags struct {
		TagList []cliTag `json:"TagList"`
	}
	parseJSON(t, out, &tags)
	require.Contains(t, tagMap(tags.TagList), "phase")
	assert.Equal(t, "cli", tagMap(tags.TagList)["phase"])
}

func TestElastiCacheCLI_ReplicationGroupAndReboot(t *testing.T) {
	// Reboot a standalone cluster.
	clusterID := "cli-reboot-cache"
	runCLI(t, awsCLI("elasticache", "create-cache-cluster",
		"--cache-cluster-id", clusterID,
		"--cache-node-type", "cache.t3.micro",
		"--engine", "redis",
		"--num-cache-nodes", "1"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-cluster",
			"--cache-cluster-id", clusterID).Run()
	})
	out := runCLI(t, awsCLI("elasticache", "reboot-cache-cluster",
		"--cache-cluster-id", clusterID,
		"--cache-node-ids-to-reboot", "0001"))
	var rebooted struct {
		CacheCluster struct {
			CacheClusterStatus string `json:"CacheClusterStatus"`
		} `json:"CacheCluster"`
	}
	parseJSON(t, out, &rebooted)
	assert.Equal(t, "rebooting cache cluster nodes", rebooted.CacheCluster.CacheClusterStatus)

	// Replication group lifecycle.
	rgID := "cli-repl-group"
	out = runCLI(t, awsCLI("elasticache", "create-replication-group",
		"--replication-group-id", rgID,
		"--replication-group-description", "cli repl group",
		"--cache-node-type", "cache.t3.micro",
		"--engine", "redis",
		"--num-cache-clusters", "2",
		"--automatic-failover-enabled"))
	var rgCreated struct {
		ReplicationGroup struct {
			ReplicationGroupId string   `json:"ReplicationGroupId"`
			Status             string   `json:"Status"`
			AutomaticFailover  string   `json:"AutomaticFailover"`
			MemberClusters     []string `json:"MemberClusters"`
			ARN                string   `json:"ARN"`
		} `json:"ReplicationGroup"`
	}
	parseJSON(t, out, &rgCreated)
	require.Equal(t, rgID, rgCreated.ReplicationGroup.ReplicationGroupId)
	assert.Equal(t, "available", rgCreated.ReplicationGroup.Status)
	assert.Equal(t, "enabled", rgCreated.ReplicationGroup.AutomaticFailover)
	require.Len(t, rgCreated.ReplicationGroup.MemberClusters, 2)
	rgARN := rgCreated.ReplicationGroup.ARN
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-replication-group",
			"--replication-group-id", rgID).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "describe-replication-groups",
		"--replication-group-id", rgID))
	var rgDesc struct {
		ReplicationGroups []struct {
			Description string `json:"Description"`
		} `json:"ReplicationGroups"`
	}
	parseJSON(t, out, &rgDesc)
	require.Len(t, rgDesc.ReplicationGroups, 1)
	assert.Equal(t, "cli repl group", rgDesc.ReplicationGroups[0].Description)

	runCLI(t, awsCLI("elasticache", "modify-replication-group",
		"--replication-group-id", rgID,
		"--replication-group-description", "modified",
		"--apply-immediately"))

	runCLI(t, awsCLI("elasticache", "add-tags-to-resource",
		"--resource-name", rgARN,
		"--tags", "Key=tier,Value=cache"))
	out = runCLI(t, awsCLI("elasticache", "list-tags-for-resource",
		"--resource-name", rgARN))
	var rgTags struct {
		TagList []cliTag `json:"TagList"`
	}
	parseJSON(t, out, &rgTags)
	assert.Equal(t, "cache", tagMap(rgTags.TagList)["tier"])

	runCLI(t, awsCLI("elasticache", "remove-tags-from-resource",
		"--resource-name", rgARN,
		"--tag-keys", "tier"))

	runCLI(t, awsCLI("elasticache", "delete-replication-group",
		"--replication-group-id", rgID))
}

func TestElastiCacheCLI_SubnetAndParameterGroups(t *testing.T) {
	// Cache subnet group lifecycle.
	sgName := "cli-cache-subnet-group"
	out := runCLI(t, awsCLI("elasticache", "create-cache-subnet-group",
		"--cache-subnet-group-name", sgName,
		"--cache-subnet-group-description", "cli subnet group",
		"--subnet-ids", "subnet-aaaa1111", "subnet-bbbb2222"))
	var sgCreated struct {
		CacheSubnetGroup struct {
			CacheSubnetGroupName string `json:"CacheSubnetGroupName"`
			Subnets              []struct {
				SubnetIdentifier string `json:"SubnetIdentifier"`
			} `json:"Subnets"`
		} `json:"CacheSubnetGroup"`
	}
	parseJSON(t, out, &sgCreated)
	require.Equal(t, sgName, sgCreated.CacheSubnetGroup.CacheSubnetGroupName)
	require.Len(t, sgCreated.CacheSubnetGroup.Subnets, 2)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-subnet-group",
			"--cache-subnet-group-name", sgName).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "describe-cache-subnet-groups",
		"--cache-subnet-group-name", sgName))
	var sgDesc struct {
		CacheSubnetGroups []struct {
			CacheSubnetGroupDescription string `json:"CacheSubnetGroupDescription"`
		} `json:"CacheSubnetGroups"`
	}
	parseJSON(t, out, &sgDesc)
	require.Len(t, sgDesc.CacheSubnetGroups, 1)
	assert.Equal(t, "cli subnet group", sgDesc.CacheSubnetGroups[0].CacheSubnetGroupDescription)

	runCLI(t, awsCLI("elasticache", "modify-cache-subnet-group",
		"--cache-subnet-group-name", sgName,
		"--cache-subnet-group-description", "modified subnet"))

	runCLI(t, awsCLI("elasticache", "delete-cache-subnet-group",
		"--cache-subnet-group-name", sgName))

	// Cache parameter group lifecycle.
	pgName := "cli-cache-param-group"
	out = runCLI(t, awsCLI("elasticache", "create-cache-parameter-group",
		"--cache-parameter-group-name", pgName,
		"--cache-parameter-group-family", "redis7",
		"--description", "cli param group"))
	var pgCreated struct {
		CacheParameterGroup struct {
			CacheParameterGroupName   string `json:"CacheParameterGroupName"`
			CacheParameterGroupFamily string `json:"CacheParameterGroupFamily"`
		} `json:"CacheParameterGroup"`
	}
	parseJSON(t, out, &pgCreated)
	require.Equal(t, pgName, pgCreated.CacheParameterGroup.CacheParameterGroupName)
	assert.Equal(t, "redis7", pgCreated.CacheParameterGroup.CacheParameterGroupFamily)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-parameter-group",
			"--cache-parameter-group-name", pgName).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "describe-cache-parameter-groups",
		"--cache-parameter-group-name", pgName))
	var pgDesc struct {
		CacheParameterGroups []struct {
			Description string `json:"Description"`
		} `json:"CacheParameterGroups"`
	}
	parseJSON(t, out, &pgDesc)
	require.Len(t, pgDesc.CacheParameterGroups, 1)
	assert.Equal(t, "cli param group", pgDesc.CacheParameterGroups[0].Description)

	runCLI(t, awsCLI("elasticache", "delete-cache-parameter-group",
		"--cache-parameter-group-name", pgName))
}

type cliTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

func tagMap(tags []cliTag) map[string]string {
	out := map[string]string{}
	for _, tag := range tags {
		out[tag.Key] = tag.Value
	}
	return out
}
