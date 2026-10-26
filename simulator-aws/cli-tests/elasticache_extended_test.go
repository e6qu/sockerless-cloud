package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCacheCLI_SnapshotsUsersGroups covers snapshots, users and user
// groups in one test to keep the appdata shard lean.
func TestElastiCacheCLI_SnapshotsUsersGroups(t *testing.T) {
	t.Parallel()
	clusterID := "cli-snap-cluster"
	runCLI(t, awsCLI("elasticache", "create-cache-cluster",
		"--cache-cluster-id", clusterID,
		"--cache-node-type", "cache.t3.micro",
		"--engine", "redis",
		"--num-cache-nodes", "1"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-cluster",
			"--cache-cluster-id", clusterID).Run()
	})

	snapName := "cli-snap"
	out := runCLI(t, awsCLI("elasticache", "create-snapshot",
		"--snapshot-name", snapName,
		"--cache-cluster-id", clusterID))
	var created struct {
		Snapshot struct {
			SnapshotName   string `json:"SnapshotName"`
			CacheClusterId string `json:"CacheClusterId"`
			SnapshotSource string `json:"SnapshotSource"`
			ARN            string `json:"ARN"`
		} `json:"Snapshot"`
	}
	parseJSON(t, out, &created)
	require.Equal(t, snapName, created.Snapshot.SnapshotName)
	assert.Equal(t, clusterID, created.Snapshot.CacheClusterId)
	assert.Equal(t, "manual", created.Snapshot.SnapshotSource)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-snapshot", "--snapshot-name", snapName).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "describe-snapshots",
		"--snapshot-name", snapName))
	var snaps struct {
		Snapshots []struct {
			SnapshotName string `json:"SnapshotName"`
			Engine       string `json:"Engine"`
		} `json:"Snapshots"`
	}
	parseJSON(t, out, &snaps)
	require.Len(t, snaps.Snapshots, 1)
	assert.Equal(t, "redis", snaps.Snapshots[0].Engine)

	copyName := "cli-snap-copy"
	runCLI(t, awsCLI("elasticache", "copy-snapshot",
		"--source-snapshot-name", snapName,
		"--target-snapshot-name", copyName))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-snapshot", "--snapshot-name", copyName).Run()
	})

	runCLI(t, awsCLI("elasticache", "delete-snapshot", "--snapshot-name", snapName))

	userID := "cli-user-1"
	out = runCLI(t, awsCLI("elasticache", "create-user",
		"--user-id", userID,
		"--user-name", "cli-app",
		"--engine", "redis",
		"--access-string", "on ~* +@all",
		"--passwords", "AStrongPasswordValue1"))
	var user struct {
		UserId       string `json:"UserId"`
		Status       string `json:"Status"`
		AccessString string `json:"AccessString"`
	}
	parseJSON(t, out, &user)
	require.Equal(t, userID, user.UserId)
	assert.Equal(t, "active", user.Status)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-user", "--user-id", userID).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "describe-users", "--user-id", userID))
	var users struct {
		Users []struct {
			UserId   string `json:"UserId"`
			UserName string `json:"UserName"`
		} `json:"Users"`
	}
	parseJSON(t, out, &users)
	require.Len(t, users.Users, 1)
	assert.Equal(t, "cli-app", users.Users[0].UserName)

	runCLI(t, awsCLI("elasticache", "modify-user",
		"--user-id", userID,
		"--access-string", "on ~app:* +@read"))

	groupID := "cli-group-1"
	out = runCLI(t, awsCLI("elasticache", "create-user-group",
		"--user-group-id", groupID,
		"--engine", "redis",
		"--user-ids", userID))
	var grp struct {
		UserGroupId string   `json:"UserGroupId"`
		UserIds     []string `json:"UserIds"`
	}
	parseJSON(t, out, &grp)
	require.Equal(t, groupID, grp.UserGroupId)
	assert.Contains(t, grp.UserIds, userID)
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-user-group", "--user-group-id", groupID).Run()
	})

	out = runCLI(t, awsCLI("elasticache", "describe-user-groups", "--user-group-id", groupID))
	var grps struct {
		UserGroups []struct {
			UserGroupId string   `json:"UserGroupId"`
			UserIds     []string `json:"UserIds"`
		} `json:"UserGroups"`
	}
	parseJSON(t, out, &grps)
	require.Len(t, grps.UserGroups, 1)
	assert.Contains(t, grps.UserGroups[0].UserIds, userID)

	runCLI(t, awsCLI("elasticache", "modify-user-group",
		"--user-group-id", groupID,
		"--user-ids-to-remove", userID))

	runCLI(t, awsCLI("elasticache", "delete-user-group", "--user-group-id", groupID))
	runCLI(t, awsCLI("elasticache", "delete-user", "--user-id", userID))
}

