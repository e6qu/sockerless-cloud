package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// A machine's data disks. The machine's workload mounts its disk bay, an
// engine volume of its own, at the path a Linux guest finds Azure's data disks
// by LUN; an attached disk is the bay's lun<N> directory. Attaching copies the
// disk's volume into it and detaching copies it back out and removes it, so a
// running guest sees a disk come and go without a reboot and the disk carries
// what the guest wrote to the next machine.

const azureDataDiskMountPath = "/dev/disk/azure/scsi1"

// azureDataDiskMaxLUN is the highest logical unit number a data disk takes.
const azureDataDiskMaxLUN = 63

// azureDiskCopyImage runs the copy helper; it is the image the simulator's
// own volume snapshots use.
const azureDiskCopyImage = "mirror.gcr.io/library/alpine:3.22"

var azureDiskCopySandbox = sim.SandboxProfile{
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

func azureDiskVolume(disk ManagedDisk) string {
	return "sockerless-azure-disk-" + strings.ToLower(disk.Properties.UniqueID)
}

func azureVMDiskBay(vm VirtualMachine) string {
	return "sockerless-azure-vm-disks-" + strings.ToLower(vm.Properties.VMID)
}

func azureLUNDir(lun int) string { return fmt.Sprintf("lun%d", lun) }

// azureDiskLocation is where a disk's contents are: a volume and a directory
// within it.
type azureDiskLocation struct {
	volume string
	dir    string
}

// azureDiskLocationOf finds a held disk in its machine's bay.
func azureDiskLocationOf(disk ManagedDisk) azureDiskLocation {
	if disk.ManagedBy != "" {
		if vm, ok := azureVMs.Get(disk.ManagedBy); ok {
			for _, entry := range azureVMDataDiskEntries(vm) {
				if strings.EqualFold(azureDataDiskID(entry), disk.ID) {
					if lun, ok := azureDataDiskLUN(entry); ok {
						return azureDiskLocation{volume: azureVMDiskBay(vm), dir: azureLUNDir(lun)}
					}
				}
			}
		}
	}
	return azureDiskLocation{volume: azureDiskVolume(disk)}
}

type azureLineSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *azureLineSink) WriteLog(line sim.LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line.Text)
}

