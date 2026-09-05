package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.Compute's operation catalog and its per-location usage.
//
// The catalog is the provider's own surface expressed as role-assignable
// actions: one entry per distinct "{provider}/{resource}/{operation}" action
// the operations in the vendored Swaggers require. Reads collapse onto one
// action — VirtualMachines_Get and VirtualMachines_List both need
// Microsoft.Compute/virtualMachines/read — which is why the catalog is shorter
// than the operation list. TestComputeOperationCatalogCoversSpec holds it to
// that derivation, so a re-vendor that adds an operation fails until the
// catalog names the action it needs.
//
// The usage is counted, not declared: every figure is the number of resources
// the simulator is actually holding in the location asked about, so creating a
// virtual machine moves it.

// computeProviderOperation is one action of the catalog.
type computeProviderOperation struct {
	Name      string
	Resource  string
	Operation string
}

// computeProviderName is the display spelling of the resource provider.
const computeProviderName = "Microsoft Compute"

// computeOperationCatalog is the Microsoft.Compute action catalog.
var computeOperationCatalog = []computeProviderOperation{
	{Name: "Microsoft.Compute/operations/read", Resource: "Operations", Operation: "List Compute Operations"},
	{Name: "Microsoft.Compute/skus/read", Resource: "Compute SKUs", Operation: "List Compute SKUs"},
	{Name: "Microsoft.Compute/locations/usages/read", Resource: "Location Usage", Operation: "List Compute Usage"},
	{Name: "Microsoft.Compute/locations/vmSizes/read", Resource: "Location Virtual Machine Size", Operation: "List Virtual Machine Sizes"},

	{Name: "Microsoft.Compute/virtualMachines/read", Resource: "Virtual Machine", Operation: "Get Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/write", Resource: "Virtual Machine", Operation: "Create or Update Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/delete", Resource: "Virtual Machine", Operation: "Delete Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/instanceView/read", Resource: "Virtual Machine", Operation: "Get Virtual Machine Instance View"},
	{Name: "Microsoft.Compute/virtualMachines/vmSizes/read", Resource: "Virtual Machine", Operation: "List Available Virtual Machine Sizes"},
	{Name: "Microsoft.Compute/virtualMachines/start/action", Resource: "Virtual Machine", Operation: "Start Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/powerOff/action", Resource: "Virtual Machine", Operation: "Power Off Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/restart/action", Resource: "Virtual Machine", Operation: "Restart Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/deallocate/action", Resource: "Virtual Machine", Operation: "Deallocate Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/generalize/action", Resource: "Virtual Machine", Operation: "Generalize Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/capture/action", Resource: "Virtual Machine", Operation: "Capture Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/convertToManagedDisks/action", Resource: "Virtual Machine", Operation: "Convert Virtual Machine To Managed Disks"},
	{Name: "Microsoft.Compute/virtualMachines/redeploy/action", Resource: "Virtual Machine", Operation: "Redeploy Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/reimage/action", Resource: "Virtual Machine", Operation: "Reimage Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/reapply/action", Resource: "Virtual Machine", Operation: "Reapply Virtual Machine State"},
	{Name: "Microsoft.Compute/virtualMachines/performMaintenance/action", Resource: "Virtual Machine", Operation: "Perform Maintenance On Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/simulateEviction/action", Resource: "Virtual Machine", Operation: "Simulate Eviction Of Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/assessPatches/action", Resource: "Virtual Machine", Operation: "Assess Patches On Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/installPatches/action", Resource: "Virtual Machine", Operation: "Install Patches On Virtual Machine"},
	{Name: "Microsoft.Compute/virtualMachines/retrieveBootDiagnosticsData/action", Resource: "Virtual Machine", Operation: "Retrieve Virtual Machine Boot Diagnostics Data"},

	{Name: "Microsoft.Compute/virtualMachines/extensions/read", Resource: "Virtual Machine Extension", Operation: "Get Virtual Machine Extension"},
	{Name: "Microsoft.Compute/virtualMachines/extensions/write", Resource: "Virtual Machine Extension", Operation: "Create or Update Virtual Machine Extension"},
	{Name: "Microsoft.Compute/virtualMachines/extensions/delete", Resource: "Virtual Machine Extension", Operation: "Delete Virtual Machine Extension"},
}

// registerComputeOperations mounts the catalog and the usage read.
func registerComputeOperations(srv *sim.Server) {
	// Operations_List.
	srv.HandleFunc("GET /providers/Microsoft.Compute/operations", func(w http.ResponseWriter, _ *http.Request) {
		value := make([]map[string]any, 0, len(computeOperationCatalog))
		for _, op := range computeOperationCatalog {
			value = append(value, map[string]any{
				"name": op.Name,
				// Every action of this provider is a control-plane action;
				// Microsoft.Compute's data plane is the machine itself, which
				// Azure Resource Manager does not gate.
				"origin": "user,system",
				"display": map[string]any{
					"provider":    computeProviderName,
					"resource":    op.Resource,
					"operation":   op.Operation,
					"description": op.Operation + ".",
				},
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
	})

	// Usage_List.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/usages",
		handleComputeUsagesByLocation)
}

// handleComputeUsagesByLocation counts what the subscription is holding in the
// location, so a virtual machine created there moves the figure. The limits are
// the subscription's own quotas, which is what a usage read compares against.
func handleComputeUsagesByLocation(w http.ResponseWriter, r *http.Request) {
	location := sim.PathParam(r, "location")

	machines, cores := 0, 0
	if azureVMs != nil {
		for _, vm := range azureVMs.List() {
			if !strings.EqualFold(vm.Location, location) {
				continue
			}
			machines++
			cores += computeVMCoreCount(vm)
		}
	}

	usage := func(name, localized, unit string, current, limit int) map[string]any {
		return map[string]any{
			"unit":         unit,
			"currentValue": current,
			"limit":        limit,
			"name":         map[string]any{"value": name, "localizedValue": localized},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []any{
			usage("virtualMachines", "Virtual Machines", "Count", machines, computeQuotaVirtualMachines),
			usage("cores", "Total Regional vCPUs", "Count", cores, computeQuotaCores),
		},
	})
}

// The subscription's Compute quotas in a location. They are the simulator's
// own, and a usage read is only meaningful against a limit, so they are named
// here rather than left at zero.
const (
	computeQuotaVirtualMachines = 25000
	computeQuotaCores           = 350
)

// computeVMCoreCount is how many cores a machine takes from the subscription's
// regional quota: the core count of the size it was created with, read from the
// same catalogue the vmSizes list serves, so the two can never disagree.
func computeVMCoreCount(vm VirtualMachine) int {
	size, _ := vm.Properties.HardwareProfile["vmSize"].(string)
	for _, known := range azureVMSizeCatalogue() {
		if name, _ := known["name"].(string); strings.EqualFold(name, size) {
			if cores, ok := known["numberOfCores"].(int); ok {
				return cores
			}
		}
	}
	// A machine on a size the catalogue does not carry still occupies the
	// subscription: counting it as nothing would under-report the quota.
	return 1
}