// TestElastiCacheCLI_ParametersAndDescribes covers the parameter detail
// operations and the read-only describes in one test to keep the appdata
// shard lean.
func TestElastiCacheCLI_ParametersAndDescribes(t *testing.T) {
	t.Parallel()
	pgName := "cli-param-detail"
	runCLI(t, awsCLI("elasticache", "create-cache-parameter-group",
		"--cache-parameter-group-name", pgName,
		"--cache-parameter-group-family", "redis7",
		"--description", "cli param detail"))
	t.Cleanup(func() {
		_ = awsCLI("elasticache", "delete-cache-parameter-group",
			"--cache-parameter-group-name", pgName).Run()
	})

	out := runCLI(t, awsCLI("elasticache", "describe-cache-parameters",
		"--cache-parameter-group-name", pgName))
	var params struct {
		Parameters []struct {
			ParameterName  string `json:"ParameterName"`
			ParameterValue string `json:"ParameterValue"`
			Source         string `json:"Source"`
		} `json:"Parameters"`
	}
	parseJSON(t, out, &params)
	require.NotEmpty(t, params.Parameters)

	runCLI(t, awsCLI("elasticache", "modify-cache-parameter-group",
		"--cache-parameter-group-name", pgName,
		"--parameter-name-values", "ParameterName=maxmemory-policy,ParameterValue=allkeys-lru"))

	out = runCLI(t, awsCLI("elasticache", "describe-cache-parameters",
		"--cache-parameter-group-name", pgName))
	parseJSON(t, out, &params)
	found := false
	for _, p := range params.Parameters {
		if p.ParameterName == "maxmemory-policy" {
			assert.Equal(t, "allkeys-lru", p.ParameterValue)
			assert.Equal(t, "user", p.Source)
			found = true
		}
	}
	assert.True(t, found, "modified parameter present")

	runCLI(t, awsCLI("elasticache", "reset-cache-parameter-group",
		"--cache-parameter-group-name", pgName,
		"--reset-all-parameters"))

	out = runCLI(t, awsCLI("elasticache", "describe-engine-default-parameters",
		"--cache-parameter-group-family", "redis7"))
	var defaults struct {
		EngineDefaults struct {
			Parameters []struct {
				ParameterName string `json:"ParameterName"`
			} `json:"Parameters"`
		} `json:"EngineDefaults"`
	}
	parseJSON(t, out, &defaults)
	// The aws CLI auto-paginates DescribeEngineDefaultParameters down to the
	// Parameters list, dropping CacheParameterGroupFamily; the SDK test asserts
	// the family.
	require.NotEmpty(t, defaults.EngineDefaults.Parameters)

	out = runCLI(t, awsCLI("elasticache", "describe-events",
		"--source-type", "cache-cluster"))
	var events struct {
		Events []struct {
			SourceType string `json:"SourceType"`
		} `json:"Events"`
	}
	parseJSON(t, out, &events)
	// A run with no clusters may have no events; the call decoding is the check.

	out = runCLI(t, awsCLI("elasticache", "describe-cache-engine-versions",
		"--engine", "redis"))
	var versions struct {
		CacheEngineVersions []struct {
			Engine        string `json:"Engine"`
			EngineVersion string `json:"EngineVersion"`
		} `json:"CacheEngineVersions"`
	}
	parseJSON(t, out, &versions)
	require.NotEmpty(t, versions.CacheEngineVersions)
	assert.Equal(t, "redis", versions.CacheEngineVersions[0].Engine)

	// Filter to a node type no test reserves.
	out = runCLI(t, awsCLI("elasticache", "describe-reserved-cache-nodes",
		"--cache-node-type", "cache.r6g.large"))
	var reserved struct {
		ReservedCacheNodes []any `json:"ReservedCacheNodes"`
	}
	parseJSON(t, out, &reserved)
	assert.Empty(t, reserved.ReservedCacheNodes)

	out = runCLI(t, awsCLI("elasticache", "describe-reserved-cache-nodes-offerings"))
	var offerings struct {
		ReservedCacheNodesOfferings []struct {
			CacheNodeType string `json:"CacheNodeType"`
		} `json:"ReservedCacheNodesOfferings"`
	}
	parseJSON(t, out, &offerings)
	require.NotEmpty(t, offerings.ReservedCacheNodesOfferings)

	out = runCLI(t, awsCLI("elasticache", "describe-service-updates"))
	var updates struct {
		ServiceUpdates []struct {
			ServiceUpdateName string `json:"ServiceUpdateName"`
		} `json:"ServiceUpdates"`
	}
	parseJSON(t, out, &updates)
	require.NotEmpty(t, updates.ServiceUpdates)

	// A VPC-only account has no cache security groups.
	out = runCLI(t, awsCLI("elasticache", "describe-cache-security-groups"))
	var sgs struct {
		CacheSecurityGroups []any `json:"CacheSecurityGroups"`
	}
	parseJSON(t, out, &sgs)
	assert.Empty(t, sgs.CacheSecurityGroups)
}
