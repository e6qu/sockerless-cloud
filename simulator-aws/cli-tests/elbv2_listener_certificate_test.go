package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2CLI_HTTPSListenerCertificate drives the HTTPS-listener certificate
// round-trip: the cert ARN attached at CreateListener must come back from
// DescribeListeners (the structured Certificates.member.N.CertificateArn parse).
func TestELBv2CLI_HTTPSListenerCertificate(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.84.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpc, "--cidr-block", "10.84.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))
	lb := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-load-balancer",
		"--name", "cli-https-lb", "--type", "application", "--subnets", sub,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")))
	tg := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group",
		"--name", "cli-https-tg", "--protocol", "HTTP", "--port", "80", "--vpc-id", vpc,
		"--query", "TargetGroups[0].TargetGroupArn", "--output", "text")))
	cert := importELBv2CertificateCLI(t, "cli-listener.example.test")
	port := availableELBv2ListenerPortCLI(t)

	runCLI(t, awsCLI("elbv2", "create-listener", "--load-balancer-arn", lb,
		"--protocol", "HTTPS", "--port", port,
		"--certificates", "CertificateArn="+cert,
		"--default-actions", "Type=forward,TargetGroupArn="+tg))

	got := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "describe-listeners",
		"--load-balancer-arn", lb,
		"--query", "Listeners[0].Certificates[0].CertificateArn", "--output", "text")))
	if got != cert {
		t.Fatalf("HTTPS listener CertificateArn = %q, want %q", got, cert)
	}
}
