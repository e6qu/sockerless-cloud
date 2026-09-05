package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestRecoverEC2InstancesStopsInstancesWithoutBackingVMs(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	ec2Instances.Put("i-lost-running", EC2Instance{InstanceId: "i-lost-running", State: "running"})
	ec2Instances.Put("i-lost-pending", EC2Instance{InstanceId: "i-lost-pending", State: "pending"})
	ec2Instances.Put("i-already-stopped", EC2Instance{InstanceId: "i-already-stopped", State: "stopped"})
	ec2Instances.Put("i-alive", EC2Instance{InstanceId: "i-alive", State: "running"})

	recoverEC2InstancesWithVMLiveness(func(instanceID string) bool {
		return instanceID == "i-alive"
	})

	const wantReason = "Server.InternalError: Instance workload not found after control-plane restart"
	for _, id := range []string{"i-lost-running", "i-lost-pending"} {
		inst, ok := ec2Instances.Get(id)
		if !ok {
			t.Fatalf("instance %s missing after recovery", id)
		}
		if inst.State != "stopped" {
			t.Errorf("instance %s State = %q, want stopped", id, inst.State)
		}
		if inst.StateReasonCode != "Server.InternalError" {
			t.Errorf("instance %s StateReasonCode = %q, want Server.InternalError", id, inst.StateReasonCode)
		}
		if inst.StateReasonMessage != wantReason {
			t.Errorf("instance %s StateReasonMessage = %q, want %q", id, inst.StateReasonMessage, wantReason)
		}
		if inst.StateTransitionReason != wantReason {
			t.Errorf("instance %s StateTransitionReason = %q, want %q", id, inst.StateTransitionReason, wantReason)
		}
	}

	alive, ok := ec2Instances.Get("i-alive")
	if !ok {
		t.Fatal("instance i-alive missing after recovery")
	}
	if alive.State != "running" {
		t.Errorf("instance i-alive State = %q, want running (its VM is still alive)", alive.State)
	}
	if alive.StateReasonCode != "" || alive.StateTransitionReason != "" {
		t.Errorf("instance i-alive carries a state reason (%q / %q), want none",
			alive.StateReasonCode, alive.StateTransitionReason)
	}

	stopped, ok := ec2Instances.Get("i-already-stopped")
	if !ok {
		t.Fatal("instance i-already-stopped missing after recovery")
	}
	if stopped.State != "stopped" || stopped.StateReasonCode != "" {
		t.Errorf("already-stopped instance modified by recovery: State=%q StateReasonCode=%q",
			stopped.State, stopped.StateReasonCode)
	}
}
