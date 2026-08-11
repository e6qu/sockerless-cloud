package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCacheCLI_Serverless exercises serverless caches and
// serverless cache snapshots through the aws CLI. Grouped to keep the
// appdata shard lean.
func TestElastiCacheCLI_Serverless(t *testing.T) {
	cacheName := "cli-sl-cache"
	out := runCLI(t, awsCLI("elasticache", "create-serverless-cache",
		"--serverless-cache-name", cacheName,
		"--engine", "redis",
		"--major-engine-version", "7",
		"--cache-usage-limits", "DataStorage={Maximum=10,Unit=GB},ECPUPerSecond={Maximum=5000}"))
	var created struct {
		ServerlessCache struct {
			ServerlessCacheName string `json:"ServerlessCacheName"`
			Status              string `json:"Status"`
			Engine              string `json:"Engine"`
			ARN                 string `json:"ARN"`
			Endpoint            struct {
				Port int `json:"Port"`
			} `json:"Endpoint"`
		} `json:"ServerlessCache"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, cacheName, created.ServerlessCache.ServerlessCacheName)
	assert.Equal(t, "available", created.ServerlessCache.Status)
	assert.Equal(t, "redis", created.ServerlessCache.Engine)
	assert.Equal(t, 6379, created.ServerlessCache.Endpoint.Port)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-serverless-cache",
			"--serverless-cache-name", cacheName).Run()
	})

	// DescribeServerlessCaches.
	out = runCLI(t, awsCLI("elasticache", "describe-serverless-caches",
		"--serverless-cache-name", cacheName))
	var listed struct {
		ServerlessCaches []struct {
			ServerlessCacheName string `json:"ServerlessCacheName"`
		} `json:"ServerlessCaches"`
	}
	parseJSON(t, out, &listed)
	require.Len(t, listed.ServerlessCaches, 1)

	// ModifyServerlessCache.
	out = runCLI(t, awsCLI("elasticache", "modify-serverless-cache",
		"--serverless-cache-name", cacheName,
		"--description", "modified"))
	var modified struct {
		ServerlessCache struct {
			Description string `json:"Description"`
		} `json:"ServerlessCache"`
	}
	parseJSON(t, out, &modified)
	assert.Equal(t, "modified", modified.ServerlessCache.Description)

	// CreateServerlessCacheSnapshot.
	snapName := "cli-sl-snap"
	out = runCLI(t, awsCLI("elasticache", "create-serverless-cache-snapshot",
		"--serverless-cache-snapshot-name", snapName,
		"--serverless-cache-name", cacheName))
	var snap struct {
		ServerlessCacheSnapshot struct {
			ServerlessCacheSnapshotName string `json:"ServerlessCacheSnapshotName"`
			SnapshotType                string `json:"SnapshotType"`
		} `json:"ServerlessCacheSnapshot"`
	}
	parseJSON(t, out, &snap)
	require.Equal(t, snapName, snap.ServerlessCacheSnapshot.ServerlessCacheSnapshotName)
	assert.Equal(t, "manual", snap.ServerlessCacheSnapshot.SnapshotType)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-serverless-cache-snapshot",
			"--serverless-cache-snapshot-name", snapName).Run()
	})

	// DescribeServerlessCacheSnapshots.
	out = runCLI(t, awsCLI("elasticache", "describe-serverless-cache-snapshots",
		"--serverless-cache-snapshot-name", snapName))
	var snaps struct {
		ServerlessCacheSnapshots []struct {
			ServerlessCacheSnapshotName string `json:"ServerlessCacheSnapshotName"`
		} `json:"ServerlessCacheSnapshots"`
	}
	parseJSON(t, out, &snaps)
	require.Len(t, snaps.ServerlessCacheSnapshots, 1)

	// CopyServerlessCacheSnapshot.
	copyName := "cli-sl-snap-copy"
	runCLI(t, awsCLI("elasticache", "copy-serverless-cache-snapshot",
		"--source-serverless-cache-snapshot-name", snapName,
		"--target-serverless-cache-snapshot-name", copyName))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-serverless-cache-snapshot",
			"--serverless-cache-snapshot-name", copyName).Run()
	})

	// ExportServerlessCacheSnapshot.
	runCLI(t, awsCLI("elasticache", "export-serverless-cache-snapshot",
		"--serverless-cache-snapshot-name", snapName,
		"--s3-bucket-name", "cli-export-bucket"))

	// DeleteServerlessCacheSnapshot.
	runCLI(t, awsCLI("elasticache", "delete-serverless-cache-snapshot",
		"--serverless-cache-snapshot-name", snapName))
}

// TestElastiCacheCLI_GlobalReplicationGroup exercises the Global
// Datastore control-plane operations through the aws CLI.
func TestElastiCacheCLI_GlobalReplicationGroup(t *testing.T) {
	primary := "cli-grg-primary"
	runCLI(t, awsCLI("elasticache", "create-replication-group",
		"--replication-group-id", primary,
		"--replication-group-description", "cli global primary",
		"--engine", "redis",
		"--cache-node-type", "cache.r7g.large",
		"--num-cache-clusters", "2"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-replication-group",
			"--replication-group-id", primary).Run()
	})

	out := runCLI(t, awsCLI("elasticache", "create-global-replication-group",
		"--global-replication-group-id-suffix", "cliglobal",
		"--primary-replication-group-id", primary,
		"--global-replication-group-description", "cli global"))
	var created struct {
		GlobalReplicationGroup struct {
			GlobalReplicationGroupId string `json:"GlobalReplicationGroupId"`
			Members                  []struct {
				Role string `json:"Role"`
			} `json:"Members"`
		} `json:"GlobalReplicationGroup"`
	}
	parseJSON(t, out, &created)
	gid := created.GlobalReplicationGroup.GlobalReplicationGroupId
	require.NotEmpty(t, gid)
	require.Len(t, created.GlobalReplicationGroup.Members, 1)
	assert.Equal(t, "PRIMARY", created.GlobalReplicationGroup.Members[0].Role)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-global-replication-group",
			"--global-replication-group-id", gid,
			"--retain-primary-replication-group").Run()
	})

	// DescribeGlobalReplicationGroups.
	out = runCLI(t, awsCLI("elasticache", "describe-global-replication-groups",
		"--global-replication-group-id", gid))
	var listed struct {
		GlobalReplicationGroups []struct {
			GlobalReplicationGroupId string `json:"GlobalReplicationGroupId"`
		} `json:"GlobalReplicationGroups"`
	}
	parseJSON(t, out, &listed)
	require.Len(t, listed.GlobalReplicationGroups, 1)

	// ModifyGlobalReplicationGroup.
	runCLI(t, awsCLI("elasticache", "modify-global-replication-group",
		"--global-replication-group-id", gid,
		"--apply-immediately",
		"--global-replication-group-description", "cli updated"))

	// IncreaseNodeGroupsInGlobalReplicationGroup.
	out = runCLI(t, awsCLI("elasticache", "increase-node-groups-in-global-replication-group",
		"--global-replication-group-id", gid,
		"--node-group-count", "2",
		"--apply-immediately"))
	var inc struct {
		GlobalReplicationGroup struct {
			GlobalNodeGroups []struct {
				GlobalNodeGroupId string `json:"GlobalNodeGroupId"`
			} `json:"GlobalNodeGroups"`
		} `json:"GlobalReplicationGroup"`
	}
	parseJSON(t, out, &inc)
	assert.Len(t, inc.GlobalReplicationGroup.GlobalNodeGroups, 2)

	// DecreaseNodeGroupsInGlobalReplicationGroup.
	runCLI(t, awsCLI("elasticache", "decrease-node-groups-in-global-replication-group",
		"--global-replication-group-id", gid,
		"--node-group-count", "1",
		"--apply-immediately"))

	// RebalanceSlotsInGlobalReplicationGroup.
	runCLI(t, awsCLI("elasticache", "rebalance-slots-in-global-replication-group",
		"--global-replication-group-id", gid,
		"--apply-immediately"))

	// FailoverGlobalReplicationGroup.
	runCLI(t, awsCLI("elasticache", "failover-global-replication-group",
		"--global-replication-group-id", gid,
		"--primary-region", "us-east-1",
		"--primary-replication-group-id", primary))

	// DisassociateGlobalReplicationGroup.
	runCLI(t, awsCLI("elasticache", "disassociate-global-replication-group",
		"--global-replication-group-id", gid,
		"--replication-group-id", primary,
		"--replication-group-region", "us-east-1"))
}

// TestElastiCacheCLI_SecurityGroupsAndUpdates exercises cache security
// groups and service-update actions through the aws CLI.
func TestElastiCacheCLI_SecurityGroupsAndUpdates(t *testing.T) {
	// Cache security group.
	csgName := "cli-csg"
	out := runCLI(t, awsCLI("elasticache", "create-cache-security-group",
		"--cache-security-group-name", csgName,
		"--description", "cli classic sg"))
	var created struct {
		CacheSecurityGroup struct {
			CacheSecurityGroupName string `json:"CacheSecurityGroupName"`
		} `json:"CacheSecurityGroup"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, csgName, created.CacheSecurityGroup.CacheSecurityGroupName)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-security-group",
			"--cache-security-group-name", csgName).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "authorize-cache-security-group-ingress",
		"--cache-security-group-name", csgName,
		"--ec2-security-group-name", "ec2-sg-cli",
		"--ec2-security-group-owner-id", "123456789012"))
	var auth struct {
		CacheSecurityGroup struct {
			EC2SecurityGroups []struct {
				EC2SecurityGroupName string `json:"EC2SecurityGroupName"`
			} `json:"EC2SecurityGroups"`
		} `json:"CacheSecurityGroup"`
	}
	parseJSON(t, out, &auth)
	require.Len(t, auth.CacheSecurityGroup.EC2SecurityGroups, 1)

	runCLI(t, awsCLI("elasticache", "revoke-cache-security-group-ingress",
		"--cache-security-group-name", csgName,
		"--ec2-security-group-name", "ec2-sg-cli",
		"--ec2-security-group-owner-id", "123456789012"))

	runCLI(t, awsCLI("elasticache", "delete-cache-security-group",
		"--cache-security-group-name", csgName))

	// Update actions on a real cluster.
	clusterID := "cli-ua-cluster"
	runCLI(t, awsCLI("elasticache", "create-cache-cluster",
		"--cache-cluster-id", clusterID,
		"--cache-node-type", "cache.t3.micro",
		"--engine", "redis",
		"--num-cache-nodes", "1"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-cluster",
			"--cache-cluster-id", clusterID).Run()
	})

	svcUpdate := "elasticache-20240101-001"
	out = runCLI(t, awsCLI("elasticache", "describe-update-actions",
		"--cache-cluster-ids", clusterID,
		"--service-update-name", svcUpdate))
	var ua struct {
		UpdateActions []struct {
			CacheClusterId string `json:"CacheClusterId"`
		} `json:"UpdateActions"`
	}
	parseJSON(t, out, &ua)
	require.Len(t, ua.UpdateActions, 1)
	assert.Equal(t, clusterID, ua.UpdateActions[0].CacheClusterId)

	out = runCLI(t, awsCLI("elasticache", "batch-apply-update-action",
		"--cache-cluster-ids", clusterID,
		"--service-update-name", svcUpdate))
	var applied struct {
		ProcessedUpdateActions []struct {
			CacheClusterId string `json:"CacheClusterId"`
		} `json:"ProcessedUpdateActions"`
	}
	parseJSON(t, out, &applied)
	require.Len(t, applied.ProcessedUpdateActions, 1)

	runCLI(t, awsCLI("elasticache", "batch-stop-update-action",
		"--cache-cluster-ids", clusterID,
		"--service-update-name", svcUpdate))
}

