package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure Resource Manager answers a virtual-machine create, start, restart,
// power-off, deallocate, redeploy, reimage, reapply or maintenance request at
// once — 201/200 for the PUT, 202 for the actions — with an
// Azure-AsyncOperation to poll, and the Compute resource provider moves the
// machine behind that operation: provisioningState reads Creating or Updating
// and the instance view's power state reads starting, stopping or deallocating
// until the guest has really moved, then Succeeded (or Failed) and the settled
// power state.

// azureBootVM and azureHaltVM move a machine's guest. A unit test on a host
// without nested KVM substitutes them to hold a boot open on a channel and
// observe the operation around it.
var (
	azureBootVM = azureStartRealVM
	azureHaltVM = azureStopRealVM
)

// azureVMProvisioningErrors holds the error of each machine whose last
// operation failed; its instance view reports it beside provisioningState
// Failed.
var azureVMProvisioningErrors sim.Store[AsyncOperationError]

func registerVirtualMachineOperationRecovery(srv *sim.Server) {
	azureVMProvisioningErrors = sim.MakeStore[AsyncOperationError](srv.DB(), "compute_virtual_machine_provisioning_errors")
	// The goroutine behind a machine's operation died with the previous
	// process, and the operation store fails that operation on restart; the
	// machine fails with it instead of staying Creating or Updating, which
	// would refuse every later operation on it.
	for _, vm := range azureVMs.List() {
		if !azureVMOperationRunning(vm.Properties.ProvisioningState) {
			continue
		}
		azureVMs.Update(vm.ID, func(stale *VirtualMachine) {
			stale.Properties.ProvisioningState = "Failed"
		})
		azureVMProvisioningErrors.Put(vm.ID, AsyncOperationError{
			Code:    "OperationInterrupted",
			Message: "The operation was interrupted by a service restart before it completed and cannot be resumed. Retry the request.",
		})
		state := "PowerState/stopped"
		if azureFabric.VMAlive(vm.ID) {
			state = "PowerState/running"
		}
		azureVMStates.Put(vm.ID, state)
	}
}

func azureVMOperationRunning(provisioningState string) bool {
	return provisioningState == "Creating" || provisioningState == "Updating"
}

// azureClaimVMOperation moves an existing machine to provisioningState
// Updating and its power state to transition, and reports false when another
// operation still holds the machine.
func azureClaimVMOperation(id, transition string) bool {
	claimed := false
	azureVMs.Update(id, func(vm *VirtualMachine) {
		if azureVMOperationRunning(vm.Properties.ProvisioningState) {
			return
		}
		vm.Properties.ProvisioningState = "Updating"
		claimed = true
	})
	if claimed && transition != "" {
		azureVMStates.Put(id, transition)
	}
	return claimed
}

// azureVMOperationConflict is ARM's refusal of an operation on a machine
// another operation still holds.
func azureVMOperationConflict(w http.ResponseWriter, operation, id string) {
	AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
		"Operation '%s' is not allowed on VM '%s' since another operation is in progress on it.",
		operation, id[strings.LastIndex(id, "/")+1:])
}

// azureRunVMOperation runs work behind a new operation and settles the
// machine's provisioningState by its outcome.
func azureRunVMOperation(id string, work func(ctx context.Context) *AsyncOperationError) string {
	azureVMProvisioningErrors.Delete(id)
	return startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		opErr := work(context.Background())
		azureVMs.Update(id, func(vm *VirtualMachine) {
			vm.Properties.ProvisioningState = "Succeeded"
			if opErr != nil {
				vm.Properties.ProvisioningState = "Failed"
			}
		})
		if opErr != nil {
			azureVMProvisioningErrors.Put(id, *opErr)
		}
		return opErr
	})
}

// azureVMBootFailure is the error a failed operation reports when the host
// could not boot the machine's guest. A networkProfile that stopped resolving
// keeps its own request-validation code; anything else is the host failing to
// place the machine, which Azure reports as AllocationFailed.
func azureVMBootFailure(id string, err error) *AsyncOperationError {
	var fault *azureVMRequestFault
	if errors.As(err, &fault) {
		return &AsyncOperationError{Code: fault.code, Message: fault.message}
	}
	return &AsyncOperationError{
		Code:    "AllocationFailed",
		Message: fmt.Sprintf("Allocation failed. The host could not boot virtual machine '%s': %v", id, err),
	}
}

// azureVMHaltFailure is the error a failed operation reports when the host
// could not stop the machine's guest.
func azureVMHaltFailure(id string, err error) *AsyncOperationError {
	return &AsyncOperationError{
		Code:    "InternalExecutionError",
		Message: fmt.Sprintf("An internal execution error occurred stopping virtual machine '%s': %v", id, err),
	}
}

// writeAzureVMOperationAccepted answers an action on a machine the way the
// Compute resource provider does: 202 with no body, the operation's status URL
// as Azure-AsyncOperation and its monitor URL as Location.
func writeAzureVMOperationAccepted(w http.ResponseWriter, r *http.Request, vm VirtualMachine, opID string) {
	asyncOperation, monitor := computeOperationURLs(r, sim.PathParam(r, "subscriptionId"), vm.Location, opID)
	writeAzureAsyncCreateHeaders(w, opID, asyncOperation, monitor)
	w.WriteHeader(http.StatusAccepted)
}

// azureVMProvisioningStatus is the ProvisioningState entry of a machine's
// instance view.
func azureVMProvisioningStatus(vm VirtualMachine) VMStatus {
	switch vm.Properties.ProvisioningState {
	case "Creating":
		return VMStatus{Code: "ProvisioningState/creating", Level: "Info", DisplayStatus: "Creating"}
	case "Updating":
		return VMStatus{Code: "ProvisioningState/updating", Level: "Info", DisplayStatus: "Updating"}
	case "Failed":
		status := VMStatus{Code: "ProvisioningState/failed", Level: "Error", DisplayStatus: "Provisioning failed"}
		if opErr, ok := azureVMProvisioningErrors.Get(vm.ID); ok {
			status.Code += "/" + opErr.Code
			status.Message = opErr.Message
		}
		return status
	default:
		return VMStatus{Code: "ProvisioningState/succeeded", Level: "Info", DisplayStatus: "Provisioning succeeded"}
	}
}
