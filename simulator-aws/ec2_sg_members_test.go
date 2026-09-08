// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"reflect"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func sgMemberTask(id, status, ip string, groups ...string) ECSTask {
	return ECSTask{
		TaskArn:    "arn:aws:ecs:us-east-1:123456789012:task/c/" + id,
		LastStatus: ECSTaskStatus(status),
		NetworkConfiguration: &ECSTaskNetworkConfig{
			AwsvpcConfiguration: &ECSTaskVpcConfig{SecurityGroups: groups},
		},
		Attachments: []ECSAttachment{{
			Type:    "ElasticNetworkInterface",
			Details: []ECSKeyValuePair{{Name: "privateIPv4Address", Value: ip}},
		}},
	}
}

// A security-group rule whose source is another group resolves to that
// group's members, one /32 each, and every one becomes a packet rule on the
// task being attached. A stopped task is not a member: its address is gone
// with its ENI, and the store only keeps it for DescribeTasks. On the e6qu
// deployment the scheduled reconciler stops every five minutes, so counting
// stopped tasks tripled the workspace task's rule count.
func TestSecurityGroupMembersExcludeStoppedTasks(t *testing.T) {
	AwaitSimulatorBackground()
	ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](nil, "ec2_network_interfaces")
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")

	const group = "sg-tasks"
	ecsTasks.Put("running-1", sgMemberTask("running-1", "RUNNING", "10.42.0.10", group))
	ecsTasks.Put("pending-1", sgMemberTask("pending-1", "PENDING", "10.42.0.11", group))
	ecsTasks.Put("stopped-1", sgMemberTask("stopped-1", "STOPPED", "10.42.0.12", group))
	ecsTasks.Put("stopped-2", sgMemberTask("stopped-2", "STOPPED", "10.42.0.13", group))
	ecsTasks.Put("other-group", sgMemberTask("other-group", "RUNNING", "10.42.0.14", "sg-other"))

	got := ec2SGMemberCIDRs(group)
	want := []string{"10.42.0.10/32", "10.42.0.11/32"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members of %s = %v, want %v (running and pending tasks only)", group, got, want)
	}
}
