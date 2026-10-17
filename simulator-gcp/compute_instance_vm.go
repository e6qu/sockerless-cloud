package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

// gcpInstanceMachine is the machine behind a Compute Engine instance: the
// placed workload, the network it sits on and the address it holds.
type gcpInstanceMachine struct {
	backend placement.Backend
	handle  placement.Handle
	network gcpWorkloadNetwork
	address string
	// halting records that the simulator is stopping the machine, so the
	// watcher that sees it exit does not read a guest shutdown into it.
	mu      sync.Mutex
	halting bool
}

// gcpInstanceMachines holds the machine of every running instance by the
// instance's self link; gcpInstanceKeptMachines the backend ID of every
// stopped or suspended instance's machine, kept with its disk for the next
// start.
var (
	gcpInstanceMachines     sync.Map
	gcpInstanceKeptMachines sync.Map
)

// gcpInstanceStopGrace is how long Compute Engine gives a guest to shut down
// on instances.stop before it powers the machine off.
const gcpInstanceStopGrace = 90 * time.Second

// gcpInstanceReadyPoll is how often a booting instance is asked whether its
// init has come up.
const gcpInstanceReadyPoll = 250 * time.Millisecond

// gcpStartupScriptPath is where the instance's startup-script metadata is
// placed for the script runner, as google_metadata_script_runner writes the
// script it fetched before running it.
const gcpStartupScriptPath = "/tmp/metadata-scripts/startup-script"

// gcpStartupScriptRunner is the user data every instance boots with: what
// google_metadata_script_runner does at startup, with the script's output
// under the startup-script prefix the guest's journal forwards to the serial
// console. The init carries the runner's output to the console.
const gcpStartupScriptRunner = `#!/bin/sh
script=` + gcpStartupScriptPath + `
status=/run/google-startup-script.status
if [ -s "$script" ]; then
	echo "google_metadata_script_runner: Starting startup scripts."
	chmod 700 "$script"
	( "$script" 2>&1; echo "$?" >"$status" ) | while IFS= read -r line; do
		echo "google_metadata_script_runner: startup-script: $line"
	done
	echo "google_metadata_script_runner: startup-script exit status $(cat "$status")"
	echo "google_metadata_script_runner: Finished running startup scripts."
else
	echo "google_metadata_script_runner: No startup scripts to run."
fi
`

// gcpInstanceMetadataValue reads one key of an instance's metadata.
func gcpInstanceMetadataValue(inst ComputeInstance, key string) (string, bool) {
	if inst.Metadata == nil {
		return "", false
	}
	for _, item := range inst.Metadata.Items {
		if item.Key == key {
			return item.Value, true
		}
	}
	return "", false
}

// gcpInstanceWorkload is the workload an instance runs as on its network.
func gcpInstanceWorkload(inst ComputeInstance, netw gcpWorkloadNetwork, image gcpResolvedImage, shape gcpMachineType) (placement.Workload, error) {
	project := gcpProjectFromSelfLink(inst.SelfLink)
	zone := inst.Zone[strings.LastIndex(inst.Zone, "/")+1:]
	fqdn := fmt.Sprintf("%s.%s.c.%s.internal", inst.Name, zone, project)
	startup, _ := gcpInstanceMetadataValue(inst, "startup-script")
	spec := vminstance.Spec{
		Name:         gcpInstanceContainerName(inst),
		ID:           inst.SelfLink,
		Hostname:     inst.Name,
		Image:        image.Image,
		Network:      netw.Network,
		Address:      netw.Address,
		DNS:          netw.dns(),
		UserData:     []byte(gcpStartupScriptRunner),
		UserDataPath: "/run/google-startup-script-runner",
		// Compute Engine runs startup-script at every boot, not once.
		UserDataEveryBoot: true,
		Env:               netw.env(),
		Seed:              []sim.SeedFile{{Path: gcpStartupScriptPath, Mode: 0o700, Data: []byte(startup)}},
		VCPUs:             shape.GuestCpus,
		MemoryMiB:         shape.MemoryMb,
		Labels:            map[string]string{"sockerless-gcp-instance-project": project},
		StopGrace:         gcpInstanceStopGrace,
		ExtraHosts:        append(netw.extraHosts(), fqdn+":"+netw.Address),
	}
	w, err := vminstance.Workload(spec)
	if err != nil {
		return placement.Workload{}, err
	}
	// A shared-core machine type holds a fraction of the vCPUs it reports.
	w.NanoCPU = int64(shape.CPUs * 1_000_000_000)
	return w, nil
}

