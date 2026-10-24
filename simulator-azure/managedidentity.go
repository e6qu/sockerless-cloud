package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// simTenantID is the single Entra tenant the simulator presents. Real Azure
// resources (managed identities, service principals, role-assignment principals)
// all live in one tenant; the all-zero GUID is reserved/invalid, so the sim uses
// a stable non-zero tenant GUID everywhere it surfaces a tenantId.
const simTenantID = "11111111-1111-1111-1111-111111111111"

// azureManagedIdentities is the package-level handle to the managed-identity
// store so the authorization slice can resolve a role-assignment principalId
// that refers to a created managed identity.
var azureManagedIdentities sim.Store[UserAssignedIdentity]

// managedIdentityPrincipalExists reports whether principalId belongs to a
// managed identity created in this sim.
func managedIdentityPrincipalExists(principalID string) bool {
	if azureManagedIdentities == nil || principalID == "" {
		return false
	}
	// One row answers, from an index keyed by the store's generation: this
	// runs per data-plane request that authenticates as a managed identity.
	_, ok := azureManagedIdentitiesByPrincipal.Lookup(azureManagedIdentities, strings.ToLower(principalID),
		func(i UserAssignedIdentity) []string { return []string{strings.ToLower(i.Properties.PrincipalId)} })
	return ok
}

var azureManagedIdentitiesByPrincipal sim.GenerationIndex[UserAssignedIdentity]

type UserAssignedIdentity struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Type       string             `json:"type"`
	Location   string             `json:"location"`
	Tags       map[string]string  `json:"tags,omitempty"`
	Properties IdentityProperties `json:"properties"`
}

type IdentityProperties struct {
	TenantId          string `json:"tenantId"`
	PrincipalId       string `json:"principalId"`
	ClientId          string `json:"clientId"`
	ProvisioningState string `json:"provisioningState,omitempty"`
}

// FederatedIdentityCredential is a federated identity credential
// (Microsoft.ManagedIdentity/userAssignedIdentities/federatedIdentityCredentials)
// — the trust binding between a user-assigned identity and an external OIDC
// issuer (GitHub Actions, Kubernetes, etc.) that powers workload-identity
// federation.
type FederatedIdentityCredential struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Type       string                  `json:"type"`
	Properties FederatedCredentialSpec `json:"properties"`
}

