package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// These drive the virtual-machine long-running operations in-process. The
// guest moves are held open on channels through the azureBootVM, azureHaltVM
// and azureDestroyVM hooks, which is what lets a test read the machine and its
// operation while the move is still under way, on a host without nested KVM.

// vmGuestHooks replaces the guest moves with ones that announce themselves and
// wait for the test to decide their outcome.
type vmGuestHooks struct {
	booting    chan string
	boot       chan error
	halting    chan string
	halt       chan error
	destroying chan string
	destroy    chan error
}

func installVMGuestHooks(t *testing.T) *vmGuestHooks {
	t.Helper()
	hooks := &vmGuestHooks{
		booting: make(chan string, 1),
		boot:    make(chan error),
		halting: make(chan string, 1),
		halt:    make(chan error),

		destroying: make(chan string, 1),
		destroy:    make(chan error),
	}
	boot, halt, destroy := azureBootVM, azureHaltVM, azureDestroyVM
	azureBootVM = func(_ context.Context, vm VirtualMachine) error {
		hooks.booting <- vm.ID
		return <-hooks.boot
	}
	azureHaltVM = func(_ context.Context, id string) error {
		hooks.halting <- id
		return <-hooks.halt
	}
	azureDestroyVM = func(_ context.Context, vm VirtualMachine) error {
		hooks.destroying <- vm.ID
		return <-hooks.destroy
	}
	// Registered before vmOpsSimulator's drain, so it runs after the drain.
	t.Cleanup(func() { azureBootVM, azureHaltVM, azureDestroyVM = boot, halt, destroy })
	return hooks
}

func (h *vmGuestHooks) awaitBoot(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-h.booting:
		if got != id {
			t.Fatalf("booted %q, want %q", got, id)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the operation never began booting %q", id)
	}
}

func (h *vmGuestHooks) awaitHalt(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-h.halting:
		if got != id {
			t.Fatalf("halted %q, want %q", got, id)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the operation never began halting %q", id)
	}
}

func (h *vmGuestHooks) awaitDestroy(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-h.destroying:
		if got != id {
			t.Fatalf("destroyed %q, want %q", got, id)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the operation never began deleting %q", id)
	}
}

func vmLROSimulator(t *testing.T) (*sim.Server, *vmGuestHooks) {
	t.Helper()
	hooks := installVMGuestHooks(t)
	return vmOpsSimulator(t), hooks
}