func (s *azureLineSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

// azureCopyDiskData replaces dst's contents with src's on the engine, where
// the volumes live. move removes src's directory afterwards, which must then
// name one.
func azureCopyDiskData(ctx context.Context, src, dst azureDiskLocation, move bool) error {
	if move && src.dir == "" {
		return fmt.Errorf("moving disk data out of %s names no directory", src.volume)
	}
	if err := sim.RequireContainerRuntime("copying managed disk data"); err != nil {
		return err
	}
	from, to := "/src/"+src.dir, "/dst/"+dst.dir
	script := fmt.Sprintf(`set -e
mkdir -p %[2]s
find %[2]s -mindepth 1 -delete
if [ -d %[1]s ]; then cp -a %[1]s/. %[2]s/; fi`, from, to)
	srcMode := ":ro"
	if move {
		script += "\nrm -rf " + from
		srcMode = ""
	}
	platform, err := workload.LocalImagePlatform(ctx, azureDiskCopyImage, "")
	if err != nil {
		return err
	}
	var sink azureLineSink
	handle, err := sim.StartContainerSyncContext(ctx, sim.ContainerConfig{
		Image:        azureDiskCopyImage,
		Architecture: platform,
		Command:      []string{"/bin/sh"},
		Args:         []string{"-c", script},
		Binds:        []string{src.volume + ":/src" + srcMode, dst.volume + ":/dst"},
		Labels:       map[string]string{"sockerless-azure-disk-copy": dst.volume},
		Sandbox:      azureDiskCopySandbox,
	}, &sink)
	if err != nil {
		return fmt.Errorf("start the disk copy from %s to %s: %w", src.volume, dst.volume, err)
	}
	result := handle.Wait()
	if result.ExitCode != 0 || result.Error != nil {
		return fmt.Errorf("copy disk data from %s/%s to %s/%s (exit %d, err %v): %s",
			src.volume, src.dir, dst.volume, dst.dir, result.ExitCode, result.Error, strings.TrimSpace(sink.String()))
	}
	return nil
}

func azureVMDataDiskEntries(vm VirtualMachine) []map[string]any {
	var out []map[string]any
	for _, entry := range anySlice(vm.Properties.StorageProfile["dataDisks"]) {
		if disk, ok := entry.(map[string]any); ok {
			out = append(out, disk)
		}
	}
	return out
}

func azureDataDiskID(entry map[string]any) string {
	managed, _ := entry["managedDisk"].(map[string]any)
	id, _ := managed["id"].(string)
	return id
}

func azureDataDiskLUN(entry map[string]any) (int, bool) {
	switch lun := entry["lun"].(type) {
	case float64:
		return int(lun), lun == float64(int(lun))
	case int:
		return lun, true
	case int32:
		return int(lun), true
	case int64:
		return int(lun), true
	}
	return 0, false
}

func azureVMMaxDataDisks(vm VirtualMachine) int {
	size, _ := vm.Properties.HardwareProfile["vmSize"].(string)
	for _, known := range azureVMSizeCatalogue() {
		if name, _ := known["name"].(string); strings.EqualFold(name, size) {
			count, _ := known["maxDataDiskCount"].(int)
			return count
		}
	}
	return 0
}

// azureDataDiskMove is one disk entering or leaving a machine's bay.
type azureDataDiskMove struct {
	diskID string
	lun    int
	// copyFrom is a Copy's source, read instead of the disk's own volume.
	copyFrom *azureDiskLocation
	// release ends the machine's hold on a detached disk; a disk that only moves
	// LUN stays held.
	release bool
}

type azureDataDiskPlan struct {
	entries []any
	created []ManagedDisk
	attach  []azureDataDiskMove
	detach  []azureDataDiskMove
}

func (p azureDataDiskPlan) moves() bool { return len(p.attach) > 0 || len(p.detach) > 0 }

// azurePlanDataDisks reads the storageProfile.dataDisks a machine write asks
// for against what the machine holds. A list the write leaves out keeps the
// disks; an empty list detaches them all.
func azurePlanDataDisks(vm VirtualMachine, prev *VirtualMachine) (azureDataDiskPlan, *azureVMRequestFault) {
	held := map[int]string{}
	if prev != nil {
		for _, entry := range azureVMDataDiskEntries(*prev) {
			if lun, ok := azureDataDiskLUN(entry); ok && azureDataDiskID(entry) != "" {
				held[lun] = azureDataDiskID(entry)
			}
		}
	}
	if _, listed := vm.Properties.StorageProfile["dataDisks"]; !listed && prev != nil {
		entries := anySlice(prev.Properties.StorageProfile["dataDisks"])
		if entries == nil {
			entries = []any{}
		}
		return azureDataDiskPlan{entries: entries}, nil
	}
	sub, rg := azureVMScope(vm.ID)
	plan := azureDataDiskPlan{entries: []any{}}
	luns := map[int]bool{}
	disks := map[string]bool{}
	wanted := map[int]string{}
	for _, entry := range azureVMDataDiskEntries(vm) {
		lun, ok := azureDataDiskLUN(entry)
		if !ok {
			return plan, azureDiskFault("InvalidParameter", http.StatusBadRequest, "Required parameter 'dataDisk.lun' is missing (null).")
		}
		if detaching, _ := entry["toBeDetached"].(bool); detaching {
			continue
		}
		if lun < 0 || lun > azureDataDiskMaxLUN {
			return plan, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"The value of parameter dataDisk.lun is invalid: %d is not between 0 and %d.", lun, azureDataDiskMaxLUN)
		}
		if luns[lun] {
			return plan, azureDiskFault("InvalidParameter", http.StatusBadRequest, "A disk at LUN %d already exists.", lun)
		}
		luns[lun] = true
		disk, move, created, fault := azurePlanDataDisk(vm, sub, rg, lun, entry)
		if fault != nil {
			return plan, fault
		}
		if disks[strings.ToLower(disk.ID)] {
			return plan, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"Disk '%s' is listed more than once in properties.storageProfile.dataDisks.", disk.ID)
		}
		disks[strings.ToLower(disk.ID)] = true
		wanted[lun] = disk.ID
		if created {
			plan.created = append(plan.created, disk)
		}
		if !strings.EqualFold(held[lun], disk.ID) {
			plan.attach = append(plan.attach, move)
		}
		plan.entries = append(plan.entries, azureNormalizedDataDisk(entry, disk, lun))
	}
	if limit := azureVMMaxDataDisks(vm); len(plan.entries) > limit {
		return plan, azureDiskFault("OperationNotAllowed", http.StatusConflict,
			"The maximum number of data disks allowed to be attached to a VM of this size is %d.", limit)
	}
	for lun, id := range held {
		if strings.EqualFold(wanted[lun], id) {
			continue
		}
		plan.detach = append(plan.detach, azureDataDiskMove{diskID: id, lun: lun, release: !disks[strings.ToLower(id)]})
	}
	sort.Slice(plan.detach, func(i, j int) bool { return plan.detach[i].lun < plan.detach[j].lun })
	return plan, nil
}