func gcpInstanceContainerName(inst ComputeInstance) string {
	zone := inst.Zone[strings.LastIndex(inst.Zone, "/")+1:]
	return fmt.Sprintf("sockerless-sim-gce-%s-%s-%s", gcpProjectFromSelfLink(inst.SelfLink), zone, inst.Name)
}

// gcpInstanceNetwork is the engine network an instance's first interface
// attaches to, with the sidecar the interface's subnetwork reserves.
func gcpInstanceNetwork(ctx context.Context, inst *ComputeInstance) (gcpWorkloadNetwork, error) {
	if len(inst.NetworkInterfaces) == 0 {
		return gcpWorkloadNetwork{}, errors.New("the instance has no network interface")
	}
	nic := &inst.NetworkInterfaces[0]
	if gcpSubnetworks == nil {
		return gcpWorkloadNetwork{}, fmt.Errorf("subnetwork %s not found", nic.Subnetwork)
	}
	subnet, ok := gcpSubnetworks.Get(nic.Subnetwork)
	if !ok {
		return gcpWorkloadNetwork{}, fmt.Errorf("subnetwork %s not found", nic.Subnetwork)
	}
	if nic.NetworkIP == "" {
		if err := gcpLeaseInstanceAddress(inst, 0); err != nil {
			return gcpWorkloadNetwork{}, err
		}
	}
	netw, err := gcpEnsureSubnetworkNetwork(ctx, subnet)
	if err != nil {
		return gcpWorkloadNetwork{}, err
	}
	netw.Address = nic.NetworkIP
	return netw, nil
}

// gcpBootInstanceMachine brings an instance's machine up: a new one from its
// boot image, or the kept one of a stopped instance on the same disk. It
// returns once the instance's init is up, which is when Compute Engine
// reports RUNNING.
func gcpBootInstanceMachine(ctx context.Context, inst *ComputeInstance) error {
	if _, running := gcpInstanceMachines.Load(inst.SelfLink); running {
		return nil
	}
	netw, err := gcpInstanceNetwork(ctx, inst)
	if err != nil {
		return err
	}
	image, err := gcpInstanceBootImage(*inst)
	if err != nil {
		return err
	}
	shape, ok := gcpLookupMachineType(inst.MachineType)
	if !ok {
		return fmt.Errorf("machine type %s does not exist", inst.MachineType)
	}
	backend, err := gcpPlacement.For(placementCompute)
	if err != nil {
		return err
	}
	workload, err := gcpInstanceWorkload(*inst, netw, image, shape)
	if err != nil {
		return err
	}
	// The guest reads its metadata from the first moment it boots, so the
	// instance is known by its address before the machine starts.
	gcpMetadataInstancesByIP.Store(netw.Address, *inst)
	var handle placement.Handle
	if kept, ok := gcpKeptInstanceMachine(inst.SelfLink); ok {
		handle, err = backend.Restart(ctx, kept, workload, sim.NoopSink{})
	} else {
		handle, err = backend.Place(ctx, workload, sim.NoopSink{})
	}
	if err != nil {
		gcpMetadataInstancesByIP.Delete(netw.Address)
		return err
	}
	if err := gcpAwaitInstanceReady(ctx, handle); err != nil {
		gcpMetadataInstancesByIP.Delete(netw.Address)
		if removeErr := backend.Remove(context.WithoutCancel(ctx), handle.ID()); removeErr != nil {
			log.Printf("compute: remove the machine of %s after a failed boot: %v", inst.SelfLink, removeErr)
		}
		gcpInstanceKeptMachines.Delete(inst.SelfLink)
		return err
	}
	machine := &gcpInstanceMachine{backend: backend, handle: handle, network: netw, address: netw.Address}
	gcpInstanceMachines.Store(inst.SelfLink, machine)
	gcpInstanceKeptMachines.Delete(inst.SelfLink)
	go gcpWatchInstanceMachine(inst.SelfLink, machine)
	return nil
}

