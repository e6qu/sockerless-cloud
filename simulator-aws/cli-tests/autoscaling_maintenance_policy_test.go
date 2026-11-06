package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoScalingInstanceMaintenancePolicyCLI expects a group to store its
// instance maintenance policy, lend it to an instance refresh that omits the
// thresholds, and clear it with -1.
func TestAutoScalingInstanceMaintenancePolicyCLI(t *testing.T) {
	t.Parallel()
	runCLI(t, awsCLI("autoscaling", "create-launch-configuration", "--launch-configuration-name", "cli-maint-lc",
		"--image-id", ec2AmazonLinuxAMI(t), "--instance-type", "t3.micro"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("autoscaling", "delete-launch-configuration", "--launch-configuration-name", "cli-maint-lc"))
	})
	assert.Contains(t, runCLIExpectError(t, awsCLI("autoscaling", "create-auto-scaling-group",
		"--auto-scaling-group-name", "cli-maint-asg", "--launch-configuration-name", "cli-maint-lc",
		"--min-size", "0", "--max-size", "2", "--desired-capacity", "0",
		"--instance-maintenance-policy", "MinHealthyPercentage=10,MaxHealthyPercentage=150")), "ValidationError")
	runCLI(t, awsCLI("autoscaling", "create-auto-scaling-group",
		"--auto-scaling-group-name", "cli-maint-asg", "--launch-configuration-name", "cli-maint-lc",
		"--min-size", "0", "--max-size", "2", "--desired-capacity", "0",
		"--instance-maintenance-policy", "MinHealthyPercentage=90,MaxHealthyPercentage=120"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("autoscaling", "delete-auto-scaling-group", "--auto-scaling-group-name", "cli-maint-asg", "--force-delete"))
	})

	var groups struct {
		AutoScalingGroups []struct {
			InstanceMaintenancePolicy *struct {
				MinHealthyPercentage int `json:"MinHealthyPercentage"`
				MaxHealthyPercentage int `json:"MaxHealthyPercentage"`
			} `json:"InstanceMaintenancePolicy"`
		} `json:"AutoScalingGroups"`
	}
	describe := func() {
		groups.AutoScalingGroups = nil
		require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-groups",
			"--auto-scaling-group-names", "cli-maint-asg"))), &groups))
		require.Len(t, groups.AutoScalingGroups, 1)
	}
	describe()
	require.NotNil(t, groups.AutoScalingGroups[0].InstanceMaintenancePolicy)
	assert.Equal(t, 90, groups.AutoScalingGroups[0].InstanceMaintenancePolicy.MinHealthyPercentage)
	assert.Equal(t, 120, groups.AutoScalingGroups[0].InstanceMaintenancePolicy.MaxHealthyPercentage)

	var refresh struct {
		ID string `json:"InstanceRefreshId"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("autoscaling", "start-instance-refresh",
		"--auto-scaling-group-name", "cli-maint-asg"))), &refresh))
	var refreshes struct {
		InstanceRefreshes []struct {
			Preferences struct {
				MinHealthyPercentage int `json:"MinHealthyPercentage"`
				MaxHealthyPercentage int `json:"MaxHealthyPercentage"`
			} `json:"Preferences"`
		} `json:"InstanceRefreshes"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("autoscaling", "describe-instance-refreshes",
		"--auto-scaling-group-name", "cli-maint-asg", "--instance-refresh-ids", refresh.ID))), &refreshes))
	require.Len(t, refreshes.InstanceRefreshes, 1)
	assert.Equal(t, 90, refreshes.InstanceRefreshes[0].Preferences.MinHealthyPercentage)
	assert.Equal(t, 120, refreshes.InstanceRefreshes[0].Preferences.MaxHealthyPercentage)

	runCLI(t, awsCLI("autoscaling", "update-auto-scaling-group", "--auto-scaling-group-name", "cli-maint-asg",
		"--instance-maintenance-policy", "MinHealthyPercentage=-1,MaxHealthyPercentage=-1"))
	describe()
	assert.Nil(t, groups.AutoScalingGroups[0].InstanceMaintenancePolicy)
}