// vmLRORequest sends one ARM request to the simulator. rawURL is a path, or an
// absolute poll URL the simulator advertised.
func vmLRORequest(t *testing.T, srv *sim.Server, method, rawURL, body string) *httptest.ResponseRecorder {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	query := parsed.Query()
	if query.Get("api-version") == "" {
		query.Set("api-version", "2022-03-01")
	}
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}
	req := httptest.NewRequest(method, parsed.Path+"?"+query.Encode(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func vmLROOperationStatus(t *testing.T, srv *sim.Server, opURL string) AsyncOperationStatus {
	t.Helper()
	rec := vmLRORequest(t, srv, http.MethodGet, opURL, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("operation status: %d: %s", rec.Code, rec.Body.String())
	}
	var op AsyncOperationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
		t.Fatalf("decode operation status: %v", err)
	}
	return op
}

func vmLROInstanceView(t *testing.T, srv *sim.Server, name string) (provisioning, power VMStatus) {
	t.Helper()
	rec := vmLRORequest(t, srv, http.MethodGet, vmOpsPath(name, "instanceView"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("instance view: %d: %s", rec.Code, rec.Body.String())
	}
	var view VMInstanceView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode instance view: %v", err)
	}
	for _, status := range view.Statuses {
		switch {
		case strings.HasPrefix(status.Code, "ProvisioningState/"):
			provisioning = status
		case strings.HasPrefix(status.Code, "PowerState/"):
			power = status
		}
	}
	return provisioning, power
}

// requireVMLROPowerState reads the power state the operation settled on from
// the store: the instance view reconciles a running record against a live
// guest, and the hooked boot starts none.
func requireVMLROPowerState(t *testing.T, id, want string) {
	t.Helper()
	if got, _ := azureVMStates.Get(id); got != want {
		t.Fatalf("power state = %q, want %q", got, want)
	}
}

func vmLROProvisioningState(t *testing.T, srv *sim.Server, name string) string {
	t.Helper()
	rec := vmLRORequest(t, srv, http.MethodGet, vmOpsPath(name, ""), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get machine: %d: %s", rec.Code, rec.Body.String())
	}
	var vm VirtualMachine
	if err := json.Unmarshal(rec.Body.Bytes(), &vm); err != nil {
		t.Fatalf("decode machine: %v", err)
	}
	return vm.Properties.ProvisioningState
}

// putVMLRONetworkInterface stores the network interface a create names, so the
// create passes request validation.
func putVMLRONetworkInterface(t *testing.T, name string) string {
	t.Helper()
	id := "/subscriptions/" + vmOpsSubscription + "/resourceGroups/" + vmOpsResourceGroup +
		"/providers/Microsoft.Network/networkInterfaces/" + name
	azureNICs.Put(id, NetworkInterface{
		ID:   id,
		Name: name,
		Type: "Microsoft.Network/networkInterfaces",
		Properties: NetworkInterfaceProperties{
			IPConfigurations: []NetworkInterfaceIPConfiguration{{
				Name: "ipconfig1",
				Properties: NetworkInterfaceIPConfigurationProperties{
					Subnet: &SubResource{ID: "/subscriptions/" + vmOpsSubscription + "/resourceGroups/" + vmOpsResourceGroup +
						"/providers/Microsoft.Network/virtualNetworks/vnet/subnets/default"},
				},
			}},
		},
	})
	t.Cleanup(func() { azureNICs.Delete(id) })
	return id
}

func createVMLROMachine(t *testing.T, srv *sim.Server, name string) *httptest.ResponseRecorder {
	t.Helper()
	nicID := putVMLRONetworkInterface(t, name+"-nic")
	t.Cleanup(func() {
		azureVMs.Delete(vmOpsID(name))
		azureVMStates.Delete(vmOpsID(name))
		azureVMProvisioningErrors.Delete(vmOpsID(name))
	})
	return vmLRORequest(t, srv, http.MethodPut, vmOpsPath(name, ""),
		`{"location":"eastus","properties":{"hardwareProfile":{"vmSize":"Standard_B1s"},`+
			`"networkProfile":{"networkInterfaces":[{"id":"`+nicID+`"}]}}}`)
}

// A create answers 201 at once with the machine Creating and starting, and the
// operation it advertises stays InProgress until the guest has booted.
func TestVirtualMachineCreateAnswersBeforeTheBoot(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	id := vmOpsID("lro-create-vm")

	rec := createVMLROMachine(t, srv, "lro-create-vm")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created VirtualMachine
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Properties.ProvisioningState != "Creating" {
		t.Fatalf("create answered provisioningState %q, want Creating", created.Properties.ProvisioningState)
	}
	opURL := rec.Header().Get("Azure-AsyncOperation")
	if opURL == "" {
		t.Fatal("create answered without an Azure-AsyncOperation")
	}
	if rec.Header().Get("Location") != "" {
		t.Error("the Compute resource provider answers a create with Azure-AsyncOperation alone")
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q on a create still booting, want 1", got)
	}

	hooks.awaitBoot(t, id)
	if op := vmLROOperationStatus(t, srv, opURL); op.Status != "InProgress" {
		t.Fatalf("operation reads %q while the guest boots, want InProgress", op.Status)
	}
	provisioning, power := vmLROInstanceView(t, srv, "lro-create-vm")
	if provisioning.Code != "ProvisioningState/creating" || power.Code != "PowerState/starting" {
		t.Fatalf("instance view while booting = %q / %q, want ProvisioningState/creating / PowerState/starting",
			provisioning.Code, power.Code)
	}

	hooks.boot <- nil
	bg.Await()

	if op := vmLROOperationStatus(t, srv, opURL); op.Status != "Succeeded" {
		t.Fatalf("operation reads %q after the boot, want Succeeded", op.Status)
	}
	requireVMLROPowerState(t, id, "PowerState/running")
	if state := vmLROProvisioningState(t, srv, "lro-create-vm"); state != "Succeeded" {
		t.Fatalf("provisioningState after the boot = %q, want Succeeded", state)
	}
	if provisioning, _ := vmLROInstanceView(t, srv, "lro-create-vm"); provisioning.Code != "ProvisioningState/succeeded" {
		t.Fatalf("provisioning status after the boot = %q", provisioning.Code)
	}
}

// A boot that fails fails the operation with the error ARM reports, and the
// machine reads Failed with that error in its instance view.
func TestVirtualMachineCreateFailsWhenTheBootFails(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	id := vmOpsID("lro-fail-vm")

	rec := createVMLROMachine(t, srv, "lro-fail-vm")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want 201: %s", rec.Code, rec.Body.String())
	}
	opURL := rec.Header().Get("Azure-AsyncOperation")
	hooks.awaitBoot(t, id)
	hooks.boot <- errors.New("kvm:/dev/kvm missing")
	bg.Await()

	op := vmLROOperationStatus(t, srv, opURL)
	if op.Status != "Failed" || op.Error == nil || op.Error.Code != "AllocationFailed" {
		t.Fatalf("operation after a failed boot = %+v, want Failed with AllocationFailed", op)
	}
	if !strings.Contains(op.Error.Message, "kvm:/dev/kvm missing") {
		t.Errorf("the operation's error hides why the boot failed: %q", op.Error.Message)
	}
	if state := vmLROProvisioningState(t, srv, "lro-fail-vm"); state != "Failed" {
		t.Fatalf("provisioningState after a failed boot = %q, want Failed", state)
	}
	provisioning, power := vmLROInstanceView(t, srv, "lro-fail-vm")
	if provisioning.Code != "ProvisioningState/failed/AllocationFailed" || provisioning.Level != "Error" {
		t.Fatalf("instance view provisioning status = %+v", provisioning)
	}
	if power.Code != "PowerState/deallocated" {
		t.Fatalf("a machine that never got a host reads %q, want PowerState/deallocated", power.Code)
	}
}

