package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Microsoft.Compute virtual-machine operations beyond the lifecycle in
// compute.go. Redeploy, Reimage, Reapply, PerformMaintenance and
// SimulateEviction move the workload through the placement hooks;
// RetrieveBootDiagnosticsData returns the guest's console output.

func azureVMResourceID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "virtualMachines", sim.PathParam(r, "vmName"))
}

// azureLookupVM writes the not-found error when the machine is absent.
func azureLookupVM(w http.ResponseWriter, r *http.Request) (VirtualMachine, string, bool) {
	id := azureVMResourceID(r)
	vm, ok := azureVMs.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
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

func registerVirtualMachineStateOperations(srv *sim.Server, armBase string) {
	// The sizes the machine can resize to: the location's catalogue.
	srv.HandleFunc("GET "+armBase+"/virtualMachines/{vmName}/vmSizes", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := azureLookupVM(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": azureVMSizeCatalogue()})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/virtualMachines",
		func(w http.ResponseWriter, r *http.Request) {
			prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
			location := sim.PathParam(r, "location")
			all := azureVMs.Filter(func(vm VirtualMachine) bool {
				return strings.HasPrefix(vm.ID, prefix) && strings.EqualFold(vm.Location, location)
			})
			page, next, pageOK := armPage(w, r, all, func(vm VirtualMachine) string { return vm.ID })
			if !pageOK {
				return
			}
			out := map[string]any{"value": page}
			if next != "" {
				out["nextLink"] = armNextLink(r, next)
			}
			sim.WriteJSON(w, http.StatusOK, out)
		})

	// Azure requires the machine to be stopped: generalizing a running system
	// would capture it mid-write.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/generalize", func(w http.ResponseWriter, r *http.Request) {
		_, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		if state, _ := azureVMStates.Get(id); state == "PowerState/running" {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
				"Generalize is not allowed on VM %q because it is not in a stopped state.", id)
			return
		}
		azureVMGeneralized.Put(id, true)
		w.WriteHeader(http.StatusOK)
	})

	// Azure requires the machine deallocated; a machine already on managed disks
	// is a no-op.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/convertToManagedDisks", func(w http.ResponseWriter, r *http.Request) {
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		if state, _ := azureVMStates.Get(id); state == "PowerState/running" {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
				"ConvertToManagedDisks is not allowed on VM %q because it is not in a deallocated state.", id)
			return
		}
		if converted := azureConvertVMDisksToManaged(&vm); converted {
			azureVMs.Put(id, vm)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// The single catalogue behind the location read and the per-machine resize
// read.
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

// azureConvertVMDisksToManaged replaces each vhd reference with a managedDisk
// reference carrying the account type, and reports whether anything changed.
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

func registerVirtualMachineGuestOperations(srv *sim.Server, armBase string) {
	logger := srv.Logger()

	// Each ends with the machine on a fresh workload, as Azure moves it to a new
	// node (redeploy), resets its disk to the image (reimage), reapplies its model
	// (reapply), or moves it off a node due for maintenance.
	for _, action := range []string{"redeploy", "reimage", "reapply", "performMaintenance"} {
		action := action
		srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/"+action, func(w http.ResponseWriter, r *http.Request) {
			vm, id, ok := azureLookupVM(w, r)
			if !ok {
				return
			}
			// These operations restore a machine's prior state; they do not start a
			// stopped one.
			priorState, known := azureVMStates.Get(id)
			if !known {
				priorState = "PowerState/running"
			}
			wasRunning := priorState == "PowerState/running"
			transition := ""
			if wasRunning {
				transition = "PowerState/stopping"
			}
			if !azureClaimVMOperation(id, transition) {
				azureVMOperationConflict(w, action, id)
				return
			}
			opID := azureRunVMOperation(id, func(ctx context.Context) *AsyncOperationError {
				if err := azureHaltVM(ctx, id); err != nil {
					azureVMStates.Put(id, priorState)
					return azureVMHaltFailure(id, err)
				}
				// A reimage resets the disk to the image, so the kept disk goes.
				if action == "reimage" {
					if err := azureDiscardVMDisk(ctx, id); err != nil {
						azureVMStates.Put(id, priorState)
						return azureVMHaltFailure(id, err)
					}
				}
				if !wasRunning {
					return nil
				}
				azureVMStates.Put(id, "PowerState/starting")
				if err := azureBootVM(ctx, vm); err != nil {
					logger.Error().Err(err).Str("vm", id).Str("action", action).
						Msg("failed to bring the virtual machine back up")
					azureVMStates.Put(id, "PowerState/stopped")
					return azureVMBootFailure(id, err)
				}
				azureVMStates.Put(id, "PowerState/running")
				return nil
			})
			writeAzureVMOperationAccepted(w, r, vm, opID)
		})
	}

	// VirtualMachines_SimulateEviction applies only to Spot machines. The
	// specification declares 204 with no body and no long-running operation: Azure
	// carries the eviction out afterwards, deleting or deallocating per the
	// eviction policy.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/simulateEviction", func(w http.ResponseWriter, r *http.Request) {
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		if !strings.EqualFold(vm.Properties.Priority, "Spot") {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
				"Simulate Eviction is only supported for Spot VMs; %q has priority %q.",
				id, vm.Properties.Priority)
			return
		}
		deletes := strings.EqualFold(vm.Properties.EvictionPolicy, "Delete")
		priorState, _ := azureVMStates.Get(id)
		var claimed bool
		if deletes {
			claimed = azureClaimVMOperationAs(id, "Deleting", "")
		} else {
			claimed = azureClaimVMOperation(id, "PowerState/deallocating")
		}
		if !claimed {
			azureVMOperationConflict(w, "SimulateEviction", id)
			return
		}
		azureRunVMWork(id, func(ctx context.Context) *AsyncOperationError {
			if deletes {
				if err := azureDestroyVM(ctx, vm); err != nil {
					logger.Error().Err(err).Str("vm", id).Msg("failed to delete an evicted virtual machine")
					return azureVMHaltFailure(id, err)
				}
				azureForgetVM(id)
				return nil
			}
			if err := azureHaltVM(ctx, id); err != nil {
				logger.Error().Err(err).Str("vm", id).Msg("failed to deallocate an evicted virtual machine")
				azureVMStates.Put(id, priorState)
				return azureVMHaltFailure(id, err)
			}
			azureVMStates.Put(id, "PowerState/deallocated")
			return nil
		})
		w.WriteHeader(http.StatusNoContent)
	})
}

