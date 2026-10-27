package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/machine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

// gcpInstanceBooted is what an instance's machine booted with: the address
// and the record the metadata server answers it by.
type gcpInstanceBooted struct {
	address string
	dns     []string
	inst    ComputeInstance
}

var (
	gcpInstanceMachinesMu sync.Mutex
	gcpInstanceMachines   *machine.Manager
	// gcpInstancesBooted is the gcpInstanceBooted of each machine by the
	// instance's self link.
	gcpInstancesBooted sync.Map
)

// gcpInstanceManager is the manager of the instances' machines, on the
// backend the placement table gives Compute Engine; it is made at the first
// boot or adoption, once the backends are registered.
func gcpInstanceManager() (*machine.Manager, error) {
	gcpInstanceMachinesMu.Lock()
	defer gcpInstanceMachinesMu.Unlock()
	if gcpInstanceMachines != nil {
		return gcpInstanceMachines, nil
	}
	backend, err := gcpPlacement.For(placementCompute)
	if err != nil {
		return nil, err
	}
	gcpInstanceMachines = machine.New(backend, machine.Hooks{
		Booting: gcpInstanceBooting,
		// The instance carries its tags now, which the rules naming them as
		// a source expand over on its neighbours.
		Ready:         func(string) { gcpReapplyIngress() },
		Down:          gcpInstanceDown,
		GuestShutdown: gcpInstanceShutDown,
	})
	return gcpInstanceMachines, nil
}

// gcpInstanceManagerIfAny is the manager when a machine was ever booted or
// adopted.
func gcpInstanceManagerIfAny() *machine.Manager {
	gcpInstanceMachinesMu.Lock()
	defer gcpInstanceMachinesMu.Unlock()
	return gcpInstanceMachines
}

func gcpInstanceBootedAs(link string) (gcpInstanceBooted, bool) {
	v, ok := gcpInstancesBooted.Load(link)
	if !ok {
		return gcpInstanceBooted{}, false
	}
	booted, ok := v.(gcpInstanceBooted)
	return booted, ok
}

// gcpInstanceBooting lets the metadata server answer the guest from the first
// moment it boots.
func gcpInstanceBooting(link string) {
	if booted, ok := gcpInstanceBootedAs(link); ok {
		gcpMetadataInstancesByIP.Store(booted.address, booted.inst)
	}
}

func gcpInstanceDown(link string) {
	if booted, ok := gcpInstanceBootedAs(link); ok {
		gcpMetadataInstancesByIP.Delete(booted.address)
	}
}

// gcpInstanceShutDown moves an instance whose guest shut itself down to
// TERMINATED; its disk stays for the next start.
func gcpInstanceShutDown(link string) {
	if gcpInstances != nil {
		gcpInstances.Update(link, func(inst *ComputeInstance) {
			if inst.Status == ComputeInstanceRunning {
				inst.Status = ComputeInstanceTerminated
			}
		})
	}
}

// gcpRunningInstances lists the instances whose machines run.
func gcpRunningInstances() []machine.RunningMachine {
	machines := gcpInstanceManagerIfAny()
	if machines == nil {
		return nil
	}
	return machines.Running()
}

// gcpInstanceStopGrace is how long Compute Engine gives a guest to shut down
// on instances.stop before it powers the machine off.
const gcpInstanceStopGrace = 90 * time.Second

// gcpStartupScriptPath is where the script runner writes the startup-script
// it fetched from the metadata server before running it, as
// google_metadata_script_runner does. It sits with the runner under the guest
// agent's state directory, on the root disk.
const gcpStartupScriptPath = gcpGuestAgentDir + "/startup-script"

const gcpGuestAgentDir = "/var/lib/google-guest-agent"

