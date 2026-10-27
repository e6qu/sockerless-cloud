package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/machine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// Amazon EC2 keeps the most recent 64 KB of console output.
const ec2ConsoleLimit = 64 * 1024

var (
	ec2MachinesMu sync.Mutex
	ec2Machines   *machine.Manager
	// The metadata service forgets a machine by this address once it is down.
	ec2MachineAddresses sync.Map
)

// Created at the first boot or recovery, once the backends are registered.
func ec2MachineManager() (*machine.Manager, error) {
	ec2MachinesMu.Lock()
	defer ec2MachinesMu.Unlock()
	if ec2Machines != nil {
		return ec2Machines, nil
	}
	backend, err := awsPlacement.For(placement.Service{Name: "ec2"})
	if err != nil {
		return nil, err
	}
	ec2Machines = machine.New(backend, machine.Hooks{
		Booting: ec2MachineBooting,
		Ready: func(id string) {
			ec2ReapplyIngress(ec2InstanceRecord(id).VpcId)
			ssmRegisterManagedNode(id)
		},
		Down:          ec2MachineDown,
		GuestShutdown: ec2InstanceShutDown,
		Forgotten:     func(id string) { ec2ReapplyIngress(ec2InstanceRecord(id).VpcId) },
	})
	return ec2Machines, nil
}

// nil until a machine was booted or adopted.
func ec2MachinesIfAny() *machine.Manager {
	ec2MachinesMu.Lock()
	defer ec2MachinesMu.Unlock()
	return ec2Machines
}

// The user data reads metadata before the init reports ready.
func ec2MachineBooting(instanceID string) {
	inst := ec2InstanceRecord(instanceID)
	ec2MachineAddresses.Store(instanceID, inst.PrivateIpAddress)
	imdsInstancesByIP.Store(inst.PrivateIpAddress, inst)
}

func ec2MachineDown(instanceID string) {
	if address, ok := ec2MachineAddresses.LoadAndDelete(instanceID); ok {
		if ip, ok := address.(string); ok {
			imdsInstancesByIP.Delete(ip)
		}
	}
}

// The init exited on its own; InstanceInitiatedShutdownBehavior makes that a
// stop or a termination.
func ec2InstanceShutDown(instanceID string) {
	halted, terminating := false, false
	var eni string
	ec2Instances.Update(instanceID, func(i *EC2Instance) {
		if i.State != "running" && i.State != "pending" {
			return
		}
		halted = true
		terminating = ec2ShutdownBehavior(*i) == "terminate"
		eni = i.NetworkInterfaceId
		i.State = "stopped"
		if terminating {
			i.State = "shutting-down"
		}
		i.StateTransitionReason = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
		i.StateReasonCode = "Client.InstanceInitiatedShutdown"
		i.StateReasonMessage = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
	})
	switch {
	case !halted:
	case terminating:
		if eni != "" {
			ec2NetworkInterfaces.Delete(eni)
		}
		ec2DeleteOnTerminationVolumes(instanceID)
		ec2FinishHalt(instanceID, "terminated")
	default:
		ec2UpdateVolumeAttachmentsForInstance(instanceID, "attached", "in-use")
	}
}

// Includes a stopped machine's console. An instance without a machine has
// none.
func ec2ConsoleOutput(ctx context.Context, instanceID string) (string, error) {
	machines := ec2MachinesIfAny()
	if machines == nil {
		return "", nil
	}
	console, err := machines.Console(ctx, instanceID)
	if errors.Is(err, machine.ErrNoMachine) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(console) > ec2ConsoleLimit {
		console = console[len(console)-ec2ConsoleLimit:]
	}
	return console, nil
}

// The AMI's root filesystem on the VPC network at the private address,
// resolving through the VPC's sidecar, with the user data and metadata
// endpoint.
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
		MAC:        ec2InstanceMAC(inst),
		DNS:        network.DNS(),
		UserData:   userData,
		CloudInit:  ec2CloudInit(inst),
		Env:        ec2MetadataEnv(network.MetadataOrigin()),
		VCPUs:      facts.VCpus,
		MemoryMiB:  facts.MemoryMiB,
		Labels:     map[string]string{"sockerless-sim-aws-instance": inst.InstanceId},
		ExtraHosts: append(workloadhost.ExtraHosts(), elbv2WorkloadExtraHosts()...),
		Ingress:    ec2IngressStages(inst.SecurityGroupIds),
		Gateway:    ec2SubnetGateway(inst.SubnetId),
	}
	return spec, network, nil
}

func ec2InstanceMAC(inst EC2Instance) string {
	if inst.NetworkInterfaceId == "" {
		return ""
	}
	return ec2InterfaceMAC(inst.NetworkInterfaceId)
}

