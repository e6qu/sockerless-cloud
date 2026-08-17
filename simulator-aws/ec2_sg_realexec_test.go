//go:build realexec_host && linux

package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// TestEC2RealSecurityGroupHostFirewall verifies the host-firewall SG enforcement
// tier end-to-end: an ECS task's namespace NIC gets nftables ingress rules that
// match its security group's IpPermissions, applied via
// ec2ApplyRealECSTaskSecurityGroups. Runs only under -tags realexec_host on Linux
// (the CI real-exec job runs as root); the metadata-only tier is covered by the
// SDK validation/storage tests that run everywhere.
func TestEC2RealSecurityGroupHostFirewall(t *testing.T) {
	requireRealNetworkFabric(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ec2Vpcs = sim.MakeStore[EC2Vpc](nil, "ec2_vpcs")
	ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
	ec2SecurityGroups = sim.MakeStore[EC2SecurityGroup](nil, "ec2_security_groups")
	ec2SecurityGroupRules = sim.MakeStore[EC2SecurityGroupRule](nil, "ec2_security_group_rules")
	ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](nil, "ec2_network_interfaces")
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")

	const cidr = "10.220.0.0/16"
	vpc := EC2Vpc{VpcId: "vpc-sgfwhw", CidrBlock: cidr, State: "available"}
	ec2Vpcs.Put(vpc.VpcId, vpc)
	subnet := EC2Subnet{SubnetId: "subnet-sgfwhw", VpcId: vpc.VpcId, CidrBlock: "10.220.1.0/24", State: "available"}
	ec2Subnets.Put(subnet.SubnetId, subnet)

	if err := ec2CreateRealVPC(ctx, vpc); err != nil {
		t.Fatalf("ec2CreateRealVPC: %v", err)
	}
	if err := ec2CreateRealSubnet(ctx, subnet); err != nil {
		t.Fatalf("ec2CreateRealSubnet: %v", err)
	}
	t.Cleanup(func() {
		ec2RealMu.Lock()
		network := ec2RealVPCs[vpc.VpcId]
		ec2RealMu.Unlock()
		if network != nil {
			_ = network.Close(context.Background())
		}
	})

	const taskID = "task-sgfwhw"
	const eniIP = "10.220.1.10"
	ec2RealMu.Lock()
	sub := ec2RealSubnets[subnet.SubnetId]
	ec2RealMu.Unlock()
	if sub == nil {
		t.Fatal("real subnet not provisioned")
	}
	nic, err := sub.AttachNamespaceNIC(ctx, realexec.NamespaceNICSpec{
		NamespaceName: "ns-sgfwhw",
		HostVethName:  "nshcsgfwhw",
		GuestVethName: "nsgcsgfwhw",
		PrivateIP:     net.ParseIP(eniIP),
		MAC:           ec2ENIMAC(eniIP),
	})
	if err != nil {
		t.Fatalf("attach task namespace NIC: %v", err)
	}
	t.Cleanup(func() { _ = nic.Close(context.Background()) })

	ec2RealMu.Lock()
	ec2RealECSNICs[taskID] = nic
	ec2RealMu.Unlock()
	t.Cleanup(func() {
		ec2RealMu.Lock()
		delete(ec2RealECSNICs, taskID)
		ec2RealMu.Unlock()
	})

	sgID := "sg-sgfwhw"
	ec2SecurityGroups.Put(sgID, EC2SecurityGroup{
		GroupId: sgID,
		VpcId:   vpc.VpcId,
		OwnerId: ec2Owner(),
		IpPermissions: []EC2IpPermission{{
			IpProtocol: "tcp",
			FromPort:   8080,
			ToPort:     8080,
			IpRanges:   []EC2IpRange{{CidrIp: "10.220.1.0/24"}},
		}},
	})

	if err := ec2ApplyRealECSTaskSecurityGroups(ctx, taskID, []string{sgID}); err != nil {
		t.Fatalf("ec2ApplyRealECSTaskSecurityGroups: %v", err)
	}

	// The task NIC's namespace nftables ruleset must now match tcp dport 8080
	// for the source CIDR. Listing it directly through the realexec runner
	// mirrors how the NAT data-plane test verifies SNAT.
	ec2RealMu.Lock()
	network := ec2RealVPCs[vpc.VpcId]
	ec2RealMu.Unlock()
	if network == nil {
		t.Fatal("real VPC network namespace missing after SG application")
	}
	runner := realexec.Runner{}
	out, err := runner.Output(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "list", "ruleset")
	if err != nil {
		t.Fatalf("listing nft ruleset in VPC namespace %s: %v", network.NamespaceName, err)
	}
	ruleset := string(out)
	if !strings.Contains(ruleset, "8080") {
		t.Fatalf("SG ingress rule (tcp dport 8080) not found in VPC nftables ruleset:\n%s", ruleset)
	}

	// Reapplying after an Authorize (simulated by appending a new permission)
	// must update the live ruleset — the running task picks up SG changes
	// without restart.
	ec2SecurityGroups.Update(sgID, func(sg *EC2SecurityGroup) {
		sg.IpPermissions = append(sg.IpPermissions, EC2IpPermission{
			IpProtocol: "tcp",
			FromPort:   8443,
			ToPort:     8443,
			IpRanges:   []EC2IpRange{{CidrIp: "10.220.2.0/24"}},
		})
	})
	if err := ec2ReapplyRealSecurityGroup(ctx, sgID); err != nil {
		t.Fatalf("ec2ReapplyRealSecurityGroup after add: %v", err)
	}
	out, err = runner.Output(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "list", "ruleset")
	if err != nil {
		t.Fatalf("listing nft ruleset after reapply: %v", err)
	}
	if !strings.Contains(string(out), "8443") {
		t.Fatalf("reapplied SG ingress rule (tcp dport 8443) not found in VPC nftables ruleset:\n%s", string(out))
	}
}