// gcpAwaitInstanceReady waits for the instance's init to report itself up,
// or for the machine to exit first, which is a boot that failed.
func gcpAwaitInstanceReady(ctx context.Context, handle placement.Handle) error {
	ticker := time.NewTicker(gcpInstanceReadyPoll)
	defer ticker.Stop()
	for {
		select {
		case <-handle.Exited():
			result := handle.Wait()
			return fmt.Errorf("the instance's init exited with status %d before it came up", result.ExitCode)
		case <-ctx.Done():
			return fmt.Errorf("the instance did not come up: %w", ctx.Err())
		case <-ticker.C:
		}
		res, err := handle.Exec(ctx, []string{"test", "-e", "/run/sockerless-instance-ready"})
		if err == nil && res.ExitCode == 0 {
			return nil
		}
	}
}

// gcpWatchInstanceMachine moves an instance whose guest shut itself down to
// TERMINATED, keeping its disk for the next start.
func gcpWatchInstanceMachine(link string, machine *gcpInstanceMachine) {
	<-machine.handle.Exited()
	machine.mu.Lock()
	halting := machine.halting
	machine.mu.Unlock()
	if halting {
		return
	}
	if current, ok := gcpInstanceMachines.Load(link); !ok || current != machine {
		return
	}
	gcpInstanceMachines.Delete(link)
	gcpInstanceKeptMachines.Store(link, machine.handle.ID())
	gcpMetadataInstancesByIP.Delete(machine.address)
	if gcpInstances != nil {
		gcpInstances.Update(link, func(inst *ComputeInstance) {
			if inst.Status == ComputeInstanceRunning {
				inst.Status = ComputeInstanceTerminated
			}
		})
	}
}

// gcpHaltInstanceMachine stops an instance's machine and keeps it, disk and
// all, for a later start or resume.
func gcpHaltInstanceMachine(_ context.Context, inst *ComputeInstance) error {
	machine, ok := gcpRunningInstanceMachine(inst.SelfLink)
	if !ok {
		return nil
	}
	machine.mu.Lock()
	machine.halting = true
	machine.mu.Unlock()
	machine.handle.Cancel()
	<-machine.handle.Exited()
	gcpInstanceMachines.Delete(inst.SelfLink)
	gcpInstanceKeptMachines.Store(inst.SelfLink, machine.handle.ID())
	gcpMetadataInstancesByIP.Delete(machine.address)
	return nil
}

// gcpDestroyInstanceMachine removes an instance's machine, running or kept.
func gcpDestroyInstanceMachine(ctx context.Context, inst *ComputeInstance) error {
	if err := gcpHaltInstanceMachine(ctx, inst); err != nil {
		return err
	}
	kept, ok := gcpKeptInstanceMachine(inst.SelfLink)
	if !ok {
		return nil
	}
	gcpInstanceKeptMachines.Delete(inst.SelfLink)
	backend, err := gcpPlacement.For(placementCompute)
	if err != nil {
		return err
	}
	return backend.Remove(ctx, kept)
}

// gcpRunningInstanceMachine is the machine of a running instance.
func gcpRunningInstanceMachine(link string) (*gcpInstanceMachine, bool) {
	v, ok := gcpInstanceMachines.Load(link)
	if !ok {
		return nil, false
	}
	machine, ok := v.(*gcpInstanceMachine)
	return machine, ok
}

// gcpKeptInstanceMachine is the backend ID of a stopped instance's kept
// machine.
func gcpKeptInstanceMachine(link string) (string, bool) {
	v, ok := gcpInstanceKeptMachines.Load(link)
	if !ok {
		return "", false
	}
	id, ok := v.(string)
	return id, ok
}

