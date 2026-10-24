package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/machine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

// A virtual machine runs as an instance on the backend the placement table
// names for virtualmachines: the OCI image its imageReference resolves to,
// its primary interface's leased address on its virtual network's engine
// network, that network's sidecar as resolver and metadata endpoint, and the
// instance init that runs its customData once and keeps the machine up. The
// instance's filesystem is the machine's OS disk: a stop or deallocate keeps
// it for the next start, a capture commits it, a delete discards it unless
// the disk's deleteOption says Detach.

const (
	azureVMLabelType = "azure-vm"
	azureVMLabelID   = "sockerless-azure-vm"
)

// azureVMMachine is what a machine booted with: the address and the record
// the instance metadata service answers it by.
type azureVMMachine struct {
	vmID     string
	address  string
	metadata azureMetadataVM
}

var (
	azureVMMachinesMu sync.Mutex
	azureVMMachines   *machine.Manager
	// azureVMBooted is the azureVMMachine of each machine by its key.
	azureVMBooted sync.Map
)

func azureVMKey(vmID string) string { return strings.ToLower(vmID) }

// azureVMManager is the manager of the machines, on the backend the placement
// table gives virtual machines; it is made at the first boot or adoption,
// once the backends are registered.
func azureVMManager() (*machine.Manager, error) {
	azureVMMachinesMu.Lock()
	defer azureVMMachinesMu.Unlock()
	if azureVMMachines != nil {
		return azureVMMachines, nil
	}
	backend, err := azurePlacementBackend(placementVirtualMachines)
	if err != nil {
		return nil, err
	}
	azureVMMachines = machine.New(backend, machine.Hooks{
		Booting:       azureVMBooting,
		Down:          azureVMDown,
		GuestShutdown: azureVMShutDown,
	})
	return azureVMMachines, nil
}

// azureVMManagerIfAny is the manager when a machine was ever booted or adopted.
func azureVMManagerIfAny() *machine.Manager {
	azureVMMachinesMu.Lock()
	defer azureVMMachinesMu.Unlock()
	return azureVMMachines
}

func azureVMBootedAs(key string) (*azureVMMachine, bool) {
	v, ok := azureVMBooted.Load(key)
	if !ok {
		return nil, false
	}
	booted, ok := v.(*azureVMMachine)
	return booted, ok
}

// azureVMBooting lets the instance metadata service answer the machine from
// its first instruction, as cloud-init reads it before the guest is ready.
func azureVMBooting(key string) {
	if booted, ok := azureVMBootedAs(key); ok {
		azureMetadataVMsByIP.Store(booted.address, booted.metadata)
	}
}

func azureVMDown(key string) {
	if booted, ok := azureVMBootedAs(key); ok {
		azureMetadataVMsByIP.Delete(booted.address)
	}
}

// azureVMShutDown moves a machine whose guest ended on its own to stopped.
func azureVMShutDown(key string) {
	if booted, ok := azureVMBootedAs(key); ok {
		azureVMStates.Update(booted.vmID, func(state *string) { *state = "PowerState/stopped" })
	}
}

// azureRunningVM is a machine that runs, with what it booted with.
type azureRunningVM struct {
	booted *azureVMMachine
	handle placement.Handle
}

func azureRunningVMs() []azureRunningVM {
	machines := azureVMManagerIfAny()
	if machines == nil {
		return nil
	}
	var out []azureRunningVM
	for _, running := range machines.Running() {
		if booted, ok := azureVMBootedAs(running.Key); ok {
			out = append(out, azureRunningVM{booted: booted, handle: running.Handle})
		}
	}
	return out
}

// azureVMHandle is the handle of a machine's running instance.
func azureVMHandle(vmID string) (placement.Handle, bool) {
	machines := azureVMManagerIfAny()
	if machines == nil {
		return nil, false
	}
	return machines.Handle(azureVMKey(vmID))
}

// azureVMSizeSpec is the cores and memory of a size the catalogue offers.
func azureVMSizeSpec(name string) (cores, memoryMB int, ok bool) {
	for _, size := range azureVMSizeCatalogue() {
		if sizeName, _ := size["name"].(string); strings.EqualFold(sizeName, name) {
			cores, _ = size["numberOfCores"].(int)
			memoryMB, _ = size["memoryInMB"].(int)
			return cores, memoryMB, true
		}
	}
	return 0, 0, false
}

