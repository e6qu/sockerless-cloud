package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Azure Resource Manager answers a virtual-machine create, action or delete at
// once — 201/200 for the PUT, 202 for the actions and DELETE — with an
// Azure-AsyncOperation. provisioningState reads Creating, Updating or Deleting
// and the power state starting, stopping or deallocating until the guest has
// moved, then Succeeded or Failed and the settled power state.

// A unit test substitutes these to hold a move open on a channel.
var (
	azureBootVM    = azureBootVMWorkload
	azureHaltVM    = azureHaltVMWorkload
	azureDestroyVM = azureDestroyVMWorkload
)

var azureGuestExec = azureGuestExecInWorkload

var azureGuestConsole = azureGuestConsoleOfWorkload

// azureVMRequestFault is a write the client cannot fix by retrying. Azure
// rejects it at request validation — 400 InvalidParameter for an unusable
// networkProfile, 404 ResourceNotFound for a missing interface — never as a
// failed operation an SDK retry policy would retry.
type azureVMRequestFault struct {
	code    string
	status  int
	message string
}

func (f *azureVMRequestFault) Error() string { return f.message }

// azureValidateVMNetworkProfile applies those rules from Azure Resource Manager
// state alone.
func azureValidateVMNetworkProfile(vm VirtualMachine) *azureVMRequestFault {
	nics := vm.Properties.NetworkProfile.NetworkInterfaces
	if len(nics) != 1 {
		return &azureVMRequestFault{
			code:   "InvalidParameter",
			status: http.StatusBadRequest,
			message: fmt.Sprintf("The value of parameter properties.networkProfile.networkInterfaces is invalid: exactly one network interface is required, got %d.",
				len(nics)),
		}
	}
	armNIC, ok := azureNICs.Get(nics[0].ID)
	if !ok {
		return &azureVMRequestFault{
			code:    "ResourceNotFound",
			status:  http.StatusNotFound,
			message: fmt.Sprintf("The Resource %q referenced by properties.networkProfile.networkInterfaces was not found.", nics[0].ID),
		}
	}
	if len(armNIC.Properties.IPConfigurations) == 0 || azurePrimaryIPConfig(armNIC).Properties.Subnet == nil {
		return &azureVMRequestFault{
			code:   "InvalidParameter",
			status: http.StatusBadRequest,
			message: fmt.Sprintf("The value of parameter properties.networkProfile.networkInterfaces is invalid: network interface %s requires a primary IP configuration with a subnet.",
				nics[0].ID),
		}
	}
	return nil
}

var azureVMProvisioningErrors sim.Store[AsyncOperationError]

func registerVirtualMachineOperationRecovery(srv *sim.Server) {
	azureVMProvisioningErrors = sim.MakeStore[AsyncOperationError](srv.DB(), "compute_virtual_machine_provisioning_errors")
	// The operation store fails an operation whose goroutine died with the
	// previous process; fail its machine too, or it would stay Creating or
	// Updating and refuse every later operation.
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
		azureVMStates.Put(vm.ID, "PowerState/stopped")
	}
}

// The provisioning states "States and billing status of Azure Virtual
// Machines" names for an operation in progress.
func azureVMOperationRunning(provisioningState string) bool {
	switch provisioningState {
	case "Creating", "Updating", "Deleting":
		return true
	}
	return false
}

// azureClaimVMOperation reports false when another operation still holds the
// machine.
func azureClaimVMOperation(id, transition string) bool {
	return azureClaimVMOperationAs(id, "Updating", transition)
}

// An empty transition leaves the power state alone.
func azureClaimVMOperationAs(id, provisioningState, transition string) bool {
	claimed := false
	azureVMs.Update(id, func(vm *VirtualMachine) {
		if azureVMOperationRunning(vm.Properties.ProvisioningState) {
			return
		}
		vm.Properties.ProvisioningState = provisioningState
		claimed = true
	})
	if claimed && transition != "" {
		azureVMStates.Put(id, transition)
	}
	return claimed
}

func azureVMOperationConflict(w http.ResponseWriter, operation, id string) {
	AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
		"Operation '%s' is not allowed on VM '%s' since another operation is in progress on it.",
		operation, id[strings.LastIndex(id, "/")+1:])
}

func azureRunVMOperation(id string, work func(ctx context.Context) *AsyncOperationError) string {
	azureVMProvisioningErrors.Delete(id)
	return startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		opErr := work(context.Background())
		azureSettleVMOperation(id, opErr)
		return opErr
	})
}

// azureRunVMWork is for a request Azure answers without an operation to poll;
// the provisioningState and instance view show the outcome.
func azureRunVMWork(id string, work func(ctx context.Context) *AsyncOperationError) {
	azureVMProvisioningErrors.Delete(id)
	bg.Go(func() { azureSettleVMOperation(id, work(context.Background())) })
}

// A machine the operation deleted has nothing to settle.
func azureSettleVMOperation(id string, opErr *AsyncOperationError) {
	settled := azureVMs.Update(id, func(vm *VirtualMachine) {
		vm.Properties.ProvisioningState = "Succeeded"
		if opErr != nil {
			vm.Properties.ProvisioningState = "Failed"
		}
	})
	if settled && opErr != nil {
		azureVMProvisioningErrors.Put(id, *opErr)
	}
}

// azureForgetVM detaches the machine's network interfaces, which outlive it.
func azureForgetVM(id string) {
	if vm, ok := azureVMs.Get(id); ok {
		syncManagedIdentityPrincipal(vm.Identity, nil, "", "")
		for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
			if azureNICs == nil {
				break
			}
			azureNICs.Update(ref.ID, func(nic *NetworkInterface) {
				if nic.Properties.VirtualMachine != nil && strings.EqualFold(nic.Properties.VirtualMachine.ID, id) {
					nic.Properties.VirtualMachine = nil
				}
			})
		}
	}
	azureVMs.Delete(id)
	azureVMStates.Delete(id)
	azureVMProvisioningErrors.Delete(id)
	azureVMGeneralized.Delete(id)
	azureForgetScheduledEvents(id)
	azureReconcileAutoRegisteredRecords()
}

// A networkProfile that stopped resolving keeps its request-validation code;
// Azure reports anything else as AllocationFailed.
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

func azureVMHaltFailure(id string, err error) *AsyncOperationError {
	return &AsyncOperationError{
		Code:    "InternalExecutionError",
		Message: fmt.Sprintf("An internal execution error occurred stopping virtual machine '%s': %v", id, err),
	}
}

// The Compute resource provider answers 202 with no body, the status URL as
// Azure-AsyncOperation and the monitor URL as Location.
func writeAzureVMOperationAccepted(w http.ResponseWriter, r *http.Request, vm VirtualMachine, opID string) {
	asyncOperation, monitor := computeOperationURLs(r, sim.PathParam(r, "subscriptionId"), vm.Location, opID)
	writeAzureAsyncCreateHeaders(w, opID, asyncOperation, monitor)
	w.WriteHeader(http.StatusAccepted)
}

func azureVMProvisioningStatus(vm VirtualMachine) VMStatus {
	switch vm.Properties.ProvisioningState {
	case "Creating":
		return VMStatus{Code: "ProvisioningState/creating", Level: "Info", DisplayStatus: "Creating"}
	case "Updating":
		return VMStatus{Code: "ProvisioningState/updating", Level: "Info", DisplayStatus: "Updating"}
	case "Deleting":
		return VMStatus{Code: "ProvisioningState/deleting", Level: "Info", DisplayStatus: "Deleting"}
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