// gcpInstanceMachineID is the backend ID of an instance's machine, running
// or kept.
func gcpInstanceMachineID(link string) (string, bool) {
	if machine, ok := gcpRunningInstanceMachine(link); ok {
		return machine.handle.ID(), true
	}
	return gcpKeptInstanceMachine(link)
}

// gcpCaptureInstanceDisk commits the instance's machine as an engine image
// under reference: the image an instance created from the captured image
// boots from.
func gcpCaptureInstanceDisk(ctx context.Context, inst ComputeInstance, reference string) error {
	id, ok := gcpInstanceMachineID(inst.SelfLink)
	if !ok {
		return fmt.Errorf("instance %s has no machine to capture", inst.SelfLink)
	}
	_, err := sim.CommitContainer(ctx, id, reference)
	return err
}

// gcpInstanceSerialPortOutput is the instance's serial console: everything
// its machine wrote since it was created, which a stop and start keep.
func gcpInstanceSerialPortOutput(ctx context.Context, link string) (string, error) {
	id, ok := gcpInstanceMachineID(link)
	if !ok {
		return "", nil
	}
	cli := sim.DockerClient()
	if cli == nil {
		return "", errors.New("no container engine")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reader, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", fmt.Errorf("read the console of %s: %w", link, err)
	}
	defer func() { _ = reader.Close() }()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, reader); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the console of %s: %w", link, err)
	}
	return out.String(), nil
}

// gcpAdoptComputeInstances takes over the machines an earlier simulator
// process left: a running one serves its instance again, a stopped one is
// kept for the next start, and an instance whose machine is gone reads
// TERMINATED, as Compute Engine reports an instance whose machine went away.
func gcpAdoptComputeInstances(ctx context.Context) {
	if gcpInstances == nil || !sim.HasPersistentWorkloadIdentity() || sim.RequireContainerRuntime("adopting Compute Engine instances") != nil {
		return
	}
	backend, err := gcpPlacement.For(placementCompute)
	if err != nil {
		log.Printf("compute: adopt instances: %v", err)
		return
	}
	for _, inst := range gcpInstances.List() {
		found, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-instance": inst.SelfLink})
		if err != nil {
			log.Printf("compute: find the machine of %s: %v", inst.SelfLink, err)
			continue
		}
		switch {
		case len(found) == 0:
			gcpInstances.Update(inst.SelfLink, func(in *ComputeInstance) {
				if in.Status == ComputeInstanceRunning || in.Status == ComputeInstanceSuspended {
					in.Status = ComputeInstanceTerminated
					in.StatusMessage = "Instance workload not found after control-plane restart"
				}
			})
		case !found[0].Running:
			gcpInstanceKeptMachines.Store(inst.SelfLink, found[0].ID)
			gcpInstances.Update(inst.SelfLink, func(in *ComputeInstance) {
				if in.Status == ComputeInstanceRunning {
					in.Status = ComputeInstanceTerminated
				}
			})
		default:
			if err := gcpAdoptInstanceMachine(ctx, backend, inst, found[0].ID); err != nil {
				log.Printf("compute: adopt the machine of %s: %v", inst.SelfLink, err)
			}
		}
	}
}

func gcpAdoptInstanceMachine(ctx context.Context, backend placement.Backend, inst ComputeInstance, id string) error {
	netw, err := gcpInstanceNetwork(ctx, &inst)
	if err != nil {
		return err
	}
	handle, err := backend.Adopt(ctx, id, placement.Workload{KeepOnExit: true, CancelGracePeriod: gcpInstanceStopGrace}, sim.NoopSink{})
	if err != nil {
		return err
	}
	machine := &gcpInstanceMachine{backend: backend, handle: handle, network: netw, address: netw.Address}
	gcpInstanceMachines.Store(inst.SelfLink, machine)
	gcpMetadataInstancesByIP.Store(netw.Address, inst)
	gcpInstances.Update(inst.SelfLink, func(in *ComputeInstance) {
		if in.Status != ComputeInstanceRunning {
			in.Status = ComputeInstanceRunning
			in.StatusMessage = ""
		}
	})
	go gcpWatchInstanceMachine(inst.SelfLink, machine)
	return nil
}