// Start answers 202 at once; the machine reads Updating and starting while the
// guest boots, and a second operation is refused until the first settles.
func TestVirtualMachineStartRunsBehindAnOperation(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-start-vm", "eastus", nil)

	rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-start-vm", "start"), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 202 start carries no body, got %s", rec.Body.String())
	}
	opURL := rec.Header().Get("Azure-AsyncOperation")
	monitorURL := rec.Header().Get("Location")
	if opURL == "" || monitorURL == "" {
		t.Fatalf("start answered without both poll URLs: %v", rec.Header())
	}
	requireComputeOperationURL(t, opURL, false)
	requireComputeOperationURL(t, monitorURL, true)

	hooks.awaitBoot(t, vm.ID)
	if monitor := vmLRORequest(t, srv, http.MethodGet, monitorURL, ""); monitor.Code != http.StatusAccepted || monitor.Body.Len() != 0 {
		t.Fatalf("monitor poll while starting: status %d body %q, want 202 with no body", monitor.Code, monitor.Body.String())
	}
	provisioning, power := vmLROInstanceView(t, srv, "lro-start-vm")
	if provisioning.Code != "ProvisioningState/updating" || power.Code != "PowerState/starting" {
		t.Fatalf("instance view while starting = %q / %q", provisioning.Code, power.Code)
	}
	conflict := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-start-vm", "powerOff"), "")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("power-off during a start: status %d, want 409: %s", conflict.Code, conflict.Body.String())
	}
	if del := vmLRORequest(t, srv, http.MethodDelete, vmOpsPath("lro-start-vm", ""), ""); del.Code != http.StatusConflict {
		t.Fatalf("delete during a start: status %d, want 409: %s", del.Code, del.Body.String())
	}

	hooks.boot <- nil
	bg.Await()

	if op := vmLROOperationStatus(t, srv, opURL); op.Status != "Succeeded" || op.ID != "" {
		t.Fatalf("start operation reads %+v, want Succeeded with no id, as the Compute envelope has none", op)
	}
	if monitor := vmLRORequest(t, srv, http.MethodGet, monitorURL, ""); monitor.Code != http.StatusOK {
		t.Fatalf("monitor poll after the start: status %d, want 200: %s", monitor.Code, monitor.Body.String())
	}
	requireVMLROPowerState(t, vm.ID, "PowerState/running")
	if provisioning, _ := vmLROInstanceView(t, srv, "lro-start-vm"); provisioning.Code != "ProvisioningState/succeeded" {
		t.Fatalf("provisioning status after start = %q", provisioning.Code)
	}
}