// ec2.internal in us-east-1, {region}.compute.internal elsewhere.
func ec2PrivateDNSName(ip string) string {
	if awsRegion() == "us-east-1" {
		return ec2PrivateDNSHostname(ip) + ".ec2.internal"
	}
	return ec2PrivateDNSHostname(ip) + "." + awsRegion() + ".compute.internal"
}

// Only with a public address in a VPC with DNS hostnames on:
// compute-1.amazonaws.com in us-east-1, {region}.compute.amazonaws.com
// elsewhere.
func ec2PublicDNSName(inst EC2Instance) string {
	if inst.PublicIpAddress == "" {
		return ""
	}
	if vpc, ok := ec2Vpcs.Get(inst.VpcId); !ok || !vpc.EnableDnsHostnames {
		return ""
	}
	name := "ec2-" + strings.ReplaceAll(inst.PublicIpAddress, ".", "-")
	if awsRegion() == "us-east-1" {
		return name + ".compute-1.amazonaws.com"
	}
	return name + "." + awsRegion() + ".compute.amazonaws.com"
}

// ip-10-0-0-7 for 10.0.0.7.
func ec2PrivateDNSHostname(ip string) string {
	return "ip-" + strings.ReplaceAll(ip, ".", "-")
}

// Fed as Amazon EC2's cloud-init datasource does: the instance id and the key
// pair's public key for the default user.
func ec2CloudInit(inst EC2Instance) *vminstance.CloudInit {
	ci := &vminstance.CloudInit{Platform: "ec2", Datasource: "DataSourceEc2Local", InstanceID: inst.InstanceId}
	if inst.KeyName != "" {
		if kp, ok := ec2KeyPairs.Get(inst.KeyName); ok && kp.PublicKey != "" {
			ci.DefaultUserKeys = []string{kp.PublicKey}
		}
	}
	return ci
}

func ec2InstanceWorkload(ctx context.Context, inst EC2Instance) (placement.Workload, error) {
	spec, _, err := ec2InstanceSpec(ctx, inst)
	if err != nil {
		return placement.Workload{}, err
	}
	return vminstance.Workload(spec)
}

// A stopped instance's kept root disk boots again; any other instance is
// placed from its AMI. A unit test of the launch state machine replaces it.
var ec2BootInstance = func(ctx context.Context, inst EC2Instance) error {
	workload, err := ec2InstanceWorkload(ctx, inst)
	if err != nil {
		return err
	}
	machines, err := ec2MachineManager()
	if err != nil {
		return err
	}
	return machines.Boot(ctx, inst.InstanceId, workload)
}

func ec2InstanceRecord(instanceID string) EC2Instance {
	inst, _ := ec2Instances.Get(instanceID)
	return inst
}

func ec2HasKeptDisk(instanceID string) bool {
	machines := ec2MachinesIfAny()
	if machines == nil {
		return false
	}
	_, kept := machines.DiskID(instanceID)
	return kept
}

// Keeps the root disk; a boot in flight ends rather than being waited out.
func ec2StopWorkload(ctx context.Context, instanceID string) {
	if machines := ec2MachinesIfAny(); machines != nil {
		if err := machines.Halt(ctx, instanceID); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: stop machine: %v\n", instanceID, err)
		}
	}
}

func ec2DiscardWorkload(ctx context.Context, instanceID string) {
	if machines := ec2MachinesIfAny(); machines != nil {
		if err := machines.Discard(ctx, instanceID); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: discard machine: %v\n", instanceID, err)
		}
	}
}

// Systems Manager runs commands through this handle.
func ec2WorkloadHandle(instanceID string) (placement.Handle, bool) {
	machines := ec2MachinesIfAny()
	if machines == nil {
		return nil, false
	}
	return machines.Handle(instanceID)
}

func ec2RunningMachines() []machine.RunningMachine {
	machines := ec2MachinesIfAny()
	if machines == nil {
		return nil
	}
	return machines.Running()
}

// The instance stays running while the init restarts on the same root disk.
func ec2RebootWorkload(ctx context.Context, instanceID string) error {
	machines := ec2MachinesIfAny()
	if machines == nil {
		return fmt.Errorf("instance %s runs no machine", instanceID)
	}
	if _, running := machines.Handle(instanceID); !running {
		return fmt.Errorf("instance %s runs no machine", instanceID)
	}
	workload, err := ec2InstanceWorkload(ctx, ec2InstanceRecord(instanceID))
	if err != nil {
		return err
	}
	if err := machines.Halt(ctx, instanceID); err != nil {
		return err
	}
	return machines.Boot(ctx, instanceID, workload)
}

// An AMI created from the instance boots this image.
func ec2CommitWorkload(ctx context.Context, instanceID, reference string) error {
	machines := ec2MachinesIfAny()
	if machines == nil {
		return fmt.Errorf("instance %s has no root disk to capture", instanceID)
	}
	if err := machines.Capture(ctx, instanceID, reference); err != nil {
		if errors.Is(err, machine.ErrNoMachine) {
			return fmt.Errorf("instance %s has no root disk to capture", instanceID)
		}
		return err
	}
	return nil
}

