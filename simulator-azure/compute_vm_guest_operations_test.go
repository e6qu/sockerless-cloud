package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The parts of the in-guest operations that can be asserted without a booted
// machine: how the guest's own output is read, which requests are refused
// before anything runs, and what is never returned. Running a command inside a
// guest needs nested KVM, so the paths that do are covered by the official-SDK
// suite on a capable host — but reading apt's plan, matching a package mask,
// and refusing an unknown handler are all decisions made here, and leaving them
// to CI alone would put them out of reach of anyone developing this.

// A real `apt-get --simulate upgrade` plan. The archive on each Inst line is
// what says whether an update is a security update, which is the only place
// that classification can honestly come from.
const aptUpgradePlan = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Inst libssl3 [3.0.2-0ubuntu1.10] (3.0.2-0ubuntu1.15 Ubuntu:22.04/jammy-security [amd64])
Inst tzdata [2023c-0ubuntu0.22.04.2] (2024a-0ubuntu0.22.04 Ubuntu:22.04/jammy-updates [all])
Conf libssl3 (3.0.2-0ubuntu1.15 Ubuntu:22.04/jammy-security [amd64])
Conf tzdata (2024a-0ubuntu0.22.04 Ubuntu:22.04/jammy-updates [all])
`

func TestParseAptUpgradePlan(t *testing.T) {
	patches := azureParseAptUpgradePlan(aptUpgradePlan, time.Now().UTC())
	if len(patches) != 2 {
		t.Fatalf("read %d upgradable packages, want 2 — Conf lines are the same packages being configured, not more updates", len(patches))
	}

	first := patches[0]
	if first["name"] != "libssl3" {
		t.Errorf("first package = %v, want libssl3", first["name"])
	}
	if first["version"] != "3.0.2-0ubuntu1.15" {
		t.Errorf("first version = %v, want the version it would move TO", first["version"])
	}
	if got, _ := first["classifications"].([]string); len(got) != 1 || got[0] != "Security" {
		t.Errorf("libssl3 came from jammy-security and is classified %v, want Security", got)
	}

	second := patches[1]
	if got, _ := second["classifications"].([]string); len(got) != 1 || got[0] != "Other" {
		t.Errorf("tzdata came from jammy-updates and is classified %v, want Other", got)
	}
}

// A guest with nothing to upgrade reports nothing, which is a truthful
// assessment rather than an empty one to be filled in.
func TestParseAptUpgradePlanWithNothingToUpgrade(t *testing.T) {
	plan := "NOTE: This is only a simulation!\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	if patches := azureParseAptUpgradePlan(plan, time.Now().UTC()); len(patches) != 0 {
		t.Fatalf("read %d packages from a plan that upgrades nothing", len(patches))
	}
}

func TestPackageNameMaskMatching(t *testing.T) {
	for _, tc := range []struct {
		name, mask string
		want       bool
	}{
		{"libssl3", "libssl3", true},
		{"libssl3", "libssl*", true},
		{"libssl3", "*ssl3", true},
		{"libssl3", "lib*3", true},
		{"libssl3", "*ssl*", true},
		{"tzdata", "libssl*", false},
		{"libssl3", "", false},
		{"linux-image-generic", "linux-*-generic", true},
	} {
		t.Run(tc.name+"/"+tc.mask, func(t *testing.T) {
			if got := azureNameMatchesMask(tc.name, tc.mask); got != tc.want {
				t.Errorf("azureNameMatchesMask(%q, %q) = %v, want %v", tc.name, tc.mask, got, tc.want)
			}
		})
	}
}

// The command comes from the handler's own settings, and the protected copy
// wins because that is where a caller puts a command carrying a secret.
func TestCustomScriptCommandSource(t *testing.T) {
	public := VirtualMachineExtensionProperties{
		Settings: map[string]any{"commandToExecute": "echo public"},
	}
	if got := azureCustomScriptCommand(public); got != "echo public" {
		t.Errorf("command = %q, want the public setting", got)
	}
	both := VirtualMachineExtensionProperties{
		Settings:          map[string]any{"commandToExecute": "echo public"},
		ProtectedSettings: map[string]any{"commandToExecute": "echo protected"},
	}
	if got := azureCustomScriptCommand(both); got != "echo protected" {
		t.Errorf("command = %q, want the protected setting to win", got)
	}
	if got := azureCustomScriptCommand(VirtualMachineExtensionProperties{}); got != "" {
		t.Errorf("command = %q, want empty when the handler was given none", got)
	}
}

func vmGuestOpsRequest(t *testing.T, srv interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}
	req := httptest.NewRequest(method, path+"?api-version=2022-03-01", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// An extension whose handler this simulator does not run is refused. Reporting
// it as provisioned would be indistinguishable from one that ran, which is the
// whole failure mode these operations exist to avoid.
func TestVirtualMachineExtensionRefusesAnUnknownHandler(t *testing.T) {
	srv := vmOpsSimulator(t)
	putVMOpsMachine(t, "ext-vm", "eastus", nil)

	rec := vmGuestOpsRequest(t, srv, http.MethodPut,
		vmOpsID("ext-vm")+"/extensions/monitoring",
		`{"properties":{"publisher":"Microsoft.Azure.Monitor","type":"AzureMonitorLinuxAgent",
		  "typeHandlerVersion":"1.0","settings":{}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a handler the simulator does not run: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Custom Script") {
		t.Errorf("the refusal should say which handlers are run: %s", rec.Body.String())
	}
	if _, stored := azureVMExtensions.Get(vmOpsID("ext-vm") + "/extensions/monitoring"); stored {
		t.Error("an extension that was refused was still recorded")
	}
}