// FederatedCredentialSpec carries the OIDC trust parameters.
type FederatedCredentialSpec struct {
	Issuer    string   `json:"issuer,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Audiences []string `json:"audiences,omitempty"`
}

// azureFederatedCredentials stores federated identity credentials keyed by
// full resource ID.
var azureFederatedCredentials sim.Store[FederatedIdentityCredential]

func registerManagedIdentity(srv *sim.Server) {
	identities := sim.MakeStore[UserAssignedIdentity](srv.DB(), "managed_identities")
	azureManagedIdentities = identities
	azureFederatedCredentials = sim.MakeStore[FederatedIdentityCredential](srv.DB(), "managed_identity_federated_credentials")

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ManagedIdentity"

	// PUT - Create or update managed identity
	srv.HandleFunc("PUT "+armBase+"/userAssignedIdentities/{identityName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		identityName := sim.PathParam(r, "identityName")

		var req struct {
			Location string            `json:"location"`
			Tags     map[string]string `json:"tags,omitempty"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/%s",
			sub, rg, identityName)

		identity := UserAssignedIdentity{
			ID:       resourceID,
			Name:     identityName,
			Type:     "Microsoft.ManagedIdentity/userAssignedIdentities",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: IdentityProperties{
				TenantId:    simTenantID,
				PrincipalId: sim.NewUUID(),
				ClientId:    sim.NewUUID(),
			},
		}

		// Preserve existing IDs on update; the createOrUpdate is atomic so a
		// concurrent PUT can't regenerate the principal/client IDs mid-flight.
		exists := identities.Update(resourceID, func(existing *UserAssignedIdentity) {
			identity.Properties.PrincipalId = existing.Properties.PrincipalId
			identity.Properties.ClientId = existing.Properties.ClientId
			*existing = identity
		})
		if !exists {
			identities.Put(resourceID, identity)
		}

		// A managed identity materializes a directory service principal whose
		// object ID equals the identity's principalId, so role assignments and
		// Graph servicePrincipal reads resolve it. Real Azure does this
		// automatically when the identity is created.
		entraRegisterServicePrincipal(identity.Properties.PrincipalId,
			identity.Properties.ClientId, identityName, "ManagedIdentity")

		// Real Azure returns 201 Created for a new identity and 200 OK for an
		// update of an existing one.
		status := http.StatusCreated
		if exists {
			status = http.StatusOK
		}
		sim.WriteJSON(w, status, identity)
	})

	// GET - Get managed identity
	srv.HandleFunc("GET "+armBase+"/userAssignedIdentities/{identityName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		identityName := sim.PathParam(r, "identityName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/%s",
			sub, rg, identityName)

		identity, ok := identities.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Identity '%s' not found.", identityName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, identity)
	})

	// PATCH - Update tags/location on an existing managed identity.
	srv.HandleFunc("PATCH "+armBase+"/userAssignedIdentities/{identityName}", handleManagedIdentityUpdate)

	// List managed identities by resource group and by subscription.
	srv.HandleFunc("GET "+armBase+"/userAssignedIdentities", handleManagedIdentityListByRG)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ManagedIdentity/userAssignedIdentities", handleManagedIdentityListBySub)

	// Federated identity credentials — OIDC trust bindings on a user-assigned
	// identity (workload-identity federation).
	srv.HandleFunc("PUT "+armBase+"/userAssignedIdentities/{identityName}/federatedIdentityCredentials/{ficName}", handleFederatedCredentialPut)
	srv.HandleFunc("GET "+armBase+"/userAssignedIdentities/{identityName}/federatedIdentityCredentials/{ficName}", handleFederatedCredentialGet)
	srv.HandleFunc("DELETE "+armBase+"/userAssignedIdentities/{identityName}/federatedIdentityCredentials/{ficName}", handleFederatedCredentialDelete)
	srv.HandleFunc("GET "+armBase+"/userAssignedIdentities/{identityName}/federatedIdentityCredentials", handleFederatedCredentialList)

	// Provider operations metadata, and SystemAssignedIdentities_GetByScope,
	// whose scope is the resource carrying the identity: an Azure Resource
	// Manager resource or a nested one (a deployment slot).
	srv.HandleFunc("GET /providers/Microsoft.ManagedIdentity/operations", handleManagedIdentityOperations)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.ManagedIdentity/identities/default", handleSystemAssignedIdentityGetByScope)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{resourceType}/{resourceName}/providers/Microsoft.ManagedIdentity/identities/default", handleSystemAssignedIdentityGetByScope)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{resourceType}/{resourceName}/{childType}/{childName}/providers/Microsoft.ManagedIdentity/identities/default", handleSystemAssignedIdentityGetByScope)

	srv.HandleFunc("GET /metadata/identity/oauth2/token", handleIMDSIdentityToken)

	// DELETE - Delete managed identity
	srv.HandleFunc("DELETE "+armBase+"/userAssignedIdentities/{identityName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		identityName := sim.PathParam(r, "identityName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/%s",
			sub, rg, identityName)

		if existing, ok := identities.Get(resourceID); ok {
			entraUnregisterServicePrincipal(existing.Properties.PrincipalId)
		}
		identities.Delete(resourceID)
		w.WriteHeader(http.StatusOK)
	})
}

func userAssignedIdentityID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/%s",
		sub, rg, name)
}

