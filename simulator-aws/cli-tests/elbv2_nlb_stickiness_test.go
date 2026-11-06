package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2CLI_NetworkTargetGroupStickiness expects a TCP target group to take
// source_ip stickiness, the type the model gives Network Load Balancers, and
// to refuse an Application Load Balancer's lb_cookie.
func TestELBv2CLI_NetworkTargetGroupStickiness(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.28.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	tgArn := q("elbv2", "create-target-group", "--name", "cli-nlb-sticky", "--protocol", "TCP", "--port", "80",
		"--vpc-id", vpcID, "--target-type", "ip", "--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-target-group", "--target-group-arn", tgArn).Run() })

	out := runCLIExpectError(t, awsCLI("elbv2", "modify-target-group-attributes", "--target-group-arn", tgArn,
		"--attributes", "Key=stickiness.enabled,Value=true", "Key=stickiness.type,Value=lb_cookie"))
	if !strings.Contains(out, "(InvalidConfigurationRequest)") {
		t.Fatalf("lb_cookie stickiness on a TCP target group: got %q", out)
	}
	runCLI(t, awsCLI("elbv2", "modify-target-group-attributes", "--target-group-arn", tgArn,
		"--attributes", "Key=stickiness.enabled,Value=true", "Key=stickiness.type,Value=source_ip"))
	got := q("elbv2", "describe-target-group-attributes", "--target-group-arn", tgArn,
		"--query", "Attributes[?Key=='stickiness.enabled' || Key=='stickiness.type'].Value | sort(@) | join(',', @)", "--output", "text")
	if got != "source_ip,true" {
		t.Fatalf("describe-target-group-attributes stickiness: got %q, want source_ip and true", got)
	}
}
