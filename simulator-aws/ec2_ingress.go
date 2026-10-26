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

// One stage of the groups' ingress permissions expanded to prefixes; what no
// permission names is dropped, as a security group does.
func ec2IngressStages(groupIDs []string) [][]netrules.PacketRule {
	return [][]netrules.PacketRule{ec2BuildIngressPacketRules(groupIDs)}
}

// Every VPC comes with a group named default admitting all traffic from its
// own members and allowing all egress.
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

// An interface launched with no group joins the VPC's default group.
func ec2DefaultSecurityGroupIDs(vpcID string) []string {
	for _, sg := range ec2SecurityGroups.Filter(func(sg EC2SecurityGroup) bool {
		return sg.VpcId == vpcID && sg.GroupName == "default"
	}) {
		return []string{sg.GroupId}
	}
	return nil
}

// Rule changes, group reassignments and member arrivals or departures all
// change what a neighbour may receive. A backend that does not filter is left
// alone.
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

// The run's groups, else the VPC's default group, as Amazon ECS applies it.
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
