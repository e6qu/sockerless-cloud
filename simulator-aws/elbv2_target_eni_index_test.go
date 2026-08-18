package main

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Resolving a load-balancer target must not cost a full read of the Amazon ECS
// task store. Every request the simulator proxies through a load balancer goes
// through elbv2TargetAddress, and a profile of the deployed simulator under
// twelve concurrent requests attributed 84.8% of all its CPU to that read —
// almost entirely JSON-decoding stopped tasks nobody asked about. The store is
// the input, so the test counts reads of it directly rather than timing
// anything.

// countingECSTaskStore counts List calls and delegates the rest, so a test can
// assert how many times a code path read the whole task store.
type countingECSTaskStore struct {
	sim.Store[ECSTask]
	lists atomic.Int64
}

func (s *countingECSTaskStore) List() []ECSTask {
	s.lists.Add(1)
	return s.Store.List()
}

// elbv2ENIIndexTestStores gives one test its own ECS task and EC2 subnet
// stores and an unbuilt ENI index, and restores the package globals after it.
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

// A target group scoped to a different VPC than the task's subnet never
// matches, which lets these tests exercise the whole resolution path without a
// container runtime: the Docker published-port lookup is only reached once the
// VPC agrees.
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

	// A second task on the same address — two VPCs may share a CIDR — is
	// reachable, not shadowed by the first.
	tasks.Put("task-2", elbv2ENIIndexTask("task-2", "10.0.1.5", "subnet-b", ECSTaskStatusRunning))
	require.Len(t, ecsRunningTaskENIs("10.0.1.5"), 2)

	// A task that stops leaves the index on the next resolution, without any
	// explicit invalidation call at the ECS lifecycle's end.
	tasks.Put("task-1", elbv2ENIIndexTask("task-1", "10.0.1.5", "subnet-a", ECSTaskStatusStopped))
	found = ecsRunningTaskENIs("10.0.1.5")
	require.Len(t, found, 1)
	require.Equal(t, "task-2", found[0].taskID)

	// So does a task that is deleted outright.
	require.True(t, tasks.Delete("task-2"))
	require.Empty(t, ecsRunningTaskENIs("10.0.1.5"))

	// An address no task holds is not an entry.
	require.Empty(t, ecsRunningTaskENIs("10.0.9.9"))
}
