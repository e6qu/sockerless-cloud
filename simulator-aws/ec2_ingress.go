package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/netrules"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// ec2IngressStages is what an interface in these security groups may
// receive: one stage of the groups' ingress permissions, expanded to
// prefixes, which drops what no permission names, as a security group does.
func ec2IngressStages(groupIDs []string) [][]netrules.PacketRule {
	return [][]netrules.PacketRule{ec2BuildIngressPacketRules(groupIDs)}
}

// ec2CreateDefaultSecurityGroup is the group every VPC comes with: named
// default, admitting all traffic from its own members and sending anything.
func ec2CreateDefaultSecurityGroup(vpcID string) {
	id := ec2ID("sg")
	sg := EC2SecurityGroup{
		GroupId:     id,
		GroupName:   "default",
		Description: "default VPC security group",
		VpcId:       vpcID,
		OwnerId:     ec2Owner(),
		IpPermissions: []EC2IpPermission{{
			IpProtocol:       "-1",
			UserIdGroupPairs: []EC2UserIdGroupPair{{GroupId: id}},
		}},
		IpPermissionsEgress: []EC2IpPermission{{
			IpProtocol: "-1",
			IpRanges:   []EC2IpRange{{CidrIp: "0.0.0.0/0"}},
		}},
	}
	ec2SecurityGroups.Put(id, sg)
	createSecurityGroupRules(id, sg.IpPermissions[0], false)
	createSecurityGroupRules(id, sg.IpPermissionsEgress[0], true)
}

// ec2DefaultSecurityGroupIDs is the VPC's default group, which an interface
// launched with no group joins.
func ec2DefaultSecurityGroupIDs(vpcID string) []string {
	for _, sg := range ec2SecurityGroups.Filter(func(sg EC2SecurityGroup) bool {
		return sg.VpcId == vpcID && sg.GroupName == "default"
	}) {
		return []string{sg.GroupId}
	}
	return nil
}

// ec2ReapplyIngress programs every running instance and awsvpc task of the
// VPC with what its groups allow now. A rule's change, a group's
// reassignment and a member's arrival or departure, which the permissions
// naming a group expand over, each change what a neighbour may receive. A
// backend that does not filter is left as it is.
func ec2ReapplyIngress(vpcID string) {
	if vpcID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, running := range ec2RunningMachines() {
		id, handle := running.Key, running.Handle
		inst := ec2InstanceRecord(id)
		if inst.VpcId != vpcID {
			continue
		}
		ec2SetIngress(ctx, "instance "+id, handle, ec2IngressStages(inst.SecurityGroupIds))
	}
	ecsProcessHandles.Range(func(key, value any) bool {
		taskID, ok := key.(string)
		if !ok {
			return true
		}
		procs, ok := value.(*ecsTaskProcesses)
		if !ok {
			return true
		}
		groups, taskVPC, ok := ecsTaskENIGroups(taskID)
		if !ok || taskVPC != vpcID {
			return true
		}
		if handle := procs.Handles[procs.MainContainerName]; handle != nil {
			ec2SetIngress(ctx, "task "+taskID, handle, ec2IngressStages(groups))
		}
		return true
	})
}

func ec2SetIngress(ctx context.Context, what string, handle placement.Handle, stages [][]netrules.PacketRule) {
	if err := handle.SetIngress(ctx, stages); err != nil && !errors.Is(err, placement.ErrNoIngressFilter) {
		fmt.Fprintf(os.Stderr, "[sim-ec2] %s: apply its security groups: %v\n", what, err)
	}
}

// ecsTaskENIGroups reads the security groups and the VPC of a task's awsvpc
// elastic network interface.
func ecsTaskENIGroups(taskID string) (groups []string, vpcID string, ok bool) {
	task, found := ecsTasks.Get(taskID)
	if !found {
		return nil, "", false
	}
	return ecsTaskENIGroupsOf(task)
}

func ecsTaskENIGroupsOf(task ECSTask) (groups []string, vpcID string, ok bool) {
	for _, att := range task.Attachments {
		if att.Type != "ElasticNetworkInterface" {
			continue
		}
		eniID := ecsTaskDetail(att.Details, "networkInterfaceId")
		eni, found := ec2NetworkInterfaces.Get(eniID)
		if !found {
			return nil, "", false
		}
		return eni.SecurityGroupIds, eni.VpcId, true
	}
	return nil, "", false
}

// ecsTaskSecurityGroups is the groups a task's elastic network interface
// joins: those the run named, else the VPC's default group, as Amazon ECS
// applies it.
func ecsTaskSecurityGroups(subnetID string, requested []string) []string {
	if len(requested) > 0 {
		return requested
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		return nil
	}
	return ec2DefaultSecurityGroupIDs(subnet.VpcId)
}
