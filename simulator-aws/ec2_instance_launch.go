package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// ec2InstanceWorkload is an instance's machine on its placement backend: the
// handle while it runs, and the backend's identifier of the kept root disk
// while it is stopped, so a start boots the same disk and a terminate
// discards it.
type ec2InstanceWorkload struct {
	instanceID string
	address    string
	backend    placement.Backend
	workload   placement.Workload
	console    *ec2Console

	mu     sync.Mutex
	handle placement.Handle
	// id is the backend's identifier of the workload, which outlives the
	// handle of a stopped instance.
	id string
	// halted marks a stop or terminate; a handle that appears afterwards is
	// cancelled at once, so a boot still placing its machine cannot leave it
	// running behind a stopped instance.
	halted bool
}

var (
	ec2WorkloadsMu sync.Mutex
	ec2Workloads   = map[string]*ec2InstanceWorkload{}
)

// ec2ConsoleLimit is how much of the console EC2 keeps: the most recent 64 KB.
const ec2ConsoleLimit = 64 * 1024

// ec2Console keeps what an instance wrote to its console.
type ec2Console struct {
	mu  sync.Mutex
	buf []byte
}

func (c *ec2Console) WriteLog(line sim.LogLine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, line.Text...)
	c.buf = append(c.buf, '\n')
	if len(c.buf) > ec2ConsoleLimit {
		c.buf = c.buf[len(c.buf)-ec2ConsoleLimit:]
	}
}

func (c *ec2Console) Output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// ec2ConsoleOutput is what GetConsoleOutput reports for an instance: the
// machine's whole console as its backend keeps it, a stopped machine's
// included, and the most recent 64 KB of it. An instance that never booted in
// this process has none.
func ec2ConsoleOutput(ctx context.Context, instanceID string) string {
	ec2WorkloadsMu.Lock()
	w := ec2Workloads[instanceID]
	ec2WorkloadsMu.Unlock()
	if w == nil {
		return ""
	}
	w.mu.Lock()
	id := w.id
	w.mu.Unlock()
	if id == "" {
		return w.console.Output()
	}
	console, err := w.backend.Console(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: read console of %s: %v\n", instanceID, id, err)
		return w.console.Output()
	}
	if len(console) > ec2ConsoleLimit {
		console = console[len(console)-ec2ConsoleLimit:]
	}
	return console
}

// ec2InstanceReadyWait is the command that returns once the instance's init
// has finished the user data and raised its ready marker.
var ec2InstanceReadyWait = []string{"/bin/sh", "-c", "until [ -e /run/sockerless-instance-ready ]; do sleep 0.2; done"}

// ec2InstanceSpec is what the instance runs as: its AMI's root filesystem on
// its subnet's VPC network at its private address, resolving through the
// VPC's sidecar, with the user data and the metadata endpoint the SDKs read.
func ec2InstanceSpec(ctx context.Context, inst EC2Instance) (vminstance.Spec, awsWorkloadNetwork, error) {
	img, ok := ec2LookupImage(inst.ImageId)
	if !ok {
		return vminstance.Spec{}, awsWorkloadNetwork{}, fmt.Errorf("the image id '[%s]' does not exist", inst.ImageId)
	}
	rootfs, err := ec2ImageRootFS(img)
	if err != nil {
		return vminstance.Spec{}, awsWorkloadNetwork{}, fmt.Errorf("image %s: %w", inst.ImageId, err)
	}
	facts, ok := ec2LookupInstanceType(inst.InstanceType)
	if !ok {
		return vminstance.Spec{}, awsWorkloadNetwork{}, fmt.Errorf("the instance type %q does not exist", inst.InstanceType)
	}
	subnet, ok := ec2Subnets.Get(inst.SubnetId)
	if !ok {
		return vminstance.Spec{}, awsWorkloadNetwork{}, fmt.Errorf("the subnet %s of instance %s does not exist", inst.SubnetId, inst.InstanceId)
	}
	network, err := ec2EnsureVPCNetwork(ctx, subnet.VpcId)
	if err != nil {
		return vminstance.Spec{}, awsWorkloadNetwork{}, err
	}
	var userData []byte
	if inst.UserData != "" {
		userData, err = base64.StdEncoding.DecodeString(inst.UserData)
		if err != nil {
			return vminstance.Spec{}, awsWorkloadNetwork{}, fmt.Errorf("instance %s user data is not base64: %w", inst.InstanceId, err)
		}
	}
	spec := vminstance.Spec{
		Name:       "sockerless-sim-aws-instance-" + inst.InstanceId,
		ID:         inst.InstanceId,
		Hostname:   ec2PrivateDNSHostname(inst.PrivateIpAddress),
		Image:      rootfs,
		Network:    network.Name,
		Address:    inst.PrivateIpAddress,
		DNS:        network.DNS(),
		UserData:   userData,
		CloudInit:  ec2CloudInit(inst),
		Env:        workloadMetadataEnv(network.MetadataOrigin(), ""),
		VCPUs:      facts.VCpus,
		MemoryMiB:  facts.MemoryMiB,
		Labels:     map[string]string{"sockerless-sim-aws-instance": inst.InstanceId},
		ExtraHosts: append(workloadhost.ExtraHosts(), elbv2WorkloadExtraHosts()...),
		Ingress:    ec2IngressStages(inst.SecurityGroupIds),
		Gateway:    ec2SubnetGateway(inst.SubnetId),
	}
	return spec, network, nil
}

