package aws_cli_test

import (
	"regexp"
	"strings"
	"testing"
)

// TestELBv2CLI_DNSNamePerType reads the DNS name `aws elbv2
// create-load-balancer` reports: an Application Load Balancer's is
// "<name>-<number>.<region>.elb.amazonaws.com", prefixed "internal-" when
// internal, and a Network Load Balancer's is
// "<name>-<id>.elb.<region>.amazonaws.com", where id ends its ARN.
func TestELBv2CLI_DNSNamePerType(t *testing.T) {
	t.Parallel()
	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.89.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subnet1 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.89.1.0/24",
		"--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")))
	subnet2 := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpcID, "--cidr-block", "10.89.2.0/24",
		"--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	create := func(name string, args ...string) (string, string) {
		t.Helper()
		out := strings.Fields(runCLI(t, awsCLI(append([]string{"elbv2", "create-load-balancer",
			"--name", name, "--subnets", subnet1, subnet2,
			"--query", "LoadBalancers[0].[LoadBalancerArn,DNSName]", "--output", "text"}, args...)...)))
		if len(out) != 2 {
			t.Fatalf("create-load-balancer answered %q", out)
		}
		t.Cleanup(func() { runCLI(t, awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", out[0])) })
		return out[0], out[1]
	}

	_, public := create("cli-dns-alb", "--type", "application")
	if !regexp.MustCompile(`^cli-dns-alb-[0-9]+\.us-east-1\.elb\.amazonaws\.com$`).MatchString(public) {
		t.Errorf("Application Load Balancer DNS name = %q", public)
	}
	_, internal := create("cli-dns-alb-int", "--type", "application", "--scheme", "internal")
	if !regexp.MustCompile(`^internal-cli-dns-alb-int-[0-9]+\.us-east-1\.elb\.amazonaws\.com$`).MatchString(internal) {
		t.Errorf("internal Application Load Balancer DNS name = %q", internal)
	}
	nlbArn, nlb := create("cli-dns-nlb", "--type", "network")
	id := nlbArn[strings.LastIndex(nlbArn, "/")+1:]
	if want := "cli-dns-nlb-" + id + ".elb.us-east-1.amazonaws.com"; nlb != want {
		t.Errorf("Network Load Balancer DNS name = %q, want %q", nlb, want)
	}
}
