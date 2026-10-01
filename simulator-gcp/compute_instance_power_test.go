package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// These drive the instance verbs that move a machine in-process. The machine
// moves are held open on channels through the gcpBootVM, gcpHaltVM and
// gcpDestroyVM hooks, which is what lets a test read the instance and its
// operation while the move is under way, on a host without nested KVM.

const (
	powerProject = "power-project"
	powerZone    = "us-central1-a"
)

type machineHooks struct {
	booting    chan string
	boot       chan error
	halting    chan string
	halt       chan error
	destroying chan string
	destroy    chan error
}

func installMachineHooks(t *testing.T) *machineHooks {
	t.Helper()
	hooks := &machineHooks{
		booting: make(chan string, 1), boot: make(chan error),
		halting: make(chan string, 1), halt: make(chan error),
		destroying: make(chan string, 1), destroy: make(chan error),
	}
	boot, halt, destroy := gcpBootVM, gcpHaltVM, gcpDestroyVM
	gcpBootVM = func(_ context.Context, inst *ComputeInstance) error {
		hooks.booting <- inst.SelfLink
		err := <-hooks.boot
		if err == nil {
			inst.NetworkInterfaces[0].NetworkIP = "10.128.0.2"
		}
		return err
	}
	gcpHaltVM = func(_ context.Context, inst *ComputeInstance) error {
		hooks.halting <- inst.SelfLink
		return <-hooks.halt
	}
	gcpDestroyVM = func(_ context.Context, inst *ComputeInstance) error {
		hooks.destroying <- inst.SelfLink
		return <-hooks.destroy
	}
	// Registered before powerSimulator's drain, so it runs after the drain.
	t.Cleanup(func() { gcpBootVM, gcpHaltVM, gcpDestroyVM = boot, halt, destroy })
	return hooks
}