// TestEC2RealSecurityGroupBuildRules covers the rule-expansion logic without
// touching real namespaces — it asserts ec2BuildIngressPacketRules expands CIDRs,
// IPv6, SG references, and the no-source case (0.0.0.0/0). Runs only on real-exec
// hosts since the package's realexec dependency is build-gated, but the function
// under test is pure Go.
func TestEC2RealSecurityGroupBuildRules(t *testing.T) {
	ec2SecurityGroups = sim.MakeStore[EC2SecurityGroup](nil, "ec2_security_groups")
	ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](nil, "ec2_network_interfaces")
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")

	webSG := "sg-buildrules-web"
	dbSG := "sg-buildrules-db"
	ec2SecurityGroups.Put(webSG, EC2SecurityGroup{
		GroupId: webSG,
		IpPermissions: []EC2IpPermission{
			{IpProtocol: "tcp", FromPort: 80, ToPort: 80, IpRanges: []EC2IpRange{{CidrIp: "0.0.0.0/0"}}},
			{IpProtocol: "tcp", FromPort: 22, ToPort: 22, Ipv6Ranges: []EC2Ipv6Range{{CidrIpv6: "2001:db8::/64"}}},
			{IpProtocol: "-1"},
		},
	})
	ec2SecurityGroups.Put(dbSG, EC2SecurityGroup{
		GroupId: dbSG,
		IpPermissions: []EC2IpPermission{{
			IpProtocol: "tcp", FromPort: 5432, ToPort: 5432,
			UserIdGroupPairs: []EC2UserIdGroupPair{{GroupId: webSG}},
		}},
	})
	// Web SG has one ENI at 10.0.0.10 — must be the source CIDR for dbSG's
	// reference rule.
	ec2NetworkInterfaces.Put("eni-web", EC2NetworkInterface{
		NetworkInterfaceId: "eni-web",
		PrivateIpAddress:   "10.0.0.10",
		SecurityGroupIds:   []string{webSG},
	})

	rules := ec2BuildIngressPacketRules([]string{webSG})
	requireRule(t, rules, "tcp", 80, 80, "0.0.0.0/0")
	requireRule(t, rules, "tcp", 22, 22, "2001:db8::/64")
	requireRule(t, rules, "-1", 0, 0, "0.0.0.0/0")

	dbRules := ec2BuildIngressPacketRules([]string{dbSG})
	requireRule(t, dbRules, "tcp", 5432, 5432, "10.0.0.10/32")
}

func requireRule(t *testing.T, rules []realexec.PacketRule, proto string, from, to int, cidr string) {
	t.Helper()
	for _, r := range rules {
		if r.Protocol == proto && r.FromPort == from && r.ToPort == to && r.SourceCIDR == cidr {
			return
		}
	}
	t.Fatalf("missing rule proto=%s ports=%d-%d cidr=%s in %+v", proto, from, to, cidr, rules)
}
