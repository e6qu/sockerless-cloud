package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A PATCH of a machine's identity reads the dictionary the way Azure Resource
// Manager does: a key with a value attaches that user-assigned identity, a
// key with null detaches it, and a null dictionary with a type that names no
// user-assigned identity leaves none — the two bodies `az vm identity remove`
// sends.
func TestVirtualMachineIdentityPatchDetaches(t *testing.T) {
	srv := vmOpsSimulator(t)
	uaiID := userAssignedIdentityID(vmOpsSubscription, vmOpsResourceGroup, "patch-uai")
	azureManagedIdentities.Put(uaiID, UserAssignedIdentity{
		ID: uaiID, Name: "patch-uai", Location: "eastus",
		Properties: IdentityProperties{TenantId: simTenantID, PrincipalId: sim.NewUUID(), ClientId: sim.NewUUID()},
	})
	t.Cleanup(func() { azureManagedIdentities.Delete(uaiID) })
	systemPrincipal := sim.NewUUID()
	vm := putVMOpsMachine(t, "identity-patch", "eastus", func(vm *VirtualMachine) {
		vm.Identity = &SiteIdentity{
			Type: "SystemAssigned, UserAssigned", TenantID: simTenantID, PrincipalID: systemPrincipal,
			UserAssignedIdentities: map[string]*SiteUserAssignedIdentity{uaiID: {PrincipalID: "p", ClientID: "c"}},
		}
		vm.SystemIdentityClientID = sim.NewUUID()
	})
	patch := func(body string) VirtualMachine {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, vm.ID+"?api-version=2024-07-01", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		now := time.Now()
		token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("mint ARM bearer: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d: %s", body, rec.Code, rec.Body.String())
		}
		var out VirtualMachine
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	got := patch(`{"identity":{"type":"SystemAssigned","userAssignedIdentities":{"` + uaiID + `":null}}}`)
	if got.Identity == nil || len(got.Identity.UserAssignedIdentities) != 0 {
		t.Fatalf("a null entry detaches the identity; got %+v", got.Identity)
	}
	patch(`{"identity":{"type":"SystemAssigned, UserAssigned","userAssignedIdentities":{"` + uaiID + `":{}}}}`)
	got = patch(`{"identity":{"type":"SystemAssigned","userAssignedIdentities":null}}`)
	if got.Identity == nil || got.Identity.Type != "SystemAssigned" || len(got.Identity.UserAssignedIdentities) != 0 {
		t.Fatalf("a type without UserAssigned leaves no user-assigned identity; got %+v", got.Identity)
	}
	if got.Identity.PrincipalID != systemPrincipal {
		t.Fatalf("the system-assigned principal survives the PATCH: %q", got.Identity.PrincipalID)
	}
	got = patch(`{"identity":{"type":"None","userAssignedIdentities":null}}`)
	if got.Identity != nil {
		t.Fatalf("type None removes the identity; got %+v", got.Identity)
	}
}
