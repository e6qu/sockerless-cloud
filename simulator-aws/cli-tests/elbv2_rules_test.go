package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2CLI_ListenerRules drives listener-rule CRUD + ModifyListener via the
// aws CLI, using the CLI `Field=...,Values=...` condition
// shorthand.
func TestELBv2CLI_ListenerRules(t *testing.T) {
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.92.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sn1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.92.1.0/24", "--availability-zone", "us-east-1a",
		"--query", "Subnet.SubnetId", "--output", "text")))
	sn2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.92.2.0/24", "--availability-zone", "us-east-1b",
		"--query", "Subnet.SubnetId", "--output", "text")))

	lbArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-rule-lb", "--type", "application", "--subnets", sn1, sn2,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")))
	tgArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group",
		"--name", "cli-rule-tg", "--protocol", "HTTP", "--port", "80", "--vpc-id", vpcID,
		"--target-type", "ip", "--query", "TargetGroups[0].TargetGroupArn", "--output", "text")))
	listenerArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-listener",
		"--load-balancer-arn", lbArn, "--protocol", "HTTP", "--port", "80",
		"--default-actions", "Type=forward,TargetGroupArn="+tgArn,
		"--query", "Listeners[0].ListenerArn", "--output", "text")))

	ruleArn := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-rule",
		"--listener-arn", listenerArn,
		"--conditions", "Field=host-header,Values=cli.example.com",
		"--priority", "50",
		"--actions", "Type=forward,TargetGroupArn="+tgArn,
		"--query", "Rules[0].RuleArn", "--output", "text")))
	// Real ELBv2 rule ARNs use the listener-rule/ resource prefix.
	if !strings.Contains(ruleArn, ":listener-rule/app/cli-rule-lb/") {
		t.Fatalf("expected an ELBv2 rule ARN, got %q", ruleArn)
	}

	rules := runCLI(t, awsCLI("elbv2", "describe-rules", "--listener-arn", listenerArn))
	if !strings.Contains(rules, ruleArn) || !strings.Contains(rules, "cli.example.com") {
		t.Fatalf("describe-rules missing rule or host condition: %s", rules)
	}

	// ModifyListener: swap default action to a fixed response.
	runCLI(t, awsCLI("elbv2", "modify-listener",
		"--listener-arn", listenerArn,
		"--default-actions", "Type=fixed-response,FixedResponseConfig={StatusCode=503,ContentType=text/plain}"))

	runCLI(t, awsCLI("elbv2", "delete-rule", "--rule-arn", ruleArn))
}
