package main

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Every proxied request resolves a target, so count task-store reads rather
// than timing.

type countingECSTaskStore struct {
	sim.Store[ECSTask]
	lists atomic.Int64
}

func (s *countingECSTaskStore) List() []ECSTask {
	s.lists.Add(1)
	return s.Store.List()
}

// Restores the package globals afterwards.
func elbv2ENIIndexTestStores(t *testing.T) *countingECSTaskStore {
	t.Helper()
	previousTasks, previousSubnets := ecsTasks, ec2Subnets
	counting := &countingECSTaskStore{Store: sim.MakeStore[ECSTask](nil, "ecs_tasks")}
	ecsTasks = counting
	ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
	resetIndex := func() {
		ecsRunningTaskENIIndex.mu.Lock()
		defer ecsRunningTaskENIIndex.mu.Unlock()
		ecsRunningTaskENIIndex.built = false
		ecsRunningTaskENIIndex.generation = 0
		ecsRunningTaskENIIndex.byAddress = nil
	}
	resetIndex()
	t.Cleanup(func() {
		ecsTasks, ec2Subnets = previousTasks, previousSubnets
		resetIndex()
	})
	return counting
}

func elbv2ENIIndexTask(taskID, address, subnetID string, status ECSTaskStatus) ECSTask {
	return ECSTask{
		TaskArn:    "arn:aws:ecs:us-east-1:123456789012:task/cluster/" + taskID,
		LastStatus: status,
		Attachments: []ECSAttachment{{
			Id:     "eni-attachment-" + taskID,
			Type:   "ElasticNetworkInterface",
			Status: "ATTACHED",
			Details: []ECSKeyValuePair{
				{Name: "subnetId", Value: subnetID},
				{Name: "privateIPv4Address", Value: address},
			},
		}},
	}
}

// A target group in another VPC never matches, so the path runs without a
// container runtime.
func TestELBv2TargetResolutionReadsTheTaskStoreOncePerChange(t *testing.T) {
	tasks := elbv2ENIIndexTestStores(t)
	ec2Subnets.Put("subnet-a", EC2Subnet{SubnetId: "subnet-a", VpcId: "vpc-a"})
	tasks.Put("task-1", elbv2ENIIndexTask("task-1", "10.0.1.5", "subnet-a", ECSTaskStatusRunning))

	readsAfterSetup := tasks.lists.Load()
	for i := 0; i < 25; i++ {
		port, ok := ecsPublishedTargetPort("10.0.1.5", 8080, "vpc-b")
		require.False(t, ok, "a target group in vpc-b must not match a task in vpc-a")
		require.Zero(t, port)
	}
	require.Equal(t, readsAfterSetup+1, tasks.lists.Load(),
		"twenty-five target resolutions with an unchanged task store must read it once")
}

func TestELBv2TargetENIIndexFollowsTheTaskStore(t *testing.T) {
	tasks := elbv2ENIIndexTestStores(t)
	ec2Subnets.Put("subnet-a", EC2Subnet{SubnetId: "subnet-a", VpcId: "vpc-a"})
	tasks.Put("task-1", elbv2ENIIndexTask("task-1", "10.0.1.5", "subnet-a", ECSTaskStatusRunning))

	found := ecsRunningTaskENIs("10.0.1.5")
	require.Len(t, found, 1)
	require.Equal(t, "task-1", found[0].taskID)
	require.Equal(t, "subnet-a", found[0].subnetID)

	// Two VPCs may share a CIDR; the second task is not shadowed.
	tasks.Put("task-2", elbv2ENIIndexTask("task-2", "10.0.1.5", "subnet-b", ECSTaskStatusRunning))
	require.Len(t, ecsRunningTaskENIs("10.0.1.5"), 2)

	// A stopped task leaves the index without explicit invalidation.
	tasks.Put("task-1", elbv2ENIIndexTask("task-1", "10.0.1.5", "subnet-a", ECSTaskStatusStopped))
	found = ecsRunningTaskENIs("10.0.1.5")
	require.Len(t, found, 1)
	require.Equal(t, "task-2", found[0].taskID)

	// So does a deleted one.
	require.True(t, tasks.Delete("task-2"))
	require.Empty(t, ecsRunningTaskENIs("10.0.1.5"))

	require.Empty(t, ecsRunningTaskENIs("10.0.9.9"))
}