func handleManagedIdentityUpdate(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "identityName")
	resourceID := userAssignedIdentityID(sub, rg, name)

	var req struct {
		Location string            `json:"location,omitempty"`
		Tags     map[string]string `json:"tags,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	updated := azureManagedIdentities.Update(resourceID, func(existing *UserAssignedIdentity) {
		if req.Location != "" {
			existing.Location = req.Location
		}
		if req.Tags != nil {
			existing.Tags = req.Tags
		}
	})
	if !updated {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Identity '%s' not found.", name)
		return
	}
	identity, _ := azureManagedIdentities.Get(resourceID)
	sim.WriteJSON(w, http.StatusOK, identity)
}

func handleManagedIdentityListByRG(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/", sub, rg)
	out := azureManagedIdentities.Filter(func(i UserAssignedIdentity) bool {
		return strings.HasPrefix(i.ID, prefix)
	})
	if out == nil {
		out = []UserAssignedIdentity{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleManagedIdentityListBySub(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := fmt.Sprintf("/subscriptions/%s/", sub)
	out := azureManagedIdentities.Filter(func(i UserAssignedIdentity) bool {
		return strings.HasPrefix(i.ID, prefix)
	})
	if out == nil {
		out = []UserAssignedIdentity{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func federatedCredentialID(sub, rg, identity, fic string) string {
	return userAssignedIdentityID(sub, rg, identity) + "/federatedIdentityCredentials/" + fic
}

func handleFederatedCredentialPut(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	identityName := sim.PathParam(r, "identityName")
	ficName := sim.PathParam(r, "ficName")
	if _, ok := azureManagedIdentities.Get(userAssignedIdentityID(sub, rg, identityName)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Identity '%s' not found.", identityName)
		return
	}
	var req struct {
		Properties FederatedCredentialSpec `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	id := federatedCredentialID(sub, rg, identityName, ficName)
	_, existed := azureFederatedCredentials.Get(id)
	fic := FederatedIdentityCredential{
		ID:         id,
		Name:       ficName,
		Type:       "Microsoft.ManagedIdentity/userAssignedIdentities/federatedIdentityCredentials",
		Properties: req.Properties,
	}
	azureFederatedCredentials.Put(id, fic)
	status := http.StatusCreated
	if existed {
		status = http.StatusOK
	}
	sim.WriteJSON(w, status, fic)
}

func handleFederatedCredentialGet(w http.ResponseWriter, r *http.Request) {
	id := federatedCredentialID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "identityName"), sim.PathParam(r, "ficName"))
	fic, ok := azureFederatedCredentials.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"Federated identity credential '%s' not found.", sim.PathParam(r, "ficName"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, fic)
}

