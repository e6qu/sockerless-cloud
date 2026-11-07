package aws_cli_test

import (
	"strings"
	"testing"
)

// TestELBv2CLI_GatewayLoadBalancerEndpointIsARouteTarget expects a Gateway
// Load Balancer to hold an address in its subnet, a Gateway Load Balancer
// endpoint to hold one in its own subnet through a network interface of that
// type, a route table to take the endpoint as a route's target, and the load
// balancer's listener to carry no port or protocol.
func TestELBv2CLI_GatewayLoadBalancerEndpointIsARouteTarget(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	provider := q("ec2", "create-vpc", "--cidr-block", "10.33.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	applianceSubnet := q("ec2", "create-subnet", "--vpc-id", provider, "--cidr-block", "10.33.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")
	lbArn := q("elbv2", "create-load-balancer", "--name", "cli-gwlb", "--type", "gateway", "--subnets", applianceSubnet,
		"--query", "LoadBalancers[0].LoadBalancerArn", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn).Run() })
	lbAddress := q("ec2", "describe-network-interfaces",
		"--filters", "Name=subnet-id,Values="+applianceSubnet, "Name=interface-type,Values=gateway_load_balancer",
		"--query", "NetworkInterfaces[0].PrivateIpAddress", "--output", "text")
	if !strings.HasPrefix(lbAddress, "10.33.1.") {
		t.Fatalf("the Gateway Load Balancer's address in its subnet: got %q", lbAddress)
	}

	serviceID := q("ec2", "create-vpc-endpoint-service-configuration", "--gateway-load-balancer-arns", lbArn,
		"--no-acceptance-required", "--query", "ServiceConfiguration.ServiceId", "--output", "text")
	t.Cleanup(func() {
		_ = awsCLI("ec2", "delete-vpc-endpoint-service-configurations", "--service-ids", serviceID).Run()
	})
	serviceName := q("ec2", "describe-vpc-endpoint-service-configurations", "--service-ids", serviceID,
		"--query", "ServiceConfigurations[0].ServiceName", "--output", "text")

	consumer := q("ec2", "create-vpc", "--cidr-block", "10.34.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	applications := q("ec2", "create-subnet", "--vpc-id", consumer, "--cidr-block", "10.34.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")
	endpointSubnet := q("ec2", "create-subnet", "--vpc-id", consumer, "--cidr-block", "10.34.2.0/24", "--query", "Subnet.SubnetId", "--output", "text")
	endpointID := q("ec2", "create-vpc-endpoint", "--vpc-endpoint-type", "GatewayLoadBalancer", "--vpc-id", consumer,
		"--service-name", serviceName, "--subnet-ids", endpointSubnet, "--query", "VpcEndpoint.VpcEndpointId", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("ec2", "delete-vpc-endpoints", "--vpc-endpoint-ids", endpointID).Run() })
	eniID := q("ec2", "describe-vpc-endpoints", "--vpc-endpoint-ids", endpointID,
		"--query", "VpcEndpoints[0].NetworkInterfaceIds[0]", "--output", "text")
	got := q("ec2", "describe-network-interfaces", "--network-interface-ids", eniID,
		"--query", "NetworkInterfaces[0].[InterfaceType,SubnetId]", "--output", "text")
	if got != "gateway_load_balancer_endpoint\t"+endpointSubnet {
		t.Fatalf("the endpoint's network interface: got %q", got)
	}
	if address := q("ec2", "describe-network-interfaces", "--network-interface-ids", eniID,
		"--query", "NetworkInterfaces[0].PrivateIpAddress", "--output", "text"); !strings.HasPrefix(address, "10.34.2.") {
		t.Fatalf("the endpoint's address in its subnet: got %q", address)
	}

	// Each VPC's internet gateway answers a filter on the VPC it is attached to.
	for _, vpc := range []string{provider, consumer} {
		igw := q("ec2", "create-internet-gateway", "--query", "InternetGateway.InternetGatewayId", "--output", "text")
		runCLI(t, awsCLI("ec2", "attach-internet-gateway", "--internet-gateway-id", igw, "--vpc-id", vpc))
		if got := q("ec2", "describe-internet-gateways", "--filters", "Name=attachment.vpc-id,Values="+vpc,
			"--query", "InternetGateways[].InternetGatewayId", "--output", "text"); got != igw {
			t.Fatalf("describe-internet-gateways attached to %s: got %q, want %s", vpc, got, igw)
		}
	}

	routeTable := q("ec2", "create-route-table", "--vpc-id", consumer, "--query", "RouteTable.RouteTableId", "--output", "text")
	runCLI(t, awsCLI("ec2", "associate-route-table", "--route-table-id", routeTable, "--subnet-id", applications))
	runCLI(t, awsCLI("ec2", "create-route", "--route-table-id", routeTable, "--destination-cidr-block", "0.0.0.0/0",
		"--vpc-endpoint-id", endpointID))
	if target := q("ec2", "describe-route-tables", "--route-table-ids", routeTable,
		"--query", "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].GatewayId | [0]", "--output", "text"); target != endpointID {
		t.Fatalf("the route through the endpoint: got %q, want %s", target, endpointID)
	}

	runCLI(t, awsCLI("ec2", "delete-vpc-endpoints", "--vpc-endpoint-ids", endpointID))
	if left := q("ec2", "describe-network-interfaces", "--filters", "Name=subnet-id,Values="+endpointSubnet,
		"--query", "length(NetworkInterfaces)", "--output", "text"); left != "0" {
		t.Fatalf("deleting the endpoint left %s network interfaces in its subnet", left)
	}

	// A Gateway Load Balancer listener takes all IP packets on all ports, so it
	// has neither a port nor a protocol.
	targetGroup := q("elbv2", "create-target-group", "--name", "cli-gwlb-targets", "--protocol", "GENEVE", "--port", "6081",
		"--vpc-id", provider, "--query", "TargetGroups[0].TargetGroupArn", "--output", "text")
	t.Cleanup(func() { _ = awsCLI("elbv2", "delete-target-group", "--target-group-arn", targetGroup).Run() })
	listener := q("elbv2", "create-listener", "--load-balancer-arn", lbArn,
		"--default-actions", "Type=forward,TargetGroupArn="+targetGroup, "--query", "Listeners[0].ListenerArn", "--output", "text")
	if got := q("elbv2", "describe-listeners", "--listener-arns", listener,
		"--query", "Listeners[0].[Port,Protocol]", "--output", "text"); got != "None\tNone" {
		t.Fatalf("the Gateway Load Balancer listener's port and protocol: got %q, want none", got)
	}
}
