package main

import (
	"context"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A machine's OS disk outlives the instance that runs it, as Azure's managed
// disk outlives the machine: a deallocated machine starts again with its data,
// and the capture sequence is deallocate, generalize, capture. On the
// container backend the disk is the instance's container, kept after it
// exits; the ledger names it by machine so a start boots it again, a capture
// commits it, and a delete removes it.

var azureVMDisks sim.Store[string]

func registerVirtualMachineDisks(srv *sim.Server) {
	azureVMDisks = sim.MakeStore[string](srv.DB(), "compute_virtual_machine_disks")
}

func azureVMDiskKey(vmID string) string { return strings.ToLower(vmID) }

// azureRecordVMDisk names the container holding the machine's OS disk.
func azureRecordVMDisk(vmID, containerID string) {
	azureVMDisks.Put(azureVMDiskKey(vmID), containerID)
}

// azureVMKeptContainer is the container holding a stopped machine's OS disk.
func azureVMKeptContainer(vmID string) (string, bool) {
	containerID, ok := azureVMDisks.Get(azureVMDiskKey(vmID))
	return containerID, ok && containerID != ""
}

// azureDiscardVMDisk removes a machine's kept OS disk, for a machine that is
// deleted or reimaged rather than stopped.
func azureDiscardVMDisk(vmID string) error {
	containerID, ok := azureVMKeptContainer(vmID)
	if !ok {
		return nil
	}
	backend, err := azurePlacementBackend(placementVirtualMachines)
	if err != nil {
		return err
	}
	if err := backend.Remove(context.Background(), containerID); err != nil {
		return err
	}
	azureVMDisks.Delete(azureVMDiskKey(vmID))
	return nil
}
