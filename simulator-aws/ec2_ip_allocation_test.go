package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestAllocateSubnetIPReusesStoppedECSTaskAddress(t *testing.T) {
	previousSubnets := ec2Subnets
	previousCursors := ec2SubnetIPCursor
	previousInterfaces := ec2NetworkInterfaces
	previousNATGateways := ec2NatGateways
	previousTasks := ecsTasks
	t.Cleanup(func() {
		ec2Subnets = previousSubnets
		ec2SubnetIPCursor = previousCursors
		ec2NetworkInterfaces = previousInterfaces
		ec2NatGateways = previousNATGateways
		ecsTasks = previousTasks
	})

	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
	ec2SubnetIPCursor = sim.MakeStore[uint32](nil, "ec2_subnet_ip_cursor")
	ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](nil, "ec2_network_interfaces")
	ec2NatGateways = sim.MakeStore[EC2NatGateway](nil, "ec2_nat_gateways")
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")

	const subnetID = "subnet-reclaim"
	ec2Subnets.Put(subnetID, EC2Subnet{
		SubnetId:  subnetID,
		CidrBlock: "10.42.0.0/29",
	})

	first, err := AllocateSubnetIP(subnetID)
	if err != nil {
		t.Fatalf("allocate Amazon ECS task address: %v", err)
	}
	if first != "10.42.0.4" {
		t.Fatalf("first address = %s, want 10.42.0.4", first)
	}
	task := ECSTask{
		TaskArn:    "arn:aws:ecs:us-east-1:123456789012:task/cluster/task-one",
		LastStatus: ECSTaskStatusRunning,
		Attachments: []ECSAttachment{{
			Type: "ElasticNetworkInterface",
			Details: []ECSKeyValuePair{
				{Name: "subnetId", Value: subnetID},
				{Name: "privateIPv4Address", Value: first},
			},
		}},
	}
	ecsTasks.Put("task-one", task)

	second, err := AllocateSubnetIP(subnetID)
	if err != nil {
		t.Fatalf("allocate network-interface address: %v", err)
	}
	ec2NetworkInterfaces.Put("eni-two", EC2NetworkInterface{
		NetworkInterfaceId: "eni-two",
		SubnetId:           subnetID,
		PrivateIpAddress:   second,
	})

	third, err := AllocateSubnetIP(subnetID)
	if err != nil {
		t.Fatalf("allocate NAT gateway address: %v", err)
	}
	ec2NatGateways.Put("nat-three", EC2NatGateway{
		NatGatewayId: "nat-three",
		SubnetId:     subnetID,
		NatGatewayAddresses: []EC2NatGatewayAddress{{
			PrivateIp: third,
		}},
	})

	if _, err := AllocateSubnetIP(subnetID); err == nil {
		t.Fatal("allocation unexpectedly succeeded while every usable address was owned")
	}

	task.LastStatus = ECSTaskStatusStopped
	ecsTasks.Put("task-one", task)
	reused, err := AllocateSubnetIP(subnetID)
	if err != nil {
		t.Fatalf("reuse stopped Amazon ECS task address: %v", err)
	}
	if reused != first {
		t.Fatalf("reused address = %s, want released task address %s", reused, first)
	}
}