// A Custom Script extension on a machine with no running guest is refused,
// because there is nowhere for its command to run.
func TestVirtualMachineExtensionRefusesAMachineWithNoGuest(t *testing.T) {
	srv := vmOpsSimulator(t)
	putVMOpsMachine(t, "ext-noguest-vm", "eastus", nil)

	rec := vmGuestOpsRequest(t, srv, http.MethodPut,
		vmOpsID("ext-noguest-vm")+"/extensions/script",
		`{"properties":{"publisher":"Microsoft.Azure.Extensions","type":"CustomScript",
		  "typeHandlerVersion":"2.1","settings":{"commandToExecute":"echo hello"}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want a refusal when the machine is not running: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not running") {
		t.Errorf("the refusal should say the machine is not running: %s", rec.Body.String())
	}
}

// A Custom Script extension carrying no command is refused: there is nothing
// for the handler to do, and recording it as succeeded would claim otherwise.
func TestVirtualMachineExtensionRefusesAnEmptyCommand(t *testing.T) {
	srv := vmOpsSimulator(t)
	putVMOpsMachine(t, "ext-empty-vm", "eastus", nil)

	rec := vmGuestOpsRequest(t, srv, http.MethodPut,
		vmOpsID("ext-empty-vm")+"/extensions/script",
		`{"properties":{"publisher":"Microsoft.Azure.Extensions","type":"CustomScript",
		  "typeHandlerVersion":"2.1","settings":{}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want a refusal for an extension with no command: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "commandToExecute") {
		t.Errorf("the refusal should name the missing member: %s", rec.Body.String())
	}
}

// Protected settings are write-only by contract: handing them back would return
// a secret the caller entrusted to the machine.
func TestVirtualMachineExtensionNeverReturnsProtectedSettings(t *testing.T) {
	stored := VirtualMachineExtension{
		ID:   vmOpsID("ext-secret-vm") + "/extensions/script",
		Name: "script",
		Properties: VirtualMachineExtensionProperties{
			Publisher:         "Microsoft.Azure.Extensions",
			Type:              "CustomScript",
			Settings:          map[string]any{"timestamp": 1},
			ProtectedSettings: map[string]any{"commandToExecute": "echo $DEPLOY_TOKEN"},
		},
	}
	response := azureVMExtensionResponse(stored, httptest.NewRequest(http.MethodPut, "/", nil))
	if response.Properties.ProtectedSettings != nil {
		t.Fatal("protected settings were returned to the caller")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "DEPLOY_TOKEN") {
		t.Fatalf("a protected value reached the response body: %s", encoded)
	}
	// The public settings still come back — only the protected half is withheld.
	if response.Properties.Settings == nil {
		t.Error("public settings were withheld along with the protected ones")
	}
}

// Capturing an image from a machine that was never generalized is refused: the
// image would carry that machine's host name, keys and logs into every machine
// created from it.
func TestVirtualMachineCaptureRequiresGeneralization(t *testing.T) {
	srv := vmOpsSimulator(t)
	putVMOpsMachine(t, "capture-vm", "eastus", nil)

	rec := vmGuestOpsRequest(t, srv, http.MethodPost, vmOpsPath("capture-vm", "capture"),
		`{"vhdPrefix":"captured","destinationContainerName":"images","overwriteVhds":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a machine that was not generalized: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "generalized") {
		t.Errorf("the refusal should say the machine was not generalized: %s", rec.Body.String())
	}
}

// The capture parameters name where the image goes, so a request missing them
// is refused before anything is copied.
func TestVirtualMachineCaptureRequiresItsDestination(t *testing.T) {
	srv := vmOpsSimulator(t)
	vm := putVMOpsMachine(t, "capture-dest-vm", "eastus", nil)
	azureVMGeneralized.Put(vm.ID, true)

	rec := vmGuestOpsRequest(t, srv, http.MethodPost, vmOpsPath("capture-dest-vm", "capture"),
		`{"overwriteVhds":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when the destination is unnamed: %s", rec.Code, rec.Body.String())
	}
}

// installPatches must be told what to do about a restart, because a patch that
// needs one leaves the machine in a state the caller has to have chosen.
func TestVirtualMachineInstallPatchesRequiresARebootSetting(t *testing.T) {
	srv := vmOpsSimulator(t)
	putVMOpsMachine(t, "patch-vm", "eastus", nil)

	rec := vmGuestOpsRequest(t, srv, http.MethodPost, vmOpsPath("patch-vm", "installPatches"), `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when rebootSetting is absent: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rebootSetting") {
		t.Errorf("the refusal should name the missing member: %s", rec.Body.String())
	}
}
