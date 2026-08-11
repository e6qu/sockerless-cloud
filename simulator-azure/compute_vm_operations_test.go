package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// These drive the virtual-machine operations in-process against a machine put
// straight into the store. Everything that does not depend on a running guest
// is asserted here — routing, the state each operation refuses or produces, the
// disk conversion, and where boot diagnostics land — because booting a guest
// needs nested KVM and a host without it would otherwise leave this logic
// untested rather than merely unbooted. The guest-moving half is covered by the
// official-SDK suite on a capable host.

const (
	vmOpsSubscription  = "00000000-0000-0000-0000-000000000001"
	vmOpsResourceGroup = "vm-ops-unit-rg"
)

func vmOpsSimulator(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	return srv
}

func vmOpsID(name string) string {
	return "/subscriptions/" + vmOpsSubscription + "/resourceGroups/" + vmOpsResourceGroup +
		"/providers/Microsoft.Compute/virtualMachines/" + name
}

// putVMOpsMachine stores a machine without booting a guest, which is what lets
// the operations be exercised on a host that cannot boot one.
func putVMOpsMachine(t *testing.T, name, location string, mutate func(*VirtualMachine)) VirtualMachine {
	t.Helper()
	vm := VirtualMachine{
		ID:       vmOpsID(name),
		Name:     name,
		Type:     "Microsoft.Compute/virtualMachines",
		Location: location,
		Properties: VMProperties{
			ProvisioningState: "Succeeded",
			StorageProfile: map[string]any{
				"osDisk": map[string]any{
					"name":         name + "-osdisk",
					"createOption": "FromImage",
					"managedDisk":  map[string]any{"storageAccountType": "Standard_LRS"},
				},
			},
		},
	}
	if mutate != nil {
		mutate(&vm)
	}
	azureVMs.Put(vm.ID, vm)
	azureVMStates.Put(vm.ID, "PowerState/stopped")
	t.Cleanup(func() {
		azureVMs.Delete(vm.ID)
		azureVMStates.Delete(vm.ID)
		azureVMGeneralized.Delete(vm.ID)
	})
	return vm
}

