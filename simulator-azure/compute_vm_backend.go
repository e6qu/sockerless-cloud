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
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
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

type azureVMWorkload struct {
	handle   placement.Handle
	address  string
	spec     vminstance.Spec
	metadata azureMetadataVM
}

var azureVMWorkloads = struct {
	sync.Mutex
	byID map[string]*azureVMWorkload
}{byID: map[string]*azureVMWorkload{}}

func azureVMWorkloadOf(vmID string) (*azureVMWorkload, bool) {
	azureVMWorkloads.Lock()
	defer azureVMWorkloads.Unlock()
	w, ok := azureVMWorkloads.byID[strings.ToLower(vmID)]
	return w, ok
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
	vnetID := azureVNetIDOfSubnet(subnetID)
	network, err := azureVNetNetwork(ctx, vnetID)
	if err != nil {
		return vminstance.Spec{}, azureMetadataVM{}, err
	}
	if vnet, ok := azureVnets.Get(vnetID); ok {
		prefix, err := azureVNetIPv4Prefix(vnet)
		if err != nil {
			return vminstance.Spec{}, azureMetadataVM{}, err
		}
		if err := azureJoinMicroVMNetwork(ctx, placementVirtualMachines, vnet.ID, prefix, network.Network); err != nil {
			return vminstance.Spec{}, azureMetadataVM{}, err
		}
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
	spec := vminstance.Spec{
		Name:      "sockerless-sim-azure-vm-" + sanitizeContainerName(vm.Name) + "-" + sim.RandomHex(4),
		ID:        strings.ToLower(vm.ID),
		Hostname:  azureVMComputerName(vm),
		Image:     image,
		Network:   network.Network,
		Address:   ipcfg.Properties.PrivateIPAddress,
		DNS:       network.resolvers(),
		UserData:  customData,
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

// azureBootVMWorkload places the machine, or starts its kept instance again,
// and reports it running once the instance init is ready. A machine already
// running stays as it is.
func azureBootVMWorkload(ctx context.Context, vm VirtualMachine) error {
	if _, running := azureVMWorkloadOf(vm.ID); running {
		return nil
	}
	spec, metadata, err := azureVMInstanceSpec(ctx, vm)
	if err != nil {
		return err
	}
	backend, err := azurePlacementBackend(placementVirtualMachines)
	if err != nil {
		return err
	}
	var handle placement.Handle
	if kept, ok := azureVMKeptContainer(vm.ID); ok {
		workload, err := vminstance.Workload(spec)
		if err != nil {
			return err
		}
		if handle, err = backend.Restart(ctx, kept, workload, sim.NoopSink{}); err != nil {
			return fmt.Errorf("start the machine's OS disk %s: %w", kept, err)
		}
	} else if handle, err = vminstance.Launch(ctx, backend, spec, sim.NoopSink{}); err != nil {
		return err
	}
	azureRecordVMDisk(vm.ID, handle.ID())
	w := &azureVMWorkload{handle: handle, address: spec.Address, spec: spec, metadata: metadata}
	azureVMWorkloads.Lock()
	azureVMWorkloads.byID[strings.ToLower(vm.ID)] = w
	azureVMWorkloads.Unlock()
	if err := azureAwaitInstanceReady(ctx, handle); err != nil {
		azureVMWorkloads.Lock()
		delete(azureVMWorkloads.byID, strings.ToLower(vm.ID))
		azureVMWorkloads.Unlock()
		return err
	}
	azureVMWorkloadUp(vm.ID, w)
	return nil
}

// azureVMWorkloadUp registers a running instance with the metadata service and
// moves the machine to stopped if its guest ends on its own.
func azureVMWorkloadUp(vmID string, w *azureVMWorkload) {
	azureMetadataVMsByIP.Store(w.address, w.metadata)
	go func() {
		<-w.handle.Exited()
		azureVMWorkloads.Lock()
		current := azureVMWorkloads.byID[strings.ToLower(vmID)] == w
		if current {
			delete(azureVMWorkloads.byID, strings.ToLower(vmID))
		}
		azureVMWorkloads.Unlock()
		if !current {
			return
		}
		azureMetadataVMsByIP.Delete(w.address)
		azureVMStates.Update(vmID, func(state *string) { *state = "PowerState/stopped" })
	}()
}

// azureAwaitInstanceReady returns once the instance init has touched its
// ready marker, or reports how the instance ended instead.
func azureAwaitInstanceReady(ctx context.Context, handle placement.Handle) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		select {
		case <-handle.Exited():
			result := handle.Wait()
			console, _ := azureGuestConsoleOfMachine(context.Background(), handle.ID())
			return fmt.Errorf("the instance exited with code %d before it was ready: %s", result.ExitCode, strings.TrimSpace(string(console)))
		case <-ctx.Done():
			return fmt.Errorf("the instance did not become ready: %w", ctx.Err())
		default:
		}
		probe, err := handle.Exec(ctx, []string{"test", "-e", "/run/sockerless-instance-ready"})
		if err == nil && probe.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-handle.Exited():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// azureHaltVMWorkload stops the machine's instance and keeps its OS disk. A
// machine with no running instance is already stopped.
func azureHaltVMWorkload(ctx context.Context, vmID string) error {
	azureVMWorkloads.Lock()
	w := azureVMWorkloads.byID[strings.ToLower(vmID)]
	delete(azureVMWorkloads.byID, strings.ToLower(vmID))
	azureVMWorkloads.Unlock()
	if w == nil {
		return nil
	}
	azureMetadataVMsByIP.Delete(w.address)
	w.handle.Cancel()
	select {
	case <-w.handle.Exited():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// azureDestroyVMWorkload stops the machine and discards its OS disk, unless
// the disk's deleteOption keeps it.
func azureDestroyVMWorkload(ctx context.Context, vm VirtualMachine) error {
	if err := azureHaltVMWorkload(ctx, vm.ID); err != nil {
		return err
	}
	osDisk, _ := vm.Properties.StorageProfile["osDisk"].(map[string]any)
	if option, _ := osDisk["deleteOption"].(string); strings.EqualFold(option, "Detach") {
		return nil
	}
	return azureDiscardVMDisk(vm.ID)
}

// azureGuestExecInWorkload runs a shell command in the running instance.
func azureGuestExecInWorkload(ctx context.Context, vmID, command string) (stdout, stderr []byte, exitCode int, err error) {
	w, ok := azureVMWorkloadOf(vmID)
	if !ok {
		return nil, nil, 0, fmt.Errorf("virtual machine %q is not running, so nothing can be executed inside it", vmID)
	}
	result, err := w.handle.Exec(ctx, []string{"/bin/sh", "-c", command})
	if err != nil {
		return nil, nil, 0, err
	}
	return []byte(result.Stdout), []byte(result.Stderr), result.ExitCode, nil
}

// azureVMContainer is the container holding the machine's OS disk: its
// running instance's, or the one kept from its last stop.
func azureVMContainer(vmID string) (string, bool) {
	if w, ok := azureVMWorkloadOf(vmID); ok {
		return w.handle.ID(), true
	}
	return azureVMKeptContainer(vmID)
}

// azureGuestConsoleOfWorkload reads what the instance wrote to its console.
func azureGuestConsoleOfWorkload(ctx context.Context, vmID string) ([]byte, error) {
	machineID, ok := azureVMContainer(vmID)
	if !ok {
		return nil, fmt.Errorf("virtual machine %q has never booted, so it has produced no console output", vmID)
	}
	return azureGuestConsoleOfMachine(ctx, machineID)
}

// azureGuestConsoleOfMachine reads a machine's console from its backend,
// which keeps it across the machine's stops.
func azureGuestConsoleOfMachine(ctx context.Context, machineID string) ([]byte, error) {
	backend, err := azurePlacementBackend(placementVirtualMachines)
	if err != nil {
		return nil, err
	}
	console, err := backend.Console(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return []byte(console), nil
}

// azureCommitVMImage commits the machine's OS disk as an OCI image under a
// reference of the simulator's own, quiescing a running guest first.
func azureCommitVMImage(ctx context.Context, vm VirtualMachine, purpose string) (vminstance.Image, error) {
	if w, ok := azureVMWorkloadOf(vm.ID); ok {
		if _, err := w.handle.Exec(ctx, []string{"sync"}); err != nil {
			return vminstance.Image{}, fmt.Errorf("quiesce %q before capturing it: %w", vm.ID, err)
		}
	}
	containerID, ok := azureVMContainer(vm.ID)
	if !ok {
		return vminstance.Image{}, fmt.Errorf("virtual machine %q has no disk to capture: it has never booted", vm.ID)
	}
	source, err := azureResolveVMImage(ctx, vm)
	if err != nil {
		return vminstance.Image{}, err
	}
	backend, err := azurePlacementBackend(placementVirtualMachines)
	if err != nil {
		return vminstance.Image{}, err
	}
	reference := "sockerless-sim-azure-" + purpose + ":" + sim.RandomHex(8)
	if err := backend.Capture(ctx, containerID, reference); err != nil {
		return vminstance.Image{}, err
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
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
// machine's kept OS disk.
func azureAdoptVMWorkloads(ctx context.Context) {
	if !sim.HasPersistentWorkloadIdentity() {
		return
	}
	existing, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-type": azureVMLabelType})
	if err != nil {
		log.Printf("adopt virtual machines: %v", err)
		return
	}
	backend, err := azurePlacementBackend(placementVirtualMachines)
	if err != nil {
		log.Printf("adopt virtual machines: %v", err)
		return
	}
	for _, c := range existing {
		vmID := c.Labels[azureVMLabelID]
		vm, ok := azureVMs.Get(vmID)
		if !ok {
			_ = sim.RemoveExistingContainer(c.ID)
			continue
		}
		azureRecordVMDisk(vm.ID, c.ID)
		if !c.Running {
			azureVMStates.Update(vm.ID, func(state *string) {
				if *state == "PowerState/running" {
					*state = "PowerState/stopped"
				}
			})
			continue
		}
		spec, metadata, err := azureVMInstanceSpec(ctx, vm)
		if err != nil {
			log.Printf("adopt virtual machine %s: %v", vm.ID, err)
			continue
		}
		workload, err := vminstance.Workload(spec)
		if err != nil {
			log.Printf("adopt virtual machine %s: %v", vm.ID, err)
			continue
		}
		handle, err := backend.Adopt(ctx, c.ID, workload, sim.NoopSink{})
		if err != nil {
			log.Printf("adopt virtual machine %s: %v", vm.ID, err)
			continue
		}
		w := &azureVMWorkload{handle: handle, address: spec.Address, spec: spec, metadata: metadata}
		azureVMWorkloads.Lock()
		azureVMWorkloads.byID[strings.ToLower(vm.ID)] = w
		azureVMWorkloads.Unlock()
		azureVMWorkloadUp(vm.ID, w)
		azureVMStates.Put(vm.ID, "PowerState/running")
	}
}

// azureReleaseVMWorkloads discards what a simulator with no persistent state
// would otherwise leave on the engine: every kept OS disk and every image a
// capture committed. A persistent simulator keeps both for its next run.
func azureReleaseVMWorkloads(ctx context.Context) {
	if sim.HasPersistentWorkloadIdentity() {
		return
	}
	for _, vm := range azureVMs.List() {
		_ = azureHaltVMWorkload(ctx, vm.ID)
		_ = azureDiscardVMDisk(vm.ID)
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
