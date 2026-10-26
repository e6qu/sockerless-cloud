package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2CLI_TargetGroupStickiness expects create-rule to keep a weighted
// forward's TargetGroupStickinessConfig for describe-rules, and to refuse one
// enabled without DurationSeconds: "You must specify ... when enabling target
// group stickiness."
func TestELBv2CLI_TargetGroupStickiness(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.26.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	zoneA := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.26.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")
	zoneB := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.26.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")
	lbArn := q("elbv2", "create-load-balancer", "--name", "cli-sticky-alb", "--type", "application", "--subnets", zoneA, zoneB,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn).Run() })
	targetGroup := func(name string) string {
		arn := q("elbv2", "create-target-group", "--name", name, "--protocol", "HTTP", "--port", "80",
			"--vpc-id", vpcID, "--target-type", "ip", "--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
		t.Cleanup(func() { _ = awsCLI("elbv2", "delete-target-group", "--target-group-arn", arn).Run() })
		return arn
	}
	groupA, groupB := targetGroup("cli-sticky-a"), targetGroup("cli-sticky-b")
	listenerArn := q("elbv2", "create-listener", "--load-balancer-arn", lbArn, "--protocol", "HTTP", "--port", "80",
		"--default-actions", "Type=fixed-response,FixedResponseConfig={StatusCode=404}",
		"--query", "Listeners[0].ListenerArn", "--output", "text")
	forward := func(stickiness string) string {
		return "Type=forward,ForwardConfig={TargetGroups=[{TargetGroupArn=" + groupA + ",Weight=1},{TargetGroupArn=" + groupB + ",Weight=1}]," +
			"TargetGroupStickinessConfig={" + stickiness + "}}"
	}
	conditions := "Field=path-pattern,Values=/split"

	out := runCLIExpectError(t, awsCLI("elbv2", "create-rule", "--listener-arn", listenerArn, "--priority", "10",
		"--conditions", conditions, "--actions", forward("Enabled=true")))
	if !strings.Contains(out, "(ValidationError)") || !strings.Contains(out, "DurationSeconds") {
		t.Fatalf("stickiness enabled without DurationSeconds: got %q", out)
	}
	out = runCLIExpectError(t, awsCLI("elbv2", "create-rule", "--listener-arn", listenerArn, "--priority", "10",
		"--conditions", conditions, "--actions", forward("Enabled=true,DurationSeconds=604801")))
	if !strings.Contains(out, "(ValidationError)") {
		t.Fatalf("stickiness beyond 604800 seconds: got %q", out)
	}

	ruleArn := q("elbv2", "create-rule", "--listener-arn", listenerArn, "--priority", "10",
		"--conditions", conditions, "--actions", forward("Enabled=true,DurationSeconds=600"),
		"--query", "Rules[0].RuleArn", "--output", "text")
	got := q("elbv2", "describe-rules", "--rule-arns", ruleArn,
		"--query", "Rules[0].Actions[0].ForwardConfig.TargetGroupStickinessConfig.[Enabled,DurationSeconds]", "--output", "text")
	if got != "True\t600" {
		t.Fatalf("describe-rules stickiness: got %q, want enabled for 600 seconds", got)
	}
	out = runCLIExpectError(t, awsCLI("elbv2", "modify-rule", "--rule-arn", ruleArn, "--actions", forward("Enabled=true,DurationSeconds=0")))
	if !strings.Contains(out, "(ValidationError)") {
		t.Fatalf("modify-rule with a zero duration: got %q", out)
	}
	got = q("elbv2", "modify-rule", "--rule-arn", ruleArn, "--actions", forward("Enabled=false"),
		"--query", "Rules[0].Actions[0].ForwardConfig.TargetGroupStickinessConfig.Enabled", "--output", "text")
	if got != "False" {
		t.Fatalf("modify-rule turning stickiness off: got %q", got)
	}
}