// ec2PrivateDNSHostname is the hostname an instance gets from its private
// address, ip-10-0-0-7 for 10.0.0.7.
func ec2PrivateDNSHostname(ip string) string {
	return "ip-" + strings.ReplaceAll(ip, ".", "-")
}

// ec2CloudInit is the AMI's cloud-init as Amazon EC2's datasource feeds it:
// the instance's id, and its key pair's public key for the default user.
func ec2CloudInit(inst EC2Instance) *vminstance.CloudInit {
	ci := &vminstance.CloudInit{Platform: "ec2", Datasource: "DataSourceEc2Local", InstanceID: inst.InstanceId}
	if inst.KeyName != "" {
		if kp, ok := ec2KeyPairs.Get(inst.KeyName); ok && kp.PublicKey != "" {
			ci.DefaultUserKeys = []string{kp.PublicKey}
		}
	}
	return ci
}

// ec2InstanceWorkloadSpec is the placement workload an instance runs as.
func ec2InstanceWorkloadSpec(spec vminstance.Spec) (placement.Workload, error) {
	return vminstance.Workload(spec)
}

// ec2BootInstance places a launched instance's machine and returns once it is
// up, or boots the kept machine of a stopped instance again. A variable so a
// unit test of the launch state machine supplies the boot.
var ec2BootInstance = func(ctx context.Context, inst EC2Instance) error {
	ec2WorkloadsMu.Lock()
	kept := ec2Workloads[inst.InstanceId]
	ec2WorkloadsMu.Unlock()
	if kept != nil && kept.id != "" {
		return kept.restart(ctx)
	}
	spec, _, err := ec2InstanceSpec(ctx, inst)
	if err != nil {
		return err
	}
	workload, err := ec2InstanceWorkloadSpec(spec)
	if err != nil {
		return err
	}
	backend, err := awsPlacement.For(placement.Service{Name: "ec2"})
	if err != nil {
		return err
	}
	w := &ec2InstanceWorkload{
		instanceID: inst.InstanceId,
		address:    inst.PrivateIpAddress,
		backend:    backend,
		workload:   workload,
		console:    &ec2Console{},
	}
	ec2WorkloadsMu.Lock()
	ec2Workloads[inst.InstanceId] = w
	ec2WorkloadsMu.Unlock()
	// The metadata service answers the instance from its first instruction:
	// the user data reads it before the init reports the instance ready.
	imdsInstancesByIP.Store(w.address, inst)
	handle, err := backend.Place(ctx, workload, w.console)
	if err != nil {
		ec2ForgetWorkload(w)
		imdsInstancesByIP.Delete(w.address)
		return err
	}
	if err := w.attach(ctx, handle); err != nil {
		return err
	}
	// The instance is a member of its groups now, which the permissions
	// naming them expand over on its neighbours.
	ec2ReapplyIngress(inst.VpcId)
	return nil
}

// restart boots the kept machine again.
func (w *ec2InstanceWorkload) restart(ctx context.Context) error {
	w.mu.Lock()
	w.halted = false
	id := w.id
	w.mu.Unlock()
	imdsInstancesByIP.Store(w.address, ec2InstanceRecord(w.instanceID))
	handle, err := w.backend.Restart(ctx, id, w.workload, w.console)
	if err != nil {
		imdsInstancesByIP.Delete(w.address)
		return err
	}
	return w.attach(ctx, handle)
}

