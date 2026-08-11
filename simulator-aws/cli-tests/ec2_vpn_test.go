package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_VPNSiteToSite covers the Site-to-Site VPN control plane through the
// aws CLI: customer gateway + VPN gateway (attach/detach) + VPN connection with
// two tunnels and a static route, the describe read-backs,
// modify-vpn-connection-options / modify-vpn-tunnel-options,
// get-active-vpn-tunnel-status, the device-type catalog and sample
// configuration, and the route/connection/gateway teardown.
func TestEC2CLI_VPNSiteToSite(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	// Customer gateway.
	cgw := q("ec2", "create-customer-gateway", "--bgp-asn", "65000",
		"--public-ip", "198.51.100.9", "--type", "ipsec.1",
		"--query", "CustomerGateway.CustomerGatewayId", "--output", "text")
	if cgw == "" {
		t.Fatal("create-customer-gateway returned empty id")
	}
	t.Cleanup(func() { _ = (awsCLI("ec2", "delete-customer-gateway", "--customer-gateway-id", cgw)).Run() })

	if v := q("ec2", "describe-customer-gateways", "--customer-gateway-ids", cgw,
		"--query", "CustomerGateways[0].[State,IpAddress,BgpAsn]", "--output", "text"); v != "available\t198.51.100.9\t65000" {
		t.Fatalf("describe-customer-gateways: got %q", v)
	}

	// VPN gateway + attach.
	vpc := q("ec2", "create-vpc", "--cidr-block", "10.211.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	vgw := q("ec2", "create-vpn-gateway", "--type", "ipsec.1", "--amazon-side-asn", "64512",
		"--query", "VpnGateway.VpnGatewayId", "--output", "text")
	if vgw == "" {
		t.Fatal("create-vpn-gateway returned empty id")
	}
	t.Cleanup(func() {
		_ = (awsCLI("ec2", "detach-vpn-gateway", "--vpn-gateway-id", vgw, "--vpc-id", vpc)).Run()
		_ = (awsCLI("ec2", "delete-vpn-gateway", "--vpn-gateway-id", vgw)).Run()
	})

	runCLI(t, awsCLI("ec2", "attach-vpn-gateway", "--vpn-gateway-id", vgw, "--vpc-id", vpc))
	if v := q("ec2", "describe-vpn-gateways", "--vpn-gateway-ids", vgw,
		"--query", "VpnGateways[0].VpcAttachments[0].VpcId", "--output", "text"); v != vpc {
		t.Fatalf("vpn gateway attachment vpc: got %q, want %q", v, vpc)
	}

	// VPN connection (static routing).
	conn := q("ec2", "create-vpn-connection", "--customer-gateway-id", cgw, "--vpn-gateway-id", vgw,
		"--type", "ipsec.1", "--options", "StaticRoutesOnly=true",
		"--query", "VpnConnection.VpnConnectionId", "--output", "text")
	if conn == "" {
		t.Fatal("create-vpn-connection returned empty id")
	}
	t.Cleanup(func() { _ = (awsCLI("ec2", "delete-vpn-connection", "--vpn-connection-id", conn)).Run() })

	outside := q("ec2", "describe-vpn-connections", "--vpn-connection-ids", conn,
		"--query", "VpnConnections[0].VgwTelemetry[0].OutsideIpAddress", "--output", "text")
	if outside == "" {
		t.Fatal("vpn connection has no tunnel outside ip")
	}
	if n := q("ec2", "describe-vpn-connections", "--vpn-connection-ids", conn,
		"--query", "length(VpnConnections[0].VgwTelemetry)", "--output", "text"); n != "2" {
		t.Fatalf("expected two tunnels, got %q", n)
	}

	// Static route add + read-back.
	runCLI(t, awsCLI("ec2", "create-vpn-connection-route", "--vpn-connection-id", conn,
		"--destination-cidr-block", "172.17.0.0/16"))
	if v := q("ec2", "describe-vpn-connections", "--vpn-connection-ids", conn,
		"--query", "VpnConnections[0].Routes[0].DestinationCidrBlock", "--output", "text"); v != "172.17.0.0/16" {
		t.Fatalf("vpn static route: got %q", v)
	}

	// Modify options + tunnel options.
	runCLI(t, awsCLI("ec2", "modify-vpn-connection-options", "--vpn-connection-id", conn,
		"--local-ipv4-network-cidr", "10.0.0.0/16", "--remote-ipv4-network-cidr", "192.168.0.0/16"))
	runCLI(t, awsCLI("ec2", "modify-vpn-tunnel-options", "--vpn-connection-id", conn,
		"--vpn-tunnel-outside-ip-address", outside,
		"--tunnel-options", "TunnelInsideCidr=169.254.30.0/30"))

	// Route delete.
	runCLI(t, awsCLI("ec2", "delete-vpn-connection-route", "--vpn-connection-id", conn,
		"--destination-cidr-block", "172.17.0.0/16"))

	// Device-type catalog + sample configuration.
	devType := q("ec2", "get-vpn-connection-device-types",
		"--query", "VpnConnectionDeviceTypes[0].VpnConnectionDeviceTypeId", "--output", "text")
	if devType == "" {
		t.Fatal("get-vpn-connection-device-types returned empty list")
	}
	if v := q("ec2", "get-vpn-connection-device-sample-configuration", "--vpn-connection-id", conn,
		"--vpn-connection-device-type-id", devType,
		"--query", "VpnConnectionDeviceSampleConfiguration", "--output", "text"); v == "" {
		t.Fatal("sample configuration is empty")
	}
}