func handleFederatedCredentialDelete(w http.ResponseWriter, r *http.Request) {
	id := federatedCredentialID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "identityName"), sim.PathParam(r, "ficName"))
	if !azureFederatedCredentials.Delete(id) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleFederatedCredentialList(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	identityName := sim.PathParam(r, "identityName")
	if _, ok := azureManagedIdentities.Get(userAssignedIdentityID(sub, rg, identityName)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Identity '%s' not found.", identityName)
		return
	}
	prefix := userAssignedIdentityID(sub, rg, identityName) + "/federatedIdentityCredentials/"
	out := azureFederatedCredentials.Filter(func(f FederatedIdentityCredential) bool {
		return strings.HasPrefix(f.ID, prefix)
	})
	if out == nil {
		out = []FederatedIdentityCredential{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleManagedIdentityOperations(w http.ResponseWriter, _ *http.Request) {
	op := func(name, resource, operation, desc string) map[string]any {
		return map[string]any{
			"name": name,
			"display": map[string]any{
				"provider":    "Microsoft Managed Identity",
				"resource":    resource,
				"operation":   operation,
				"description": desc,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []map[string]any{
			op("Microsoft.ManagedIdentity/userAssignedIdentities/read", "User Assigned Identity", "Get User Assigned Identities", "Gets list of existing user assigned identities."),
			op("Microsoft.ManagedIdentity/userAssignedIdentities/write", "User Assigned Identity", "Create or Update User Assigned Identity", "Creates a new user assigned identity or updates an existing one."),
			op("Microsoft.ManagedIdentity/userAssignedIdentities/delete", "User Assigned Identity", "Delete User Assigned Identity", "Deletes an existing user assigned identity."),
			op("Microsoft.ManagedIdentity/register/action", "Microsoft Managed Identity Resource Provider", "Register Managed Identity Resource Provider", "Registers the Managed Identity Resource Provider."),
		},
	})
}

// imdsIdentityError writes the Instance Metadata Service's identity error
// body (IdentityErrorResponse: error and error_description).
func imdsIdentityError(w http.ResponseWriter, status int, code, description string) {
	sim.WriteJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// imdsInvalidRequest is the description IMDS documents for invalid_request.
const imdsInvalidRequest = "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed."

// handleIMDSIdentityToken is Identity_GetToken: the Instance Metadata Service
// mints a token for the requesting virtual machine's own managed identity —
// its system-assigned identity, or the user-assigned identity it carries that
// client_id, object_id or msi_res_id selects. A request from no machine, or
// for an identity the machine does not carry, is refused with IMDS's
// invalid_request "Identity not found".
func handleIMDSIdentityToken(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Metadata") != "true" {
		imdsIdentityError(w, http.StatusBadRequest, "bad_request_102", "Required metadata header not specified")
		return
	}
	q := r.URL.Query()
	resource := strings.TrimSpace(q.Get("resource"))
	clientID, objectID, msiResID := strings.TrimSpace(q.Get("client_id")), strings.TrimSpace(q.Get("object_id")), strings.TrimSpace(q.Get("msi_res_id"))
	selectors := 0
	for _, v := range []string{clientID, objectID, msiResID} {
		if v != "" {
			selectors++
		}
	}
	if resource == "" || selectors > 1 || len(q["resource"]) > 1 {
		imdsIdentityError(w, http.StatusBadRequest, "invalid_request", imdsInvalidRequest)
		return
	}
	vm, ok := azureIMDSMachine(r)
	if !ok {
		imdsIdentityError(w, http.StatusBadRequest, "invalid_request", "Identity not found")
		return
	}
	identity, ok := selectManagedIdentity(vm.VM.Identity, vm.VM.ID, clientID, objectID, msiResID)
	if !ok {
		imdsIdentityError(w, http.StatusBadRequest, "invalid_request", "Identity not found")
		return
	}
	now := time.Now()
	expires := now.Add(24 * time.Hour)
	token, err := mintAzureSimSignedJWT(map[string]any{
		"aud":       resource,
		"iss":       fmt.Sprintf("https://sts.windows.net/%s/", simTenantID),
		"oid":       identity.principalID,
		"sub":       identity.principalID,
		"tid":       simTenantID,
		"appid":     identity.clientID,
		"idtyp":     "app",
		"xms_mirid": identity.resourceID,
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       expires.Unix(),
		"ver":       "1.0",
	})
	if err != nil {
		imdsIdentityError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	lifetime := strconv.FormatInt(int64(expires.Sub(now)/time.Second), 10)
	out := map[string]any{
		"access_token":   token,
		"client_id":      identity.clientID,
		"expires_in":     lifetime,
		"expires_on":     strconv.FormatInt(expires.Unix(), 10),
		"ext_expires_in": lifetime,
		"not_before":     strconv.FormatInt(now.Unix(), 10),
		"resource":       resource,
		"token_type":     "Bearer",
	}
	if objectID != "" {
		out["object_id"] = identity.principalID
	}
	if msiResID != "" {
		out["msi_res_id"] = identity.resourceID
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// azureResourceIdentity is the managed identity of the resource resourceID —
// a virtual machine, an App Service app or slot, a container app or a
// Container Apps job — with its system-assigned identity's client ID and the
// resource's location.
func azureResourceIdentity(resourceID string) (identity *SiteIdentity, clientID, location string, ok bool) {
	if vm, found := azureVMs.Get(resourceID); found {
		return vm.Identity, vm.SystemIdentityClientID, vm.Location, true
	}
	if site, found := webSiteRecord(resourceID); found {
		return site.Identity, site.SystemIdentityClientID, site.Location, true
	}
	if app, found := acaApps.Get(resourceID); found {
		return app.Identity, app.SystemIdentityClientID, app.Location, true
	}
	if job, found := acaJobs.Get(resourceID); found {
		return job.Identity, job.SystemIdentityClientID, job.Location, true
	}
	return nil, "", "", false
}

// handleSystemAssignedIdentityGetByScope answers SystemAssignedIdentities_GetByScope
// with the system-assigned identity of the resource the scope names.
func handleSystemAssignedIdentityGetByScope(w http.ResponseWriter, r *http.Request) {
	const suffix = "/providers/Microsoft.ManagedIdentity/identities/default"
	scope := r.URL.Path[:len(r.URL.Path)-len(suffix)]
	identity, clientID, location, ok := azureResourceIdentity(scope)
	if !ok || !identity.systemAssigned() {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.ManagedIdentity/identities/default' under scope '%s' was not found: the scope carries no system-assigned identity.", scope)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":       scope + suffix,
		"name":     "default",
		"type":     "Microsoft.ManagedIdentity/identities",
		"location": location,
		"properties": map[string]any{
			"tenantId":    identity.TenantID,
			"principalId": identity.PrincipalID,
			"clientId":    clientID,
		},
	})
}
