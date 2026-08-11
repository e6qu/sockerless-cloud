package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The Microsoft.Compute virtual-machine operations beyond the lifecycle in
// compute.go. They divide by what they actually need, and each is served here
// only because that need is met:
//
//   - Off the machine's own state: ListAvailableSizes, ListByLocation,
//     Generalize, ConvertToManagedDisks.
//   - Off the real guest process: Redeploy, Reimage, Reapply,
//     PerformMaintenance and SimulateEviction all move a running Firecracker
//     guest, and RetrieveBootDiagnosticsData returns the console output that
//     guest really produced.
//
// The operations still unserved — Capture and the extension and patch
// families — need an execution path inside the guest that does not exist, and
// are tracked rather than answered with invented data.

// azureVMResourceID renders the ARM id for the virtual machine a request
// addresses.
func azureVMResourceID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "vmName"))
}

// azureLookupVM resolves the addressed machine, writing ARM's not-found error
// when it does not exist.
func azureLookupVM(w http.ResponseWriter, r *http.Request) (VirtualMachine, string, bool) {
	id := azureVMResourceID(r)
	vm, ok := azureVMs.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
		return VirtualMachine{}, id, false
	}
	return vm, id, true
}

func registerVirtualMachineOperations(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute"

	registerVirtualMachineStateOperations(srv, armBase)
	registerVirtualMachineGuestOperations(srv, armBase)
	registerVirtualMachineBootDiagnostics(srv, armBase)
}

// ===== Operations answered from the machine's own state =====

