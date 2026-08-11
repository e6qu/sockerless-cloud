package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Azure virtual machine extensions. An extension is not a record that something
// was installed — it is the guest agent running the handler's payload inside
// the machine, and the instance view afterwards is what that run produced. So
// the Custom Script extension's commandToExecute really executes in the guest,
// through the execution path realexec provides, and its output and exit status
// are what the extension reports.
//
// An extension whose handler this simulator does not implement is refused
// rather than reported as succeeded: a caller cannot tell a handler that did
// nothing from one that ran, and provisioning an extension that never ran is
// precisely the fiction these operations exist to avoid.

// VirtualMachineExtension is the ARM resource.
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
	// ProtectedSettings is accepted and never returned, which is what makes it
	// protected. It is stored so the handler can read it, exactly as the guest
	// agent does, and stripped from every response.
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

// customScriptHandlers are the extension handlers this simulator runs. The pair
// is (publisher, type) as Azure names them; the Custom Script extension is the
// one whose contract is "run this in the machine", which is the contract the
// execution path satisfies.
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
			sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
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
			sim.AzureErrorf(w, "VMExtensionHandlerNotFound", http.StatusBadRequest, "%v", err)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		var patch VirtualMachineExtension
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		azureMergeVMExtensionProperties(&existing.Properties, patch.Properties)
		if len(patch.Tags) > 0 {
			existing.Tags = patch.Tags
		}
		// A patch re-runs the handler, because changing what an extension is
		// supposed to do without doing it would leave the instance view
		// describing the previous run.
		view, err := azureRunVMExtension(r.Context(), vm, existing.Name, existing.Properties)
		if err != nil {
			sim.AzureErrorf(w, "VMExtensionHandlerNotFound", http.StatusBadRequest, "%v", err)
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
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
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
			// Azure answers a delete of an extension that is not there with 204,
			// the same as a delete that removed one.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func azureVMExtensionID(r *http.Request) string {
	return azureVMResourceID(r) + "/extensions/" + sim.PathParam(r, "vmExtensionName")
}

// azureVMExtensionResponse strips the members Azure never returns. Protected
// settings are write-only by contract — returning them would hand back a secret
// the caller entrusted to the machine.
func azureVMExtensionResponse(e VirtualMachineExtension, r *http.Request) VirtualMachineExtension {
	e.Properties.ProtectedSettings = nil
	// The instance view is returned on a read only when it was asked for, which
	// is how Azure keeps a list cheap.
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

// azureRunVMExtension executes the extension in the machine and reports what
// happened. The command comes from the handler's own settings — commandToExecute
// for the Custom Script extension, which is the member that contract defines —
// and it runs in the guest, so the status reflects a real execution.
func azureRunVMExtension(
	ctx context.Context,
	vm VirtualMachine,
	name string,
	properties VirtualMachineExtensionProperties,
) (*VirtualMachineExtensionInstanceView, error) {
	handler := properties.Publisher + "/" + properties.Type
	if !customScriptHandlers[handler] {
		return nil, fmt.Errorf(
			"extension handler %q is not one this simulator runs; it executes the Custom Script "+
				"handlers, whose contract is to run a command inside the machine", handler)
	}
	command := azureCustomScriptCommand(properties)
	if command == "" {
		return nil, fmt.Errorf(
			"extension %q carries no commandToExecute, so there is nothing for the handler to run", name)
	}

	guest, err := azureGuestFor(vm.ID)
	if err != nil {
		return nil, err
	}
	result, err := guest.Exec(ctx, "/bin/sh", "-c", command)
	view := &VirtualMachineExtensionInstanceView{
		Name:               name,
		Type:               properties.Type,
		TypeHandlerVersion: properties.TypeHandlerVersion,
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		return nil, fmt.Errorf("run extension %q in the machine: %w", name, err)
	}
	status := VMStatus{
		Code:          "ProvisioningState/succeeded",
		Level:         "Info",
		DisplayStatus: "Provisioning succeeded",
		Time:          now,
	}
	outcome := "Enable succeeded"
	if result.ExitCode != 0 {
		status = VMStatus{
			Code:          "ProvisioningState/failed",
			Level:         "Error",
			DisplayStatus: "Provisioning failed",
			Time:          now,
		}
		// The message says what happened. Reporting "Enable succeeded" on a
		// command that exited non-zero would contradict the status beside it.
		outcome = fmt.Sprintf("Enable failed: the command exited %d", result.ExitCode)
	}
	// The handler reports what the command wrote, which is what an operator
	// reads to find out what an extension actually did.
	status.Message = fmt.Sprintf("%s: \n[stdout]\n%s\n[stderr]\n%s",
		outcome, string(result.Stdout), string(result.Stderr))
	view.Statuses = []VMStatus{status}
	return view, nil
}

// azureCustomScriptCommand reads the command out of the handler's settings. The
// Custom Script extension accepts it in the public settings or the protected
// ones, and the protected copy wins because that is where a caller puts a
// command carrying a secret.
func azureCustomScriptCommand(properties VirtualMachineExtensionProperties) string {
	if command, ok := properties.ProtectedSettings["commandToExecute"].(string); ok && command != "" {
		return command
	}
	if command, ok := properties.Settings["commandToExecute"].(string); ok && command != "" {
		return command
	}
	return ""
}

// azureGuestFor resolves the running guest of a machine. An operation that runs
// something inside a machine needs the machine to be running, and saying so is
// the honest answer — Azure refuses the same way.
func azureGuestFor(vmID string) (*realexec.FirecrackerVM, error) {
	azureRealMu.Lock()
	guest := azureRealVMs[vmID]
	azureRealMu.Unlock()
	if guest == nil || !guest.Alive() {
		return nil, fmt.Errorf(
			"virtual machine %q is not running, so nothing can be executed inside it", vmID)
	}
	return guest, nil
}