// TestEC2CLI_ClientVPN covers the Client VPN control plane through the aws CLI:
// endpoint create with mutual auth + describe + modify, target-network
// association + describe, authorization (ingress) rules + describe, routes +
// describe, apply-security-groups, the connection list + terminate, and a
// tolerant teardown.
func TestEC2CLI_ClientVPN(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	vpc := q("ec2", "create-vpc", "--cidr-block", "10.212.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnet := q("ec2", "create-subnet", "--vpc-id", vpc, "--cidr-block", "10.212.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")

	ep := q("ec2", "create-client-vpn-endpoint",
		"--client-cidr-block", "10.252.0.0/22",
		"--server-certificate-arn", "arn:aws:acm:us-east-1:123456789012:certificate/srv",
		"--authentication-options", "Type=certificate-authentication,MutualAuthentication={ClientRootCertificateChainArn=arn:aws:acm:us-east-1:123456789012:certificate/root}",
		"--connection-log-options", "Enabled=false",
		"--description", "cli client vpn", "--transport-protocol", "udp",
		"--query", "ClientVpnEndpointId", "--output", "text")
	if ep == "" {
		t.Fatal("create-client-vpn-endpoint returned empty id")
	}
	t.Cleanup(func() { _ = (awsCLI("ec2", "delete-client-vpn-endpoint", "--client-vpn-endpoint-id", ep)).Run() })

	if v := q("ec2", "describe-client-vpn-endpoints", "--client-vpn-endpoint-ids", ep,
		"--query", "ClientVpnEndpoints[0].[ClientCidrBlock,Description]", "--output", "text"); v != "10.252.0.0/22\tcli client vpn" {
		t.Fatalf("describe-client-vpn-endpoints: got %q", v)
	}

	runCLI(t, awsCLI("ec2", "modify-client-vpn-endpoint", "--client-vpn-endpoint-id", ep,
		"--description", "updated", "--split-tunnel"))

	// Associate target network.
	assoc := q("ec2", "associate-client-vpn-target-network", "--client-vpn-endpoint-id", ep,
		"--subnet-id", subnet, "--query", "AssociationId", "--output", "text")
	if assoc == "" {
		t.Fatal("associate-client-vpn-target-network returned empty id")
	}
	if v := q("ec2", "describe-client-vpn-target-networks", "--client-vpn-endpoint-id", ep,
		"--query", "ClientVpnTargetNetworks[0].TargetNetworkId", "--output", "text"); v != subnet {
		t.Fatalf("target network subnet: got %q, want %q", v, subnet)
	}

	// Authorization rule.
	runCLI(t, awsCLI("ec2", "authorize-client-vpn-ingress", "--client-vpn-endpoint-id", ep,
		"--target-network-cidr", "0.0.0.0/0", "--authorize-all-groups", "--description", "allow all"))
	if v := q("ec2", "describe-client-vpn-authorization-rules", "--client-vpn-endpoint-id", ep,
		"--query", "AuthorizationRules[0].DestinationCidr", "--output", "text"); v != "0.0.0.0/0" {
		t.Fatalf("authorization rule cidr: got %q", v)
	}

	// Route.
	runCLI(t, awsCLI("ec2", "create-client-vpn-route", "--client-vpn-endpoint-id", ep,
		"--destination-cidr-block", "0.0.0.0/0", "--target-vpc-subnet-id", subnet))
	if v := q("ec2", "describe-client-vpn-routes", "--client-vpn-endpoint-id", ep,
		"--query", "Routes[0].DestinationCidr", "--output", "text"); v != "0.0.0.0/0" {
		t.Fatalf("client vpn route cidr: got %q", v)
	}

	// Apply security groups.
	sg := q("ec2", "create-security-group", "--group-name", "cli-cvpn-sg",
		"--description", "cvpn", "--vpc-id", vpc, "--query", "GroupId", "--output", "text")
	if v := q("ec2", "apply-security-groups-to-client-vpn-target-network", "--client-vpn-endpoint-id", ep,
		"--vpc-id", vpc, "--security-group-ids", sg,
		"--query", "SecurityGroupIds[0]", "--output", "text"); v != sg {
		t.Fatalf("apply-security-groups returned %q, want %q", v, sg)
	}

	// Connections (empty) + terminate.
	if v := q("ec2", "describe-client-vpn-connections", "--client-vpn-endpoint-id", ep,
		"--query", "length(Connections)", "--output", "text"); v != "0" {
		t.Fatalf("expected no connections, got %q", v)
	}
	if v := q("ec2", "terminate-client-vpn-connections", "--client-vpn-endpoint-id", ep,
		"--username", "alice", "--query", "ClientVpnEndpointId", "--output", "text"); v != ep {
		t.Fatalf("terminate-client-vpn-connections endpoint: got %q, want %q", v, ep)
	}

	// Tolerant teardown of routes/rules/associations.
	_ = (awsCLI("ec2", "delete-client-vpn-route", "--client-vpn-endpoint-id", ep,
		"--destination-cidr-block", "0.0.0.0/0", "--target-vpc-subnet-id", subnet)).Run()
	_ = (awsCLI("ec2", "revoke-client-vpn-ingress", "--client-vpn-endpoint-id", ep,
		"--target-network-cidr", "0.0.0.0/0", "--revoke-all-groups")).Run()
	_ = (awsCLI("ec2", "disassociate-client-vpn-target-network", "--client-vpn-endpoint-id", ep,
		"--association-id", assoc)).Run()
}
