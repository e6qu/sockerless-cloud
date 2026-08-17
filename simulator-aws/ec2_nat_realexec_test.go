//go:build realexec_host && linux

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// TestEC2RealNATGatewayDataPlane proves the NAT gateway is fully implemented —
// not just modeled at the control plane. On a real-execution host (root, with
// CAP_NET_ADMIN + nft), the sim's CreateNatGateway / CreateRoute wiring must
// build the real data plane: a namespace NIC for the gateway and an nftables
// SNAT (masquerade) rule in the VPC network namespace that rewrites traffic
// from the private subnet to the gateway's public IP.
//
// Runs only under `-tags realexec_host` on Linux (the CI real-exec job runs it
// as root); the control-plane path is covered by the SDK/CLI tests that run
// everywhere.
func TestEC2RealNATGatewayDataPlane(t *testing.T) {
	requireRealNetworkFabric(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Fresh in-memory stores for this test.
	ec2Vpcs = sim.MakeStore[EC2Vpc](nil, "ec2_vpcs")
	ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
	ec2NatGateways = sim.MakeStore[EC2NatGateway](nil, "ec2_nat_gateways")
	ec2RouteTables = sim.MakeStore[EC2RouteTable](nil, "ec2_route_tables")

	vpc := EC2Vpc{VpcId: "vpc-rnat", CidrBlock: "10.210.0.0/16", State: "available"}
	ec2Vpcs.Put(vpc.VpcId, vpc)
	subnet := EC2Subnet{SubnetId: "subnet-rnat", VpcId: vpc.VpcId, CidrBlock: "10.210.1.0/24", State: "available"}
	ec2Subnets.Put(subnet.SubnetId, subnet)

	if err := ec2CreateRealVPC(ctx, vpc); err != nil {
		t.Fatalf("ec2CreateRealVPC: %v", err)
	}
	if err := ec2CreateRealSubnet(ctx, subnet); err != nil {
		t.Fatalf("ec2CreateRealSubnet: %v", err)
	}
	// Tear the VPC network (and everything in its namespace) down last.
	t.Cleanup(func() {
		ec2RealMu.Lock()
		network := ec2RealVPCs[vpc.VpcId]
		ec2RealMu.Unlock()
		if network != nil {
			_ = network.Close(context.Background())
		}
	})

	const publicIP = "203.0.113.10"
	natgw := EC2NatGateway{
		NatGatewayId: "nat-rnat",
		SubnetId:     subnet.SubnetId,
		VpcId:        vpc.VpcId,
		State:        "available",
		NatGatewayAddresses: []EC2NatGatewayAddress{{
			AllocationId:       "eipalloc-rnat",
			PublicIp:           publicIP,
			PrivateIp:          "10.210.1.5",
			NetworkInterfaceId: "eni-rnat",
		}},
	}
	ec2NatGateways.Put(natgw.NatGatewayId, natgw)

	if err := ec2CreateRealNATGateway(ctx, natgw); err != nil {
		t.Fatalf("ec2CreateRealNATGateway built no real data plane: %v", err)
	}
	ec2RealMu.Lock()
	nic := ec2RealNATNICs[natgw.NatGatewayId]
	ec2RealMu.Unlock()
	if nic == nil {
		t.Fatal("ec2CreateRealNATGateway did not register a real namespace NIC for the gateway")
	}
	t.Cleanup(func() { _ = nic.Close(context.Background()) })

	// Default route from the private subnet through the NAT gateway programs
	// the real SNAT rule.
	rt := EC2RouteTable{
		RouteTableId: "rtb-rnat",
		VpcId:        vpc.VpcId,
		Associations: []EC2RouteTableAssociation{{SubnetId: subnet.SubnetId}},
	}
	ec2RouteTables.Put(rt.RouteTableId, rt)
	if err := ec2ConfigureRealNATRoute(ctx, rt.RouteTableId, "0.0.0.0/0", natgw.NatGatewayId); err != nil {
		t.Fatalf("ec2ConfigureRealNATRoute did not program real SNAT: %v", err)
	}

	// The SNAT (masquerade to the gateway public IP) must be live in the VPC
	// namespace's nftables ruleset.
	ec2RealMu.Lock()
	network := ec2RealVPCs[vpc.VpcId]
	ec2RealMu.Unlock()
	if network == nil {
		t.Fatal("real VPC network namespace missing after NAT route configuration")
	}
	runner := realexec.Runner{}
	out, err := runner.Output(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "list", "ruleset")
	if err != nil {
		t.Fatalf("listing nft ruleset in VPC namespace %s: %v", network.NamespaceName, err)
	}
	ruleset := string(out)
	if !strings.Contains(ruleset, "snat") || !strings.Contains(ruleset, publicIP) {
		t.Fatalf("SNAT rule to gateway public IP %s not found in VPC nftables ruleset:\n%s", publicIP, ruleset)
	}
}