// TestElastiCacheCLI_ReplicaShardAndReserved exercises replica/shard
// reconfiguration, online migration, node-type modifications, and
// reserved-node purchase through the aws CLI.
func TestElastiCacheCLI_ReplicaShardAndReserved(t *testing.T) {
	rgID := "cli-rsc-group"
	runCLI(t, awsCLI("elasticache", "create-replication-group",
		"--replication-group-id", rgID,
		"--replication-group-description", "cli replica/shard",
		"--engine", "redis",
		"--cache-node-type", "cache.r7g.large",
		"--num-cache-clusters", "2",
		"--automatic-failover-enabled"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-replication-group",
			"--replication-group-id", rgID).Run()
	})

	out := runCLI(t, awsCLI("elasticache", "increase-replica-count",
		"--replication-group-id", rgID,
		"--new-replica-count", "2",
		"--apply-immediately"))
	var inc struct {
		ReplicationGroup struct {
			MemberClusters []string `json:"MemberClusters"`
		} `json:"ReplicationGroup"`
	}
	parseJSON(t, out, &inc)
	assert.Len(t, inc.ReplicationGroup.MemberClusters, 3)

	runCLI(t, awsCLI("elasticache", "decrease-replica-count",
		"--replication-group-id", rgID,
		"--new-replica-count", "1",
		"--apply-immediately"))

	out = runCLI(t, awsCLI("elasticache", "modify-replication-group-shard-configuration",
		"--replication-group-id", rgID,
		"--node-group-count", "2",
		"--apply-immediately"))
	var shard struct {
		ReplicationGroup struct {
			ClusterEnabled bool `json:"ClusterEnabled"`
		} `json:"ReplicationGroup"`
	}
	parseJSON(t, out, &shard)
	assert.True(t, shard.ReplicationGroup.ClusterEnabled)

	runCLI(t, awsCLI("elasticache", "test-failover",
		"--replication-group-id", rgID,
		"--node-group-id", "0001"))

	// Online migration.
	runCLI(t, awsCLI("elasticache", "start-migration",
		"--replication-group-id", rgID,
		"--customer-node-endpoint-list", "Address=10.0.0.1,Port=6379"))
	runCLI(t, awsCLI("elasticache", "test-migration",
		"--replication-group-id", rgID,
		"--customer-node-endpoint-list", "Address=10.0.0.1,Port=6379"))
	out = runCLI(t, awsCLI("elasticache", "complete-migration",
		"--replication-group-id", rgID))
	var cm struct {
		ReplicationGroup struct {
			Status string `json:"Status"`
		} `json:"ReplicationGroup"`
	}
	parseJSON(t, out, &cm)
	assert.Equal(t, "available", cm.ReplicationGroup.Status)

	// Node-type modifications.
	out = runCLI(t, awsCLI("elasticache", "list-allowed-node-type-modifications"))
	var allowed struct {
		ScaleUpModifications   []string `json:"ScaleUpModifications"`
		ScaleDownModifications []string `json:"ScaleDownModifications"`
	}
	parseJSON(t, out, &allowed)
	assert.NotEmpty(t, allowed.ScaleUpModifications)
	assert.NotEmpty(t, allowed.ScaleDownModifications)

	// Reserved-node purchase.
	out = runCLI(t, awsCLI("elasticache", "purchase-reserved-cache-nodes-offering",
		"--reserved-cache-nodes-offering-id", "649fd0c8-cf6d-47a0-bfa6-060f8e75e95f",
		"--reserved-cache-node-id", "cli-reservation",
		"--cache-node-count", "1"))
	var rcn struct {
		ReservedCacheNode struct {
			ReservedCacheNodeId string `json:"ReservedCacheNodeId"`
			CacheNodeCount      int    `json:"CacheNodeCount"`
		} `json:"ReservedCacheNode"`
	}
	parseJSON(t, out, &rcn)
	assert.Equal(t, "cli-reservation", rcn.ReservedCacheNode.ReservedCacheNodeId)
	assert.Equal(t, 1, rcn.ReservedCacheNode.CacheNodeCount)
}
