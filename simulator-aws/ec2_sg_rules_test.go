// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Three rules naming the same source group resolve its members once, not
// three times: membership is a scan over every ENI, instance and task in the
// store, and the packet rules it yields are identical for every rule.
func TestIngressRulesResolveASourceGroupOnce(t *testing.T) {
	AwaitSimulatorBackground()
	ec2SecurityGroups = sim.MakeStore[EC2SecurityGroup](nil, "ec2_security_groups")
	ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](nil, "ec2_network_interfaces")
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")

	ecsTasks.Put("member", sgMemberTask("member", "RUNNING", "10.42.0.10", "sg-tasks"))
	ec2SecurityGroups.Put("sg-workspaces", EC2SecurityGroup{
		GroupId: "sg-workspaces",
		IpPermissions: []EC2IpPermission{
			{IpProtocol: "tcp", FromPort: 3000, ToPort: 3000, UserIdGroupPairs: []EC2UserIdGroupPair{{GroupId: "sg-tasks"}}},
			{IpProtocol: "tcp", FromPort: 3001, ToPort: 3001, UserIdGroupPairs: []EC2UserIdGroupPair{{GroupId: "sg-tasks"}}},
			{IpProtocol: "tcp", FromPort: 22, ToPort: 22, UserIdGroupPairs: []EC2UserIdGroupPair{{GroupId: "sg-tasks"}}},
		},
	})

	rules := ec2BuildIngressPacketRules([]string{"sg-workspaces"})
	if len(rules) != 3 {
		t.Fatalf("rules = %+v, want one per port for the single member", rules)
	}
	for _, rule := range rules {
		if rule.SourceCIDR != "10.42.0.10/32" {
			t.Fatalf("rule source = %q, want the member's /32", rule.SourceCIDR)
		}
	}
}