func vmOpsRequest(t *testing.T, srv *sim.Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	// The ARM plane requires a valid bearer; mint one the way a client acquires
	// it from the token endpoint so the request reaches the handler under test.
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}
	req := httptest.NewRequest(method, path+"?api-version=2022-03-01", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func vmOpsPath(name, action string) string {
	path := vmOpsID(name)
	if action != "" {
		path += "/" + action
	}
	return path
}

func TestVirtualMachineListAvailableSizes(t *testing.T) {
	srv := vmOpsSimulator(t)
	putVMOpsMachine(t, "sizes-vm", "eastus", nil)

	rec := vmOpsRequest(t, srv, http.MethodGet, vmOpsPath("sizes-vm", "vmSizes"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Value []struct {
			Name          string `json:"name"`
			NumberOfCores int    `json:"numberOfCores"`
			MemoryInMB    int    `json:"memoryInMB"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Value) == 0 {
		t.Fatal("a machine offers no size to resize to")
	}
	for _, size := range body.Value {
		if size.Name == "" || size.NumberOfCores == 0 || size.MemoryInMB == 0 {
			t.Fatalf("size %+v is missing the members that describe it", size)
		}
	}

	// A machine that does not exist has no sizes, rather than the catalogue.
	rec = vmOpsRequest(t, srv, http.MethodGet, vmOpsPath("absent-vm", "vmSizes"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status for an absent machine = %d, want 404", rec.Code)
	}
}

func TestVirtualMachineListByLocation(t *testing.T) {
	srv := vmOpsSimulator(t)
	putVMOpsMachine(t, "east-vm", "eastus", nil)
	putVMOpsMachine(t, "west-vm", "westeurope", nil)

	names := func(location string) []string {
		rec := vmOpsRequest(t, srv, http.MethodGet,
			"/subscriptions/"+vmOpsSubscription+"/providers/Microsoft.Compute/locations/"+location+"/virtualMachines")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Value []struct {
				Name string `json:"name"`
			} `json:"value"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var out []string
		for _, vm := range body.Value {
			out = append(out, vm.Name)
		}
		return out
	}

	east := names("eastus")
	if !listsMachine(east, "east-vm") {
		t.Errorf("eastus listing %v omits the machine in eastus", east)
	}
	if listsMachine(east, "west-vm") {
		t.Errorf("eastus listing %v includes a machine in westeurope", east)
	}
	if west := names("westeurope"); !listsMachine(west, "west-vm") || listsMachine(west, "east-vm") {
		t.Errorf("westeurope listing = %v, want only the westeurope machine", west)
	}
}

// Generalizing is refused while the machine runs, because generalizing a
// running system would capture it mid-write.
func TestVirtualMachineGeneralize(t *testing.T) {
	srv := vmOpsSimulator(t)
	vm := putVMOpsMachine(t, "generalize-vm", "eastus", nil)

	// The refusal for a RUNNING machine needs a live guest, which needs nested
	// KVM; the official-SDK suite covers it on a capable host. What is asserted
	// here is the path that does not: a stopped machine generalizes, and the
	// state is readable back where a caller looks for it.
	azureVMStates.Put(vm.ID, "PowerState/stopped")
	rec := vmOpsRequest(t, srv, http.MethodPost, vmOpsPath("generalize-vm", "generalize"))
	if rec.Code != http.StatusOK {
		t.Fatalf("generalizing a stopped machine: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if generalized, _ := azureVMGeneralized.Get(vm.ID); !generalized {
		t.Fatal("the machine was not recorded as generalized")
	}

	// The instance view is where a caller reads it back.
	stored, _ := azureVMs.Get(vm.ID)
	var codes []string
	for _, status := range virtualMachineWithInstanceView(stored).Properties.InstanceView.Statuses {
		codes = append(codes, status.Code)
	}
	if !listsMachine(codes, "OSState/generalized") {
		t.Fatalf("instance view statuses %v omit the generalized state", codes)
	}
}

// SimulateEviction applies only to Spot machines, and the eviction policy
// decides what the eviction leaves behind.
func TestVirtualMachineSimulateEviction(t *testing.T) {
	srv := vmOpsSimulator(t)

	putVMOpsMachine(t, "regular-vm", "eastus", nil)
	rec := vmOpsRequest(t, srv, http.MethodPost, vmOpsPath("regular-vm", "simulateEviction"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("evicting a regular machine: status = %d, want 409: %s", rec.Code, rec.Body.String())
	}

	spot := putVMOpsMachine(t, "spot-vm", "eastus", func(vm *VirtualMachine) {
		vm.Properties.Priority = "Spot"
		vm.Properties.EvictionPolicy = "Deallocate"
	})
	rec = vmOpsRequest(t, srv, http.MethodPost, vmOpsPath("spot-vm", "simulateEviction"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("evicting a Spot machine: status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if state, _ := azureVMStates.Get(spot.ID); state != "PowerState/deallocated" {
		t.Fatalf("an evicted Spot machine is in %q, want PowerState/deallocated", state)
	}

	// A Delete policy removes the machine, which is what Azure does.
	deleted := putVMOpsMachine(t, "spot-delete-vm", "eastus", func(vm *VirtualMachine) {
		vm.Properties.Priority = "Spot"
		vm.Properties.EvictionPolicy = "Delete"
	})
	rec = vmOpsRequest(t, srv, http.MethodPost, vmOpsPath("spot-delete-vm", "simulateEviction"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if _, still := azureVMs.Get(deleted.ID); still {
		t.Fatal("a Spot machine evicted under a Delete policy was left behind")
	}
}

// The conversion rewrites unmanaged disks to managed ones and leaves a machine
// already on managed disks alone.
func TestVirtualMachineConvertToManagedDisks(t *testing.T) {
	srv := vmOpsSimulator(t)
	vm := putVMOpsMachine(t, "unmanaged-vm", "eastus", func(vm *VirtualMachine) {
		vm.Properties.StorageProfile = map[string]any{
			"osDisk": map[string]any{
				"name": "unmanaged-osdisk",
				"vhd":  map[string]any{"uri": "https://acct.blob.core.windows.net/vhds/os.vhd"},
			},
			"dataDisks": []any{
				map[string]any{"lun": 0, "name": "data0",
					"vhd": map[string]any{"uri": "https://acct.blob.core.windows.net/vhds/data0.vhd"}},
			},
		}
	})

	rec := vmOpsRequest(t, srv, http.MethodPost, vmOpsPath("unmanaged-vm", "convertToManagedDisks"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	stored, _ := azureVMs.Get(vm.ID)
	osDisk, _ := stored.Properties.StorageProfile["osDisk"].(map[string]any)
	if _, unmanaged := osDisk["vhd"]; unmanaged {
		t.Error("the OS disk still references a vhd after conversion")
	}
	if _, managed := osDisk["managedDisk"]; !managed {
		t.Error("the OS disk gained no managed-disk reference")
	}
	dataDisks, _ := stored.Properties.StorageProfile["dataDisks"].([]any)
	if len(dataDisks) != 1 {
		t.Fatalf("data disks = %d, want 1", len(dataDisks))
	}
	data, _ := dataDisks[0].(map[string]any)
	if _, unmanaged := data["vhd"]; unmanaged {
		t.Error("a data disk still references a vhd after conversion")
	}
}

func TestVirtualMachineConvertToManagedDisksLeavesManagedDisksAlone(t *testing.T) {
	vm := VirtualMachine{Properties: VMProperties{StorageProfile: map[string]any{
		"osDisk": map[string]any{"managedDisk": map[string]any{"storageAccountType": "Premium_LRS"}},
	}}}
	if azureConvertVMDisksToManaged(&vm) {
		t.Fatal("a machine already on managed disks was reported as converted")
	}
	osDisk, _ := vm.Properties.StorageProfile["osDisk"].(map[string]any)
	managed, _ := osDisk["managedDisk"].(map[string]any)
	if managed["storageAccountType"] != "Premium_LRS" {
		t.Fatalf("the existing managed-disk reference was rewritten: %+v", managed)
	}
}

// Boot diagnostics resolves the storage account the machine names, and refuses
// clearly when there is none to resolve.
func TestVirtualMachineBootDiagnosticsAccountResolution(t *testing.T) {
	for _, tc := range []struct {
		name        string
		diagnostics map[string]any
		want        string
		wantErr     string
	}{
		{
			"an account named by storageUri",
			map[string]any{"bootDiagnostics": map[string]any{
				"enabled": true, "storageUri": "https://simdiag.blob.core.windows.net/"}},
			"simdiag", "",
		},
		{
			"no diagnostics profile at all",
			nil, "", "not enabled",
		},
		{
			"boot diagnostics explicitly disabled",
			map[string]any{"bootDiagnostics": map[string]any{"enabled": false}},
			"", "not enabled",
		},
		{
			"managed boot diagnostics names no account",
			map[string]any{"bootDiagnostics": map[string]any{"enabled": true}},
			"", "storageUri",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm := VirtualMachine{ID: vmOpsID("diag"), Properties: VMProperties{DiagnosticsProfile: tc.diagnostics}}
			got, err := azureBootDiagnosticsAccount(vm)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("account = %q, want a refusal mentioning %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("refusal %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if got != tc.want {
				t.Fatalf("account = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVirtualMachineBootDiagnosticsPath(t *testing.T) {
	container, blob := azureBootDiagnosticsPath(VirtualMachine{Name: "Web-01"})
	if container != "bootdiagnostics-web-01" {
		t.Errorf("container = %q, want the per-machine boot-diagnostics container", container)
	}
	if !strings.HasSuffix(blob, ".serialconsole.log") {
		t.Errorf("blob = %q, want the serial console log", blob)
	}
}

// A machine with no running guest has produced no console output, and the
// refusal says so rather than returning an empty log that a caller could not
// distinguish from a guest that booted silently.
func TestVirtualMachineBootDiagnosticsRefusesWithoutAGuest(t *testing.T) {
	if _, err := azureGuestConsoleOutput(vmOpsID("no-guest")); err == nil {
		t.Fatal("console output was produced for a machine with no guest")
	}
}

// listsMachine reports whether a listing contains an exact entry. The package's
// own `contains` tests substring containment, which would match a name that
// merely shares a prefix.
func listsMachine(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