func awaitMove(t *testing.T, moving chan string, link, what string) {
	t.Helper()
	select {
	case got := <-moving:
		if got != link {
			t.Fatalf("%s %q, want %q", what, got, link)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the operation never began to %s %q", what, link)
	}
}

func powerSimulator(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	t.Cleanup(bg.Await)
	return srv
}

// putPowerInstance stores an instance without booting a machine, which is what
// lets the verbs be exercised on a host that cannot boot one.
func putPowerInstance(t *testing.T, name string, status ComputeInstanceStatus) string {
	t.Helper()
	link := computeInstanceSelfLink(powerProject, powerZone, name)
	gcpInstances.Put(link, ComputeInstance{
		Kind:     "compute#instance",
		Name:     name,
		SelfLink: link,
		Status:   status,
		NetworkInterfaces: []ComputeNetworkInterface{{
			Name:       "nic0",
			Network:    "projects/" + powerProject + "/global/networks/default",
			Subnetwork: "projects/" + powerProject + "/regions/us-central1/subnetworks/absent",
		}},
	})
	t.Cleanup(func() { gcpInstances.Delete(link) })
	return link
}

func powerRequest(t *testing.T, srv *sim.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://compute.googleapis.com"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func powerInstancePath(name, verb string) string {
	path := "/compute/v1/projects/" + powerProject + "/zones/" + powerZone + "/instances/" + name
	if verb != "" {
		path += "/" + verb
	}
	return path
}

type powerOperation struct {
	Name                string `json:"name"`
	Status              string `json:"status"`
	OperationType       string `json:"operationType"`
	TargetLink          string `json:"targetLink"`
	EndTime             string `json:"endTime"`
	HTTPErrorStatusCode int    `json:"httpErrorStatusCode"`
	Error               *struct {
		Errors []struct{ Code, Message string } `json:"errors"`
	} `json:"error"`
}

func decodePowerOperation(t *testing.T, rec *httptest.ResponseRecorder) powerOperation {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var op powerOperation
	if err := json.Unmarshal(rec.Body.Bytes(), &op); err != nil {
		t.Fatalf("decode operation: %v: %s", err, rec.Body.String())
	}
	return op
}

func pollPowerOperation(t *testing.T, srv *sim.Server, name string) powerOperation {
	t.Helper()
	return decodePowerOperation(t, powerRequest(t, srv, http.MethodGet,
		"/compute/v1/projects/"+powerProject+"/zones/"+powerZone+"/operations/"+name, ""))
}

// startPowerVerb sends the verb and holds it to the answer Compute Engine
// gives: a RUNNING zone operation naming the instance, with no end time.
func startPowerVerb(t *testing.T, srv *sim.Server, method, name, verb, body, opType string) powerOperation {
	t.Helper()
	op := decodePowerOperation(t, powerRequest(t, srv, method, powerInstancePath(name, verb), body))
	if op.Status != "RUNNING" || op.EndTime != "" {
		t.Fatalf("%s answered an operation %s with end time %q, want RUNNING with none", opType, op.Status, op.EndTime)
	}
	if op.OperationType != opType {
		t.Fatalf("operationType = %q, want %q", op.OperationType, opType)
	}
	if !strings.HasSuffix(op.TargetLink, "/instances/"+name) {
		t.Fatalf("targetLink = %q, want the instance", op.TargetLink)
	}
	return op
}

func requirePowerStatus(t *testing.T, link string, want ComputeInstanceStatus) ComputeInstance {
	t.Helper()
	inst, ok := gcpInstances.Get(link)
	if !ok {
		t.Fatalf("instance %s is gone, want it %s", link, want)
	}
	if inst.Status != want {
		t.Fatalf("instance status = %s, want %s", inst.Status, want)
	}
	return inst
}

func requirePowerOperationDone(t *testing.T, srv *sim.Server, name string) {
	t.Helper()
	op := pollPowerOperation(t, srv, name)
	if op.Status != "DONE" || op.EndTime == "" || op.Error != nil {
		t.Fatalf("operation after the move = %+v, want DONE with an end time and no error", op)
	}
}

// Start answers before the boot: the instance reads STAGING and the operation
// RUNNING until the machine is up, then RUNNING and DONE.
func TestComputeInstanceStartBootsBehindTheOperation(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "starting", ComputeInstanceTerminated)

	op := startPowerVerb(t, srv, http.MethodPost, "starting", "start", "", "start")
	awaitMove(t, hooks.booting, link, "boot")
	requirePowerStatus(t, link, ComputeInstanceStaging)
	if got := pollPowerOperation(t, srv, op.Name); got.Status != "RUNNING" {
		t.Fatalf("operation reads %s while the machine boots, want RUNNING", got.Status)
	}

	hooks.boot <- nil
	bg.Await()

	requirePowerOperationDone(t, srv, op.Name)
	inst := requirePowerStatus(t, link, ComputeInstanceRunning)
	if inst.NetworkInterfaces[0].NetworkIP != "10.128.0.2" {
		t.Fatalf("networkIP = %q, want the address the boot leased", inst.NetworkInterfaces[0].NetworkIP)
	}
}

// A boot that fails finishes the operation with the error and leaves the
// instance where it was.
func TestComputeInstanceStartFailureFinishesTheOperationWithTheError(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "unbootable", ComputeInstanceTerminated)

	op := startPowerVerb(t, srv, http.MethodPost, "unbootable", "start", "", "start")
	awaitMove(t, hooks.booting, link, "boot")
	hooks.boot <- errors.New("kvm:/dev/kvm missing")
	bg.Await()

	got := pollPowerOperation(t, srv, op.Name)
	if got.Status != "DONE" || got.Error == nil || len(got.Error.Errors) != 1 ||
		got.Error.Errors[0].Code != "INTERNAL_ERROR" || got.HTTPErrorStatusCode != http.StatusServiceUnavailable {
		t.Fatalf("operation after a failed boot = %+v, want DONE with INTERNAL_ERROR", got)
	}
	if !strings.Contains(got.Error.Errors[0].Message, "kvm:/dev/kvm missing") {
		t.Fatalf("the operation's error hides why the boot failed: %q", got.Error.Errors[0].Message)
	}
	requirePowerStatus(t, link, ComputeInstanceTerminated)
}

