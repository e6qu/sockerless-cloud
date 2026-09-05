package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
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
	// A restart during an insert catches the instance before STAGING.
	provisioning := ComputeInstance{
		Name:     "was-provisioning",
		SelfLink: "projects/p/zones/us-central1-a/instances/was-provisioning",
		Status:   ComputeInstanceProvisioning,
	}
	stopped := ComputeInstance{
		Name:     "already-stopped",
		SelfLink: "projects/p/zones/us-central1-a/instances/already-stopped",
		Status:   ComputeInstanceTerminated,
	}
	instances.Put(running.SelfLink, running)
	instances.Put(staging.SelfLink, staging)
	instances.Put(provisioning.SelfLink, provisioning)
	instances.Put(stopped.SelfLink, stopped)

	recoverComputeInstances(instances)

	for _, selfLink := range []string{running.SelfLink, staging.SelfLink, provisioning.SelfLink} {
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

// TestRecoverComputeOperationsSettlesOperationsCaughtMidFlight covers the other
// half of a restart during an instance insert: the goroutine carrying the boot
// died with the previous process, so an operation left RUNNING would never
// reach DONE and a client polling it would wait forever. Recovery finishes each
// one with the error, which is the only verdict the sim can honestly report.
func TestRecoverComputeOperationsSettlesOperationsCaughtMidFlight(t *testing.T) {
	computeOpRegistry = sim.MakeStore[ComputeOperationRecord](nil, "test_compute_operations_recovery")

	running := newComputeOpRecord("p", "zones/us-central1-a",
		"projects/p/zones/us-central1-a/instances/mid-boot", "insert")
	recordComputeOp(running)

	finished := newComputeOpRecord("p", "zones/us-central1-a",
		"projects/p/zones/us-central1-a/disks/already-done", "insert")
	finished.Status = "DONE"
	finished.Progress = 100
	finished.EndTime = "2026-01-01T00:00:00Z"
	recordComputeOp(finished)

	recoverComputeOperations()

	got, ok := computeOpRegistry.Get(running.Name)
	if !ok {
		t.Fatalf("operation %s missing after recovery", running.Name)
	}
	if got.Status != "DONE" {
		t.Errorf("Status = %s, want DONE", got.Status)
	}
	if got.ErrorCode == "" || got.ErrorMessage == "" {
		t.Errorf("an operation that never completed must carry its error, got %+v", got)
	}
	if got.EndTime == "" {
		t.Errorf("a finished operation carries its end time")
	}

	still, _ := computeOpRegistry.Get(finished.Name)
	if still.ErrorCode != "" {
		t.Errorf("an already-finished operation gained an error: %q", still.ErrorCode)
	}
	if still.EndTime != "2026-01-01T00:00:00Z" {
		t.Errorf("an already-finished operation's end time changed to %q", still.EndTime)
	}
}