func azureVMScope(vmID string) (sub, rg string) {
	parts := strings.Split(strings.TrimPrefix(vmID, "/"), "/")
	if len(parts) >= 4 {
		return parts[1], parts[3]
	}
	return "", ""
}

// azurePlanDataDisk resolves one requested data disk to the managed disk it
// names or, with created set, the one it creates.
func azurePlanDataDisk(vm VirtualMachine, sub, rg string, lun int, entry map[string]any) (ManagedDisk, azureDataDiskMove, bool, *azureVMRequestFault) {
	move := azureDataDiskMove{lun: lun}
	option := stringProperty(entry, "createOption")
	if id := azureDataDiskID(entry); id != "" {
		disk, ok := azureLookupDisk(id)
		if !ok {
			return ManagedDisk{}, move, false, azureDiskFault("ResourceNotFound", http.StatusNotFound,
				"The Resource %q referenced by properties.storageProfile.dataDisks was not found.", id)
		}
		if disk.ManagedBy != "" && !strings.EqualFold(disk.ManagedBy, vm.ID) {
			return ManagedDisk{}, move, false, azureDiskFault("OperationNotAllowed", http.StatusConflict,
				"Disk '%s' cannot be attached to VM '%s': it is attached to VM '%s'.", disk.Name, vm.Name, disk.ManagedBy)
		}
		if !strings.EqualFold(disk.Location, vm.Location) {
			return ManagedDisk{}, move, false, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"Disk '%s' is in location '%s', and VM '%s' is in '%s'.", disk.Name, disk.Location, vm.Name, vm.Location)
		}
		move.diskID = disk.ID
		return disk, move, false, nil
	}
	switch {
	case strings.EqualFold(option, "Attach"):
		return ManagedDisk{}, move, false, azureDiskFault("InvalidParameter", http.StatusBadRequest,
			"Required parameter 'dataDisk.managedDisk.id' is missing (null): a disk attached by createOption Attach names it.")
	case strings.EqualFold(option, "Empty"), strings.EqualFold(option, "Copy"):
	default:
		return ManagedDisk{}, move, false, azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator creates a machine's data disks Empty, as a Copy of a managed disk, or attaches one; not %s.", option)
	}
	name := stringProperty(entry, "name")
	if name == "" {
		name = fmt.Sprintf("%s_lun_%d_%s", vm.Name, lun, strings.ReplaceAll(sim.NewUUID(), "-", ""))
	}
	id := armResourceID(sub, rg, "Microsoft.Compute", "disks", name)
	if _, exists := azureLookupDisk(id); exists {
		return ManagedDisk{}, move, false, azureDiskFault("OperationNotAllowed", http.StatusConflict,
			"Disk %s already exists in resource group %s. Only CreateOption.Attach is supported.", name, rg)
	}
	managed, _ := entry["managedDisk"].(map[string]any)
	req := ManagedDisk{
		Location:   vm.Location,
		Sku:        &DiskSku{Name: stringProperty(managed, "storageAccountType")},
		Properties: ManagedDiskProperties{CreationData: map[string]any{"createOption": option}, DiskSizeGB: int32(intProperty(entry, "diskSizeGB"))},
	}
	if source, _ := entry["sourceResource"].(map[string]any); source != nil {
		req.Properties.CreationData["sourceResourceId"] = stringProperty(source, "id")
	}
	disk, source, fault := azureNewDisk(id, name, req)
	if fault != nil {
		return ManagedDisk{}, move, false, fault
	}
	if source != nil {
		location := azureDiskLocationOf(*source)
		move.copyFrom = &location
		disk.Properties.ProvisioningState = "Updating"
	} else {
		disk.Properties.ProvisioningState = "Succeeded"
	}
	move.diskID = disk.ID
	return disk, move, true, nil
}

