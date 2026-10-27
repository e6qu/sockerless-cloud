package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Azure virtual machine extensions: the Custom Script extension's
// commandToExecute runs in the guest through azureGuestExec, and its output and
// exit status are what the extension reports. A handler this simulator does not
// implement is refused rather than reported as succeeded.

type VirtualMachineExtension struct {
	ID         string                            `json:"id,omitempty"`
	Name       string                            `json:"name,omitempty"`
	Type       string                            `json:"type,omitempty"`
	Location   string                            `json:"location,omitempty"`
	Tags       map[string]string                 `json:"tags,omitempty"`
	Properties VirtualMachineExtensionProperties `json:"properties"`
}

type VirtualMachineExtensionProperties struct {
	ForceUpdateTag          string                               `json:"forceUpdateTag,omitempty"`
	Publisher               string                               `json:"publisher,omitempty"`
	Type                    string                               `json:"type,omitempty"`
	TypeHandlerVersion      string                               `json:"typeHandlerVersion,omitempty"`
	AutoUpgradeMinorVersion *bool                                `json:"autoUpgradeMinorVersion,omitempty"`
	EnableAutomaticUpgrade  *bool                                `json:"enableAutomaticUpgrade,omitempty"`
	Settings                map[string]any                       `json:"settings,omitempty"`
	SuppressFailures        *bool                                `json:"suppressFailures,omitempty"`
	ProvisioningState       string                               `json:"provisioningState,omitempty"`
	InstanceView            *VirtualMachineExtensionInstanceView `json:"instanceView,omitempty"`
	// Keep ProtectedSettings for the handler, as the guest agent does, and strip
	// it from every response.
	ProtectedSettings map[string]any `json:"protectedSettings,omitempty"`
}

type VirtualMachineExtensionInstanceView struct {
	Name               string     `json:"name,omitempty"`
	Type               string     `json:"type,omitempty"`
	TypeHandlerVersion string     `json:"typeHandlerVersion,omitempty"`
	Statuses           []VMStatus `json:"statuses,omitempty"`
	Substatuses        []VMStatus `json:"substatuses,omitempty"`
}

var azureVMExtensions sim.Store[VirtualMachineExtension]

// The pair is (publisher, type) as Azure names them.
var customScriptHandlers = map[string]bool{
	"Microsoft.Azure.Extensions/CustomScript":       true,
	"Microsoft.OSTCExtensions/CustomScriptForLinux": true,
	"Microsoft.Compute/CustomScriptExtension":       true,
}