func registerVirtualMachineStateOperations(srv *sim.Server, armBase string) {
	// VirtualMachines_ListAvailableSizes — the sizes this machine can be
	// resized to. Azure answers with the sizes its current hardware cluster
	// offers, which is the location's catalogue minus nothing the simulator
	// models differently, so it is the same catalogue the location-scoped
	// vmSizes read returns.
	srv.HandleFunc("GET "+armBase+"/virtualMachines/{vmName}/vmSizes", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := azureLookupVM(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": azureVMSizeCatalogue()})
	})

	// VirtualMachines_ListByLocation — every machine in one location across
	// the subscription's resource groups, paged the way ARM pages.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/virtualMachines",
		func(w http.ResponseWriter, r *http.Request) {
			prefix := fmt.Sprintf("/subscriptions/%s/", sim.PathParam(r, "subscriptionId"))
			location := sim.PathParam(r, "location")
			all := azureVMs.Filter(func(vm VirtualMachine) bool {
				return strings.HasPrefix(vm.ID, prefix) && strings.EqualFold(vm.Location, location)
			})
			sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
			page, next := armPage(r, all)
			if page == nil {
				page = []VirtualMachine{}
			}
			out := map[string]any{"value": page}
			if next != "" {
				out["nextLink"] = armNextLink(r, next)
			}
			sim.WriteJSON(w, http.StatusOK, out)
		})

	// VirtualMachines_Generalize — marks the machine's operating system
	// generalized so an image can be captured from it. Azure requires the
	// machine to be stopped first and refuses otherwise, because generalizing
	// a running system would capture a machine mid-write.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/generalize", func(w http.ResponseWriter, r *http.Request) {
		_, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		state, _ := azureVMStates.Get(id)
		if state == "PowerState/running" && azureRealVMAlive(id) {
			sim.AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
				"Generalize is not allowed on VM %q because it is not in a stopped state.", id)
			return
		}
		azureVMGeneralized.Put(id, true)
		w.WriteHeader(http.StatusOK)
	})

	// VirtualMachines_ConvertToManagedDisks — moves a machine off unmanaged
	// (page-blob) disks onto managed ones. Azure requires the machine to be
	// deallocated, and the conversion is a no-op for a machine already on
	// managed disks.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/convertToManagedDisks", func(w http.ResponseWriter, r *http.Request) {
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		state, _ := azureVMStates.Get(id)
		if state == "PowerState/running" && azureRealVMAlive(id) {
			sim.AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
				"ConvertToManagedDisks is not allowed on VM %q because it is not in a deallocated state.", id)
			return
		}
		if converted := azureConvertVMDisksToManaged(&vm); converted {
			azureVMs.Put(id, vm)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// azureVMSizeCatalogue is the machine sizes this simulator's Compute slice
// offers. It is the single catalogue behind both the location-scoped read and
// the per-machine resize read, so the two cannot disagree.
func azureVMSizeCatalogue() []map[string]any {
	return []map[string]any{
		{
			"name":                 "Standard_B1s",
			"numberOfCores":        1,
			"osDiskSizeInMB":       1047552,
			"resourceDiskSizeInMB": 4096,
			"memoryInMB":           1024,
			"maxDataDiskCount":     2,
		},
		{
			"name":                 "Standard_B2s",
			"numberOfCores":        2,
			"osDiskSizeInMB":       1047552,
			"resourceDiskSizeInMB": 8192,
			"memoryInMB":           4096,
			"maxDataDiskCount":     4,
		},
	}
}

// azureConvertVMDisksToManaged rewrites the machine's storage profile from
// unmanaged disks to managed ones, which is what the conversion produces: the
// vhd reference is replaced by a managedDisk reference carrying the account
// type. It reports whether anything changed, so a machine already on managed
// disks is left alone exactly as the real conversion leaves it.
func azureConvertVMDisksToManaged(vm *VirtualMachine) bool {
	storage := vm.Properties.StorageProfile
	if storage == nil {
		return false
	}
	changed := false
	convert := func(disk map[string]any) {
		if _, unmanaged := disk["vhd"]; !unmanaged {
			return
		}
		delete(disk, "vhd")
		if _, ok := disk["managedDisk"]; !ok {
			disk["managedDisk"] = map[string]any{"storageAccountType": "Standard_LRS"}
		}
		if _, ok := disk["createOption"]; !ok {
			disk["createOption"] = "Attach"
		}
		changed = true
	}
	if os, ok := storage["osDisk"].(map[string]any); ok {
		convert(os)
	}
	if data, ok := storage["dataDisks"].([]any); ok {
		for _, entry := range data {
			if disk, ok := entry.(map[string]any); ok {
				convert(disk)
			}
		}
	}
	return changed
}

// ===== Operations that move the real guest =====

func registerVirtualMachineGuestOperations(srv *sim.Server, armBase string) {
	logger := srv.Logger()

	// Each of these ends with the machine running again on a fresh guest
	// process, which is what the operation means on real hardware: Azure moves
	// the machine to a new node (redeploy), resets its disk to the image
	// (reimage), reapplies its model (reapply), or moves it off a node due for
	// maintenance. The simulator does the same thing to the Firecracker guest
	// rather than recording that it happened.
	for _, action := range []string{"redeploy", "reimage", "reapply", "performMaintenance"} {
		action := action
		srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/"+action, func(w http.ResponseWriter, r *http.Request) {
			vm, id, ok := azureLookupVM(w, r)
			if !ok {
				return
			}
			// A machine that was never started, or was deliberately stopped,
			// stays stopped: these operations restore a machine to the state it
			// was in, they do not start a stopped one.
			state, known := azureVMStates.Get(id)
			wasRunning := !known || state == "PowerState/running"

			if err := azureStopRealVM(r.Context(), id); err != nil {
				sim.AzureErrorf(w, "OperationNotAllowed", http.StatusServiceUnavailable,
					"failed to stop the virtual machine for %s: %v", action, err)
				return
			}
			if !wasRunning {
				sim.WriteJSON(w, http.StatusOK, map[string]any{"status": "Succeeded"})
				return
			}
			if err := azureStartRealVM(r.Context(), vm); err != nil {
				logger.Error().Err(err).Str("vm", id).Str("action", action).
					Msg("failed to bring the virtual machine back up")
				sim.AzureErrorf(w, "OperationNotAllowed", http.StatusServiceUnavailable,
					"failed to bring the virtual machine back up after %s: %v", action, err)
				return
			}
			azureVMStates.Put(id, "PowerState/running")
			sim.WriteJSON(w, http.StatusOK, map[string]any{"status": "Succeeded"})
		})
	}

	// VirtualMachines_SimulateEviction — Azure evicts a Spot machine, which
	// stops it. It applies only to Spot machines and is refused for any other,
	// because a regular machine is never evicted. The response carries no body.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/simulateEviction", func(w http.ResponseWriter, r *http.Request) {
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		if !strings.EqualFold(vm.Properties.Priority, "Spot") {
			sim.AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
				"Simulate Eviction is only supported for Spot VMs; %q has priority %q.",
				id, vm.Properties.Priority)
			return
		}
		if err := azureStopRealVM(r.Context(), id); err != nil {
			sim.AzureErrorf(w, "OperationNotAllowed", http.StatusServiceUnavailable,
				"failed to evict the virtual machine: %v", err)
			return
		}
		// Azure deletes an evicted machine whose eviction policy says Delete and
		// deallocates one whose policy says Deallocate.
		if strings.EqualFold(vm.Properties.EvictionPolicy, "Delete") {
			_ = azureDeleteRealVM(r.Context(), vm)
			azureVMs.Delete(id)
			azureVMStates.Delete(id)
		} else {
			azureVMStates.Put(id, "PowerState/deallocated")
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ===== Boot diagnostics =====

func registerVirtualMachineBootDiagnostics(srv *sim.Server, armBase string) {
	// VirtualMachines_RetrieveBootDiagnosticsData returns the URIs of the
	// machine's boot artifacts. The serial console log is the output the
	// Firecracker guest really wrote to its console, stored into the storage
	// account the machine's diagnostics profile names and read back through the
	// same Blob data plane a client downloads it from.
	//
	// No console screenshot is returned. The guest is a serial-console machine
	// with no framebuffer, so there is no screenshot to take; the member is
	// optional in the response, and a URI pointing at an image that does not
	// exist would be worse than its absence.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/retrieveBootDiagnosticsData", func(w http.ResponseWriter, r *http.Request) {
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		account, err := azureBootDiagnosticsAccount(vm)
		if err != nil {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "%v", err)
			return
		}
		console, err := azureGuestConsoleOutput(id)
		if err != nil {
			sim.AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "%v", err)
			return
		}
		container, blobName := azureBootDiagnosticsPath(vm)
		now := time.Now().UTC().Format(http.TimeFormat)
		putBlobObject(BlobObject{
			Account:      account,
			Container:    container,
			Name:         blobName,
			Data:         console,
			ContentType:  "text/plain",
			BlobType:     "BlockBlob",
			ETag:         azureNetworkEtag(),
			LastModified: now,
			CreationTime: now,
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"serialConsoleLogBlobUri": fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s",
				account, container, blobName),
		})
	})
}

