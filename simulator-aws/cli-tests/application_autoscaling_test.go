package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplicationAutoScalingCLI_TargetAndPolicy drives the `aws
// application-autoscaling` verbs a runner platform uses to autoscale an ECS
// service. Distinct from `aws autoscaling` (EC2 Auto Scaling).
func TestApplicationAutoScalingCLI_TargetAndPolicy(t *testing.T) {
	const (
		ns         = "ecs"
		resourceID = "service/cli-cluster/cli-svc"
		dimension  = "ecs:service:DesiredCount"
	)

	runCLI(t, awsCLI("application-autoscaling", "register-scalable-target",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dimension,
		"--min-capacity", "1",
		"--max-capacity", "5"))
	t.Cleanup(func() {
		_ = awsCLI("application-autoscaling", "deregister-scalable-target",
			"--service-namespace", ns,
			"--resource-id", resourceID,
			"--scalable-dimension", dimension).Run()
	})

	descOut := runCLI(t, awsCLI("application-autoscaling", "describe-scalable-targets",
		"--service-namespace", ns,
		"--resource-ids", resourceID,
		"--output", "json"))
	var desc struct {
		ScalableTargets []struct {
			ResourceId  string `json:"ResourceId"`
			MinCapacity int    `json:"MinCapacity"`
			MaxCapacity int    `json:"MaxCapacity"`
		} `json:"ScalableTargets"`
	}
	parseJSON(t, descOut, &desc)
	require.Len(t, desc.ScalableTargets, 1)
	assert.Equal(t, resourceID, desc.ScalableTargets[0].ResourceId)
	assert.Equal(t, 5, desc.ScalableTargets[0].MaxCapacity)

	putOut := runCLI(t, awsCLI("application-autoscaling", "put-scaling-policy",
		"--policy-name", "cli-cpu-tt",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dimension,
		"--policy-type", "TargetTrackingScaling",
		"--target-tracking-scaling-policy-configuration",
		`{"TargetValue":55.0,"PredefinedMetricSpecification":{"PredefinedMetricType":"ECSServiceAverageCPUUtilization"}}`,
		"--output", "json"))
	var put struct {
		PolicyARN string `json:"PolicyARN"`
	}
	parseJSON(t, putOut, &put)
	assert.Contains(t, put.PolicyARN, ":scalingPolicy:")

	polOut := runCLI(t, awsCLI("application-autoscaling", "describe-scaling-policies",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--output", "json"))
	var pol struct {
		ScalingPolicies []struct {
			PolicyName string `json:"PolicyName"`
			PolicyType string `json:"PolicyType"`
		} `json:"ScalingPolicies"`
	}
	parseJSON(t, polOut, &pol)
	require.Len(t, pol.ScalingPolicies, 1)
	assert.Equal(t, "cli-cpu-tt", pol.ScalingPolicies[0].PolicyName)
	assert.Equal(t, "TargetTrackingScaling", pol.ScalingPolicies[0].PolicyType)

	runCLI(t, awsCLI("application-autoscaling", "delete-scaling-policy",
		"--policy-name", "cli-cpu-tt",
		"--service-namespace", ns,
		"--resource-id", resourceID,
		"--scalable-dimension", dimension))
}