// gcpStartupScriptRunner is what google_metadata_script_runner does at every
// boot: fetch the instance's startup-script from the metadata server as it
// is now, run it, and write its output under the startup-script prefix the
// guest's journal forwards to the serial console. The init carries the
// runner's output to the console.
const gcpStartupScriptRunner = `#!/bin/bash
script=` + gcpStartupScriptPath + `
status=/run/google-startup-script.status
: >"$script"
if exec 3<>/dev/tcp/metadata.google.internal/80; then
	printf 'GET /computeMetadata/v1/instance/attributes/startup-script HTTP/1.0\r\nHost: metadata.google.internal\r\nMetadata-Flavor: Google\r\n\r\n' >&3
	read -r _ code _ <&3
	while IFS= read -r header <&3 && [ "${header%$'\r'}" != "" ]; do :; done
	if [ "$code" = "200" ]; then
		cat <&3 >"$script"
	fi
	exec 3<&-
fi
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

// gcpCloudInitKeys are the ssh-keys entries cloud-init's GCE datasource
// gives the default user: those of the user "cloudinit" or "ubuntu", the
// default user of the Ubuntu images that carry cloud-init, from the
// instance's metadata and, unless the instance blocks them, the project's.
func gcpCloudInitKeys(inst ComputeInstance) []string {
	lines, _ := gcpInstanceMetadataValue(inst, "ssh-keys")
	if block, _ := gcpInstanceMetadataValue(inst, "block-project-ssh-keys"); !strings.EqualFold(block, "true") && gcpComputeProjects != nil {
		if held, ok := gcpComputeProjects.Get(gcpProjectFromSelfLink(inst.SelfLink)); ok {
			if metadata, ok := held["commonInstanceMetadata"].(map[string]any); ok {
				items, _ := metadata["items"].([]any)
				for _, item := range items {
					entry, _ := item.(map[string]any)
					if key, _ := entry["key"].(string); key == "ssh-keys" {
						value, _ := entry["value"].(string)
						lines += "\n" + value
					}
				}
			}
		}
	}
	var keys []string
	for _, line := range strings.Split(lines, "\n") {
		user, key, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && (user == "cloudinit" || user == "ubuntu") && strings.TrimSpace(key) != "" {
			keys = append(keys, strings.TrimSpace(key))
		}
	}
	return keys
}

// gcpInstanceWorkload is the workload an instance runs as on its network.
func gcpInstanceWorkload(inst ComputeInstance, netw gcpWorkloadNetwork, image gcpResolvedImage, shape gcpMachineType) (placement.Workload, error) {
	project := gcpProjectFromSelfLink(inst.SelfLink)
	zone := inst.Zone[strings.LastIndex(inst.Zone, "/")+1:]
	fqdn := fmt.Sprintf("%s.%s.c.%s.internal", inst.Name, zone, project)
	userData, _ := gcpInstanceMetadataValue(inst, "user-data")
	spec := vminstance.Spec{
		Name:     gcpInstanceContainerName(inst),
		ID:       inst.SelfLink,
		Hostname: inst.Name,
		Image:    image.Image,
		Network:  netw.Network,
		Address:  netw.Address,
		MAC:      gceInterfaceMAC(netw.Address),
		DNS:      netw.dns(),
		UserData: []byte(userData),
		CloudInit: &vminstance.CloudInit{
			Platform: "gce", Datasource: "DataSourceGCELocal", InstanceID: inst.Id,
			DefaultUserKeys: gcpCloudInitKeys(inst),
		},
		// The guest environment runs startup-script at every boot.
		Startup:    &vminstance.Startup{Path: gcpGuestAgentDir + "/startup-script-runner", Script: []byte(gcpStartupScriptRunner)},
		Env:        netw.env(),
		Gateway:    gcpInstanceGateway(inst),
		VCPUs:      shape.GuestCpus,
		MemoryMiB:  shape.MemoryMb,
		Labels:     map[string]string{"sockerless-gcp-instance-project": project},
		StopGrace:  gcpInstanceStopGrace,
		ExtraHosts: append(netw.extraHosts(), fqdn+":"+netw.Address),
	}
	ingress, err := gcpIngressStages(inst)
	if err != nil {
		return placement.Workload{}, err
	}
	spec.Ingress = ingress
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
// reports RUNNING. A machine that never comes up is stopped with its disk
// kept.
func gcpBootInstanceMachine(ctx context.Context, inst *ComputeInstance) error {
	machines, err := gcpInstanceManager()
	if err != nil {
		return err
	}
	if _, running := machines.Handle(inst.SelfLink); running {
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
	workload, err := gcpInstanceWorkload(*inst, netw, image, shape)
	if err != nil {
		return err
	}
	gcpInstancesBooted.Store(inst.SelfLink, gcpInstanceBooted{address: netw.Address, dns: netw.dns(), inst: *inst})
	return machines.Boot(ctx, inst.SelfLink, workload)
}

// gcpHaltInstanceMachine stops an instance's machine and keeps it, disk and
// all, for a later start or resume; a boot in flight ends rather than being
// waited out.
func gcpHaltInstanceMachine(ctx context.Context, inst *ComputeInstance) error {
	machines := gcpInstanceManagerIfAny()
	if machines == nil {
		return nil
	}
	return machines.Halt(ctx, inst.SelfLink)
}

// gcpDestroyInstanceMachine removes an instance's machine, running or kept.
func gcpDestroyInstanceMachine(ctx context.Context, inst *ComputeInstance) error {
	machines := gcpInstanceManagerIfAny()
	if machines == nil {
		return nil
	}
	if err := machines.Discard(ctx, inst.SelfLink); err != nil {
		return err
	}
	gcpInstancesBooted.Delete(inst.SelfLink)
	return nil
}

// gcpCaptureInstanceDisk stores the instance's machine as an engine image
// under reference: the image an instance created from the captured image
// boots from.
func gcpCaptureInstanceDisk(ctx context.Context, inst ComputeInstance, reference string) error {
	machines := gcpInstanceManagerIfAny()
	if machines == nil {
		return fmt.Errorf("instance %s has no machine to capture", inst.SelfLink)
	}
	if err := machines.Capture(ctx, inst.SelfLink, reference); err != nil {
		if errors.Is(err, machine.ErrNoMachine) {
			return fmt.Errorf("instance %s has no machine to capture", inst.SelfLink)
		}
		return err
	}
	return nil
}

// gcpInstanceSerialPortOutput is the instance's serial console: everything
// its machine wrote since it was created, which a stop and start keep.
func gcpInstanceSerialPortOutput(ctx context.Context, link string) (string, error) {
	machines := gcpInstanceManagerIfAny()
	if machines == nil {
		return "", nil
	}
	console, err := machines.Console(ctx, link)
	if errors.Is(err, machine.ErrNoMachine) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the console of %s: %w", link, err)
	}
	return console, nil
}

// gcpAdoptComputeInstances takes over the machines an earlier simulator
// process left: a running one serves its instance again, a stopped one is
// kept for the next start, and an instance whose machine is gone reads
// TERMINATED, as Compute Engine reports an instance whose machine went away.
func gcpAdoptComputeInstances(ctx context.Context) {
	if gcpInstances == nil || !sim.HasPersistentWorkloadIdentity() || sim.RequireContainerRuntime("adopting Compute Engine instances") != nil {
		return
	}
	machines, err := gcpInstanceManager()
	if err != nil {
		log.Printf("compute: adopt instances: %v", err)
		return
	}
	for _, inst := range gcpInstances.List() {
		found, err := machines.Find(ctx, map[string]string{"sockerless-sim-instance": inst.SelfLink})
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
			if _, _, err := machines.Adopt(ctx, inst.SelfLink, &found[0], placement.Workload{}); err != nil {
				log.Printf("compute: adopt the disk of %s: %v", inst.SelfLink, err)
				continue
			}
			gcpInstances.Update(inst.SelfLink, func(in *ComputeInstance) {
				if in.Status == ComputeInstanceRunning {
					in.Status = ComputeInstanceTerminated
				}
			})
		default:
			if err := gcpAdoptInstanceMachine(ctx, machines, inst, found[0]); err != nil {
				log.Printf("compute: adopt the machine of %s: %v", inst.SelfLink, err)
			}
		}
	}
}

func gcpAdoptInstanceMachine(ctx context.Context, machines *machine.Manager, inst ComputeInstance, existing placement.Existing) error {
	netw, err := gcpInstanceNetwork(ctx, &inst)
	if err != nil {
		return err
	}
	image, err := gcpInstanceBootImage(inst)
	if err != nil {
		return err
	}
	shape, ok := gcpLookupMachineType(inst.MachineType)
	if !ok {
		return fmt.Errorf("machine type %s does not exist", inst.MachineType)
	}
	workload, err := gcpInstanceWorkload(inst, netw, image, shape)
	if err != nil {
		return err
	}
	gcpInstancesBooted.Store(inst.SelfLink, gcpInstanceBooted{address: netw.Address, dns: netw.dns(), inst: inst})
	gcpInstanceBooting(inst.SelfLink)
	if _, _, err := machines.Adopt(ctx, inst.SelfLink, &existing, workload); err != nil {
		gcpInstanceDown(inst.SelfLink)
		return err
	}
	gcpInstances.Update(inst.SelfLink, func(in *ComputeInstance) {
		if in.Status != ComputeInstanceRunning {
			in.Status = ComputeInstanceRunning
			in.StatusMessage = ""
		}
	})
	return nil
}

// gceInterfaceMAC is the MAC address Compute Engine gives an interface:
// 42:01 and the four octets of its IPv4 address.
func gceInterfaceMAC(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return ""
	}
	o := addr.As4()
	return fmt.Sprintf("42:01:%02x:%02x:%02x:%02x", o[0], o[1], o[2], o[3])
}