// azureValidateVMRequest applies the request-validation rules a machine write
// meets before the Compute resource provider provisions it: its network
// profile, its image and its size.
func azureValidateVMRequest(ctx context.Context, vm VirtualMachine) *azureVMRequestFault {
	if fault := azureValidateVMNetworkProfile(vm); fault != nil {
		return fault
	}
	if _, err := azureResolveVMImage(ctx, vm); err != nil {
		var fault *azureVMRequestFault
		if errors.As(err, &fault) {
			return fault
		}
		return &azureVMRequestFault{code: "InvalidParameter", status: http.StatusBadRequest,
			message: "The value of parameter properties.storageProfile is invalid: " + err.Error()}
	}
	size, _ := vm.Properties.HardwareProfile["vmSize"].(string)
	if _, _, ok := azureVMSizeSpec(size); !ok {
		return &azureVMRequestFault{code: "InvalidParameter", status: http.StatusBadRequest,
			message: fmt.Sprintf("The value of parameter vmSize is invalid: %q is not a size this location offers.", size)}
	}
	return nil
}

// azureVMCustomData is the script osProfile.customData carries, which
// cloud-init runs once at first boot.
func azureVMCustomData(vm VirtualMachine) ([]byte, error) {
	encoded, _ := vm.Properties.OSProfile["customData"].(string)
	if encoded == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("osProfile.customData is not base64: %w", err)
	}
	return data, nil
}

// azureVMCloudInit is the image's cloud-init as Azure's datasource feeds it:
// the machine's vmId, and its osProfile's adminUsername as the default user
// with the linuxConfiguration's public keys.
func azureVMCloudInit(vm VirtualMachine) *vminstance.CloudInit {
	ci := &vminstance.CloudInit{Platform: "azure", Datasource: "DataSourceAzure [seed=/dev/sr0]", InstanceID: vm.Properties.VMID}
	ci.DefaultUser, _ = vm.Properties.OSProfile["adminUsername"].(string)
	linux, _ := vm.Properties.OSProfile["linuxConfiguration"].(map[string]any)
	ssh, _ := linux["ssh"].(map[string]any)
	keys, _ := ssh["publicKeys"].([]any)
	for _, k := range keys {
		entry, _ := k.(map[string]any)
		if data, _ := entry["keyData"].(string); strings.TrimSpace(data) != "" {
			ci.DefaultUserKeys = append(ci.DefaultUserKeys, strings.TrimSpace(data))
		}
	}
	return ci
}

// azureVMInstanceSpec builds the instance the machine runs as, realizing its
// virtual network's engine network and sidecar on the way.
func azureVMInstanceSpec(ctx context.Context, vm VirtualMachine) (vminstance.Spec, azureMetadataVM, error) {
	if fault := azureValidateVMNetworkProfile(vm); fault != nil {
		return vminstance.Spec{}, azureMetadataVM{}, fault
	}
	image, err := azureResolveVMImage(ctx, vm)
	if err != nil {
		return vminstance.Spec{}, azureMetadataVM{}, err
	}
	nic, _ := azureNICs.Get(vm.Properties.NetworkProfile.NetworkInterfaces[0].ID)
	ipcfg := azurePrimaryIPConfig(nic)
	subnetID := ipcfg.Properties.Subnet.ID
	network, err := azureSubnetNetwork(ctx, placementVirtualMachines, subnetID)
	if err != nil {
		return vminstance.Spec{}, azureMetadataVM{}, err
	}
	size, _ := vm.Properties.HardwareProfile["vmSize"].(string)
	cores, memoryMB, ok := azureVMSizeSpec(size)
	if !ok {
		return vminstance.Spec{}, azureMetadataVM{}, fmt.Errorf("vmSize %q is not a size this location offers", size)
	}
	customData, err := azureVMCustomData(vm)
	if err != nil {
		return vminstance.Spec{}, azureMetadataVM{}, err
	}
	ingress, err := azureIngressPacketStages(nic)
	if err != nil {
		return vminstance.Spec{}, azureMetadataVM{}, err
	}
	spec := vminstance.Spec{
		Ingress:   ingress,
		Gateway:   azureVMGateway(nic),
		Name:      "sockerless-sim-azure-vm-" + sanitizeContainerName(vm.Name) + "-" + sim.RandomHex(4),
		ID:        strings.ToLower(vm.ID),
		Hostname:  azureVMComputerName(vm),
		Image:     image,
		Network:   network.Network,
		Address:   ipcfg.Properties.PrivateIPAddress,
		MAC:       azureNICMAC(nic.ID),
		DNS:       network.resolvers(),
		UserData:  customData,
		CloudInit: azureVMCloudInit(vm),
		Env:       map[string]string{"AZURE_INSTANCE_METADATA_ENDPOINT": "http://" + network.DNS + "/metadata/instance"},
		VCPUs:     cores,
		MemoryMiB: memoryMB,
		Labels: map[string]string{
			"sockerless-sim-type": azureVMLabelType,
			azureVMLabelID:        vm.ID,
		},
	}
	return spec, azureMetadataVM{VM: vm, NIC: nic, SubnetID: subnetID}, nil
}