// Deallocate reads deallocating until the guest has stopped, then deallocated.
func TestVirtualMachineDeallocateRunsBehindAnOperation(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-deallocate-vm", "eastus", nil)
	azureVMStates.Put(vm.ID, "PowerState/running")

	rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-deallocate-vm", "deallocate"), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("deallocate: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	hooks.awaitHalt(t, vm.ID)
	if _, power := vmLROInstanceView(t, srv, "lro-deallocate-vm"); power.Code != "PowerState/deallocating" {
		t.Fatalf("power state while deallocating = %q", power.Code)
	}
	hooks.halt <- nil
	bg.Await()
	if _, power := vmLROInstanceView(t, srv, "lro-deallocate-vm"); power.Code != "PowerState/deallocated" {
		t.Fatalf("power state after deallocate = %q", power.Code)
	}
}

// Redeploy moves a running machine to a fresh guest: stopping, then starting,
// then running, with the operation InProgress throughout.
func TestVirtualMachineRedeployReportsEachStage(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-redeploy-vm", "eastus", nil)
	azureVMStates.Put(vm.ID, "PowerState/running")

	rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-redeploy-vm", "redeploy"), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("redeploy: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	opURL := rec.Header().Get("Azure-AsyncOperation")

	hooks.awaitHalt(t, vm.ID)
	if _, power := vmLROInstanceView(t, srv, "lro-redeploy-vm"); power.Code != "PowerState/stopping" {
		t.Fatalf("power state while the old guest stops = %q", power.Code)
	}
	hooks.halt <- nil
	hooks.awaitBoot(t, vm.ID)
	if _, power := vmLROInstanceView(t, srv, "lro-redeploy-vm"); power.Code != "PowerState/starting" {
		t.Fatalf("power state while the new guest boots = %q", power.Code)
	}
	if op := vmLROOperationStatus(t, srv, opURL); op.Status != "InProgress" {
		t.Fatalf("redeploy operation reads %q mid-boot, want InProgress", op.Status)
	}
	hooks.boot <- nil
	bg.Await()

	if op := vmLROOperationStatus(t, srv, opURL); op.Status != "Succeeded" {
		t.Fatalf("redeploy operation reads %q, want Succeeded", op.Status)
	}
	requireVMLROPowerState(t, vm.ID, "PowerState/running")
}