// azureBootDiagnosticsAccount resolves the storage account the machine's boot
// artifacts are written to. Azure refuses the read outright when boot
// diagnostics is not enabled for the machine, which is the same refusal here.
func azureBootDiagnosticsAccount(vm VirtualMachine) (string, error) {
	diagnostics, _ := vm.Properties.DiagnosticsProfile["bootDiagnostics"].(map[string]any)
	if diagnostics == nil {
		return "", fmt.Errorf("boot diagnostics is not enabled for virtual machine %q", vm.ID)
	}
	if enabled, ok := diagnostics["enabled"].(bool); ok && !enabled {
		return "", fmt.Errorf("boot diagnostics is not enabled for virtual machine %q", vm.ID)
	}
	storageURI, _ := diagnostics["storageUri"].(string)
	if storageURI == "" {
		// Managed boot diagnostics stores the artifacts in an account the
		// platform owns rather than one the machine names. The simulator has no
		// such account, and a URI pointing at storage that does not exist is
		// worse than a refusal that says which coordinate is missing.
		return "", fmt.Errorf(
			"virtual machine %q uses managed boot diagnostics; this simulator serves boot "+
				"diagnostics from the storage account the machine names, so set "+
				"diagnosticsProfile.bootDiagnostics.storageUri", vm.ID)
	}
	host := strings.TrimPrefix(strings.TrimPrefix(storageURI, "https://"), "http://")
	account, _, _ := strings.Cut(host, ".")
	if account == "" {
		return "", fmt.Errorf("boot diagnostics storageUri %q does not name a storage account", storageURI)
	}
	return account, nil
}

// azureBootDiagnosticsPath is where in the account the artifacts land. Azure
// uses a per-machine container named for the machine's identifier.
func azureBootDiagnosticsPath(vm VirtualMachine) (container, blob string) {
	name := strings.ToLower(vm.Name)
	if name == "" {
		name = "vm"
	}
	return "bootdiagnostics-" + name, name + ".serialconsole.log"
}

// azureGuestConsoleOutput reads the console output the machine's Firecracker
// guest wrote. A machine with no live guest has produced no console output, and
// saying so is the honest answer — an empty log would be indistinguishable from
// a guest that booted silently.
func azureGuestConsoleOutput(vmID string) ([]byte, error) {
	azureRealMu.Lock()
	guest := azureRealVMs[vmID]
	azureRealMu.Unlock()
	if guest == nil {
		return nil, fmt.Errorf(
			"virtual machine %q has no running guest, so it has produced no console output", vmID)
	}
	console, err := os.ReadFile(filepath.Join(guest.WorkDir, "firecracker-console.log"))
	if err != nil {
		return nil, fmt.Errorf("read the guest console output of %q: %w", vmID, err)
	}
	return console, nil
}