func registerVirtualMachineBootDiagnostics(srv *sim.Server, armBase string) {
	// RetrieveBootDiagnosticsData: the serial console log is the guest's console
	// output, stored in the account the diagnostics profile names and read through
	// the Blob data plane. The guest has no framebuffer, so the optional screenshot
	// member is absent.
	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/retrieveBootDiagnosticsData", func(w http.ResponseWriter, r *http.Request) {
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		account, err := azureBootDiagnosticsAccount(vm)
		if err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "%v", err)
			return
		}
		console, err := azureGuestConsole(r.Context(), id)
		if err != nil {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "%v", err)
			return
		}
		container, blobName := azureBootDiagnosticsPath(vm)
		now := time.Now().UTC().Format(http.TimeFormat)
		if err := putBlobWithContents(BlobObject{
			Account:      account,
			Container:    container,
			Name:         blobName,
			ContentType:  "text/plain",
			BlobType:     "BlockBlob",
			ETag:         azureNetworkEtag(),
			LastModified: now,
			CreationTime: now,
		}, console); err != nil {
			AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"serialConsoleLogBlobUri": fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s",
				account, container, blobName),
		})
	})
}

// Azure refuses the read when boot diagnostics is not enabled.
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
		// Managed boot diagnostics writes to an account the platform owns, which the
		// simulator does not have; refuse and name the missing coordinate.
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

// Azure uses a per-machine container named for the machine's identifier.
func azureBootDiagnosticsPath(vm VirtualMachine) (container, blob string) {
	name := strings.ToLower(vm.Name)
	if name == "" {
		name = "vm"
	}
	return "bootdiagnostics-" + name, name + ".serialconsole.log"
}
