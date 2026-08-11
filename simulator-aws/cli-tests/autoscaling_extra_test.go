package aws_cli_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// asxSetupGroupCLI creates a VPC, subnet, launch configuration and Auto Scaling
// group (desired capacity 0 so no real EC2 instance is launched) using the same
// coordinates a real consumer drives. Returns the subnet ID; registers tolerant
// cleanups.
func asxSetupGroupCLI(t *testing.T, lcName, groupName string, desired int) {
	t.Helper()

	var vpc struct {
		Vpc struct {
			VpcId string `json:"VpcId"`
		} `json:"Vpc"`
	}
	parseJSON(t, runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.92.0.0/16", "--output", "json")), &vpc)

	var subnet struct {
		Subnet struct {
			SubnetId string `json:"SubnetId"`
		} `json:"Subnet"`
	}
	parseJSON(t, runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpc.Vpc.VpcId,
		"--cidr-block", "10.92.1.0/24",
		"--availability-zone", "us-east-1a",
		"--output", "json")), &subnet)

	runCLI(t, awsCLI("autoscaling", "create-launch-configuration",
		"--launch-configuration-name", lcName,
		"--image-id", "ami-asxcli01",
		"--instance-type", "t3.micro"))
	t.Cleanup(func() {
		_ = awsCLI("autoscaling", "delete-launch-configuration", "--launch-configuration-name", lcName).Run()
	})

	runCLI(t, awsCLI("autoscaling", "create-auto-scaling-group",
		"--auto-scaling-group-name", groupName,
		"--launch-configuration-name", lcName,
		"--min-size", "0",
		"--max-size", "3",
		"--desired-capacity", strconv.Itoa(desired),
		"--vpc-zone-identifier", subnet.Subnet.SubnetId))
	t.Cleanup(func() {
		_ = awsCLI("autoscaling", "delete-auto-scaling-group", "--auto-scaling-group-name", groupName, "--force-delete").Run()
	})
}

