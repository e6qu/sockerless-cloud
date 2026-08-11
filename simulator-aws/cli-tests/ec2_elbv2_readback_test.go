package aws_cli_test

import (
	"fmt"
	"strings"
	"testing"
)

// TestEC2CLI_LaunchTemplateProvenanceTags drives the launch-template system tags
// (aws:ec2launchtemplate:id/version) that the provider reads back.
func TestEC2CLI_LaunchTemplateProvenanceTags(t *testing.T) {
	lt := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-launch-template",
		"--launch-template-name", "cli-readback-lt",
		"--launch-template-data", `{"ImageId":"ami-0abc1234","InstanceType":"t4g.nano"}`,
		"--query", "LaunchTemplate.LaunchTemplateId", "--output", "text")))
	inst := strings.TrimSpace(runCLI(t, awsCLI("ec2", "run-instances",
		"--launch-template", "LaunchTemplateId="+lt+",Version=$Latest",
		"--query", "Instances[0].InstanceId", "--output", "text")))

	gotID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-instances",
		"--instance-ids", inst,
		"--query", "Reservations[0].Instances[0].Tags[?Key=='aws:ec2launchtemplate:id'].Value | [0]", "--output", "text")))
	if gotID != lt {
		t.Fatalf("aws:ec2launchtemplate:id tag = %q, want %q", gotID, lt)
	}
	gotImage := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-instances",
		"--instance-ids", inst, "--query", "Reservations[0].Instances[0].ImageId", "--output", "text")))
	if gotImage != "ami-0abc1234" {
		t.Fatalf("inherited ImageId = %q, want ami-0abc1234", gotImage)
	}
}

// TestEC2CLI_RouteNetworkInterfaceId drives the route NetworkInterfaceId round-trip.
func TestEC2CLI_RouteNetworkInterfaceId(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.44.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.44.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))
	eni := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-network-interface", "--subnet-id", sub, "--query", "NetworkInterface.NetworkInterfaceId", "--output", "text")))
	rt := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-route-table", "--vpc-id", vpc, "--query", "RouteTable.RouteTableId", "--output", "text")))
	runCLI(t, awsCLI("ec2", "create-route", "--route-table-id", rt, "--destination-cidr-block", "0.0.0.0/0", "--network-interface-id", eni))

	got := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-route-tables", "--route-table-ids", rt,
		"--query", "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].NetworkInterfaceId | [0]", "--output", "text")))
	if got != eni {
		t.Fatalf("route NetworkInterfaceId = %q, want %q", got, eni)
	}
}

// TestEC2CLI_SecurityGroupEgressIpv6Ranges drives the egress Ipv6Ranges round-trip.
func TestEC2CLI_SecurityGroupEgressIpv6Ranges(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.45.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sg := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group", "--group-name", "cli-ipv6-egress", "--description", "ipv6", "--vpc-id", vpc, "--query", "GroupId", "--output", "text")))
	// Revoke the default ALLOW ALL IPv4 egress rule before authorizing a
	// combined IPv4+IPv6 all-traffic rule.
	runCLI(t, awsCLI("ec2", "revoke-security-group-egress", "--group-id", sg,
		"--ip-permissions", `[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}]}]`))
	runCLI(t, awsCLI("ec2", "authorize-security-group-egress", "--group-id", sg,
		"--ip-permissions", `[{"IpProtocol":"-1","IpRanges":[{"CidrIp":"0.0.0.0/0"}],"Ipv6Ranges":[{"CidrIpv6":"::/0"}]}]`))

	got := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-groups", "--group-ids", sg,
		"--query", "SecurityGroups[0].IpPermissionsEgress[0].Ipv6Ranges[0].CidrIpv6", "--output", "text")))
	if got != "::/0" {
		t.Fatalf("egress Ipv6Ranges CidrIpv6 = %q, want ::/0", got)
	}
}

// TestELBv2CLI_ListenerSslPolicy drives the HTTPS listener SslPolicy round-trip.
func TestELBv2CLI_ListenerSslPolicy(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc", "--cidr-block", "10.46.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	subA := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.46.1.0/24", "--availability-zone", "us-east-1a", "--query", "Subnet.SubnetId", "--output", "text")))
	subB := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.46.2.0/24", "--availability-zone", "us-east-1b", "--query", "Subnet.SubnetId", "--output", "text")))
	lb := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-load-balancer", "--name", "cli-sslp-lb", "--type", "application", "--subnets", subA, subB, "--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")))
	tg := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "create-target-group", "--name", "cli-sslp-tg", "--protocol", "HTTP", "--port", "80", "--vpc-id", vpc, "--query", "TargetGroups[0].TargetGroupArn", "--output", "text")))
	cert := importELBv2CertificateCLI(t, "cli-ssl.example.test")
	port := availableELBv2ListenerPortCLI(t)
	const policy = "ELBSecurityPolicy-TLS13-1-2-2021-06"
	runCLI(t, awsCLI("elbv2", "create-listener", "--load-balancer-arn", lb, "--protocol", "HTTPS", "--port", port,
		"--ssl-policy", policy, "--certificates", "CertificateArn="+cert,
		"--default-actions", "Type=forward,TargetGroupArn="+tg))

	got := strings.TrimSpace(runCLI(t, awsCLI("elbv2", "describe-listeners", "--load-balancer-arn", lb,
		"--query", fmt.Sprintf("Listeners[?Port==`%s`].SslPolicy | [0]", port), "--output", "text")))
	if got != policy {
		t.Fatalf("listener SslPolicy = %q, want %q", got, policy)
	}
}
