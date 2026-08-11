package aws_cli_test

import (
	"strings"
	"testing"
)

func TestAutoScalingGroupLifecycleCLI(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.82.0.0/16",
		"--query", "Vpc.VpcId",
		"--output", "text"))
	vpcID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.82.1.0/24",
		"--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId",
		"--output", "text"))
	subnetID := strings.TrimSpace(out)

	runCLI(t, awsCLI("autoscaling", "create-launch-configuration",
		"--launch-configuration-name", "cli-lc",
		"--image-id", "ami-cli-asg",
		"--instance-type", "t3.micro"))
	runCLI(t, awsCLI("autoscaling", "create-auto-scaling-group",
		"--auto-scaling-group-name", "cli-asg",
		"--launch-configuration-name", "cli-lc",
		"--min-size", "1",
		"--max-size", "2",
		"--desired-capacity", "1",
		"--vpc-zone-identifier", subnetID))

	out = runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-groups",
		"--auto-scaling-group-names", "cli-asg",
		"--query", "AutoScalingGroups[0].Instances[0].InstanceId",
		"--output", "text"))
	if !strings.HasPrefix(strings.TrimSpace(out), "i-") {
		t.Fatalf("expected materialized EC2 instance id, got %q", out)
	}

	runCLI(t, awsCLI("autoscaling", "set-desired-capacity",
		"--auto-scaling-group-name", "cli-asg",
		"--desired-capacity", "2"))
	out = runCLI(t, awsCLI("autoscaling", "describe-scaling-activities",
		"--auto-scaling-group-name", "cli-asg",
		"--query", "Activities[0].StatusCode",
		"--output", "text"))
	if strings.TrimSpace(out) != "Successful" {
		t.Fatalf("expected successful scaling activity, got %q", out)
	}

	runCLI(t, awsCLI("autoscaling", "delete-auto-scaling-group",
		"--auto-scaling-group-name", "cli-asg",
		"--force-delete"))
	runCLI(t, awsCLI("autoscaling", "delete-launch-configuration",
		"--launch-configuration-name", "cli-lc"))
}