// TestAutoScalingCLI_ExtendedOps drives the extended Auto Scaling operations
// through the real aws CLI, grouped into one test so the EDGE shard regex
// (^TestAutoScaling) keeps them together.
func TestAutoScalingCLI_ExtendedOps(t *testing.T) {
	const group = "asx-cli-grp"
	asxSetupGroupCLI(t, "asx-cli-lc", group, 1)

	// Load balancers.
	runCLI(t, awsCLI("autoscaling", "attach-load-balancers",
		"--auto-scaling-group-name", group, "--load-balancer-names", "asx-cli-clb"))
	var lbOut struct {
		LoadBalancers []struct {
			LoadBalancerName string `json:"LoadBalancerName"`
			State            string `json:"State"`
		} `json:"LoadBalancers"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-load-balancers",
		"--auto-scaling-group-name", group, "--output", "json")), &lbOut)
	require.Len(t, lbOut.LoadBalancers, 1)
	assert.Equal(t, "asx-cli-clb", lbOut.LoadBalancers[0].LoadBalancerName)
	runCLI(t, awsCLI("autoscaling", "detach-load-balancers",
		"--auto-scaling-group-name", group, "--load-balancer-names", "asx-cli-clb"))

	// Target groups.
	const tgARN = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/asx-cli/aabbccddeeff0011"
	runCLI(t, awsCLI("autoscaling", "attach-load-balancer-target-groups",
		"--auto-scaling-group-name", group, "--target-group-arns", tgARN))
	var tgOut struct {
		LoadBalancerTargetGroups []struct {
			LoadBalancerTargetGroupARN string `json:"LoadBalancerTargetGroupARN"`
		} `json:"LoadBalancerTargetGroups"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-load-balancer-target-groups",
		"--auto-scaling-group-name", group, "--output", "json")), &tgOut)
	require.Len(t, tgOut.LoadBalancerTargetGroups, 1)
	assert.Equal(t, tgARN, tgOut.LoadBalancerTargetGroups[0].LoadBalancerTargetGroupARN)
	runCLI(t, awsCLI("autoscaling", "detach-load-balancer-target-groups",
		"--auto-scaling-group-name", group, "--target-group-arns", tgARN))

	// Traffic sources.
	runCLI(t, awsCLI("autoscaling", "attach-traffic-sources",
		"--auto-scaling-group-name", group,
		"--traffic-sources", "Identifier="+tgARN+",Type=elbv2"))
	var tsOut struct {
		TrafficSources []struct {
			Identifier string `json:"Identifier"`
			State      string `json:"State"`
		} `json:"TrafficSources"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-traffic-sources",
		"--auto-scaling-group-name", group, "--output", "json")), &tsOut)
	require.Len(t, tsOut.TrafficSources, 1)
	assert.Equal(t, tgARN, tsOut.TrafficSources[0].Identifier)
	runCLI(t, awsCLI("autoscaling", "detach-traffic-sources",
		"--auto-scaling-group-name", group,
		"--traffic-sources", "Identifier="+tgARN+",Type=elbv2"))

	// Instance refresh.
	var refreshOut struct {
		InstanceRefreshId string `json:"InstanceRefreshId"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "start-instance-refresh",
		"--auto-scaling-group-name", group, "--output", "json")), &refreshOut)
	require.NotEmpty(t, refreshOut.InstanceRefreshId)
	var refreshesOut struct {
		InstanceRefreshes []struct {
			InstanceRefreshId string `json:"InstanceRefreshId"`
			Status            string `json:"Status"`
		} `json:"InstanceRefreshes"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-instance-refreshes",
		"--auto-scaling-group-name", group, "--output", "json")), &refreshesOut)
	require.Len(t, refreshesOut.InstanceRefreshes, 1)
	assert.Equal(t, "Successful", refreshesOut.InstanceRefreshes[0].Status)
	runCLI(t, awsCLI("autoscaling", "rollback-instance-refresh", "--auto-scaling-group-name", group))
	// CancelInstanceRefresh has no in-progress refresh -> real error.
	runCLIExpectError(t, awsCLI("autoscaling", "cancel-instance-refresh", "--auto-scaling-group-name", group))

	// Warm pool.
	runCLI(t, awsCLI("autoscaling", "put-warm-pool",
		"--auto-scaling-group-name", group, "--min-size", "2", "--pool-state", "Stopped"))
	var wpOut struct {
		WarmPoolConfiguration struct {
			MinSize   int    `json:"MinSize"`
			PoolState string `json:"PoolState"`
		} `json:"WarmPoolConfiguration"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-warm-pool",
		"--auto-scaling-group-name", group, "--output", "json")), &wpOut)
	assert.Equal(t, 2, wpOut.WarmPoolConfiguration.MinSize)
	assert.Equal(t, "Stopped", wpOut.WarmPoolConfiguration.PoolState)
	runCLI(t, awsCLI("autoscaling", "delete-warm-pool", "--auto-scaling-group-name", group))

	// Notifications.
	const topic = "arn:aws:sns:us-east-1:123456789012:asx-cli-topic"
	runCLI(t, awsCLI("autoscaling", "put-notification-configuration",
		"--auto-scaling-group-name", group, "--topic-arn", topic,
		"--notification-types", "autoscaling:EC2_INSTANCE_LAUNCH"))
	var ncOut struct {
		NotificationConfigurations []struct {
			TopicARN string `json:"TopicARN"`
		} `json:"NotificationConfigurations"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-notification-configurations",
		"--auto-scaling-group-names", group, "--output", "json")), &ncOut)
	require.Len(t, ncOut.NotificationConfigurations, 1)
	assert.Equal(t, topic, ncOut.NotificationConfigurations[0].TopicARN)
	runCLI(t, awsCLI("autoscaling", "delete-notification-configuration",
		"--auto-scaling-group-name", group, "--topic-arn", topic))

	// Metrics + processes.
	runCLI(t, awsCLI("autoscaling", "enable-metrics-collection",
		"--auto-scaling-group-name", group, "--granularity", "1Minute"))
	runCLI(t, awsCLI("autoscaling", "disable-metrics-collection", "--auto-scaling-group-name", group))
	runCLI(t, awsCLI("autoscaling", "suspend-processes",
		"--auto-scaling-group-name", group, "--scaling-processes", "Terminate"))
	runCLI(t, awsCLI("autoscaling", "resume-processes",
		"--auto-scaling-group-name", group, "--scaling-processes", "Terminate"))

	// Lifecycle hook + actions.
	runCLI(t, awsCLI("autoscaling", "put-lifecycle-hook",
		"--auto-scaling-group-name", group, "--lifecycle-hook-name", "asx-cli-hook",
		"--lifecycle-transition", "autoscaling:EC2_INSTANCE_LAUNCHING"))
	runCLI(t, awsCLI("autoscaling", "record-lifecycle-action-heartbeat",
		"--auto-scaling-group-name", group, "--lifecycle-hook-name", "asx-cli-hook",
		"--lifecycle-action-token", "00000000-0000-0000-0000-0000000cli01"))
	runCLI(t, awsCLI("autoscaling", "complete-lifecycle-action",
		"--auto-scaling-group-name", group, "--lifecycle-hook-name", "asx-cli-hook",
		"--lifecycle-action-token", "00000000-0000-0000-0000-0000000cli01",
		"--lifecycle-action-result", "CONTINUE"))

	// Batch scheduled actions.
	runCLI(t, awsCLI("autoscaling", "batch-put-scheduled-update-group-action",
		"--auto-scaling-group-name", group,
		"--scheduled-update-group-actions", "ScheduledActionName=asx-cli-sched,MinSize=0,MaxSize=3,DesiredCapacity=1,Recurrence=0 12 * * *"))
	var schedOut struct {
		ScheduledUpdateGroupActions []struct {
			ScheduledActionName string `json:"ScheduledActionName"`
		} `json:"ScheduledUpdateGroupActions"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-scheduled-actions",
		"--auto-scaling-group-name", group, "--output", "json")), &schedOut)
	require.Len(t, schedOut.ScheduledUpdateGroupActions, 1)
	runCLI(t, awsCLI("autoscaling", "batch-delete-scheduled-action",
		"--auto-scaling-group-name", group, "--scheduled-action-names", "asx-cli-sched"))

	// Predictive forecast.
	runCLI(t, awsCLI("autoscaling", "get-predictive-scaling-forecast",
		"--auto-scaling-group-name", group, "--policy-name", "asx-cli-pred",
		"--start-time", "2026-01-01T00:00:00Z", "--end-time", "2026-01-02T00:00:00Z"))

	// Per-instance ops on the group's instance (LaunchInstances is covered by
	// the SDK test; it is absent from older aws CLI builds).
	var groupOut struct {
		AutoScalingGroups []struct {
			Instances []struct {
				InstanceId string `json:"InstanceId"`
			} `json:"Instances"`
		} `json:"AutoScalingGroups"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-groups",
		"--auto-scaling-group-names", group, "--output", "json")), &groupOut)
	require.NotEmpty(t, groupOut.AutoScalingGroups)
	require.NotEmpty(t, groupOut.AutoScalingGroups[0].Instances)
	instanceID := groupOut.AutoScalingGroups[0].Instances[0].InstanceId
	require.NotEmpty(t, instanceID)

	runCLI(t, awsCLI("autoscaling", "set-instance-protection",
		"--auto-scaling-group-name", group, "--instance-ids", instanceID, "--protected-from-scale-in"))
	var standbyOut struct {
		Activities []struct {
			ActivityId string `json:"ActivityId"`
		} `json:"Activities"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "enter-standby",
		"--auto-scaling-group-name", group, "--instance-ids", instanceID,
		"--should-decrement-desired-capacity", "--output", "json")), &standbyOut)
	require.NotEmpty(t, standbyOut.Activities)
	runCLI(t, awsCLI("autoscaling", "exit-standby",
		"--auto-scaling-group-name", group, "--instance-ids", instanceID))
	runCLI(t, awsCLI("autoscaling", "detach-instances",
		"--auto-scaling-group-name", group, "--instance-ids", instanceID,
		"--should-decrement-desired-capacity"))
	runCLI(t, awsCLI("autoscaling", "attach-instances",
		"--auto-scaling-group-name", group, "--instance-ids", instanceID))

	// Static describe/enumeration operations.
	var limOut struct {
		MaxNumberOfAutoScalingGroups int `json:"MaxNumberOfAutoScalingGroups"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-account-limits", "--output", "json")), &limOut)
	assert.Equal(t, 500, limOut.MaxNumberOfAutoScalingGroups)

	var adjOut struct {
		AdjustmentTypes []struct {
			AdjustmentType string `json:"AdjustmentType"`
		} `json:"AdjustmentTypes"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-adjustment-types", "--output", "json")), &adjOut)
	require.Len(t, adjOut.AdjustmentTypes, 3)

	var notifTypesOut struct {
		AutoScalingNotificationTypes []string `json:"AutoScalingNotificationTypes"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-notification-types", "--output", "json")), &notifTypesOut)
	assert.Contains(t, notifTypesOut.AutoScalingNotificationTypes, "autoscaling:EC2_INSTANCE_LAUNCH")

	var hookTypesOut struct {
		LifecycleHookTypes []string `json:"LifecycleHookTypes"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-lifecycle-hook-types", "--output", "json")), &hookTypesOut)
	assert.Contains(t, hookTypesOut.LifecycleHookTypes, "autoscaling:EC2_INSTANCE_LAUNCHING")

	var metricsOut struct {
		Metrics []struct {
			Metric string `json:"Metric"`
		} `json:"Metrics"`
		Granularities []struct {
			Granularity string `json:"Granularity"`
		} `json:"Granularities"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-metric-collection-types", "--output", "json")), &metricsOut)
	require.NotEmpty(t, metricsOut.Metrics)
	require.NotEmpty(t, metricsOut.Granularities)
	assert.Equal(t, "1Minute", metricsOut.Granularities[0].Granularity)

	var procOut struct {
		Processes []struct {
			ProcessName string `json:"ProcessName"`
		} `json:"Processes"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-scaling-process-types", "--output", "json")), &procOut)
	require.NotEmpty(t, procOut.Processes)

	var termOut struct {
		TerminationPolicyTypes []string `json:"TerminationPolicyTypes"`
	}
	parseJSON(t, runCLI(t, awsCLI("autoscaling", "describe-termination-policy-types", "--output", "json")), &termOut)
	assert.Contains(t, termOut.TerminationPolicyTypes, "Default")
}