// Stop answers before the machine halts: STOPPING, then TERMINATED.
func TestComputeInstanceStopHaltsBehindTheOperation(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "stopping", ComputeInstanceRunning)

	op := startPowerVerb(t, srv, http.MethodPost, "stopping", "stop", "", "stop")
	awaitMove(t, hooks.halting, link, "halt")
	requirePowerStatus(t, link, ComputeInstanceStopping)

	hooks.halt <- nil
	bg.Await()

	requirePowerOperationDone(t, srv, op.Name)
	requirePowerStatus(t, link, ComputeInstanceTerminated)
}

// Delete answers before the machine is torn down; the instance stays readable,
// STOPPING, until it is, and is gone once the operation is DONE. A teardown
// that fails keeps the instance and reports the error on the operation.
func TestComputeInstanceDeleteTearsDownBehindTheOperation(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "deleting", ComputeInstanceRunning)

	op := startPowerVerb(t, srv, http.MethodDelete, "deleting", "", "", "delete")
	awaitMove(t, hooks.destroying, link, "destroy")
	requirePowerStatus(t, link, ComputeInstanceStopping)
	if rec := powerRequest(t, srv, http.MethodGet, powerInstancePath("deleting", ""), ""); rec.Code != http.StatusOK {
		t.Fatalf("get while deleting: status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	hooks.destroy <- nil
	bg.Await()

	requirePowerOperationDone(t, srv, op.Name)
	if rec := powerRequest(t, srv, http.MethodGet, powerInstancePath("deleting", ""), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after the delete: status %d, want 404", rec.Code)
	}

	kept := putPowerInstance(t, "undeletable", ComputeInstanceRunning)
	op = startPowerVerb(t, srv, http.MethodDelete, "undeletable", "", "", "delete")
	awaitMove(t, hooks.destroying, kept, "destroy")
	hooks.destroy <- errors.New("tap busy")
	bg.Await()
	if got := pollPowerOperation(t, srv, op.Name); got.Error == nil {
		t.Fatalf("a delete whose teardown failed reports no error: %+v", got)
	}
	requirePowerStatus(t, kept, ComputeInstanceRunning)
}

// Suspend halts a running machine behind SUSPENDING and resume boots it back
// behind STAGING; each refuses an instance in the wrong status at once.
func TestComputeInstanceSuspendAndResumeMoveBehindTheOperation(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "napping", ComputeInstanceRunning)

	if rec := powerRequest(t, srv, http.MethodPost, powerInstancePath("napping", "resume"), ""); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "only a suspended instance can be resumed") {
		t.Fatalf("resuming a running instance: %d %s, want 400", rec.Code, rec.Body.String())
	}

	op := startPowerVerb(t, srv, http.MethodPost, "napping", "suspend", "", "suspend")
	awaitMove(t, hooks.halting, link, "halt")
	requirePowerStatus(t, link, ComputeInstanceSuspending)
	hooks.halt <- nil
	bg.Await()
	requirePowerOperationDone(t, srv, op.Name)
	requirePowerStatus(t, link, ComputeInstanceSuspended)

	if rec := powerRequest(t, srv, http.MethodPost, powerInstancePath("napping", "suspend"), ""); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "only a running instance can be suspended") {
		t.Fatalf("suspending a suspended instance: %d %s, want 400", rec.Code, rec.Body.String())
	}

	op = startPowerVerb(t, srv, http.MethodPost, "napping", "resume", "", "resume")
	awaitMove(t, hooks.booting, link, "boot")
	requirePowerStatus(t, link, ComputeInstanceStaging)
	hooks.boot <- nil
	bg.Await()
	requirePowerOperationDone(t, srv, op.Name)
	requirePowerStatus(t, link, ComputeInstanceRunning)
}