// TestAutoScalingCLI_PoliciesAndHooks drives the `aws autoscaling`
// scaling-policy, scheduled-action, lifecycle-hook, and per-instance verbs
// against a real group.
func TestAutoScalingCLI_PoliciesAndHooks(t *testing.T) {
	out := runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.84.0.0/16",
		"--query", "Vpc.VpcId",
		"--output", "text"))
	vpcID := strings.TrimSpace(out)

	out = runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID,
		"--cidr-block", "10.84.1.0/24",
		"--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId",
		"--output", "text"))
	subnetID := strings.TrimSpace(out)

	runCLI(t, awsCLI("autoscaling", "create-launch-configuration",
		"--launch-configuration-name", "cli-pol-lc",
		"--image-id", "ami-cli-pol",
		"--instance-type", "t3.micro"))
	t.Cleanup(func() {
		_ = awsCLI("autoscaling", "delete-launch-configuration",
			"--launch-configuration-name", "cli-pol-lc").Run()
	})
	runCLI(t, awsCLI("autoscaling", "create-auto-scaling-group",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--launch-configuration-name", "cli-pol-lc",
		"--min-size", "1",
		"--max-size", "4",
		"--desired-capacity", "2",
		"--vpc-zone-identifier", subnetID))
	t.Cleanup(func() {
		_ = awsCLI("autoscaling", "delete-auto-scaling-group",
			"--auto-scaling-group-name", "cli-pol-asg",
			"--force-delete").Run()
	})

	runCLI(t, awsCLI("autoscaling", "put-scaling-policy",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--policy-name", "cli-scale-out",
		"--policy-type", "SimpleScaling",
		"--adjustment-type", "ChangeInCapacity",
		"--scaling-adjustment", "1"))
	out = runCLI(t, awsCLI("autoscaling", "describe-policies",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--query", "ScalingPolicies[0].PolicyName",
		"--output", "text"))
	if strings.TrimSpace(out) != "cli-scale-out" {
		t.Fatalf("expected cli-scale-out policy, got %q", out)
	}
	runCLI(t, awsCLI("autoscaling", "execute-policy",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--policy-name", "cli-scale-out"))
	out = runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-groups",
		"--auto-scaling-group-names", "cli-pol-asg",
		"--query", "AutoScalingGroups[0].DesiredCapacity",
		"--output", "text"))
	if strings.TrimSpace(out) != "3" {
		t.Fatalf("expected DesiredCapacity 3 after execute-policy, got %q", out)
	}
	runCLI(t, awsCLI("autoscaling", "delete-policy",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--policy-name", "cli-scale-out"))

	runCLI(t, awsCLI("autoscaling", "put-scheduled-update-group-action",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--scheduled-action-name", "cli-nightly",
		"--recurrence", "0 0 * * *",
		"--min-size", "0",
		"--max-size", "5",
		"--desired-capacity", "1"))
	out = runCLI(t, awsCLI("autoscaling", "describe-scheduled-actions",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--query", "ScheduledUpdateGroupActions[0].ScheduledActionName",
		"--output", "text"))
	if strings.TrimSpace(out) != "cli-nightly" {
		t.Fatalf("expected cli-nightly scheduled action, got %q", out)
	}
	runCLI(t, awsCLI("autoscaling", "delete-scheduled-action",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--scheduled-action-name", "cli-nightly"))

	runCLI(t, awsCLI("autoscaling", "put-lifecycle-hook",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--lifecycle-hook-name", "cli-drain",
		"--lifecycle-transition", "autoscaling:EC2_INSTANCE_TERMINATING",
		"--default-result", "CONTINUE",
		"--heartbeat-timeout", "300"))
	out = runCLI(t, awsCLI("autoscaling", "describe-lifecycle-hooks",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--query", "LifecycleHooks[0].DefaultResult",
		"--output", "text"))
	if strings.TrimSpace(out) != "CONTINUE" {
		t.Fatalf("expected CONTINUE lifecycle hook default result, got %q", out)
	}
	runCLI(t, awsCLI("autoscaling", "delete-lifecycle-hook",
		"--auto-scaling-group-name", "cli-pol-asg",
		"--lifecycle-hook-name", "cli-drain"))

	out = runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-instances",
		"--query", "AutoScalingInstances[?AutoScalingGroupName=='cli-pol-asg'].InstanceId | [0]",
		"--output", "text"))
	instanceID := strings.TrimSpace(out)
	if !strings.HasPrefix(instanceID, "i-") {
		t.Fatalf("expected describe-auto-scaling-instances to list a member, got %q", out)
	}
	runCLI(t, awsCLI("autoscaling", "terminate-instance-in-auto-scaling-group",
		"--instance-id", instanceID,
		"--should-decrement-desired-capacity"))

	out = runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-instances",
		"--query", "AutoScalingInstances[?AutoScalingGroupName=='cli-pol-asg'].InstanceId | [0]",
		"--output", "text"))
	remaining := strings.TrimSpace(out)
	if strings.HasPrefix(remaining, "i-") {
		runCLI(t, awsCLI("autoscaling", "set-instance-health",
			"--instance-id", remaining,
			"--health-status", "Healthy"))
	}
}

func TestCloudTrailRecordsAPICallsCLI(t *testing.T) {
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", "cli-cloudtrail-bucket"))
	runCLI(t, awsCLI("cloudtrail", "create-trail",
		"--name", "cli-trail",
		"--s3-bucket-name", "cli-cloudtrail-bucket",
		"--s3-key-prefix", "trail-logs"))
	runCLI(t, awsCLI("cloudtrail", "start-logging", "--name", "cli-trail"))
	runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.83.0.0/16"))

	out := runCLI(t, awsCLI("cloudtrail", "lookup-events",
		"--lookup-attributes", "AttributeKey=EventName,AttributeValue=CreateVpc",
		"--query", "Events[0].EventName",
		"--output", "text"))
	if strings.TrimSpace(out) != "CreateVpc" {
		t.Fatalf("expected CreateVpc CloudTrail event, got %q", out)
	}

	out = runCLI(t, awsCLI("s3api", "list-objects-v2",
		"--bucket", "cli-cloudtrail-bucket",
		"--prefix", "trail-logs/AWSLogs/123456789012/CloudTrail/us-east-1/",
		"--query", "Contents[0].Key",
		"--output", "text"))
	if !strings.Contains(strings.TrimSpace(out), "cli-trail_") {
		t.Fatalf("expected delivered CloudTrail log object, got %q", out)
	}

	runCLI(t, awsCLI("cloudtrail", "stop-logging", "--name", "cli-trail"))
	runCLI(t, awsCLI("cloudtrail", "delete-trail", "--name", "cli-trail"))
}
