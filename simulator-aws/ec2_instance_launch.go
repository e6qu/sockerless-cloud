package main

import (
	"context"
)

// ec2BootInstance boots a launched instance's VM on a real-execution host; on
// an API-only host the instance runs at the control plane alone.
var ec2BootInstance = func(ctx context.Context, inst EC2Instance) error {
	if !ec2RealVMHostAvailable() {
		return nil
	}
	return ec2StartRealVM(ctx, inst)
}

// ec2LaunchInstance boots a pending instance and reports it running only once
// its VM is up, calling booted just before. A failed boot terminates the
// instance with the Server.InternalError state reason EC2 reports for a launch
// that failed, and returns the boot error. launched is false when the instance
// is gone or was stopped or terminated while it booted; stopping it cancels the
// boot, as EC2 stops a pending instance without waiting for it to come up.
func ec2LaunchInstance(instanceID string, booted func()) (launched bool, err error) {
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		return false, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	boot := &ec2RealBoot{cancel: cancel}
	ec2RealMu.Lock()
	ec2RealBoots[instanceID] = boot
	ec2RealMu.Unlock()
	defer func() {
		ec2RealMu.Lock()
		if ec2RealBoots[instanceID] == boot {
			delete(ec2RealBoots, instanceID)
		}
		ec2RealMu.Unlock()
		cancel()
	}()
	if err := ec2BootInstance(ctx, inst); err != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		_ = ec2DeleteRealNIC(context.Background(), inst.NetworkInterfaceId)
		ec2Instances.Update(instanceID, func(i *EC2Instance) {
			i.State = "terminated"
			i.StateReasonCode = "Server.InternalError"
			i.StateReasonMessage = "Server.InternalError: Internal error on launch"
		})
		ec2NetworkInterfaces.Delete(inst.NetworkInterfaceId)
		ec2DeleteOnTerminationVolumes(instanceID)
		return false, err
	}
	if current, ok := ec2Instances.Get(instanceID); !ok || current.State != "pending" {
		_ = ec2StopRealVM(context.Background(), instanceID)
		return false, nil
	}
	if booted != nil {
		booted()
	}
	ec2Instances.Update(instanceID, func(i *EC2Instance) {
		if i.State == "pending" {
			i.State = "running"
		}
	})
	return true, nil
}