// Maintenance on a stopped machine restores it stopped, without a boot.
func TestVirtualMachineMaintenanceLeavesAStoppedMachineStopped(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-maint-stopped-vm", "eastus", nil)

	rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-maint-stopped-vm", "performMaintenance"), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("performMaintenance: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	hooks.awaitHalt(t, vm.ID)
	hooks.halt <- nil
	bg.Await()

	if op := vmLROOperationStatus(t, srv, rec.Header().Get("Azure-AsyncOperation")); op.Status != "Succeeded" {
		t.Fatalf("maintenance operation reads %q, want Succeeded", op.Status)
	}
	if _, power := vmLROInstanceView(t, srv, "lro-maint-stopped-vm"); power.Code != "PowerState/stopped" {
		t.Fatalf("power state after maintenance = %q, want PowerState/stopped", power.Code)
	}
}

// Maintenance whose boot fails fails the operation and leaves the machine
// stopped, since its old guest is already gone.
func TestVirtualMachineMaintenanceBootFailureFailsTheOperation(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-maint-fail-vm", "eastus", nil)
	azureVMStates.Put(vm.ID, "PowerState/running")

	rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-maint-fail-vm", "performMaintenance"), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("performMaintenance: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	hooks.awaitHalt(t, vm.ID)
	hooks.halt <- nil
	hooks.awaitBoot(t, vm.ID)
	hooks.boot <- errors.New("firecracker exited")
	bg.Await()

	op := vmLROOperationStatus(t, srv, rec.Header().Get("Azure-AsyncOperation"))
	if op.Status != "Failed" || op.Error == nil || op.Error.Code != "AllocationFailed" {
		t.Fatalf("maintenance operation after a failed boot = %+v", op)
	}
	provisioning, power := vmLROInstanceView(t, srv, "lro-maint-fail-vm")
	if provisioning.Code != "ProvisioningState/failed/AllocationFailed" || power.Code != "PowerState/stopped" {
		t.Fatalf("instance view after a failed maintenance = %q / %q", provisioning.Code, power.Code)
	}

	// A failed machine takes the next operation.
	retry := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-maint-fail-vm", "start"), "")
	if retry.Code != http.StatusAccepted {
		t.Fatalf("start after a failed operation: status %d, want 202: %s", retry.Code, retry.Body.String())
	}
	hooks.awaitBoot(t, vm.ID)
	hooks.boot <- nil
	bg.Await()
	if provisioning, _ := vmLROInstanceView(t, srv, "lro-maint-fail-vm"); provisioning.Code != "ProvisioningState/succeeded" {
		t.Fatalf("provisioning status after the retry = %q", provisioning.Code)
	}
}

// A machine whose operation was cut off by a restart fails rather than staying
// Updating, which would refuse every later operation on it.
func TestVirtualMachineOperationInterruptedByRestartFails(t *testing.T) {
	srv, _ := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-restart-vm", "eastus", func(vm *VirtualMachine) {
		vm.Properties.ProvisioningState = "Updating"
	})
	azureVMStates.Put(vm.ID, "PowerState/starting")

	registerVirtualMachineOperationRecovery(srv)
	t.Cleanup(func() { azureVMProvisioningErrors.Delete(vm.ID) })

	stored, _ := azureVMs.Get(vm.ID)
	if stored.Properties.ProvisioningState != "Failed" {
		t.Fatalf("provisioningState after restart = %q, want Failed", stored.Properties.ProvisioningState)
	}
	provisioning, power := vmLROInstanceView(t, srv, "lro-restart-vm")
	if provisioning.Code != "ProvisioningState/failed/OperationInterrupted" || power.Code != "PowerState/stopped" {
		t.Fatalf("instance view after restart = %q / %q", provisioning.Code, power.Code)
	}
}