// startWithEncryptionKey refuses a request with no disk keys before anything
// moves, and otherwise boots the same way start does.
func TestComputeInstanceStartWithEncryptionKeyBootsBehindTheOperation(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "sealed", ComputeInstanceTerminated)

	if rec := powerRequest(t, srv, http.MethodPost, powerInstancePath("sealed", "startWithEncryptionKey"), `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("start without keys: status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	requirePowerStatus(t, link, ComputeInstanceTerminated)

	op := startPowerVerb(t, srv, http.MethodPost, "sealed", "startWithEncryptionKey",
		`{"disks":[{"source":"projects/`+powerProject+`/zones/`+powerZone+`/disks/boot","diskEncryptionKey":{"rawKey":"a2V5"}}]}`,
		"startWithEncryptionKey")
	awaitMove(t, hooks.booting, link, "boot")
	requirePowerStatus(t, link, ComputeInstanceStaging)
	hooks.boot <- nil
	bg.Await()
	requirePowerOperationDone(t, srv, op.Name)
	requirePowerStatus(t, link, ComputeInstanceRunning)
}

// An instance deleted while its machine boots leaves no machine behind: the
// boot that outlived it tears down what it brought up.
func TestComputeInstanceBootOutlivedByItsInstanceTearsDownTheMachine(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "orphan", ComputeInstanceTerminated)

	startPowerVerb(t, srv, http.MethodPost, "orphan", "start", "", "start")
	awaitMove(t, hooks.booting, link, "boot")
	gcpInstances.Delete(link)
	hooks.boot <- nil
	awaitMove(t, hooks.destroying, link, "destroy")
	hooks.destroy <- nil
	bg.Await()
}

// Without the hooks the verb drives the real machine path, which cannot boot
// an instance whose subnetwork was never created on any host: the request
// still answers at once, and the operation carries the verdict.
func TestComputeInstanceStartOnTheRealPathReportsTheBootFailureOnTheOperation(t *testing.T) {
	srv := powerSimulator(t)
	link := putPowerInstance(t, "real-path", ComputeInstanceTerminated)

	op := startPowerVerb(t, srv, http.MethodPost, "real-path", "start", "", "start")
	bg.Await()

	got := pollPowerOperation(t, srv, op.Name)
	if got.Status != "DONE" || got.Error == nil || got.Error.Errors[0].Code != "INTERNAL_ERROR" {
		t.Fatalf("operation after a boot the host could not make = %+v, want DONE with INTERNAL_ERROR", got)
	}
	requirePowerStatus(t, link, ComputeInstanceTerminated)
}

// Reset halts the machine and boots it again behind a RUNNING operation; the
// instance reads RUNNING throughout and the reboot leases its address again.
func TestComputeInstanceResetRebootsBehindTheOperation(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "rebooting", ComputeInstanceRunning)

	op := startPowerVerb(t, srv, http.MethodPost, "rebooting", "reset", "", "reset")
	awaitMove(t, hooks.halting, link, "halt")
	requirePowerStatus(t, link, ComputeInstanceRunning)
	if got := pollPowerOperation(t, srv, op.Name); got.Status != "RUNNING" {
		t.Fatalf("operation reads %s while the machine halts, want RUNNING", got.Status)
	}
	hooks.halt <- nil
	awaitMove(t, hooks.booting, link, "boot")
	if got := pollPowerOperation(t, srv, op.Name); got.Status != "RUNNING" {
		t.Fatalf("operation reads %s while the machine boots again, want RUNNING", got.Status)
	}
	hooks.boot <- nil
	bg.Await()

	requirePowerOperationDone(t, srv, op.Name)
	inst := requirePowerStatus(t, link, ComputeInstanceRunning)
	if inst.NetworkInterfaces[0].NetworkIP != "10.128.0.2" {
		t.Fatalf("networkIP = %q, want the address the reboot leased", inst.NetworkInterfaces[0].NetworkIP)
	}
}

// A reset whose reboot fails leaves the halted machine's instance TERMINATED
// and reports the failure on the operation; a reset of an instance that is not
// running is refused before anything moves.
func TestComputeInstanceResetFailureLeavesTheInstanceTerminated(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "unrebootable", ComputeInstanceRunning)

	op := startPowerVerb(t, srv, http.MethodPost, "unrebootable", "reset", "", "reset")
	awaitMove(t, hooks.halting, link, "halt")
	hooks.halt <- nil
	awaitMove(t, hooks.booting, link, "boot")
	hooks.boot <- errors.New("kvm:/dev/kvm missing")
	bg.Await()

	got := pollPowerOperation(t, srv, op.Name)
	if got.Status != "DONE" || got.Error == nil || got.Error.Errors[0].Code != "INTERNAL_ERROR" {
		t.Fatalf("operation after a failed reboot = %+v, want DONE with INTERNAL_ERROR", got)
	}
	requirePowerStatus(t, link, ComputeInstanceTerminated)

	if rec := powerRequest(t, srv, http.MethodPost, powerInstancePath("unrebootable", "reset"), ""); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "only a running instance can be reset") {
		t.Fatalf("resetting a terminated instance: %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// An instance with deletionProtection refuses the delete with the reason
// clients branch on, and nothing moves; clearing the flag lets it go.
func TestComputeInstanceDeleteRefusesAProtectedInstance(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "protected", ComputeInstanceRunning)
	gcpInstances.Update(link, func(inst *ComputeInstance) { inst.DeletionProtection = true })

	rec := powerRequest(t, srv, http.MethodDelete, powerInstancePath("protected", ""), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete of a protected instance: status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var refusal struct {
		Error struct {
			Code   int `json:"code"`
			Errors []struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if len(refusal.Error.Errors) != 1 || refusal.Error.Errors[0].Reason != "resourceInUseByAnotherResource" ||
		!strings.Contains(refusal.Error.Errors[0].Message, "protected against deletion") {
		t.Fatalf("refusal = %s, want reason resourceInUseByAnotherResource", rec.Body.String())
	}
	requirePowerStatus(t, link, ComputeInstanceRunning)

	if rec := powerRequest(t, srv, http.MethodPost, powerInstancePath("protected", "setDeletionProtection")+"?deletionProtection=false", ""); rec.Code != http.StatusOK {
		t.Fatalf("setDeletionProtection=false: status %d: %s", rec.Code, rec.Body.String())
	}
	op := startPowerVerb(t, srv, http.MethodDelete, "protected", "", "", "delete")
	awaitMove(t, hooks.destroying, link, "destroy")
	hooks.destroy <- nil
	bg.Await()
	requirePowerOperationDone(t, srv, op.Name)
}

// zoneOperations.wait blocks on the operation itself: a wait issued while the
// machine boots answers once the boot ends, with the operation DONE.
func TestComputeOperationWaitBlocksUntilTheOperationFinishes(t *testing.T) {
	srv := powerSimulator(t)
	hooks := installMachineHooks(t)
	link := putPowerInstance(t, "awaited", ComputeInstanceTerminated)

	op := startPowerVerb(t, srv, http.MethodPost, "awaited", "start", "", "start")
	awaitMove(t, hooks.booting, link, "boot")

	waitPath := "/compute/v1/projects/" + powerProject + "/zones/" + powerZone + "/operations/" + op.Name + "/wait"
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- powerRequest(t, srv, http.MethodPost, waitPath, "") }()
	hooks.boot <- nil

	got := decodePowerOperation(t, <-answered)
	if got.Status != "DONE" || got.Error != nil {
		t.Fatalf("wait answered %+v, want the operation DONE", got)
	}
	bg.Await()

	if again := decodePowerOperation(t, powerRequest(t, srv, http.MethodPost, waitPath, "")); again.Status != "DONE" {
		t.Fatalf("wait on a finished operation answered %s, want DONE", again.Status)
	}
	missing := "/compute/v1/projects/" + powerProject + "/zones/" + powerZone + "/operations/operation-absent/wait"
	if rec := powerRequest(t, srv, http.MethodPost, missing, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("wait on an unknown operation: status %d, want 404", rec.Code)
	}
}

func TestComputeInstancePowerVerbsRefuseAnUnknownInstance(t *testing.T) {
	srv := powerSimulator(t)
	for _, tc := range []struct{ method, verb string }{
		{http.MethodPost, "start"}, {http.MethodPost, "stop"}, {http.MethodDelete, ""},
		{http.MethodPost, "suspend"}, {http.MethodPost, "resume"}, {http.MethodPost, "reset"},
	} {
		if rec := powerRequest(t, srv, tc.method, powerInstancePath("nobody", tc.verb), ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s on an unknown instance: status %d, want 404", tc.method, tc.verb, rec.Code)
		}
	}
}