// Stopping or terminating cancels the boot rather than waiting it out.
type ec2Boot struct {
	cancel context.CancelFunc
}

var (
	ec2BootMu sync.Mutex
	ec2Boots  = map[string]*ec2Boot{}
)

// Amazon EC2 stops a pending instance without waiting for it to come up.
func ec2CancelBoot(instanceID string) {
	ec2BootMu.Lock()
	defer ec2BootMu.Unlock()
	if boot, ok := ec2Boots[instanceID]; ok {
		boot.cancel()
	}
}

// Reports running only once the machine is up, calling booted just before. A
// failed launch terminates with the Server.InternalError state reason; a
// failed start leaves a stopped instance stopped with its root disk. launched
// is false when the instance is gone, or was stopped or terminated meanwhile.
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
	restart := ec2HasKeptDisk(instanceID)
	if err := ec2BootInstance(ctx, inst); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, machine.ErrHalted) {
			return false, nil
		}
		if restart {
			ec2Instances.Update(instanceID, func(i *EC2Instance) {
				if i.State == "pending" {
					i.State = "stopped"
					i.StateReasonCode = "Server.InternalError"
					i.StateReasonMessage = "Server.InternalError: Internal error on launch"
				}
			})
			return false, err
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

// pending while the machine boots, running once the init is ready.
func ec2StartInstance(instanceID string) {
	bg.Go(func() {
		if _, err := ec2LaunchInstance(instanceID, nil); err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: start: %v\n", instanceID, err)
		}
	})
}

// Records the final state once the machine is down.
func ec2FinishHalt(instanceID, next string) {
	bg.Go(func() {
		ctx := context.Background()
		if next == "terminated" {
			ec2DiscardWorkload(ctx, instanceID)
			ssmDeregisterTerminatedNode(instanceID)
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

// Adopt the machines a previous process left and settle instances whose
// machines are gone.
func recoverEC2Instances() error {
	if !sim.HasPersistentWorkloadIdentity() {
		return nil
	}
	ctx := context.Background()
	machines, err := ec2MachineManager()
	if err != nil {
		return err
	}
	for _, inst := range ec2Instances.List() {
		switch inst.State {
		case "pending", "running", "stopping", "stopped", "shutting-down":
		default:
			continue
		}
		existing, err := machines.Find(ctx, map[string]string{"sockerless-sim-instance": inst.InstanceId})
		if err != nil {
			return fmt.Errorf("find instance %s machine: %w", inst.InstanceId, err)
		}
		if len(existing) == 0 {
			ec2SettleInstanceWithoutMachine(inst)
			continue
		}
		if err := ec2AdoptInstanceMachine(ctx, machines, inst, existing[0]); err != nil {
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

// A running machine keeps the instance running, a stopped one is the kept
// root disk, and a terminating instance's machine is discarded.
func ec2AdoptInstanceMachine(ctx context.Context, machines *machine.Manager, inst EC2Instance, existing placement.Existing) error {
	workload, err := ec2InstanceWorkload(ctx, inst)
	if err != nil {
		return fmt.Errorf("adopt instance %s: %w", inst.InstanceId, err)
	}
	if existing.Running {
		ec2MachineBooting(inst.InstanceId)
	}
	found, ready, err := machines.Adopt(ctx, inst.InstanceId, &existing, workload)
	if err != nil {
		return fmt.Errorf("adopt instance %s machine %s: %w", inst.InstanceId, existing.ID, err)
	}
	switch {
	case inst.State == "shutting-down":
		ec2DiscardWorkload(ctx, inst.InstanceId)
		ec2Instances.Update(inst.InstanceId, func(i *EC2Instance) { i.State = "terminated" })
		return nil
	case inst.State == "stopping":
		ec2FinishHalt(inst.InstanceId, "stopped")
		return nil
	case found == machine.Kept:
		ec2Instances.Update(inst.InstanceId, func(i *EC2Instance) {
			if i.State != "stopped" {
				i.State = "stopped"
				i.StateReasonCode = "Client.InstanceInitiatedShutdown"
				i.StateReasonMessage = "Client.InstanceInitiatedShutdown: Instance initiated shutdown"
			}
		})
		return nil
	}
	instanceID := inst.InstanceId
	bg.Go(func() {
		if err := <-ready; err != nil {
			fmt.Fprintf(os.Stderr, "[sim-ec2] instance %s: adopted machine never became ready: %v\n", instanceID, err)
			return
		}
		ec2Instances.Update(instanceID, func(i *EC2Instance) {
			if i.State == "pending" {
				i.State = "running"
			}
		})
	})
	return nil
}