// azureBootVMWorkload places the machine, or starts its kept OS disk again,
// and reports it running once the instance init is ready. A machine already
// running stays as it is. A guest that never becomes ready is stopped with
// its OS disk kept.
func azureBootVMWorkload(ctx context.Context, vm VirtualMachine) error {
	if _, running := azureVMHandle(vm.ID); running {
		return nil
	}
	spec, metadata, err := azureVMInstanceSpec(ctx, vm)
	if err != nil {
		return err
	}
	workload, err := vminstance.Workload(spec)
	if err != nil {
		return err
	}
	machines, err := azureVMManager()
	if err != nil {
		return err
	}
	key := azureVMKey(vm.ID)
	azureVMBooted.Store(key, &azureVMMachine{vmID: vm.ID, address: spec.Address, metadata: metadata})
	err = machines.Boot(ctx, key, workload)
	var notReady *machine.NotReadyError
	if errors.As(err, &notReady) && notReady.ExitCode >= 0 {
		console, _ := machines.Console(context.WithoutCancel(ctx), key)
		return fmt.Errorf("the instance exited with code %d before it was ready: %s", notReady.ExitCode, strings.TrimSpace(console))
	}
	return err
}

// azureHaltVMWorkload stops the machine's instance and keeps its OS disk. A
// machine with no running instance is already stopped.
func azureHaltVMWorkload(ctx context.Context, vmID string) error {
	machines := azureVMManagerIfAny()
	if machines == nil {
		return nil
	}
	return machines.Halt(ctx, azureVMKey(vmID))
}

// azureDiscardVMDisk stops the machine and removes its OS disk, for a machine
// that is deleted or reimaged rather than stopped.
func azureDiscardVMDisk(ctx context.Context, vmID string) error {
	machines := azureVMManagerIfAny()
	if machines == nil {
		return nil
	}
	key := azureVMKey(vmID)
	if err := machines.Discard(ctx, key); err != nil {
		return err
	}
	azureVMBooted.Delete(key)
	return nil
}

// azureDestroyVMWorkload stops the machine and discards its OS disk, unless
// the disk's deleteOption detaches it to outlive the machine.
func azureDestroyVMWorkload(ctx context.Context, vm VirtualMachine) error {
	osDisk, _ := vm.Properties.StorageProfile["osDisk"].(map[string]any)
	if option, _ := osDisk["deleteOption"].(string); !strings.EqualFold(option, "Detach") {
		return azureDiscardVMDisk(ctx, vm.ID)
	}
	if err := azureHaltVMWorkload(ctx, vm.ID); err != nil {
		return err
	}
	if machines := azureVMManagerIfAny(); machines != nil {
		machines.Release(azureVMKey(vm.ID))
	}
	azureVMBooted.Delete(azureVMKey(vm.ID))
	return nil
}

// azureGuestExecInWorkload runs a shell command in the running instance.
func azureGuestExecInWorkload(ctx context.Context, vmID, command string) (stdout, stderr []byte, exitCode int, err error) {
	handle, ok := azureVMHandle(vmID)
	if !ok {
		return nil, nil, 0, fmt.Errorf("virtual machine %q is not running, so nothing can be executed inside it", vmID)
	}
	result, err := handle.Exec(ctx, []string{"/bin/sh", "-c", command})
	if err != nil {
		return nil, nil, 0, err
	}
	return []byte(result.Stdout), []byte(result.Stderr), result.ExitCode, nil
}

// azureGuestConsoleOfWorkload reads what the instance wrote to its console,
// across the stops of its OS disk.
func azureGuestConsoleOfWorkload(ctx context.Context, vmID string) ([]byte, error) {
	machines := azureVMManagerIfAny()
	if machines == nil {
		return nil, fmt.Errorf("virtual machine %q has never booted, so it has produced no console output", vmID)
	}
	console, err := machines.Console(ctx, azureVMKey(vmID))
	if errors.Is(err, machine.ErrNoMachine) {
		return nil, fmt.Errorf("virtual machine %q has never booted, so it has produced no console output", vmID)
	}
	if err != nil {
		return nil, err
	}
	return []byte(console), nil
}