// A create without a location is ARM's request-validation error, answered
// before any operation starts.
func TestVirtualMachineCreateRequiresALocation(t *testing.T) {
	srv, _ := vmLROSimulator(t)
	nicID := putVMLRONetworkInterface(t, "lro-noloc-nic")
	rec := vmLRORequest(t, srv, http.MethodPut, vmOpsPath("lro-noloc-vm", ""),
		`{"properties":{"networkProfile":{"networkInterfaces":[{"id":"`+nicID+`"}]}}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "LocationRequired") {
		t.Fatalf("create without a location: %d %s, want 400 LocationRequired", rec.Code, rec.Body.String())
	}
	if _, stored := azureVMs.Get(vmOpsID("lro-noloc-vm")); stored {
		t.Fatal("a rejected create left a machine behind")
	}
}

// requireComputeOperationURL holds an operation URL to the Compute resource
// provider's shape: .../providers/Microsoft.Compute/locations/{location}/operations/{id},
// with monitor=true on the Location.
func requireComputeOperationURL(t *testing.T, raw string, monitor bool) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse operation URL %q: %v", raw, err)
	}
	want := "/subscriptions/" + vmOpsSubscription + "/providers/Microsoft.Compute/locations/eastus/operations/"
	if !strings.HasPrefix(parsed.Path, want) || strings.Count(strings.TrimPrefix(parsed.Path, want), "/") != 0 {
		t.Fatalf("operation URL path %q, want %s{operationId}", parsed.Path, want)
	}
	if got := parsed.Query().Get("monitor") == "true"; got != monitor {
		t.Fatalf("operation URL %q: monitor=true present %v, want %v", raw, got, monitor)
	}
	if parsed.Query().Get("api-version") == "" {
		t.Fatalf("operation URL %q carries no api-version", raw)
	}
}

// Delete answers 202 at once with both poll URLs; the machine reads Deleting,
// stays readable and refuses other operations until its guest is gone, and is
// gone once the operation succeeds.
func TestVirtualMachineDeleteRunsBehindAnOperation(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-delete-vm", "eastus", nil)
	azureVMStates.Put(vm.ID, "PowerState/running")

	rec := vmLRORequest(t, srv, http.MethodDelete, vmOpsPath("lro-delete-vm", ""), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 202 delete carries no body, got %s", rec.Body.String())
	}
	opURL := rec.Header().Get("Azure-AsyncOperation")
	monitorURL := rec.Header().Get("Location")
	requireComputeOperationURL(t, opURL, false)
	requireComputeOperationURL(t, monitorURL, true)
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q on a delete still running, want 1", got)
	}

	hooks.awaitDestroy(t, vm.ID)
	if state := vmLROProvisioningState(t, srv, "lro-delete-vm"); state != "Deleting" {
		t.Fatalf("provisioningState while deleting = %q, want Deleting", state)
	}
	if provisioning, _ := vmLROInstanceView(t, srv, "lro-delete-vm"); provisioning.Code != "ProvisioningState/deleting" {
		t.Fatalf("provisioning status while deleting = %q, want ProvisioningState/deleting", provisioning.Code)
	}
	if op := vmLROOperationStatus(t, srv, opURL); op.Status != "InProgress" {
		t.Fatalf("delete operation reads %q while the guest is torn down, want InProgress", op.Status)
	}
	if start := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-delete-vm", "start"), ""); start.Code != http.StatusConflict {
		t.Fatalf("start during a delete: status %d, want 409: %s", start.Code, start.Body.String())
	}

	hooks.destroy <- nil
	bg.Await()

	if op := vmLROOperationStatus(t, srv, opURL); op.Status != "Succeeded" {
		t.Fatalf("delete operation reads %q, want Succeeded", op.Status)
	}
	if monitor := vmLRORequest(t, srv, http.MethodGet, monitorURL, ""); monitor.Code != http.StatusOK {
		t.Fatalf("monitor poll after the delete: status %d, want 200: %s", monitor.Code, monitor.Body.String())
	}
	if get := vmLRORequest(t, srv, http.MethodGet, vmOpsPath("lro-delete-vm", ""), ""); get.Code != http.StatusNotFound {
		t.Fatalf("get after the delete: status %d, want 404", get.Code)
	}
	if _, kept := azureVMStates.Get(vm.ID); kept {
		t.Fatal("the deleted machine's power state was left behind")
	}
}

// A delete whose guest cannot be torn down fails the operation and leaves the
// machine Failed, with the error in its instance view.
func TestVirtualMachineDeleteFailureFailsTheOperation(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-delete-fail-vm", "eastus", nil)

	rec := vmLRORequest(t, srv, http.MethodDelete, vmOpsPath("lro-delete-fail-vm", ""), "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete: status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	hooks.awaitDestroy(t, vm.ID)
	hooks.destroy <- errors.New("tap busy")
	bg.Await()

	op := vmLROOperationStatus(t, srv, rec.Header().Get("Azure-AsyncOperation"))
	if op.Status != "Failed" || op.Error == nil || op.Error.Code != "InternalExecutionError" {
		t.Fatalf("delete operation after a failed teardown = %+v", op)
	}
	if provisioning, _ := vmLROInstanceView(t, srv, "lro-delete-fail-vm"); provisioning.Code != "ProvisioningState/failed/InternalExecutionError" {
		t.Fatalf("provisioning status after a failed delete = %q", provisioning.Code)
	}
}

// SimulateEviction answers 204 at once; a Deallocate policy reads deallocating
// until the guest has stopped, then deallocated.
func TestVirtualMachineSimulateEvictionDeallocatesAfterAnswering(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-evict-vm", "eastus", func(vm *VirtualMachine) {
		vm.Properties.Priority = "Spot"
		vm.Properties.EvictionPolicy = "Deallocate"
	})
	azureVMStates.Put(vm.ID, "PowerState/running")

	rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-evict-vm", "simulateEviction"), "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("evict: status %d body %q, want 204 with no body", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Azure-AsyncOperation") != "" || rec.Header().Get("Location") != "" {
		t.Fatalf("the specification declares no long-running operation, got headers %v", rec.Header())
	}

	hooks.awaitHalt(t, vm.ID)
	provisioning, power := vmLROInstanceView(t, srv, "lro-evict-vm")
	if provisioning.Code != "ProvisioningState/updating" || power.Code != "PowerState/deallocating" {
		t.Fatalf("instance view while evicting = %q / %q", provisioning.Code, power.Code)
	}
	hooks.halt <- nil
	bg.Await()

	provisioning, power = vmLROInstanceView(t, srv, "lro-evict-vm")
	if provisioning.Code != "ProvisioningState/succeeded" || power.Code != "PowerState/deallocated" {
		t.Fatalf("instance view after the eviction = %q / %q", provisioning.Code, power.Code)
	}
}

// Under a Delete policy the evicted machine reads Deleting until its guest is
// gone, then is gone.
func TestVirtualMachineSimulateEvictionDeletesAfterAnswering(t *testing.T) {
	srv, hooks := vmLROSimulator(t)
	vm := putVMOpsMachine(t, "lro-evict-delete-vm", "eastus", func(vm *VirtualMachine) {
		vm.Properties.Priority = "Spot"
		vm.Properties.EvictionPolicy = "Delete"
	})

	rec := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-evict-delete-vm", "simulateEviction"), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("evict: status %d, want 204: %s", rec.Code, rec.Body.String())
	}
	hooks.awaitDestroy(t, vm.ID)
	if state := vmLROProvisioningState(t, srv, "lro-evict-delete-vm"); state != "Deleting" {
		t.Fatalf("provisioningState while the eviction deletes = %q, want Deleting", state)
	}
	if again := vmLRORequest(t, srv, http.MethodPost, vmOpsPath("lro-evict-delete-vm", "simulateEviction"), ""); again.Code != http.StatusConflict {
		t.Fatalf("a second eviction during the first: status %d, want 409", again.Code)
	}
	hooks.destroy <- nil
	bg.Await()

	if get := vmLRORequest(t, srv, http.MethodGet, vmOpsPath("lro-evict-delete-vm", ""), ""); get.Code != http.StatusNotFound {
		t.Fatalf("get after the eviction: status %d, want 404", get.Code)
	}
}