// attach takes a placed handle, watches it and returns once the init is
// ready. A halt that arrived while the machine was placed cancels it here.
func (w *ec2InstanceWorkload) attach(ctx context.Context, handle placement.Handle) error {
	w.mu.Lock()
	w.handle = handle
	w.id = handle.ID()
	halted := w.halted
	w.mu.Unlock()
	bg.Go(func() { w.watch(handle) })
	if halted {
		handle.Cancel()
		<-handle.Exited()
		return context.Canceled
	}
	if err := w.awaitReady(ctx, handle); err != nil {
		if ctx.Err() == nil {
			handle.Cancel()
			<-handle.Exited()
			w.discard()
		}
		return err
	}
	return nil
}

func ec2InstanceRecord(instanceID string) EC2Instance {
	inst, _ := ec2Instances.Get(instanceID)
	return inst
}

// awaitReady returns once the init raised its ready marker, or with the
// reason it never will.
func (w *ec2InstanceWorkload) awaitReady(ctx context.Context, handle placement.Handle) error {
	done := make(chan error, 1)
	go func() {
		result, err := handle.Exec(ctx, ec2InstanceReadyWait)
		switch {
		case err != nil:
			done <- err
		case result.ExitCode != 0:
			done <- fmt.Errorf("the instance's ready wait exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
		default:
			done <- nil
		}
	}()
	select {
	case err := <-done:
		select {
		case <-handle.Exited():
			return fmt.Errorf("the instance's init exited %d before the instance was ready", handle.Wait().ExitCode)
		default:
		}
		return err
	case <-handle.Exited():
		return fmt.Errorf("the instance's init exited %d before the instance was ready", handle.Wait().ExitCode)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// watch follows the handle to its exit. An init that exits on its own is the
// instance shutting itself down, which an instance's default shutdown
// behaviour turns into a stop.
func (w *ec2InstanceWorkload) watch(handle placement.Handle) {
	<-handle.Exited()
	w.mu.Lock()
	halted := w.halted
	if w.handle == handle {
		w.handle = nil
	}
	w.mu.Unlock()
	imdsInstancesByIP.Delete(w.address)
	if halted {
		return
	}
	stopped := false
	ec2Instances.Update(w.instanceID, func(i *EC2Instance) {
		if i.State != "running" && i.State != "pending" {
			return
		}
		stopped = true
		i.State = "stopped"
		i.StateTransitionReason = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
		i.StateReasonCode = "Client.InstanceInitiatedShutdown"
		i.StateReasonMessage = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
	})
	if stopped {
		ec2UpdateVolumeAttachmentsForInstance(w.instanceID, "attached", "in-use")
	}
}

// halt stops the machine; keep leaves its root disk for a start.
func (w *ec2InstanceWorkload) halt(ctx context.Context, keep bool) {
	w.mu.Lock()
	w.halted = true
	handle := w.handle
	id := w.id
	w.mu.Unlock()
	if handle != nil {
		handle.Cancel()
		<-handle.Exited()
	}
	imdsInstancesByIP.Delete(w.address)
	if keep || id == "" {
		return
	}
	if err := w.backend.Remove(ctx, id); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: discard machine: %v\n", w.instanceID, err)
	}
}

// discard removes the machine and forgets it.
func (w *ec2InstanceWorkload) discard() {
	w.halt(context.Background(), false)
	ec2ForgetWorkload(w)
}

func ec2ForgetWorkload(w *ec2InstanceWorkload) {
	ec2WorkloadsMu.Lock()
	forgotten := ec2Workloads[w.instanceID] == w
	if forgotten {
		delete(ec2Workloads, w.instanceID)
	}
	ec2WorkloadsMu.Unlock()
	if forgotten {
		ec2ReapplyIngress(ec2InstanceRecord(w.instanceID).VpcId)
	}
}

// ec2StopWorkload stops an instance's machine and keeps its root disk; a
// boot still placing the machine finds it halted when it lands.
func ec2StopWorkload(ctx context.Context, instanceID string) {
	ec2WorkloadsMu.Lock()
	w := ec2Workloads[instanceID]
	ec2WorkloadsMu.Unlock()
	if w != nil {
		w.halt(ctx, true)
	}
}

// ec2DiscardWorkload stops an instance's machine and discards its root disk.
func ec2DiscardWorkload(ctx context.Context, instanceID string) {
	ec2WorkloadsMu.Lock()
	w := ec2Workloads[instanceID]
	delete(ec2Workloads, instanceID)
	ec2WorkloadsMu.Unlock()
	if w != nil {
		w.halt(ctx, false)
	}
}

// ec2WorkloadHandle is the handle of an instance's running machine, for a
// command Systems Manager runs on it.
func ec2WorkloadHandle(instanceID string) (placement.Handle, bool) {
	ec2WorkloadsMu.Lock()
	w := ec2Workloads[instanceID]
	ec2WorkloadsMu.Unlock()
	if w == nil {
		return nil, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.handle, w.handle != nil
}

// ec2RebootWorkload restarts an instance's machine in place: the init stops
// and boots again on the same root disk while the instance stays running.
func ec2RebootWorkload(ctx context.Context, instanceID string) error {
	ec2WorkloadsMu.Lock()
	w := ec2Workloads[instanceID]
	ec2WorkloadsMu.Unlock()
	if w == nil {
		return fmt.Errorf("instance %s runs no machine", instanceID)
	}
	w.mu.Lock()
	w.halted = true
	handle := w.handle
	w.mu.Unlock()
	if handle != nil {
		handle.Cancel()
		<-handle.Exited()
	}
	return w.restart(ctx)
}

// ec2CommitWorkload captures an instance's root disk as an OCI image under
// reference: the image an AMI created from the instance boots.
func ec2CommitWorkload(ctx context.Context, instanceID, reference string) error {
	ec2WorkloadsMu.Lock()
	w := ec2Workloads[instanceID]
	ec2WorkloadsMu.Unlock()
	if w == nil {
		return fmt.Errorf("instance %s has no root disk to capture", instanceID)
	}
	w.mu.Lock()
	id := w.id
	w.mu.Unlock()
	if id == "" {
		return fmt.Errorf("instance %s has no root disk to capture", instanceID)
	}
	return w.backend.Capture(ctx, id, reference)
}

// ec2Boot is a pending instance's boot, which stopping or terminating the
// instance cancels rather than waits out.
type ec2Boot struct {
	cancel context.CancelFunc
}

var (
	ec2BootMu sync.Mutex
	ec2Boots  = map[string]*ec2Boot{}
)

// ec2CancelBoot cancels the boot of an instance still launching, as EC2 stops
// a pending instance without waiting for it to come up.
func ec2CancelBoot(instanceID string) {
	ec2BootMu.Lock()
	defer ec2BootMu.Unlock()
	if boot, ok := ec2Boots[instanceID]; ok {
		boot.cancel()
	}
}

// ec2LaunchInstance boots a pending instance and reports it running only once
// its machine is up, calling booted just before. A failed boot terminates the
// instance with the Server.InternalError state reason EC2 reports for a launch
// that failed, and returns the boot error. launched is false when the instance
// is gone or was stopped or terminated while it booted.
func ec2LaunchInstance(instanceID string, booted func()) (launched bool, err error) {
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		return false, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	boot := &ec2Boot{cancel: cancel}
	ec2BootMu.Lock()
	ec2Boots[instanceID] = boot
	ec2BootMu.Unlock()
	defer func() {
		ec2BootMu.Lock()
		if ec2Boots[instanceID] == boot {
			delete(ec2Boots, instanceID)
		}
		ec2BootMu.Unlock()
		cancel()
	}()
	if err := ec2BootInstance(ctx, inst); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return false, nil
		}
		ec2Instances.Update(instanceID, func(i *EC2Instance) {
			i.State = "terminated"
			i.StateReasonCode = "Server.InternalError"
			i.StateReasonMessage = "Server.InternalError: Internal error on launch"
		})
		ec2NetworkInterfaces.Delete(inst.NetworkInterfaceId)
		ec2DeleteOnTerminationVolumes(instanceID)
		ec2DiscardWorkload(context.Background(), instanceID)
		return false, err
	}
	if current, ok := ec2Instances.Get(instanceID); !ok || current.State != "pending" {
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

// ec2StartInstance boots a stopped instance again: pending while its machine
// boots, running once the init is ready.
func ec2StartInstance(instanceID string) {
	bg.Go(func() {
		if _, err := ec2LaunchInstance(instanceID, nil); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: start: %v\n", instanceID, err)
		}
	})
}

// ec2FinishHalt stops or terminates an instance's machine in the background
// and records the final state once the machine is down.
func ec2FinishHalt(instanceID, next string) {
	bg.Go(func() {
		ctx := context.Background()
		if next == "terminated" {
			ec2DiscardWorkload(ctx, instanceID)
		} else {
			ec2StopWorkload(ctx, instanceID)
		}
		ec2Instances.Update(instanceID, func(i *EC2Instance) {
			switch {
			case next == "stopped" && i.State == "stopping":
				i.State = "stopped"
			case next == "terminated" && i.State == "shutting-down":
				i.State = "terminated"
			}
		})
	})
}

// recoverEC2Instances adopts the machines a previous simulator process left
// for the instances the store keeps, and settles the instances whose
// machines are gone.
func recoverEC2Instances() error {
	if !sim.HasPersistentWorkloadIdentity() {
		return nil
	}
	ctx := context.Background()
	backend, err := awsPlacement.For(placement.Service{Name: "ec2"})
	if err != nil {
		return err
	}
	for _, inst := range ec2Instances.List() {
		switch inst.State {
		case "pending", "running", "stopping", "stopped", "shutting-down":
		default:
			continue
		}
		existing, err := backend.Find(ctx, map[string]string{"sockerless-sim-instance": inst.InstanceId})
		if err != nil {
			return fmt.Errorf("find instance %s machine: %w", inst.InstanceId, err)
		}
		if len(existing) == 0 {
			ec2SettleInstanceWithoutMachine(inst)
			continue
		}
		if err := ec2AdoptInstanceMachine(ctx, inst, existing[0]); err != nil {
			return err
		}
	}
	return nil
}

func ec2SettleInstanceWithoutMachine(inst EC2Instance) {
	switch inst.State {
	case "pending":
		instanceID := inst.InstanceId
		bg.Go(func() {
			if _, err := ec2LaunchInstance(instanceID, nil); err != nil {
				fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: boot after restart: %v\n", instanceID, err)
			}
		})
	case "running", "stopping":
		ec2Instances.Update(inst.InstanceId, func(i *EC2Instance) {
			i.State = "stopped"
			i.StateTransitionReason = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
			i.StateReasonCode = "Client.InstanceInitiatedShutdown"
			i.StateReasonMessage = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
		})
	case "shutting-down":
		ec2Instances.Update(inst.InstanceId, func(i *EC2Instance) { i.State = "terminated" })
	}
}

// ec2AdoptInstanceMachine takes over a machine the previous process placed: a
// running one keeps the instance running, a stopped one is the kept root disk
// of a stopped instance, and a machine of an instance that was terminating
// is discarded.
func ec2AdoptInstanceMachine(ctx context.Context, inst EC2Instance, existing placement.Existing) error {
	spec, _, err := ec2InstanceSpec(ctx, inst)
	if err != nil {
		return fmt.Errorf("adopt instance %s: %w", inst.InstanceId, err)
	}
	workload, err := ec2InstanceWorkloadSpec(spec)
	if err != nil {
		return err
	}
	backend, err := awsPlacement.For(placement.Service{Name: "ec2"})
	if err != nil {
		return err
	}
	w := &ec2InstanceWorkload{
		instanceID: inst.InstanceId,
		address:    inst.PrivateIpAddress,
		backend:    backend,
		workload:   workload,
		console:    &ec2Console{},
		id:         existing.ID,
	}
	ec2WorkloadsMu.Lock()
	ec2Workloads[inst.InstanceId] = w
	ec2WorkloadsMu.Unlock()
	if inst.State == "shutting-down" {
		ec2DiscardWorkload(ctx, inst.InstanceId)
		ec2Instances.Update(inst.InstanceId, func(i *EC2Instance) { i.State = "terminated" })
		return nil
	}
	if !existing.Running {
		ec2Instances.Update(inst.InstanceId, func(i *EC2Instance) {
			if i.State != "stopped" {
				i.State = "stopped"
				i.StateReasonCode = "Client.InstanceInitiatedShutdown"
				i.StateReasonMessage = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
			}
		})
		return nil
	}
	handle, err := backend.Adopt(ctx, existing.ID, workload, w.console)
	if err != nil {
		return fmt.Errorf("adopt instance %s machine %s: %w", inst.InstanceId, existing.ID, err)
	}
	instanceID := inst.InstanceId
	bg.Go(func() {
		readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := w.attach(readyCtx, handle); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: adopted machine never became ready: %v\n", instanceID, err)
			return
		}
		ec2Instances.Update(instanceID, func(i *EC2Instance) {
			if i.State == "pending" || i.State == "stopping" {
				i.State = "running"
			}
		})
	})
	return nil
}
