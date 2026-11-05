package aws_cli_test

import (
	"strings"
	"testing"
)

// TestAutoScalingLaunchTemplateTagsMergeCLI expects an instance a group
// launches from a launch template to carry the template's instance tags merged
// with the group's propagated tags, the group's value winning on a shared key.
func TestAutoScalingLaunchTemplateTagsMergeCLI(t *testing.T) {
	t.Parallel()
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.93.0.0/16",
		"--query", "Vpc.VpcId", "--output", "text")))
	subnetID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpcID,
		"--cidr-block", "10.93.1.0/24", "--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId", "--output", "text")))

	runCLI(t, awsCLI("ec2", "create-launch-template", "--launch-template-name", "cli-asg-tags-lt",
		"--launch-template-data", `{"ImageId":"`+ec2AmazonLinuxAMI(t)+`","InstanceType":"t3.micro","TagSpecifications":[`+
			`{"ResourceType":"instance","Tags":[{"Key":"env","Value":"template"},{"Key":"owner","Value":"platform"}]},`+
			`{"ResourceType":"volume","Tags":[{"Key":"backup","Value":"daily"}]}]}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("ec2", "delete-launch-template", "--launch-template-name", "cli-asg-tags-lt"))
	})
	runCLI(t, awsCLI("autoscaling", "create-auto-scaling-group",
		"--auto-scaling-group-name", "cli-asg-tags",
		"--launch-template", "LaunchTemplateName=cli-asg-tags-lt,Version=$Latest",
		"--min-size", "1", "--max-size", "1", "--desired-capacity", "1",
		"--vpc-zone-identifier", subnetID,
		"--tags", "Key=env,Value=group,PropagateAtLaunch=true",
		"Key=cost-center,Value=group-only,PropagateAtLaunch=false"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("autoscaling", "delete-auto-scaling-group", "--auto-scaling-group-name", "cli-asg-tags", "--force-delete"))
	})

	instanceID := strings.TrimSpace(runCLI(t, awsCLI("autoscaling", "describe-auto-scaling-groups",
		"--auto-scaling-group-names", "cli-asg-tags",
		"--query", "AutoScalingGroups[0].Instances[0].InstanceId", "--output", "text")))
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", instanceID))
	out := runCLI(t, awsCLI("ec2", "describe-tags",
		"--filters", "Name=resource-id,Values="+instanceID,
		"--query", "sort_by(Tags, &Key)[].[Key,Value]", "--output", "text"))
	if got := strings.Join(strings.Fields(out), " "); got != "aws:autoscaling:groupName cli-asg-tags env group owner platform" {
		t.Fatalf("expected the template's instance tags under the group's propagated ones, got %q", got)
	}
}