func registerVirtualMachineExtensions(srv *sim.Server) {
	azureVMExtensions = sim.MakeStore[VirtualMachineExtension](srv.DB(), "compute_vm_extensions")
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute"
	const extPath = armBase + "/virtualMachines/{vmName}/extensions/{vmExtensionName}"

	srv.HandleFunc("PUT "+extPath, func(w http.ResponseWriter, r *http.Request) {
		vm, _, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		var body VirtualMachineExtension
		if err := sim.ReadJSON(r, &body); err != nil {
			AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		name := sim.PathParam(r, "vmExtensionName")
		body.ID = azureVMExtensionID(r)
		body.Name = name
		body.Type = "Microsoft.Compute/virtualMachines/extensions"
		if body.Location == "" {
			body.Location = vm.Location
		}

		view, err := azureRunVMExtension(r.Context(), vm, name, body.Properties)
		if err != nil {
			azureWriteVMExtensionError(w, name, err)
			return
		}
		body.Properties.InstanceView = view
		body.Properties.ProvisioningState = "Succeeded"
		if azureVMExtensionFailed(view) {
			body.Properties.ProvisioningState = "Failed"
		}
		azureVMExtensions.Put(body.ID, body)
		sim.WriteJSON(w, http.StatusOK, azureVMExtensionResponse(body, r))
	})

	srv.HandleFunc("PATCH "+extPath, func(w http.ResponseWriter, r *http.Request) {
		vm, _, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		id := azureVMExtensionID(r)
		existing, found := azureVMExtensions.Get(id)
		if !found {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		var patch VirtualMachineExtension
		if err := sim.ReadJSON(r, &patch); err != nil {
			AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		azureMergeVMExtensionProperties(&existing.Properties, patch.Properties)
		if len(patch.Tags) > 0 {
			existing.Tags = patch.Tags
		}
		// A patch re-runs the handler, so the instance view describes the new
		// settings.
		view, err := azureRunVMExtension(r.Context(), vm, existing.Name, existing.Properties)
		if err != nil {
			azureWriteVMExtensionError(w, existing.Name, err)
			return
		}
		existing.Properties.InstanceView = view
		existing.Properties.ProvisioningState = "Succeeded"
		if azureVMExtensionFailed(view) {
			existing.Properties.ProvisioningState = "Failed"
		}
		azureVMExtensions.Put(id, existing)
		sim.WriteJSON(w, http.StatusOK, azureVMExtensionResponse(existing, r))
	})

	srv.HandleFunc("GET "+extPath, func(w http.ResponseWriter, r *http.Request) {
		id := azureVMExtensionID(r)
		extension, ok := azureVMExtensions.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, azureVMExtensionResponse(extension, r))
	})

	srv.HandleFunc("GET "+armBase+"/virtualMachines/{vmName}/extensions", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := azureLookupVM(w, r); !ok {
			return
		}
		prefix := azureVMResourceID(r) + "/extensions/"
		items := azureVMExtensions.Filter(func(e VirtualMachineExtension) bool {
			return strings.HasPrefix(e.ID, prefix)
		})
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		out := make([]VirtualMachineExtension, 0, len(items))
		for _, item := range items {
			out = append(out, azureVMExtensionResponse(item, r))
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
	})

	srv.HandleFunc("DELETE "+extPath, func(w http.ResponseWriter, r *http.Request) {
		id := azureVMExtensionID(r)
		if !azureVMExtensions.Delete(id) {
			// Azure answers a delete of an absent extension with 204.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Azure refuses a handler that cannot run at all as VMExtensionHandlerNotFound;
// any other failure is VMExtensionProvisioningError.
type azureVMExtensionHandlerError struct{ message string }

func (e *azureVMExtensionHandlerError) Error() string { return e.message }

func azureWriteVMExtensionError(w http.ResponseWriter, name string, err error) {
	var handler *azureVMExtensionHandlerError
	if errors.As(err, &handler) {
		AzureErrorf(w, "VMExtensionHandlerNotFound", http.StatusBadRequest, "%v", err)
		return
	}
	AzureErrorf(w, "VMExtensionProvisioningError", http.StatusConflict,
		"VM has reported a failure when processing extension '%s'. Error message: %v", name, err)
}

func azureVMExtensionID(r *http.Request) string {
	return armChildID(azureVMResourceID(r), "extensions", sim.PathParam(r, "vmExtensionName"))
}

// Protected settings are write-only by contract.
func azureVMExtensionResponse(e VirtualMachineExtension, r *http.Request) VirtualMachineExtension {
	e.Properties.ProtectedSettings = nil
	// Azure returns the instance view on a read only when $expand asks for it.
	if r.Method == http.MethodGet && !strings.EqualFold(r.URL.Query().Get("$expand"), "instanceView") {
		e.Properties.InstanceView = nil
	}
	return e
}

func azureMergeVMExtensionProperties(into *VirtualMachineExtensionProperties, patch VirtualMachineExtensionProperties) {
	if patch.ForceUpdateTag != "" {
		into.ForceUpdateTag = patch.ForceUpdateTag
	}
	if patch.Publisher != "" {
		into.Publisher = patch.Publisher
	}
	if patch.Type != "" {
		into.Type = patch.Type
	}
	if patch.TypeHandlerVersion != "" {
		into.TypeHandlerVersion = patch.TypeHandlerVersion
	}
	if patch.AutoUpgradeMinorVersion != nil {
		into.AutoUpgradeMinorVersion = patch.AutoUpgradeMinorVersion
	}
	if patch.EnableAutomaticUpgrade != nil {
		into.EnableAutomaticUpgrade = patch.EnableAutomaticUpgrade
	}
	if patch.SuppressFailures != nil {
		into.SuppressFailures = patch.SuppressFailures
	}
	if patch.Settings != nil {
		into.Settings = patch.Settings
	}
	if patch.ProtectedSettings != nil {
		into.ProtectedSettings = patch.ProtectedSettings
	}
}

func azureVMExtensionFailed(view *VirtualMachineExtensionInstanceView) bool {
	if view == nil {
		return false
	}
	for _, status := range view.Statuses {
		if strings.EqualFold(status.Level, "Error") {
			return true
		}
	}
	return false
}

func azureRunVMExtension(
	ctx context.Context,
	vm VirtualMachine,
	name string,
	properties VirtualMachineExtensionProperties,
) (*VirtualMachineExtensionInstanceView, error) {
	handler := properties.Publisher + "/" + properties.Type
	if !customScriptHandlers[handler] {
		return nil, &azureVMExtensionHandlerError{message: fmt.Sprintf(
			"extension handler %q is not one this simulator runs; it executes the Custom Script "+
				"handlers, whose contract is to run a command inside the machine", handler)}
	}
	command := azureCustomScriptCommand(properties)
	if command == "" {
		return nil, &azureVMExtensionHandlerError{message: fmt.Sprintf(
			"extension %q carries no commandToExecute, so there is nothing for the handler to run", name)}
	}

	stdout, stderr, exitCode, err := azureGuestExec(ctx, vm.ID, command)
	if err != nil {
		return nil, fmt.Errorf("run extension %q in the machine: %w", name, err)
	}
	view := &VirtualMachineExtensionInstanceView{
		Name:               name,
		Type:               properties.Type,
		TypeHandlerVersion: properties.TypeHandlerVersion,
	}
	now := time.Now().UTC().Format(time.RFC3339)
	status := VMStatus{
		Code:          "ProvisioningState/succeeded",
		Level:         "Info",
		DisplayStatus: "Provisioning succeeded",
		Time:          now,
	}
	outcome := "Enable succeeded"
	if exitCode != 0 {
		status = VMStatus{
			Code:          "ProvisioningState/failed",
			Level:         "Error",
			DisplayStatus: "Provisioning failed",
			Time:          now,
		}
		outcome = fmt.Sprintf("Enable failed: the command exited %d", exitCode)
	}
	status.Message = fmt.Sprintf("%s: \n[stdout]\n%s\n[stderr]\n%s",
		outcome, string(stdout), string(stderr))
	view.Statuses = []VMStatus{status}
	return view, nil
}

// The Custom Script extension accepts the command in public or protected
// settings; the protected copy wins, as that is where a command carrying a
// secret goes.
func azureCustomScriptCommand(properties VirtualMachineExtensionProperties) string {
	if command, ok := properties.ProtectedSettings["commandToExecute"].(string); ok && command != "" {
		return command
	}
	if command, ok := properties.Settings["commandToExecute"].(string); ok && command != "" {
		return command
	}
	return ""
}
