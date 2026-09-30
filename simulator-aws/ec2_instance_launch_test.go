package main

import (
	"context"
	"errors"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func ec2PendingInstance(t *testing.T) string {
	t.Helper()
	instanceID := ec2ID("i")
	eniID := ec2ID("eni")
	ec2NetworkInterfaces.Put(eniID, EC2NetworkInterface{NetworkInterfaceId: eniID, SubnetId: "subnet-asg", Status: "in-use"})
	ec2Instances.Put(instanceID, EC2Instance{InstanceId: instanceID, State: "pending", SubnetId: "subnet-asg", NetworkInterfaceId: eniID})
	return instanceID
}

// TestEC2RunInstancesRunsOnlyOnceBooted shows a launched instance stays
// pending while its VM boots and reports running once the boot completes.
func TestEC2RunInstancesRunsOnlyOnceBooted(t *testing.T) {
	asLaunchTestStores(t)
	booting := make(chan string, 1)
	release := make(chan struct{})
	ec2BootInstance = func(_ context.Context, inst EC2Instance) error {
		booting <- inst.InstanceId
		<-release
		return nil
	}

	instanceID := ec2PendingInstance(t)
	bg.Go(func() {
		if _, err := ec2LaunchInstance(instanceID, nil); err != nil {
			t.Errorf("launch: %v", err)
		}
	})
	if booted := <-booting; booted != instanceID {
		t.Fatalf("booted %s, want %s", booted, instanceID)
	}
	if inst, _ := ec2Instances.Get(instanceID); inst.State != "pending" {
		t.Fatalf("EC2 state while booting = %q, want pending", inst.State)
	}
	close(release)
	bg.Await()
	if inst, _ := ec2Instances.Get(instanceID); inst.State != "running" {
		t.Fatalf("EC2 state after boot = %q, want running", inst.State)
	}
}

// TestEC2RunInstancesTerminatesAnInstanceWhoseBootFails shows a failed boot
// terminates the instance with the Server.InternalError state reason and
// releases its network interface.
func TestEC2RunInstancesTerminatesAnInstanceWhoseBootFails(t *testing.T) {
	asLaunchTestStores(t)
	ec2BootInstance = func(context.Context, EC2Instance) error {
		return errors.New("guest never answered")
	}

	instanceID := ec2PendingInstance(t)
	if launched, err := ec2LaunchInstance(instanceID, nil); launched || err == nil {
		t.Fatalf("launch with a failing boot = %t, %v; want the boot error", launched, err)
	}
	inst, _ := ec2Instances.Get(instanceID)
	if inst.State != "terminated" || inst.StateReasonCode != "Server.InternalError" {
		t.Fatalf("instance after failed boot = %q / %q, want terminated / Server.InternalError", inst.State, inst.StateReasonCode)
	}
	if _, ok := ec2NetworkInterfaces.Get(inst.NetworkInterfaceId); ok {
		t.Fatalf("network interface %s outlived the failed launch", inst.NetworkInterfaceId)
	}
}