// azureCommitVMImage commits the machine's OS disk as an OCI image under a
// reference of the simulator's own, quiescing a running guest first.
func azureCommitVMImage(ctx context.Context, vm VirtualMachine, purpose string) (vminstance.Image, error) {
	source, err := azureResolveVMImage(ctx, vm)
	if err != nil {
		return vminstance.Image{}, err
	}
	machines := azureVMManagerIfAny()
	if machines == nil {
		return vminstance.Image{}, fmt.Errorf("virtual machine %q has no disk to capture: it has never booted", vm.ID)
	}
	reference := "sockerless-sim-azure-" + purpose + ":" + sim.RandomHex(8)
	if err := machines.Capture(ctx, azureVMKey(vm.ID), reference); err != nil {
		if errors.Is(err, machine.ErrNoMachine) {
			return vminstance.Image{}, fmt.Errorf("virtual machine %q has no disk to capture: it has never booted", vm.ID)
		}
		return vminstance.Image{}, fmt.Errorf("capture %q: %w", vm.ID, err)
	}
	return vminstance.Image{OCI: reference, Architecture: source.Architecture}, nil
}

// azureExportImage reads an OCI image's bytes off the engine, which is what a
// capture writes into the storage account as the disk.
func azureExportImage(ctx context.Context, reference string) ([]byte, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("exporting an image"); err != nil {
		return nil, err
	}
	saved, err := cli.ImageSave(ctx, []string{reference})
	if err != nil {
		return nil, fmt.Errorf("export image %s: %w", reference, err)
	}
	defer saved.Close()
	data, err := io.ReadAll(saved)
	if err != nil {
		return nil, fmt.Errorf("export image %s: %w", reference, err)
	}
	return data, nil
}

// azureAdoptVMWorkloads takes over the instances a previous process of this
// persistent simulator placed: a running one is adopted, a stopped one is the
// machine's kept OS disk, and one whose machine is gone is removed.
func azureAdoptVMWorkloads(ctx context.Context) {
	if !sim.HasPersistentWorkloadIdentity() {
		return
	}
	machines, err := azureVMManager()
	if err != nil {
		log.Printf("adopt virtual machines: %v", err)
		return
	}
	existing, err := machines.Find(ctx, map[string]string{"sockerless-sim-type": azureVMLabelType})
	if err != nil {
		log.Printf("adopt virtual machines: %v", err)
		return
	}
	for _, c := range existing {
		vm, ok := azureVMs.Get(c.Labels[azureVMLabelID])
		if !ok {
			if err := machines.Remove(ctx, c.ID); err != nil {
				log.Printf("remove the OS disk %s of a deleted virtual machine: %v", c.ID, err)
			}
			continue
		}
		if err := azureAdoptVM(ctx, machines, vm, c); err != nil {
			log.Printf("adopt virtual machine %s: %v", vm.ID, err)
		}
	}
}

func azureAdoptVM(ctx context.Context, machines *machine.Manager, vm VirtualMachine, existing placement.Existing) error {
	key := azureVMKey(vm.ID)
	if !existing.Running {
		if _, _, err := machines.Adopt(ctx, key, &existing, placement.Workload{}); err != nil {
			return err
		}
		azureVMStates.Update(vm.ID, func(state *string) {
			if *state == "PowerState/running" {
				*state = "PowerState/stopped"
			}
		})
		return nil
	}
	spec, metadata, err := azureVMInstanceSpec(ctx, vm)
	if err != nil {
		return err
	}
	workload, err := vminstance.Workload(spec)
	if err != nil {
		return err
	}
	azureVMBooted.Store(key, &azureVMMachine{vmID: vm.ID, address: spec.Address, metadata: metadata})
	azureVMBooting(key)
	if _, _, err := machines.Adopt(ctx, key, &existing, workload); err != nil {
		azureVMDown(key)
		return err
	}
	azureVMStates.Put(vm.ID, "PowerState/running")
	return nil
}

// azureReleaseVMWorkloads discards what a simulator with no persistent state
// would otherwise leave on the engine: every machine and kept OS disk,
// detached ones included, and every image a capture committed. A persistent
// simulator keeps both for its next run.
func azureReleaseVMWorkloads(ctx context.Context) {
	if sim.HasPersistentWorkloadIdentity() {
		return
	}
	if machines := azureVMManagerIfAny(); machines != nil {
		for _, vm := range azureVMs.List() {
			_ = azureDiscardVMDisk(ctx, vm.ID)
		}
		if left, err := machines.Find(ctx, map[string]string{"sockerless-sim-type": azureVMLabelType}); err == nil {
			for _, c := range left {
				_ = machines.Remove(ctx, c.ID)
			}
		}
	}
	cli := sim.DockerClient()
	if cli == nil || azureImageBackings == nil {
		return
	}
	for _, backing := range azureImageBackings.List() {
		if strings.HasPrefix(backing.OCI, "sockerless-sim-azure-") {
			_, _ = cli.ImageRemove(ctx, backing.OCI, client.ImageRemoveOptions{Force: true})
		}
	}
}