// azureNormalizedDataDisk is the DataDisk a machine reports for an attached
// managed disk.
func azureNormalizedDataDisk(entry map[string]any, disk ManagedDisk, lun int) map[string]any {
	out := map[string]any{
		"lun":                     lun,
		"name":                    disk.Name,
		"createOption":            stringProperty(entry, "createOption"),
		"caching":                 "None",
		"writeAcceleratorEnabled": false,
		"toBeDetached":            false,
		"deleteOption":            "Detach",
		"diskSizeGB":              disk.Properties.DiskSizeGB,
		"managedDisk": map[string]any{
			"id":                 disk.ID,
			"storageAccountType": disk.Sku.Name,
		},
	}
	if out["createOption"] == "" {
		out["createOption"] = "Attach"
	}
	for _, key := range []string{"caching", "deleteOption"} {
		if v := stringProperty(entry, key); v != "" {
			out[key] = v
		}
	}
	if accelerated, ok := entry["writeAcceleratorEnabled"].(bool); ok {
		out["writeAcceleratorEnabled"] = accelerated
	}
	return out
}

// azureHoldDataDisks records the plan before the machine's operation runs:
// the disks it creates, and its hold on the disks it attaches.
func azureHoldDataDisks(vmID string, plan azureDataDiskPlan) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, disk := range plan.created {
		azureDisks.Put(disk.ID, disk)
	}
	for _, move := range plan.attach {
		azureDisks.Update(move.diskID, func(d *ManagedDisk) {
			d.ManagedBy = vmID
			d.Properties.LastOwnershipUpdateTime = now
		})
	}
}

