package main

import (
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

func TestRecoverComputeInstancesTerminatesInstancesWithoutBackingVMs(t *testing.T) {
	instances := sim.MakeStore[ComputeInstance](nil, "test_compute_instances_recovery")

	running := ComputeInstance{
		Name:     "was-running",
		SelfLink: "projects/p/zones/us-central1-a/instances/was-running",
		Status:   ComputeInstanceRunning,
	}
	staging := ComputeInstance{
		Name:     "was-staging",
		SelfLink: "projects/p/zones/us-central1-a/instances/was-staging",
		Status:   ComputeInstanceStaging,
	}
	stopped := ComputeInstance{
		Name:     "already-stopped",
		SelfLink: "projects/p/zones/us-central1-a/instances/already-stopped",
		Status:   ComputeInstanceTerminated,
	}
	instances.Put(running.SelfLink, running)
	instances.Put(staging.SelfLink, staging)
	instances.Put(stopped.SelfLink, stopped)

	recoverComputeInstances(instances)

	for _, selfLink := range []string{running.SelfLink, staging.SelfLink} {
		inst, ok := instances.Get(selfLink)
		if !ok {
			t.Fatalf("instance %s missing after recovery", selfLink)
		}
		if inst.Status != ComputeInstanceTerminated {
			t.Errorf("instance %s Status = %s, want TERMINATED", selfLink, inst.Status)
		}
		if inst.StatusMessage == "" {
			t.Errorf("instance %s StatusMessage not set", selfLink)
		}
	}

	after, _ := instances.Get(stopped.SelfLink)
	if after.StatusMessage != "" {
		t.Errorf("already-terminated instance gained StatusMessage %q", after.StatusMessage)
	}
}