// azureMoveDataDisks moves the plan's disks out of and into the machine's bay:
// detaches first, so an attach may take a LUN a detach frees.
func azureMoveDataDisks(ctx context.Context, vm VirtualMachine, plan azureDataDiskPlan) error {
	bay := azureVMDiskBay(vm)
	for _, move := range plan.detach {
		disk, ok := azureLookupDisk(move.diskID)
		if !ok {
			continue
		}
		if err := azureCopyDiskData(ctx, azureDiskLocation{volume: bay, dir: azureLUNDir(move.lun)}, azureDiskLocation{volume: azureDiskVolume(disk)}, true); err != nil {
			return err
		}
		if move.release {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			azureDisks.Update(disk.ID, func(d *ManagedDisk) {
				if strings.EqualFold(d.ManagedBy, vm.ID) {
					d.ManagedBy = ""
					d.Properties.LastOwnershipUpdateTime = now
				}
			})
		}
	}
	for _, move := range plan.attach {
		disk, ok := azureLookupDisk(move.diskID)
		if !ok {
			return fmt.Errorf("managed disk %s is gone", move.diskID)
		}
		source := azureDiskLocation{volume: azureDiskVolume(disk)}
		if move.copyFrom != nil {
			source = *move.copyFrom
		}
		err := azureCopyDiskData(ctx, source, azureDiskLocation{volume: bay, dir: azureLUNDir(move.lun)}, false)
		if move.copyFrom != nil {
			azureSettleDisk(disk.ID, err)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// azureDataDiskFailure is how a failed move reports on the machine's
// operation.
func azureDataDiskFailure(id string, err error) *AsyncOperationError {
	return &AsyncOperationError{
		Code:    "InternalExecutionError",
		Message: fmt.Sprintf("An internal execution error occurred moving the data disks of virtual machine '%s': %v", id, err),
	}
}

// azureReleaseVMDataDisks runs once a deleted machine's workload is gone: a
// disk whose deleteOption says Delete goes with the machine, every other one
// is copied back out and left unattached, and the bay goes unless the kept OS
// disk still mounts it.
func azureReleaseVMDataDisks(ctx context.Context, vm VirtualMachine) error {
	bay := azureVMDiskBay(vm)
	for _, entry := range azureVMDataDiskEntries(vm) {
		lun, ok := azureDataDiskLUN(entry)
		disk, found := azureLookupDisk(azureDataDiskID(entry))
		if !ok || !found {
			continue
		}
		if strings.EqualFold(stringProperty(entry, "deleteOption"), "Delete") {
			azureDisks.Delete(disk.ID)
			sim.RemoveVolumeSettled(azureDiskVolume(disk), "azure")
			continue
		}
		if err := azureCopyDiskData(ctx, azureDiskLocation{volume: bay, dir: azureLUNDir(lun)}, azureDiskLocation{volume: azureDiskVolume(disk)}, true); err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		azureDisks.Update(disk.ID, func(d *ManagedDisk) {
			d.ManagedBy = ""
			d.Properties.LastOwnershipUpdateTime = now
		})
	}
	osDisk, _ := vm.Properties.StorageProfile["osDisk"].(map[string]any)
	if !strings.EqualFold(stringProperty(osDisk, "deleteOption"), "Detach") {
		sim.RemoveVolumeSettled(bay, "azure")
	}
	return nil
}

// registerVirtualMachineDataDiskOperations serves
// VirtualMachines_AttachDetachDataDisks: the machine's dataDisks with the
// named disks detached and attached, its StorageProfile the operation's result.
func registerVirtualMachineDataDiskOperations(srv *sim.Server, armBase string) {
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/attachDetachDataDisks", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DataDisksToAttach []struct {
				DiskID                  string `json:"diskId"`
				LUN                     *int   `json:"lun"`
				Caching                 string `json:"caching"`
				DeleteOption            string `json:"deleteOption"`
				WriteAcceleratorEnabled *bool  `json:"writeAcceleratorEnabled"`
			} `json:"dataDisksToAttach"`
			DataDisksToDetach []struct {
				DiskID string `json:"diskId"`
			} `json:"dataDisksToDetach"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		if len(req.DataDisksToAttach) == 0 && len(req.DataDisksToDetach) == 0 {
			AzureError(w, "InvalidParameter", "At least one of dataDisksToAttach or dataDisksToDetach is required.", http.StatusBadRequest)
			return
		}
		detaching := map[string]bool{}
		for _, d := range req.DataDisksToDetach {
			detaching[strings.ToLower(d.DiskID)] = true
		}
		var entries []any
		used := map[int]bool{}
		for _, entry := range azureVMDataDiskEntries(vm) {
			if detaching[strings.ToLower(azureDataDiskID(entry))] {
				delete(detaching, strings.ToLower(azureDataDiskID(entry)))
				continue
			}
			if lun, ok := azureDataDiskLUN(entry); ok {
				used[lun] = true
			}
			entries = append(entries, entry)
		}
		for diskID := range detaching {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"Disk '%s' named in dataDisksToDetach is not attached to VM '%s'.", diskID, vm.Name)
			return
		}
		for _, d := range req.DataDisksToAttach {
			lun := 0
			if d.LUN != nil {
				lun = *d.LUN
			} else {
				for used[lun] {
					lun++
				}
			}
			used[lun] = true
			entry := map[string]any{"lun": float64(lun), "createOption": "Attach", "managedDisk": map[string]any{"id": d.DiskID}}
			if d.Caching != "" {
				entry["caching"] = d.Caching
			}
			if d.DeleteOption != "" {
				entry["deleteOption"] = d.DeleteOption
			}
			if d.WriteAcceleratorEnabled != nil {
				entry["writeAcceleratorEnabled"] = *d.WriteAcceleratorEnabled
			}
			entries = append(entries, entry)
		}
		requested := vm
		requested.Properties.StorageProfile = azureCopyStorageProfile(vm.Properties.StorageProfile)
		requested.Properties.StorageProfile["dataDisks"] = entries
		plan, fault := azurePlanDataDisks(requested, &vm)
		if fault != nil {
			AzureError(w, fault.code, fault.message, fault.status)
			return
		}
		if !azureClaimVMOperation(id, "") {
			azureVMOperationConflict(w, "AttachDetachDataDisks", id)
			return
		}
		azureHoldDataDisks(id, plan)
		azureVMs.Update(id, func(stored *VirtualMachine) {
			stored.Properties.StorageProfile = azureCopyStorageProfile(stored.Properties.StorageProfile)
			stored.Properties.StorageProfile["dataDisks"] = plan.entries
		})
		vm, _ = azureVMs.Get(id)
		azureVMProvisioningErrors.Delete(id)
		opID := startAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
			var opErr *AsyncOperationError
			if err := azureMoveDataDisks(context.Background(), vm, plan); err != nil {
				opErr = azureDataDiskFailure(id, err)
			}
			azureSettleVMOperation(id, opErr)
			if opErr != nil {
				return nil, opErr
			}
			settled, _ := azureVMs.Get(id)
			profile, err := json.Marshal(settled.Properties.StorageProfile)
			if err != nil {
				return nil, &AsyncOperationError{Code: "InternalExecutionError", Message: err.Error()}
			}
			return profile, nil
		})
		writeAzureVMOperationAccepted(w, r, vm, opID)
	})
}

// azureCopyStorageProfile copies the profile's top level, so a write to the
// copy's dataDisks leaves a stored machine untouched.
func azureCopyStorageProfile(profile map[string]any) map[string]any {
	out := make(map[string]any, len(profile)+1)
	for k, v := range profile {
		out[k] = v
	}
	return out
}
